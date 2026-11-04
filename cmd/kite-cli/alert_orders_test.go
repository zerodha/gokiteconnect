package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// alertOrderApp is the explorer with alerts, Telegram and baskets on, in
// mode, and a contract of NIFTY's near expiry: the 25000 call, lot 75,
// at 110.
func alertOrderApp(t *testing.T, mode broker.Mode) (*harness, models.Instrument, *fakeTriggers, *fakeTelegram, *fakePlacer) {
	t.Helper()
	deps, placer, baskets := basketDeps(t)
	triggers, telegram := newFakeTriggers(), &fakeTelegram{}
	deps.triggers, deps.alert, deps.mode = triggers, telegram.send, mode
	if _, err := baskets.CreateBasket(context.Background(), models.Basket{CreatedBy: "AB1234", Name: "Exits", Items: []models.BasketItem{
		{Exchange: "NSE", TradingSymbol: "RELIANCE", TransactionType: kite.TransactionTypeSell, OrderType: kite.OrderTypeMarket, Product: kite.ProductCNC, Quantity: 10},
	}}); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.settle()
	store, _, _ := deps.openCatalog(context.Background())
	contract, err := store.Get(context.Background(), "NFO:NIFTYW125000CE")
	if err != nil {
		t.Fatal(err)
	}
	return h, contract, triggers, telegram, placer
}

// waitForBaskets waits for the alert form to offer the stored basket.
func waitForBaskets(h *harness) {
	h.t.Helper()
	h.waitFor("the baskets", func() bool {
		o, ok := h.a.overlay.(*alertForm)
		return ok && slices.Contains(o.form.field(alertFieldOrder).options, "Exits (1)")
	})
}

func TestAlertPlacesItsOrderWhenItFires(t *testing.T) {
	h, contract, triggers, telegram, placer := alertOrderApp(t, broker.Paper)
	h.run(h.a.openAlertForm(contract, 110))
	waitForBaskets(h)
	o := h.a.overlay.(*alertForm)
	price := alertField(h, alertFieldValue)
	for range len(price.value()) {
		h.press("backspace")
	}
	h.typeText("120")

	// Sending only a message, the order's fields stay hidden.
	if !alertField(h, alertFieldOrder).hidden || !alertField(h, alertFieldQty).hidden || strings.Contains(h.screen(), "Account") {
		t.Fatalf("order fields show with no order:\n%s", h.screen())
	}
	// Both shows them, a buy first: the quantity in lots of 75, and the
	// paper account.
	h.press("down", "down", "right", "right")
	if then := alertField(h, alertFieldThen).value(); then != "Both" || alertField(h, alertFieldOrder).value() != alertOrderBuy {
		t.Fatalf("then %q:\n%s", then, h.screen())
	}
	if screen := h.screen(); !strings.Contains(screen, "1 lot") || !strings.Contains(screen, "PAPER  places them when it fires") {
		t.Fatalf("a buy's fields:\n%s", screen)
	}
	// Right steps a lot; a limit starts at the alert's price.
	h.press("down", "down", "right", "down", "right")
	if qty, limit := alertField(h, alertFieldQty), alertField(h, alertFieldLimit); qty.value() != "150" || limit.hidden || limit.value() != "120.00" ||
		!strings.Contains(h.screen(), "2 lots") {
		t.Fatalf("qty %q, limit %q:\n%s", qty.value(), limit.value(), h.screen())
	}
	// A quantity off the lot is refused.
	h.press("up")
	retype(h, "100")
	pressButton(h, o.form, 0)
	if !strings.Contains(h.screen(), "the quantity must be a multiple of 75") || h.idle() {
		t.Fatalf("an odd quantity:\n%s", h.screen())
	}
	setAlertField(h, alertFieldQty, "150")
	setAlertField(h, alertFieldLimit, "121")
	pressButton(h, o.form, 0)
	h.waitFor("the alert saved", h.idle)
	h.waitFor("the confirmation", h.shows("places 1 order in paper"))
	saved := triggers.all("AB1234")
	want := models.BasketItem{Exchange: "NFO", TradingSymbol: "NIFTYW125000CE", TransactionType: kite.TransactionTypeBuy, OrderType: kite.OrderTypeLimit,
		Product: kite.ProductNRML, Quantity: 150, Price: 121}
	if len(saved) != 1 || len(saved[0].Orders) != 1 || saved[0].Orders[0] != want || saved[0].OrderMode != "paper" {
		t.Fatalf("stored %+v", saved)
	}
	if len(placer.placed()) != 0 {
		t.Fatal("an order went before the alert fired")
	}

	// Crossing places it once into the paper account, and says so.
	h.settle()
	quotes := h.a.deps.loadQuotes
	source, _, _ := quotes(context.Background())
	source.(*fakeQuotes).set(string(contract.ID), 122)
	h.dispatch(tickMsg{})
	h.waitFor("the toast", h.shows("1 order placed"))
	placed := placer.placed()
	if len(placed) != 1 || placed[0].mode != broker.Paper || placed[0].userID != "AB1234" || placed[0].params.Quantity != 150 ||
		placed[0].params.Price != 121 || placed[0].params.OrderType != kite.OrderTypeLimit || placed[0].params.Tag != orderTag {
		t.Fatalf("placed %+v", placed)
	}
	sent := telegram.messages()
	if len(sent) != 1 || !strings.Contains(sent[0], "Orders (paper):\n✔ BUY 150 NIFTYW125000CE · 2510120001") {
		t.Fatalf("sent %q", sent)
	}
	if stored := triggers.all("AB1234"); len(stored[0].OrderResults) != 1 || stored[0].OrderResults[0].OrderID != "2510120001" {
		t.Fatalf("results %+v", stored[0].OrderResults)
	}
	h.dispatch(tickMsg{})
	h.settle()
	if len(placer.placed()) != 1 {
		t.Fatal("the order went twice")
	}
}

