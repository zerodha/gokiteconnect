package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/devshoe/gokiteconnect/models"
)

// Lists live in their own SQLite database, which several processes can
// write: the instrument catalog is read-only between refreshes. Items store
// only the instrument ID and when it was added; list order is the order
// items were added. A smart list has a parent_id and no items: it reads the
// parent's.
const createListTablesSQL = `
	CREATE TABLE IF NOT EXISTS list_metadata (
		list_id        TEXT PRIMARY KEY,
		created_by     TEXT NOT NULL,
		list_number    INTEGER NOT NULL,
		name           TEXT NOT NULL,
		is_index       BOOLEAN NOT NULL DEFAULT FALSE,
		auto_generated BOOLEAN NOT NULL DEFAULT FALSE,
		url            TEXT,
		parent_id      TEXT,
		created_at     DATETIME NOT NULL,
		updated_at     DATETIME NOT NULL,
		UNIQUE (created_by, name)
	);

	CREATE TABLE IF NOT EXISTS list_items (
		list_id    TEXT NOT NULL,
		id         TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		PRIMARY KEY (list_id, id)
	);

	CREATE TABLE IF NOT EXISTS list_migrations (
		source      TEXT PRIMARY KEY,
		migrated_at DATETIME NOT NULL
	);
`

// listColumns are the columns lists had in the DuckDB catalog;
// storedListColumns adds the ones only SQLite has.
const (
	listColumns       = "list_id, created_by, list_number, name, is_index, auto_generated, url, created_at, updated_at"
	storedListColumns = listColumns + ", parent_id"
)

// listItemBatchSize bounds the number of rows in one multi-row INSERT.
const listItemBatchSize = 400

// duckDBListsMigration marks the one-time copy of lists out of the DuckDB
// instrument catalog.
const duckDBListsMigration = "duckdb-instruments"

// ListsSQLiteRepository stores instrument lists in SQLite.
type ListsSQLiteRepository struct {
	db *sql.DB
}

// NewListsSQLiteRepository opens the lists database at databasePath, creating it when new. The
// first time, it copies the lists an instruments DuckDB catalog at legacyInstrumentsPath held
// before lists moved here; an empty legacyInstrumentsPath skips that.
func NewListsSQLiteRepository(ctx context.Context, databasePath, legacyInstrumentsPath string) (*ListsSQLiteRepository, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", models.ErrInvalidInput)
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, fmt.Errorf("%w: lists database path is required", models.ErrInvalidInput)
	}
	dsn := "file:" + databasePath + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	if databasePath == ":memory:" {
		dsn = "file::memory:?_txlock=immediate"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("lists: open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, createListTablesSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lists: create tables: %w", err)
	}
	if err := addParentColumn(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &ListsSQLiteRepository{db: db}
	if legacy := strings.TrimSpace(legacyInstrumentsPath); legacy != "" && legacy != ":memory:" {
		if err := store.migrateFromDuckDB(ctx, legacy); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

// Close closes the database.
func (s *ListsSQLiteRepository) Close() error { return s.db.Close() }

// addParentColumn adds parent_id to a database made before smart lists.
func addParentColumn(ctx context.Context, db *sql.DB) error {
	var has int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM pragma_table_info('list_metadata') WHERE name = 'parent_id'",
	).Scan(&has); err != nil {
		return fmt.Errorf("lists: read columns: %w", err)
	}
	if has > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE list_metadata ADD COLUMN parent_id TEXT"); err != nil {
		return fmt.Errorf("lists: add parent_id: %w", err)
	}
	return nil
}

// migrateFromDuckDB copies the lists of the instruments catalog at path, or
// of the copy Replace kept aside, once.
func (s *ListsSQLiteRepository) migrateFromDuckDB(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lists: begin migration: %w", err)
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM list_migrations WHERE source = ?", duckDBListsMigration).Scan(&done); err != nil {
		return fmt.Errorf("lists: read migrations: %w", err)
	}
	if done > 0 {
		return nil
	}
	for _, source := range []string{LegacyListsPath(path), path} {
		copied, err := copyDuckDBLists(ctx, tx, source)
		if err != nil {
			return err
		}
		if copied {
			break
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO list_migrations (source, migrated_at) VALUES (?, ?)", duckDBListsMigration, time.Now().UTC()); err != nil {
		return fmt.Errorf("lists: record migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lists: commit migration: %w", err)
	}
	return nil
}

