package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// Alerts: T on an instrument opens alertForm, which sets an alert in the
// active user's portfolio database; Enter or T on the portfolio's Alerts
// tab edits one. An alert watches one of the instrument's tick fields, the
// last price, a day's change, volume or open interest among them: WHEN
// the field is at, past or crosses a value, THEN it sends a message,
// places a basket of orders, or both. kite-cli checks every user's active
// alerts against quotes each refresh, counting each check, and each time
// one fires it shows a toast in the status bar and, when its action sends
// one and a Telegram bot is set, a message to the user's chat. An alert
// fires as many times as its budget allows, once by default, or without
// limit. Its orders are a buy or sell of its instrument, a basket's
// orders, copied in when it is set, or orders made in the order form.

const (
	alertFieldWhen   = "When"
	alertFieldIs     = "Is"
	alertFieldValue  = "Value"
	alertFieldMove   = "Move"
	alertFieldThen   = "Then"
	alertFieldNote   = "Note"
	alertFieldActive = "Active"

	alertFieldBudget    = "Budget"
	alertFieldFired     = "Fired"
	alertFieldEvaluated = "Evaluated"
	alertFieldLastFired = "Last fired"

	alertFieldOrder   = "Order"
	alertFieldQty     = "Qty"
	alertFieldType    = "Type"
	alertFieldLimit   = "Limit"
	alertFieldProduct = "Product"
	alertFieldOrders  = "Orders"
	alertFieldAccount = "Account"

	alertButtonSet    = "Set alert"
	alertButtonSave   = "Save"
	alertButtonCancel = "Cancel"
)

// lastFiredLayout is how the alert form shows and reads Last fired, in
// local time.
const lastFiredLayout = "2006-01-02 15:04:05"

// alertScale is how far, in percent, the Move slider reaches either side
// of a price, and its step: options, which move more, slide further.
func alertScale(instrument models.Instrument) (reach, stride float64) {
	if isOption(instrument) {
		return 50, 1
	}
	return 10, 0.25
}

// conditionLabels are how the form offers models.TriggerConditions, in
// their order.
var conditionLabels = []string{"≥ at or above", "> above", "< below", "≤ at or below", "= equals", "↗ crosses above", "↘ crosses below"}

// actionLabels are how the form offers models.TriggerActions, in their
// order.
var actionLabels = []string{"Send message", "Place basket", "Both"}

// The Order field's first options; the user's baskets follow. Order form
// builds the alert's own orders in the order form, any of the
// instrument's options and futures among them.
const (
	alertOrderBuy  = "Buy"
	alertOrderSell = "Sell"
	alertOrderForm = "Order form"
)

// triggerStore keeps each user's alerts in their portfolio database.
type triggerStore interface {
	// Users are the users who have a portfolio database.
	Users() ([]string, error)
	AddPriceTrigger(ctx context.Context, userID string, trigger models.PriceTrigger) (models.PriceTrigger, error)
	// PriceTriggers are the user's alerts, only the active ones when
	// activeOnly is set.
	PriceTriggers(ctx context.Context, userID string, activeOnly bool) ([]models.PriceTrigger, error)
	// UpdatePriceTrigger applies edit to an alert as it is stored then.
	UpdatePriceTrigger(ctx context.Context, userID string, id int64, edit func(*models.PriceTrigger) error) (models.PriceTrigger, error)
	// EvaluatePriceTriggers checks the user's active alerts against
	// ticks, counting each check and firing those whose condition came to
	// hold. Another kite-cli checking at once never fires the same one.
	EvaluatePriceTriggers(ctx context.Context, userID string, ticks map[models.InstrumentID]models.Tick) ([]models.TriggerEvaluation, error)
	// RecordTriggerOrders keeps how placing a fired alert's orders went.
	RecordTriggerOrders(ctx context.Context, userID string, id int64, results []models.TriggerOrderResult) error
	DeletePriceTrigger(ctx context.Context, userID string, id int64) error
}

// alertSender sends a fired alert's text to a user.
type alertSender func(ctx context.Context, userID, text string) error

// userTrigger is an active alert and the user who set it.
type userTrigger struct {
	user    string
	trigger models.PriceTrigger
}

func (t userTrigger) same(other userTrigger) bool {
	return t.user == other.user && t.trigger.ID == other.trigger.ID
}

// alertWatch is the active alerts kite-cli checks, with the quote source
// it checks them on.
type alertWatch struct {
	active   []userTrigger
	source   quoteSource
	checking bool
	// tickerDown marks source as the quotes standing in for a ticker that
	// was down, kept until retryAt, when the ticker is tried again.
	tickerDown bool
	retryAt    time.Time
	// failure is the last check's error, shown once until a check works;
	// evalFailure is the last evaluation's, and entryFailure the last
	// entry lookup's, likewise.
	failure, evalFailure, entryFailure string
	// ticks are the last quotes checked, by instrument.
	ticks map[models.InstrumentID]models.Tick
}

// firedAlert is an alert a check fired, as it was after firing: on value
// of its field, with the instrument at ltp.
type firedAlert struct {
	userTrigger
	value, ltp float64
	// results are how placing its orders went; recordErr is why storing
	// them failed.
	results   []models.TriggerOrderResult
	recordErr error
	// sendErr is why sending it failed; sent is whether it went.
	sent    bool
	sendErr error
	// cancelled are the alerts of its OCO group its firing deleted.
	cancelled []int64
}

type (
	alertsLoadedMsg struct {
		active []userTrigger
		err    error
	}
	alertQuotesMsg struct {
		source quoteSource
		// fresh is whether source was connected for this check, and down
		// whether it stands in for a ticker that is down.
		fresh, down bool
		quotes      kite.Quote
		err         error
		// entries are the entry orders that were settled, or failed to
		// be looked up.
		entries []alertEntry
	}
	// alertsEvaluatedMsg is the alerts a check counted, as they now are,
	// and those it fired.
	alertsEvaluatedMsg struct {
		checked []userTrigger
		fired   []firedAlert
		err     error
	}
	alertSavedMsg struct {
		form  *alertForm
		saved userTrigger
		// edited is whether it was an alert edited, rather than set.
		edited bool
		err    error
	}
	alertsClearedMsg struct {
		form    *alertForm
		cleared []userTrigger
		err     error
	}
	// alertQuoteMsg is the quote fetched for the alert form's instrument.
	alertQuoteMsg struct {
		form   *alertForm
		source quoteSource
		down   bool
		quote  models.Tick
		err    error
	}
	// alertInstrumentMsg is the catalog's instrument for an alert opened
	// on a bare ID.
	alertInstrumentMsg struct {
		instrument models.Instrument
		ltp        float64
		editing    *models.PriceTrigger
	}
)

// loadAlerts reads every user's active alerts.
func (a *app) loadAlerts() tea.Cmd {
	store := a.deps.triggers
	if store == nil {
		return nil
	}
	return a.task(func(ctx context.Context) tea.Msg {
		users, err := store.Users()
		if err != nil {
			return alertsLoadedMsg{err: err}
		}
		var msg alertsLoadedMsg
		for _, user := range users {
			triggers, err := store.PriceTriggers(ctx, user, true)
			if err != nil {
				msg.err = errors.Join(msg.err, err)
				continue
			}
			for _, trigger := range triggers {
				msg.active = append(msg.active, userTrigger{user: user, trigger: trigger})
			}
		}
		return msg
	})
}

// alertTickerRetry is how long alerts check quotes after finding the
// ticker down before they try it again.
const alertTickerRetry = 30 * time.Second

// alertQuotes is where alerts read prices: the watch's source, else the
// explorer's while it reads the ticker, else the data user's once
// connected. down reports a source standing in for a ticker that is down,
// which is given up after alertTickerRetry to try the ticker again.
func (a *app) alertQuotes() (source quoteSource, down bool, load quoteLoader) {
	w, load := &a.alerts, a.deps.loadQuotes
	switch {
	case w.source != nil && (!w.tickerDown || time.Now().Before(w.retryAt)):
		return w.source, w.tickerDown, load
	case a.explorer.quoteSource != nil && !a.explorer.tickerDown:
		return a.explorer.quoteSource, false, load
	}
	return nil, false, load
}

