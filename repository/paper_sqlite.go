package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// legacyPaperSuffix named the paper database once kept beside a user's
// portfolio one, CC0006_paper.db next to CC0006.db, before paper accounts
// moved into the user's database.
const legacyPaperSuffix = "_paper"

// A paper account is its orders and fills; everything else is worked out
// from them. Its tables sit in the user's database beside the live order
// record, all named paper_. paper_orders keeps every state an order went through, like
// order_updates does for a live account, and paper_fills every execution.
// paper_account holds the opening capital and how far day-end work has
// run. Times are stored in UTC.
const createPaperTablesSQL = `
	CREATE TABLE IF NOT EXISTS paper_account (
		id              INTEGER PRIMARY KEY CHECK (id = 1),
		capital         REAL NOT NULL,
		created_at      DATETIME NOT NULL,
		settled_through DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS paper_orders (
		update_id   INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id    TEXT NOT NULL,
		status      TEXT NOT NULL,
		data        TEXT NOT NULL,
		recorded_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS paper_orders_by_order ON paper_orders (order_id, update_id);

	CREATE TABLE IF NOT EXISTS paper_fills (
		trade_id         TEXT PRIMARY KEY,
		order_id         TEXT NOT NULL,
		kind             TEXT NOT NULL,
		exchange         TEXT NOT NULL,
		trading_symbol   TEXT NOT NULL,
		instrument_token INTEGER NOT NULL,
		product          TEXT NOT NULL,
		transaction_type TEXT NOT NULL,
		quantity         INTEGER NOT NULL,
		price            REAL NOT NULL,
		charges          REAL NOT NULL,
		filled_at        DATETIME NOT NULL,
		instrument_type  TEXT NOT NULL,
		expiry           DATETIME,
		strike           REAL NOT NULL,
		underlying       TEXT NOT NULL,
		underlying_token INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS paper_fills_by_order ON paper_fills (order_id);
`

// LegacyPaperDatabasePath is where userID's paper account was kept before
// it moved into their database: dir/<USERID>_paper.db.
func LegacyPaperDatabasePath(dir, userID string) (string, error) {
	path, err := PortfolioDatabasePath(dir, userID)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".db") + legacyPaperSuffix + ".db", nil
}

// isLegacyPaperDatabase reports whether a portfolio directory entry is a
// paper database from before paper accounts moved into the user's one.
func isLegacyPaperDatabase(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))), legacyPaperSuffix)
}

// OpenUserPaperSQLiteRepository opens userID's paper account in their
// database in the portfolio directory dir, dir/<USERID>.db, beside their
// live order record; the directory and database are created when new. The
// first time, a paper account still in dir/<USERID>_paper.db is moved in,
// and that file is renamed <USERID>_paper.db.migrated.
func OpenUserPaperSQLiteRepository(ctx context.Context, dir, userID string) (*PaperSQLiteRepository, error) {
	path, err := PortfolioDatabasePath(dir, userID)
	if err != nil {
		return nil, err
	}
	legacy, _ := LegacyPaperDatabasePath(dir, userID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("paper: create directory: %w", err)
	}
	repo, err := NewPaperSQLiteRepository(ctx, path, userID)
	if err != nil {
		return nil, err
	}
	if err := repo.moveIn(ctx, legacy); err != nil {
		_ = repo.Close()
		return nil, err
	}
	return repo, nil
}

// paperCopies copy a paper account between databases attached as main and
// legacy, keeping its row IDs.
var paperCopies = []string{
	"INSERT INTO main.paper_account (id, capital, created_at, settled_through) " +
		"SELECT id, capital, created_at, settled_through FROM legacy.paper_account",
	"INSERT INTO main.paper_orders (update_id, order_id, status, data, recorded_at) " +
		"SELECT update_id, order_id, status, data, recorded_at FROM legacy.paper_orders",
	"INSERT INTO main.paper_fills (" + paperFillColumns + ") SELECT " + paperFillColumns + " FROM legacy.paper_fills",
}

