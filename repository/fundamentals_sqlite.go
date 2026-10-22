package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/devshoe/gokiteconnect/models"
)

// instrument_fundamentals holds the latest read of Kite's screener, one row
// per instrument token; each refresh replaces every row. tags is a JSON
// array. fundamentals_refresh keeps when that read was taken.
const createFundamentalsTablesSQL = `
	CREATE TABLE IF NOT EXISTS instrument_fundamentals (
		instrument_token   INTEGER PRIMARY KEY,
		exchange           TEXT NOT NULL,
		trading_symbol     TEXT NOT NULL,
		isin               TEXT NOT NULL,
		name               TEXT NOT NULL,
		sector             TEXT NOT NULL,
		tags               TEXT NOT NULL,
		last_price         REAL NOT NULL,
		market_cap         REAL NOT NULL,
		pe                 REAL NOT NULL,
		dividend_yield     REAL NOT NULL,
		roe                REAL NOT NULL,
		debt_to_equity     REAL NOT NULL,
		week_52_high       REAL NOT NULL,
		week_52_low        REAL NOT NULL,
		revenue_growth_yoy REAL NOT NULL,
		pat_growth_yoy     REAL NOT NULL,
		free_cash_flow     REAL NOT NULL,
		updated_at         DATETIME
	);
	CREATE INDEX IF NOT EXISTS instrument_fundamentals_by_symbol ON instrument_fundamentals (exchange, trading_symbol);

	CREATE TABLE IF NOT EXISTS fundamentals_refresh (
		id           INTEGER PRIMARY KEY CHECK (id = 1),
		refreshed_at DATETIME NOT NULL
	);
`

const fundamentalsColumns = `instrument_token, exchange, trading_symbol, isin, name, sector, tags,
	last_price, market_cap, pe, dividend_yield, roe, debt_to_equity, week_52_high, week_52_low,
	revenue_growth_yoy, pat_growth_yoy, free_cash_flow, updated_at`

// FundamentalsSQLiteRepository stores instrument fundamentals in SQLite.
type FundamentalsSQLiteRepository struct {
	db *sql.DB
}

// NewFundamentalsSQLiteRepository opens the fundamentals database at
// databasePath, creating it when new. databasePath may be ":memory:".
func NewFundamentalsSQLiteRepository(ctx context.Context, databasePath string) (*FundamentalsSQLiteRepository, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", models.ErrInvalidInput)
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, fmt.Errorf("%w: fundamentals database path is required", models.ErrInvalidInput)
	}
	dsn := "file:" + databasePath + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	if databasePath == ":memory:" {
		dsn = "file::memory:?_txlock=immediate"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("fundamentals: open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, createFundamentalsTablesSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("fundamentals: create tables: %w", err)
	}
	return &FundamentalsSQLiteRepository{db: db}, nil
}

// Close closes the database.
func (r *FundamentalsSQLiteRepository) Close() error { return r.db.Close() }

