package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

func TestLegacyPaperDatabasePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "portfolio")
	if path, err := LegacyPaperDatabasePath(dir, "cc0006"); err != nil || path != filepath.Join(dir, "CC0006_paper.db") {
		t.Fatalf("LegacyPaperDatabasePath() = %q, %v", path, err)
	}
	if _, err := LegacyPaperDatabasePath(dir, "../CC0006"); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("LegacyPaperDatabasePath(bad) error = %v, want ErrInvalidInput", err)
	}
}

// TestPaperMovesIntoTheUserDatabase opens a paper account kept in its own
// file: it moves into the user's database, beside the live order record,
// once, and the old file is renamed aside.
func TestPaperMovesIntoTheUserDatabase(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "portfolio")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, _ := LegacyPaperDatabasePath(dir, "CC0006")
	created := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	old, err := NewPaperSQLiteRepository(ctx, legacy, "CC0006")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.OpenAccount(ctx, models.PaperAccount{Capital: 5_00_000, CreatedAt: created, SettledThrough: created}); err != nil {
		t.Fatal(err)
	}
	order := kite.Order{OrderID: "PAPER-1", Status: "COMPLETE", TradingSymbol: "INFY"}
	fill := models.PaperFill{TradeID: "T-1", OrderID: "PAPER-1", Exchange: "NSE", TradingSymbol: "INFY", Product: "CNC",
		TransactionType: "BUY", Quantity: 3, Price: 1500, FilledAt: created}
	if err := old.Record(ctx, PaperChange{Orders: []kite.Order{order}, Fills: []models.PaperFill{fill}}); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	// The live record is already in the user's database.
	path, _ := PortfolioDatabasePath(dir, "CC0006")
	live, err := NewPortfolioSQLiteRepository(ctx, path, "CC0006")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.RecordOrderUpdates(ctx, kite.Order{OrderID: "LIVE-1", Status: "OPEN"}); err != nil {
		t.Fatal(err)
	}
	_ = live.Close()

	check := func(repo *PaperSQLiteRepository) {
		t.Helper()
		account, err := repo.Account(ctx)
		if err != nil || account.Capital != 5_00_000 || !account.CreatedAt.Equal(created) {
			t.Fatalf("Account() = %+v, %v", account, err)
		}
		orders, err := repo.Orders(ctx)
		if err != nil || len(orders) != 1 || orders[0].OrderID != "PAPER-1" {
			t.Fatalf("Orders() = %+v, %v", orders, err)
		}
		fills, err := repo.Fills(ctx)
		if err != nil || len(fills) != 1 || fills[0].Quantity != 3 {
			t.Fatalf("Fills() = %+v, %v", fills, err)
		}
	}
	repo, err := OpenUserPaperSQLiteRepository(ctx, dir, "cc0006")
	if err != nil {
		t.Fatalf("OpenUserPaperSQLiteRepository() error = %v", err)
	}
	check(repo)
	_ = repo.Close()
	for _, gone := range []string{legacy, legacy + "-wal", legacy + "-shm"} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there: %v", filepath.Base(gone), err)
		}
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Fatalf("the old file was not kept aside: %v", err)
	}

	// The live record is untouched, and opening again moves nothing.
	live, err = NewPortfolioSQLiteRepository(ctx, path, "CC0006")
	if err != nil {
		t.Fatal(err)
	}
	if updates, err := live.OrderHistory(ctx, "LIVE-1"); err != nil || len(updates) != 1 {
		t.Fatalf("live order updates = %+v, %v", updates, err)
	}
	_ = live.Close()
	repo, err = OpenUserPaperSQLiteRepository(ctx, dir, "CC0006")
	if err != nil {
		t.Fatal(err)
	}
	check(repo)
	_ = repo.Close()

	// The same account back in the old place (a move cut short) is renamed
	// aside again; another account there is refused, and left alone.
	if err := os.Rename(legacy+".migrated", legacy); err != nil {
		t.Fatal(err)
	}
	repo, err = OpenUserPaperSQLiteRepository(ctx, dir, "CC0006")
	if err != nil {
		t.Fatalf("reopening after a cut-short move: %v", err)
	}
	check(repo)
	_ = repo.Close()
	other, err := NewPaperSQLiteRepository(ctx, legacy, "CC0006")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.OpenAccount(ctx, models.PaperAccount{Capital: 1, CreatedAt: created.Add(time.Hour), SettledThrough: created}); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	if _, err := OpenUserPaperSQLiteRepository(ctx, dir, "CC0006"); err == nil || !strings.Contains(err.Error(), "another paper account") {
		t.Fatalf("two accounts error = %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("the other account was moved: %v", err)
	}
}

