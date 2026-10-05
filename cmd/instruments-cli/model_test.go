package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

func TestCollectExpiriesAndBuildChainRows(t *testing.T) {
	near := cliDate(2026, time.June, 25)
	far := cliDate(2026, time.July, 30)
	options := []models.Instrument{
		option("NFO:NIFTY20000PE", models.OptionTypePut, 20000, 0, near),
		option("NFO:NIFTY20100CE", models.OptionTypeCall, 20100, 0, near),
		option("NFO:NIFTY20000CE", models.OptionTypeCall, 20000, 0, near),
		option("NFO:NIFTY21000CE", models.OptionTypeCall, 21000, 1, far),
	}
	futures := []models.Instrument{
		future("NFO:NIFTYJULFUT", 1, far),
		future("NFO:NIFTYJUNFUT", 0, near),
	}

	expiries := collectExpiries(futures, options)
	wantExpiries := []expiryChoice{{number: 0, date: near}, {number: 1, date: far}}
	if !reflect.DeepEqual(expiries, wantExpiries) {
		t.Fatalf("collectExpiries() = %#v, want %#v", expiries, wantExpiries)
	}

	minimum, maximum := 19950.0, 20050.0
	rows := buildChainRows(options, chainFilter{
		expiryNumber: 0,
		minStrike:    &minimum,
		maxStrike:    &maximum,
		side:         optionSideBoth,
	})
	if len(rows) != 1 || rows[0].strike != 20000 || rows[0].call == nil || rows[0].put == nil {
		t.Fatalf("buildChainRows() = %#v", rows)
	}
	if rows[0].call.ID != "NFO:NIFTY20000CE" || rows[0].put.ID != "NFO:NIFTY20000PE" {
		t.Fatalf("paired row = %#v", rows[0])
	}

	calls := buildChainRows(options, chainFilter{expiryNumber: 0, side: optionSideCalls})
	if len(calls) != 2 || calls[0].strike != 20000 || calls[1].strike != 20100 || calls[0].put != nil || calls[1].put != nil {
		t.Fatalf("call-only rows = %#v", calls)
	}
	puts := buildChainRows(options, chainFilter{expiryNumber: 0, side: optionSidePuts})
	if len(puts) != 1 || puts[0].call != nil || puts[0].put == nil {
		t.Fatalf("put-only rows = %#v", puts)
	}

	visibleFutures := filterFutures(futures, chainFilter{expiryNumber: 0})
	if len(visibleFutures) != 1 || visibleFutures[0].ID != "NFO:NIFTYJUNFUT" {
		t.Fatalf("filterFutures() = %#v", visibleFutures)
	}
}

func TestDrilldownUnderlying(t *testing.T) {
	underlying := models.InstrumentID("NSE:NIFTY 50")
	if got, ok := drilldownUnderlying(models.Instrument{UnderlyingID: &underlying}); !ok || got != underlying {
		t.Fatalf("derivative drilldown = %q, %v", got, ok)
	}
	if got, ok := drilldownUnderlying(models.Instrument{ID: underlying, OptionsCount: 1}); !ok || got != underlying {
		t.Fatalf("underlying drilldown = %q, %v", got, ok)
	}
	if _, ok := drilldownUnderlying(models.Instrument{ID: "NSE:INFY"}); ok {
		t.Fatal("cash instrument unexpectedly supports drilldown")
	}
}