// copyDuckDBLists copies the list tables of the DuckDB file at path,
// reporting false when there is no such file or it has none.
func copyDuckDBLists(ctx context.Context, tx *sql.Tx, path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	source, err := sql.Open("duckdb", path+"?access_mode=read_only")
	if err != nil {
		return false, fmt.Errorf("lists: open %s to migrate its lists: %w", path, err)
	}
	defer source.Close()
	has, err := hasListTables(ctx, source)
	if err != nil {
		return false, fmt.Errorf("lists: migrate from %s: %w", path, err)
	}
	if !has {
		return false, nil
	}

	rows, err := source.QueryContext(ctx, "SELECT "+listColumns+" FROM list_metadata ORDER BY created_by, list_number")
	if err != nil {
		return false, fmt.Errorf("lists: read lists of %s: %w", path, err)
	}
	var lists []models.List
	for rows.Next() {
		list, err := scanList(rows)
		if err != nil {
			rows.Close()
			return false, fmt.Errorf("lists: read lists of %s: %w", path, err)
		}
		lists = append(lists, list)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, fmt.Errorf("lists: read lists of %s: %w", path, err)
	}
	for _, list := range lists {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO list_metadata ("+listColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			list.ID, list.CreatedBy, list.Number, list.Name, list.IsIndex, list.AutoGenerated, nullableText(list.URL),
			list.CreatedAt.UTC(), list.UpdatedAt.UTC(),
		); err != nil {
			return false, fmt.Errorf("lists: migrate %q: %w", list.Name, err)
		}
	}

	items, err := source.QueryContext(ctx, "SELECT list_id, id, created_at FROM list_items ORDER BY list_id, created_at, id")
	if err != nil {
		return false, fmt.Errorf("lists: read list items of %s: %w", path, err)
	}
	defer items.Close()
	for items.Next() {
		var listID, id string
		var createdAt time.Time
		if err := items.Scan(&listID, &id, &createdAt); err != nil {
			return false, fmt.Errorf("lists: read list items of %s: %w", path, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO list_items (list_id, id, created_at) VALUES (?, ?, ?)", listID, id, createdAt.UTC(),
		); err != nil {
			return false, fmt.Errorf("lists: migrate items of %q: %w", listID, err)
		}
	}
	if err := items.Err(); err != nil {
		return false, fmt.Errorf("lists: read list items of %s: %w", path, err)
	}
	return true, nil
}

// CreateList stores a new list and its items. ID, Number and timestamps are
// assigned by the repository. Names are unique per user. A list with a
// ParentID is smart: its Items are ignored, and a parent that is itself
// smart is replaced by that list's parent.
func (s *ListsSQLiteRepository) CreateList(ctx context.Context, list models.List) (models.List, error) {
	list.Name = strings.TrimSpace(list.Name)
	list.CreatedBy = strings.TrimSpace(list.CreatedBy)
	if list.Name == "" || list.CreatedBy == "" {
		return models.List{}, fmt.Errorf("%w: list name and creator are required", models.ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.List{}, fmt.Errorf("lists: begin create: %w", err)
	}
	defer tx.Rollback()

	if _, err := findListID(ctx, tx, list.CreatedBy, list.Name); err == nil {
		return models.List{}, fmt.Errorf("%w: %s already has a list named %q", models.ErrAlreadyExists, list.CreatedBy, list.Name)
	} else if !errors.Is(err, models.ErrNotFound) {
		return models.List{}, err
	}
	if list.ParentID = strings.TrimSpace(list.ParentID); list.ParentID != "" {
		var grandparent sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT parent_id FROM list_metadata WHERE list_id = ?", list.ParentID).Scan(&grandparent)
		if errors.Is(err, sql.ErrNoRows) {
			return models.List{}, fmt.Errorf("%w: parent list %q", models.ErrNotFound, list.ParentID)
		}
		if err != nil {
			return models.List{}, fmt.Errorf("lists: read parent %q: %w", list.ParentID, err)
		}
		if grandparent.Valid {
			list.ParentID = grandparent.String
		}
		list.Items = nil
	}

	now := time.Now().UTC()
	list.ID = uuid.NewString()
	if err := insertListMetadata(ctx, tx, list, now); err != nil {
		return models.List{}, err
	}
	if err := insertListItems(ctx, tx, list.ID, list.InstrumentIDs(), now); err != nil {
		return models.List{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.List{}, fmt.Errorf("lists: commit create: %w", err)
	}
	return s.GetList(ctx, list.ID)
}

// ReplaceAutoGeneratedList creates or updates the auto-generated list that
// has the same creator and name. The list keeps its ID and number; items that
// are still present keep their original created_at.
func (s *ListsSQLiteRepository) ReplaceAutoGeneratedList(ctx context.Context, list models.List) (models.List, error) {
	list.Name = strings.TrimSpace(list.Name)
	list.CreatedBy = strings.TrimSpace(list.CreatedBy)
	if list.Name == "" || list.CreatedBy == "" {
		return models.List{}, fmt.Errorf("%w: list name and creator are required", models.ErrInvalidInput)
	}
	list.AutoGenerated = true
	list.ParentID = ""

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.List{}, fmt.Errorf("lists: begin replace: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	list.ID, err = findListID(ctx, tx, list.CreatedBy, list.Name)
	switch {
	case errors.Is(err, models.ErrNotFound):
		list.ID = uuid.NewString()
		if err := insertListMetadata(ctx, tx, list, now); err != nil {
			return models.List{}, err
		}
	case err != nil:
		return models.List{}, err
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE list_metadata
			SET is_index = ?, auto_generated = TRUE, url = ?, updated_at = ?
			WHERE list_id = ?`,
			list.IsIndex, nullableText(list.URL), now, list.ID,
		); err != nil {
			return models.List{}, fmt.Errorf("lists: update %q: %w", list.Name, err)
		}
		if err := deleteListItemsNotIn(ctx, tx, list.ID, list.InstrumentIDs()); err != nil {
			return models.List{}, err
		}
	}

	if err := insertListItems(ctx, tx, list.ID, list.InstrumentIDs(), now); err != nil {
		return models.List{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.List{}, fmt.Errorf("lists: commit replace: %w", err)
	}
	return s.GetList(ctx, list.ID)
}

// GetList returns one list with its items, a smart list's being its
// parent's.
func (s *ListsSQLiteRepository) GetList(ctx context.Context, id string) (models.List, error) {
	list, err := scanStoredList(s.db.QueryRowContext(ctx,
		"SELECT "+storedListColumns+" FROM list_metadata WHERE list_id = ?", id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return models.List{}, fmt.Errorf("%w: list %q", models.ErrNotFound, id)
	}
	if err != nil {
		return models.List{}, fmt.Errorf("lists: read %q: %w", id, err)
	}
	items, err := s.listItems(ctx, "WHERE list_id = ?", itemsListID(list))
	if err != nil {
		return models.List{}, err
	}
	list.Items = items[itemsListID(list)]
	return list, nil
}

// itemsListID is the list whose items list has: its parent's when it is
// smart.
func itemsListID(list models.List) string {
	if list.IsSmart() {
		return list.ParentID
	}
	return list.ID
}

// GetLists returns every list created by createdBy, or every list when
// createdBy is empty, ordered by creator and list number. Smart lists have
// their parents' items.
func (s *ListsSQLiteRepository) GetLists(ctx context.Context, createdBy string) ([]models.List, error) {
	where, args := "", []any{}
	if createdBy = strings.TrimSpace(createdBy); createdBy != "" {
		where, args = " WHERE created_by = ?", []any{createdBy}
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT "+storedListColumns+" FROM list_metadata"+where+" ORDER BY created_by, list_number", args...,
	)
	if err != nil {
		return nil, fmt.Errorf("lists: query lists: %w", err)
	}
	lists := make([]models.List, 0)
	for rows.Next() {
		list, err := scanStoredList(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("lists: scan list: %w", err)
		}
		lists = append(lists, list)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("lists: query lists: %w", err)
	}

	// The pool has a single connection, so items are read after the list rows
	// are closed.
	items, err := s.listItems(ctx, "WHERE list_id IN (SELECT COALESCE(parent_id, list_id) FROM list_metadata"+where+")", args...)
	if err != nil {
		return nil, err
	}
	for i := range lists {
		lists[i].Items = items[itemsListID(lists[i])]
	}
	return lists, nil
}

// RenameList changes a list's name. Names are unique per user.
func (s *ListsSQLiteRepository) RenameList(ctx context.Context, id string, name string) error {
	if name = strings.TrimSpace(name); name == "" {
		return fmt.Errorf("%w: list name is required", models.ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lists: begin rename: %w", err)
	}
	defer tx.Rollback()

	var createdBy string
	if err := tx.QueryRowContext(ctx, "SELECT created_by FROM list_metadata WHERE list_id = ?", id).Scan(&createdBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: list %q", models.ErrNotFound, id)
		}
		return fmt.Errorf("lists: read %q: %w", id, err)
	}
	if existing, err := findListID(ctx, tx, createdBy, name); err == nil && existing != id {
		return fmt.Errorf("%w: %s already has a list named %q", models.ErrAlreadyExists, createdBy, name)
	} else if err != nil && !errors.Is(err, models.ErrNotFound) {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE list_metadata SET name = ?, updated_at = ? WHERE list_id = ?", name, time.Now().UTC(), id,
	); err != nil {
		return fmt.Errorf("lists: rename %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lists: commit rename: %w", err)
	}
	return nil
}

// DeleteList removes a list and its items. The smart lists following it
// keep its items as their own and stop following it.
func (s *ListsSQLiteRepository) DeleteList(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lists: begin delete: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO list_items (list_id, id, created_at)
		SELECT child.list_id, item.id, item.created_at
		FROM list_metadata child JOIN list_items item ON item.list_id = child.parent_id
		WHERE child.parent_id = ?
		ORDER BY child.list_id, item.created_at, item.rowid`, id,
	); err != nil {
		return fmt.Errorf("lists: copy %q into the lists following it: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE list_metadata SET parent_id = NULL WHERE parent_id = ?", id); err != nil {
		return fmt.Errorf("lists: detach the lists following %q: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM list_items WHERE list_id = ?", id); err != nil {
		return fmt.Errorf("lists: delete items of %q: %w", id, err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM list_metadata WHERE list_id = ?", id)
	if err != nil {
		return fmt.Errorf("lists: delete %q: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("%w: list %q", models.ErrNotFound, id)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lists: commit delete: %w", err)
	}
	return nil
}

// AddListItems appends instruments to a list. Instruments already in the list
// keep their position. Smart lists refuse with models.ErrSmartList.
func (s *ListsSQLiteRepository) AddListItems(ctx context.Context, id string, instrumentIDs []models.InstrumentID) error {
	return s.changeListItems(ctx, id, func(tx *sql.Tx, now time.Time) error {
		return insertListItems(ctx, tx, id, instrumentIDs, now)
	})
}

// RemoveListItems removes instruments from a list. Smart lists refuse with
// models.ErrSmartList.
func (s *ListsSQLiteRepository) RemoveListItems(ctx context.Context, id string, instrumentIDs []models.InstrumentID) error {
	return s.changeListItems(ctx, id, func(tx *sql.Tx, _ time.Time) error {
		if len(instrumentIDs) == 0 {
			return nil
		}
		args := []any{id}
		for _, instrumentID := range instrumentIDs {
			args = append(args, string(instrumentID))
		}
		_, err := tx.ExecContext(ctx,
			"DELETE FROM list_items WHERE list_id = ? AND id IN ("+placeholders(len(instrumentIDs))+")", args...,
		)
		if err != nil {
			return fmt.Errorf("lists: remove items from %q: %w", id, err)
		}
		return nil
	})
}

func (s *ListsSQLiteRepository) changeListItems(ctx context.Context, id string, change func(*sql.Tx, time.Time) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lists: begin item change: %w", err)
	}
	defer tx.Rollback()

	var parent sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT parent_id FROM list_metadata WHERE list_id = ?", id).Scan(&parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: list %q", models.ErrNotFound, id)
		}
		return fmt.Errorf("lists: read %q: %w", id, err)
	}
	if parent.Valid {
		return fmt.Errorf("%w: list %q", models.ErrSmartList, id)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, "UPDATE list_metadata SET updated_at = ? WHERE list_id = ?", now, id); err != nil {
		return fmt.Errorf("lists: touch %q: %w", id, err)
	}
	if err := change(tx, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lists: commit item change: %w", err)
	}
	return nil
}

func (s *ListsSQLiteRepository) listItems(ctx context.Context, where string, args ...any) (map[string][]models.ListItem, error) {
	// Items added together share created_at; rowid keeps them in the order
	// they were added.
	rows, err := s.db.QueryContext(ctx,
		"SELECT list_id, id, created_at FROM list_items "+where+" ORDER BY list_id, created_at, rowid", args...,
	)
	if err != nil {
		return nil, fmt.Errorf("lists: query items: %w", err)
	}
	defer rows.Close()

	items := make(map[string][]models.ListItem)
	for rows.Next() {
		var listID, instrumentID string
		var item models.ListItem
		if err := rows.Scan(&listID, &instrumentID, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("lists: scan item: %w", err)
		}
		item.InstrumentID = models.InstrumentID(instrumentID)
		items[listID] = append(items[listID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lists: query items: %w", err)
	}
	return items, nil
}

func findListID(ctx context.Context, tx *sql.Tx, createdBy, name string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		"SELECT list_id FROM list_metadata WHERE created_by = ? AND name = ?", createdBy, name,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: list %q", models.ErrNotFound, name)
	}
	if err != nil {
		return "", fmt.Errorf("lists: find %q: %w", name, err)
	}
	return id, nil
}

func insertListMetadata(ctx context.Context, tx *sql.Tx, list models.List, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO list_metadata (list_id, created_by, list_number, name, is_index, auto_generated, url, parent_id, created_at, updated_at)
		SELECT ?, ?, COALESCE(max(list_number), 0) + 1, ?, ?, ?, ?, ?, ?, ?
		FROM list_metadata
		WHERE created_by = ?`,
		list.ID, list.CreatedBy, list.Name, list.IsIndex, list.AutoGenerated, nullableText(list.URL), nullableText(list.ParentID), now, now, list.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("lists: insert %q: %w", list.Name, err)
	}
	return nil
}

func insertListItems(ctx context.Context, tx *sql.Tx, listID string, instrumentIDs []models.InstrumentID, now time.Time) error {
	for start := 0; start < len(instrumentIDs); start += listItemBatchSize {
		batch := instrumentIDs[start:min(start+listItemBatchSize, len(instrumentIDs))]
		values := make([]string, 0, len(batch))
		args := make([]any, 0, 3*len(batch))
		for _, instrumentID := range batch {
			values = append(values, "(?, ?, ?)")
			args = append(args, listID, string(instrumentID), now)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO list_items (list_id, id, created_at) VALUES "+strings.Join(values, ", ")+" ON CONFLICT DO NOTHING", args...,
		); err != nil {
			return fmt.Errorf("lists: insert items into %q: %w", listID, err)
		}
	}
	return nil
}

func deleteListItemsNotIn(ctx context.Context, tx *sql.Tx, listID string, keep []models.InstrumentID) error {
	query, args := "DELETE FROM list_items WHERE list_id = ?", []any{listID}
	if len(keep) > 0 {
		query += " AND id NOT IN (" + placeholders(len(keep)) + ")"
		for _, instrumentID := range keep {
			args = append(args, string(instrumentID))
		}
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("lists: prune items of %q: %w", listID, err)
	}
	return nil
}

func scanList(row scanner) (models.List, error) {
	var list models.List
	var url sql.NullString
	err := row.Scan(
		&list.ID, &list.CreatedBy, &list.Number, &list.Name, &list.IsIndex,
		&list.AutoGenerated, &url, &list.CreatedAt, &list.UpdatedAt,
	)
	list.URL = url.String
	return list, err
}

// scanStoredList scans storedListColumns.
func scanStoredList(row scanner) (models.List, error) {
	var list models.List
	var url, parent sql.NullString
	err := row.Scan(
		&list.ID, &list.CreatedBy, &list.Number, &list.Name, &list.IsIndex,
		&list.AutoGenerated, &url, &list.CreatedAt, &list.UpdatedAt, &parent,
	)
	list.URL, list.ParentID = url.String, parent.String
	return list, err
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