// quoteOn fetches ids' quotes on source, connecting with load first when
// source is nil, and returns the source it used and whether the ticker was
// down when connecting: it is then load's quotes.
func quoteOn(ctx context.Context, source quoteSource, load quoteLoader, ids ...string) (quoteSource, kite.Quote, bool, error) {
	down := false
	if source == nil {
		if load == nil {
			return nil, nil, false, errors.New("prices are off")
		}
		var err error
		// A ticker that is down still returns the REST source, with why.
		if source, _, err = load(ctx); source == nil {
			return nil, nil, false, err
		}
		var unavailable tickerUnavailableError
		down = errors.As(err, &unavailable)
	}
	quotes, err := source.GetQuote(ids...)
	return source, quotes, down, err
}

// checkAlerts quotes the instruments with active alerts. With entries set
// it also looks up the entry orders alerts wait on, arming the alerts
// whose entry filled.
func (a *app) checkAlerts(entries bool) tea.Cmd {
	w := &a.alerts
	if a.deps.triggers == nil || w.checking || len(w.active) == 0 {
		return nil
	}
	source, down, load := a.alertQuotes()
	waiting := map[alertEntryKey][]userTrigger{}
	var ids []string
	for _, active := range w.active {
		ids = append(ids, string(active.trigger.InstrumentID))
		if trigger := active.trigger; entries && trigger.Entry != "" && a.deps.orderStatus != nil {
			key := alertEntryKey{user: active.user, mode: broker.Mode(trigger.OrderMode), orderID: trigger.Entry}
			waiting[key] = append(waiting[key], active)
		}
	}
	if source == nil && load == nil && len(waiting) == 0 {
		return nil
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	store, lookup := a.deps.triggers, a.deps.orderStatus
	w.checking = true
	return a.task(func(ctx context.Context) tea.Msg {
		msg := alertQuotesMsg{entries: settleEntries(ctx, store, lookup, waiting)}
		if source == nil && load == nil {
			return msg
		}
		msg.fresh = source == nil
		msg.source, msg.quotes, msg.down, msg.err = quoteOn(ctx, source, load, ids...)
		if !msg.fresh {
			msg.down = down
		}
		return msg
	})
}

// checkAlertsOnTicks checks alerts as ticks arrive, when they read the
// ticker, rather than at the next refresh.
func (a *app) checkAlertsOnTicks() tea.Cmd {
	if _, ticker := a.alerts.source.(*tickerQuotes); !ticker {
		return nil
	}
	return a.checkAlerts(false)
}

// alertEntryKey is an entry order alerts wait on: a stoploss and target
// arm once the order they guard fills.
type alertEntryKey struct {
	user, orderID string
	mode          broker.Mode
}

// alertEntry is what became of an entry order: the order as it was looked
// up, the alerts its fill armed, or those dropped when it went unfilled.
type alertEntry struct {
	alertEntryKey
	order          kite.Order
	armed, dropped []userTrigger
	err            error
}

// settleEntries looks up the entry orders alerts wait on. A filled entry
// arms its alerts, their orders for the quantity that filled; one rejected
// or cancelled unfilled drops them; one still open leaves them waiting.
// It returns the entries settled or that failed to be looked up.
func settleEntries(ctx context.Context, store triggerStore, lookup orderLookup, waiting map[alertEntryKey][]userTrigger) []alertEntry {
	var settled []alertEntry
	for key, alerts := range waiting {
		entry := alertEntry{alertEntryKey: key}
		entry.order, entry.err = lookup(ctx, key.user, key.mode, key.orderID)
		filled := int(entry.order.FilledQuantity)
		ended := entry.order.Status == kite.OrderStatusRejected || entry.order.Status == kite.OrderStatusCancelled
		switch {
		case entry.err != nil:
		case entry.order.Status == kite.OrderStatusComplete || ended && filled > 0:
			for _, alert := range alerts {
				armed, err := store.UpdatePriceTrigger(ctx, key.user, alert.trigger.ID, func(trigger *models.PriceTrigger) error {
					trigger.Entry = ""
					for index := range trigger.Orders {
						if filled > 0 {
							trigger.Orders[index].Quantity = filled
						}
					}
					return nil
				})
				if err != nil {
					entry.err = errors.Join(entry.err, err)
					continue
				}
				entry.armed = append(entry.armed, userTrigger{user: key.user, trigger: armed})
			}
		case ended:
			for _, alert := range alerts {
				if err := store.DeletePriceTrigger(ctx, key.user, alert.trigger.ID); err != nil {
					entry.err = errors.Join(entry.err, err)
					continue
				}
				entry.dropped = append(entry.dropped, alert)
			}
		default:
			continue
		}
		settled = append(settled, entry)
	}
	slices.SortFunc(settled, func(x, y alertEntry) int { return strings.Compare(x.orderID, y.orderID) })
	return settled
}

// settled takes in what became of entry orders: armed alerts are checked
// from now, dropped ones forgotten, and each is told in the status bar.
func (a *app) settled(entries []alertEntry) tea.Cmd {
	w := &a.alerts
	var reloads []tea.Cmd
	failure := ""
	for _, entry := range entries {
		for _, armed := range entry.armed {
			w.keep(armed)
		}
		w.active = slices.DeleteFunc(w.active, func(active userTrigger) bool {
			return slices.ContainsFunc(entry.dropped, active.same)
		})
		switch {
		case len(entry.armed) > 0:
			a.notify(statusOK(fmt.Sprintf("Order %s filled", entry.orderID)) + statusInfo(" · armed "+exitsText(entry.armed)))
		case len(entry.dropped) > 0:
			a.notify(statusWarn(fmt.Sprintf("Order %s %s unfilled", entry.orderID, strings.ToLower(entry.order.Status))) + statusInfo(" · dropped "+exitsText(entry.dropped)))
		}
		if entry.err != nil && failure == "" {
			failure = fmt.Sprintf("order %s: %s", entry.orderID, quoteErrorText(entry.err))
		}
		if len(entry.armed)+len(entry.dropped) > 0 {
			reloads = append(reloads, a.alertsChanged(entry.user))
		}
	}
	if failure != "" && failure != w.entryFailure {
		a.notify(statusWarn("Alerts cannot look up their entry: " + failure))
	}
	w.entryFailure = failure
	return tea.Batch(reloads...)
}

// exitsText names the alerts guarding an entry, short for the status bar:
// "SL ₹95.00, target ₹110.00".
func exitsText(alerts []userTrigger) string {
	var parts []string
	for _, alert := range alerts {
		trigger := alert.trigger
		name := cmp.Or(trigger.Exit, "alert")
		if name == models.TriggerStoploss {
			name = "SL"
		}
		parts = append(parts, name+" "+fieldValue(trigger.Field, trigger.Value))
	}
	return strings.Join(parts, ", ")
}

// evaluateAlerts has users' stores check their active alerts against
// ticks, then does what those that fired do: places their orders, sends
// their message, or both.
func (a *app) evaluateAlerts(users []string, ticks map[models.InstrumentID]models.Tick) tea.Cmd {
	store, send, place := a.deps.triggers, a.deps.alert, a.deps.place
	a.alerts.checking = true
	return a.task(func(ctx context.Context) tea.Msg {
		var msg alertsEvaluatedMsg
		for _, user := range users {
			checks, err := store.EvaluatePriceTriggers(ctx, user, ticks)
			if err != nil {
				msg.err = errors.Join(msg.err, err)
				continue
			}
			for _, check := range checks {
				checked := userTrigger{user: user, trigger: check.Trigger}
				msg.checked = append(msg.checked, checked)
				if !check.Fired {
					continue
				}
				fired := firedAlert{userTrigger: checked, value: check.Trigger.LastFiredValue, ltp: ticks[check.Trigger.InstrumentID].LastPrice, cancelled: check.Cancelled}
				if orders := fired.trigger.Orders; fired.trigger.PlacesOrders() && len(orders) > 0 {
					fired.results = placeTriggerOrders(ctx, place, user, broker.Mode(fired.trigger.OrderMode), orders)
					fired.recordErr = store.RecordTriggerOrders(ctx, user, fired.trigger.ID, fired.results)
				}
				if send != nil && fired.trigger.SendsMessage() {
					fired.sendErr = send(ctx, user, alertMessage(fired))
					fired.sent = fired.sendErr == nil
				}
				msg.fired = append(msg.fired, fired)
			}
		}
		return msg
	})
}

// placeTriggerOrders places a fired alert's orders, in the order they were
// set (buys first), into user's account in mode.
func placeTriggerOrders(ctx context.Context, place orderPlacer, user string, mode broker.Mode, orders []models.BasketItem) []models.TriggerOrderResult {
	results := make([]models.TriggerOrderResult, len(orders))
	for index, order := range orders {
		if place == nil {
			results[index].Error = "ordering is unavailable"
			continue
		}
		orderID, err := place(ctx, user, mode, itemParams(order))
		if err != nil {
			results[index].Error = quoteErrorText(err)
			continue
		}
		results[index].OrderID = orderID
	}
	return results
}

// placedText sums up how placing an alert's orders went: how many were
// placed, and the first failure.
func placedText(results []models.TriggerOrderResult) (string, bool) {
	placed, failure := 0, ""
	for _, result := range results {
		if result.Error == "" {
			placed++
		} else if failure == "" {
			failure = result.Error
		}
	}
	if failure == "" {
		return fmt.Sprintf("%d %s placed", placed, pluralize("order", placed)), true
	}
	return fmt.Sprintf("%d of %d %s failed: %s", len(results)-placed, len(results), pluralize("order", len(results)), failure), false
}

func (a *app) handleAlerts(msg tea.Msg) (tea.Cmd, bool) {
	w := &a.alerts
	switch msg := msg.(type) {
	case alertInstrumentMsg:
		return a.openAlertFormOn(msg.instrument, msg.ltp, msg.editing), true
	case alertsLoadedMsg:
		// Alerts set while these loaded are kept.
		for _, loaded := range msg.active {
			if !slices.ContainsFunc(w.active, loaded.same) {
				w.active = append(w.active, loaded)
			}
		}
		if msg.err != nil {
			a.notify(statusErr("Alerts failed to load:", msg.err.Error()))
		}
		return a.checkAlerts(true), true
	case alertQuotesMsg:
		w.checking = false
		settled := a.settled(msg.entries)
		if msg.source != nil {
			if msg.down && !w.tickerDown {
				a.notify(statusWarn("Alerts check quotes: the ticker is down") + statusInfo(" · tried again every 30s"))
			}
			if msg.fresh && msg.down {
				w.retryAt = time.Now().Add(alertTickerRetry)
			}
			w.source, w.tickerDown = msg.source, msg.down
		}
		if msg.source == nil && msg.err == nil {
			return settled, true
		}
		if msg.err != nil && len(msg.quotes) == 0 {
			if text := quoteErrorText(msg.err); text != w.failure {
				w.failure = text
				a.notify(statusWarn("Alerts cannot check prices: " + text))
			}
			return settled, true
		}
		w.failure = ""
		if w.ticks == nil {
			w.ticks = map[models.InstrumentID]models.Tick{}
		}
		for id, quote := range msg.quotes {
			if quote.LastPrice > 0 {
				w.ticks[models.InstrumentID(id)] = quoteTick(quote)
			}
		}
		if a.portfolio.tab == tabAlerts {
			a.portfolio.renderAlerts(w.ticks, true)
		}
		// The stores check every active alert quoted, those another
		// kite-cli set among them.
		var users []string
		for _, active := range w.active {
			if w.ticks[active.trigger.InstrumentID].LastPrice > 0 && !slices.Contains(users, active.user) {
				users = append(users, active.user)
			}
		}
		if len(users) == 0 {
			return settled, true
		}
		return tea.Batch(settled, a.evaluateAlerts(users, maps.Clone(w.ticks))), true
	case alertsEvaluatedMsg:
		w.checking = false
		for _, checked := range msg.checked {
			w.keep(checked)
		}
		// A firing deletes the rest of its OCO group.
		w.active = slices.DeleteFunc(w.active, func(active userTrigger) bool {
			return slices.ContainsFunc(msg.fired, func(fired firedAlert) bool {
				return fired.user == active.user && slices.Contains(fired.cancelled, active.trigger.ID)
			})
		})
		a.portfolio.alertsChecked(msg.checked, w.ticks)
		var reload tea.Cmd
		for _, fired := range msg.fired {
			if reload == nil {
				reload = a.alertsChanged(fired.user)
			}
		}
		if len(msg.fired) > 0 {
			a.notify(a.alertToast(msg.fired))
		}
		if msg.err == nil {
			w.evalFailure = ""
		} else if text := msg.err.Error(); text != w.evalFailure {
			w.evalFailure = text
			a.notify(statusErr("Alerts failed to check:", text))
		}
		return reload, true
	case alertSavedMsg:
		var reload tea.Cmd
		if msg.err == nil {
			w.keep(msg.saved)
			reload = a.alertsChanged(msg.saved.user)
		}
		if a.overlay != msg.form {
			return reload, true
		}
		if msg.err != nil {
			msg.form.saving = false
			msg.form.form.message = statusErr("Cannot save the alert:", msg.err.Error())
			return reload, true
		}
		a.closeOverlay()
		trigger := msg.saved.trigger
		verb := "set"
		if msg.edited {
			verb = "saved"
		}
		text := statusOK(fmt.Sprintf("Alert %s: %s", verb, alertText(trigger)))
		if trigger.Reference != 0 {
			text += statusInfo(" (" + awayText(trigger.Field, trigger.Value, trigger.Reference) + ")")
		}
		text += statusInfo(" · " + budgetText(trigger))
		if n := len(trigger.Orders); trigger.PlacesOrders() && n > 0 {
			text += statusInfo(fmt.Sprintf(" · places %d %s in %s", n, pluralize("order", n), modeName(broker.Mode(trigger.OrderMode))))
		}
		switch {
		case !trigger.SendsMessage():
			text += statusInfo(" · no message")
		case a.deps.alert == nil:
			text += statusInfo(" · shows here; Telegram is off")
		}
		a.notify(text)
		return tea.Batch(reload, a.checkAlerts(false)), true
	case alertsClearedMsg:
		w.active = slices.DeleteFunc(w.active, func(active userTrigger) bool {
			return slices.ContainsFunc(msg.cleared, active.same)
		})
		reload := a.alertsChanged(msg.form.user)
		if a.overlay != msg.form {
			return reload, true
		}
		if msg.err != nil {
			msg.form.saving = false
			msg.form.existing = slices.DeleteFunc(msg.form.existing, func(trigger models.PriceTrigger) bool {
				return slices.ContainsFunc(msg.cleared, func(cleared userTrigger) bool { return cleared.trigger.ID == trigger.ID })
			})
			msg.form.form.message = statusErr("Cannot clear the alerts:", msg.err.Error())
			return reload, true
		}
		a.closeOverlay()
		a.notify(statusOK(fmt.Sprintf("Cleared %d %s on %s", len(msg.cleared), pluralize("alert", len(msg.cleared)), symbolOf(msg.form.instrument.ID))))
		return reload, true
	case alertQuoteMsg:
		// Quotes standing in for the ticker are left for checks to retry.
		if msg.source != nil && w.source == nil && !msg.down {
			w.source = msg.source
		}
		if a.overlay != msg.form {
			return nil, true
		}
		if msg.quote.LastPrice <= 0 {
			if msg.form.ltp <= 0 {
				text := "no price"
				if msg.err != nil {
					text = quoteErrorText(msg.err)
				}
				msg.form.form.message = statusWarn("LTP unavailable: " + text + " · enter a value")
			}
			return nil, true
		}
		msg.form.priced(msg.quote)
		return nil, true
	}
	return nil, false
}

// keep puts an alert as it now is in the watch: in place of what it was,
// or gone once no longer active.
func (w *alertWatch) keep(alert userTrigger) {
	index := slices.IndexFunc(w.active, alert.same)
	switch {
	case alert.trigger.Status != models.TriggerActive:
		if index >= 0 {
			w.active = slices.Delete(w.active, index, index+1)
		}
	case index >= 0:
		w.active[index] = alert
	default:
		w.active = append(w.active, alert)
	}
}

// firesOnce reports whether trigger has the budget alerts are set with.
func firesOnce(trigger models.PriceTrigger) bool {
	return trigger.FiringBudget != nil && *trigger.FiringBudget == 1
}

// firingText is how often trigger fired of how often it may: "2/3", or
// "2/∞" without a limit.
func firingText(trigger models.PriceTrigger) string {
	budget := "∞"
	if trigger.FiringBudget != nil {
		budget = strconv.Itoa(*trigger.FiringBudget)
	}
	return fmt.Sprintf("%d/%s", trigger.TimesFired, budget)
}

// budgetText is how many times trigger may fire, in words.
func budgetText(trigger models.PriceTrigger) string {
	switch {
	case trigger.FiringBudget == nil:
		return "fires each time it holds"
	case *trigger.FiringBudget == 1:
		return "fires once"
	}
	return fmt.Sprintf("fires %d times", *trigger.FiringBudget)
}

// fieldInfo describes field, as a price when it is none known.
func fieldInfo(field models.TickField) models.TickFieldInfo {
	if info, ok := field.Info(); ok {
		return info
	}
	return models.TickFieldInfo{Field: field, Label: string(field), Kind: models.KindPrice}
}

// conditionSymbol is a condition as a symbol: ≥, or ↗ for crosses above.
func conditionSymbol(condition string) string {
	if index := slices.Index(models.TriggerConditions, condition); index >= 0 {
		symbol, _, _ := strings.Cut(conditionLabels[index], " ")
		return symbol
	}
	return condition
}

// conditionWords is a condition as alerts read: a symbol for a level, in
// words for a crossing.
func conditionWords(condition string) string {
	switch condition {
	case models.TriggerCrossAbove:
		return "crosses above"
	case models.TriggerCrossBelow:
		return "crosses below"
	}
	return conditionSymbol(condition)
}

// fieldValue is a value of field as it reads: a price in rupees, a change
// signed, a count grouped.
func fieldValue(field models.TickField, value float64) string {
	switch fieldInfo(field).Kind {
	case models.KindChange:
		text := formatSigned(value)
		if text[0] == '+' || text[0] == '-' {
			return text[:1] + "₹" + text[1:]
		}
		return "₹" + text
	case models.KindPercent:
		return fmt.Sprintf("%+.2f%%", value)
	case models.KindQuantity:
		return strings.TrimSuffix(formatAmount(math.Round(value)), ".00")
	}
	return "₹" + formatAmount(value)
}

// awayText is how far value is from from, a value of field: in percent
// for a price or a count, in rupees or points for a change.
func awayText(field models.TickField, value, from float64) string {
	switch fieldInfo(field).Kind {
	case models.KindChange:
		return fieldValue(field, value-from)
	case models.KindPercent:
		return fmt.Sprintf("%+.2f pts", value-from)
	}
	return formatPercent(value-from, from)
}

// fieldName is field's name as alerts read: its label, LTP for the last
// price.
func fieldName(field models.TickField) string {
	if field == models.FieldLastPrice {
		return "LTP"
	}
	return fieldInfo(field).Label
}

// conditionText is an alert's condition: "LTP ≥ ₹1,550.00".
func conditionText(trigger models.PriceTrigger) string {
	return fieldName(trigger.Field) + " " + conditionWords(trigger.Condition) + " " + fieldValue(trigger.Field, trigger.Value)
}

// actionText is what an alert does when it fires, in words.
func actionText(trigger models.PriceTrigger) string {
	if index := slices.Index(models.TriggerActions, trigger.Action); index >= 0 {
		return actionLabels[index]
	}
	return trigger.Action
}

// firedValueText is the value that fired an alert, with the LTP when the
// alert watches another field: "LTP ₹25,110.00", or "Volume 12,00,000 ·
// LTP ₹25,110.00".
func firedValueText(fired firedAlert) string {
	if fired.trigger.Field == models.FieldLastPrice {
		return "LTP " + fieldValue(models.FieldLastPrice, fired.value)
	}
	text := fieldName(fired.trigger.Field) + " " + fieldValue(fired.trigger.Field, fired.value)
	if fired.ltp > 0 {
		text += " · LTP ₹" + formatAmount(fired.ltp)
	}
	return text
}

// alertToast is the status bar's word on fired alerts: the first, and how
// many more.
func (a *app) alertToast(fired []firedAlert) string {
	first := fired[0]
	trigger := first.trigger
	text := statusWarn("Alert: ") + styleBold.Render(symbolOf(trigger.InstrumentID)) +
		styleText.Render(" "+conditionText(trigger)+" · "+firedValueText(first))
	if trigger.Note != "" {
		text += styleMuted.Render(" · " + trigger.Note)
	}
	if !firesOnce(trigger) {
		text += styleMuted.Render(" · fired " + firingText(trigger))
	}
	if first.user != a.activeUser() {
		text += styleMuted.Render(" · " + first.user)
	}
	if len(first.results) > 0 {
		if placed, ok := placedText(first.results); ok {
			text += statusOK(" · " + placed)
		} else {
			text += styleRed.Render(" · " + placed)
		}
	}
	if first.recordErr != nil {
		text += styleRed.Render(" · not recorded: " + first.recordErr.Error())
	}
	if n := len(first.cancelled); n > 0 {
		text += styleMuted.Render(fmt.Sprintf(" · cancelled %d linked %s", n, pluralize("alert", n)))
	}
	switch {
	case first.sent:
		text += statusInfo(" · sent to Telegram")
	case first.sendErr != nil:
		text += styleRed.Render(" · Telegram failed: " + first.sendErr.Error())
	}
	if len(fired) > 1 {
		text += statusInfo(fmt.Sprintf(" · and %d more", len(fired)-1))
	}
	return text
}

// alertMessage is the text a fired alert sends, with how placing its
// orders went.
func alertMessage(fired firedAlert) string {
	trigger := fired.trigger
	lines := []string{"🔔 " + alertText(trigger), firedValueText(fired)}
	if trigger.Reference != 0 {
		lines[1] += fmt.Sprintf(", %s since set at %s", awayText(trigger.Field, fired.value, trigger.Reference), fieldValue(trigger.Field, trigger.Reference))
	}
	if trigger.Note != "" {
		lines = append(lines, "Note: "+trigger.Note)
	}
	if !firesOnce(trigger) {
		lines = append(lines, "Fired "+firingText(trigger))
	}
	if len(fired.results) > 0 {
		lines = append(lines, "Orders ("+modeName(broker.Mode(trigger.OrderMode))+"):")
		for index, order := range trigger.Orders {
			if index >= len(fired.results) {
				break
			}
			if result := fired.results[index]; result.Error != "" {
				lines = append(lines, "✖ "+itemText(order)+": "+result.Error)
			} else {
				lines = append(lines, "✔ "+itemText(order)+" · "+result.OrderID)
			}
		}
	}
	return strings.Join(append(lines, fired.user+" · "+string(trigger.InstrumentID)), "\n")
}

// alertForm sets an alert on an instrument for a user, or edits one: WHEN
// a tick field Is past or crossing a Value, THEN it does an action. Value
// starts at the field's value now; for a price or a count, Move slides it
// a percentage either side, and Is follows the side it is on, while
// flipping Is to face the other way mirrors the value to the other side.
// Budget is how many times it may fire, empty for no limit; editing also
// shows its counts, which can be changed.
type alertForm struct {
	form       *form
	user       string
	instrument models.Instrument
	// quote is the instrument's latest tick, and ltp its last price; zero
	// until known.
	quote models.Tick
	ltp   float64
	// editing is the alert edited, as it was when the form opened; nil
	// sets a new one.
	editing *models.PriceTrigger
	// existing are the user's active alerts on the instrument, when
	// setting one.
	existing []models.PriceTrigger
	saving   bool
	// mode is the account the alert's orders go into: the one it was set
	// in.
	mode broker.Mode
	// fixed counts the Order field's options before the baskets, which
	// follow in order.
	fixed   int
	baskets []models.Basket
	// added are the orders made in the order form, in the order added.
	added []models.BasketItem
	// built is set once every field is added, when the fields can sync.
	built bool
}

// openAlertForm opens the alert form on instrument, last priced at ltp,
// for the active user.
func (a *app) openAlertForm(instrument models.Instrument, ltp float64) tea.Cmd {
	return a.openAlert(instrument, ltp, nil)
}

// editAlert opens the alert form on the Alerts tab's alert, to edit it.
func (a *app) editAlert(row alertRow) tea.Cmd {
	trigger := row.trigger
	return a.openAlert(a.portfolio.instrumentOf(trigger.InstrumentID), row.tick.LastPrice, &trigger)
}

// openAlert opens the alert form on instrument: a new alert, or editing
// one.
func (a *app) openAlert(instrument models.Instrument, ltp float64, editing *models.PriceTrigger) tea.Cmd {
	if a.deps.triggers == nil {
		a.notify(statusWarn("Alerts are off"))
		return nil
	}
	if a.activeUser() == "" {
		a.notify(statusWarn("Select a user to set alerts for"))
		return nil
	}
	// A bare ID, an alert's or a row's the portfolio has no details of, is
	// looked up first: what the alert can order depends on what it is.
	if instrument.Segment == "" && a.deps.openCatalog != nil {
		open := a.deps.openCatalog
		return a.task(func(ctx context.Context) tea.Msg {
			if catalog, _, err := open(ctx); err == nil {
				if found, err := catalog.Get(ctx, instrument.ID); err == nil {
					instrument = found
				}
			}
			return alertInstrumentMsg{instrument: instrument, ltp: ltp, editing: editing}
		})
	}
	return a.openAlertFormOn(instrument, ltp, editing)
}

// openAlertFormOn opens the alert form on instrument as it is, then
// quotes it for the fields' values now.
func (a *app) openAlertFormOn(instrument models.Instrument, ltp float64, editing *models.PriceTrigger) tea.Cmd {
	user := a.activeUser()
	if editing != nil {
		user = editing.UserID
	}
	if user == "" {
		return nil
	}
	// The last quote alerts checked serves until a fresh one comes.
	quote := a.alerts.ticks[instrument.ID]
	if ltp > 0 {
		quote.LastPrice = ltp
	}
	o := &alertForm{user: user, instrument: instrument, quote: quote, ltp: max(quote.LastPrice, 0), mode: a.mode, editing: editing}
	for _, active := range a.alerts.active {
		if editing == nil && active.user == user && active.trigger.InstrumentID == instrument.ID {
			o.existing = append(o.existing, active.trigger)
		}
	}
	buttons := []string{alertButtonSet}
	if editing != nil {
		buttons = []string{alertButtonSave}
	}
	if len(o.existing) > 0 {
		buttons = append(buttons, o.clearLabel())
	}
	f := newForm(10, 19, append(buttons, alertButtonCancel)...)
	o.form = f
	var labels []string
	for _, info := range models.TickFields {
		labels = append(labels, info.Label)
	}
	when := f.addSelect(alertFieldWhen, labels, 0)
	is := f.addSelect(alertFieldIs, conditionLabels, 0)
	f.addInput(alertFieldValue, "", decimalNumber, false)
	f.addSlider(alertFieldMove, 0, 0, 0, 0)
	then := f.addSelect(alertFieldThen, actionLabels, 0)
	o.addOrderFields(a)
	note, budget := "", "1"
	if editing != nil {
		note, budget = editing.Note, ""
		if editing.FiringBudget != nil {
			budget = strconv.Itoa(*editing.FiringBudget)
		}
		when.index = max(slices.IndexFunc(models.TickFields, func(info models.TickFieldInfo) bool { return info.Field == editing.Field }), 0)
		is.index = max(slices.Index(models.TriggerConditions, editing.Condition), 0)
		then.index = max(slices.Index(models.TriggerActions, editing.Action), 0)
		if editing.OrderMode != "" {
			o.mode = broker.Mode(editing.OrderMode)
		}
	}
	f.addInput(alertFieldNote, note, anyText, false)
	f.addInput(alertFieldBudget, budget, wholeNumber, false)
	if editing != nil {
		f.addInput(alertFieldFired, strconv.Itoa(editing.TimesFired), wholeNumber, false)
		f.addInput(alertFieldEvaluated, strconv.Itoa(editing.TimesEvaluated), wholeNumber, false)
		f.addInput(alertFieldLastFired, lastFiredText(editing.LastFired), anyText, false)
	}
	if len(o.existing) > 0 {
		var set []string
		for _, trigger := range o.existing {
			set = append(set, conditionText(trigger))
		}
		f.addNote(alertFieldActive, strings.Join(set, " · "))
	}
	o.built = true
	o.chose()
	if editing != nil {
		o.setText(o.formatValue(editing.Value))
		if len(editing.Orders) > 0 {
			// Its orders are edited as the order form's.
			o.added = slices.Clone(editing.Orders)
			order := f.field(alertFieldOrder)
			order.index = max(slices.Index(order.options, alertOrderForm), 0)
		}
		o.measure()
	}
	f.focusField(alertFieldValue)
	o.sync()
	a.overlay = o
	var baskets tea.Cmd
	if a.deps.baskets != nil {
		baskets = a.basketOperation(user, "", nil, func(a *app, _ string, stored []models.Basket) tea.Cmd {
			o.listBaskets(a, stored)
			return nil
		})
	}
	if o.ltp <= 0 {
		f.message = statusInfo("Fetching the LTP…")
	}
	source, down, load := a.alertQuotes()
	if source == nil && load == nil {
		return baskets
	}
	id := string(instrument.ID)
	return tea.Batch(baskets, a.task(func(ctx context.Context) tea.Msg {
		given := source != nil
		source, quotes, fellBack, err := quoteOn(ctx, source, load, id)
		msg := alertQuoteMsg{form: o, source: source, down: fellBack || given && down, err: err}
		if quote, ok := quotes[id]; ok {
			msg.quote = quoteTick(quote)
		}
		return msg
	}))
}

// addOrderFields adds the orders the alert places, when its action places
// any: a buy or sell of the instrument when it trades, orders made in the
// order form, or one of the user's baskets.
func (o *alertForm) addOrderFields(a *app) {
	f := o.form
	var options []string
	if o.instrument.Segment != "INDICES" {
		options = append(options, alertOrderBuy, alertOrderSell)
	}
	if o.formOrders(a) || (o.editing != nil && len(o.editing.Orders) > 0) {
		options = append(options, alertOrderForm)
	}
	o.fixed = len(options)
	f.addSelect(alertFieldOrder, options, 0)
	lot := o.lot()
	f.addCounter(alertFieldQty, lot, lot, lot)
	f.addSelect(alertFieldType, []string{kite.OrderTypeMarket, kite.OrderTypeLimit}, 0)
	f.addInput(alertFieldLimit, "", decimalNumber, false)
	f.addSelect(alertFieldProduct, orderProducts(o.instrument), 0)
	f.addNote(alertFieldOrders, "")
	f.addNote(alertFieldAccount, "")
	o.listBaskets(a, nil)
}

// formOrders reports whether the order form can order for the alert: on
// the instrument itself, or its underlying's options and futures.
func (o *alertForm) formOrders(a *app) bool {
	if a.deps.place == nil {
		return false
	}
	_, derivatives := optionUnderlying(o.instrument)
	return derivatives || o.instrument.Segment != "INDICES"
}

// own reports whether the Order field picks the order form's orders.
func (o *alertForm) own() bool {
	return o.form.field(alertFieldOrder).value() == alertOrderForm
}

func (o *alertForm) lot() int {
	return max(o.instrument.LotSize, 1)
}

// listBaskets offers the user's stored baskets, then their drafts, keeping
// the one chosen.
func (o *alertForm) listBaskets(a *app, stored []models.Basket) {
	chosen, _ := o.basket()
	o.baskets = slices.Clone(stored)
	for _, draft := range a.baskets.drafts {
		if draft.CreatedBy == o.user {
			o.baskets = append(o.baskets, draft)
		}
	}
	order := o.form.field(alertFieldOrder)
	order.options = order.options[:o.fixed]
	for index, basket := range o.baskets {
		name := basket.Name
		if isDraft(basket.ID) {
			name += " *"
		}
		order.options = append(order.options, fmt.Sprintf("%s (%d)", name, len(basket.Items)))
		if basket.ID == chosen.ID {
			order.index = o.fixed + index
		}
	}
	if order.index >= len(order.options) {
		order.index = 0
	}
	o.sync()
}

// basket is the basket the Order field names, if it names one.
func (o *alertForm) basket() (models.Basket, bool) {
	index := o.form.field(alertFieldOrder).index - o.fixed
	if index < 0 || index >= len(o.baskets) {
		return models.Basket{}, false
	}
	return o.baskets[index], true
}

// side is the Order field's BUY or SELL of the instrument, or "".
func (o *alertForm) side() string {
	switch o.form.field(alertFieldOrder).value() {
	case alertOrderBuy:
		return kite.TransactionTypeBuy
	case alertOrderSell:
		return kite.TransactionTypeSell
	}
	return ""
}

// places reports whether Then places orders.
func (o *alertForm) places() bool {
	return models.PriceTrigger{Action: o.action()}.PlacesOrders()
}

// orders are what the alert places when it fires, buys first, or why they
// cannot be; none when Then only sends a message.
func (o *alertForm) orders() ([]models.BasketItem, error) {
	f := o.form
	if !o.places() {
		return nil, nil
	}
	if o.own() {
		if len(o.added) == 0 {
			return nil, errors.New("add an order: Enter on Order opens the order form")
		}
		return buysFirst(o.added), nil
	}
	if basket, ok := o.basket(); ok {
		if len(basket.Items) == 0 {
			return nil, fmt.Errorf("%s has no orders", basket.Name)
		}
		return buysFirst(basket.Items), nil
	}
	side := o.side()
	if side == "" {
		return nil, errors.New("pick the orders to place in Order")
	}
	qty, _ := strconv.Atoi(f.field(alertFieldQty).value())
	if lot := o.lot(); qty <= 0 || qty%lot != 0 {
		return nil, fmt.Errorf("the quantity must be a multiple of %d", lot)
	}
	order := models.BasketItem{TransactionType: side, OrderType: f.field(alertFieldType).value(), Product: f.field(alertFieldProduct).value(), Quantity: qty}
	order.Exchange, order.TradingSymbol, _ = strings.Cut(string(o.instrument.ID), ":")
	if order.OrderType == kite.OrderTypeLimit {
		order.Price, _ = strconv.ParseFloat(f.field(alertFieldLimit).value(), 64)
		if order.Price <= 0 {
			return nil, errors.New("enter a limit price above zero")
		}
	}
	return []models.BasketItem{order}, nil
}

// buysFirst copies items as an alert keeps them, without their basket IDs
// and times, buys first so short legs are covered.
func buysFirst(items []models.BasketItem) []models.BasketItem {
	orders := make([]models.BasketItem, len(items))
	for index, item := range items {
		item.ID, item.CreatedAt, item.UpdatedAt = "", time.Time{}, time.Time{}
		orders[index] = item
	}
	slices.SortStableFunc(orders, func(x, y models.BasketItem) int {
		return cmp.Compare(boolRank(x.TransactionType != kite.TransactionTypeBuy), boolRank(y.TransactionType != kite.TransactionTypeBuy))
	})
	return orders
}

// syncOrder offers Then's orders only when there are some to place, and
// shows the fields the chosen orders need.
func (o *alertForm) syncOrder() {
	f := o.form
	order, then := f.field(alertFieldOrder), f.field(alertFieldThen)
	then.disabled = len(order.options) == 0
	if then.disabled {
		then.index = 0
	}
	places := o.places()
	order.hidden = !places
	_, isBasket := o.basket()
	isBasket = places && (isBasket || o.own())
	single := places && o.side() != ""
	for _, label := range []string{alertFieldQty, alertFieldType, alertFieldProduct} {
		f.field(label).hidden = !single
	}
	f.field(alertFieldLimit).hidden = !single || f.field(alertFieldType).value() != kite.OrderTypeLimit
	qty := f.field(alertFieldQty)
	qty.hint = ""
	if value, _ := strconv.Atoi(qty.value()); value <= 0 || value%o.lot() != 0 {
		qty.hint = statusWarn(fmt.Sprintf("a multiple of %d", o.lot()))
	} else if o.lot() > 1 {
		qty.hint = styleFaint.Render(fmt.Sprintf("%d %s", value/o.lot(), pluralize("lot", value/o.lot())))
	}

	orders := f.field(alertFieldOrders)
	orders.hidden = !isBasket
	if isBasket {
		// In the order they go: buys first.
		items, _ := o.orders()
		var lines []string
		for index, item := range items {
			if index == 4 {
				lines = append(lines, fmt.Sprintf("and %d more", len(items)-index))
				break
			}
			lines = append(lines, itemText(item)+" "+item.OrderType)
		}
		orders.note = styleText.Render(strings.Join(lines, "\n"))
		switch {
		case o.own() && len(lines) == 0:
			orders.note = styleMuted.Render("none yet · Enter on Order adds one")
		case o.own():
			orders.note += "\n" + styleFaint.Render("Enter adds another · Backspace drops the last")
		case len(lines) == 0:
			orders.note = styleText.Render("no orders")
		}
	}
	account := f.field(alertFieldAccount)
	account.hidden = !single && !isBasket
	account.note = modePill(o.mode) + styleMuted.Render(" places them when it fires")
	if o.mode == broker.Live {
		account.note = modePill(o.mode) + styleRed.Render(" real orders, placed when it fires")
	}
}

func (o *alertForm) clearLabel() string {
	return fmt.Sprintf("Clear %d", len(o.existing))
}

func (o *alertForm) tickSize() float64 {
	if tick := o.instrument.TickSize; tick > 0 {
		return tick
	}
	return 0.05
}

// field is the tick field When names.
func (o *alertForm) field() models.TickFieldInfo {
	return models.TickFields[o.form.field(alertFieldWhen).index]
}

// condition is the condition Is names.
func (o *alertForm) condition() string {
	return models.TriggerConditions[o.form.field(alertFieldIs).index]
}

// action is the action Then names.
func (o *alertForm) action() string {
	return models.TriggerActions[o.form.field(alertFieldThen).index]
}

// now is the field's value in the latest quote, if it has one.
func (o *alertForm) now() (float64, bool) {
	return o.quote.Value(o.field().Field)
}

// value is the value typed, if it is a number.
func (o *alertForm) value() (float64, bool) {
	value, err := strconv.ParseFloat(o.form.field(alertFieldValue).value(), 64)
	return value, err == nil
}

// movable reports whether Move slides the value: a price or a count, by a
// percentage of its value now.
func (o *alertForm) movable() bool {
	kind := o.field().Kind
	return kind == models.KindPrice || kind == models.KindQuantity
}

// scale is how far, in percent, Move reaches either side of the value
// now, and its step.
func (o *alertForm) scale() (reach, stride float64) {
	if o.field().Kind == models.KindQuantity {
		return 100, 5
	}
	return alertScale(o.instrument)
}

// snap rounds value to what the field takes: a price to the tick, at
// least one, a change to the tick, a percentage to a hundredth, a count
// to a whole number, at least zero.
func (o *alertForm) snap(value float64) float64 {
	tick := o.tickSize()
	switch o.field().Kind {
	case models.KindPrice:
		return math.Max(math.Round(value/tick), 1) * tick
	case models.KindChange:
		return math.Round(value/tick) * tick
	case models.KindPercent:
		return math.Round(value*100) / 100
	}
	return math.Max(math.Round(value), 0)
}

// formatValue writes value as Value shows it.
func (o *alertForm) formatValue(value float64) string {
	switch o.field().Kind {
	case models.KindPrice, models.KindChange:
		return formatTicked(value, o.tickSize())
	case models.KindPercent:
		return strconv.FormatFloat(value, 'f', 2, 64)
	}
	return strconv.FormatFloat(value, 'f', 0, 64)
}

func (o *alertForm) setText(text string) {
	input := &o.form.field(alertFieldValue).input
	input.SetValue(text)
	input.CursorEnd()
}

// setValue shows value, snapped to what the field takes.
func (o *alertForm) setValue(value float64) {
	o.setText(o.formatValue(o.snap(value)))
}

// upward and downward report whether a condition faces up, holding past a
// value from below, or down; equals faces neither way.
func upward(condition string) bool {
	return condition == models.TriggerGTE || condition == models.TriggerGT || condition == models.TriggerCrossAbove
}

func downward(condition string) bool {
	return condition == models.TriggerLTE || condition == models.TriggerLT || condition == models.TriggerCrossBelow
}

// mirroredConditions face each condition the other way.
var mirroredConditions = map[string]string{
	models.TriggerGTE: models.TriggerLTE, models.TriggerLTE: models.TriggerGTE,
	models.TriggerGT: models.TriggerLT, models.TriggerLT: models.TriggerGT,
	models.TriggerCrossAbove: models.TriggerCrossBelow, models.TriggerCrossBelow: models.TriggerCrossAbove,
}

// follow faces Is towards the side of the value now value is on.
func (o *alertForm) follow(value float64) {
	now, known := o.now()
	condition := o.condition()
	if !known || value == now || (value > now && !downward(condition)) || (value < now && !upward(condition)) {
		return
	}
	if mirrored, ok := mirroredConditions[condition]; ok {
		o.form.field(alertFieldIs).index = slices.Index(models.TriggerConditions, mirrored)
	}
}

// chose sets Value and Move up for the field When names, starting Value
// at the field's value now.
func (o *alertForm) chose() {
	f := o.form
	input, move := f.field(alertFieldValue), f.field(alertFieldMove)
	switch o.field().Kind {
	case models.KindPrice:
		input.numeric = decimalNumber
	case models.KindQuantity:
		input.numeric = wholeNumber
	default:
		input.numeric = signedDecimal
	}
	reach, stride := o.scale()
	move.low, move.high, move.stride, move.amount = -reach, reach, stride, 0
	o.setText("")
	if now, known := o.now(); known {
		o.setValue(now)
	}
	o.sync()
}

// priced takes a quote fetched after the form opened, starting an empty
// Value at the field's value now.
func (o *alertForm) priced(quote models.Tick) {
	o.quote, o.ltp = quote, quote.LastPrice
	if o.form.message == statusInfo("Fetching the LTP…") {
		o.form.message = ""
	}
	if _, typed := o.value(); !typed {
		if now, known := o.now(); known {
			o.setValue(now)
		}
	}
	o.measure()
}

// measure points the slider at the value, leaving Is as it is.
func (o *alertForm) measure() {
	value, ok := o.value()
	if now, known := o.now(); ok && known && o.movable() && now > 0 {
		o.form.field(alertFieldMove).amount = (value/now - 1) * 100
	}
	o.sync()
}

// typed moves the slider and Is to the value typed.
func (o *alertForm) typed() {
	if value, ok := o.value(); ok {
		o.follow(value)
	}
	o.measure()
}

// slid sets the value the slider points at.
func (o *alertForm) slid() {
	now, known := o.now()
	if !known || now <= 0 {
		return
	}
	o.setValue(now * (1 + o.form.field(alertFieldMove).amount/100))
	if value, ok := o.value(); ok {
		o.follow(value)
	}
	o.sync()
}

// flipped mirrors the value to the side of the value now that Is faces.
func (o *alertForm) flipped() {
	value, ok := o.value()
	now, known := o.now()
	condition := o.condition()
	if ok && known && ((value < now && upward(condition)) || (value > now && downward(condition))) {
		o.setValue(2*now - value)
		o.typed()
		return
	}
	o.sync()
}

// syncValue shows how far the value is from the field's now, warning when
// the alert would fire at once.
func (o *alertForm) syncValue() {
	f := o.form
	input, move := f.field(alertFieldValue), f.field(alertFieldMove)
	field := o.field()
	now, known := o.now()
	input.hint = ""
	move.hidden = !o.movable()
	move.disabled = !known || now <= 0
	move.hint = styleFaint.Render("needs the LTP")
	value, ok := o.value()
	if !ok || !known {
		return
	}
	move.hint = styleText.Render(awayText(field.Field, value, now))
	trigger := models.PriceTrigger{Condition: o.condition(), Value: value}
	switch {
	case trigger.Crossing():
		input.hint = styleFaint.Render("now " + fieldValue(field.Field, now) + " · fires when it crosses")
	case trigger.Holds(now, nil):
		input.hint = statusWarn("fires at once")
	case field.Field != models.FieldLastPrice:
		input.hint = styleFaint.Render("now " + fieldValue(field.Field, now))
	}
}

// syncCounts says what Budget allows and how Last fired is written.
func (o *alertForm) syncCounts() {
	f := o.form
	budget := f.field(alertFieldBudget)
	limit, err := o.budget()
	switch {
	case err != nil:
		budget.hint = statusWarn(err.Error())
	case limit == nil:
		budget.hint = styleFaint.Render("no limit · fires each time it holds")
	case *limit == 0:
		budget.hint = statusWarn("never fires")
	case o.editing != nil:
		fired, _ := strconv.Atoi(f.field(alertFieldFired).value())
		budget.hint = styleFaint.Render(fmt.Sprintf("%d left · empty: no limit", max(*limit-fired, 0)))
	case *limit == 1:
		budget.hint = styleFaint.Render("fires once · empty: no limit")
	default:
		budget.hint = styleFaint.Render(fmt.Sprintf("fires %d times · empty: no limit", *limit))
	}
	if o.editing != nil {
		last := f.field(alertFieldLastFired)
		last.hint = styleFaint.Render("local · empty: never")
		if _, err := o.lastFired(); err != nil {
			last.hint = statusWarn("YYYY-MM-DD hh:mm:ss")
		}
	}
}

// budget is how many times the alert may fire, nil for no limit.
func (o *alertForm) budget() (*int, error) {
	text := o.form.field(alertFieldBudget).value()
	if text == "" {
		return nil, nil
	}
	budget, err := strconv.Atoi(text)
	if err != nil {
		return nil, errors.New("a whole number, or empty")
	}
	return &budget, nil
}

// lastFired is when Last fired says the alert last fired, zero for never.
func (o *alertForm) lastFired() (time.Time, error) {
	text := o.form.field(alertFieldLastFired).value()
	if text == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation(lastFiredLayout, text, time.Local)
}

// lastFiredText is at as Last fired shows it, "" for never.
func lastFiredText(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Local().Format(lastFiredLayout)
}

// sync shows how far the value is from now, warning when the alert would
// fire at once, what its budget allows, and the fields its orders need.
func (o *alertForm) sync() {
	if !o.built {
		return
	}
	o.syncValue()
	o.syncCounts()
	o.syncOrder()
}

func (o *alertForm) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		a.closeOverlay()
		return nil
	}
	if o.saving {
		return nil
	}
	current := o.form.current()
	// On the order form's orders, Enter adds one there and Backspace
	// takes the last off.
	if current != nil && current.label == alertFieldOrder && o.own() {
		switch msg.String() {
		case "enter":
			return a.openAlertOrder(o)
		case "backspace":
			if last := len(o.added) - 1; last >= 0 {
				o.form.message = statusInfo("Removed " + itemText(o.added[last]))
				o.added = o.added[:last]
				o.sync()
			}
			return nil
		}
	}
	pressed, changed := o.form.update(msg)
	if changed && current != nil {
		switch current.label {
		case alertFieldWhen:
			o.chose()
		case alertFieldIs:
			o.flipped()
		case alertFieldValue:
			o.typed()
		case alertFieldMove:
			o.slid()
		case alertFieldType:
			// A limit starts at the alert's price, or the LTP.
			if limit := o.form.field(alertFieldLimit); limit.value() == "" {
				price := o.ltp
				if value, ok := o.value(); ok && value > 0 && o.field().Field == models.FieldLastPrice {
					price = value
				}
				if price > 0 {
					limit.input.SetValue(formatTicked(price, o.tickSize()))
					limit.input.CursorEnd()
				}
			}
			o.sync()
		default:
			o.sync()
		}
	}
	if pressed < 0 {
		return nil
	}
	switch o.form.buttons[pressed] {
	case alertButtonSet, alertButtonSave:
		return o.save(a)
	case alertButtonCancel:
		a.closeOverlay()
		return nil
	}
	return o.clear(a)
}