// Replace stores fundamentals in place of every stored row, as read at
// refreshedAt. Of two rows with one instrument token the later is kept. It
// refuses an empty read, which would only empty the table.
func (r *FundamentalsSQLiteRepository) Replace(ctx context.Context, fundamentals []models.Fundamentals, refreshedAt time.Time) error {
	if len(fundamentals) == 0 {
		return fmt.Errorf("%w: no fundamentals to store", models.ErrInvalidInput)
	}
	if refreshedAt.IsZero() {
		return fmt.Errorf("%w: refresh time is required", models.ErrInvalidInput)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fundamentals: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM instrument_fundamentals"); err != nil {
		return fmt.Errorf("fundamentals: clear: %w", err)
	}
	insert, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO instrument_fundamentals ("+fundamentalsColumns+") VALUES ("+placeholders(19)+")")
	if err != nil {
		return fmt.Errorf("fundamentals: prepare: %w", err)
	}
	defer insert.Close()
	for _, f := range fundamentals {
		if f.InstrumentToken <= 0 || strings.TrimSpace(f.Exchange) == "" || strings.TrimSpace(f.TradingSymbol) == "" {
			return fmt.Errorf("%w: fundamentals need an instrument token, exchange and trading symbol, got %d %s:%s",
				models.ErrInvalidInput, f.InstrumentToken, f.Exchange, f.TradingSymbol)
		}
		tags := f.Tags
		if tags == nil {
			tags = []string{}
		}
		encoded, err := json.Marshal(tags)
		if err != nil {
			return fmt.Errorf("fundamentals: encode tags of %s: %w", f.ID(), err)
		}
		var updatedAt any
		if !f.UpdatedAt.IsZero() {
			updatedAt = f.UpdatedAt.UTC()
		}
		if _, err := insert.ExecContext(ctx,
			f.InstrumentToken, f.Exchange, f.TradingSymbol, f.ISIN, f.Name, f.Sector, string(encoded),
			f.LastPrice, f.MarketCap, f.PE, f.DividendYield, f.ROE, f.DebtToEquity, f.Week52High, f.Week52Low,
			f.RevenueGrowthYOY, f.PATGrowthYOY, f.FreeCashFlow, updatedAt,
		); err != nil {
			return fmt.Errorf("fundamentals: store %s: %w", f.ID(), err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO fundamentals_refresh (id, refreshed_at) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET refreshed_at = excluded.refreshed_at",
		refreshedAt.UTC()); err != nil {
		return fmt.Errorf("fundamentals: record refresh: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fundamentals: commit: %w", err)
	}
	return nil
}

// LastRefreshedAt is when the stored fundamentals were read, zero when
// they never were.
func (r *FundamentalsSQLiteRepository) LastRefreshedAt(ctx context.Context) (time.Time, error) {
	var refreshedAt time.Time
	err := r.db.QueryRowContext(ctx, "SELECT refreshed_at FROM fundamentals_refresh WHERE id = 1").Scan(&refreshedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("fundamentals: read refresh time: %w", err)
	}
	return refreshedAt, nil
}

// Count is how many instruments have stored fundamentals.
func (r *FundamentalsSQLiteRepository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM instrument_fundamentals").Scan(&count); err != nil {
		return 0, fmt.Errorf("fundamentals: count: %w", err)
	}
	return count, nil
}

// Get is the fundamentals of the instrument with token, or ErrNotFound.
func (r *FundamentalsSQLiteRepository) Get(ctx context.Context, token int64) (models.Fundamentals, error) {
	rows, err := r.query(ctx, "SELECT "+fundamentalsColumns+" FROM instrument_fundamentals WHERE instrument_token = ?", token)
	if err != nil {
		return models.Fundamentals{}, err
	}
	if len(rows) == 0 {
		return models.Fundamentals{}, fmt.Errorf("%w: no fundamentals for instrument token %d", models.ErrNotFound, token)
	}
	return rows[0], nil
}

// List is the stored fundamentals by market cap, largest first, of the
// instruments with any of tags, or of every instrument without tags.
func (r *FundamentalsSQLiteRepository) List(ctx context.Context, tags ...string) ([]models.Fundamentals, error) {
	query := "SELECT " + fundamentalsColumns + " FROM instrument_fundamentals"
	args := make([]any, 0, len(tags))
	if len(tags) > 0 {
		query += " WHERE EXISTS (SELECT 1 FROM json_each(tags) WHERE value IN (" + placeholders(len(tags)) + "))"
		for _, tag := range tags {
			args = append(args, tag)
		}
	}
	return r.query(ctx, query+" ORDER BY market_cap DESC, instrument_token", args...)
}

func (r *FundamentalsSQLiteRepository) query(ctx context.Context, query string, args ...any) ([]models.Fundamentals, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fundamentals: query: %w", err)
	}
	defer rows.Close()
	var all []models.Fundamentals
	for rows.Next() {
		var f models.Fundamentals
		var tags string
		var updatedAt sql.NullTime
		if err := rows.Scan(&f.InstrumentToken, &f.Exchange, &f.TradingSymbol, &f.ISIN, &f.Name, &f.Sector, &tags,
			&f.LastPrice, &f.MarketCap, &f.PE, &f.DividendYield, &f.ROE, &f.DebtToEquity, &f.Week52High, &f.Week52Low,
			&f.RevenueGrowthYOY, &f.PATGrowthYOY, &f.FreeCashFlow, &updatedAt); err != nil {
			return nil, fmt.Errorf("fundamentals: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(tags), &f.Tags); err != nil {
			return nil, fmt.Errorf("fundamentals: decode tags of %s: %w", f.ID(), err)
		}
		if updatedAt.Valid {
			f.UpdatedAt = updatedAt.Time
		}
		all = append(all, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fundamentals: query: %w", err)
	}
	return all, nil
}
