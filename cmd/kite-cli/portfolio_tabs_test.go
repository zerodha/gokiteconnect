package main

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

func TestPortfolioTabsListBasketsAndAlerts(t *testing.T) {
	ctx := context.Background()
	deps, _, baskets := basketDeps(t)
	if _, err := baskets.CreateBasket(ctx, models.Basket{CreatedBy: "AB1234", Name: "Hedge", Items: []models.BasketItem{
		{Exchange: "NFO", TradingSymbol: "NIFTY25OCT25000PE", TransactionType: kite.TransactionTypeBuy, OrderType: kite.OrderTypeMarket, Product: kite.ProductNRML, Quantity: 75},
	}}); err != nil {
		t.Fatal(err)
	}
	triggers := newFakeTriggers()
	triggers.seed(t, "AB1234",
		models.PriceTrigger{InstrumentID: "NSE:RELIANCE", Condition: models.TriggerLTE, Value: 1300, Reference: 1400},
		models.PriceTrigger{InstrumentID: "NSE:NIFTY 50", Condition: models.TriggerGTE, Value: 25100, Reference: 25040, Note: "breakout"},
	)
	if _, err := triggers.EvaluatePriceTriggers(ctx, "AB1234", map[models.InstrumentID]models.Tick{"NSE:RELIANCE": {LastPrice: 1299}}); err != nil {
		t.Fatal(err)
	}
	deps.triggers = triggers
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
		return portfolio{loadedAt: time.Now()}
	}
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("enter")
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	h.waitFor("the orders tab", h.shows("Orders │ Baskets │ Alerts"))

	// ] shows the baskets, focused, each with its orders under it.
	h.press("]")
	p := h.a.portfolio
	if p.tab != tabBaskets || p.focused() != &p.basketGrid {
		t.Fatalf("] gave tab %v", p.tab)
	}
	h.waitFor("the basket", h.shows("▾ Hedge (1)"))
	h.waitFor("its details", h.shows("BUY 75 NIFTY25OCT25000PE"))
	if screen := h.screen(); !strings.Contains(screen, "1 basket · 1 order") || !regexp.MustCompile(`NIFTY25OCT25000PE +NFO +BUY +75 MARKET +NRML +DAY`).MatchString(screen) {
		t.Fatalf("the baskets tab:\n%s", screen)
	}
	// Enter collapses the basket, and again expands it.
	h.press("enter")
	if screen := h.screen(); !strings.Contains(screen, "▸ Hedge (1)") || len(p.basketGrid.rows) != 1 {
		t.Fatalf("Enter did not collapse the basket:\n%s", screen)
	}
	h.press("enter", "down")
	if row, ok := p.focused().selectedRef().(basketOrderRow); !ok || row.basket.Name != "Hedge" || row.item.Quantity != 75 {
		t.Fatalf("selected %+v", p.focused().selectedRef())
	}
	h.waitFor("the order's details", h.shows("in Hedge"))

	// The alerts list active ones first, priced as alerts are checked.
	h.press("]")
	h.waitFor("the alerts", h.shows("1 active · 1 fired"))
	h.dispatch(tickMsg{})
	h.waitFor("the alert's LTP", h.shows("25040.00"))
	screen := h.screen()
	active, fired := strings.Index(screen, "● active"), strings.Index(screen, "RELIANCE")
	if active < 0 || fired < 0 || active > fired || !strings.Contains(screen, "+0.24%") || !strings.Contains(screen, "breakout") {
		t.Fatalf("alerts tab:\n%s", screen)
	}
	if row, ok := p.focused().selectedRef().(alertRow); !ok || row.trigger.ID != 2 || row.trigger.UserID != "AB1234" {
		t.Fatalf("selected %+v", p.focused().selectedRef())
	}

	// Enter edits the alert, its instrument looked up in the catalog: an
	// index, which only baskets and the order form order for.
	h.press("enter")
	h.waitFor("the alert form", func() bool { _, ok := h.a.overlay.(*alertForm); return ok })
	form, ok := h.a.overlay.(*alertForm)
	if !ok || form.editing == nil || form.editing.ID != 2 || len(form.existing) != 0 || form.instrument.Segment != "INDICES" ||
		slices.Contains(form.form.field(alertFieldOrder).options, alertOrderBuy) {
		t.Fatalf("Enter opened %T", h.a.overlay)
	}
	if screen := h.screen(); !strings.Contains(screen, "Edit alert #2") || !strings.Contains(screen, "1 left") || alertField(h, alertFieldNote).value() != "breakout" {
		t.Fatalf("the edit form:\n%s", screen)
	}
	// An empty budget fires on every crossing; the checks start again.
	setAlertField(h, alertFieldBudget, "")
	setAlertField(h, alertFieldEvaluated, "0")
	pressButton(h, form.form, 0)
	h.waitFor("the save", h.shows("Alert saved: NIFTY 50 LTP ≥ ₹25,100.00 (+0.24%) · fires each time it holds"))
	if stored := triggers.all("AB1234")[1]; stored.FiringBudget != nil || stored.TimesEvaluated != 0 || stored.Note != "breakout" || stored.Value != 25100 {
		t.Fatalf("edited %+v", stored)
	}
	h.waitFor("the reload", h.shows("0/∞"))
	h.press("d")
	h.waitFor("the deletion", h.shows("Deleted alert: NIFTY 50 LTP ≥ ₹25,100"))
	h.waitFor("the reload", h.shows("0 active · 1 fired"))
	if left := triggers.all("AB1234"); len(left) != 1 || left[0].ID != 1 {
		t.Fatalf("store keeps %+v", left)
	}
	if len(h.a.alerts.active) != 0 {
		t.Fatalf("still watching %+v", h.a.alerts.active)
	}

	// [ goes back; Enter on an order opens it on Admin's Baskets tab.
	h.press("[")
	h.waitFor("the basket again", h.shows("▾ Hedge (1)"))
	if _, ok := p.focused().selectedRef().(basketOrderRow); !ok {
		t.Fatalf("selected %+v", p.focused().selectedRef())
	}
	h.press("enter")
	h.waitFor("the Baskets tab", func() bool {
		basket, ok := h.a.baskets.selected()
		item, itemOK := h.a.baskets.selectedItem()
		return h.a.onBaskets() && ok && basket.Name == "Hedge" && itemOK && item.Quantity == 75 && h.a.baskets.focus == 1 && h.idle()
	})
	if screen := h.screen(); !strings.Contains(screen, "◫ Baskets") || !strings.Contains(screen, "ADMIN · BASKETS") {
		t.Fatalf("Admin's Baskets tab:\n%s", screen)
	}
	// Esc leaves the orders for the baskets, then Admin.
	h.press("esc")
	if !h.a.onBaskets() || h.a.baskets.focus != 0 {
		t.Fatalf("Esc on the orders left screen %v, focus %d", h.a.screen, h.a.baskets.focus)
	}
	// Orders wraps round to Alerts.
	h.press("esc")
	h.waitFor("the portfolio again", func() bool { return h.a.screen == screenPortfolio })
	h.press("[", "[")
	if p.tab != tabAlerts {
		t.Fatalf("[ from Orders gave tab %v", p.tab)
	}
}

