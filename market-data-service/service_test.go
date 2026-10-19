package marketdata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
	"github.com/devshoe/gokiteconnect/repository"
)

// fakeSource serves one candle per day at 09:15 for each day in a request,
// and records the requests.
type fakeSource struct {
	tokens   map[models.InstrumentID]int64
	requests []SyncRequest
	err      error
}

func (f *fakeSource) StreamCandles(_ context.Context, id models.InstrumentID, interval models.CandleInterval, from, to time.Time, consume func([]candles.Candle) error) error {
	f.requests = append(f.requests, SyncRequest{InstrumentID: id, Interval: interval, From: from, To: to})
	if f.err != nil {
		return f.err
	}
	var batch []candles.Candle
	day := time.Date(from.Year(), from.Month(), from.Day(), 9, 15, 0, 0, models.ExchangeLocation)
	for ; day.Before(to); day = day.AddDate(0, 0, 1) {
		if day.Before(from) {
			continue
		}
		batch = append(batch, candles.Candle{
			InstrumentID: id, InstrumentToken: f.tokens[id], Interval: interval,
			Timestamp: day, Open: 1, High: 2, Low: 1, Close: 2, Volume: 10,
		})
	}
	return consume(batch)
}

// now is 2026-08-12 10:00 IST, so the settled close is 2026-08-11 15:30.
var now = time.Date(2026, time.August, 12, 10, 0, 0, 0, models.ExchangeLocation)