func TestAlertPlacesABasketLiveAfterAsking(t *testing.T) {
	h, _, triggers, telegram, placer := alertOrderApp(t, broker.Live)
	// A hedge's sell and buy, in a draft: the buy is listed and goes first.
	h.a.baskets.newDraft("AB1234", "Hedge",
		models.BasketItem{ID: "x", Exchange: "NFO", TradingSymbol: "NIFTYW125000CE", TransactionType: kite.TransactionTypeSell, OrderType: kite.OrderTypeMarket, Product: kite.ProductNRML, Quantity: 75},
		models.BasketItem{ID: "y", Exchange: "NFO", TradingSymbol: "NIFTYW125100CE", TransactionType: kite.TransactionTypeBuy, OrderType: kite.OrderTypeMarket, Product: kite.ProductNRML, Quantity: 75})
	store, _, _ := h.a.deps.openCatalog(context.Background())
	nifty, _ := store.Get(context.Background(), "NSE:NIFTY 50")
	h.run(h.a.openAlertForm(nifty, 25040))
	waitForBaskets(h)
	order := alertField(h, alertFieldOrder)
	// An index cannot be bought or sold, only trigger a basket.
	if slices.Contains(order.options, alertOrderBuy) || !slices.Contains(order.options, "Hedge * (2)") {
		t.Fatalf("order options %q", order.options)
	}
	h.press("down", "down", "right", "down")
	for order.value() != "Hedge * (2)" {
		h.press("right")
	}
	if screen, buy := h.screen(), strings.Index(h.screen(), "BUY 75 NIFTYW125100CE MARKET"); buy < 0 || buy > strings.Index(screen, "SELL 75 NIFTYW125000CE MARKET") ||
		!strings.Contains(screen, "LIVE  real orders, placed when it fires") {
		t.Fatalf("the basket's orders:\n%s", screen)
	}

	// Set asks first, and Cancel keeps nothing.
	pressButton(h, h.a.overlay.(*alertForm).form, 0)
	if _, ok := h.a.overlay.(*dialog); !ok || !strings.Contains(h.screen(), "LIVE Kite account") || !strings.Contains(h.screen(), "BUY 75 NIFTYW125100CE MARKET") {
		t.Fatalf("no confirmation (%T):\n%s", h.a.overlay, h.screen())
	}
	h.press("enter")
	if _, ok := h.a.overlay.(*alertForm); ok || len(triggers.all("AB1234")) != 0 {
		t.Fatalf("Cancel kept %+v, overlay %T", triggers.all("AB1234"), h.a.overlay)
	}
	h.run(h.a.openAlertForm(nifty, 25040))
	waitForBaskets(h)
	order = alertField(h, alertFieldOrder)
	retype(h, "25100")
	h.press("down", "down", "right", "right", "down")
	for order.value() != "Hedge * (2)" {
		h.press("right")
	}
	pressButton(h, h.a.overlay.(*alertForm).form, 0)
	h.press("left", "enter")
	h.waitFor("the alert saved", h.idle)
	h.waitFor("the confirmation", h.shows("places 2 orders in LIVE"))
	saved := triggers.all("AB1234")
	if len(saved) != 1 || saved[0].OrderMode != "live" || len(saved[0].Orders) != 2 ||
		saved[0].Orders[0].TransactionType != kite.TransactionTypeBuy || saved[0].Orders[0].ID != "" {
		t.Fatalf("stored %+v", saved)
	}

	// The account refuses both: the toast and Telegram say why.
	h.settle()
	placer.fail(errors.New("insufficient margin"))
	source, _, _ := h.a.deps.loadQuotes(context.Background())
	source.(*fakeQuotes).set("NSE:NIFTY 50", 25200)
	h.dispatch(tickMsg{})
	h.waitFor("the toast", h.shows("2 of 2 orders failed"))
	if sent := telegram.messages(); len(sent) != 1 || !strings.Contains(sent[0], "Orders (LIVE):\n✖ BUY 75 NIFTYW125100CE: insufficient margin\n✖ SELL 75 NIFTYW125000CE: insufficient margin") {
		t.Fatalf("sent %q", sent)
	}
	if cell := alertOrdersCell(triggers.all("AB1234")[0]); cell.text != "✖ 2/2" {
		t.Fatalf("the Alerts tab shows %q", cell.text)
	}
}