func TestPortfolioHoldingsTabShowsTheFunds(t *testing.T) {
	deps, _, _ := basketDeps(t)
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
		return portfolio{
			holdings: kite.Holdings{{Tradingsymbol: "INFY", Exchange: "NSE", Quantity: 10, AveragePrice: 1400, LastPrice: 1500}},
			funds:    kite.MFHoldings{{Folio: "F123", Fund: "Parag Parikh Flexi Cap", Tradingsymbol: "INF879O01027", Quantity: 100, AveragePrice: 50, LastPrice: 60}},
			loadedAt: time.Now(),
		}
	}
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("enter")
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	p := h.a.portfolio
	if screen := h.screen(); !strings.Contains(screen, "Holdings │ Mutual funds") || !strings.Contains(screen, "INFY") || strings.Contains(screen, "Parag Parikh") {
		t.Fatalf("the holdings panel does not open on the holdings:\n%s", screen)
	}
	// A narrow panel keeps a short summary beside the tabs.
	h.dispatch(tea.WindowSizeMsg{Width: 130, Height: 36})
	if screen := h.screen(); !strings.Contains(screen, "Holdings │ Mutual funds ") || !strings.Contains(screen, "1 · P&L +1,000.00 (+7.14%)") {
		t.Fatalf("the narrow holdings panel lacks its summary:\n%s", screen)
	}
	h.dispatch(tea.WindowSizeMsg{Width: 200, Height: 40})
	if screen := h.screen(); !strings.Contains(screen, "1 · invested 14,000.00 · current 15,000.00 · P&L +1,000.00 (+7.14%)") {
		t.Fatalf("the wide holdings panel lacks its full summary:\n%s", screen)
	}

	// From the positions, ] switches the orders panel, as before.
	h.press("]")
	if p.tab != tabBaskets || p.holdingsTab != tabHoldings {
		t.Fatalf("] on the positions gave tabs %v and %v", p.holdingsTab, p.tab)
	}
	h.press("[")

	// Switching focuses the orders panel; Shift+Tab goes up to the
	// holdings, whose ] shows the funds and leaves the orders tab alone.
	h.dispatch(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	h.press("]")
	if p.holdingsTab != tabFunds || p.focused() != &p.funds || p.tab != tabOrders {
		t.Fatalf("] on the holdings gave tabs %v and %v, focus %d", p.holdingsTab, p.tab, p.focus)
	}
	screen := h.screen()
	if !strings.Contains(screen, "Parag Parikh") || !strings.Contains(screen, "1 · invested 5,000.00 · current 6,000.00") || strings.Contains(screen, "INFY ") {
		t.Fatalf("the funds tab does not show the funds:\n%s", screen)
	}
	if !strings.Contains(screen, "F123") {
		t.Fatalf("the details pane lacks the fund's folio:\n%s", screen)
	}

	// ] wraps back to the holdings, and Down leaves for the orders.
	h.press("]")
	if p.holdingsTab != tabHoldings || p.focused() != &p.holdings {
		t.Fatalf("] did not wrap to the holdings: %v", p.holdingsTab)
	}
	h.press("down", "down")
	if p.focused() != p.tabGrid() {
		t.Fatalf("Down past the holdings reached panel %d", p.focus)
	}
}
