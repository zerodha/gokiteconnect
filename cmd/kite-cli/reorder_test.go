package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/devshoe/gokiteconnect/models"
)

var shiftDown = tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}

// itemIDs are a stored list's instruments, in order.
func itemIDs(t *testing.T, store listStore, id string) []models.InstrumentID {
	t.Helper()
	lists, err := store.GetLists(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range lists {
		if list.ID == id {
			return list.InstrumentIDs()
		}
	}
	t.Fatalf("no list %s", id)
	return nil
}

func TestWatchlistsAndListsMove(t *testing.T) {
	h, lists, _ := watchlistHarness(t)
	e := h.a.explorer
	nifty, reliance, tcs := models.InstrumentID("NSE:NIFTY 50"), models.InstrumentID("NSE:RELIANCE"), models.InstrumentID("NSE:TCS")
	mine, err := lists.CreateList(context.Background(), models.List{CreatedBy: "AB1234", Name: "Mine", Items: []models.ListItem{
		{InstrumentID: nifty}, {InstrumentID: reliance}, {InstrumentID: tcs},
	}})
	if err != nil {
		t.Fatal(err)
	}

	// The default watchlist is smart: it keeps its parent's order.
	h.press("J")
	if !strings.Contains(h.screen(), "follows its parent's order") {
		t.Fatalf("a smart watchlist moved:\n%s", h.screen())
	}

	// J, K and shift+arrows move the selected instrument, stopping at the
	// ends, and the store keeps the order.
	h.run(h.a.loadWatchlists(mine.ID))
	h.waitFor("Mine", func() bool { return e.currentWatchlistID() == mine.ID && len(e.resultItems) == 3 && h.idle() })
	for _, step := range []struct {
		key  any
		want []models.InstrumentID
	}{
		{"J", []models.InstrumentID{reliance, nifty, tcs}},
		{shiftDown, []models.InstrumentID{reliance, tcs, nifty}},
		{shiftDown, []models.InstrumentID{reliance, tcs, nifty}},
		{"K", []models.InstrumentID{reliance, nifty, tcs}},
	} {
		if key, ok := step.key.(string); ok {
			h.press(key)
		} else {
			h.dispatch(step.key.(tea.KeyPressMsg))
		}
		if got := shownIDs(e); !slices.Equal(got, step.want) {
			t.Fatalf("after %v shown = %v, want %v", step.key, got, step.want)
		}
		if selected, _ := e.selectedInstrument(); selected.ID != nifty {
			t.Fatalf("after %v the selection moved to %s", step.key, selected.ID)
		}
		h.waitFor("the stored order", func() bool { return slices.Equal(itemIDs(t, lists, mine.ID), step.want) })
	}

	// A sorted table does not move.
	h.run(h.a.setResultSort(resultSort{column: 2}))
	h.press("J")
	if !strings.Contains(h.screen(), "pick List order") || !slices.Equal(itemIDs(t, lists, mine.ID), []models.InstrumentID{reliance, nifty, tcs}) {
		t.Fatalf("a sorted watchlist moved:\n%s", h.screen())
	}
	h.run(h.a.setResultSort(resultSort{}))

	// On the lists view, J moves the selected list, which reorders the
	// watchlists, and the store numbers them again.
	h.press("l")
	h.waitFor("the lists", func() bool { return e.mode == viewLists && h.idle() })
	if list, _ := e.selectedList(); list.ID == mine.ID {
		t.Fatalf("Mine is selected first")
	}
	h.press("J")
	if list, _ := e.selectedList(); list.ID == mine.ID || e.listsGrid.cursor != 1 || e.watchlists[0].ID != mine.ID {
		t.Fatalf("after J selected %s at %d, watchlists %v", list.Name, e.listsGrid.cursor, e.watchlists)
	}
	h.waitFor("the stored lists", func() bool {
		own, _ := lists.GetLists(context.Background(), "AB1234")
		return len(own) == 2 && own[0].ID == mine.ID && own[0].Number == 1 && own[1].Number == 2
	})

	// With the items focused, J moves the selected item.
	h.press("up", "tab", "J")
	want := []models.InstrumentID{nifty, reliance, tcs}
	if shown, _ := e.listByID(mine.ID); !slices.Equal(shown.InstrumentIDs(), want) {
		t.Fatalf("items shown %v, want %v", shown.InstrumentIDs(), want)
	}
	if id, _ := e.selectedListItem(); id != reliance {
		t.Fatalf("selected %s after moving RELIANCE", id)
	}
	h.waitFor("the stored items", func() bool { return slices.Equal(itemIDs(t, lists, mine.ID), want) })
}

func TestAFailedListMoveReloadsTheList(t *testing.T) {
	store, quotes := niftyMarket()
	lists := &fakeListStore{lists: []models.List{{ID: "mine", CreatedBy: "AB1234", Number: 1, Name: "Mine", Items: []models.ListItem{
		{InstrumentID: "NSE:NIFTY 50"}, {InstrumentID: "NSE:RELIANCE"},
	}}}, moveErr: errors.New("disk full")}
	h := newHarness(t, explorerDeps(t, store, quotes, lists, &fakePlacer{}))
	openExplorer(h)
	e := h.a.explorer
	h.waitFor("Mine", func() bool { return e.watching && len(e.resultItems) == 2 && h.idle() })

	h.press("J")
	h.waitFor("the error", h.shows("disk full"))
	h.waitFor("the stored order", func() bool {
		return slices.Equal(shownIDs(e), []models.InstrumentID{"NSE:NIFTY 50", "NSE:RELIANCE"})
	})
}

func TestBasketsMove(t *testing.T) {
	deps, _, baskets := basketDeps(t)
	ctx := context.Background()
	order := func(symbol string) models.BasketItem {
		return models.BasketItem{Exchange: "NFO", TradingSymbol: symbol, TransactionType: "BUY", OrderType: "MARKET",
			Product: "NRML", Quantity: 75, Validity: "DAY"}
	}
	fly, err := baskets.CreateBasket(ctx, models.Basket{CreatedBy: "AB1234", Name: "Fly", Items: []models.BasketItem{
		order("NIFTYW125000CE"), order("NIFTYW125000PE"), order("NIFTYW124500PE"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Condor", "Strangle"} {
		if _, err := baskets.CreateBasket(ctx, models.Basket{CreatedBy: "AB1234", Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.run(h.a.openBaskets())
	h.waitFor("the baskets", h.shows("Strangle"))
	s := h.a.baskets
	names := func(baskets []models.Basket) []string {
		var names []string
		for _, basket := range baskets {
			names = append(names, basket.Name)
		}
		return names
	}
	selectBasket := func(name string) {
		t.Helper()
		index := slices.IndexFunc(s.shown(), func(b models.Basket) bool { return b.Name == name })
		if index < 0 || !s.baskets.selectWhere(func(r row) bool { return r.ref == s.shown()[index].ID }) {
			t.Fatalf("no basket %s", name)
		}
	}

	// J moves the selected basket; the store keeps the order.
	h.press("J")
	if got := names(s.shown()); !slices.Equal(got, []string{"Condor", "Fly", "Strangle"}) {
		t.Fatalf("shown %v", got)
	}
	if basket, _ := s.selected(); basket.ID != fly.ID {
		t.Fatalf("selected %s after moving Fly", basket.Name)
	}
	h.waitFor("the stored order", func() bool { return slices.Equal(names(stored(t, baskets)), []string{"Condor", "Fly", "Strangle"}) })

	// Drafts move among themselves, after the stored baskets.
	h.press("n", "enter", "n", "enter")
	selectBasket("Basket 5")
	h.press("K", "K")
	if got := names(s.shown()); !slices.Equal(got, []string{"Condor", "Fly", "Strangle", "Basket 5", "Basket 4"}) {
		t.Fatalf("shown %v", got)
	}
	selectBasket("Strangle")
	h.press("J")
	if got := names(s.shown()); !slices.Equal(got, []string{"Condor", "Fly", "Strangle", "Basket 5", "Basket 4"}) {
		t.Fatalf("a stored basket moved among the drafts: %v", got)
	}

	// With the orders focused, J and K move the selected order.
	selectBasket("Fly")
	h.run(h.a.basketsChanged(""))
	h.press("tab", "J", "J", "K")
	symbols := func(items []models.BasketItem) []string {
		var symbols []string
		for _, item := range items {
			symbols = append(symbols, item.TradingSymbol)
		}
		return symbols
	}
	want := []string{"NIFTYW125000PE", "NIFTYW125000CE", "NIFTYW124500PE"}
	if basket, _ := s.selected(); !slices.Equal(symbols(basket.Items), want) {
		t.Fatalf("shown orders %v, want %v", symbols(basket.Items), want)
	}
	if item, _ := s.selectedItem(); item.TradingSymbol != "NIFTYW125000CE" {
		t.Fatalf("selected %s after moving the call", item.TradingSymbol)
	}
	h.waitFor("the stored orders", func() bool {
		for _, basket := range stored(t, baskets) {
			if basket.ID == fly.ID {
				return slices.Equal(symbols(basket.Items), want)
			}
		}
		return false
	})
}