// pressButton moves the focus onto the form's button and presses it.
func pressButton(h *harness, f *form, button int) {
	h.t.Helper()
	for range len(f.fields) + len(f.buttons) {
		if f.button() == button {
			break
		}
		h.press("down")
	}
	h.press("enter")
}

func TestAlertOnAnIndexOrdersItsOptionsFromTheOrderForm(t *testing.T) {
	h, _, triggers, _, placer := alertOrderApp(t, broker.Paper)
	store, _, _ := h.a.deps.openCatalog(context.Background())
	nifty, _ := store.Get(context.Background(), "NSE:NIFTY 50")
	h.run(h.a.openAlertForm(nifty, 25040))
	waitForBaskets(h)
	alert := h.a.overlay.(*alertForm)
	retype(h, "25100")
	order := alertField(h, alertFieldOrder)
	h.press("down", "down", "right", "right", "down")
	for order.value() != alertOrderForm {
		h.press("right")
	}
	if !strings.Contains(h.screen(), "none yet · Enter on Order adds one") {
		t.Fatalf("no orders yet:\n%s", h.screen())
	}

	// Enter opens the order form on the nearest call, adding to the alert.
	h.press("enter")
	form, ok := h.a.overlay.(*orderForm)
	if !ok || form.ticket.alert != alert {
		t.Fatalf("Enter opened %T", h.a.overlay)
	}
	h.waitFor("the ATM call", func() bool { return form.ticket.instrument.TradingSymbol == "NIFTYW125000CE" && !form.legs.loading })
	if screen := h.screen(); !strings.Contains(screen, "Alert order") || !strings.Contains(screen, "Add to alert") || strings.Contains(screen, "Place BUY") {
		t.Fatalf("the alert's order form:\n%s", screen)
	}
	// Shift+→ shows the put; Add puts it on the alert, back on its form.
	h.dispatch(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	if form.ticket.instrument.TradingSymbol != "NIFTYW125000PE" {
		t.Fatalf("shift+right showed %s", form.ticket.instrument.TradingSymbol)
	}
	pressButton(h, form.form, 0)
	if h.a.overlay != alert || len(alert.added) != 1 || !strings.Contains(h.screen(), "BUY 75 NIFTYW125000PE MARKET") {
		t.Fatalf("after adding (%T) %+v:\n%s", h.a.overlay, alert.added, h.screen())
	}

	// A second order, the future; Escape from a third adds nothing, and
	// Backspace takes the last off.
	h.press("enter")
	form = h.a.overlay.(*orderForm)
	h.waitFor("the contracts", func() bool { return !form.legs.loading })
	h.press("f")
	pressButton(h, form.form, 0)
	h.press("enter", "esc")
	if h.a.overlay != alert || len(alert.added) != 2 || alert.added[1].TradingSymbol != "NFO:NIFTYW1FUT" {
		t.Fatalf("after the future (%T) %+v", h.a.overlay, alert.added)
	}
	h.press("backspace")
	if len(alert.added) != 1 || strings.Contains(alertField(h, alertFieldOrders).note, "NIFTYW1FUT") || !strings.Contains(h.screen(), "Removed BUY 75") {
		t.Fatalf("Backspace left %+v", alert.added)
	}

	// Set stores the put; crossing places it.
	pressButton(h, alert.form, 0)
	h.waitFor("the alert saved", h.idle)
	saved := triggers.all("AB1234")
	want := models.BasketItem{Exchange: "NFO", TradingSymbol: "NIFTYW125000PE", TransactionType: kite.TransactionTypeBuy, OrderType: kite.OrderTypeMarket,
		Product: kite.ProductNRML, Quantity: 75}
	if len(saved) != 1 || len(saved[0].Orders) != 1 || saved[0].Orders[0] != want || saved[0].OrderMode != "paper" {
		t.Fatalf("stored %+v", saved)
	}
	h.settle()
	source, _, _ := h.a.deps.loadQuotes(context.Background())
	source.(*fakeQuotes).set("NSE:NIFTY 50", 25200)
	h.dispatch(tickMsg{})
	h.waitFor("the order", func() bool { return len(placer.placed()) == 1 })
	if placed := placer.placed()[0]; placed.params.Tradingsymbol != "NIFTYW125000PE" || placed.mode != broker.Paper || placed.params.Validity != kite.ValidityDay {
		t.Fatalf("placed %+v", placed)
	}
}

func TestEditingASpentAlertKeepsItsOrdersAndRaisesItsBudget(t *testing.T) {
	h, contract, triggers, _, _ := alertOrderApp(t, broker.Live)
	sell := models.BasketItem{Exchange: "NFO", TradingSymbol: contract.TradingSymbol, TransactionType: kite.TransactionTypeSell,
		OrderType: kite.OrderTypeMarket, Product: kite.ProductNRML, Quantity: 75}
	triggers.seed(t, "AB1234", models.PriceTrigger{InstrumentID: contract.ID, Condition: models.TriggerGTE, Value: 120, Reference: 110,
		Orders: []models.BasketItem{sell}, OrderMode: "paper"})
	if _, err := triggers.EvaluatePriceTriggers(context.Background(), "AB1234", map[models.InstrumentID]models.Tick{contract.ID: {LastPrice: 121}}); err != nil {
		t.Fatal(err)
	}
	spent := triggers.all("AB1234")[0]
	if spent.Status != models.TriggerFired {
		t.Fatalf("seeded %+v", spent)
	}

	// The edit form shows its orders as the order form's, in the account
	// they were set in, and its counts.
	h.run(h.a.editAlert(alertRow{trigger: spent, tick: models.Tick{LastPrice: 110}}))
	h.waitFor("the alert form", func() bool { _, ok := h.a.overlay.(*alertForm); return ok })
	o := h.a.overlay.(*alertForm)
	if !o.own() || len(o.added) != 1 || o.mode != broker.Paper || alertField(h, alertFieldFired).value() != "1" || alertField(h, alertFieldEvaluated).value() != "1" {
		t.Fatalf("order %q, added %v, mode %s:\n%s", alertField(h, alertFieldOrder).value(), o.added, o.mode, h.screen())
	}
	if screen := h.screen(); !strings.Contains(screen, "SELL 75 "+contract.TradingSymbol) || !strings.Contains(screen, "PAPER") || !strings.Contains(screen, "0 left") {
		t.Fatalf("the edit form:\n%s", screen)
	}
	// A budget of three leaves two; a bad time is refused.
	setAlertField(h, alertFieldBudget, "3")
	setAlertField(h, alertFieldLastFired, "yesterday")
	pressButton(h, o.form, 0)
	if !strings.Contains(h.screen(), "Last fired: write it as YYYY-MM-DD hh:mm:ss") {
		t.Fatalf("a bad time saved:\n%s", h.screen())
	}
	setAlertField(h, alertFieldLastFired, "2026-11-03 10:00:00")
	pressButton(h, o.form, 0)
	h.waitFor("the save", h.shows("Alert saved: NIFTYW125000CE LTP ≥ ₹120.00"))
	stored := triggers.all("AB1234")[0]
	want := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	if stored.Status != models.TriggerActive || *stored.FiringBudget != 3 || stored.TimesFired != 1 || !stored.LastFired.Equal(want) ||
		len(stored.Orders) != 1 || stored.Orders[0] != sell || stored.OrderMode != "paper" || !stored.Tripped {
		t.Fatalf("stored %+v", stored)
	}
	if active := h.a.alerts.active; len(active) != 1 || active[0].trigger.ID != stored.ID {
		t.Fatalf("watching %+v", active)
	}
}
