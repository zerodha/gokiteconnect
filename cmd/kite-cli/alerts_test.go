package main

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// fakeTriggers keeps alerts in memory, as the portfolio databases would.
type fakeTriggers struct {
	mu       sync.Mutex
	triggers map[string][]models.PriceTrigger
	next     int64
	// firedElsewhere are alerts another kite-cli fires first.
	firedElsewhere []int64
}

func newFakeTriggers() *fakeTriggers {
	return &fakeTriggers{triggers: map[string][]models.PriceTrigger{}}
}

func (f *fakeTriggers) Users() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var users []string
	for user := range f.triggers {
		users = append(users, user)
	}
	slices.Sort(users)
	return users, nil
}

func (f *fakeTriggers) AddPriceTrigger(_ context.Context, user string, trigger models.PriceTrigger) (models.PriceTrigger, error) {
	if err := trigger.Validate(); err != nil {
		return models.PriceTrigger{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	trigger.ID, trigger.UserID, trigger.CreatedAt = f.next, user, time.Now()
	f.triggers[user] = append(f.triggers[user], trigger)
	return trigger, nil
}

func (f *fakeTriggers) PriceTriggers(_ context.Context, user string, activeOnly bool) ([]models.PriceTrigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var triggers []models.PriceTrigger
	for _, trigger := range f.triggers[user] {
		if !activeOnly || trigger.Status == models.TriggerActive {
			triggers = append(triggers, trigger)
		}
	}
	return triggers, nil
}

func (f *fakeTriggers) UpdatePriceTrigger(_ context.Context, user string, id int64, edit func(*models.PriceTrigger) error) (models.PriceTrigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, stored := range f.triggers[user] {
		if stored.ID != id {
			continue
		}
		trigger := stored
		if err := edit(&trigger); err != nil {
			return models.PriceTrigger{}, err
		}
		trigger.ID, trigger.UserID, trigger.CreatedAt = stored.ID, stored.UserID, stored.CreatedAt
		if err := trigger.Validate(); err != nil {
			return models.PriceTrigger{}, err
		}
		f.triggers[user][index] = trigger
		return trigger, nil
	}
	return models.PriceTrigger{}, models.ErrNotFound
}

func (f *fakeTriggers) EvaluatePriceTriggers(_ context.Context, user string, ticks map[models.InstrumentID]models.Tick) ([]models.TriggerEvaluation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var checks []models.TriggerEvaluation
	cancelled := map[int64]bool{}
	for index := range f.triggers[user] {
		trigger := &f.triggers[user][index]
		tick := ticks[trigger.InstrumentID]
		if _, ok := tick.Value(trigger.Field); trigger.Status != models.TriggerActive || trigger.Entry != "" || cancelled[trigger.ID] || !ok {
			continue
		}
		fired := trigger.Evaluate(tick, time.Now())
		check := models.TriggerEvaluation{Trigger: *trigger, Fired: fired && !slices.Contains(f.firedElsewhere, trigger.ID)}
		if check.Fired && trigger.OCO != "" {
			for _, other := range f.triggers[user] {
				if other.ID != trigger.ID && other.OCO == trigger.OCO && !cancelled[other.ID] {
					cancelled[other.ID] = true
					check.Cancelled = append(check.Cancelled, other.ID)
				}
			}
		}
		checks = append(checks, check)
	}
	f.triggers[user] = slices.DeleteFunc(f.triggers[user], func(trigger models.PriceTrigger) bool { return cancelled[trigger.ID] })
	return checks, nil
}

func (f *fakeTriggers) RecordTriggerOrders(_ context.Context, user string, id int64, results []models.TriggerOrderResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, trigger := range f.triggers[user] {
		if trigger.ID == id {
			f.triggers[user][index].OrderResults = slices.Clone(results)
		}
	}
	return nil
}

func (f *fakeTriggers) DeletePriceTrigger(_ context.Context, user string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers[user] = slices.DeleteFunc(f.triggers[user], func(trigger models.PriceTrigger) bool { return trigger.ID == id })
	return nil
}

func (f *fakeTriggers) all(user string) []models.PriceTrigger {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.triggers[user])
}

// seed stores triggers as the alert form sets them: firing once, unless
// given a budget.
func (f *fakeTriggers) seed(t *testing.T, user string, triggers ...models.PriceTrigger) {
	t.Helper()
	for _, trigger := range triggers {
		if trigger.FiringBudget == nil {
			trigger.FiringBudget = new(1)
		}
		if _, err := f.AddPriceTrigger(context.Background(), user, trigger); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeTelegram records what alerts send, failing while err is set.
type fakeTelegram struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (f *fakeTelegram) send(_ context.Context, user, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, user+": "+text)
	return nil
}

func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func (f *fakeQuotes) set(id string, price float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prices[id] = price
}

func alertField(h *harness, label string) *field {
	h.t.Helper()
	form, ok := h.a.overlay.(*alertForm)
	if !ok {
		h.t.Fatalf("the alert form is not open: %T", h.a.overlay)
	}
	return form.form.field(label)
}

// setAlertField types text over the alert form's field.
func setAlertField(h *harness, label, text string) {
	h.t.Helper()
	field := alertField(h, label)
	h.a.overlay.(*alertForm).form.focusField(label)
	for range len(field.value()) {
		h.press("backspace")
	}
	h.typeText(text)
}

func TestAlertWithoutALimitFiresOnEachCrossing(t *testing.T) {
	store, quotes := niftyMarket()
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	triggers, telegram := newFakeTriggers(), &fakeTelegram{}
	if _, err := triggers.AddPriceTrigger(context.Background(), "AB1234",
		models.PriceTrigger{InstrumentID: "NSE:NIFTY 50", Condition: models.TriggerGTE, Value: 25100, Reference: 25040}); err != nil {
		t.Fatal(err)
	}
	deps.triggers, deps.alert = triggers, telegram.send
	h := newHarness(t, deps)
	h.waitFor("the alert", func() bool { return len(h.a.alerts.active) == 1 })
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.settle()

	// Up through it, staying up, back down, up again: it fires twice.
	for _, ltp := range []float64{25110, 25120, 25090, 25105} {
		quotes.set("NSE:NIFTY 50", ltp)
		h.dispatch(tickMsg{})
		h.settle()
	}
	sent := telegram.messages()
	if len(sent) != 2 || !strings.Contains(sent[0], "Fired 1/∞") || !strings.Contains(sent[1], "LTP ₹25,105.00") || !strings.Contains(sent[1], "Fired 2/∞") {
		t.Fatalf("sent %q", sent)
	}
	stored := triggers.all("AB1234")[0]
	if stored.Status != models.TriggerActive || stored.TimesFired != 2 || stored.TimesEvaluated < 5 || !stored.Tripped || stored.LastFiredValue != 25105 {
		t.Fatalf("stored %+v", stored)
	}
	if active := h.a.alerts.active; len(active) != 1 || active[0].trigger.TimesFired != 2 {
		t.Fatalf("watching %+v", active)
	}
}

func TestAlertSetFromTheExplorerFiresOnce(t *testing.T) {
	store, quotes := niftyMarket()
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	triggers, telegram := newFakeTriggers(), &fakeTelegram{}
	deps.triggers, deps.alert = triggers, telegram.send
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.settle()

	// T opens the form on NIFTY at its LTP, which would fire at once.
	h.press("t")
	form, ok := h.a.overlay.(*alertForm)
	if !ok {
		t.Fatalf("T opened %T", h.a.overlay)
	}
	if form.instrument.ID != "NSE:NIFTY 50" || form.ltp != 25040 || form.user != "AB1234" {
		t.Fatalf("form on %s at %v for %s", form.instrument.ID, form.ltp, form.user)
	}
	price, is := alertField(h, alertFieldValue), alertField(h, alertFieldIs)
	if price.value() != "25040.00" || is.value() != "≥ at or above" || alertField(h, alertFieldWhen).value() != "Last price" ||
		!strings.Contains(h.screen(), "fires at once") {
		t.Fatalf("price %q is %q:\n%s", price.value(), is.value(), h.screen())
	}

	// The slider moves the price from the LTP: Shift by a percent.
	h.press("down")
	h.dispatch(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	h.dispatch(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	h.press("right")
	if price.value() != "25603.40" || is.value() != "≥ at or above" || !strings.Contains(h.screen(), "+2.25%") {
		t.Fatalf("after sliding price %q is %q:\n%s", price.value(), is.value(), h.screen())
	}
	if strings.Contains(h.screen(), "fires at once") {
		t.Fatalf("a price above the LTP still warns:\n%s", h.screen())
	}
	// Turning Is to face down mirrors the price below the LTP; > still
	// faces up.
	h.press("up", "up", "right")
	if price.value() != "25603.40" || is.value() != "> above" {
		t.Fatalf("after > price %q is %q:\n%s", price.value(), is.value(), h.screen())
	}
	h.press("right")
	if price.value() != "24476.60" || is.value() != "< below" || !strings.Contains(h.screen(), "-2.25%") {
		t.Fatalf("after flipping price %q is %q:\n%s", price.value(), is.value(), h.screen())
	}
	h.press("right")
	if price.value() != "24476.60" || is.value() != "≤ at or below" {
		t.Fatalf("after ≤ price %q is %q:\n%s", price.value(), is.value(), h.screen())
	}
	// A typed price moves the slider, and Is follows its side.
	h.press("down")
	for range len(price.value()) {
		h.press("backspace")
	}
	h.typeText("25100")
	if is.value() != "≥ at or above" || !strings.Contains(h.screen(), "+0.24%") {
		t.Fatalf("after typing is %q:\n%s", is.value(), h.screen())
	}
	// Sending a message, Then needs no orders.
	if alertField(h, alertFieldThen).value() != "Send message" || !alertField(h, alertFieldOrder).hidden {
		t.Fatalf("then %q:\n%s", alertField(h, alertFieldThen).value(), h.screen())
	}
	h.press("down", "down", "down")
	h.typeText("breakout")
	pressButton(h, form.form, 0)
	h.waitFor("the alert saved", h.idle)
	h.waitFor("the confirmation", h.shows("Alert set: NIFTY 50 LTP ≥ ₹25,100.00 (+0.24%)"))
	saved := triggers.all("AB1234")
	if len(saved) != 1 || saved[0].Field != models.FieldLastPrice || saved[0].Condition != models.TriggerGTE || saved[0].Value != 25100 ||
		saved[0].Reference != 25040 || saved[0].Note != "breakout" || saved[0].Action != models.TriggerActionMessage {
		t.Fatalf("stored %+v", saved)
	}

	// A refresh below the price fires nothing.
	h.dispatch(tickMsg{})
	h.settle()
	if len(telegram.messages()) != 0 || h.a.alerts.checking {
		t.Fatalf("fired early: %v", telegram.messages())
	}

	// Crossing it fires the alert once: a toast and a Telegram message.
	quotes.set("NSE:NIFTY 50", 25110)
	h.dispatch(tickMsg{})
	h.waitFor("the toast", h.shows("Alert: NIFTY 50 LTP ≥ ₹25,100.00 · LTP ₹25,110.00 · breakout · sent to Telegram"))
	sent := telegram.messages()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "AB1234: 🔔 NIFTY 50 LTP ≥ ₹25,100.00\nLTP ₹25,110.00, +0.28% since set at ₹25,040.00\nNote: breakout") {
		t.Fatalf("sent %q", sent)
	}
	if stored := triggers.all("AB1234"); stored[0].Status != models.TriggerFired || stored[0].LastFiredValue != 25110 {
		t.Fatalf("after firing stored %+v", stored)
	}
	h.dispatch(tickMsg{})
	h.settle()
	if len(telegram.messages()) != 1 || len(h.a.alerts.active) != 0 {
		t.Fatalf("fired again: %v, active %v", telegram.messages(), h.a.alerts.active)
	}
}

func TestStoredAlertsLoadFireAndClear(t *testing.T) {
	store, quotes := niftyMarket()
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	triggers, telegram := newFakeTriggers(), &fakeTelegram{err: errors.New("chat not found")}
	triggers.seed(t, "AB1234",
		models.PriceTrigger{InstrumentID: "NSE:NIFTY 50", Condition: models.TriggerGTE, Value: 30000, Reference: 25000},
		models.PriceTrigger{InstrumentID: "NSE:NIFTY 50", Condition: models.TriggerLTE, Value: 20000, Reference: 25000},
	)
	// Another user's alert is checked too, but one fired elsewhere is not
	// shown here.
	triggers.seed(t, "CD5678", models.PriceTrigger{InstrumentID: "NSE:RELIANCE", Condition: models.TriggerLTE, Value: 1450, Reference: 1500})
	triggers.seed(t, "EF9012", models.PriceTrigger{InstrumentID: "NSE:RELIANCE", Condition: models.TriggerLTE, Value: 1450})
	triggers.firedElsewhere = []int64{4}
	deps.triggers, deps.alert = triggers, telegram.send
	quotes.set("NSE:RELIANCE", 1500)
	h := newHarness(t, deps)
	h.waitFor("the alerts", func() bool { return len(h.a.alerts.active) == 4 })
	openExplorer(h)
	// Once the first check is done and the watchlists, whose load closes
	// dialogs, are in, RELIANCE falls through its alerts.
	h.waitFor("the first check", func() bool { return !h.a.alerts.checking })
	h.settle()
	quotes.set("NSE:RELIANCE", 1400)
	h.dispatch(tickMsg{})
	h.waitFor("the toast", h.shows("Alert: RELIANCE LTP ≤ ₹1,450.00 · LTP ₹1,400.00 · CD5678 · Telegram failed: chat not found"))
	if strings.Contains(h.screen(), "and 1 more") || len(h.a.alerts.active) != 2 {
		t.Fatalf("active %v:\n%s", h.a.alerts.active, h.screen())
	}

	// T on NIFTY lists its alerts and clears them.
	h.press("t")
	if screen := h.screen(); !strings.Contains(screen, "LTP ≥ ₹30,000.00 · LTP ≤ ₹20,000.00") || !strings.Contains(screen, "Clear 2") {
		t.Fatalf("the form does not list the alerts:\n%s", screen)
	}
	h.press("down", "down", "down", "down", "down", "right", "enter")
	h.waitFor("the alerts cleared", h.idle)
	h.waitFor("the confirmation", h.shows("Cleared 2 alerts on NIFTY 50"))
	if left := triggers.all("AB1234"); len(left) != 0 || len(h.a.alerts.active) != 0 {
		t.Fatalf("left %+v, watching %v", left, h.a.alerts.active)
	}
}

func TestPortfolioAlertFetchesTheLTP(t *testing.T) {
	store, quotes := niftyMarket()
	quotes.set("NSE:INFY", 1500)
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	triggers := newFakeTriggers()
	deps.triggers = triggers
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
		return portfolio{
			positions: []kite.Position{{Tradingsymbol: "INFY", Exchange: "NSE", Product: "CNC", Quantity: 10}},
			loadedAt:  time.Now(),
			loggedIn:  true,
		}
	}
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("enter")
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	h.waitFor("the position", h.shows("INFY"))

	h.press("T")
	h.waitFor("the alert form", func() bool { _, ok := h.a.overlay.(*alertForm); return ok })
	form, ok := h.a.overlay.(*alertForm)
	if !ok || form.instrument.ID != "NSE:INFY" {
		t.Fatalf("T opened %T", h.a.overlay)
	}
	h.waitFor("the LTP", func() bool { return form.ltp == 1500 })
	if price := alertField(h, alertFieldValue).value(); price != "1500.00" || alertField(h, alertFieldMove).disabled {
		t.Fatalf("price %q:\n%s", price, h.screen())
	}
	// Without Telegram the alert stays in kite-cli.
	h.press("up", "right", "right", "right", "down", "down", "left")
	pressButton(h, form.form, 0)
	h.waitFor("the alert saved", h.idle)
	h.waitFor("the confirmation", h.shows("Alert set: INFY LTP ≤ ₹1,496.25 (-0.25%) · fires once · shows here; Telegram is off"))
}

func TestAlertsWatchOtherFieldsAndActOnTheirAction(t *testing.T) {
	store, quotes := niftyMarket()
	placer := &fakePlacer{}
	deps := explorerDeps(t, store, quotes, nil, placer)
	triggers, telegram := newFakeTriggers(), &fakeTelegram{}
	buy := models.BasketItem{Exchange: "NSE", TradingSymbol: "RELIANCE", TransactionType: kite.TransactionTypeBuy, OrderType: kite.OrderTypeMarket,
		Product: kite.ProductCNC, Quantity: 10}
	if _, err := triggers.AddPriceTrigger(context.Background(), "AB1234", models.PriceTrigger{InstrumentID: "NSE:NIFTY 50",
		Field: models.FieldDayChange, Condition: models.TriggerCrossAbove, Value: 495}); err != nil {
		t.Fatal(err)
	}
	// Placing a basket sends no message.
	triggers.seed(t, "AB1234", models.PriceTrigger{InstrumentID: "NSE:RELIANCE", Condition: models.TriggerLT, Value: 1350,
		Action: models.TriggerActionBasket, Orders: []models.BasketItem{buy}, OrderMode: "paper"})
	deps.triggers, deps.alert = triggers, telegram.send
	h := newHarness(t, deps)
	h.waitFor("the alerts", func() bool { return len(h.a.alerts.active) == 2 })
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.settle()

	// The quotes close 2% below the LTP: NIFTY's day change is ₹490.98
	// at 25040, ₹496.08 at 25300. Crossing fires it; staying above does
	// not, nor does a first check already above.
	for _, ltp := range []float64{25300, 25400, 25040, 25300} {
		quotes.set("NSE:NIFTY 50", ltp)
		h.dispatch(tickMsg{})
		h.settle()
	}
	sent := telegram.messages()
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "AB1234: 🔔 NIFTY 50 Day change crosses above +₹495.00\nDay change +₹496.08 · LTP ₹25,300.00") {
		t.Fatalf("sent %q", sent)
	}
	stored := triggers.all("AB1234")[0]
	if stored.TimesFired != 2 || stored.LastFiredValue < 496 || stored.LastValue == nil || *stored.LastValue != stored.LastFiredValue {
		t.Fatalf("stored %+v", stored)
	}

	quotes.set("NSE:RELIANCE", 1340)
	h.dispatch(tickMsg{})
	h.waitFor("the toast", func() bool {
		return h.shows("Alert: RELIANCE LTP < ₹1,350.00 · LTP ₹1,340.00")() && h.shows("1 order placed")()
	})
	if placed := placer.placed(); len(placed) != 1 || placed[0].params.Tradingsymbol != "RELIANCE" || placed[0].mode != broker.Paper {
		t.Fatalf("placed %+v", placed)
	}
	if len(telegram.messages()) != 2 || strings.Contains(h.screen(), "sent to Telegram") {
		t.Fatalf("a basket alert sent %q", telegram.messages())
	}
}

func TestAlertFormSetsAnAlertOnAChangeField(t *testing.T) {
	store, quotes := niftyMarket()
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	triggers := newFakeTriggers()
	deps.triggers = triggers
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.settle()
	h.press("t")
	form := h.a.overlay.(*alertForm)
	h.waitFor("the quote", func() bool { return form.quote.OHLC.Close > 0 })

	// When Day change % starts Value at it now, with no slider.
	h.press("up", "up")
	for alertField(h, alertFieldWhen).value() != "Day change %" {
		h.press("right")
	}
	value := alertField(h, alertFieldValue)
	if value.value() != "2.00" || !alertField(h, alertFieldMove).hidden || !strings.Contains(h.screen(), "fires at once") {
		t.Fatalf("value %q:\n%s", value.value(), h.screen())
	}
	// It takes a minus first only; below now, Is turns to face down.
	setAlertField(h, alertFieldValue, "1-.5")
	if value.value() != "1.5" {
		t.Fatalf("typed %q", value.value())
	}
	setAlertField(h, alertFieldValue, "-1.5")
	if value.value() != "-1.5" || alertField(h, alertFieldIs).value() != "≤ at or below" || !strings.Contains(h.screen(), "now +2.00%") {
		t.Fatalf("value %q is %q:\n%s", value.value(), alertField(h, alertFieldIs).value(), h.screen())
	}
	pressButton(h, form.form, 0)
	h.waitFor("the alert saved", h.idle)
	h.waitFor("the confirmation", h.shows("Alert set: NIFTY 50 Day change % ≤ -1.50% (-3.50 pts) · fires once"))
	saved := triggers.all("AB1234")
	if len(saved) != 1 || saved[0].Field != models.FieldDayChangePercent || saved[0].Condition != models.TriggerLTE || saved[0].Value != -1.5 ||
		math.Abs(saved[0].Reference-2) > 1e-9 {
		t.Fatalf("stored %+v", saved)
	}
}

func TestSignedDecimalsTakeAMinusFirst(t *testing.T) {
	for _, c := range []struct {
		kind        numeric
		value, text string
		want        bool
	}{
		{signedDecimal, "", "-", true},
		{signedDecimal, "", "-1.5", true},
		{signedDecimal, "1", "-", false},
		{signedDecimal, "-1", ".", true},
		{signedDecimal, "-1.5", ".", false},
		{decimalNumber, "", "-", false},
		{wholeNumber, "1", ".", false},
	} {
		if got := accepts(c.kind, c.value, c.text); got != c.want {
			t.Errorf("accepts(%d, %q, %q) = %v", c.kind, c.value, c.text, got)
		}
	}
}
