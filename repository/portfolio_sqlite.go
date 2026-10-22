package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// Each user's portfolio lives in its own SQLite database, so the tables
// carry no user column. order_updates keeps every distinct state an order
// was seen in, from ticker order updates and from polled order books;
// digest, the SHA-256 of the stored JSON, makes recording the same state
// twice harmless. portfolio_snapshots keeps each read of the account,
// one Kite response as JSON per row.
const createPortfolioTablesSQL = `
	CREATE TABLE IF NOT EXISTS order_updates (
		update_id   INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id    TEXT NOT NULL,
		status      TEXT NOT NULL,
		tag         TEXT NOT NULL,
		digest      TEXT NOT NULL,
		data        TEXT NOT NULL,
		recorded_at DATETIME NOT NULL,
		UNIQUE (order_id, digest)
	);
	CREATE INDEX IF NOT EXISTS order_updates_by_order ON order_updates (order_id, update_id);

	CREATE TABLE IF NOT EXISTS portfolio_snapshots (
		snapshot_id   INTEGER PRIMARY KEY AUTOINCREMENT,
		snapshot_type TEXT NOT NULL,
		taken_at      DATETIME NOT NULL,
		data          TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS portfolio_snapshots_by_type ON portfolio_snapshots (snapshot_type, snapshot_id);
`

// terminalStatusSQL is 1 for an update whose status ends its order, so
// ordering by it then update_id picks an order's current update even when
// a late OPEN arrives after its COMPLETE.
const terminalStatusSQL = "(status IN ('" + models.OrderStatusComplete + "', '" +
	models.OrderStatusCancelled + "', '" + models.OrderStatusRejected + "'))"

// PortfolioOrderUpdate is one recorded state of an order.
type PortfolioOrderUpdate struct {
	ID         int64
	RecordedAt time.Time
	Order      kite.Order
}

// PortfolioSQLiteRepository stores one user's order updates and portfolio
// snapshots in SQLite.
type PortfolioSQLiteRepository struct {
	db     *sql.DB
	userID string
	now    func() time.Time
}

// NewPortfolioSQLiteRepository opens userID's portfolio database at
// databasePath, creating it when new. databasePath may be ":memory:".
func NewPortfolioSQLiteRepository(ctx context.Context, databasePath, userID string) (*PortfolioSQLiteRepository, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", models.ErrInvalidInput)
	}
	user, err := models.NormalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, fmt.Errorf("%w: portfolio database path is required", models.ErrInvalidInput)
	}
	dsn := "file:" + databasePath + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	if databasePath == ":memory:" {
		dsn = "file::memory:?_txlock=immediate"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("portfolio %s: open SQLite: %w", user, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, createPortfolioTablesSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("portfolio %s: create tables: %w", user, err)
	}
	return &PortfolioSQLiteRepository{db: db, userID: user, now: time.Now}, nil
}

// UserID is the user whose portfolio the database holds.
func (r *PortfolioSQLiteRepository) UserID() string { return r.userID }

// Close closes the database.
func (r *PortfolioSQLiteRepository) Close() error { return r.db.Close() }

