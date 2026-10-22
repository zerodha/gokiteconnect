package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

func openPortfolio(t *testing.T, ctx context.Context, path, userID string) *PortfolioSQLiteRepository {
	t.Helper()
	repo, err := NewPortfolioSQLiteRepository(ctx, path, userID)
	if err != nil {
		t.Fatalf("NewPortfolioSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// tick makes repo's clock advance a second per read from start.
func tick(repo *PortfolioSQLiteRepository, start time.Time) {
	now := start
	repo.now = func() time.Time { now = now.Add(time.Second); return now }
}

func orderIDs(updates []PortfolioOrderUpdate) []string {
	var ids []string
	for _, u := range updates {
		ids = append(ids, u.Order.OrderID+":"+u.Order.Status)
	}
	return ids
}

func TestPortfolioOrderUpdates(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", " cc0006 ")
	if repo.UserID() != "CC0006" {
		t.Fatalf("UserID() = %q, want CC0006", repo.UserID())
	}
	tick(repo, time.Date(2026, 10, 22, 9, 15, 0, 0, time.UTC))

	placed := models.Time{Time: time.Date(2026, 10, 22, 9, 15, 3, 0, models.ExchangeLocation)}
	first := []kite.Order{
		{OrderID: "1", Status: "open", Tag: "algo", TradingSymbol: "SBIN", OrderTimestamp: placed, Quantity: 10},
		{OrderID: "2", Status: "OPEN", Tags: []string{"swing", "algo"}, TradingSymbol: "INFY"},
		{OrderID: "3", Status: "OPEN", TradingSymbol: "TCS", Meta: map[string]any{"iceberg": map[string]any{"legs": float64(2)}}},
	}
	stored, err := repo.RecordOrderUpdates(ctx, first...)
	if err != nil || stored != 3 {
		t.Fatalf("RecordOrderUpdates() = %d, %v; want 3", stored, err)
	}
	// The same states again store nothing.
	if stored, err := repo.RecordOrderUpdates(ctx, first...); err != nil || stored != 0 {
		t.Fatalf("repeat RecordOrderUpdates() = %d, %v; want 0", stored, err)
	}
	// 1 completes, then a late OPEN arrives; 2 is cancelled; 3 stays open.
	later := []kite.Order{
		{OrderID: "1", Status: "COMPLETE", Tag: "algo", TradingSymbol: "SBIN", FilledQuantity: 10},
		{OrderID: "1", Status: "OPEN", Tag: "algo", TradingSymbol: "SBIN", FilledQuantity: 5},
		{OrderID: "2", Status: "CANCELLED", Tags: []string{"swing", "algo"}, TradingSymbol: "INFY"},
	}
	if stored, err := repo.RecordOrderUpdates(ctx, later...); err != nil || stored != 3 {
		t.Fatalf("later RecordOrderUpdates() = %d, %v; want 3", stored, err)
	}

	history, err := repo.OrderHistory(ctx, "1")
	if err != nil {
		t.Fatalf("OrderHistory() error = %v", err)
	}
	if got, want := orderIDs(history), []string{"1:OPEN", "1:COMPLETE", "1:OPEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OrderHistory() = %v, want %v", got, want)
	}
	if !history[0].Order.OrderTimestamp.Equal(placed.Time) || history[0].Order.Quantity != 10 || history[0].RecordedAt.IsZero() {
		t.Fatalf("first update = %#v", history[0])
	}

	latest, err := repo.LatestOrderUpdate(ctx, "1")
	if err != nil || latest.Order.Status != "COMPLETE" || latest.Order.FilledQuantity != 10 {
		t.Fatalf("LatestOrderUpdate(1) = %#v, %v; want the COMPLETE update", latest.Order, err)
	}
	if _, err := repo.LatestOrderUpdate(ctx, "9"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("LatestOrderUpdate(9) error = %v, want ErrNotFound", err)
	}

	open, err := repo.OpenOrders(ctx)
	if err != nil {
		t.Fatalf("OpenOrders() error = %v", err)
	}
	if got := orderIDs(open); !reflect.DeepEqual(got, []string{"3:OPEN"}) {
		t.Fatalf("OpenOrders() = %v, want [3:OPEN]", got)
	}
	if legs := open[0].Order.Meta["iceberg"].(map[string]any)["legs"]; legs != float64(2) {
		t.Fatalf("meta = %#v", open[0].Order.Meta)
	}

	for name, test := range map[string]struct {
		filter models.OrderUpdateFilter
		want   []string
	}{
		"tag or tags": {models.OrderUpdateFilter{Tags: []string{"algo"}, LatestOnly: true}, []string{"1:COMPLETE", "2:CANCELLED"}},
		"tags only":   {models.OrderUpdateFilter{Tags: []string{"swing"}}, []string{"2:OPEN", "2:CANCELLED"}},
		"statuses":    {models.OrderUpdateFilter{Statuses: []string{" open "}}, []string{"1:OPEN", "2:OPEN", "3:OPEN", "1:OPEN"}},
		"latest open": {models.OrderUpdateFilter{Statuses: []string{"OPEN"}, LatestOnly: true}, []string{"3:OPEN"}},
		"order IDs":   {models.OrderUpdateFilter{OrderIDs: []string{"2", "3", "2"}, LatestOnly: true}, []string{"3:OPEN", "2:CANCELLED"}},
	} {
		updates, err := repo.OrderUpdates(ctx, test.filter)
		if err != nil {
			t.Fatalf("%s: OrderUpdates() error = %v", name, err)
		}
		if got := orderIDs(updates); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: OrderUpdates() = %v, want %v", name, got, test.want)
		}
	}

	if _, err := repo.RecordOrderUpdates(ctx, kite.Order{OrderID: "4"}); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("statusless RecordOrderUpdates() error = %v, want ErrInvalidInput", err)
	}
	if _, err := repo.RecordOrderUpdates(ctx, kite.Order{OrderID: "4", Status: "OPEN", AccountID: "AB1234"}); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("other account RecordOrderUpdates() error = %v, want ErrInvalidInput", err)
	}
	if stored, err := repo.RecordOrderUpdates(ctx, kite.Order{OrderID: "4", Status: "OPEN", AccountID: "cc0006"}); err != nil || stored != 1 {
		t.Fatalf("own account RecordOrderUpdates() = %d, %v; want 1", stored, err)
	}
}