func newTestService(t *testing.T, source Source, lists Lists) (*Service, *repository.CandlesDuckDBRepository) {
	t.Helper()
	store, err := repository.NewCandlesDuckDBRepository(context.Background(), filepath.Join(t.TempDir(), "candles.duckdb"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := New(store, source, lists, nil)
	svc.now = func() time.Time { return now }
	return svc, store
}

func testCandle(id models.InstrumentID, token int64, at time.Time, close float64) candles.Candle {
	return candles.Candle{InstrumentID: id, InstrumentToken: token, Interval: models.CandleInterval1m, Timestamp: at, Open: close, High: close, Low: close, Close: close, Volume: 1}
}

func TestSyncResumesAfterLastStoredCandle(t *testing.T) {
	ctx := context.Background()
	source := &fakeSource{tokens: map[models.InstrumentID]int64{"NSE:INFY": 408065}}
	svc, _ := newTestService(t, source, nil)

	first, err := svc.Sync(ctx, SyncRequest{InstrumentID: "nse:infy", Interval: models.CandleInterval1m, From: time.Date(2026, time.August, 10, 0, 0, 0, 0, models.ExchangeLocation)})
	if err != nil {
		t.Fatal(err)
	}
	if first.InstrumentID != "NSE:INFY" || first.Candles != 2 || !first.To.Equal(time.Date(2026, time.August, 11, 15, 30, 0, 0, models.ExchangeLocation)) {
		t.Fatalf("first sync = %+v", first)
	}

	svc.now = func() time.Time { return now.AddDate(0, 0, 1) }
	second, err := svc.Sync(ctx, SyncRequest{InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m})
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, time.August, 11, 9, 16, 0, 0, models.ExchangeLocation)
	if !second.From.Equal(wantFrom) || second.Candles != 1 {
		t.Fatalf("resumed sync = %+v, want from %s and 1 candle", second, wantFrom)
	}

	recent, err := svc.RecentCandles(ctx, []models.InstrumentID{"NSE:INFY"}, models.CandleInterval1m, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 || recent[0].InstrumentToken != 408065 {
		t.Fatalf("stored candles = %#v", recent)
	}
}

func TestSyncDefaultsToEarliestDateAndSkipsEmptyRange(t *testing.T) {
	ctx := context.Background()
	source := &fakeSource{err: errors.New("stop")}
	svc, _ := newTestService(t, source, nil)

	if _, err := svc.Sync(ctx, SyncRequest{InstrumentID: "NSE:INFY", Interval: models.CandleInterval1d}); err == nil {
		t.Fatal("source error was not returned")
	}
	if got := source.requests[0].From; !got.Equal(DefaultStartDate(models.CandleInterval1d)) {
		t.Fatalf("from = %s, want %s", got, DefaultStartDate(models.CandleInterval1d))
	}

	result, err := svc.Sync(ctx, SyncRequest{InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m, From: now})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || len(source.requests) != 1 {
		t.Fatalf("sync past the settled close = %+v, requests = %d", result, len(source.requests))
	}
}

func TestSyncStandardSyncsMinuteFrom2015AndDailyFromEarliest(t *testing.T) {
	ctx := context.Background()
	source := &fakeSource{tokens: map[models.InstrumentID]int64{"NSE:INFY": 408065}}
	svc, _ := newTestService(t, source, nil)

	results, err := svc.SyncStandard(ctx, "NSE:INFY")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || len(source.requests) != 2 {
		t.Fatalf("results = %+v, requests = %+v", results, source.requests)
	}
	want := []SyncRequest{
		{InstrumentID: "NSE:INFY", Interval: models.CandleInterval1m, From: time.Date(2015, time.January, 1, 0, 0, 0, 0, models.ExchangeLocation)},
		{InstrumentID: "NSE:INFY", Interval: models.CandleInterval1d, From: time.Date(1990, time.January, 1, 0, 0, 0, 0, models.ExchangeLocation)},
	}
	for index, request := range source.requests {
		if request.Interval != want[index].Interval || !request.From.Equal(want[index].From) {
			t.Fatalf("request %d = %+v, want %s from %s", index, request, want[index].Interval, want[index].From)
		}
	}

	// A second run resumes after the stored candles: the fake's last 1m
	// candle is 09:15 on the settled day, and the 1d series is current.
	source.requests = nil
	results, err = svc.SyncStandard(ctx, "NSE:INFY")
	if err != nil {
		t.Fatal(err)
	}
	resume := time.Date(2026, time.August, 11, 9, 16, 0, 0, models.ExchangeLocation)
	if len(source.requests) != 1 || !source.requests[0].From.Equal(resume) || !results[1].Skipped {
		t.Fatalf("second run requests = %+v, results = %+v", source.requests, results)
	}
}

func TestSyncReportsProgressAfterEachBatch(t *testing.T) {
	ctx := context.Background()
	source := &fakeSource{tokens: map[models.InstrumentID]int64{"NSE:INFY": 408065}}
	svc, _ := newTestService(t, source, nil)

	var reports []SyncProgress
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, models.ExchangeLocation)
	result, err := svc.Sync(ctx, SyncRequest{InstrumentID: "NSE:INFY", Interval: models.CandleInterval1d, From: from,
		Progress: func(p SyncProgress) { reports = append(reports, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	report := reports[0]
	if report.Candles != result.Candles || report.Interval != models.CandleInterval1d || !report.From.Equal(from) {
		t.Errorf("report = %+v, result = %+v", report, result)
	}
	// The fake's last candle is 09:15 on the settled day, just before To.
	if fraction := report.Fraction(); fraction < 0.95 || fraction > 1 {
		t.Errorf("fraction = %v", fraction)
	}
}

func TestSyncAllContinuesPastFailures(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, time.August, 10, 9, 15, 0, 0, models.ExchangeLocation)
	source := &fakeSource{tokens: map[models.InstrumentID]int64{"NSE:INFY": 1, "NSE:TCS": 2}}
	svc, _ := newTestService(t, source, nil)
	if _, err := svc.PutCandles(ctx, []candles.Candle{testCandle("NSE:INFY", 1, at, 1), testCandle("NSE:TCS", 2, at, 1)}, false); err != nil {
		t.Fatal(err)
	}

	source.err = errors.New("kite down")
	result, err := svc.SyncAll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 2 || len(result.Synced) != 0 {
		t.Fatalf("sync all = %+v", result)
	}

	source.err = nil
	result, err = svc.SyncAll(ctx, models.CandleInterval1m)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Synced) != 2 || len(result.Failed) != 0 {
		t.Fatalf("sync all = %+v", result)
	}
}

func TestReconcileDropsRemovedAndBackfillsAdded(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, time.August, 10, 9, 15, 0, 0, models.ExchangeLocation)
	source := &fakeSource{tokens: map[models.InstrumentID]int64{"NSE:SBIN": 3}}
	lists := func(_ context.Context, listID string) (models.List, error) {
		if listID != "watch" {
			return models.List{}, models.ErrNotFound
		}
		return models.List{ID: "watch", Name: "Watch", Items: []models.ListItem{{InstrumentID: "NSE:INFY"}, {InstrumentID: "NSE:SBIN"}}}, nil
	}
	svc, _ := newTestService(t, source, lists)
	if _, err := svc.PutCandles(ctx, []candles.Candle{testCandle("NSE:INFY", 1, at, 1), testCandle("NSE:TCS", 2, at, 1)}, false); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.PreviewReconcile(ctx, "missing"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("missing list error = %v", err)
	}
	preview, err := svc.PreviewReconcile(ctx, "watch")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Added) != 1 || preview.Added[0] != "NSE:SBIN" || len(preview.Removed) != 1 || preview.Removed[0] != "NSE:TCS" {
		t.Fatalf("preview = %+v", preview)
	}
	if len(source.requests) != 0 {
		t.Fatal("preview fetched candles")
	}

	result, err := svc.Reconcile(ctx, "watch", models.CandleInterval1d)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Synced) != 1 || !source.requests[0].From.Equal(DefaultStartDate(models.CandleInterval1d)) {
		t.Fatalf("reconcile = %+v, requests = %+v", result, source.requests)
	}
	availability, err := svc.Availability(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[models.InstrumentID]bool{}
	for _, row := range availability {
		stored[row.InstrumentID] = true
	}
	if !stored["NSE:INFY"] || !stored["NSE:SBIN"] || stored["NSE:TCS"] {
		t.Fatalf("stored instruments after reconcile = %v", stored)
	}
}

func TestCandleCRUD(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, nil, nil)
	at := time.Date(2026, time.August, 10, 9, 15, 0, 0, models.ExchangeLocation)
	rows := []candles.Candle{testCandle("nse:infy", 1, at, 1), testCandle("NSE:INFY", 1, at.Add(time.Minute), 2)}

	if stored, err := svc.PutCandles(ctx, rows, false); err != nil || stored != 2 {
		t.Fatalf("PutCandles = %d, %v", stored, err)
	}
	if _, err := svc.PutCandles(ctx, rows[:1], false); err == nil {
		t.Fatal("adding a stored candle again was accepted")
	}
	replaced := testCandle("NSE:INFY", 1, at, 5)
	if _, err := svc.PutCandles(ctx, []candles.Candle{replaced}, true); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Candles(ctx, CandleRange{InstrumentIDs: []models.InstrumentID{"NSE:INFY"}, Interval: models.CandleInterval1m, From: at, To: at.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Close != 5 || got[1].Close != 2 {
		t.Fatalf("candles = %#v", got)
	}

	if err := svc.DeleteCandles(ctx, []models.InstrumentID{"NSE:INFY"}, models.CandleInterval1m); err != nil {
		t.Fatal(err)
	}
	got, err = svc.RecentCandles(ctx, []models.InstrumentID{"NSE:INFY"}, models.CandleInterval1m, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("candles after delete = %#v", got)
	}
}

func TestInvalidRequestsAreErrInvalid(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, nil, nil)
	at := time.Date(2026, time.August, 10, 9, 15, 0, 0, models.ExchangeLocation)
	bad := testCandle("NSE:INFY", 1, at, 1)
	bad.High, bad.Low = 1, 2

	for name, err := range map[string]error{
		"sync without instrument": func() error { _, err := svc.Sync(ctx, SyncRequest{Interval: models.CandleInterval1m}); return err }(),
		"sync bad interval": func() error {
			_, err := svc.Sync(ctx, SyncRequest{InstrumentID: "NSE:INFY", Interval: "7m"})
			return err
		}(),
		"bad instrument": func() error { _, err := svc.Availability(ctx, []models.InstrumentID{"INFY"}); return err }(),
		"candles without range": func() error {
			_, err := svc.Candles(ctx, CandleRange{InstrumentIDs: []models.InstrumentID{"NSE:INFY"}, Interval: models.CandleInterval1m})
			return err
		}(),
		"high below low":         func() error { _, err := svc.PutCandles(ctx, []candles.Candle{bad}, false); return err }(),
		"delete without ids":     svc.DeleteCandles(ctx, nil, ""),
		"reconcile without list": func() error { _, err := svc.Reconcile(ctx, " ", models.CandleInterval1m); return err }(),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSettledMarketClose(t *testing.T) {
	// 01:00 IST on the 12th is still the 11th in UTC; the close is the 11th's.
	at := time.Date(2026, time.August, 11, 19, 30, 0, 0, time.UTC)
	want := time.Date(2026, time.August, 11, 15, 30, 0, 0, models.ExchangeLocation)
	if got := settledMarketClose(at); !got.Equal(want) {
		t.Fatalf("settledMarketClose(%s) = %s, want %s", at, got, want)
	}
}
