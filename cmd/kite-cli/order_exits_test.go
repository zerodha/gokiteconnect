package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// fakeOrderStatus answers order lookups with the order as set.
type fakeOrderStatus struct {
	mu      sync.Mutex
	order   kite.Order
	lookups int
}

func (f *fakeOrderStatus) lookup(_ context.Context, _ string, _ broker.Mode, orderID string) (kite.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	order := f.order
	order.OrderID = orderID
	return order, nil
}

func (f *fakeOrderStatus) set(status string, filled float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order.Status, f.order.FilledQuantity = status, filled
}

// ctrl presses Ctrl and key.
func ctrl(h *harness, key rune) {
	h.dispatch(tea.KeyPressMsg{Code: key, Mod: tea.ModCtrl})
}

// setOrderField types text into the order form's field label.
func setOrderField(h *harness, label, text string) {
	h.t.Helper()
	o := h.a.overlay.(*orderForm)
	o.form.focusField(label)
	for range len(o.form.field(label).value()) {
		h.press("backspace")
	}
	h.typeText(text)
}

// openContractOrder opens the order form on contract, priced.
func openContractOrder(h *harness, contract models.Instrument) *orderForm {
	h.t.Helper()
	h.run(h.a.openTradeForm(tradeTarget{instrument: contract, ltp: 110}))
	h.waitFor("the priced form", func() bool {
		o, ok := h.a.overlay.(*orderForm)
		return ok && o.ticket.instrument.ID == contract.ID && o.ticket.ltp > 0
	})
	return h.a.overlay.(*orderForm)
}

func TestOrderStoplossAndTargetArmOnFillAndCancelEachOther(t *testing.T) {
	h, contract, triggers, telegram, placer := alertOrderApp(t, broker.Paper)
	status := &fakeOrderStatus{order: kite.Order{Status: "OPEN"}}
	h.a.deps.orderStatus = status.lookup
	o := openContractOrder(h, contract)
	entry := o.ticket.ltp

	// A buy's stoploss above its entry is refused, and Ctrl+O places
	// nothing.
	setOrderField(h, orderFieldStoploss, "125")
	if screen := h.screen(); !strings.Contains(screen, "a buy's stoploss must be below its entry") {
		t.Fatalf("a stoploss above the entry:\n%s", screen)
	}
	ctrl(h, 'o')
	h.settle()
	if len(placer.placed()) != 0 || h.idle() {
		t.Fatal("placed with the stoploss on the wrong side")
	}
	// Below it, the hints show how far each is.
	setOrderField(h, orderFieldStoploss, "100")
	setOrderField(h, orderFieldTarget, "130")
	if screen := h.screen(); !strings.Contains(screen, formatSigned(100-entry)) || !strings.Contains(screen, "Exits at market: stoploss 100.00, target 130.00") {
		t.Fatalf("the exits:\n%s", screen)
	}
	// They aren't kept in a basket.
	ctrl(h, 'b')
	if screen := h.screen(); !strings.Contains(screen, "Exits are set on placing: clear them to add to a basket") {
		t.Fatalf("Ctrl+B with exits:\n%s", screen)
	}

	// Ctrl+O places it from any field, and sets both, waiting for its fill.
	ctrl(h, 'o')
	h.waitFor("the order", h.idle)
	h.waitFor("the toast", h.shows("then SL ₹100.00, target ₹130.00"))
	if placed := placer.placed(); len(placed) != 1 || placed[0].params.TransactionType != kite.TransactionTypeBuy || placed[0].params.Quantity != 75 {
		t.Fatalf("placed %+v", placed)
	}
	exits := triggers.all("AB1234")
	if len(exits) != 2 {
		t.Fatalf("exits %+v", exits)
	}
	stop, target := exits[0], exits[1]
	sell := models.BasketItem{Exchange: "NFO", TradingSymbol: "NIFTYW125000CE", TransactionType: kite.TransactionTypeSell,
		OrderType: kite.OrderTypeMarket, Product: kite.ProductNRML, Quantity: 75}
	if stop.Exit != models.TriggerStoploss || stop.Condition != models.TriggerLTE || stop.Value != 100 || target.Exit != models.TriggerTarget ||
		target.Condition != models.TriggerGTE || target.Value != 130 || stop.Entry != "2510120001" || stop.OCO == "" || stop.OCO != target.OCO ||
		len(stop.Orders) != 1 || stop.Orders[0] != sell || stop.OrderMode != "paper" || stop.Action != models.TriggerActionBoth {
		t.Fatalf("stop %+v\ntarget %+v", stop, target)
	}

	// While the order is open, a price past the stop doesn't fire it.
	h.settle()
	source, _, _ := h.a.deps.loadQuotes(context.Background())
	source.(*fakeQuotes).set(string(contract.ID), 95)
	h.dispatch(tickMsg{})
	h.waitFor("the lookup", func() bool { status.mu.Lock(); defer status.mu.Unlock(); return status.lookups > 0 })
	h.settle()
	if len(placer.placed()) != 1 || triggers.all("AB1234")[0].TimesEvaluated != 0 {
		t.Fatalf("an exit waiting for its fill was checked: %+v", triggers.all("AB1234"))
	}

	// A part fill arms both for what filled.
	source.(*fakeQuotes).set(string(contract.ID), 105)
	status.set(kite.OrderStatusCancelled, 50)
	h.dispatch(tickMsg{})
	h.waitFor("the arming", h.shows("Order 2510120001 filled · armed SL ₹100.00, target ₹130.00"))
	h.settle()
	for _, exit := range triggers.all("AB1234") {
		if exit.Entry != "" || exit.Orders[0].Quantity != 50 || exit.Status != models.TriggerActive {
			t.Fatalf("armed %+v", exit)
		}
	}

	// The stop fires, sells what filled, and deletes the target.
	source.(*fakeQuotes).set(string(contract.ID), 99)
	h.dispatch(tickMsg{})
	h.waitFor("the stop", h.shows("1 order placed"))
	placed := placer.placed()
	if len(placed) != 2 || placed[1].params.TransactionType != kite.TransactionTypeSell || placed[1].params.Quantity != 50 ||
		placed[1].params.OrderType != kite.OrderTypeMarket {
		t.Fatalf("placed %+v", placed)
	}
	if left := triggers.all("AB1234"); len(left) != 1 || left[0].ID != stop.ID || left[0].Status != models.TriggerFired {
		t.Fatalf("left %+v", left)
	}
	if sent := telegram.messages(); len(sent) != 1 || !strings.Contains(sent[0], "stoploss of order 2510120001") {
		t.Fatalf("sent %q", sent)
	}
	// The target is gone from the watch, so a rally places nothing more.
	source.(*fakeQuotes).set(string(contract.ID), 140)
	h.dispatch(tickMsg{})
	h.settle()
	if len(placer.placed()) != 2 || len(h.a.alerts.active) != 0 {
		t.Fatalf("after the stop: placed %d, watching %+v", len(placer.placed()), h.a.alerts.active)
	}
}

