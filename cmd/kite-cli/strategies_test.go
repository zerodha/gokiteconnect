package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

// strategyChain opens NIFTY's chain, its near expiry listing strikes 24500
// to 25600 with calls priced 300 - (strike-24500)/5 and puts 100 +
// (strike-24500)/5, spot at 25,040.
func strategyChain(t *testing.T) (*harness, *repository.BasketsSQLiteRepository) {
	t.Helper()
	store, quotes := niftyMarket()
	near := *store.options[0].Expiry
	nifty := models.InstrumentID("NSE:NIFTY 50")
	listed := map[models.InstrumentID]bool{}
	for _, contract := range store.options {
		listed[contract.ID] = true
	}
	for strike := 24500.0; strike <= 25600; strike += 100 {
		for _, optionType := range []models.OptionType{models.OptionTypeCall, models.OptionTypePut} {
			id := models.InstrumentID(fmt.Sprintf("NFO:NIFTYW1%.0f%s", strike, optionType))
			price := 300 - (strike-24500)/5
			if optionType == models.OptionTypePut {
				price = 100 + (strike-24500)/5
			}
			quotes.prices[string(id)] = price
			if listed[id] {
				continue
			}
			contract := option(id, optionType, strike, 1, near)
			contract.Exchange, contract.LotSize, contract.UnderlyingID = "NFO", 75, &nifty
			contract.TradingSymbol = strings.TrimPrefix(string(id), "NFO:")
			store.options = append(store.options, contract)
		}
	}
	deps, _, baskets := basketMarketDeps(t, store, quotes)
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.press("o")
	h.waitFor("the chain", func() bool { return h.a.explorer.mode == viewChain && h.idle() && h.a.explorer.spot() > 0 })
	return h, baskets
}

// openStrategyForm picks the menu's numbered strategy and waits for its
// legs' prices.
func openStrategyForm(h *harness, key string) *strategyForm {
	h.t.Helper()
	h.press("b")
	if _, ok := h.a.overlay.(*menu); !ok || !strings.Contains(h.screen(), "Basket from the chain") {
		h.t.Fatalf("B on the chain opened %T:\n%s", h.a.overlay, h.screen())
	}
	h.press(key)
	o, ok := h.a.overlay.(*strategyForm)
	if !ok {
		h.t.Fatalf("%s opened %T", key, h.a.overlay)
	}
	h.waitFor("the legs' prices", func() bool { return !strings.Contains(h.screen(), "Pricing the legs") })
	return o
}

// addToDraft presses Add to basket, which closes the form on a new draft
// named for the strategy.
func addToDraft(h *harness, o *strategyForm) models.Basket {
	h.t.Helper()
	for o.form.button() != 0 {
		h.press("down")
	}
	before := len(h.a.baskets.drafts)
	h.press("enter")
	h.waitFor("the draft", func() bool { return h.idle() && len(h.a.baskets.drafts) == before+1 })
	draft := h.a.baskets.drafts[before]
	if !strings.Contains(h.screen(), "Added "+draft.Name+" to Baskets") {
		h.t.Fatalf("no note of the draft:\n%s", h.screen())
	}
	return draft
}

// retype replaces the focused input's text.
func retype(h *harness, text string) {
	h.t.Helper()
	h.press("backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	h.typeText(text)
}

func itemsText(items []models.BasketItem) string {
	var parts []string
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s %d %s %s %s", item.TransactionType, item.Quantity, item.TradingSymbol, item.OrderType, item.Product))
	}
	return strings.Join(parts, ", ")
}

