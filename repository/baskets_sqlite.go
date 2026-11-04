package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/devshoe/gokiteconnect/models"
)

// Baskets live in the lists database, beside the lists. Each item is the
// parameters of one order. Positions order a user's baskets and a basket's
// items: new ones go last, and MoveBasket and MoveBasketItem move them.
const createBasketTablesSQL = `
	CREATE TABLE IF NOT EXISTS baskets (
		basket_id  TEXT PRIMARY KEY,
		created_by TEXT NOT NULL,
		name       TEXT NOT NULL,
		position   INTEGER,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE (created_by, name)
	);

	CREATE TABLE IF NOT EXISTS basket_items (
		item_id          TEXT PRIMARY KEY,
		basket_id        TEXT NOT NULL,
		position         INTEGER NOT NULL,
		exchange         TEXT NOT NULL,
		tradingsymbol    TEXT NOT NULL,
		transaction_type TEXT NOT NULL,
		order_type       TEXT NOT NULL,
		product          TEXT NOT NULL,
		quantity         INTEGER NOT NULL,
		price            REAL NOT NULL DEFAULT 0,
		trigger_price    REAL NOT NULL DEFAULT 0,
		validity         TEXT NOT NULL DEFAULT '',
		tag              TEXT NOT NULL DEFAULT '',
		created_at       DATETIME NOT NULL,
		updated_at       DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS basket_items_by_basket ON basket_items (basket_id, position);
`

// basketOrder orders a user's baskets. Baskets a build older than positions
// made have none, and go last, oldest first.
const basketOrder = "position IS NULL, position, created_at, rowid"

const basketItemColumns = "item_id, exchange, tradingsymbol, transaction_type, order_type, product, quantity, " +
	"price, trigger_price, validity, tag, created_at, updated_at"

// BasketsSQLiteRepository stores order baskets in the lists database.
type BasketsSQLiteRepository struct {
	db *sql.DB
}

// Baskets returns the order baskets kept in the lists database, creating
// their tables when new. They share the lists' connection: close the lists
// repository, not them.
func (s *ListsSQLiteRepository) Baskets(ctx context.Context) (*BasketsSQLiteRepository, error) {
	if _, err := s.db.ExecContext(ctx, createBasketTablesSQL); err != nil {
		return nil, fmt.Errorf("baskets: create tables: %w", err)
	}
	if err := addBasketPositions(ctx, s.db); err != nil {
		return nil, err
	}
	return &BasketsSQLiteRepository{db: s.db}, nil
}

// addBasketPositions adds position to the baskets of a database made before
// baskets could be moved, numbering each user's oldest first.
func addBasketPositions(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("baskets: begin adding positions: %w", err)
	}
	defer tx.Rollback()
	var has int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM pragma_table_info('baskets') WHERE name = 'position'",
	).Scan(&has); err != nil {
		return fmt.Errorf("baskets: read columns: %w", err)
	}
	if has > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE baskets ADD COLUMN position INTEGER"); err != nil {
		return fmt.Errorf("baskets: add position: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE baskets SET position = ordered.position
		FROM (
			SELECT rowid AS basket, row_number() OVER (PARTITION BY created_by ORDER BY created_at, rowid) AS position
			FROM baskets
		) AS ordered
		WHERE baskets.rowid = ordered.basket`,
	); err != nil {
		return fmt.Errorf("baskets: number baskets: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("baskets: commit positions: %w", err)
	}
	return nil
}

// CreateBasket stores a new basket and its items, after the creator's other
// baskets. IDs and timestamps are assigned by the repository. Names are
// unique per user.
func (s *BasketsSQLiteRepository) CreateBasket(ctx context.Context, basket models.Basket) (models.Basket, error) {
	basket.Name = strings.TrimSpace(basket.Name)
	basket.CreatedBy = strings.TrimSpace(basket.CreatedBy)
	if basket.Name == "" || basket.CreatedBy == "" {
		return models.Basket{}, fmt.Errorf("%w: basket name and creator are required", models.ErrInvalidInput)
	}
	for _, item := range basket.Items {
		if err := item.Validate(); err != nil {
			return models.Basket{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Basket{}, fmt.Errorf("baskets: begin create: %w", err)
	}
	defer tx.Rollback()
	if err := uniqueBasketName(ctx, tx, basket.CreatedBy, basket.Name, ""); err != nil {
		return models.Basket{}, err
	}
	now := time.Now().UTC()
	basket.ID = uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO baskets (basket_id, created_by, name, position, created_at, updated_at)
		SELECT ?, ?, ?, COALESCE(MAX(position), 0) + 1, ?, ?
		FROM baskets
		WHERE created_by = ?`,
		basket.ID, basket.CreatedBy, basket.Name, now, now, basket.CreatedBy,
	); err != nil {
		return models.Basket{}, fmt.Errorf("baskets: create %q: %w", basket.Name, err)
	}
	if err := insertBasketItems(ctx, tx, basket.ID, basket.Items, now); err != nil {
		return models.Basket{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.Basket{}, fmt.Errorf("baskets: commit create: %w", err)
	}
	return s.GetBasket(ctx, basket.ID)
}

