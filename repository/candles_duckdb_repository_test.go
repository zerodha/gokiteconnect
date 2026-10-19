package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
)

func openCandles(t *testing.T) *CandlesDuckDBRepository {
	t.Helper()
	repo, err := NewCandlesDuckDBRepository(context.Background(), filepath.Join(t.TempDir(), "candles.duckdb"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestGetCandlesReturnsExactWarmupPerInstrument(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	from := time.Date(2026, time.August, 19, 9, 20, 0, 0, models.ExchangeLocation)
	rows := make([]candles.Candle, 0, 12)
	for instrumentIndex, instrumentID := range []models.InstrumentID{"NSE:INFY", "NSE:TCS"} {
		for index := -4; index < 2; index++ {
			value := float64(index + 10)
			rows = append(rows, candles.Candle{
				InstrumentToken: int64(instrumentIndex + 1), InstrumentID: instrumentID,
				Interval: models.CandleInterval1m, Timestamp: from.Add(time.Duration(index) * time.Minute),
				Open: value, High: value, Low: value, Close: value, Volume: 1,
			})
		}
	}
	if err := repo.InsertHistoricalCandles(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetCandles(ctx, models.GetCandlesParams{
		InstrumentIDs: []models.InstrumentID{"NSE:INFY", "NSE:TCS"},
		Intervals:     []models.CandleInterval{models.CandleInterval1m},
		Start:         from,
		End:           from.Add(2 * time.Minute),
		Warmup:        2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("rows = %d, want 8", len(got))
	}
	for index := 0; index < len(got); index += 4 {
		if !got[index].Timestamp.Equal(from.Add(-2*time.Minute)) || !got[index+2].Timestamp.Equal(from) {
			t.Fatalf("unexpected group timestamps: %#v", got[index:index+4])
		}
	}
	if got[0].InstrumentID != "NSE:INFY" || got[0].InstrumentToken != 1 || got[0].Interval != models.CandleInterval1m || got[0].Close != 8 {
		t.Fatalf("first candle = %#v", got[0])
	}
}

func TestGetCandlesWarmupWorksForAggregatedInterval(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	start := time.Date(2026, time.August, 19, 9, 15, 0, 0, models.ExchangeLocation)
	rows := make([]candles.Candle, 0, 20)
	for index := 0; index < 20; index++ {
		value := float64(index)
		rows = append(rows, candles.Candle{
			InstrumentToken: 1, InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m,
			Timestamp: start.Add(time.Duration(index) * time.Minute),
			Open:      value, High: value, Low: value, Close: value, Volume: 1,
		})
	}
	if err := repo.InsertHistoricalCandles(ctx, rows); err != nil {
		t.Fatal(err)
	}
	from := start.Add(10 * time.Minute)
	got, err := repo.GetCandles(ctx, models.GetCandlesParams{
		InstrumentIDs: []models.InstrumentID{"NSE:INFY"},
		Intervals:     []models.CandleInterval{models.CandleInterval5m},
		Start:         from,
		End:           start.Add(20 * time.Minute),
		Warmup:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[0].Timestamp.Equal(start.Add(5*time.Minute)) || !got[1].Timestamp.Equal(from) {
		t.Fatalf("aggregated rows = %#v", got)
	}
	if got[1].Open != 10 || got[1].Close != 14 || got[1].Volume != 5 || got[1].Interval != models.CandleInterval5m {
		t.Fatalf("aggregated candle = %#v", got[1])
	}
}

func TestGetCandlesRejectsInvalidParams(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	for _, params := range []models.GetCandlesParams{
		{Warmup: -1},
		{Intervals: []models.CandleInterval{"7m"}},
		{Intervals: []models.CandleInterval{models.CandleInterval1m, models.CandleInterval5m}},
		{Intervals: []models.CandleInterval{models.CandleInterval1m, models.CandleInterval5m}, Warmup: 1},
	} {
		if _, err := repo.GetCandles(ctx, params); err == nil {
			t.Errorf("GetCandles(%+v) was accepted", params)
		}
	}
}

func TestGetRecentCandlesBatchLimitsEachInstrument(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	start := time.Date(2026, time.August, 5, 9, 15, 0, 0, models.ExchangeLocation)
	rows := make([]candles.Candle, 0, 6)
	for instrumentIndex, instrumentID := range []models.InstrumentID{"NSE:INFY", "NSE:TCS"} {
		for minute := 0; minute < 3; minute++ {
			rows = append(rows, candles.Candle{
				InstrumentToken: int64(100 + instrumentIndex),
				InstrumentID:    instrumentID,
				Interval:        models.CandleInterval1m,
				Timestamp:       start.Add(time.Duration(minute) * time.Minute),
				Open:            float64(minute),
				High:            float64(minute),
				Low:             float64(minute),
				Close:           float64(minute),
			})
		}
	}
	if err := repo.InsertHistoricalCandles(ctx, rows); err != nil {
		t.Fatal(err)
	}

	recent, err := repo.GetRecentCandlesBatch(ctx, []models.InstrumentID{"NSE:TCS", "NSE:INFY"}, models.CandleInterval1m, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 4 {
		t.Fatalf("recent rows = %d, want 4", len(recent))
	}
	for index := 0; index < len(recent); index += 2 {
		if !recent[index].Timestamp.Equal(start.Add(time.Minute)) || !recent[index+1].Timestamp.Equal(start.Add(2*time.Minute)) {
			t.Fatalf("unexpected timestamps: %#v", recent[index:index+2])
		}
		if recent[index].Timestamp.Location().String() != models.ExchangeLocation.String() {
			t.Fatalf("timestamp location = %s, want %s", recent[index].Timestamp.Location(), models.ExchangeLocation)
		}
	}

	single, err := repo.GetRecentCandles(ctx, "NSE:TCS", models.CandleInterval1m, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 2 || !single[0].Timestamp.Equal(start.Add(time.Minute)) || !single[1].Timestamp.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("recent TCS candles = %#v", single)
	}
}

func TestDataAvailabilityAggregateFiltersBeforeGrouping(t *testing.T) {
	query := dataAvailabilityAggregateSQL("historical_data", []models.InstrumentID{" NSE:INFY ", "NSE:INFY"})
	whereIndex := strings.Index(query, "WHERE id IN ('NSE:INFY')")
	groupIndex := strings.Index(query, "GROUP BY it, i")
	if whereIndex < 0 {
		t.Fatalf("instrument filter missing from aggregate query: %s", query)
	}
	if groupIndex < 0 || whereIndex > groupIndex {
		t.Fatalf("instrument filter must precede aggregation: %s", query)
	}
}

func TestGetDataAvailabilityFiltersToRequestedInstrument(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	start := time.Date(2026, time.August, 7, 9, 15, 0, 0, models.ExchangeLocation)
	if err := repo.InsertHistoricalCandles(ctx, []candles.Candle{
		{InstrumentToken: 408065, InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m, Timestamp: start},
		{InstrumentToken: 408065, InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m, Timestamp: start.Add(time.Minute)},
		{InstrumentToken: 2953217, InstrumentID: "NSE:TCS", Interval: models.CandleInterval1m, Timestamp: start},
	}); err != nil {
		t.Fatal(err)
	}

	availability, err := repo.GetDataAvailability(ctx, []models.InstrumentID{"NSE:INFY"})
	if err != nil {
		t.Fatal(err)
	}
	if len(availability) != 1 {
		t.Fatalf("availability rows = %d, want 1", len(availability))
	}
	row := availability[0]
	if row.InstrumentID != "NSE:INFY" || row.Interval != models.CandleInterval1m {
		t.Fatalf("availability row = %#v", row)
	}
	wantStart := time.Date(2026, time.August, 7, 9, 15, 0, 0, time.UTC)
	if !row.HistStart.Equal(wantStart) || !row.HistEnd.Equal(wantStart.Add(time.Minute)) {
		t.Fatalf("availability range = %s-%s", row.HistStart, row.HistEnd)
	}

	instruments, err := repo.GetAvailableInstruments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 2 || instruments[408065] != "NSE:INFY" || instruments[2953217] != "NSE:TCS" {
		t.Fatalf("available instruments = %v", instruments)
	}

	if err := repo.DropInstrumentsByID(ctx, []models.InstrumentID{"NSE:TCS"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteCandlesByInstrumentAndInterval(ctx, "NSE:INFY", models.CandleInterval5m); err != nil {
		t.Fatal(err)
	}
	instruments, err = repo.GetAvailableInstruments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 1 || instruments[408065] != "NSE:INFY" {
		t.Fatalf("available instruments after drop = %v", instruments)
	}
}

func TestParseDerivativeContract(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		underlyingID models.InstrumentID
		expiry       time.Time
		contractType string
		strike       float64
	}{
		{
			name:         "weekly index put",
			id:           "NFO:NIFTY2371317900PE",
			underlyingID: "NSE:NIFTY 50",
			expiry:       time.Date(2023, time.July, 13, 0, 0, 0, 0, models.ExchangeLocation),
			contractType: "PE",
			strike:       17900,
		},
		{
			name:         "monthly stock call after Tuesday transition",
			id:           "nfo:infy26jul1600ce",
			underlyingID: "NSE:INFY",
			expiry:       time.Date(2026, time.July, 28, 0, 0, 0, 0, models.ExchangeLocation),
			contractType: "CE",
			strike:       1600,
		},
		{
			name:         "monthly index future",
			id:           "NFO:NIFTY26JULFUT",
			underlyingID: "NSE:NIFTY 50",
			expiry:       time.Date(2026, time.July, 28, 0, 0, 0, 0, models.ExchangeLocation),
			contractType: "FUT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, ok := parseDerivativeContract(test.id)
			if !ok {
				t.Fatalf("parseDerivativeContract(%q) did not parse", test.id)
			}
			if contract.UnderlyingID != test.underlyingID {
				t.Fatalf("underlying = %q, want %q", contract.UnderlyingID, test.underlyingID)
			}
			if !contract.Expiry.Equal(test.expiry) {
				t.Fatalf("expiry = %s, want %s", contract.Expiry, test.expiry)
			}
			if contract.Type != test.contractType || contract.Strike != test.strike {
				t.Fatalf("contract = %+v, want type=%s strike=%f", contract, test.contractType, test.strike)
			}
		})
	}
}

func TestGetExpiryPriceBatchesFuturesAndIntrinsicOptionSettlement(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	repo.now = func() time.Time {
		return time.Date(2026, time.July, 29, 10, 0, 0, 0, models.ExchangeLocation)
	}
	if err := repo.InsertHistoricalCandles(ctx, []candles.Candle{
		{
			InstrumentToken: 256265, InstrumentID: "NSE:NIFTY 50", Interval: models.CandleInterval1m,
			Timestamp: time.Date(2026, time.July, 28, 15, 28, 0, 0, models.ExchangeLocation),
			Open:      25090, High: 25110, Low: 25080, Close: 25100, Volume: 1,
		},
		{
			InstrumentToken: 256265, InstrumentID: "NSE:NIFTY 50", Interval: models.CandleInterval1m,
			Timestamp: time.Date(2026, time.July, 28, 15, 29, 0, 0, models.ExchangeLocation),
			Open:      25100, High: 25130, Low: 25095, Close: 25123, Volume: 1,
		},
	}); err != nil {
		t.Fatalf("InsertHistoricalCandles() error = %v", err)
	}

	prices, err := repo.GetExpiryPrice(ctx,
		"NFO:NIFTY26JULFUT",
		"NFO:NIFTY26JUL25000CE",
		"NFO:NIFTY26JUL25200PE",
		"NFO:NIFTY26AUG25000CE",
		"NSE:INFY",
	)
	if err != nil {
		t.Fatalf("GetExpiryPrice() error = %v", err)
	}

	expiresAt := time.Date(2026, time.July, 28, 15, 30, 0, 0, models.ExchangeLocation)
	assertExpiryPrice(t, prices, "NFO:NIFTY26JULFUT", 25123, expiresAt)
	assertExpiryPrice(t, prices, "NFO:NIFTY26JUL25000CE", 123, expiresAt)
	assertExpiryPrice(t, prices, "NFO:NIFTY26JUL25200PE", 77, expiresAt)
	if _, ok := prices["NFO:NIFTY26AUG25000CE"]; ok {
		t.Fatal("unexpired contract should not have an expiry price")
	}
	if _, ok := prices["NSE:INFY"]; ok {
		t.Fatal("cash instrument should not have an expiry price")
	}
}

func assertExpiryPrice(t *testing.T, prices map[models.InstrumentID]models.ExpirySettlement, id models.InstrumentID, wantPrice float64, wantExpiry time.Time) {
	t.Helper()
	got, ok := prices[id]
	if !ok {
		t.Fatalf("missing expiry price for %s in %v", id, prices)
	}
	if got.Price != wantPrice {
		t.Fatalf("expiry price for %s = %f, want %f", id, got.Price, wantPrice)
	}
	if !got.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expiry timestamp for %s = %s, want %s", id, got.ExpiresAt, wantExpiry)
	}
}

func TestUpsertHistoricalCandlesReplacesExistingCandles(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	at := time.Date(2026, time.August, 5, 9, 15, 0, 0, models.ExchangeLocation)
	first := candles.Candle{InstrumentToken: 1, InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m, Timestamp: at, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	if err := repo.InsertHistoricalCandles(ctx, []candles.Candle{first}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertHistoricalCandles(ctx, []candles.Candle{first}); err == nil {
		t.Fatal("inserting a stored candle again was accepted")
	}
	second := first
	second.Close, second.Volume = 2, 5
	if err := repo.UpsertHistoricalCandles(ctx, []candles.Candle{second, {InstrumentToken: 1, InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m, Timestamp: at.Add(time.Minute), Close: 3}}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRecentCandles(ctx, "NSE:INFY", models.CandleInterval1m, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Close != 2 || got[0].Volume != 5 || got[1].Close != 3 {
		t.Fatalf("candles after upsert = %#v", got)
	}
}

func TestCandlesDuckDBRepositoryWidensVolumeAndAddsOI(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "candles.duckdb")

	// A database made before volume was BIGINT and OI was kept.
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE historical_data (
			it INT, id TEXT, i TEXT, t TIMESTAMP,
			o DECIMAL(10,4), h DECIMAL(10,4), l DECIMAL(10,4), c DECIMAL(10,4), v INT,
			PRIMARY KEY (it, i, t)
		);
		CREATE INDEX idx_hd_id_i_t ON historical_data(id, i, t);
		INSERT INTO historical_data VALUES (1, 'NSE:IDEA', '1d', '2017-05-18', 1, 1, 1, 1, 2000000000);
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repo, err := NewCandlesDuckDBRepository(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	day := time.Date(2017, time.May, 19, 0, 0, 0, 0, models.ExchangeLocation)
	if err := repo.UpsertHistoricalCandles(ctx, []candles.Candle{{
		InstrumentID: "NSE:IDEA", InstrumentToken: 1, Interval: models.CandleInterval1d,
		Timestamp: day, Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 2399245304, OI: 12345,
	}}); err != nil {
		t.Fatal(err)
	}

	var volume, oi int64
	if err := repo.db.QueryRow("SELECT v, oi FROM historical_data WHERE t = '2017-05-19'").Scan(&volume, &oi); err != nil {
		t.Fatal(err)
	}
	if volume != 2399245304 || oi != 12345 {
		t.Errorf("stored v = %d, oi = %d", volume, oi)
	}
	var indexes int
	if err := repo.db.QueryRow("SELECT count(*) FROM duckdb_indexes() WHERE index_name = 'idx_hd_id_i_t'").Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 1 {
		t.Errorf("idx_hd_id_i_t wasn't rebuilt")
	}
	if err := repo.InsertHistoricalCandles(ctx, []candles.Candle{{InstrumentID: "NSE:IDEA", InstrumentToken: 1, Interval: models.CandleInterval1d, Timestamp: day}}); err == nil {
		t.Errorf("the primary key no longer refuses a duplicate candle")
	}
}

func TestGetCandlesResamplesIntradayFrom1mAndWeeksFromDays(t *testing.T) {
	ctx := context.Background()
	repo := openCandles(t)
	ist := models.ExchangeLocation
	candle := func(interval models.CandleInterval, ts time.Time, open, high, low, close float64, volume int) candles.Candle {
		return candles.Candle{InstrumentID: "NSE:TCS", InstrumentToken: 1, Interval: interval, Timestamp: ts, Open: open, High: high, Low: low, Close: close, Volume: volume}
	}
	// Mon 12 Oct to Fri 16 Oct 2026 have day candles closing at the auction
	// price; Mon 19 Oct has only 1m candles, as a day not yet synced.
	var rows []candles.Candle
	for day := 12; day <= 16; day++ {
		base := float64(100 + day)
		rows = append(rows, candle(models.CandleInterval1d, time.Date(2026, time.October, day, 0, 0, 0, 0, ist), base, base+5, base-5, base+1, 1000))
	}
	for minute := range 20 {
		ts := time.Date(2026, time.October, 16, 9, 15+minute, 0, 0, ist)
		rows = append(rows, candle(models.CandleInterval1m, ts, 116, 118, 114, 116.5, 10))
	}
	for minute := range 3 {
		ts := time.Date(2026, time.October, 19, 9, 15+minute, 0, 0, ist)
		rows = append(rows, candle(models.CandleInterval1m, ts, float64(120+minute), 125, 119, float64(121+minute), 7))
	}
	if err := repo.InsertHistoricalCandles(ctx, rows); err != nil {
		t.Fatal(err)
	}
	get := func(interval models.CandleInterval) []candles.Candle {
		t.Helper()
		got, err := repo.GetCandles(ctx, models.GetCandlesParams{
			InstrumentIDs: []models.InstrumentID{"NSE:TCS"},
			Intervals:     []models.CandleInterval{interval},
			Start:         time.Date(2026, time.October, 12, 0, 0, 0, 0, ist),
			End:           time.Date(2026, time.October, 20, 0, 0, 0, 0, ist),
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// 1m and 15m leave the day candles out.
	if got := get(models.CandleInterval1m); len(got) != 23 {
		t.Errorf("1m candles = %d, want 23", len(got))
	}
	fifteen := get(models.CandleInterval15m)
	if len(fifteen) != 3 || fifteen[0].Volume != 150 || fifteen[1].Volume != 50 || fifteen[2].Volume != 21 {
		t.Errorf("15m candles = %+v", fifteen)
	}

	// 1d is the stored day candles, then 19 Oct resampled from 1m.
	days := get(models.CandleInterval1d)
	if len(days) != 6 {
		t.Fatalf("1d candles = %+v", days)
	}
	if friday := days[4]; friday.Close != 117 || friday.Volume != 1000 {
		t.Errorf("16 Oct = %+v, want the stored day candle", friday)
	}
	if monday := days[5]; !monday.Timestamp.Equal(time.Date(2026, time.October, 19, 0, 0, 0, 0, ist)) || monday.Open != 120 || monday.Close != 123 || monday.High != 125 || monday.Volume != 21 {
		t.Errorf("19 Oct = %+v, want it resampled from 1m", monday)
	}

	// 1W is resampled from the days, in weeks from Monday.
	weeks := get(models.CandleInterval1W)
	if len(weeks) != 2 {
		t.Fatalf("1W candles = %+v", weeks)
	}
	if week := weeks[0]; !week.Timestamp.Equal(time.Date(2026, time.October, 12, 0, 0, 0, 0, ist)) || week.Open != 112 || week.Close != 117 || week.High != 121 || week.Low != 107 || week.Volume != 5000 {
		t.Errorf("week of 12 Oct = %+v", week)
	}
	if week := weeks[1]; week.Open != 120 || week.Close != 123 || week.Volume != 21 {
		t.Errorf("week of 19 Oct = %+v", week)
	}
}
