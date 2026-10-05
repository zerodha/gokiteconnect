package repository

import (
	"database/sql"
	"reflect"
	"testing"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/models"
)

func TestEquivalentSQLMatchesGoNormalization(t *testing.T) {
	nearExpiry := instrumentDate(2026, time.June, 25)
	farExpiry := instrumentDate(2026, time.July, 30)
	source := kiteconnect.Instruments{
		rawInstrument(256265, " nse ", "nifty 50", "NIFTY 50", "indices", "eq", time.Time{}, 0),
		rawInstrument(408065, "NSE", "infy", " Infosys ", "nse", "eq", time.Time{}, 0),
		rawInstrument(1004, "BSE", "sensex", "SENSEX", "indices", "eq", time.Time{}, 0),
		rawInstrument(2002, "NFO", "nifty26julfut", "nifty", "nfo-fut", "fut", farExpiry, 0),
		rawInstrument(2001, "NFO", "nifty26junfut", "nifty", "nfo-fut", "fut", nearExpiry, 0),
		rawInstrument(3001, "NFO", "nifty26jun20000ce", "nifty", "nfo-opt", "ce", nearExpiry, 20000),
		rawInstrument(3002, "NFO", "nifty26jun20000pe", "nifty", "nfo-opt", "pe", nearExpiry, 20000),
		rawInstrument(4001, "BFO", "sensex26jun80000ce", "sensex", "bfo-opt", "ce", nearExpiry, 80000),
		rawInstrument(5001, "MCX", "crudeoil26junfut", "crudeoil", "mcx-fut", "fut", nearExpiry, 0),
	}

	want, err := source.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE source_instruments (
			instrument_token BIGINT,
			exchange_token INTEGER,
			tradingsymbol VARCHAR,
			name VARCHAR,
			expiry DATE,
			strike DOUBLE,
			tick_size DOUBLE,
			lot_size DOUBLE,
			instrument_type VARCHAR,
			segment VARCHAR,
			exchange VARCHAR
		)
	`); err != nil {
		t.Fatalf("create source_instruments: %v", err)
	}

	insert, err := db.Prepare(`INSERT INTO source_instruments VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare source insert: %v", err)
	}
	for _, instrument := range source {
		var expiry any
		if !instrument.Expiry.IsZero() {
			expiry = dateKey(instrument.Expiry.Time)
		}
		if _, err := insert.Exec(
			instrument.InstrumentToken,
			instrument.ExchangeToken,
			instrument.Tradingsymbol,
			instrument.Name,
			expiry,
			instrument.StrikePrice,
			instrument.TickSize,
			instrument.LotSize,
			instrument.InstrumentType,
			instrument.Segment,
			instrument.Exchange,
		); err != nil {
			t.Fatalf("insert source instrument: %v", err)
		}
	}
	if err := insert.Close(); err != nil {
		t.Fatalf("close source insert: %v", err)
	}

	rows, err := db.Query(equivalentSQL)
	if err != nil {
		t.Fatalf("equivalentSQL query error = %v", err)
	}
	defer rows.Close()
	got := make(map[models.InstrumentID]models.Instrument, len(source))
	for rows.Next() {
		instrument, err := scanNormalizedInstrument(rows)
		if err != nil {
			t.Fatalf("scan equivalentSQL row: %v", err)
		}
		got[instrument.ID] = instrument
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("equivalentSQL rows error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("equivalentSQL returned %d rows, want %d", len(got), len(want))
	}
	for _, instrument := range want {
		if !reflect.DeepEqual(got[instrument.ID], instrument) {
			t.Errorf("equivalentSQL instrument %s = %#v, want %#v", instrument.ID, got[instrument.ID], instrument)
		}
	}
}
func rawInstrument(token int, exchange, symbol, name, segment, instrumentType string, expiry time.Time, strike float64) kiteconnect.Instrument {
	return kiteconnect.Instrument{
		InstrumentToken: token,
		ExchangeToken:   token + 10,
		Tradingsymbol:   symbol,
		Name:            name,
		Expiry:          models.Time{Time: expiry},
		StrikePrice:     strike,
		TickSize:        0.05,
		LotSize:         50,
		InstrumentType:  instrumentType,
		Segment:         segment,
		Exchange:        exchange,
	}
}
func scanNormalizedInstrument(rows *sql.Rows) (models.Instrument, error) {
	var (
		instrument   models.Instrument
		name         sql.NullString
		expiry       sql.NullTime
		expiryNumber sql.NullInt64
		underlyingID sql.NullString
	)
	err := rows.Scan(
		&instrument.ID,
		&instrument.Exchange,
		&instrument.TradingSymbol,
		&instrument.InstrumentToken,
		&instrument.ExchangeToken,
		&name,
		&instrument.DisplayName,
		&instrument.SearchString,
		&expiry,
		&expiryNumber,
		&instrument.Strike,
		&instrument.TickSize,
		&instrument.LotSize,
		&instrument.InstrumentType,
		&instrument.Segment,
		&instrument.IsFO,
		&underlyingID,
		&instrument.UnderlyingIsListed,
		&instrument.OptionsCount,
		&instrument.FuturesCount,
	)
	if err != nil {
		return models.Instrument{}, err
	}
	if name.Valid {
		instrument.Name = stringPointer(name.String)
	}
	if expiry.Valid {
		instrument.Expiry = timePointer(normalizeDate(expiry.Time))
	}
	if expiryNumber.Valid {
		instrument.ExpiryNumber = intPointer(int(expiryNumber.Int64))
	}
	if underlyingID.Valid {
		id := models.InstrumentID(underlyingID.String)
		instrument.UnderlyingID = &id
	}
	return instrument, nil
}