// RecordOrderUpdates stores orders as order updates, skipping any state
// already stored, and returns how many it stored. An order must have an
// ID and a status, and an account ID, when it has one, must be the
// repository's user.
func (r *PortfolioSQLiteRepository) RecordOrderUpdates(ctx context.Context, orders ...kite.Order) (int, error) {
	type row struct{ orderID, status, tag, digest, data string }
	rows := make([]row, 0, len(orders))
	for _, order := range orders {
		orderID, status := strings.TrimSpace(order.OrderID), strings.ToUpper(strings.TrimSpace(order.Status))
		if orderID == "" || status == "" {
			return 0, fmt.Errorf("%w: order update needs an order ID and a status", models.ErrInvalidInput)
		}
		if account := strings.TrimSpace(order.AccountID); account != "" && !strings.EqualFold(account, r.userID) {
			return 0, fmt.Errorf("%w: order %s belongs to %s, not %s", models.ErrInvalidInput, orderID, account, r.userID)
		}
		order.OrderID, order.Status = orderID, status
		data, err := json.Marshal(order)
		if err != nil {
			return 0, fmt.Errorf("portfolio %s: encode order %s: %w", r.userID, orderID, err)
		}
		sum := sha256.Sum256(data)
		rows = append(rows, row{orderID, status, order.Tag, hex.EncodeToString(sum[:]), string(data)})
	}
	if len(rows) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("portfolio %s: begin: %w", r.userID, err)
	}
	defer tx.Rollback()
	insert, err := tx.PrepareContext(ctx, `INSERT INTO order_updates (order_id, status, tag, digest, data, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (order_id, digest) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("portfolio %s: prepare order update: %w", r.userID, err)
	}
	defer insert.Close()
	recordedAt := r.now().UTC()
	stored := 0
	for _, row := range rows {
		result, err := insert.ExecContext(ctx, row.orderID, row.status, row.tag, row.digest, row.data, recordedAt)
		if err != nil {
			return 0, fmt.Errorf("portfolio %s: store order %s: %w", r.userID, row.orderID, err)
		}
		if n, _ := result.RowsAffected(); n > 0 {
			stored++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("portfolio %s: commit order updates: %w", r.userID, err)
	}
	return stored, nil
}

// OrderUpdates returns the updates filter selects, oldest first. Tags
// match an order's tag or any of its tags.
func (r *PortfolioSQLiteRepository) OrderUpdates(ctx context.Context, filter models.OrderUpdateFilter) ([]PortfolioOrderUpdate, error) {
	var where []string
	var args []any
	if filter.LatestOnly {
		where = append(where, `update_id = (SELECT v.update_id FROM order_updates v WHERE v.order_id = u.order_id
			ORDER BY `+terminalStatusSQL+` DESC, v.update_id DESC LIMIT 1)`)
	}
	if ids := cleanValues(filter.OrderIDs, false); len(ids) > 0 {
		where = append(where, "order_id IN ("+placeholders(len(ids))+")")
		args = appendAll(args, ids)
	}
	if statuses := cleanValues(filter.Statuses, true); len(statuses) > 0 {
		where = append(where, "status IN ("+placeholders(len(statuses))+")")
		args = appendAll(args, statuses)
	}
	if tags := cleanValues(filter.Tags, false); len(tags) > 0 {
		marks := placeholders(len(tags))
		where = append(where, "(tag IN ("+marks+") OR EXISTS (SELECT 1 FROM json_each(u.data, '$.tags') WHERE value IN ("+marks+")))")
		args = appendAll(appendAll(args, tags), tags)
	}
	query := "SELECT update_id, recorded_at, data FROM order_updates u"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY update_id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("portfolio %s: read order updates: %w", r.userID, err)
	}
	defer rows.Close()
	var updates []PortfolioOrderUpdate
	for rows.Next() {
		var update PortfolioOrderUpdate
		var data string
		if err := rows.Scan(&update.ID, &update.RecordedAt, &data); err != nil {
			return nil, fmt.Errorf("portfolio %s: scan order update: %w", r.userID, err)
		}
		if err := json.Unmarshal([]byte(data), &update.Order); err != nil {
			return nil, fmt.Errorf("portfolio %s: decode order update %d: %w", r.userID, update.ID, err)
		}
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("portfolio %s: read order updates: %w", r.userID, err)
	}
	return updates, nil
}

// OrderHistory returns every recorded state of orderID, oldest first.
func (r *PortfolioSQLiteRepository) OrderHistory(ctx context.Context, orderID string) ([]PortfolioOrderUpdate, error) {
	if strings.TrimSpace(orderID) == "" {
		return nil, fmt.Errorf("%w: order ID is required", models.ErrInvalidInput)
	}
	return r.OrderUpdates(ctx, models.OrderUpdateFilter{OrderIDs: []string{orderID}})
}

// LatestOrderUpdate returns orderID's current update: its terminal one if
// it has one, otherwise the last recorded. It wraps models.ErrNotFound when
// the order has none.
func (r *PortfolioSQLiteRepository) LatestOrderUpdate(ctx context.Context, orderID string) (PortfolioOrderUpdate, error) {
	if strings.TrimSpace(orderID) == "" {
		return PortfolioOrderUpdate{}, fmt.Errorf("%w: order ID is required", models.ErrInvalidInput)
	}
	updates, err := r.OrderUpdates(ctx, models.OrderUpdateFilter{OrderIDs: []string{orderID}, LatestOnly: true})
	if err != nil {
		return PortfolioOrderUpdate{}, err
	}
	if len(updates) == 0 {
		return PortfolioOrderUpdate{}, fmt.Errorf("%w: order %s of %s", models.ErrNotFound, strings.TrimSpace(orderID), r.userID)
	}
	return updates[0], nil
}

// OpenOrders returns the current update of each order not yet complete,
// cancelled or rejected.
func (r *PortfolioSQLiteRepository) OpenOrders(ctx context.Context) ([]PortfolioOrderUpdate, error) {
	updates, err := r.OrderUpdates(ctx, models.OrderUpdateFilter{LatestOnly: true})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(updates, func(u PortfolioOrderUpdate) bool {
		return models.IsTerminalOrderStatus(u.Order.Status)
	}), nil
}

// SaveSnapshot stores value, marshalled to JSON, as a snapshot of type
// snapshotType taken now. A json.RawMessage is stored as it is.
func (r *PortfolioSQLiteRepository) SaveSnapshot(ctx context.Context, snapshotType models.SnapshotType, value any) (models.PortfolioSnapshot, error) {
	if !snapshotType.IsValid() {
		return models.PortfolioSnapshot{}, fmt.Errorf("%w: snapshot type %q", models.ErrInvalidInput, snapshotType)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return models.PortfolioSnapshot{}, fmt.Errorf("portfolio %s: encode %s snapshot: %w", r.userID, snapshotType, err)
	}
	snapshot := models.PortfolioSnapshot{Type: snapshotType, TakenAt: r.now().UTC(), Data: data}
	if err := r.db.QueryRowContext(ctx,
		"INSERT INTO portfolio_snapshots (snapshot_type, taken_at, data) VALUES (?, ?, ?) RETURNING snapshot_id",
		string(snapshotType), snapshot.TakenAt, string(data),
	).Scan(&snapshot.ID); err != nil {
		return models.PortfolioSnapshot{}, fmt.Errorf("portfolio %s: store %s snapshot: %w", r.userID, snapshotType, err)
	}
	return snapshot, nil
}

// LatestSnapshot returns the last snapshot of type snapshotType, wrapping
// models.ErrNotFound when there is none.
func (r *PortfolioSQLiteRepository) LatestSnapshot(ctx context.Context, snapshotType models.SnapshotType) (models.PortfolioSnapshot, error) {
	if !snapshotType.IsValid() {
		return models.PortfolioSnapshot{}, fmt.Errorf("%w: snapshot type %q", models.ErrInvalidInput, snapshotType)
	}
	snapshots, err := r.querySnapshots(ctx,
		"WHERE snapshot_type = ? ORDER BY snapshot_id DESC LIMIT 1", string(snapshotType))
	if err != nil {
		return models.PortfolioSnapshot{}, err
	}
	if len(snapshots) == 0 {
		return models.PortfolioSnapshot{}, fmt.Errorf("%w: no %s snapshot of %s", models.ErrNotFound, snapshotType, r.userID)
	}
	return snapshots[0], nil
}

// Snapshots returns the snapshots of type snapshotType taken from from up
// to but not including to, oldest first. A zero bound is open.
func (r *PortfolioSQLiteRepository) Snapshots(ctx context.Context, snapshotType models.SnapshotType, from, to time.Time) ([]models.PortfolioSnapshot, error) {
	if !snapshotType.IsValid() {
		return nil, fmt.Errorf("%w: snapshot type %q", models.ErrInvalidInput, snapshotType)
	}
	clause := "WHERE snapshot_type = ?"
	args := []any{string(snapshotType)}
	if !from.IsZero() {
		clause += " AND taken_at >= ?"
		args = append(args, from.UTC())
	}
	if !to.IsZero() {
		clause += " AND taken_at < ?"
		args = append(args, to.UTC())
	}
	return r.querySnapshots(ctx, clause+" ORDER BY snapshot_id", args...)
}

// LastSnapshotTimes returns when each snapshot type was last taken; a type
// never taken is absent.
func (r *PortfolioSQLiteRepository) LastSnapshotTimes(ctx context.Context) (map[models.SnapshotType]time.Time, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT snapshot_type, taken_at FROM portfolio_snapshots
		WHERE snapshot_id IN (SELECT max(snapshot_id) FROM portfolio_snapshots GROUP BY snapshot_type)`)
	if err != nil {
		return nil, fmt.Errorf("portfolio %s: read snapshot times: %w", r.userID, err)
	}
	defer rows.Close()
	times := map[models.SnapshotType]time.Time{}
	for rows.Next() {
		var snapshotType string
		var takenAt time.Time
		if err := rows.Scan(&snapshotType, &takenAt); err != nil {
			return nil, fmt.Errorf("portfolio %s: scan snapshot time: %w", r.userID, err)
		}
		times[models.SnapshotType(snapshotType)] = takenAt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("portfolio %s: read snapshot times: %w", r.userID, err)
	}
	return times, nil
}

