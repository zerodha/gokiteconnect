package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
	duckdb "github.com/duckdb/duckdb-go/v2"
)

const defaultCandleStart = "2020-01-01 00:00:00"

// CandlesDuckDBRepository stores candles in DuckDB, in the historical_data
// table, and reads them merged with live candles from Postgres when a live
// data URI is given.
type CandlesDuckDBRepository struct {
	db               *sql.DB
	postgresURI      string
	liveDataAttached bool
	now              func() time.Time
}

// NewCandlesDuckDBRepository opens the candles database at databasePath,
// creating it if needed. liveDataURI is a Postgres URI whose live_data
// table holds live candles, merged into every read; empty means none.
func NewCandlesDuckDBRepository(ctx context.Context, databasePath, liveDataURI string) (*CandlesDuckDBRepository, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", models.ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, fmt.Errorf("%w: database path is required", models.ErrInvalidInput)
	}
	if databasePath != ":memory:" {
		info, err := os.Stat(databasePath)
		switch {
		case err == nil && info.IsDir():
			return nil, fmt.Errorf("%w: database path is a directory", models.ErrInvalidInput)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("candles: inspect database path: %w", err)
		}
	}

	db, err := sql.Open("duckdb", databasePath)
	if err != nil {
		return nil, fmt.Errorf("candles: open DuckDB: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("candles: connect to DuckDB: %w", err)
	}
	repo := &CandlesDuckDBRepository{
		db:          db,
		postgresURI: strings.TrimSpace(liveDataURI),
		now:         time.Now,
	}
	if err := repo.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repo, nil
}

// Close closes the database.
func (r *CandlesDuckDBRepository) Close() error {
	return r.db.Close()
}

type derivativeContract struct {
	ID           models.InstrumentID
	UnderlyingID models.InstrumentID
	Expiry       time.Time
	Type         string
	Strike       float64
}

var (
	monthlyOptionContract = regexp.MustCompile(`^(.+?)(\d{2})(JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC)(\d+(?:\.\d+)?)(CE|PE)$`)
	weeklyOptionContract  = regexp.MustCompile(`^(.+?)(\d{2})([1-9OND])(\d{2})(\d+(?:\.\d+)?)(CE|PE)$`)
	monthlyFutureContract = regexp.MustCompile(`^(.+?)(\d{2})(JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC)FUT$`)
)

var contractMonths = map[string]time.Month{
	"JAN": time.January,
	"FEB": time.February,
	"MAR": time.March,
	"APR": time.April,
	"MAY": time.May,
	"JUN": time.June,
	"JUL": time.July,
	"AUG": time.August,
	"SEP": time.September,
	"OCT": time.October,
	"NOV": time.November,
	"DEC": time.December,
	"1":   time.January,
	"2":   time.February,
	"3":   time.March,
	"4":   time.April,
	"5":   time.May,
	"6":   time.June,
	"7":   time.July,
	"8":   time.August,
	"9":   time.September,
	"O":   time.October,
	"N":   time.November,
	"D":   time.December,
}