func TestPaperRecordsOrdersAndFills(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "CC0006_paper.db")
	repo, err := NewPaperSQLiteRepository(ctx, path, "cc0006")
	if err != nil {
		t.Fatalf("NewPaperSQLiteRepository() error = %v", err)
	}
	if _, err := repo.Account(ctx); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Account() before opening error = %v, want ErrNotFound", err)
	}
	created := time.Date(2026, 10, 22, 4, 0, 0, 0, time.UTC)
	settled := time.Date(2026, 10, 21, 0, 0, 0, 0, models.ExchangeLocation)
	account, err := repo.OpenAccount(ctx, models.PaperAccount{Capital: 1_000_000, CreatedAt: created, SettledThrough: settled})
	if err != nil || account.Capital != 1_000_000 || !account.CreatedAt.Equal(created) || !account.SettledThrough.Equal(settled) {
		t.Fatalf("OpenAccount() = %+v, %v", account, err)
	}
	// Opening again keeps the first account.
	if again, err := repo.OpenAccount(ctx, models.PaperAccount{Capital: 5, CreatedAt: created}); err != nil || again.Capital != 1_000_000 {
		t.Fatalf("OpenAccount() again = %+v, %v; want the stored account", again, err)
	}

	open := kite.Order{OrderID: "P1", Status: "OPEN", TradingSymbol: "INFY", Quantity: 10}
	done := open
	done.Status, done.FilledQuantity = "COMPLETE", 10
	other := kite.Order{OrderID: "P2", Status: "REJECTED"}
	expiry := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	later := models.PaperFill{TradeID: "T2", OrderID: "P1", Kind: models.PaperFillTrade, Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000CE",
		InstrumentToken: 9, Product: "NRML", TransactionType: "BUY", Quantity: 75, Price: 101.5, Charges: 23.4,
		FilledAt: created.Add(time.Hour), InstrumentType: "CE", Expiry: expiry, Strike: 25000, Underlying: "NSE:NIFTY 50", UnderlyingToken: 256265}
	earlier := models.PaperFill{TradeID: "T1", OrderID: "P1", Kind: models.PaperFillTrade, Exchange: "NSE", TradingSymbol: "INFY",
		InstrumentToken: 408065, Product: "CNC", TransactionType: "BUY", Quantity: 10, Price: 1500, FilledAt: created, InstrumentType: "EQ"}
	if err := repo.Record(ctx, PaperChange{Orders: []kite.Order{open}}); err != nil {
		t.Fatalf("Record(open) error = %v", err)
	}
	next := time.Date(2026, 10, 22, 0, 0, 0, 0, models.ExchangeLocation)
	if err := repo.Record(ctx, PaperChange{Orders: []kite.Order{other, done}, Fills: []models.PaperFill{later, earlier}, SettledThrough: next}); err != nil {
		t.Fatalf("Record(fills) error = %v", err)
	}

	orders, err := repo.Orders(ctx)
	if err != nil || len(orders) != 2 || orders[0].OrderID != "P1" || orders[0].Status != "COMPLETE" || orders[1].OrderID != "P2" {
		t.Fatalf("Orders() = %+v, %v; want P1 complete then P2", orders, err)
	}
	if history, err := repo.OrderHistory(ctx, "P1"); err != nil || len(history) != 2 || history[0].Status != "OPEN" {
		t.Fatalf("OrderHistory() = %+v, %v", history, err)
	}
	if _, err := repo.OrderHistory(ctx, "P9"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("OrderHistory(missing) error = %v, want ErrNotFound", err)
	}
	fills, err := repo.Fills(ctx)
	if err != nil || len(fills) != 2 {
		t.Fatalf("Fills() = %+v, %v", fills, err)
	}
	// Oldest first, whatever order they were recorded in.
	for index, want := range []models.PaperFill{earlier, later} {
		got := fills[index]
		if !got.FilledAt.Equal(want.FilledAt) || !got.Expiry.Equal(want.Expiry) {
			t.Fatalf("fill %d times = %v, %v; want %v, %v", index, got.FilledAt, got.Expiry, want.FilledAt, want.Expiry)
		}
		got.FilledAt, got.Expiry, want.FilledAt, want.Expiry = time.Time{}, time.Time{}, time.Time{}, time.Time{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fill %d = %+v; want %+v", index, got, want)
		}
	}
	if account, err := repo.Account(ctx); err != nil || !account.SettledThrough.Equal(next) {
		t.Fatalf("Account() after settling = %+v, %v", account, err)
	}

	if err := repo.Record(ctx, PaperChange{Fills: []models.PaperFill{{TradeID: "T3", OrderID: "P1"}}}); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("Record(bad fill) error = %v, want ErrInvalidInput", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPaperSQLiteRepository(ctx, path, "CC0006")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if fills, err := reopened.Fills(ctx); err != nil || len(fills) != 2 {
		t.Fatalf("reopened Fills() = %d, %v", len(fills), err)
	}
}