// uniqueBasketName refuses name when createdBy has another basket, not id,
// by that name.
func uniqueBasketName(ctx context.Context, tx *sql.Tx, createdBy, name, id string) error {
	var existing string
	err := tx.QueryRowContext(ctx, "SELECT basket_id FROM baskets WHERE created_by = ? AND name = ?", createdBy, name).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("baskets: find %q: %w", name, err)
	case existing != id:
		return fmt.Errorf("%w: %s already has a basket named %q", models.ErrAlreadyExists, createdBy, name)
	}
	return nil
}

// insertBasketItems appends items after the basket's last, giving each an
// ID.
func insertBasketItems(ctx context.Context, tx *sql.Tx, basketID string, items []models.BasketItem, now time.Time) error {
	var last int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), 0) FROM basket_items WHERE basket_id = ?", basketID).Scan(&last); err != nil {
		return fmt.Errorf("baskets: read items of %q: %w", basketID, err)
	}
	for index, item := range items {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO basket_items (basket_id, position, "+basketItemColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			basketID, last+index+1, uuid.NewString(), item.Exchange, item.TradingSymbol, item.TransactionType, item.OrderType,
			item.Product, item.Quantity, item.Price, item.TriggerPrice, item.Validity, item.Tag, now, now,
		); err != nil {
			return fmt.Errorf("baskets: add %s to %q: %w", item.InstrumentID(), basketID, err)
		}
	}
	return nil
}

// GetBasket returns one basket with its items.
func (s *BasketsSQLiteRepository) GetBasket(ctx context.Context, id string) (models.Basket, error) {
	baskets, err := s.queryBaskets(ctx, "WHERE basket_id = ?", id)
	if err != nil {
		return models.Basket{}, err
	}
	if len(baskets) == 0 {
		return models.Basket{}, fmt.Errorf("%w: basket %q", models.ErrNotFound, id)
	}
	return baskets[0], nil
}

// GetBaskets returns every basket created by createdBy, or every basket
// when createdBy is empty, ordered by creator and position.
func (s *BasketsSQLiteRepository) GetBaskets(ctx context.Context, createdBy string) ([]models.Basket, error) {
	if createdBy = strings.TrimSpace(createdBy); createdBy != "" {
		return s.queryBaskets(ctx, "WHERE created_by = ?", createdBy)
	}
	return s.queryBaskets(ctx, "")
}

func (s *BasketsSQLiteRepository) queryBaskets(ctx context.Context, where string, args ...any) ([]models.Basket, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT basket_id, created_by, name, created_at, updated_at FROM baskets "+where+" ORDER BY created_by, "+basketOrder, args...)
	if err != nil {
		return nil, fmt.Errorf("baskets: query baskets: %w", err)
	}
	baskets := make([]models.Basket, 0)
	index := map[string]int{}
	for rows.Next() {
		var b models.Basket
		if err := rows.Scan(&b.ID, &b.CreatedBy, &b.Name, &b.CreatedAt, &b.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("baskets: scan basket: %w", err)
		}
		index[b.ID] = len(baskets)
		baskets = append(baskets, b)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("baskets: query baskets: %w", err)
	}

	// The pool has a single connection, so items are read after the basket
	// rows are closed.
	items, err := s.db.QueryContext(ctx,
		"SELECT basket_id, "+basketItemColumns+" FROM basket_items WHERE basket_id IN (SELECT basket_id FROM baskets "+where+") ORDER BY basket_id, position", args...)
	if err != nil {
		return nil, fmt.Errorf("baskets: query items: %w", err)
	}
	defer items.Close()
	for items.Next() {
		var basketID string
		var item models.BasketItem
		if err := items.Scan(&basketID, &item.ID, &item.Exchange, &item.TradingSymbol, &item.TransactionType, &item.OrderType,
			&item.Product, &item.Quantity, &item.Price, &item.TriggerPrice, &item.Validity, &item.Tag, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("baskets: scan item: %w", err)
		}
		if i, ok := index[basketID]; ok {
			baskets[i].Items = append(baskets[i].Items, item)
		}
	}
	if err := items.Err(); err != nil {
		return nil, fmt.Errorf("baskets: query items: %w", err)
	}
	return baskets, nil
}

// RenameBasket changes a basket's name. Names are unique per user.
func (s *BasketsSQLiteRepository) RenameBasket(ctx context.Context, id, name string) error {
	if name = strings.TrimSpace(name); name == "" {
		return fmt.Errorf("%w: basket name is required", models.ErrInvalidInput)
	}
	return s.change(ctx, id, func(tx *sql.Tx, createdBy string, now time.Time) error {
		if err := uniqueBasketName(ctx, tx, createdBy, name, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE baskets SET name = ? WHERE basket_id = ?", name, id); err != nil {
			return fmt.Errorf("baskets: rename %q: %w", id, err)
		}
		return nil
	})
}