func TestStrategyBasketStraddle(t *testing.T) {
	h, baskets := strategyChain(t)
	o := openStrategyForm(h, "1")
	if strike := o.form.field(strategyFieldStrike).value(); strike != formatStrike(25000) {
		t.Fatalf("the straddle opened at %s, want the ATM 25000", strike)
	}
	// The call and put at 25000 are 200 each: a 400 credit, 30,000 a lot,
	// breaking even 400 either side.
	screen := h.screen()
	for _, want := range []string{"75 × NIFTYW125000CE  200.00", "75 × NIFTYW125000PE  200.00", "Credit      400.00 · ₹30,000.00",
		"Max loss    unlimited", "Breakevens  " + formatPrice(24600) + " – " + formatPrice(25400)} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the straddle lacks %q:\n%s", want, screen)
		}
	}

	// Buying it at the next strike, two lots, pays 180 + 220.
	h.press("right", "down", "right", "down")
	// Left and Right step the lots, from 1 up; - and + too, and typing.
	lots := o.form.field(strategyFieldLots)
	h.press("left")
	if lots.value() != "1" {
		t.Fatalf("Left took the lots to %s", lots.value())
	}
	h.press("right", "right", "+", "-")
	if lots.value() != "3" {
		t.Fatalf("Right twice, + and - made %s lots, want 3", lots.value())
	}
	h.press("-")
	retype(h, "")
	h.typeText("2")
	h.waitFor("the new legs' prices", h.shows("150 × NIFTYW125100PE  220.00"))
	if screen := h.screen(); !strings.Contains(screen, "Debit       400.00 · ₹60,000.00") || !strings.Contains(screen, "Max profit  unlimited") {
		t.Fatalf("the bought straddle:\n%s", screen)
	}
	basket := addToDraft(h, o)
	near := o.expiry.date.Format("02 Jan")
	if basket.Name != "NIFTY 50 "+near+" 25100 straddle" || basket.CreatedBy != "AB1234" ||
		itemsText(basket.Items) != "BUY 150 NIFTYW125100CE MARKET NRML, BUY 150 NIFTYW125100PE MARKET NRML" {
		t.Fatalf("basket %q by %s: %s", basket.Name, basket.CreatedBy, itemsText(basket.Items))
	}

	// The same straddle again is a second draft, numbered.
	o = openStrategyForm(h, "1")
	h.press("right", "down", "right")
	if again := addToDraft(h, o); again.Name != basket.Name+" 2" {
		t.Fatalf("the second straddle is %q", again.Name)
	}

	// S on Baskets asks for the name, then stores the draft under it.
	h.press("b", "b")
	h.waitFor("the baskets", func() bool { return h.a.onBaskets() && h.idle() })
	h.press("s")
	input, ok := h.a.overlay.(*inputOverlay)
	if !ok || !strings.Contains(h.screen(), "Save basket") || input.form.field("Name").value() != basket.Name {
		t.Fatalf("S opened %T:\n%s", h.a.overlay, h.screen())
	}
	for range len(basket.Name) {
		h.press("backspace")
	}
	h.typeText("Weekly straddle")
	h.press("enter")
	h.waitFor("the saved basket", func() bool { return len(h.a.baskets.drafts) == 1 && h.idle() })
	if saved := stored(t, baskets)[0]; saved.Name != "Weekly straddle" || len(saved.Items) != 2 || len(h.a.baskets.drafts) != 1 {
		t.Fatalf("saved %q with %d orders, %d drafts left", saved.Name, len(saved.Items), len(h.a.baskets.drafts))
	}
}