// GetExpiryPrice returns final settlement prices for expired NFO contracts.
// Futures settle at the underlying close; calls and puts settle at intrinsic
// value. IDs that are not expired, cannot be decoded, or have no underlying
// candle at expiry are omitted from the result.
func (r *CandlesDuckDBRepository) GetExpiryPrice(ctx context.Context, contractIDs ...models.InstrumentID) (map[models.InstrumentID]models.ExpirySettlement, error) {
	result := make(map[models.InstrumentID]models.ExpirySettlement)
	if len(contractIDs) == 0 {
		return result, nil
	}

	today := exchangeDate(r.now())
	contracts := make([]derivativeContract, 0, len(contractIDs))
	seen := make(map[models.InstrumentID]bool)
	for _, contractID := range contractIDs {
		contract, ok := parseDerivativeContract(string(contractID))
		if !ok || !contract.Expiry.Before(today) || seen[contract.ID] {
			continue
		}
		contracts = append(contracts, contract)
		seen[contract.ID] = true
	}
	if len(contracts) == 0 {
		return result, nil
	}

	valueRows := make([]string, 0, len(contracts))
	args := make([]any, 0, len(contracts)*3)
	contractsByID := make(map[models.InstrumentID]derivativeContract, len(contracts))
	for _, contract := range contracts {
		valueRows = append(valueRows, "(?, ?, ?)")
		args = append(args, string(contract.ID), string(contract.UnderlyingID), contract.Expiry.Format("2006-01-02"))
		contractsByID[contract.ID] = contract
	}

	query := fmt.Sprintf(`
		WITH requests(contract_id, underlying_id, expiry) AS (
			VALUES %s
		), ranked AS (
			SELECT
				requests.contract_id,
				candles.c,
				ROW_NUMBER() OVER (
					PARTITION BY requests.contract_id
					ORDER BY candles.t DESC,
						CASE WHEN candles.i = '1m' THEN 0 ELSE 1 END
				) AS row_number
			FROM requests
			JOIN historical_data AS candles
				ON UPPER(candles.id) = UPPER(requests.underlying_id)
			WHERE candles.t::DATE <= requests.expiry::DATE
				AND candles.t::DATE >= requests.expiry::DATE - INTERVAL 7 DAY
				AND candles.c > 0
		)
		SELECT contract_id, c
		FROM ranked
		WHERE row_number = 1
	`, strings.Join(valueRows, ","))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var contractID string
		var underlyingClose float64
		if err := rows.Scan(&contractID, &underlyingClose); err != nil {
			return nil, err
		}
		contract := contractsByID[models.InstrumentID(contractID)]
		settlement := models.ExpirySettlement{
			ExpiresAt: derivativeExpiryTime(contract.Expiry),
		}
		switch contract.Type {
		case "FUT":
			settlement.Price = underlyingClose
		case "CE":
			settlement.Price = math.Max(underlyingClose-contract.Strike, 0)
		case "PE":
			settlement.Price = math.Max(contract.Strike-underlyingClose, 0)
		}
		result[contract.ID] = settlement
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func derivativeExpiryTime(expiryDate time.Time) time.Time {
	local := expiryDate.In(models.ExchangeLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 15, 30, 0, 0, models.ExchangeLocation)
}

func parseDerivativeContract(contractID string) (derivativeContract, bool) {
	id := strings.ToUpper(strings.TrimSpace(contractID))
	exchange, symbol, found := strings.Cut(id, ":")
	if !found || exchange != "NFO" || symbol == "" {
		return derivativeContract{}, false
	}

	if match := monthlyFutureContract.FindStringSubmatch(symbol); match != nil {
		expiry, ok := monthlyNFOExpiry(match[2], match[3])
		if !ok {
			return derivativeContract{}, false
		}
		return derivativeContract{
			ID:           models.InstrumentID(id),
			UnderlyingID: nfoUnderlyingID(match[1]),
			Expiry:       expiry,
			Type:         "FUT",
		}, true
	}

	if match := monthlyOptionContract.FindStringSubmatch(symbol); match != nil {
		expiry, ok := monthlyNFOExpiry(match[2], match[3])
		if !ok {
			return derivativeContract{}, false
		}
		strike, err := strconv.ParseFloat(match[4], 64)
		if err != nil {
			return derivativeContract{}, false
		}
		return derivativeContract{
			ID:           models.InstrumentID(id),
			UnderlyingID: nfoUnderlyingID(match[1]),
			Expiry:       expiry,
			Type:         match[5],
			Strike:       strike,
		}, true
	}

	if match := weeklyOptionContract.FindStringSubmatch(symbol); match != nil {
		year, ok := contractYear(match[2])
		month, monthOK := contractMonths[match[3]]
		day, dayErr := strconv.Atoi(match[4])
		strike, strikeErr := strconv.ParseFloat(match[5], 64)
		if !ok || !monthOK || dayErr != nil || strikeErr != nil {
			return derivativeContract{}, false
		}
		expiry := time.Date(year, month, day, 0, 0, 0, 0, models.ExchangeLocation)
		if expiry.Month() != month {
			return derivativeContract{}, false
		}
		return derivativeContract{
			ID:           models.InstrumentID(id),
			UnderlyingID: nfoUnderlyingID(match[1]),
			Expiry:       expiry,
			Type:         match[6],
			Strike:       strike,
		}, true
	}

	return derivativeContract{}, false
}

func monthlyNFOExpiry(twoDigitYear string, monthCode string) (time.Time, bool) {
	year, ok := contractYear(twoDigitYear)
	month, monthOK := contractMonths[monthCode]
	if !ok || !monthOK {
		return time.Time{}, false
	}

	weekday := time.Thursday
	transition := time.Date(2025, time.September, 1, 0, 0, 0, 0, models.ExchangeLocation)
	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, models.ExchangeLocation)
	if !monthStart.Before(transition) {
		weekday = time.Tuesday
	}
	return lastWeekdayOfMonth(year, month, weekday), true
}

func contractYear(twoDigitYear string) (int, bool) {
	value, err := strconv.Atoi(twoDigitYear)
	if err != nil {
		return 0, false
	}
	return 2000 + value, true
}

func lastWeekdayOfMonth(year int, month time.Month, weekday time.Weekday) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, models.ExchangeLocation)
	offset := (int(lastDay.Weekday()) - int(weekday) + 7) % 7
	return lastDay.AddDate(0, 0, -offset)
}

func exchangeDate(value time.Time) time.Time {
	value = value.In(models.ExchangeLocation)
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, models.ExchangeLocation)
}