// save checks the alert and its orders, asks before an alert that places
// LIVE orders, and stores it.
func (o *alertForm) save(a *app) tea.Cmd {
	field := o.field()
	value, ok := o.value()
	switch {
	case !ok:
		o.form.message = statusWarn("Enter a value for " + field.Label)
		o.form.focusField(alertFieldValue)
		return nil
	case field.Kind == models.KindPrice && value <= 0:
		o.form.message = statusWarn("Enter a price above zero")
		o.form.focusField(alertFieldValue)
		return nil
	}
	budget, err := o.budget()
	if err != nil {
		o.form.message = statusWarn("Budget: " + err.Error())
		o.form.focusField(alertFieldBudget)
		return nil
	}
	orders, err := o.orders()
	if err != nil {
		o.form.message = statusWarn(err.Error())
		return nil
	}
	trigger := models.PriceTrigger{
		InstrumentID: o.instrument.ID,
		Field:        field.Field,
		Condition:    o.condition(),
		Value:        value,
		Action:       o.action(),
		Note:         o.form.field(alertFieldNote).value(),
		FiringBudget: budget,
		Orders:       orders,
	}
	if now, known := o.now(); known {
		trigger.Reference = now
	}
	if o.editing != nil {
		if _, err := o.lastFired(); err != nil {
			o.form.message = statusWarn("Last fired: write it as YYYY-MM-DD hh:mm:ss, or leave it empty")
			o.form.focusField(alertFieldLastFired)
			return nil
		}
	}
	if len(orders) == 0 {
		return o.store(a, trigger)
	}
	if a.deps.place == nil {
		o.form.message = statusWarn("Ordering is unavailable")
		return nil
	}
	trigger.OrderMode = string(o.mode)
	if o.mode != broker.Live {
		return o.store(a, trigger)
	}
	lines := []string{fmt.Sprintf("When %s, place %d %s in %s's %s Kite account:\n",
		alertText(trigger), len(orders), pluralize("order", len(orders)), o.user, styleRed.Bold(true).Render("LIVE"))}
	for _, order := range orders {
		lines = append(lines, "  "+itemText(order)+" "+order.OrderType)
	}
	a.confirm("Live orders", strings.Join(lines, "\n"), o.form.buttons[0], func(a *app) tea.Cmd {
		a.overlay = o
		return o.store(a, trigger)
	})
	return nil
}