const paperFillColumns = "trade_id, order_id, kind, exchange, trading_symbol, instrument_token, product, transaction_type, " +
	"quantity, price, charges, filled_at, instrument_type, expiry, strike, underlying, underlying_token"

// moveIn copies the paper account kept in the database at legacyPath into
// this one, then renames that file aside. A missing file is nothing to
// move. When both hold an account, the same one (opened at the same time)
// was moved already, and anything else is an error, leaving both alone.
func (r *PaperSQLiteRepository) moveIn(ctx context.Context, legacyPath string) error {
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("paper %s: read %s: %w", r.userID, legacyPath, err)
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("paper %s: connect: %w", r.userID, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS legacy", legacyPath); err != nil {
		return fmt.Errorf("paper %s: open %s: %w", r.userID, legacyPath, err)
	}
	attached := true
	defer func() {
		if attached {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "DETACH DATABASE legacy")
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("paper %s: begin move: %w", r.userID, err)
	}
	defer tx.Rollback()
	var current, old sql.NullTime
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT created_at FROM main.paper_account), (SELECT created_at FROM legacy.paper_account)").
		Scan(&current, &old); err != nil {
		return fmt.Errorf("paper %s: read %s: %w", r.userID, legacyPath, err)
	}
	switch {
	case !old.Valid:
		// The old file never opened an account: nothing to move.
	case current.Valid && !current.Time.Equal(old.Time):
		return fmt.Errorf("paper %s: %s holds another paper account than the user's database; remove or rename it",
			r.userID, legacyPath)
	case !current.Valid:
		for _, copy := range paperCopies {
			if _, err := tx.ExecContext(ctx, copy); err != nil {
				return fmt.Errorf("paper %s: move %s: %w", r.userID, legacyPath, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("paper %s: commit move: %w", r.userID, err)
	}
	if _, err := conn.ExecContext(ctx, "DETACH DATABASE legacy"); err != nil {
		return fmt.Errorf("paper %s: close %s: %w", r.userID, legacyPath, err)
	}
	attached = false
	// SQLite removes the log when the last connection closes, so one still
	// there is another program's: renaming the file under it would lose
	// what it writes next.
	if _, err := os.Stat(legacyPath + "-wal"); err == nil {
		return fmt.Errorf("paper %s: %s is open in another program; quit it, then open the paper account again", r.userID, legacyPath)
	}
	if err := os.Rename(legacyPath, legacyPath+".migrated"); err != nil {
		return fmt.Errorf("paper %s: rename %s: %w", r.userID, legacyPath, err)
	}
	return nil
}

// PaperChange is what one step of a paper account records together: new
// order states, fills, and how far day-end work has run.
type PaperChange struct {
	Orders []kite.Order
	Fills  []models.PaperFill
	// SettledThrough moves the account's settled day on when set.
	SettledThrough time.Time
}

// PaperSQLiteRepository stores one user's paper account in SQLite.
type PaperSQLiteRepository struct {
	db     *sql.DB
	userID string
	now    func() time.Time
}

// NewPaperSQLiteRepository opens userID's paper database at databasePath,
// creating it when new. databasePath may be ":memory:".
func NewPaperSQLiteRepository(ctx context.Context, databasePath, userID string) (*PaperSQLiteRepository, error) {
	user, err := models.NormalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, fmt.Errorf("%w: paper database path is required", models.ErrInvalidInput)
	}
	dsn := "file:" + databasePath + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	if databasePath == ":memory:" {
		dsn = "file::memory:?_txlock=immediate"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("paper %s: open SQLite: %w", user, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, createPaperTablesSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("paper %s: create tables: %w", user, err)
	}
	return &PaperSQLiteRepository{db: db, userID: user, now: time.Now}, nil
}

// UserID is the user whose paper account the database holds.
func (r *PaperSQLiteRepository) UserID() string { return r.userID }

// Close closes the database.
func (r *PaperSQLiteRepository) Close() error { return r.db.Close() }

// Account returns the paper account, wrapping models.ErrNotFound before
// it is opened.
func (r *PaperSQLiteRepository) Account(ctx context.Context) (models.PaperAccount, error) {
	var account models.PaperAccount
	err := r.db.QueryRowContext(ctx, "SELECT capital, created_at, settled_through FROM paper_account WHERE id = 1").
		Scan(&account.Capital, &account.CreatedAt, &account.SettledThrough)
	if errors.Is(err, sql.ErrNoRows) {
		return account, fmt.Errorf("%w: paper account of %s", models.ErrNotFound, r.userID)
	}
	if err != nil {
		return account, fmt.Errorf("paper %s: read account: %w", r.userID, err)
	}
	return account, nil
}

// OpenAccount creates the paper account as account describes unless it
// already exists, and returns the stored one.
func (r *PaperSQLiteRepository) OpenAccount(ctx context.Context, account models.PaperAccount) (models.PaperAccount, error) {
	if account.Capital <= 0 || account.CreatedAt.IsZero() {
		return models.PaperAccount{}, fmt.Errorf("%w: paper account needs capital and a creation time", models.ErrInvalidInput)
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO paper_account (id, capital, created_at, settled_through)
		VALUES (1, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		account.Capital, account.CreatedAt.UTC(), account.SettledThrough.UTC()); err != nil {
		return models.PaperAccount{}, fmt.Errorf("paper %s: open account: %w", r.userID, err)
	}
	return r.Account(ctx)
}

// Reset deletes the paper account: its orders, its fills and the account
// itself, in one transaction. The user's live order record is untouched.
func (r *PaperSQLiteRepository) Reset(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("paper %s: begin reset: %w", r.userID, err)
	}
	defer tx.Rollback()
	for _, table := range []string{"paper_fills", "paper_orders", "paper_account"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("paper %s: reset %s: %w", r.userID, table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("paper %s: commit reset: %w", r.userID, err)
	}
	return nil
}

// Record stores change in one transaction.
func (r *PaperSQLiteRepository) Record(ctx context.Context, change PaperChange) error {
	for _, order := range change.Orders {
		if strings.TrimSpace(order.OrderID) == "" || strings.TrimSpace(order.Status) == "" {
			return fmt.Errorf("%w: paper order needs an ID and a status", models.ErrInvalidInput)
		}
	}
	for _, fill := range change.Fills {
		if fill.TradeID == "" || fill.OrderID == "" || fill.Quantity <= 0 || fill.Price < 0 || fill.FilledAt.IsZero() {
			return fmt.Errorf("%w: paper fill %q needs an ID, an order, a quantity, a price and a time", models.ErrInvalidInput, fill.TradeID)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("paper %s: begin: %w", r.userID, err)
	}
	defer tx.Rollback()
	recordedAt := r.now().UTC()
	for _, order := range change.Orders {
		data, err := json.Marshal(order)
		if err != nil {
			return fmt.Errorf("paper %s: encode order %s: %w", r.userID, order.OrderID, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO paper_orders (order_id, status, data, recorded_at) VALUES (?, ?, ?, ?)",
			order.OrderID, order.Status, string(data), recordedAt); err != nil {
			return fmt.Errorf("paper %s: store order %s: %w", r.userID, order.OrderID, err)
		}
	}
	for _, fill := range change.Fills {
		var expiry any
		if !fill.Expiry.IsZero() {
			expiry = fill.Expiry.UTC()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO paper_fills (trade_id, order_id, kind, exchange, trading_symbol,
			instrument_token, product, transaction_type, quantity, price, charges, filled_at, instrument_type, expiry,
			strike, underlying, underlying_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fill.TradeID, fill.OrderID, fill.Kind, fill.Exchange, fill.TradingSymbol, fill.InstrumentToken, fill.Product,
			fill.TransactionType, fill.Quantity, fill.Price, fill.Charges, fill.FilledAt.UTC(), fill.InstrumentType, expiry,
			fill.Strike, string(fill.Underlying), fill.UnderlyingToken); err != nil {
			return fmt.Errorf("paper %s: store fill %s: %w", r.userID, fill.TradeID, err)
		}
	}
	if !change.SettledThrough.IsZero() {
		if _, err := tx.ExecContext(ctx, "UPDATE paper_account SET settled_through = ? WHERE id = 1", change.SettledThrough.UTC()); err != nil {
			return fmt.Errorf("paper %s: store settled day: %w", r.userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("paper %s: commit: %w", r.userID, err)
	}
	return nil
}

// Orders returns each order's current state, in the order they were
// placed.
func (r *PaperSQLiteRepository) Orders(ctx context.Context) (kite.Orders, error) {
	return r.orders(ctx, `SELECT data FROM paper_orders u
		WHERE update_id = (SELECT MAX(v.update_id) FROM paper_orders v WHERE v.order_id = u.order_id)
		ORDER BY (SELECT MIN(v.update_id) FROM paper_orders v WHERE v.order_id = u.order_id)`)
}

// OrderHistory returns every state of orderID, oldest first, wrapping
// models.ErrNotFound when there is no such order.
func (r *PaperSQLiteRepository) OrderHistory(ctx context.Context, orderID string) ([]kite.Order, error) {
	orders, err := r.orders(ctx, "SELECT data FROM paper_orders WHERE order_id = ? ORDER BY update_id", strings.TrimSpace(orderID))
	if err == nil && len(orders) == 0 {
		err = fmt.Errorf("%w: paper order %s of %s", models.ErrNotFound, orderID, r.userID)
	}
	return orders, err
}

func (r *PaperSQLiteRepository) orders(ctx context.Context, query string, args ...any) (kite.Orders, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("paper %s: read orders: %w", r.userID, err)
	}
	defer rows.Close()
	var orders kite.Orders
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("paper %s: scan order: %w", r.userID, err)
		}
		var order kite.Order
		if err := json.Unmarshal([]byte(data), &order); err != nil {
			return nil, fmt.Errorf("paper %s: decode order: %w", r.userID, err)
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("paper %s: read orders: %w", r.userID, err)
	}
	return orders, nil
}

// Fills returns every fill, oldest first.
func (r *PaperSQLiteRepository) Fills(ctx context.Context) ([]models.PaperFill, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT trade_id, order_id, kind, exchange, trading_symbol, instrument_token,
		product, transaction_type, quantity, price, charges, filled_at, instrument_type, expiry, strike, underlying,
		underlying_token FROM paper_fills ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("paper %s: read fills: %w", r.userID, err)
	}
	defer rows.Close()
	var fills []models.PaperFill
	for rows.Next() {
		var fill models.PaperFill
		var expiry sql.NullTime
		var underlying string
		if err := rows.Scan(&fill.TradeID, &fill.OrderID, &fill.Kind, &fill.Exchange, &fill.TradingSymbol, &fill.InstrumentToken,
			&fill.Product, &fill.TransactionType, &fill.Quantity, &fill.Price, &fill.Charges, &fill.FilledAt, &fill.InstrumentType,
			&expiry, &fill.Strike, &underlying, &fill.UnderlyingToken); err != nil {
			return nil, fmt.Errorf("paper %s: scan fill: %w", r.userID, err)
		}
		if expiry.Valid {
			fill.Expiry = expiry.Time
		}
		fill.Underlying = models.InstrumentID(underlying)
		fills = append(fills, fill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("paper %s: read fills: %w", r.userID, err)
	}
	// Fills found by replaying candles can be recorded after later ones.
	slices.SortStableFunc(fills, func(a, b models.PaperFill) int { return a.FilledAt.Compare(b.FilledAt) })
	return fills, nil
}