func nfoUnderlyingID(symbol string) models.InstrumentID {
	switch symbol {
	case "NIFTY":
		return "NSE:NIFTY 50"
	case "BANKNIFTY":
		return "NSE:NIFTY BANK"
	case "FINNIFTY":
		return "NSE:NIFTY FIN SERVICE"
	case "MIDCPNIFTY":
		return "NSE:NIFTY MID SELECT"
	case "NIFTYNXT50":
		return "NSE:NIFTY NEXT 50"
	default:
		return models.InstrumentID("NSE:" + symbol)
	}
}

// GetCandles returns the candles params selects, ordered by instrument and
// time. Intraday intervals are built from the stored 1m candles, and 1W from
// the stored 1d candles.
func (r *CandlesDuckDBRepository) GetCandles(ctx context.Context, params models.GetCandlesParams) ([]candles.Candle, error) {
	if params.Warmup < 0 {
		return nil, fmt.Errorf("%w: warmup must be non-negative", models.ErrInvalidInput)
	}
	if params.Warmup > 0 {
		return r.getCandlesWithWarmup(ctx, params)
	}
	fromPart, wherePart, singleInterval, err := r.buildGetCandlesQuery(params)
	if err != nil {
		return nil, err
	}

	timestampExpr := "hc.t"
	if singleInterval == models.CandleInterval1d {
		timestampExpr = "hc.t::DATE"
	}

	query := fmt.Sprintf(`
		SELECT
			hc.id,
			hc.it,
			hc.i,
			%s AS t,
			hc.o,
			hc.h,
			hc.l,
			hc.c,
			hc.v
		FROM
			%s
		WHERE
			%s
		ORDER BY
			id, t
	`, timestampExpr, fromPart, wherePart)

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCandles(rows)
}

func (r *CandlesDuckDBRepository) getCandlesWithWarmup(ctx context.Context, params models.GetCandlesParams) ([]candles.Candle, error) {
	intervals := params.Intervals
	if len(intervals) == 0 {
		intervals = []models.CandleInterval{models.CandleInterval15m}
	}
	if len(intervals) != 1 {
		return nil, fmt.Errorf("%w: warmup candle queries require exactly one interval", models.ErrInvalidInput)
	}
	interval := intervals[0]
	if !interval.IsValid() {
		return nil, fmt.Errorf("%w: unsupported interval %q", models.ErrInvalidInput, interval)
	}
	start := params.Start
	if start.IsZero() {
		start = parseDefaultStart()
	}
	end := params.End
	if end.IsZero() {
		end = r.now()
	}
	if !end.After(start) {
		return nil, fmt.Errorf("%w: end must be after start", models.ErrInvalidInput)
	}

	idsFilter := instrumentIDsSQLFilter("hc.id", params.InstrumentIDs)
	var candidatesSQL string
	if interval == models.CandleInterval1m {
		conditions := []string{
			fmt.Sprintf("hc.t < %s", quoteLiteral(formatTimestamp(end))),
			fmt.Sprintf("hc.i = %s", quoteLiteral(string(interval))),
		}
		if idsFilter != "" {
			conditions = append(conditions, idsFilter)
		}
		candidatesSQL = fmt.Sprintf(`
			SELECT hc.id, hc.it, hc.i, hc.t, hc.o, hc.h, hc.l, hc.c, hc.v
			FROM %s hc
			WHERE %s
		`, r.histUnionLiveSQL(), strings.Join(conditions, " AND "))
	} else {
		candidatesSQL = fmt.Sprintf("SELECT * FROM %s hc", r.candlesSQLForInstruments(interval, time.Time{}, end, params.InstrumentIDs))
	}
	timestampExpr := "t"
	if interval == models.CandleInterval1d {
		timestampExpr = "t::DATE"
	}

	query := fmt.Sprintf(`
		WITH candidates AS (
			%s
		), ranked AS (
			SELECT
				*,
				ROW_NUMBER() OVER (
					PARTITION BY id, t < %s
					ORDER BY t DESC
				) AS range_rank
			FROM candidates
		)
		SELECT id, it, i, %s AS t, o, h, l, c, v
		FROM ranked
		WHERE
			(t >= %s AND t < %s)
			OR (t < %s AND range_rank <= %d)
		ORDER BY id, t
	`, candidatesSQL,
		quoteLiteral(formatTimestamp(start)), timestampExpr,
		quoteLiteral(formatTimestamp(start)), quoteLiteral(formatTimestamp(end)),
		quoteLiteral(formatTimestamp(start)), params.Warmup,
	)

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCandles(rows)
}

// InsertHistoricalCandles stores candles, which must each name their
// instrument, token and interval. Timestamps are stored as exchange clocks.
// A candle already stored for the same token, interval and time is an error.
func (r *CandlesDuckDBRepository) InsertHistoricalCandles(ctx context.Context, rows []candles.Candle) error {
	return r.writeHistoricalCandles(ctx, "INSERT", rows)
}