// store saves the alert, or the edits to it. The form stays open with the
// error if that fails.
func (o *alertForm) store(a *app, trigger models.PriceTrigger) tea.Cmd {
	o.saving = true
	o.form.message = statusInfo("Saving…")
	store, user := a.deps.triggers, o.user
	if o.editing != nil {
		id, edit := o.editing.ID, o.edit(trigger)
		return a.task(func(ctx context.Context) tea.Msg {
			saved, err := store.UpdatePriceTrigger(ctx, user, id, edit)
			return alertSavedMsg{form: o, saved: userTrigger{user: user, trigger: saved}, edited: true, err: err}
		})
	}
	return a.task(func(ctx context.Context) tea.Msg {
		saved, err := store.AddPriceTrigger(ctx, user, trigger)
		return alertSavedMsg{form: o, saved: userTrigger{user: user, trigger: saved}, err: err}
	})
}

// edit is the change the form makes to the alert it edits, as stored when
// saved: its condition, action, budget and orders, and the counts changed
// here. Checks go on while the form is open, so counts left alone keep
// theirs.
func (o *alertForm) edit(trigger models.PriceTrigger) func(*models.PriceTrigger) error {
	f, original := o.form, *o.editing
	fired, _ := strconv.Atoi(f.field(alertFieldFired).value())
	evaluated, _ := strconv.Atoi(f.field(alertFieldEvaluated).value())
	lastText := f.field(alertFieldLastFired).value()
	lastFired, _ := o.lastFired()
	return func(stored *models.PriceTrigger) error {
		// A new condition waits for no firing the old one made, and a new
		// field crosses from its own values.
		if stored.Field != trigger.Field || stored.Condition != trigger.Condition || stored.Value != trigger.Value {
			stored.Tripped = false
		}
		if stored.Field != trigger.Field {
			stored.LastValue = nil
		}
		stored.Field, stored.Condition, stored.Value, stored.Action = trigger.Field, trigger.Condition, trigger.Value, trigger.Action
		stored.Note, stored.FiringBudget, stored.Orders, stored.OrderMode = trigger.Note, trigger.FiringBudget, trigger.Orders, trigger.OrderMode
		if fired != original.TimesFired {
			stored.TimesFired = fired
		}
		if evaluated != original.TimesEvaluated {
			stored.TimesEvaluated = evaluated
		}
		if lastText != lastFiredText(original.LastFired) {
			stored.LastFired = lastFired
		}
		return nil
	}
}

