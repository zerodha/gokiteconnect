package repository

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

func openFundamentals(t *testing.T, ctx context.Context, path string) *FundamentalsSQLiteRepository {
	t.Helper()
	repo, err := NewFundamentalsSQLiteRepository(ctx, path)
	if err != nil {
		t.Fatalf("NewFundamentalsSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestFundamentalsReplaceAndRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fundamentals.sqlite3")
	repo := openFundamentals(t, ctx, path)

	if at, err := repo.LastRefreshedAt(ctx); err != nil || !at.IsZero() {
		t.Fatalf("new LastRefreshedAt() = %v, %v; want zero", at, err)
	}
	updated := time.Date(2026, 10, 22, 1, 30, 14, 0, time.UTC)
	reliance := models.Fundamentals{
		InstrumentToken: 738561, Exchange: "NSE", TradingSymbol: "RELIANCE", ISIN: "INE002A01018", Name: "RELIANCE INDUSTRIES",
		Sector: "Oil & Gas", Tags: []string{"Nifty 50", "Nifty 500"}, LastPrice: 1400, MarketCap: 1900000, PE: 25,
		DividendYield: 0.4, ROE: 9, DebtToEquity: 0.4, Week52High: 1600, Week52Low: 1100,
		RevenueGrowthYOY: 7, PATGrowthYOY: 5, FreeCashFlow: 30000, UpdatedAt: updated,
	}
	hfcl := models.Fundamentals{InstrumentToken: 3343617, Exchange: "NSE", TradingSymbol: "HFCL-BE", Tags: []string{"Nifty 500"}, MarketCap: 15000}
	etf := models.Fundamentals{InstrumentToken: 194809857, Exchange: "NSE", TradingSymbol: "GROWWPSE"}
	refreshed := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)
	if err := repo.Replace(ctx, []models.Fundamentals{etf, hfcl, reliance}, refreshed); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	got, err := repo.Get(ctx, 738561)
	if err != nil || !reflect.DeepEqual(got, reliance) {
		t.Fatalf("Get() = %+v, %v; want %+v", got, err, reliance)
	}
	if got, err := repo.Get(ctx, 194809857); err != nil || !got.UpdatedAt.IsZero() || got.Tags == nil {
		t.Fatalf("Get(ETF) = %+v, %v; want no update time and empty tags", got, err)
	}
	if _, err := repo.Get(ctx, 1); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
	ids := func(list []models.Fundamentals) []models.InstrumentID {
		var out []models.InstrumentID
		for _, f := range list {
			out = append(out, f.ID())
		}
		return out
	}
	all, err := repo.List(ctx)
	if want := []models.InstrumentID{"NSE:RELIANCE", "NSE:HFCL-BE", "NSE:GROWWPSE"}; err != nil || !reflect.DeepEqual(ids(all), want) {
		t.Fatalf("List() = %v, %v; want %v by market cap", ids(all), err, want)
	}
	nifty, err := repo.List(ctx, "Nifty 500", "Nifty Bank")
	if want := []models.InstrumentID{"NSE:RELIANCE", "NSE:HFCL-BE"}; err != nil || !reflect.DeepEqual(ids(nifty), want) {
		t.Fatalf("List(Nifty 500) = %v, %v; want %v", ids(nifty), err, want)
	}
	if at, err := repo.LastRefreshedAt(ctx); err != nil || !at.Equal(refreshed) {
		t.Fatalf("LastRefreshedAt() = %v, %v; want %v", at, err, refreshed)
	}

	// A refresh replaces every row, and survives reopening.
	later := refreshed.Add(30 * 24 * time.Hour)
	hfcl.TradingSymbol = "HFCL"
	if err := repo.Replace(ctx, []models.Fundamentals{hfcl}, later); err != nil {
		t.Fatalf("second Replace() error = %v", err)
	}
	_ = repo.Close()
	repo = openFundamentals(t, ctx, path)
	if count, err := repo.Count(ctx); err != nil || count != 1 {
		t.Fatalf("Count() after refresh = %d, %v; want 1", count, err)
	}
	if got, err := repo.Get(ctx, hfcl.InstrumentToken); err != nil || got.TradingSymbol != "HFCL" {
		t.Fatalf("Get() after refresh = %+v, %v", got, err)
	}
	if at, err := repo.LastRefreshedAt(ctx); err != nil || !at.Equal(later) {
		t.Fatalf("LastRefreshedAt() after refresh = %v, %v; want %v", at, err, later)
	}
}

func TestFundamentalsReplaceRejectsBadReads(t *testing.T) {
	ctx := context.Background()
	repo := openFundamentals(t, ctx, ":memory:")
	good := models.Fundamentals{InstrumentToken: 1, Exchange: "NSE", TradingSymbol: "A"}
	if err := repo.Replace(ctx, []models.Fundamentals{good}, time.Now()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	for name, test := range map[string]struct {
		rows []models.Fundamentals
		at   time.Time
	}{
		"empty read":  {nil, time.Now()},
		"no time":     {[]models.Fundamentals{good}, time.Time{}},
		"no token":    {[]models.Fundamentals{good, {Exchange: "NSE", TradingSymbol: "B"}}, time.Now()},
		"no exchange": {[]models.Fundamentals{{InstrumentToken: 2, TradingSymbol: "B"}}, time.Now()},
	} {
		if err := repo.Replace(ctx, test.rows, test.at); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("%s: Replace() error = %v, want ErrInvalidInput", name, err)
		}
	}
	// A refused read leaves the stored rows alone.
	if count, err := repo.Count(ctx); err != nil || count != 1 {
		t.Fatalf("Count() = %d, %v; want the 1 stored row", count, err)
	}
}