// UpsertHistoricalCandles stores candles like InsertHistoricalCandles, but
// replaces a candle already stored for the same token, interval and time.
func (r *CandlesDuckDBRepository) UpsertHistoricalCandles(ctx context.Context, rows []candles.Candle) error {
	return r.writeHistoricalCandles(ctx, "INSERT OR REPLACE", rows)
}

// writeHistoricalCandles appends the candles to a staging table and moves
// them into historical_data with one INSERT. A statement binding every
// candle as parameters is planned row by row, which takes seconds per
// sync's worth of 1m candles.
func (r *CandlesDuckDBRepository) writeHistoricalCandles(ctx context.Context, verb string, rows []candles.Candle) error {
	if len(rows) == 0 {
		return nil
	}

	connection, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("candles: reserve write connection: %w", err)
	}
	defer connection.Close()

	if _, err := connection.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		return fmt.Errorf("candles: begin write: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if _, err := connection.ExecContext(ctx, `
		CREATE OR REPLACE TEMP TABLE candle_staging (
			it BIGINT, id TEXT, i TEXT, t TIMESTAMP,
			o DOUBLE, h DOUBLE, l DOUBLE, c DOUBLE, v BIGINT, oi BIGINT
		)`); err != nil {
		return fmt.Errorf("candles: create staging table: %w", err)
	}
	if err := appendCandles(ctx, connection, rows); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, verb+` INTO historical_data (it, id, i, t, o, h, l, c, v, oi)
		SELECT it, id, i, t, o, h, l, c, v, oi FROM candle_staging`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "DROP TABLE candle_staging"); err != nil {
		return fmt.Errorf("candles: drop staging table: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("candles: commit write: %w", err)
	}
	committed = true
	return nil
}

func appendCandles(ctx context.Context, connection *sql.Conn, rows []candles.Candle) error {
	return connection.Raw(func(driverConnection any) error {
		rawConnection, ok := driverConnection.(driver.Conn)
		if !ok {
			return errors.New("candles: DuckDB connection does not expose a driver connection")
		}
		appender, err := duckdb.NewAppender(rawConnection, "temp", "main", "candle_staging")
		if err != nil {
			return fmt.Errorf("candles: create staging appender: %w", err)
		}
		closed := false
		defer func() {
			if !closed {
				_ = appender.Close()
			}
		}()

		for index, candle := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := appender.AppendRow(
				candle.InstrumentToken,
				string(candle.InstrumentID),
				string(candle.Interval),
				exchangeClockUTC(candle.Timestamp),
				candle.Open,
				candle.High,
				candle.Low,
				candle.Close,
				int64(candle.Volume),
				int64(candle.OI),
			); err != nil {
				return fmt.Errorf("candles: append candle %d (%s %s): %w", index+1, candle.InstrumentID, formatTimestamp(models.ExchangeClockTime(candle.Timestamp)), err)
			}
		}
		if err := appender.Close(); err != nil {
			return fmt.Errorf("candles: finish staging append: %w", err)
		}
		closed = true
		return nil
	})
}

// exchangeClockUTC is the timestamp's exchange clock reading as a UTC time,
// which the Appender stores unchanged in a TIMESTAMP column.
func exchangeClockUTC(ts time.Time) time.Time {
	clock := models.ExchangeClockTime(ts)
	return time.Date(clock.Year(), clock.Month(), clock.Day(), clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(), time.UTC)
}

// GetRecentCandles returns the last limit candles of an instrument, oldest
// first.
func (r *CandlesDuckDBRepository) GetRecentCandles(ctx context.Context, instrumentID models.InstrumentID, interval models.CandleInterval, limit int) ([]candles.Candle, error) {
	if limit <= 0 {
		return []candles.Candle{}, nil
	}
	query := fmt.Sprintf(`
		SELECT
			hc.id,
			hc.it,
			hc.i,
			hc.t,
			hc.o,
			hc.h,
			hc.l,
			hc.c,
			hc.v
		FROM
			%s hc
		WHERE
			hc.id = %s
			AND hc.i = %s
		ORDER BY
			hc.t DESC
		LIMIT %d
	`, r.histUnionLiveSQL(), quoteLiteral(strings.TrimSpace(string(instrumentID))), quoteLiteral(string(interval)), limit)

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	recent, err := scanCandles(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	return recent, nil
}

// GetRecentCandlesBatch returns the last limit candles of each instrument,
// ordered by instrument and time.
func (r *CandlesDuckDBRepository) GetRecentCandlesBatch(
	ctx context.Context,
	instrumentIDs []models.InstrumentID,
	interval models.CandleInterval,
	limit int,
) ([]candles.Candle, error) {
	if limit <= 0 {
		return []candles.Candle{}, nil
	}
	filter := instrumentIDsSQLFilter("hc.id", instrumentIDs)
	if filter == "" {
		return []candles.Candle{}, nil
	}
	// ARG_MAX keeps only limit rows per instrument while scanning. A windowed
	// ROW_NUMBER must retain and sort every matching historical candle first,
	// which makes startup memory proportional to the complete candle history.
	query := fmt.Sprintf(`
		WITH recent AS (
			SELECT UNNEST(
				ARG_MAX(
					STRUCT_PACK(
						id := hc.id,
						it := hc.it,
						i := hc.i,
						t := hc.t,
						o := hc.o,
						h := hc.h,
						l := hc.l,
						c := hc.c,
						v := hc.v
					),
					hc.t,
					%d
				),
				recursive := true
			)
			FROM %s hc
			WHERE %s AND hc.i = %s
			GROUP BY hc.id
		)
		SELECT id, it, i, t, o, h, l, c, v
		FROM recent
		ORDER BY id, t
	`, limit, r.histUnionLiveSQL(), filter, quoteLiteral(string(interval)))
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCandles(rows)
}

// DeleteCandlesByInstrumentAndInterval deletes an instrument's historical
// and live candles of interval.
func (r *CandlesDuckDBRepository) DeleteCandlesByInstrumentAndInterval(ctx context.Context, instrumentID models.InstrumentID, interval models.CandleInterval) error {
	id := strings.TrimSpace(string(instrumentID))
	if id == "" {
		return nil
	}
	idLiteral := quoteLiteral(id)
	intervalLiteral := quoteLiteral(string(interval))
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM historical_data WHERE id = %s AND i = %s",
		idLiteral,
		intervalLiteral,
	)); err != nil {
		return err
	}
	if r.liveDataAttached {
		if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
			"DELETE FROM ld.live_data WHERE id = %s AND i = %s",
			idLiteral,
			intervalLiteral,
		)); err != nil {
			return err
		}
	}
	return nil
}

// FlushLiveCandlesByInstrumentAndInterval deletes an instrument's live
// candles of interval.
func (r *CandlesDuckDBRepository) FlushLiveCandlesByInstrumentAndInterval(ctx context.Context, instrumentID models.InstrumentID, interval models.CandleInterval) error {
	if !r.liveDataAttached {
		return nil
	}
	id := strings.TrimSpace(string(instrumentID))
	if id == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM ld.live_data WHERE id = %s AND i = %s",
		quoteLiteral(id),
		quoteLiteral(string(interval)),
	))
	return err
}

// FlushLiveCandles deletes every live candle.
func (r *CandlesDuckDBRepository) FlushLiveCandles(ctx context.Context) error {
	if !r.liveDataAttached {
		return nil
	}
	_, err := r.db.ExecContext(ctx, "DELETE FROM ld.live_data")
	return err
}

// DropInstrumentsByID deletes every candle of the instruments.
func (r *CandlesDuckDBRepository) DropInstrumentsByID(ctx context.Context, ids []models.InstrumentID) error {
	filter := instrumentIDsSQLFilter("id", ids)
	if filter == "" {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "DELETE FROM historical_data WHERE "+filter); err != nil {
		return err
	}
	if r.liveDataAttached {
		if _, err := r.db.ExecContext(ctx, "DELETE FROM ld.live_data WHERE "+filter); err != nil {
			return err
		}
	}
	return nil
}

// GetAvailableInstruments returns the instruments with candles, by token.
func (r *CandlesDuckDBRepository) GetAvailableInstruments(ctx context.Context) (map[int64]models.InstrumentID, error) {
	query := `
		SELECT DISTINCT id, it
		FROM historical_data
	`
	if r.liveDataAttached {
		query = `
			SELECT DISTINCT id, it
			FROM (
				SELECT id, it FROM historical_data
				UNION ALL
				SELECT id, it FROM ld.live_data
			)
		`
	}

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]models.InstrumentID)
	for rows.Next() {
		var id string
		var token int64
		if err := rows.Scan(&id, &token); err != nil {
			return nil, err
		}
		out[token] = models.InstrumentID(id)
	}
	return out, rows.Err()
}

// GetDataAvailability returns the range of stored candles of each
// instrument and interval; no instruments means all of them.
func (r *CandlesDuckDBRepository) GetDataAvailability(ctx context.Context, instrumentIDs []models.InstrumentID) ([]models.CandleAvailability, error) {
	if !r.liveDataAttached {
		return r.getHistoricalDataAvailabilityOnly(ctx, instrumentIDs)
	}
	historicalAggregate := dataAvailabilityAggregateSQL("historical_data", instrumentIDs)
	liveAggregate := dataAvailabilityAggregateSQL("ld.live_data", instrumentIDs)

	query := `
		WITH hda AS (
			` + historicalAggregate + `
		), lda AS (
			` + liveAggregate + `
		)
		SELECT
			hda.id,
			hda.it,
			hda.i,
			hda.start AS hist_start,
			hda.end AS hist_end,
			lda.start AS live_start,
			lda.end AS live_end
		FROM hda
		LEFT JOIN lda ON hda.it = lda.it AND hda.i = lda.i
		ORDER BY hda.id
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.CandleAvailability, 0)
	for rows.Next() {
		var row models.CandleAvailability
		var liveStart sql.NullTime
		var liveEnd sql.NullTime

		if err := rows.Scan(
			&row.InstrumentID,
			&row.InstrumentToken,
			&row.Interval,
			&row.HistStart,
			&row.HistEnd,
			&liveStart,
			&liveEnd,
		); err != nil {
			return nil, err
		}
		if liveStart.Valid {
			t := liveStart.Time
			row.LiveStart = &t
		}
		if liveEnd.Valid {
			t := liveEnd.Time
			row.LiveEnd = &t
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetLastClosePrice returns today's last close of each instrument.
func (r *CandlesDuckDBRepository) GetLastClosePrice(ctx context.Context) ([]models.LastClosePrice, error) {
	query := fmt.Sprintf(`
		SELECT
			LAST(id) AS id,
			it,
			MAX(t) AS t,
			LAST(c ORDER BY t) AS ltp
		FROM %s
		WHERE i = '1m' AND t::DATE = CURRENT_DATE
		GROUP BY it
	`, r.histUnionLiveSQL())

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.LastClosePrice, 0)
	for rows.Next() {
		var row models.LastClosePrice
		if err := rows.Scan(&row.InstrumentID, &row.InstrumentToken, &row.Timestamp, &row.LTP); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetNthDate returns the nth latest date with historical candles, 0 being
// the latest.
func (r *CandlesDuckDBRepository) GetNthDate(ctx context.Context, n int) (time.Time, error) {
	if n < 0 {
		return time.Time{}, fmt.Errorf("%w: n must be non-negative", models.ErrInvalidInput)
	}

	row := r.db.QueryRowContext(ctx, `
		SELECT DISTINCT t::DATE AS t
		FROM historical_data
		ORDER BY t DESC
		LIMIT 1
		OFFSET ?
	`, n)

	var ts time.Time
	if err := row.Scan(&ts); err != nil {
		return time.Time{}, err
	}
	return ts, nil
}

func scanCandle(row scanner) (candles.Candle, error) {
	var candle candles.Candle
	err := row.Scan(
		&candle.InstrumentID,
		&candle.InstrumentToken,
		&candle.Interval,
		&candle.Timestamp,
		&candle.Open,
		&candle.High,
		&candle.Low,
		&candle.Close,
		&candle.Volume,
	)
	if err != nil {
		return candles.Candle{}, err
	}
	candle.Timestamp = models.ExchangeClockTime(candle.Timestamp)
	return candle, nil
}

func scanCandles(rows *sql.Rows) ([]candles.Candle, error) {
	out := make([]candles.Candle, 0)
	for rows.Next() {
		candle, err := scanCandle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, candle)
	}
	return out, rows.Err()
}

func (r *CandlesDuckDBRepository) buildGetCandlesQuery(params models.GetCandlesParams) (fromPart string, wherePart string, singleInterval models.CandleInterval, err error) {
	start := params.Start
	if start.IsZero() {
		start = parseDefaultStart()
	}
	end := params.End
	if end.IsZero() {
		end = r.now()
	}

	intervals := params.Intervals
	if len(intervals) == 0 {
		intervals = []models.CandleInterval{models.CandleInterval15m}
	}
	for _, interval := range intervals {
		if !interval.IsValid() {
			return "", "", "", fmt.Errorf("%w: unsupported interval %q", models.ErrInvalidInput, interval)
		}
	}

	if len(intervals) == 1 {
		singleInterval = intervals[0]
		fromPart = fmt.Sprintf("%s hc", r.candlesSQL(intervals[0], start, end))
	} else {
		for _, interval := range intervals {
			if interval == models.CandleInterval1m {
				return "", "", "", fmt.Errorf("%w: 1m can't be mixed with other intervals", models.ErrInvalidInput)
			}
		}
		parts := make([]string, len(intervals))
		for i, interval := range intervals {
			parts[i] = fmt.Sprintf("SELECT * FROM %s", r.candlesSQL(interval, start, end))
		}
		fromPart = "(" + strings.Join(parts, " UNION ") + ") hc"
	}

	wherePart = "true"
	if filter := instrumentIDsSQLFilter("id", params.InstrumentIDs); filter != "" {
		wherePart = filter
	}

	if singleInterval == models.CandleInterval1m {
		fromPart = fmt.Sprintf("%s hc", r.histUnionLiveSQL())
		timeFilter := fmt.Sprintf("i = '1m' AND t >= %s AND t < %s", quoteLiteral(formatTimestamp(start)), quoteLiteral(formatTimestamp(end)))
		if wherePart == "true" {
			wherePart = timeFilter
		} else {
			wherePart = wherePart + " AND " + timeFilter
		}
	}

	return fromPart, wherePart, singleInterval, nil
}

func (r *CandlesDuckDBRepository) histUnionLiveSQL() string {
	if !r.liveDataAttached {
		return "(SELECT * FROM historical_data)"
	}
	return `
		(
			SELECT *
			FROM historical_data
			UNION ALL BY NAME
			SELECT *
			FROM ld.live_data
		)
	`
}

func (r *CandlesDuckDBRepository) candlesSQL(interval models.CandleInterval, fromTS time.Time, toTS time.Time) string {
	return r.candlesSQLForInstruments(interval, fromTS, toTS, nil)
}

// candlesSQLForInstruments selects an interval's candles in [fromTS, toTS):
// 1m as stored; intraday intervals resampled from 1m in buckets from the
// 09:15 open; 1d as stored, with days after the last stored one resampled
// from 1m, so today's live candles show; and 1W resampled from those days,
// in weeks from Monday.
func (r *CandlesDuckDBRepository) candlesSQLForInstruments(interval models.CandleInterval, fromTS time.Time, toTS time.Time, instrumentIDs []models.InstrumentID) string {
	conditions := []string{
		fmt.Sprintf("t < %s::TIMESTAMP", quoteLiteral(formatTimestamp(toTS))),
	}
	if !fromTS.IsZero() {
		conditions = append(conditions, fmt.Sprintf("t >= %s::TIMESTAMP", quoteLiteral(formatTimestamp(fromTS))))
	}
	idsFilter := instrumentIDsSQLFilter("id", instrumentIDs)
	if idsFilter != "" {
		conditions = append(conditions, idsFilter)
	}
	where := strings.Join(conditions, " AND ")

	switch {
	case interval == models.CandleInterval1m:
		return fmt.Sprintf(`
			(
				SELECT id, it, i, t, o, h, l, c, v
				FROM %s
				WHERE i = '1m' AND %s
			)
		`, r.histUnionLiveSQL(), where)
	case interval.IsIntraday():
		return fmt.Sprintf(`
			(
				SELECT
					LAST(id) AS id,
					it,
					'%s' AS i,
					time_bucket('%s'::INTERVAL, t, '2000-01-01 09:15:00'::TIMESTAMP) AS t,
					FIRST(o ORDER BY t ASC) AS o,
					MAX(h) AS h,
					MIN(l) AS l,
					LAST(c ORDER BY t ASC) AS c,
					SUM(v) AS v
				FROM
					%s
				WHERE
					i = '1m' AND %s
				GROUP BY
					it, time_bucket('%s'::INTERVAL, t, '2000-01-01 09:15:00'::TIMESTAMP)
				ORDER BY
					t ASC
			)
		`, interval, interval, r.histUnionLiveSQL(), where, interval)
	case interval == models.CandleInterval1d:
		return r.dailyCandlesSQL(where, idsFilter)
	default:
		return fmt.Sprintf(`
			(
				SELECT
					LAST(id ORDER BY t ASC) AS id,
					it,
					'%s' AS i,
					time_bucket(INTERVAL 1 WEEK, t) AS t,
					FIRST(o ORDER BY t ASC) AS o,
					MAX(h) AS h,
					MIN(l) AS l,
					LAST(c ORDER BY t ASC) AS c,
					SUM(v) AS v
				FROM
					%s days
				GROUP BY
					it, time_bucket(INTERVAL 1 WEEK, t)
				ORDER BY
					t ASC
			)
		`, interval, r.dailyCandlesSQL(where, idsFilter))
	}
}

// dailyCandlesSQL selects the stored 1d candles matching where, and the
// days after each instrument's last stored day resampled from 1m. Kite's
// day candle closes at the closing auction price, which 1m candles miss,
// so a day is resampled only until its 1d candle is synced.
func (r *CandlesDuckDBRepository) dailyCandlesSQL(where, idsFilter string) string {
	lastDayWhere := "i = '1d'"
	if idsFilter != "" {
		lastDayWhere += " AND " + idsFilter
	}
	return fmt.Sprintf(`
		(
			WITH last_day AS (
				SELECT it, MAX(t) AS t
				FROM %[1]s
				WHERE %[3]s
				GROUP BY it
			)
			SELECT id, it, '1d' AS i, t, o, h, l, c, v
			FROM %[1]s
			WHERE i = '1d' AND %[2]s
			UNION ALL
			SELECT
				LAST(m.id ORDER BY m.t ASC) AS id,
				m.it,
				'1d' AS i,
				m.t::DATE::TIMESTAMP AS t,
				FIRST(m.o ORDER BY m.t ASC) AS o,
				MAX(m.h) AS h,
				MIN(m.l) AS l,
				LAST(m.c ORDER BY m.t ASC) AS c,
				SUM(m.v) AS v
			FROM
				(SELECT * FROM %[1]s WHERE i = '1m' AND %[2]s) m
				LEFT JOIN last_day d ON d.it = m.it
			WHERE
				d.t IS NULL OR m.t >= d.t + INTERVAL 1 DAY
			GROUP BY
				m.it, m.t::DATE
		)
	`, r.histUnionLiveSQL(), where, lastDayWhere)
}

func (r *CandlesDuckDBRepository) migrate(ctx context.Context) error {
	if r.postgresURI != "" {
		attachQuery := fmt.Sprintf("ATTACH %s AS ld (TYPE POSTGRES)", quoteLiteral(r.postgresURI))
		if _, err := r.db.ExecContext(ctx, attachQuery); err != nil {
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "already exists") {
				return fmt.Errorf("candles: attach live data: %w", err)
			}
		}
		r.liveDataAttached = true
	}

	_, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS historical_data (
			it INT,
			id TEXT,
			i TEXT,
			t TIMESTAMP,
			o DECIMAL(10,4),
			h DECIMAL(10,4),
			l DECIMAL(10,4),
			c DECIMAL(10,4),
			v BIGINT,
			oi BIGINT,
			PRIMARY KEY (it, i, t)
		);

		CREATE INDEX IF NOT EXISTS idx_hd_id_i_t ON historical_data(id, i, t);

		ALTER TABLE historical_data ADD COLUMN IF NOT EXISTS oi BIGINT;
	`)
	if err != nil {
		return fmt.Errorf("candles: create historical_data: %w", err)
	}
	return r.widenVolume(ctx)
}

// widenVolume makes an INT volume column BIGINT: a day's volume can pass
// INT's 2.1 billion. DuckDB won't alter a column of an indexed table, so the
// secondary index is dropped and rebuilt, which takes about a minute on a
// table of a few hundred million candles.
func (r *CandlesDuckDBRepository) widenVolume(ctx context.Context) error {
	var volumeType string
	if err := r.db.QueryRowContext(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema = 'main' AND table_name = 'historical_data' AND column_name = 'v'
	`).Scan(&volumeType); err != nil {
		return fmt.Errorf("candles: read volume column type: %w", err)
	}
	if volumeType != "INTEGER" {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, `
		BEGIN TRANSACTION;
		DROP INDEX IF EXISTS idx_hd_id_i_t;
		ALTER TABLE historical_data ALTER COLUMN v TYPE BIGINT;
		CREATE INDEX idx_hd_id_i_t ON historical_data(id, i, t);
		COMMIT;
	`); err != nil {
		_, _ = r.db.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return fmt.Errorf("candles: widen volume column: %w", err)
	}
	return nil
}

