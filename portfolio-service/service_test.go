package portfolio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

// fakeSource serves a fixed account; failing makes one type's read fail.
type fakeSource struct {
	profile kite.UserProfile
	orders  kite.Orders
	history map[string][]kite.Order
	failing models.SnapshotType
}

func (f *fakeSource) fail(t models.SnapshotType) error {
	if f.failing == t {
		return errors.New("kite is down")
	}
	return nil
}

func (f *fakeSource) GetUserProfile() (kite.UserProfile, error) {
	return f.profile, f.fail(models.SnapshotProfile)
}
func (f *fakeSource) GetUserMargins() (kite.AllMargins, error) {
	return kite.AllMargins{Equity: kite.Margins{Net: 5000}}, f.fail(models.SnapshotMargins)
}
func (f *fakeSource) GetHoldings() (kite.Holdings, error) {
	return kite.Holdings{{Tradingsymbol: "SBIN", Exchange: "NSE", Quantity: 10}}, f.fail(models.SnapshotHoldings)
}
func (f *fakeSource) GetPositions() (kite.Positions, error) {
	return kite.Positions{Net: []kite.Position{{Tradingsymbol: "NIFTY26OCTFUT", Quantity: 75}}}, f.fail(models.SnapshotPositions)
}
func (f *fakeSource) GetOrders() (kite.Orders, error) { return f.orders, f.fail(models.SnapshotOrders) }
func (f *fakeSource) GetTrades() (kite.Trades, error) {
	return kite.Trades{}, f.fail(models.SnapshotTrades)
}
func (f *fakeSource) GetMFHoldings() (kite.MFHoldings, error) {
	return kite.MFHoldings{}, f.fail(models.SnapshotMFHoldings)
}
func (f *fakeSource) GetOrderHistory(orderID string) ([]kite.Order, error) {
	return f.history[orderID], nil
}

func newService(t *testing.T, dir string, sources map[string]*fakeSource) *Service {
	t.Helper()
	directory, err := repository.NewPortfolioSQLiteDirectory(dir)
	if err != nil {
		t.Fatalf("NewPortfolioSQLiteDirectory() error = %v", err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return New(DirectoryStore(directory), func(_ context.Context, userID string) (Source, error) {
		source, ok := sources[userID]
		if !ok {
			return nil, errors.New("no session")
		}
		return source, nil
	}, nil)
}

func TestSyncStoresEachUserInItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "portfolio")
	cc := &fakeSource{
		profile: kite.UserProfile{UserID: "CC0006"},
		orders:  kite.Orders{{OrderID: "1", Status: "OPEN"}, {OrderID: "2", Status: "COMPLETE"}},
	}
	ab := &fakeSource{profile: kite.UserProfile{UserID: "AB1234"}, failing: models.SnapshotMFHoldings}
	service := newService(t, dir, map[string]*fakeSource{"CC0006": cc, "AB1234": ab})

	result, err := service.Sync(ctx, "cc0006")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.UserID != "CC0006" || len(result.Snapshots) != len(models.SnapshotTypes) || result.OrderUpdates != 2 {
		t.Fatalf("Sync() = %+v", result)
	}
	holdings, takenAt, err := Latest[kite.Holdings](ctx, service, "CC0006", models.SnapshotHoldings)
	if err != nil || len(holdings) != 1 || holdings[0].Tradingsymbol != "SBIN" || takenAt.IsZero() {
		t.Fatalf("Latest holdings = %v, %v, %v", holdings, takenAt, err)
	}
	open, err := service.OpenOrders(ctx, "CC0006")
	if err != nil || len(open) != 1 || open[0].OrderID != "1" {
		t.Fatalf("OpenOrders() = %v, %v; want order 1", open, err)
	}
	// Syncing the same order book again records no new states.
	if again, err := service.Sync(ctx, "CC0006", models.SnapshotOrders); err != nil || again.OrderUpdates != 0 || len(again.Snapshots) != 1 {
		t.Fatalf("second Sync() = %+v, %v", again, err)
	}

	// A failed type is reported and the rest are stored.
	partial, err := service.Sync(ctx, "AB1234")
	if err == nil || partial.Errors[models.SnapshotMFHoldings] == nil || len(partial.Snapshots) != len(models.SnapshotTypes)-1 {
		t.Fatalf("partial Sync() = %+v, %v", partial, err)
	}
	if _, err := service.LatestSnapshot(ctx, "AB1234", models.SnapshotMFHoldings); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("failed type LatestSnapshot() error = %v, want ErrNotFound", err)
	}
	if orders, err := service.OpenOrders(ctx, "AB1234"); err != nil || len(orders) != 0 {
		t.Fatalf("AB1234 OpenOrders() = %v, %v; want none of CC0006's", orders, err)
	}

	for _, name := range []string{"CC0006.db", "AB1234.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("database %s: %v", name, err)
		}
	}
	synced, err := service.LastSynced(ctx, "CC0006")
	if err != nil || len(synced) != len(models.SnapshotTypes) {
		t.Fatalf("LastSynced() = %v, %v", synced, err)
	}
}

