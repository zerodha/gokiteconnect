package main

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/devshoe/gokiteconnect/kite"
)

func TestPriceHints(t *testing.T) {
	buy, sell := kite.TransactionTypeBuy, kite.TransactionTypeSell
	for _, test := range []struct {
		side, orderType        string
		price, trigger, ltp    float64
		wantPrice, wantTrigger string
	}{
		{buy, kite.OrderTypeLimit, 110.05, 0, 110, hintImmediate, ""},
		{buy, kite.OrderTypeLimit, 110, 0, 110, "", ""},
		{sell, kite.OrderTypeLimit, 109.95, 0, 110, hintImmediate, ""},
		{sell, kite.OrderTypeLimit, 110.05, 0, 110, "", ""},
		{buy, kite.OrderTypeLimit, 120, 0, 0, "", ""},
		{buy, kite.OrderTypeSL, 125, 120, 110, "", ""},
		{buy, kite.OrderTypeSL, 125, 110, 110, "", hintTriggerAbove},
		{buy, kite.OrderTypeSL, 115, 120, 110, hintPriceAtLeast, ""},
		{sell, kite.OrderTypeSL, 95, 100, 110, "", ""},
		{sell, kite.OrderTypeSL, 95, 115, 110, "", hintTriggerBelow},
		{sell, kite.OrderTypeSL, 105, 100, 110, hintPriceAtMost, ""},
		{buy, kite.OrderTypeSLM, 0, 105, 110, "", hintTriggerAbove},
		{sell, kite.OrderTypeSLM, 0, 105, 110, "", ""},
		{buy, kite.OrderTypeSLM, 0, 0, 110, "", ""},
		{buy, kite.OrderTypeMarket, 200, 200, 110, "", ""},
	} {
		price, trigger := priceHints(test.side, test.orderType, test.price, test.trigger, test.ltp)
		if price != test.wantPrice || trigger != test.wantTrigger {
			t.Errorf("priceHints(%s %s, price %v, trigger %v, LTP %v) = %q, %q; want %q, %q", test.side, test.orderType,
				test.price, test.trigger, test.ltp, price, trigger, test.wantPrice, test.wantTrigger)
		}
	}
}

func TestOrderFormLotsHintsAndCosts(t *testing.T) {
	store, quotes := niftyMarket()
	placer := &fakePlacer{}
	deps := explorerDeps(t, store, quotes, nil, placer)
	var (
		mu   sync.Mutex
		legs []kite.OrderMarginParam
	)
	asked := func() []kite.OrderMarginParam { mu.Lock(); defer mu.Unlock(); return slices.Clone(legs) }
	deps.basketMargins = func(_ context.Context, order []kite.OrderMarginParam) (kite.BasketMargins, error) {
		mu.Lock()
		legs = append(legs, order...)
		mu.Unlock()
		var margins kite.BasketMargins
		margins.Initial.Total, margins.Final.Total = 150000, 150000
		return margins, nil
	}
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.press("+", "c")
	strikes := h.a.overlay.(*strikePicker)
	h.waitFor("priced strikes", func() bool { _, ok := strikes.selected(); return ok && strikes.spot() > 0 })
	h.press("enter")
	form := h.a.overlay.(*orderForm)
	quantity := form.form.field(orderFieldQuantity)

	// The margin and charges share one row, without the charges' breakdown.
	h.waitFor("the margin", h.shows("Margin ≈ ₹1,50,000"))
	if screen := ansi.Strip(h.screen()); !regexp.MustCompile(`Margin ≈ ₹1,50,000\.00 +· +Charges ≈ ₹[\d,.]+`).MatchString(screen) ||
		strings.Contains(screen, "brokerage") {
		t.Errorf("costs row:\n%s", screen)
	}
	if got := asked(); len(got) != 1 || got[0].Tradingsymbol != "NIFTYW125000CE" || got[0].Quantity != 75 {
		t.Errorf("margin asked for %+v", got)
	}

	// Right and Left on the quantity step a lot at a time; the margin is
	// asked for again once, for the order the form rests on.
	h.press("right", "right", "left")
	if got := quantity.value(); got != "150" {
		t.Fatalf("quantity = %s, want 2 lots", got)
	}
	h.waitFor("the 2-lot margin", func() bool { got := asked(); return len(got) == 2 && got[1].Quantity == 150 })
	h.press("left", "left")
	if got := quantity.value(); got != "75" {
		t.Fatalf("quantity = %s, want no less than a lot", got)
	}

	// A limit buy above the last price, 110, fills at once.
	price, trigger := form.form.field(orderFieldPrice), form.form.field(orderFieldTrigger)
	h.press("down", "right", "down", "right")
	if !strings.Contains(h.screen(), hintImmediate) || price.value() != "110.05" {
		t.Fatalf("price %s, no warning:\n%s", price.value(), h.screen())
	}
	h.press("left", "left")
	if strings.Contains(h.screen(), hintImmediate) {
		t.Fatalf("a limit below the LTP warns:\n%s", h.screen())
	}

	// A stop-loss buy triggers above the last price, and its limit is at
	// least the trigger.
	h.press("up", "right", "down", "down")
	h.typeText("100")
	if trigger.value() != "100" || !strings.Contains(h.screen(), hintTriggerAbove) {
		t.Fatalf("trigger %s, no warning:\n%s", trigger.value(), h.screen())
	}
	// Neither can be placed or added to a basket.
	if !strings.Contains(h.screen(), "a stop-loss buy's trigger must be above the LTP, 110.00") {
		t.Fatalf("no reason:\n%s", h.screen())
	}
	for _, button := range []int{0, 1} {
		form.form.focus = len(form.form.fields) + button
		h.press("enter")
		if h.a.overlay != form || len(placer.placed()) != 0 {
			t.Fatalf("button %d left the form for %T, placed %d", button, h.a.overlay, len(placer.placed()))
		}
	}
	form.form.focusField(orderFieldTrigger)
	h.press("backspace", "backspace", "backspace")
	h.typeText("120")
	if screen := h.screen(); strings.Contains(screen, hintTriggerAbove) || !strings.Contains(screen, hintPriceAtLeast) ||
		!strings.Contains(screen, "a stop-loss buy's price must be at least its trigger") {
		t.Fatalf("trigger above the price:\n%s", screen)
	}
	// A valid stop-loss buy can be placed.
	form.form.focusField(orderFieldPrice)
	h.press("backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	h.typeText("125")
	if _, params, err := form.order(); err != nil || params.Price != 125 || params.TriggerPrice != 120 || strings.Contains(h.screen(), "! ") {
		t.Fatalf("a valid stop-loss: %+v, %v:\n%s", params, err, h.screen())
	}
	form.form.focusField(orderFieldTrigger)
	// Market orders have neither.
	h.press("up", "up", "left", "left")
	if screen := h.screen(); strings.Contains(screen, hintPriceAtLeast) || strings.Contains(screen, "!") {
		t.Fatalf("a market order warns:\n%s", screen)
	}
}