func TestStrategyBasketStrangle(t *testing.T) {
	h, _ := strategyChain(t)
	o := openStrategyForm(h, "2")
	strikes := func() string {
		return o.form.field(strategyFieldPut).value() + "/" + o.form.field(strategyFieldCall).value()
	}
	// 2% from 25,040 is 24,539 and 25,541: the strikes at or beyond them.
	if strikes() != "24500/25600" || !strings.Contains(h.screen(), "2% from spot") {
		t.Fatalf("the strangle opened at %s:\n%s", strikes(), h.screen())
	}
	// Left on Width slides both in to 1.5%: 24,664 and 25,416.
	h.press("down", "left")
	if strikes() != "24600/25500" || !strings.Contains(h.screen(), "1.5% from spot") {
		t.Fatalf("1.5%% gave %s", strikes())
	}
	h.waitFor("the new legs' prices", h.shows("75 × NIFTYW125500CE  100.00"))
	if screen := h.screen(); !strings.Contains(screen, "75 × NIFTYW124600PE  120.00") || !strings.Contains(screen, "Credit      220.00 · ₹16,500.00") {
		t.Fatalf("the 1.5%% strangle:\n%s", screen)
	}

	// A typed strike stands until Width moves again; one not listed says so.
	h.press("down")
	retype(h, "25350")
	if screen := h.screen(); !strings.Contains(screen, "not listed") || !strings.Contains(screen, "no call listed at "+formatStrike(25350)) {
		t.Fatalf("an unlisted call strike:\n%s", screen)
	}
	retype(h, "25300")
	h.waitFor("the typed leg's price", h.shows("75 × NIFTYW125300CE  140.00"))
	if strikes() != "24600/25300" || strings.Contains(h.screen(), "not listed") {
		t.Fatalf("typing gave %s:\n%s", strikes(), h.screen())
	}
	h.press("up", "right")
	if strikes() != "24500/25600" {
		t.Fatalf("2%% again gave %s", strikes())
	}
	// Past the listed strikes, each leg keeps the farthest.
	for range 4 {
		h.dispatch(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	}
	if strikes() != "24500/25600" || !strings.Contains(h.screen(), "10% from spot") {
		t.Fatalf("10%% gave %s", strikes())
	}
	for range 4 {
		h.dispatch(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	}
	h.press("down")
	retype(h, "24500")
	if !strings.Contains(h.screen(), "the put strike must be below the call's") {
		t.Fatalf("a call below the put:\n%s", h.screen())
	}
	retype(h, "25500")

	basket := addToDraft(h, o)
	if basket.Name != "NIFTY 50 "+o.expiry.date.Format("02 Jan")+" 24500/25500 strangle" ||
		itemsText(basket.Items) != "SELL 75 NIFTYW125500CE MARKET NRML, SELL 75 NIFTYW124500PE MARKET NRML" {
		t.Fatalf("basket %q: %s", basket.Name, itemsText(basket.Items))
	}
}

func TestStrategyBasketCallSpread(t *testing.T) {
	h, _ := strategyChain(t)
	o := openStrategyForm(h, "3")
	// A bull spread buys the ATM 25000 call at 200 and sells the 25100 at
	// 180: a 20 debit, making up to 80 above 25,100.
	screen := h.screen()
	for _, want := range []string{"75 × NIFTYW125000CE  200.00", "75 × NIFTYW125100CE  180.00", "Debit       20.00 · ₹1,500.00",
		"Max profit  80.00 · ₹6,000.00", "Max loss    20.00 · ₹1,500.00", "Breakeven   " + formatPrice(25020)} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the call spread lacks %q:\n%s", want, screen)
		}
	}
	// The upper strike must be above the lower.
	h.press("down", "down", "left", "left")
	if !strings.Contains(h.screen(), "the upper strike must be above the lower") {
		t.Fatalf("a sell strike below the buy:\n%s", h.screen())
	}
	h.press("right", "right", "right")

	// A bear spread sells the lower call, buying the higher first, for MIS.
	h.press("up", "up", "right", "down", "down", "down", "down", "right")
	h.waitFor("the new legs' prices", h.shows("75 × NIFTYW125200CE  160.00"))
	if screen := h.screen(); !strings.Contains(screen, "Credit      40.00 · ₹3,000.00") || !strings.Contains(screen, "Max loss    160.00 · ₹12,000.00") {
		t.Fatalf("the bear spread:\n%s", screen)
	}
	basket := addToDraft(h, o)
	if basket.Name != "NIFTY 50 "+o.expiry.date.Format("02 Jan")+" 25000/25200 call spread" ||
		itemsText(basket.Items) != "BUY 75 NIFTYW125200CE MARKET MIS, SELL 75 NIFTYW125000CE MARKET MIS" {
		t.Fatalf("basket %q: %s", basket.Name, itemsText(basket.Items))
	}
}

func TestStrategyMenuStillOpensBaskets(t *testing.T) {
	h, _ := strategyChain(t)
	h.press("b", "B")
	if !h.a.onBaskets() {
		t.Fatalf("B in the menu showed screen %v", h.a.screen)
	}
}