func TestOrderExitsDropWithAnUnfilledEntry(t *testing.T) {
	h, contract, triggers, _, placer := alertOrderApp(t, broker.Paper)
	status := &fakeOrderStatus{}
	h.a.deps.orderStatus = status.lookup
	openContractOrder(h, contract)
	// A sell's stoploss goes above its entry.
	h.press("s")
	setOrderField(h, orderFieldStoploss, "120")
	ctrl(h, 'o')
	h.waitFor("the order", h.idle)
	if exits := triggers.all("AB1234"); len(exits) != 1 || exits[0].Condition != models.TriggerGTE || exits[0].Orders[0].TransactionType != kite.TransactionTypeBuy {
		t.Fatalf("exits %+v", exits)
	}
	status.set(kite.OrderStatusRejected, 0)
	h.settle()
	h.dispatch(tickMsg{})
	h.waitFor("the drop", h.shows("Order 2510120001 rejected unfilled · dropped SL ₹120.00"))
	if left := triggers.all("AB1234"); len(left) != 0 || len(h.a.alerts.active) != 0 || len(placer.placed()) != 1 {
		t.Fatalf("left %+v", left)
	}
}

func TestOrderFormShortcutsPlaceAndAddToABasket(t *testing.T) {
	h, contract, _, _, placer := alertOrderApp(t, broker.Paper)
	o := openContractOrder(h, contract)
	if screen := h.screen(); !strings.Contains(screen, "^O place · ^B add to basket") {
		t.Fatalf("the form's keys:\n%s", screen)
	}
	// Ctrl+B adds to a basket from the quantity field.
	o.form.focusField(orderFieldQuantity)
	ctrl(h, 'b')
	h.waitFor("the basket picker", h.shows("Add BUY 75 NIFTYW125000CE to"))
	h.press("esc")
	if h.a.overlay != o {
		t.Fatalf("Esc left %T", h.a.overlay)
	}
	// Ctrl+O places from the price field.
	o.form.focusField(orderFieldPrice)
	ctrl(h, 'o')
	h.waitFor("the order", h.idle)
	if placed := placer.placed(); len(placed) != 1 || placed[0].params.Quantity != 75 {
		t.Fatalf("placed %+v", placed)
	}
}