func TestSyncRefusesAnotherAccountsSession(t *testing.T) {
	ctx := context.Background()
	wrong := &fakeSource{profile: kite.UserProfile{UserID: "AB1234"}, orders: kite.Orders{{OrderID: "1", Status: "OPEN"}}}
	service := newService(t, t.TempDir(), map[string]*fakeSource{"CC0006": wrong})

	if _, err := service.Sync(ctx, "CC0006"); err == nil {
		t.Fatal("Sync() with another account's session succeeded")
	}
	if synced, err := service.LastSynced(ctx, "CC0006"); err != nil || len(synced) != 0 {
		t.Fatalf("LastSynced() = %v, %v; want nothing stored", synced, err)
	}
}

func TestSyncRejectsBadRequests(t *testing.T) {
	ctx := context.Background()
	service := newService(t, t.TempDir(), nil)
	if _, err := service.Sync(ctx, "../etc"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad user Sync() error = %v, want ErrInvalid", err)
	}
	if _, err := service.Sync(ctx, "CC0006", "funds"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad type Sync() error = %v, want ErrInvalid", err)
	}
	if _, err := service.Sync(ctx, "CC0006"); err == nil || errors.Is(err, ErrInvalid) {
		t.Errorf("sessionless Sync() error = %v, want a session failure", err)
	}
	offline := New(service.store, nil, nil)
	if _, err := offline.Sync(ctx, "CC0006"); !errors.Is(err, ErrInvalid) {
		t.Errorf("sourceless Sync() error = %v, want ErrInvalid", err)
	}
}

func TestOrderUpdatesAndRefresh(t *testing.T) {
	ctx := context.Background()
	source := &fakeSource{history: map[string][]kite.Order{
		"7": {{OrderID: "7", Status: "OPEN"}, {OrderID: "7", Status: "CANCELLED"}},
	}}
	service := newService(t, t.TempDir(), map[string]*fakeSource{"CC0006": source})

	// The ticker's update arrives first.
	if stored, err := service.RecordOrderUpdates(ctx, "CC0006", kite.Order{OrderID: "7", Status: "OPEN"}); err != nil || stored != 1 {
		t.Fatalf("RecordOrderUpdates() = %d, %v", stored, err)
	}
	if order, err := service.LatestOrder(ctx, "CC0006", "7"); err != nil || order.Status != "OPEN" {
		t.Fatalf("LatestOrder() = %+v, %v", order, err)
	}
	order, err := service.RefreshOrder(ctx, "CC0006", "7")
	if err != nil || order.Status != "CANCELLED" {
		t.Fatalf("RefreshOrder() = %+v, %v; want CANCELLED", order, err)
	}
	history, err := service.OrderHistory(ctx, "CC0006", "7")
	if err != nil || len(history) != 2 {
		t.Fatalf("OrderHistory() = %v, %v; want OPEN then CANCELLED", history, err)
	}
	if _, err := service.LatestOrder(ctx, "CC0006", "8"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("unknown LatestOrder() error = %v, want ErrNotFound", err)
	}
}
