package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
	"github.com/devshoe/gokiteconnect/repository"
)

func basketDeps(t *testing.T) (appDeps, *fakePlacer, *repository.BasketsSQLiteRepository) {
	t.Helper()
	store, quotes := niftyMarket()
	placer := &fakePlacer{}
	deps := explorerDeps(t, store, quotes, nil, placer)
	lists, err := repository.NewListsSQLiteRepository(context.Background(), ":memory:", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lists.Close() })
	baskets, err := lists.Baskets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deps.baskets = baskets
	deps.basketMargins = func(_ context.Context, legs []kite.OrderMarginParam) (kite.BasketMargins, error) {
		var margins kite.BasketMargins
		margins.Initial.Total, margins.Final.Total = 200000, 150000
		return margins, nil
	}
	return deps, placer, baskets
}

// stored is AB1234's baskets as the store keeps them.
func stored(t *testing.T, baskets *repository.BasketsSQLiteRepository) []models.Basket {
	t.Helper()
	got, err := baskets.GetBaskets(context.Background(), "AB1234")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// addToBasket opens the order form's Add to basket picker; the button is
// after Place.
func addToBasket(h *harness) *menu {
	h.t.Helper()
	if _, ok := h.a.overlay.(*orderForm); !ok {
		h.t.Fatalf("no order form: %T", h.a.overlay)
	}
	h.press("down", "down", "down", "down", "enter")
	var picker *menu
	h.waitFor("the basket picker", func() bool { picker, _ = h.a.overlay.(*menu); return picker != nil })
	return picker
}

func TestBasketsDraftSaveEditAndPlace(t *testing.T) {
	deps, placer, baskets := basketDeps(t)
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))

	// A NIFTY call buy goes to a new basket, which stays a draft: nothing
	// is stored and nothing placed.
	h.press("+", "c")
	strikes := h.a.overlay.(*strikePicker)
	h.waitFor("priced strikes", func() bool { _, ok := strikes.selected(); return ok && strikes.spot() > 0 })
	h.press("enter")
	picker := addToBasket(h)
	if !strings.Contains(h.screen(), "+ New basket") || len(picker.items) != 1 {
		t.Fatalf("picker:\n%s", h.screen())
	}
	// Esc goes back to the order form, and Add to basket offers it again.
	h.press("esc")
	if _, ok := h.a.overlay.(*orderForm); !ok {
		t.Fatalf("Esc on the picker opened %T", h.a.overlay)
	}
	h.press("up", "up", "up", "up")
	addToBasket(h)
	h.press("enter")
	if !strings.Contains(h.screen(), "Basket 1") {
		t.Fatalf("no suggested name:\n%s", h.screen())
	}
	h.press("enter")
	if !h.idle() || len(h.a.baskets.drafts) != 1 || len(h.a.baskets.drafts[0].Items) != 1 || len(stored(t, baskets)) != 0 || len(placer.placed()) != 0 {
		t.Fatalf("overlay %T, drafts %+v", h.a.overlay, h.a.baskets.drafts)
	}
	if !strings.Contains(h.screen(), "UNSAVED 1") || !strings.Contains(h.screen(), "S on Baskets saves it") {
		t.Errorf("no unsaved badge:\n%s", h.screen())
	}

	// A RELIANCE sell joins the draft: the picker lists it after New.
	h.press("down", "+", "s")
	addToBasket(h)
	if !strings.Contains(h.screen(), "Basket 1 *") {
		t.Fatalf("picker lacks the draft:\n%s", h.screen())
	}
	h.press("down", "enter")
	if draft := h.a.baskets.drafts[0]; len(draft.Items) != 2 || draft.Items[1].TransactionType != kite.TransactionTypeSell {
		t.Fatalf("draft = %+v", draft)
	}

	// B shows the Baskets tab: both orders, priced, with the hedged margin.
	h.press("b")
	if h.a.screen != screenBaskets {
		t.Fatalf("B showed screen %d", h.a.screen)
	}
	h.waitFor("the prices and margin", func() bool { return h.shows("hedges save")() && h.shows("Charges")() })
	for _, want := range []string{"NIFTYW125000CE", "RELIANCE", "2 orders", "Charges", "unsaved · S saves", "Baskets · AB1234"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("baskets lack %q:\n%s", want, h.screen())
		}
	}

	// Enter on an order edits it in the form, its account fixed; Save puts
	// it back in the draft.
	h.press("tab", "enter")
	var form *orderForm
	h.waitFor("the order form", func() bool { form, _ = h.a.overlay.(*orderForm); return form != nil })
	if form.ticket.item == nil || form.form.buttons[0] != "Save to basket" || !form.form.field(orderFieldAccount).disabled {
		t.Fatalf("form = %+v", form.ticket)
	}
	h.press("+", "down", "down", "down", "enter")
	if !h.idle() || h.a.baskets.drafts[0].Items[0].Quantity != 150 {
		t.Fatalf("overlay %T, draft %+v", h.a.overlay, h.a.baskets.drafts[0].Items)
	}

	// S stores it.
	h.press("s")
	h.waitFor("the saved basket", func() bool { return len(stored(t, baskets)) == 1 && h.idle() })
	saved := stored(t, baskets)[0]
	if saved.Name != "Basket 1" || len(saved.Items) != 2 || saved.Items[0].Quantity != 150 || len(h.a.baskets.drafts) != 0 {
		t.Fatalf("saved %+v, drafts %d", saved, len(h.a.baskets.drafts))
	}
	if h.a.baskets.focus != 0 {
		h.press("esc")
	}
	if strings.Contains(h.screen(), "UNSAVED") || !strings.Contains(h.screen(), "Saved Basket 1 with 2 orders") {
		t.Errorf("after saving:\n%s", h.screen())
	}

	// O asks, then places both in paper, buys first; the basket stays.
	h.press("o")
	if !strings.Contains(h.screen(), "Place all 2 orders of Basket 1 in AB1234's paper account?") {
		t.Fatalf("O did not ask:\n%s", h.screen())
	}
	h.press("left", "enter")
	h.waitFor("the orders", func() bool { return len(placer.placed()) == 2 && h.idle() })
	placed := placer.placed()
	if placed[0].params.Tradingsymbol != "NIFTYW125000CE" || placed[0].params.Quantity != 150 || placed[1].params.Tradingsymbol != "RELIANCE" ||
		placed[0].mode != broker.Paper || placed[1].mode != broker.Paper || placed[0].userID != "AB1234" || placed[0].params.Tag != orderTag {
		t.Fatalf("placed %+v", placed)
	}
	if !strings.Contains(h.screen(), "2 orders placed for AB1234 in the paper account") || !strings.Contains(h.screen(), "✔ 2510120001") ||
		len(stored(t, baskets)) != 1 {
		t.Fatalf("after placing:\n%s", h.screen())
	}

	// An order from Watchlists joins the stored basket at once.
	h.press("w", "+", "s")
	addToBasket(h)
	h.press("down", "enter")
	h.waitFor("the added order", func() bool { return h.idle() && len(stored(t, baskets)[0].Items) == 3 })

	// Rejections are shown per order.
	placer.fail(errors.New("insufficient funds"))
	h.press("b", "o", "left", "enter")
	h.waitFor("the rejections", h.shows("3 of 3 orders failed"))
	h.press("enter")
	if !strings.Contains(h.screen(), "✖ insufficient funds") {
		t.Fatalf("no rejection:\n%s", h.screen())
	}

	// E renames; D on an order removes it, D on the basket deletes it.
	h.press("e", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	h.typeText("Iron fly")
	h.press("enter")
	h.waitFor("the rename", func() bool { return h.idle() && stored(t, baskets)[0].Name == "Iron fly" })
	h.press("tab", "d")
	h.waitFor("the removal", func() bool { return h.idle() && len(stored(t, baskets)[0].Items) == 2 })
	h.press("esc", "d", "left", "enter")
	h.waitFor("the deletion", func() bool { return h.idle() && len(stored(t, baskets)) == 0 })
	if !strings.Contains(h.screen(), "No baskets yet") {
		t.Fatalf("after deleting:\n%s", h.screen())
	}
}