func TestParseStrikeRangeAndFormatting(t *testing.T) {
	minimum, maximum, err := parseStrikeRange(" 100.5 ", "200")
	if err != nil || minimum == nil || *minimum != 100.5 || maximum == nil || *maximum != 200 {
		t.Fatalf("parseStrikeRange() = %v, %v, %v", minimum, maximum, err)
	}
	if _, _, err := parseStrikeRange("201", "200"); err == nil {
		t.Fatal("reversed strike range was accepted")
	}
	if _, _, err := parseStrikeRange("-1", ""); err == nil {
		t.Fatal("negative strike was accepted")
	}
	if _, _, err := parseStrikeRange("NaN", ""); err == nil {
		t.Fatal("non-finite strike was accepted")
	}
	date := cliDate(2026, time.June, 25)
	if got := formatExpiry(&date); got != "25 Jun 2026" {
		t.Fatalf("formatExpiry() = %q", got)
	}
	if got := formatStrike(20000.5); got != "20000.5" {
		t.Fatalf("formatStrike() = %q", got)
	}
}

func TestLoadInitialCatalogAndRetry(t *testing.T) {
	wantTime := time.Date(2026, time.September, 16, 8, 30, 0, 0, time.UTC)
	want := []models.Instrument{{ID: "NSE:NIFTY 50"}}
	store := &fakeCatalog{lastRefreshed: wantTime, underlyings: want}
	attempts := 0
	factory := func(context.Context) (catalog, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary open failure")
		}
		return store, nil
	}

	if _, err := loadInitialCatalog(context.Background(), factory); err == nil {
		t.Fatal("first initialization unexpectedly succeeded")
	}
	got, err := loadInitialCatalog(context.Background(), factory)
	if err != nil {
		t.Fatalf("retry initialization error = %v", err)
	}
	if got.catalog != store || !got.lastRefreshed.Equal(wantTime) || !reflect.DeepEqual(got.underlyings, want) {
		t.Fatalf("initial catalog = %#v", got)
	}

	failing := &fakeCatalog{listFOErr: errors.New("query failed")}
	if _, err := loadInitialCatalog(context.Background(), func(context.Context) (catalog, error) { return failing, nil }); err == nil {
		t.Fatal("failed initial query unexpectedly succeeded")
	}
	if failing.closeCalls != 1 {
		t.Fatalf("failed catalog close calls = %d, want 1", failing.closeCalls)
	}
}

func TestLoadInitialCatalogHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loadInitialCatalog(ctx, func(ctx context.Context) (catalog, error) {
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loadInitialCatalog() error = %v, want context.Canceled", err)
	}
}

func TestLoadChainDataAndPreserveFilter(t *testing.T) {
	near := cliDate(2026, time.June, 25)
	far := cliDate(2026, time.July, 30)
	store := &fakeCatalog{
		futures: []models.Instrument{future("NFO:NIFTYJUNFUT", 0, near)},
		options: []models.Instrument{
			option("NFO:NIFTY20000CE", models.OptionTypeCall, 20000, 0, near),
			option("NFO:NIFTY21000CE", models.OptionTypeCall, 21000, 1, far),
		},
	}
	data, err := loadChainData(context.Background(), store, "NSE:NIFTY 50")
	if err != nil {
		t.Fatalf("loadChainData() error = %v", err)
	}
	if data.filter.expiryNumber != 0 || len(data.expiries) != 2 {
		t.Fatalf("chain defaults = %#v", data)
	}
	minimum := 19900.0
	preserved := preserveChainFilter(data, chainFilter{expiryNumber: 1, minStrike: &minimum, side: optionSideCalls})
	if preserved.expiryNumber != 1 || preserved.minStrike == nil || *preserved.minStrike != minimum || preserved.side != optionSideCalls {
		t.Fatalf("preserveChainFilter() = %#v", preserved)
	}
	preserved = preserveChainFilter(data, chainFilter{expiryNumber: 9, side: optionSidePuts})
	if preserved.expiryNumber != 0 || preserved.side != optionSidePuts {
		t.Fatalf("missing expiry fallback = %#v", preserved)
	}
}

