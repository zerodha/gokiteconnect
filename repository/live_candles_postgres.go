package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	_ "github.com/lib/pq"
)

// liveCandleChunk is the most candles one INSERT carries: ten parameters
// each, well under Postgres's 65535.
const liveCandleChunk = 1000

// LiveCandle is one instrument's minute candle built from live ticks.
// FirstVolume and LastVolume are the day's cumulative volume at the first
// and last tick folded into it; the candle's volume is their difference.
type LiveCandle struct {
	Token        uint32
	InstrumentID models.InstrumentID
	// Minute is the candle's start, in exchange time.
	Minute                 time.Time
	Open, High, Low, Close float64
	FirstVolume            int64
	LastVolume             int64
}

// LiveCandlesPostgresRepository keeps live 1m candles in Postgres's
// live_data table, which CandlesDuckDBRepository merges into its reads when
// it is given the same database.
type LiveCandlesPostgresRepository struct {
	db *sql.DB
}

// NewLiveCandlesPostgresRepository connects to the Postgres database at dsn
// and creates live_data if it doesn't exist.
func NewLiveCandlesPostgresRepository(ctx context.Context, dsn string) (*LiveCandlesPostgresRepository, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("%w: live candles database is required", models.ErrInvalidInput)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("live candles: open Postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("live candles: connect to Postgres: %w", err)
	}
	// Existing tables keep their INT volumes; new ones take a day's volume
	// past INT's 2.1 billion.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS live_data (
			it INT,
			id TEXT,
			i TEXT,
			t TIMESTAMP,
			o DECIMAL(10,4),
			h DECIMAL(10,4),
			l DECIMAL(10,4),
			c DECIMAL(10,4),
			v BIGINT,
			v0 BIGINT,
			PRIMARY KEY (id, i, t)
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("live candles: create live_data: %w", err)
	}
	return &LiveCandlesPostgresRepository{db: db}, nil
}

// Close closes the database.
func (r *LiveCandlesPostgresRepository) Close() error {
	return r.db.Close()
}

// UpsertLiveCandles writes the candles in one transaction. A stored candle
// keeps its open and first volume, and takes the wider high and low and the
// newer close and volume.
func (r *LiveCandlesPostgresRepository) UpsertLiveCandles(ctx context.Context, candles []LiveCandle) error {
	if len(candles) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("live candles: begin: %w", err)
	}
	defer tx.Rollback()
	for start := 0; start < len(candles); start += liveCandleChunk {
		if err := upsertLiveCandles(ctx, tx, candles[start:min(start+liveCandleChunk, len(candles))]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("live candles: commit: %w", err)
	}
	return nil
}

func upsertLiveCandles(ctx context.Context, tx *sql.Tx, candles []LiveCandle) error {
	var query strings.Builder
	query.WriteString("INSERT INTO live_data (it, id, i, t, o, h, l, c, v, v0) VALUES ")
	args := make([]any, 0, len(candles)*10)
	for index, candle := range candles {
		if index > 0 {
			query.WriteString(", ")
		}
		base := index * 10
		fmt.Fprintf(&query, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10)
		// live_data keeps timezone-naive exchange clocks, like historical_data.
		minute := candle.Minute.In(models.ExchangeLocation).Format("2006-01-02 15:04:05")
		args = append(args, int64(candle.Token), string(candle.InstrumentID), string(models.CandleInterval1m), minute,
			candle.Open, candle.High, candle.Low, candle.Close, candle.LastVolume-candle.FirstVolume, candle.FirstVolume)
	}
	// EXCLUDED.v + EXCLUDED.v0 is the newest cumulative volume.
	query.WriteString(` ON CONFLICT (id, i, t) DO UPDATE SET
		h = GREATEST(live_data.h, EXCLUDED.h),
		l = LEAST(live_data.l, EXCLUDED.l),
		c = EXCLUDED.c,
		v = EXCLUDED.v + EXCLUDED.v0 - live_data.v0`)
	if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
		return fmt.Errorf("live candles: upsert: %w", err)
	}
	return nil
}