func TestBasketsNewDraftNamesAndDiscards(t *testing.T) {
	deps, _, baskets := basketDeps(t)
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.run(h.a.switchTo(screenBaskets))
	h.waitFor("the baskets", h.shows("No baskets yet"))

	// N names a draft; a taken name is refused.
	h.press("n", "enter")
	if len(h.a.baskets.drafts) != 1 || !strings.Contains(h.screen(), "Basket 1 *") {
		t.Fatalf("drafts %+v:\n%s", h.a.baskets.drafts, h.screen())
	}
	h.press("n", "backspace")
	h.typeText("1")
	h.press("enter")
	if _, ok := h.a.overlay.(*inputOverlay); !ok || !strings.Contains(h.screen(), "There is a basket named Basket 1 already") {
		t.Fatalf("a taken name was accepted:\n%s", h.screen())
	}
	h.press("esc")

	// An empty basket has nothing to place; D discards a draft without
	// touching the store.
	h.press("o")
	if !h.idle() || !strings.Contains(h.screen(), "Basket 1 has no orders") {
		t.Fatalf("O on an empty basket:\n%s", h.screen())
	}
	h.press("d", "left", "enter")
	if len(h.a.baskets.drafts) != 0 || len(stored(t, baskets)) != 0 || !strings.Contains(h.screen(), "Discarded Basket 1") {
		t.Fatalf("after discarding:\n%s", h.screen())
	}
}