func TestPortfolioSnapshots(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	start := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	tick(repo, start)

	if _, err := repo.LatestSnapshot(ctx, models.SnapshotHoldings); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("empty LatestSnapshot() error = %v, want ErrNotFound", err)
	}
	holdings := kite.Holdings{{Tradingsymbol: "SBIN", Exchange: "NSE", Quantity: 10}}
	if _, err := repo.SaveSnapshot(ctx, models.SnapshotHoldings, kite.Holdings{}); err != nil { // 09:00:01
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	saved, err := repo.SaveSnapshot(ctx, models.SnapshotHoldings, holdings) // 09:00:02
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	margins := kite.AllMargins{Equity: kite.Margins{Enabled: true, Net: 1234.5}}
	if _, err := repo.SaveSnapshot(ctx, models.SnapshotMargins, margins); err != nil { // 09:00:03
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	if _, err := repo.SaveSnapshot(ctx, "funds", margins); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("unknown SaveSnapshot() error = %v, want ErrInvalidInput", err)
	}

	latest, err := repo.LatestSnapshot(ctx, models.SnapshotHoldings)
	if err != nil || latest.ID != saved.ID || !latest.TakenAt.Equal(start.Add(2*time.Second)) {
		t.Fatalf("LatestSnapshot() = %#v, %v; want %#v", latest, err, saved)
	}
	var gotHoldings kite.Holdings
	if err := latest.Decode(&gotHoldings); err != nil || len(gotHoldings) != 1 || gotHoldings[0].Tradingsymbol != "SBIN" || gotHoldings[0].Quantity != 10 {
		t.Fatalf("Decode() = %#v, %v", gotHoldings, err)
	}
	var gotMargins kite.AllMargins
	if m, err := repo.LatestSnapshot(ctx, models.SnapshotMargins); err != nil || m.Decode(&gotMargins) != nil || gotMargins.Equity.Net != 1234.5 {
		t.Fatalf("margins snapshot = %#v, %v", gotMargins, err)
	}

	all, err := repo.Snapshots(ctx, models.SnapshotHoldings, time.Time{}, time.Time{})
	if err != nil || len(all) != 2 || all[0].ID >= all[1].ID {
		t.Fatalf("Snapshots() = %v, %v; want both holdings snapshots, oldest first", all, err)
	}
	ranged, err := repo.Snapshots(ctx, models.SnapshotHoldings, start.Add(2*time.Second), start.Add(time.Hour))
	if err != nil || len(ranged) != 1 || ranged[0].ID != saved.ID {
		t.Fatalf("ranged Snapshots() = %v, %v; want the second", ranged, err)
	}

	times, err := repo.LastSnapshotTimes(ctx)
	if err != nil {
		t.Fatalf("LastSnapshotTimes() error = %v", err)
	}
	if len(times) != 2 || !times[models.SnapshotHoldings].Equal(start.Add(2*time.Second)) || !times[models.SnapshotMargins].Equal(start.Add(3*time.Second)) {
		t.Fatalf("LastSnapshotTimes() = %v", times)
	}

	// Pruning everything still keeps the last of each type.
	deleted, err := repo.DeleteSnapshotsBefore(ctx, start.Add(time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteSnapshotsBefore() = %d, %v; want 1", deleted, err)
	}
	if latest, err := repo.LatestSnapshot(ctx, models.SnapshotHoldings); err != nil || latest.ID != saved.ID {
		t.Fatalf("LatestSnapshot() after prune = %#v, %v", latest, err)
	}
}

func TestPortfolioDirectoryKeepsOneDatabasePerUser(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "portfolio")
	if path, err := PortfolioDatabasePath(dir, "cc0006"); err != nil || path != filepath.Join(dir, "CC0006.db") {
		t.Fatalf("PortfolioDatabasePath() = %q, %v", path, err)
	}
	for _, bad := range []string{"", "../CC0006", "CC 0006", "a/b"} {
		if _, err := PortfolioDatabasePath(dir, bad); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("PortfolioDatabasePath(%q) error = %v, want ErrInvalidInput", bad, err)
		}
	}

	portfolios, err := NewPortfolioSQLiteDirectory(dir)
	if err != nil {
		t.Fatalf("NewPortfolioSQLiteDirectory() error = %v", err)
	}
	if users, err := portfolios.Users(); err != nil || len(users) != 0 {
		t.Fatalf("Users() before any = %v, %v", users, err)
	}
	a, err := portfolios.User(ctx, "CC0006")
	if err != nil {
		t.Fatalf("User() error = %v", err)
	}
	if again, err := portfolios.User(ctx, "cc0006"); err != nil || again != a {
		t.Fatalf("User() again = %p, %v; want the open %p", again, err, a)
	}
	b, err := portfolios.User(ctx, "AB1234")
	if err != nil {
		t.Fatalf("User() error = %v", err)
	}
	if _, err := a.RecordOrderUpdates(ctx, kite.Order{OrderID: "1", Status: "OPEN"}); err != nil {
		t.Fatalf("RecordOrderUpdates() error = %v", err)
	}
	if updates, err := b.OrderUpdates(ctx, models.OrderUpdateFilter{}); err != nil || len(updates) != 0 {
		t.Fatalf("other user's OrderUpdates() = %v, %v; want none", updates, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory = %v, %v; want mode 0700", info, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A paper database is not a user, even named in capitals.
	if err := os.WriteFile(filepath.Join(dir, "CC0006_PAPER.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if users, err := portfolios.Users(); err != nil || !reflect.DeepEqual(users, []string{"AB1234", "CC0006"}) {
		t.Fatalf("Users() = %v, %v", users, err)
	}

	if err := portfolios.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := portfolios.User(ctx, "CC0006"); err == nil {
		t.Fatal("User() after Close succeeded")
	}

	// The file outlives the directory handle.
	reopened := openPortfolio(t, ctx, filepath.Join(dir, "CC0006.db"), "CC0006")
	if latest, err := reopened.LatestOrderUpdate(ctx, "1"); err != nil || latest.Order.Status != "OPEN" {
		t.Fatalf("reopened LatestOrderUpdate() = %#v, %v", latest.Order, err)
	}
}