func TestReloadAfterRefreshRestoresCurrentView(t *testing.T) {
	near := cliDate(2026, time.June, 25)
	refreshedAt := time.Date(2026, time.September, 16, 9, 0, 0, 0, time.UTC)
	searchResults := []models.Instrument{{ID: "NSE:INFY"}}
	store := &fakeCatalog{
		lastRefreshed: refreshedAt,
		searchResults: searchResults,
		underlyings:   []models.Instrument{{ID: "NSE:NIFTY 50"}, {ID: "BSE:SENSEX"}},
	}

	searchView, err := reloadAfterRefresh(context.Background(), store, viewDashboard, "infy", "NSE:INFY", "", chainFilter{})
	if err != nil {
		t.Fatalf("search refresh error = %v", err)
	}
	if store.refreshCalls != 1 || searchView.underlyingCount != 2 || searchView.query != "infy" || searchView.selectedID != "NSE:INFY" || !reflect.DeepEqual(searchView.results, searchResults) {
		t.Fatalf("search refresh view = %#v, calls=%d", searchView, store.refreshCalls)
	}

	minimum := 19900.0
	store.futures = []models.Instrument{future("NFO:NIFTYJUNFUT", 0, near)}
	store.options = []models.Instrument{option("NFO:NIFTY20000CE", models.OptionTypeCall, 20000, 0, near)}
	chainView, err := reloadAfterRefresh(context.Background(), store, viewChain, "nifty", "", "NSE:NIFTY 50", chainFilter{
		expiryNumber: 0,
		minStrike:    &minimum,
		side:         optionSideCalls,
	})
	if err != nil {
		t.Fatalf("chain refresh error = %v", err)
	}
	if chainView.chain == nil || chainView.chain.filter.minStrike == nil || *chainView.chain.filter.minStrike != minimum || chainView.chain.filter.side != optionSideCalls {
		t.Fatalf("chain refresh view = %#v", chainView)
	}

	store.refreshErr = errors.New("refresh failed")
	if _, err := reloadAfterRefresh(context.Background(), store, viewDashboard, "infy", "NSE:INFY", "", chainFilter{}); !errors.Is(err, store.refreshErr) {
		t.Fatalf("refresh failure = %v", err)
	}
}

type fakeCatalog struct {
	lastRefreshed time.Time
	underlyings   []models.Instrument
	futures       []models.Instrument
	options       []models.Instrument
	listFOErr     error
	closeCalls    int
	refreshCalls  int
	refreshErr    error
	searchResults []models.Instrument
}

func (f *fakeCatalog) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeCatalog) Refresh(context.Context) error {
	f.refreshCalls++
	return f.refreshErr
}

func (f *fakeCatalog) LastRefreshedAt(context.Context) (time.Time, error) {
	return f.lastRefreshed, nil
}

func (f *fakeCatalog) ListFO(context.Context, ...string) ([]models.Instrument, error) {
	return f.underlyings, f.listFOErr
}

func (f *fakeCatalog) Search(context.Context, string, int) ([]models.Instrument, error) {
	return f.searchResults, nil
}

func (f *fakeCatalog) GetFutures(context.Context, models.FuturesFilter) ([]models.Instrument, error) {
	return f.futures, nil
}

func (f *fakeCatalog) GetOptions(context.Context, models.OptionsFilter) ([]models.Instrument, error) {
	return f.options, nil
}

func option(id models.InstrumentID, optionType models.OptionType, strike float64, expiryNumber int, expiry time.Time) models.Instrument {
	return models.Instrument{
		ID:             id,
		TradingSymbol:  string(id),
		InstrumentType: string(optionType),
		Strike:         strike,
		Expiry:         &expiry,
		ExpiryNumber:   &expiryNumber,
	}
}

func future(id models.InstrumentID, expiryNumber int, expiry time.Time) models.Instrument {
	return models.Instrument{
		ID:             id,
		TradingSymbol:  string(id),
		InstrumentType: "FUT",
		Expiry:         &expiry,
		ExpiryNumber:   &expiryNumber,
	}
}

func cliDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.FixedZone("Asia/Kolkata", 5*60*60+30*60))
}