func (r *CandlesDuckDBRepository) getHistoricalDataAvailabilityOnly(ctx context.Context, instrumentIDs []models.InstrumentID) ([]models.CandleAvailability, error) {
	historicalAggregate := dataAvailabilityAggregateSQL("historical_data", instrumentIDs)
	rows, err := r.db.QueryContext(ctx, `
		WITH hda AS (
			`+historicalAggregate+`
		)
		SELECT
			hda.id,
			hda.it,
			hda.i,
			hda.start,
			hda.end
		FROM hda
		ORDER BY hda.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.CandleAvailability, 0)
	for rows.Next() {
		var row models.CandleAvailability
		if err := rows.Scan(
			&row.InstrumentID,
			&row.InstrumentToken,
			&row.Interval,
			&row.HistStart,
			&row.HistEnd,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// dataAvailabilityAggregateSQL deliberately applies the instrument predicate
// before grouping. Row syncs request one instrument at a time; filtering after
// this aggregate would rescan the complete candle table for every row in the
// admin progress loop.
func dataAvailabilityAggregateSQL(table string, instrumentIDs []models.InstrumentID) string {
	where := ""
	if filter := instrumentIDsSQLFilter("id", instrumentIDs); filter != "" {
		where = " WHERE " + filter
	}
	return `
		SELECT
			LAST(id) AS id,
			it,
			i,
			MIN(t) AS start,
			MAX(t) AS end
		FROM ` + table + where + `
		GROUP BY it, i
	`
}

// instrumentIDsSQLFilter is "column IN (...)" for the distinct, trimmed
// instrumentIDs, or empty when there are none.
func instrumentIDsSQLFilter(column string, instrumentIDs []models.InstrumentID) string {
	quoted := make([]string, 0, len(instrumentIDs))
	seen := make(map[string]struct{}, len(instrumentIDs))
	for _, instrumentID := range instrumentIDs {
		id := strings.TrimSpace(string(instrumentID))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		quoted = append(quoted, quoteLiteral(id))
	}
	if len(quoted) == 0 {
		return ""
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(quoted, ","))
}

func formatTimestamp(ts time.Time) string {
	return ts.Format("2006-01-02 15:04:05")
}

func parseDefaultStart() time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", defaultCandleStart)
	if err != nil {
		return time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	}
	return parsed
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