// equivalentSQL is the DuckDB equivalent of kiteconnect.Instruments.Normalize
// for a source_instruments relation containing the Kite instrument-master
// columns. Normalization stays in Go so refreshing never depends on DuckDB
// reading or validating external data; this query documents the transformation.
const equivalentSQL = `
WITH normalized_source AS (
	SELECT
		upper(trim(exchange)) AS exchange,
		upper(trim(tradingsymbol)) AS trading_symbol,
		instrument_token,
		CAST(exchange_token AS VARCHAR) AS exchange_token,
		nullif(trim(name), '') AS name,
		CAST(expiry AS DATE) AS expiry,
		strike,
		tick_size,
		lot_size,
		upper(trim(segment)) AS segment,
		upper(trim(instrument_type)) AS source_instrument_type
	FROM source_instruments
),
typed_source AS (
	SELECT
		*,
		CASE
			WHEN segment = 'INDICES' THEN 'INDICES'
			ELSE source_instrument_type
		END AS instrument_type
	FROM normalized_source
),
raw AS (
	SELECT
		exchange || ':' || trading_symbol AS id,
		exchange,
		trading_symbol,
		instrument_token,
		exchange_token,
		name,
		expiry,
		strike,
		tick_size,
		CAST(lot_size AS INTEGER) AS lot_size,
		instrument_type,
		segment,
		instrument_type IN ('CE', 'PE', 'FUT') AS is_fo,
		CASE
			WHEN instrument_type NOT IN ('CE', 'PE', 'FUT') THEN NULL
			WHEN upper(name) = 'NIFTY' THEN 'NSE:NIFTY 50'
			WHEN upper(name) = 'BANKNIFTY' THEN 'NSE:NIFTY BANK'
			WHEN upper(name) = 'MIDCPNIFTY' THEN 'NSE:NIFTY MID SELECT'
			WHEN upper(name) = 'FINNIFTY' THEN 'NSE:NIFTY FIN SERVICE'
			WHEN exchange = 'NFO' THEN 'NSE:' || upper(name)
			WHEN exchange = 'BFO' THEN 'BSE:' || upper(name)
			ELSE exchange || ':' || upper(name)
		END AS underlying_id
	FROM typed_source
),
expiry_dates AS (
	SELECT DISTINCT underlying_id, expiry
	FROM raw
	WHERE underlying_id IS NOT NULL AND expiry IS NOT NULL
),
expiry_numbers AS (
	SELECT
		underlying_id,
		expiry,
		row_number() OVER (
			PARTITION BY underlying_id
			ORDER BY expiry
		) - 1 AS expiry_number
	FROM expiry_dates
),
contract_counts AS (
	SELECT
		underlying_id,
		count(*) FILTER (WHERE instrument_type IN ('CE', 'PE')) AS options_count,
		count(*) FILTER (WHERE instrument_type = 'FUT') AS futures_count
	FROM raw
	WHERE underlying_id IS NOT NULL
	GROUP BY underlying_id
),
enriched AS (
	SELECT
		raw.*,
		expiry_numbers.expiry_number,
		raw.underlying_id IS NOT NULL
			AND EXISTS (
				SELECT 1
				FROM raw AS listed
				WHERE listed.id = raw.underlying_id
			) AS underlying_is_listed,
		coalesce(contract_counts.options_count, 0)::INTEGER AS options_count,
		coalesce(contract_counts.futures_count, 0)::INTEGER AS futures_count,
		CASE
			WHEN raw.instrument_type = 'FUT' THEN concat_ws(' ',
				coalesce(raw.name, raw.trading_symbol),
				strftime(raw.expiry, '%d %b %Y'),
				'Futures'
			)
			WHEN raw.instrument_type IN ('CE', 'PE') THEN concat_ws(' ',
				coalesce(raw.name, raw.trading_symbol),
				strftime(raw.expiry, '%d %b %Y'),
				CASE
					WHEN raw.strike = trunc(raw.strike)
						THEN CAST(CAST(raw.strike AS BIGINT) AS VARCHAR)
					ELSE CAST(raw.strike AS VARCHAR)
				END,
				CASE raw.instrument_type
					WHEN 'CE' THEN 'Call'
					WHEN 'PE' THEN 'Put'
				END
			)
			ELSE raw.trading_symbol
		END AS display_name
	FROM raw
	LEFT JOIN expiry_numbers
		ON raw.underlying_id = expiry_numbers.underlying_id
		AND raw.expiry = expiry_numbers.expiry
	LEFT JOIN contract_counts
		ON coalesce(raw.underlying_id, raw.id) = contract_counts.underlying_id
),
catalog AS (
	SELECT
		*,
		regexp_replace(
			trim(concat_ws(' ',
				display_name,
				exchange,
				CAST(instrument_token AS VARCHAR),
				id,
				trading_symbol,
				instrument_type,
				segment,
				name,
				underlying_id,
				CASE instrument_type
					WHEN 'CE' THEN 'CE CALL'
					WHEN 'PE' THEN 'PE PUT'
					WHEN 'FUT' THEN 'FUT FUTURES'
				END
			)),
			'\s+',
			' ',
			'g'
		) AS search_string
	FROM enriched
)
SELECT
	id,
	exchange,
	trading_symbol,
	instrument_token,
	exchange_token,
	name,
	display_name,
	search_string,
	expiry,
	CAST(expiry_number AS INTEGER) AS expiry_number,
	strike,
	tick_size,
	lot_size,
	instrument_type,
	segment,
	is_fo,
	underlying_id,
	underlying_is_listed,
	options_count,
	futures_count
FROM catalog;
`