// clear deletes the user's active alerts on the instrument.
func (o *alertForm) clear(a *app) tea.Cmd {
	o.saving = true
	o.form.message = statusInfo("Clearing…")
	store, user, existing := a.deps.triggers, o.user, slices.Clone(o.existing)
	return a.task(func(ctx context.Context) tea.Msg {
		msg := alertsClearedMsg{form: o}
		for _, trigger := range existing {
			if msg.err = store.DeletePriceTrigger(ctx, user, trigger.ID); msg.err != nil {
				break
			}
			msg.cleared = append(msg.cleared, userTrigger{user: user, trigger: trigger})
		}
		return msg
	})
}

func (o *alertForm) view(_ *app, w, _ int) string {
	width := min(62, w-2)
	name := fallback(strings.TrimSpace(o.instrument.DisplayName), o.instrument.TradingSymbol)
	name = fallback(name, symbolOf(o.instrument.ID))
	ltp := "—"
	if o.ltp > 0 {
		ltp = "₹" + formatAmount(o.ltp)
	}
	header := styleBold.Render(name) + "\n" +
		styleFaint.Render(fmt.Sprintf("%s · LTP %s · for %s", o.instrument.ID, ltp, o.user))
	help := "↑↓ field · ←→ change"
	if o.movable() {
		_, stride := o.scale()
		help += fmt.Sprintf(" · Shift+←→ slide %g%%", stride*sliderShift)
	}
	title := "Alert"
	if o.editing != nil {
		title = fmt.Sprintf("Edit alert #%d", o.editing.ID)
	}
	return modal(title, "", header+"\n\n"+o.form.view(width-4)+"\n\n"+styleFaint.Render(help+" · Esc cancel"), width)
}

// openAlertOrder opens the order form on the alert's instrument, or for an
// index on its nearest call, to add an order to the alert.
func (a *app) openAlertOrder(o *alertForm) tea.Cmd {
	return a.openTradeTicket(tradeTarget{instrument: o.instrument, ltp: o.ltp}, o)
}

// addAlertOrder adds the order form's order to its alert and goes back to
// the alert.
func (a *app) addAlertOrder(form *orderForm) tea.Cmd {
	_, params, err := form.order()
	if err != nil {
		form.summary = styleRed.Render(err.Error())
		return nil
	}
	o := form.ticket.alert
	item := basketItemOf(params)
	item.Validity, item.Tag = "", ""
	o.added = append(o.added, item)
	a.overlay = o
	o.sync()
	o.form.message = statusOK("Added " + itemText(item))
	return nil
}
