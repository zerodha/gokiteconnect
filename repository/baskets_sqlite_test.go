package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/devshoe/gokiteconnect/models"
)

func basketItem(symbol, side string, quantity int) models.BasketItem {
	return models.BasketItem{Exchange: "NFO", TradingSymbol: symbol, TransactionType: side, OrderType: "MARKET",
		Product: "NRML", Quantity: quantity, Validity: "DAY", Tag: "kite-cli"}
}

// openBaskets opens the lists database at path and the baskets in it,
// with one list of AB1234's beside them.
func openBaskets(t *testing.T, path string) (*BasketsSQLiteRepository, *ListsSQLiteRepository) {
	t.Helper()
	ctx := context.Background()
	lists, err := NewListsSQLiteRepository(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lists.Close() })
	if got, _ := lists.GetLists(ctx, "AB1234"); len(got) == 0 {
		if _, err := lists.CreateList(ctx, models.List{CreatedBy: "AB1234", Name: "Strikes"}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := lists.Baskets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return store, lists
}

func TestBasketsCreateEditAndDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lists.sqlite3")
	store, lists := openBaskets(t, path)

	created, err := store.CreateBasket(ctx, models.Basket{CreatedBy: "AB1234", Name: " Iron fly ", Items: []models.BasketItem{
		basketItem("NIFTY25000CE", "SELL", 75), basketItem("NIFTY25000PE", "SELL", 75),
	}})
	if err != nil {
		t.Fatalf("CreateBasket() error = %v", err)
	}
	if created.ID == "" || created.Name != "Iron fly" || len(created.Items) != 2 || created.Items[0].ID == "" ||
		created.Items[1].TradingSymbol != "NIFTY25000PE" || created.Items[0].InstrumentID() != "NFO:NIFTY25000CE" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := store.CreateBasket(ctx, models.Basket{CreatedBy: "AB1234", Name: "Iron fly"}); !errors.Is(err, models.ErrAlreadyExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := store.CreateBasket(ctx, models.Basket{CreatedBy: "CD5678", Name: "Iron fly"}); err != nil {
		t.Fatalf("another user's basket by the same name: %v", err)
	}
	if _, err := store.CreateBasket(ctx, models.Basket{CreatedBy: "AB1234", Name: "Bad", Items: []models.BasketItem{{Exchange: "NSE"}}}); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("invalid item error = %v", err)
	}

	// Items append in order, change in place and are removed by ID.
	if err := store.AddBasketItems(ctx, created.ID, []models.BasketItem{basketItem("NIFTY24500PE", "BUY", 75), basketItem("NIFTY25500CE", "BUY", 75)}); err != nil {
		t.Fatal(err)
	}
	changed := created.Items[0]
	changed.OrderType, changed.Price, changed.Quantity = "LIMIT", 110.5, 150
	if err := store.UpdateBasketItem(ctx, created.ID, changed); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBasketItems(ctx, created.ID, []string{created.Items[1].ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameBasket(ctx, created.ID, "Fly"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBasket(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var symbols []string
	for _, item := range got.Items {
		symbols = append(symbols, item.TradingSymbol)
	}
	if got.Name != "Fly" || len(got.Items) != 3 || symbols[0] != "NIFTY25000CE" || symbols[1] != "NIFTY24500PE" || symbols[2] != "NIFTY25500CE" ||
		got.Items[0].Price != 110.5 || got.Items[0].Quantity != 150 || got.Items[0].OrderType != "LIMIT" || !got.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("got = %+v", got)
	}
	if err := store.UpdateBasketItem(ctx, created.ID, models.BasketItem{ID: "missing", Exchange: "NSE", TradingSymbol: "INFY",
		TransactionType: "BUY", OrderType: "MARKET", Product: "CNC", Quantity: 1}); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("missing item error = %v", err)
	}

	// Each user sees their own; the store keeps them across opens, and
	// the lists beside them are untouched.
	_ = lists.Close()
	store, lists = openBaskets(t, path)
	if got, err := lists.GetLists(ctx, "AB1234"); err != nil || len(got) != 1 || got[0].Name != "Strikes" {
		t.Fatalf("GetLists() = %+v, %v", got, err)
	}
	mine, err := store.GetBaskets(ctx, "AB1234")
	if err != nil || len(mine) != 1 || len(mine[0].Items) != 3 {
		t.Fatalf("GetBaskets(AB1234) = %+v, %v", mine, err)
	}
	all, err := store.GetBaskets(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("GetBaskets() = %d, %v", len(all), err)
	}

	if err := store.DeleteBasket(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetBasket(ctx, created.ID); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("deleted basket error = %v", err)
	}
	if err := store.DeleteBasket(ctx, created.ID); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
	if err := store.AddBasketItems(ctx, created.ID, []models.BasketItem{basketItem("X", "BUY", 1)}); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("add to deleted error = %v", err)
	}
}