// DeleteSnapshotsBefore deletes snapshots taken before before, except the
// last of each type, and returns how many it deleted.
func (r *PortfolioSQLiteRepository) DeleteSnapshotsBefore(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("%w: a cutoff time is required", models.ErrInvalidInput)
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM portfolio_snapshots WHERE taken_at < ?
		AND snapshot_id NOT IN (SELECT max(snapshot_id) FROM portfolio_snapshots GROUP BY snapshot_type)`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("portfolio %s: delete snapshots: %w", r.userID, err)
	}
	return result.RowsAffected()
}

func (r *PortfolioSQLiteRepository) querySnapshots(ctx context.Context, clause string, args ...any) ([]models.PortfolioSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT snapshot_id, snapshot_type, taken_at, data FROM portfolio_snapshots "+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("portfolio %s: read snapshots: %w", r.userID, err)
	}
	defer rows.Close()
	var snapshots []models.PortfolioSnapshot
	for rows.Next() {
		var snapshot models.PortfolioSnapshot
		var snapshotType, data string
		if err := rows.Scan(&snapshot.ID, &snapshotType, &snapshot.TakenAt, &data); err != nil {
			return nil, fmt.Errorf("portfolio %s: scan snapshot: %w", r.userID, err)
		}
		snapshot.Type, snapshot.Data = models.SnapshotType(snapshotType), json.RawMessage(data)
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("portfolio %s: read snapshots: %w", r.userID, err)
	}
	return snapshots, nil
}

// cleanValues trims values, drops empty and repeated ones, and upper-cases
// them when upper is set.
func cleanValues(values []string, upper bool) []string {
	var cleaned []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if upper {
			value = strings.ToUpper(value)
		}
		if value != "" && !slices.Contains(cleaned, value) {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func appendAll(args []any, values []string) []any {
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

// PortfolioDatabasePath is userID's portfolio database in dir:
// dir/<USERID>.db, such as ~/tradebot/portfolio/CC0006.db.
func PortfolioDatabasePath(dir, userID string) (string, error) {
	user, err := models.NormalizeUserID(userID)
	if err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("%w: portfolio directory is required", models.ErrInvalidInput)
	}
	return filepath.Join(dir, user+".db"), nil
}

// PortfolioSQLiteDirectory opens each user's portfolio database in one
// directory, keeping it open until Close.
type PortfolioSQLiteDirectory struct {
	dir string

	mu     sync.Mutex
	open   map[string]*PortfolioSQLiteRepository
	closed bool
}

// NewPortfolioSQLiteDirectory keeps portfolio databases in dir, which is
// created, mode 0700, when the first one is opened.
func NewPortfolioSQLiteDirectory(dir string) (*PortfolioSQLiteDirectory, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("%w: portfolio directory is required", models.ErrInvalidInput)
	}
	return &PortfolioSQLiteDirectory{dir: filepath.Clean(dir), open: map[string]*PortfolioSQLiteRepository{}}, nil
}

// Dir is the directory the databases are in.
func (d *PortfolioSQLiteDirectory) Dir() string { return d.dir }

// User returns userID's portfolio, opening or creating its database the
// first time. The directory owns it: close the directory, not it.
func (d *PortfolioSQLiteDirectory) User(ctx context.Context, userID string) (*PortfolioSQLiteRepository, error) {
	path, err := PortfolioDatabasePath(d.dir, userID)
	if err != nil {
		return nil, err
	}
	user, _ := models.NormalizeUserID(userID)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("portfolio %s: directory is closed", user)
	}
	if repository, ok := d.open[user]; ok {
		return repository, nil
	}
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return nil, fmt.Errorf("portfolio %s: create directory: %w", user, err)
	}
	repository, err := NewPortfolioSQLiteRepository(ctx, path, user)
	if err != nil {
		return nil, err
	}
	d.open[user] = repository
	return repository, nil
}

// Users returns the users who have a portfolio database, sorted.
func (d *PortfolioSQLiteDirectory) Users() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("portfolio: read directory: %w", err)
	}
	var users []string
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".db")
		// A paper database from before paper accounts moved in may still
		// sit beside the user's.
		if !ok || entry.IsDir() || isLegacyPaperDatabase(entry.Name()) {
			continue
		}
		if user, err := models.NormalizeUserID(name); err == nil && user == name {
			users = append(users, user)
		}
	}
	slices.Sort(users)
	return users, nil
}

// Close closes every database the directory opened.
func (d *PortfolioSQLiteDirectory) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	var errs []error
	for user, repository := range d.open {
		if err := repository.Close(); err != nil {
			errs = append(errs, fmt.Errorf("portfolio %s: close: %w", user, err))
		}
		delete(d.open, user)
	}
	return errors.Join(errs...)
}