// DeleteBasket removes a basket and its items.
func (s *BasketsSQLiteRepository) DeleteBasket(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("baskets: begin delete: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM basket_items WHERE basket_id = ?", id); err != nil {
		return fmt.Errorf("baskets: delete items of %q: %w", id, err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM baskets WHERE basket_id = ?", id)
	if err != nil {
		return fmt.Errorf("baskets: delete %q: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("%w: basket %q", models.ErrNotFound, id)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("baskets: commit delete: %w", err)
	}
	return nil
}

// AddBasketItems appends orders to a basket.
func (s *BasketsSQLiteRepository) AddBasketItems(ctx context.Context, id string, items []models.BasketItem) error {
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	return s.change(ctx, id, func(tx *sql.Tx, _ string, now time.Time) error {
		return insertBasketItems(ctx, tx, id, items, now)
	})
}

// UpdateBasketItem replaces the parameters of the basket's item with
// item.ID, keeping its position.
func (s *BasketsSQLiteRepository) UpdateBasketItem(ctx context.Context, id string, item models.BasketItem) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.change(ctx, id, func(tx *sql.Tx, _ string, now time.Time) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE basket_items
			SET exchange = ?, tradingsymbol = ?, transaction_type = ?, order_type = ?, product = ?, quantity = ?,
				price = ?, trigger_price = ?, validity = ?, tag = ?, updated_at = ?
			WHERE basket_id = ? AND item_id = ?`,
			item.Exchange, item.TradingSymbol, item.TransactionType, item.OrderType, item.Product, item.Quantity,
			item.Price, item.TriggerPrice, item.Validity, item.Tag, now, id, item.ID)
		if err != nil {
			return fmt.Errorf("baskets: update item %q: %w", item.ID, err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected == 0 {
			return fmt.Errorf("%w: basket item %q", models.ErrNotFound, item.ID)
		}
		return nil
	})
}

// RemoveBasketItems removes orders from a basket.
func (s *BasketsSQLiteRepository) RemoveBasketItems(ctx context.Context, id string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	return s.change(ctx, id, func(tx *sql.Tx, _ string, _ time.Time) error {
		args := []any{id}
		for _, itemID := range itemIDs {
			args = append(args, itemID)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM basket_items WHERE basket_id = ? AND item_id IN ("+placeholders(len(itemIDs))+")", args...,
		); err != nil {
			return fmt.Errorf("baskets: remove items from %q: %w", id, err)
		}
		return nil
	})
}

// MoveBasket moves a basket offset places among its creator's baskets, a
// negative offset towards the first, stopping at either end.
func (s *BasketsSQLiteRepository) MoveBasket(ctx context.Context, id string, offset int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("baskets: begin move: %w", err)
	}
	defer tx.Rollback()
	var createdBy string
	if err := tx.QueryRowContext(ctx, "SELECT created_by FROM baskets WHERE basket_id = ?", id).Scan(&createdBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: basket %q", models.ErrNotFound, id)
		}
		return fmt.Errorf("baskets: read %q: %w", id, err)
	}
	ids, err := queryIDs(ctx, tx, "SELECT basket_id FROM baskets WHERE created_by = ? ORDER BY "+basketOrder, createdBy)
	if err != nil {
		return fmt.Errorf("baskets: read baskets of %s: %w", createdBy, err)
	}
	order, _ := moveID(ids, id, offset)
	if err := renumber(ctx, tx, "UPDATE baskets SET position = ? WHERE basket_id = ?", order); err != nil {
		return fmt.Errorf("baskets: move %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("baskets: commit move: %w", err)
	}
	return nil
}

// MoveBasketItem moves an order offset places within its basket, a negative
// offset towards the first, stopping at either end.
func (s *BasketsSQLiteRepository) MoveBasketItem(ctx context.Context, id, itemID string, offset int) error {
	return s.change(ctx, id, func(tx *sql.Tx, _ string, _ time.Time) error {
		ids, err := queryIDs(ctx, tx, "SELECT item_id FROM basket_items WHERE basket_id = ? ORDER BY position, rowid", id)
		if err != nil {
			return fmt.Errorf("baskets: read items of %q: %w", id, err)
		}
		order, ok := moveID(ids, itemID, offset)
		if !ok {
			return fmt.Errorf("%w: basket item %q", models.ErrNotFound, itemID)
		}
		if err := renumber(ctx, tx, "UPDATE basket_items SET position = ? WHERE basket_id = ? AND item_id = ?", order, id); err != nil {
			return fmt.Errorf("baskets: move item %q: %w", itemID, err)
		}
		return nil
	})
}

// change runs fn on basket id in one transaction and marks it updated.
func (s *BasketsSQLiteRepository) change(ctx context.Context, id string, fn func(tx *sql.Tx, createdBy string, now time.Time) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("baskets: begin change: %w", err)
	}
	defer tx.Rollback()
	var createdBy string
	if err := tx.QueryRowContext(ctx, "SELECT created_by FROM baskets WHERE basket_id = ?", id).Scan(&createdBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: basket %q", models.ErrNotFound, id)
		}
		return fmt.Errorf("baskets: read %q: %w", id, err)
	}
	now := time.Now().UTC()
	if err := fn(tx, createdBy, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE baskets SET updated_at = ? WHERE basket_id = ?", now, id); err != nil {
		return fmt.Errorf("baskets: touch %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("baskets: commit change: %w", err)
	}
	return nil
}
