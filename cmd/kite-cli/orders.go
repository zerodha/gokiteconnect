package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// Order form labels.
const (
	orderFieldSide     = "Side"
	orderFieldQuantity = "Quantity"
	orderFieldType     = "Order type"
	orderFieldPrice    = "Price"
	orderFieldTrigger  = "Trigger price"
	orderFieldProduct  = "Product"
	orderFieldStoploss = "Stoploss"
	orderFieldTarget   = "Target"
)

// orderForm is the buy and sell form.
type orderForm struct {
	form   *form
	ticket orderTicket
	// userID and mode are the account the order goes to.
	userID  string
	mode    broker.Mode
	placing bool
	// summary shows the order as it will be placed, or why it cannot be.
	summary string
	// charges estimates what the order costs.
	charges string
	// margin is what the order blocks, fetched for marginFor; marginSeq
	// counts the orders it was asked for, so only the last one is fetched.
	margin    string
	marginFor kite.OrderMarginParam
	marginSeq int
	// target is what a form + opened is for, and legs its tabs; both are
	// nil on other forms, and legs on a form with no derivatives to offer.
	target *tradeTarget
	legs   *tradeLegs
}

type (
	// orderMarginDueMsg fetches the margin of leg once the form has rested
	// on it.
	orderMarginDueMsg struct {
		form *orderForm
		seq  int
	}
	orderMarginMsg struct {
		form    *orderForm
		leg     kite.OrderMarginParam
		margins kite.BasketMargins
		err     error
	}
)

type orderDoneMsg struct {
	form    *orderForm
	userID  string
	mode    broker.Mode
	orderID string
	params  kite.OrderParams
	// modified reports a change to an open order rather than a new one.
	modified bool
	err      error
	// exits are the stoploss and target alerts set on the placed order,
	// and exitErr why setting them failed.
	exits   []userTrigger
	exitErr error
}

// portfolioMode is the account the shown portfolio is of: its orders and
// positions are changed there.
func (a *app) portfolioMode() broker.Mode {
	return cmp.Or(a.portfolio.data.mode, a.mode)
}

// openOrder opens the order form for ticket, placing for ticket's user or
// else the active one.
func (a *app) openOrder(ticket orderTicket) tea.Cmd {
	o, err := a.newOrderForm(ticket)
	if err != nil {
		a.showError(err)
		return nil
	}
	a.overlay = o
	return a.orderMargin(o)
}

// newOrderForm builds the order form for ticket.
func (a *app) newOrderForm(ticket orderTicket) (*orderForm, error) {
	accounts := a.users.users
	if len(accounts) == 0 && ticket.userID == "" {
		return nil, errors.New("add a user before placing orders")
	}
	if ticket.side == "" {
		ticket.side = kite.TransactionTypeBuy
	}
	if ticket.orderType == "" {
		ticket.orderType = kite.OrderTypeMarket
	}
	products := orderProducts(ticket.instrument)
	if ticket.product == "" {
		ticket.product = products[0]
	} else if !slices.Contains(products, ticket.product) {
		// Keep a position's own product, such as MTF, to close it.
		products = append(products, ticket.product)
	}
	// An open order is modified with the session that placed it, and a
	// basket's orders go to its owner.
	o := &orderForm{ticket: ticket, mode: a.mode, userID: ticket.userID}
	if ticket.alert != nil {
		o.mode = ticket.alert.mode
	}
	if o.userID == "" {
		o.userID = cmp.Or(a.activeUser(), accounts[0].UserID)
	}
	if ticket.modify != nil || ticket.closing {
		// Open orders and positions are the portfolio's account's.
		o.mode = a.portfolioMode()
	}
	price, trigger := "", ""
	switch {
	case ticket.price > 0:
		price = strconv.FormatFloat(ticket.price, 'f', -1, 64)
	case ticket.ltp > 0:
		price = strconv.FormatFloat(ticket.ltp, 'f', -1, 64)
	}
	if ticket.trigger > 0 {
		trigger = strconv.FormatFloat(ticket.trigger, 'f', -1, 64)
	}
	// A new order can wait in the basket instead; a basket order is saved
	// back to it.
	buttons := []string{"Place", "Add to basket", "Cancel"}
	switch {
	case ticket.modify != nil:
		buttons = []string{"Modify", "Cancel"}
	case ticket.item != nil:
		buttons = []string{"Save to basket", "Cancel"}
	case ticket.alert != nil:
		buttons = []string{"Add to alert", "Cancel"}
	}
	f := newForm(14, 14, buttons...)
	f.addSelect(orderFieldSide, orderSides, max(slices.Index(orderSides, ticket.side), 0))
	f.addInput(orderFieldQuantity, strconv.Itoa(ticket.quantity), wholeNumber, false)
	f.addSelect(orderFieldType, orderTypes, max(slices.Index(orderTypes, ticket.orderType), 0))
	f.addInput(orderFieldPrice, price, decimalNumber, false)
	f.addInput(orderFieldTrigger, trigger, decimalNumber, false)
	f.addSelect(orderFieldProduct, products, max(slices.Index(products, ticket.product), 0))
	// A new order can be guarded by a stoploss and a target, set as alerts
	// once it is placed.
	if ticket.modify == nil && ticket.item == nil && ticket.alert == nil && !ticket.closing && a.deps.triggers != nil {
		f.addInput(orderFieldStoploss, "", decimalNumber, false)
		f.addInput(orderFieldTarget, "", decimalNumber, false)
	}
	if ticket.modify != nil {
		// Kite changes only an order's quantity, type and prices.
		f.field(orderFieldSide).disabled = true
		f.field(orderFieldProduct).disabled = true
	}
	o.form = f
	o.refresh()
	// Start on the quantity, the field most orders change.
	f.focusField(orderFieldQuantity)
	return o, nil
}

// orderMargin asks for the margin of the form's order once it has rested
// for marginDelay, so typing does not ask Kite at every keystroke, unless
// it is already shown.
func (a *app) orderMargin(o *orderForm) tea.Cmd {
	_, params, err := o.order()
	if err != nil || a.deps.basketMargins == nil {
		o.margin, o.marginFor = "", kite.OrderMarginParam{}
		return nil
	}
	leg := marginLeg(params)
	if leg == o.marginFor {
		return nil
	}
	o.marginSeq++
	o.marginFor, o.margin = leg, styleFaint.Render("Margin …")
	seq := o.marginSeq
	return tea.Tick(marginDelay, func(time.Time) tea.Msg { return orderMarginDueMsg{form: o, seq: seq} })
}

// order reads the form into the account and the order to place.
func (o *orderForm) order() (string, kite.OrderParams, error) {
	if o.legs != nil && o.legs.missing != "" {
		return o.userID, kite.OrderParams{}, errors.New(o.legs.missing)
	}
	f := o.form
	params, err := orderParams(o.ticket.instrument, orderInput{
		side:      f.field(orderFieldSide).value(),
		orderType: f.field(orderFieldType).value(),
		product:   f.field(orderFieldProduct).value(),
		quantity:  f.field(orderFieldQuantity).value(),
		price:     f.field(orderFieldPrice).value(),
		trigger:   f.field(orderFieldTrigger).value(),
	})
	if err == nil {
		err = stopLossError(params, o.ticket.ltp)
	}
	if err == nil {
		_, _, err = o.exitLevels(params)
	}
	return o.userID, params, err
}

// entryPrice is what params is expected to fill at: its limit price, an
// SL-M's trigger, or else the last price; 0 when unknown.
func (o *orderForm) entryPrice(params kite.OrderParams) float64 {
	switch params.OrderType {
	case kite.OrderTypeLimit, kite.OrderTypeSL:
		return params.Price
	case kite.OrderTypeSLM:
		return params.TriggerPrice
	}
	return o.ticket.ltp
}

// exitLevels are the stoploss and target the form guards params with, 0
// for one left empty, or why they cannot guard it: a buy's stoploss goes
// below its entry and its target above, a sell's the other way round.
func (o *orderForm) exitLevels(params kite.OrderParams) (stop, target float64, err error) {
	f := o.form
	stop, _ = strconv.ParseFloat(f.field(orderFieldStoploss).value(), 64)
	target, _ = strconv.ParseFloat(f.field(orderFieldTarget).value(), 64)
	entry := o.entryPrice(params)
	if entry <= 0 {
		return stop, target, nil
	}
	side, at := strings.ToLower(params.TransactionType), formatAmount(entry)
	buy := params.TransactionType == kite.TransactionTypeBuy
	switch {
	case stop > 0 && buy && stop >= entry:
		err = fmt.Errorf("a %s's stoploss must be below its entry, %s", side, at)
	case stop > 0 && !buy && stop <= entry:
		err = fmt.Errorf("a %s's stoploss must be above its entry, %s", side, at)
	case target > 0 && buy && target <= entry:
		err = fmt.Errorf("a %s's target must be above its entry, %s", side, at)
	case target > 0 && !buy && target >= entry:
		err = fmt.Errorf("a %s's target must be below its entry, %s", side, at)
	}
	return stop, target, err
}

// exitHints show how far the stoploss and target are from params' entry,
// in red when on the wrong side of it.
func (o *orderForm) exitHints(params kite.OrderParams) {
	f := o.form
	entry := o.entryPrice(params)
	buy := params.TransactionType == kite.TransactionTypeBuy
	for _, label := range []string{orderFieldStoploss, orderFieldTarget} {
		field := f.field(label)
		level, _ := strconv.ParseFloat(field.value(), 64)
		field.hint = ""
		if level <= 0 || entry <= 0 {
			continue
		}
		move := level - entry
		text := fmt.Sprintf("%s (%+.2f%%)", formatSigned(move), move/entry*100)
		// A buy loses below its entry and gains above; a sell the reverse.
		if wrong := (label == orderFieldStoploss) == (buy == (move > 0)); wrong {
			field.hint = styleRed.Bold(true).Render(text)
		} else {
			field.hint = styleMuted.Render(text)
		}
	}
}

// exitTriggers are the alerts that guard params, placed as orderID in
// mode: a stoploss and a target, either left out when 0, each placing the
// opposite order at market for the same quantity, one-cancels-other, and
// waiting for orderID to fill.
func exitTriggers(instrument models.Instrument, params kite.OrderParams, mode broker.Mode, orderID string, stop, target, ltp float64) []models.PriceTrigger {
	buy := params.TransactionType == kite.TransactionTypeBuy
	side := kite.TransactionTypeBuy
	if buy {
		side = kite.TransactionTypeSell
	}
	exit := models.BasketItem{Exchange: params.Exchange, TradingSymbol: params.Tradingsymbol, TransactionType: side,
		OrderType: kite.OrderTypeMarket, Product: params.Product, Quantity: params.Quantity}
	var triggers []models.PriceTrigger
	for _, level := range []struct {
		exit  string
		value float64
	}{{models.TriggerStoploss, stop}, {models.TriggerTarget, target}} {
		if level.value <= 0 {
			continue
		}
		// A buy's stoploss fires at or below it and its target at or
		// above; a sell's the other way round.
		condition := models.TriggerGTE
		if buy == (level.exit == models.TriggerStoploss) {
			condition = models.TriggerLTE
		}
		triggers = append(triggers, models.PriceTrigger{
			InstrumentID: instrument.ID, Field: models.FieldLastPrice, Condition: condition, Value: level.value,
			Action: models.TriggerActionBoth, Reference: ltp, Note: level.exit + " of order " + orderID,
			FiringBudget: new(1), Orders: []models.BasketItem{exit}, OrderMode: string(mode),
			OCO: "order:" + orderID, Entry: orderID, Exit: level.exit,
		})
	}
	return triggers
}

// refresh enables the price fields the order type uses, names the Place
// button after the side and describes the order.
func (o *orderForm) refresh() {
	f := o.form
	orderType := f.field(orderFieldType).value()
	f.field(orderFieldPrice).disabled = !usesPrice(orderType)
	f.field(orderFieldTrigger).disabled = !usesTrigger(orderType)
	side := f.field(orderFieldSide).value()
	switch {
	case o.ticket.modify != nil:
		f.buttons[0] = "Modify " + o.ticket.modify.OrderID
	case o.ticket.item == nil && o.ticket.alert == nil:
		f.buttons[0] = "Place " + side
	}
	f.syncFocus()
	o.hint()

	userID, params, err := o.order()
	o.exitHints(params)
	if err != nil {
		o.summary, o.charges = styleRed.Render(err.Error()), ""
		return
	}
	o.charges = orderCharges(params, o.ticket.ltp)
	at := "at market"
	switch params.OrderType {
	case kite.OrderTypeLimit:
		at = "limit " + formatAmount(params.Price)
	case kite.OrderTypeSL:
		at = fmt.Sprintf("limit %s, trigger %s", formatAmount(params.Price), formatAmount(params.TriggerPrice))
	case kite.OrderTypeSLM:
		at = "market, trigger " + formatAmount(params.TriggerPrice)
	}
	lots := ""
	if lot := o.ticket.instrument.LotSize; lot > 1 {
		lots = fmt.Sprintf(" (%d %s)", params.Quantity/lot, pluralize("lot", params.Quantity/lot))
	}
	value := ""
	if params.Price > 0 {
		value = " · ≈ ₹" + formatAmount(params.Price*float64(params.Quantity))
	} else if o.ticket.ltp > 0 {
		value = " · ≈ ₹" + formatAmount(o.ticket.ltp*float64(params.Quantity))
	}
	style := styleGreen
	if params.TransactionType == kite.TransactionTypeSell {
		style = styleRed
	}
	o.summary = style.Bold(true).Render(fmt.Sprintf("%s %d", params.TransactionType, params.Quantity)) +
		styleText.Render(fmt.Sprintf("%s %s · %s · ", lots, at, params.Product)) + styleYellow.Render(userID) + styleMuted.Render(value)
	if stop, target, _ := o.exitLevels(params); stop > 0 || target > 0 {
		var exits []string
		if stop > 0 {
			exits = append(exits, "stoploss "+formatAmount(stop))
		}
		if target > 0 {
			exits = append(exits, "target "+formatAmount(target))
		}
		o.summary += "\n" + styleMuted.Render("Exits at market: "+strings.Join(exits, ", ")+" · armed once it fills, one cancels the other")
	}
}

// hint flags the price and trigger against the last price.
func (o *orderForm) hint() {
	f := o.form
	price, _ := strconv.ParseFloat(f.field(orderFieldPrice).value(), 64)
	trigger, _ := strconv.ParseFloat(f.field(orderFieldTrigger).value(), 64)
	priceHint, triggerHint := priceHints(f.field(orderFieldSide).value(), f.field(orderFieldType).value(), price, trigger, o.ticket.ltp)
	f.field(orderFieldPrice).hint, f.field(orderFieldTrigger).hint = hintText(priceHint), hintText(triggerHint)
}

// hintText styles a price hint: a warning that the order fills at once, or
// a value Kite rejects.
func hintText(hint string) string {
	switch hint {
	case "":
		return ""
	case hintImmediate:
		return styleYellow.Bold(true).Render(hint)
	}
	return styleRed.Bold(true).Render(hint)
}

func (o *orderForm) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if o.placing {
		return nil
	}
	switch msg.String() {
	case "esc":
		return o.close(a)
	case "ctrl+o":
		// The form's own action, from any field.
		return o.press(a, 0)
	case "ctrl+b":
		if len(o.form.buttons) == 3 {
			return o.press(a, 1)
		}
		return nil
	}
	if cmd, handled := a.tradeKey(o, msg.String()); handled {
		return cmd
	}
	if o.step(msg.String()) {
		o.refresh()
		return a.orderMargin(o)
	}
	pressed, changed := o.form.update(msg)
	if changed && o.changedContract() {
		return tea.Batch(a.orderMargin(o), a.fetchTradeQuotes(o))
	}
	if changed {
		o.refresh()
	}
	switch {
	case pressed < 0 && changed:
		return a.orderMargin(o)
	case pressed < 0:
		return nil
	}
	return o.press(a, pressed)
}

// press does what the form's button pressed does.
func (o *orderForm) press(a *app, pressed int) tea.Cmd {
	switch {
	case pressed == 0 && o.ticket.modify != nil:
		return a.modifyOrder(o)
	case pressed == 0 && o.ticket.item != nil:
		return a.saveBasketItem(o)
	case pressed == 0 && o.ticket.alert != nil:
		return a.addAlertOrder(o)
	case pressed == 0:
		return a.placeOrder(o)
	case pressed == 1 && len(o.form.buttons) == 3:
		return a.pickBasket(o)
	}
	return o.close(a)
}

// close leaves the form.
func (o *orderForm) close(a *app) tea.Cmd {
	a.closeOverlay()
	// An alert's order goes back to the alert.
	if alert := o.ticket.alert; alert != nil {
		a.overlay = alert
	}
	return nil
}

// step handles the form's steppers: + and - change the quantity by a lot
// from any field, and Left and Right change it on the quantity and move
// the price or trigger by a tick.
func (o *orderForm) step(key string) bool {
	f := o.form
	switch key {
	case "+", "=", "-", "_":
		o.stepLots(key == "+" || key == "=")
		return true
	case "left", "right":
		current := f.current()
		if current == nil || current.disabled {
			return false
		}
		if current.label == orderFieldQuantity {
			o.stepLots(key == "right")
			return true
		}
		if current.label != orderFieldPrice && current.label != orderFieldTrigger && current.label != orderFieldStoploss && current.label != orderFieldTarget {
			return false
		}
		steps := 1
		if key == "left" {
			steps = -1
		}
		tick := o.ticket.instrument.TickSize
		if tick <= 0 {
			tick = 0.05
		}
		price, _ := strconv.ParseFloat(current.value(), 64)
		current.input.SetValue(formatTicked(stepPrice(price, o.ticket.ltp, tick, steps), tick))
		current.input.CursorEnd()
		return true
	}
	return false
}

// stepLots moves the quantity up or down to the next whole lot, never
// below one.
func (o *orderForm) stepLots(up bool) {
	lot := max(o.ticket.instrument.LotSize, 1)
	quantity := o.form.field(orderFieldQuantity)
	current, _ := strconv.Atoi(quantity.value())
	next := (current/lot + 1) * lot
	if !up {
		next = max((current+lot-1)/lot*lot-lot, lot)
	}
	quantity.input.SetValue(strconv.Itoa(next))
	quantity.input.CursorEnd()
}

// placeOrder places the form's order. The form stays open with the error if
// Kite rejects it, and closes once Kite accepts it.
func (a *app) placeOrder(o *orderForm) tea.Cmd {
	userID, params, err := o.order()
	if err != nil {
		o.summary = styleRed.Render(err.Error())
		return nil
	}
	if a.deps.place == nil {
		o.summary = styleRed.Render("Ordering is unavailable")
		return nil
	}
	o.placing = true
	o.summary = styleYellow.Render(fmt.Sprintf("Placing %s %d %s for %s…", params.TransactionType, params.Quantity, params.Tradingsymbol, userID))
	place, mode, store := a.deps.place, o.mode, a.deps.triggers
	instrument, ltp := o.ticket.instrument, o.ticket.ltp
	stop, target, _ := o.exitLevels(params)
	return a.task(func(ctx context.Context) tea.Msg {
		orderID, err := place(ctx, userID, mode, params)
		msg := orderDoneMsg{form: o, userID: userID, mode: mode, orderID: orderID, params: params, err: err}
		if err != nil || store == nil {
			return msg
		}
		// The exits are set once Kite has the order, to arm when it fills.
		for _, trigger := range exitTriggers(instrument, params, mode, orderID, stop, target, ltp) {
			added, err := store.AddPriceTrigger(ctx, userID, trigger)
			if err != nil {
				msg.exitErr = errors.Join(msg.exitErr, err)
				continue
			}
			msg.exits = append(msg.exits, userTrigger{user: userID, trigger: added})
		}
		return msg
	})
}

// modifyOrder sends the form's quantity, type and prices for the open
// order. Like placing, the form stays open with Kite's reason if it fails.
func (a *app) modifyOrder(o *orderForm) tea.Cmd {
	userID, params, err := o.order()
	if err != nil {
		o.summary = styleRed.Render(err.Error())
		return nil
	}
	if a.deps.modify == nil {
		o.summary = styleRed.Render("Modifying orders is unavailable")
		return nil
	}
	o.placing = true
	order := *o.ticket.modify
	o.summary = styleYellow.Render(fmt.Sprintf("Modifying order %s…", order.OrderID))
	modify, mode, params := a.deps.modify, o.mode, modifyParams(params)
	return a.task(func(ctx context.Context) tea.Msg {
		err := modify(ctx, userID, mode, order, params)
		return orderDoneMsg{form: o, userID: userID, mode: mode, orderID: order.OrderID, params: params, modified: true, err: err}
	})
}

func (o *orderForm) view(_ *app, w, h int) string {
	width := min(66, w-2)
	instrument := o.ticket.instrument
	side := o.form.field(orderFieldSide).value()
	name := fallback(strings.TrimSpace(instrument.DisplayName), instrument.TradingSymbol)
	ltp := "—"
	if o.ticket.ltp > 0 {
		ltp = formatAmount(o.ticket.ltp)
	}
	header := sidePill(side) + " " + styleBold.Render(name) + "\n" +
		styleFaint.Render(fmt.Sprintf("%s · lot %d · LTP %s", instrument.ID, max(instrument.LotSize, 1), ltp))
	// The form's actions have their own keys, from any field.
	actions := "^O place"
	switch {
	case o.ticket.modify != nil:
		actions = "^O modify"
	case o.ticket.item != nil:
		actions = "^O save to basket"
	case o.ticket.alert != nil:
		actions = "^O add to alert"
	}
	if len(o.form.buttons) == 3 {
		actions += " · ^B add to basket"
	}
	if o.target != nil {
		if _, err := closingTicket(*o.target); err == nil {
			actions += " · X close position"
		}
	}
	help := []string{styleFaint.Render(actions), styleFaint.Render("↑↓ field · ←→ change · +/- lot · B/S side · Esc cancel")}
	if legs := o.legs; legs != nil {
		switch info := legs.contractInfo(instrument, o.ticket.ltp); {
		case legs.missing != "":
			header = styleBold.Render(symbolOf(legs.underlying)) + "\n" + styleMuted.Render(legs.missing)
		case info != "":
			header += "\n" + info
		}
		if legs.priceErr != nil {
			header += "\n" + styleRed.Render("Prices failed: "+quoteErrorText(legs.priceErr))
		}
		header = legs.tabRow() + "\n\n" + header
		keys := "⇧←→ or O C P F tab"
		if _, contract := contractTab(o.ticket.instrument); legs.tab != tradeOrder || contract {
			keys += " · [ ] expiry"
		}
		help = append([]string{styleFaint.Render(keys)}, help...)
	}
	title := "Place order"
	switch {
	case o.ticket.modify != nil:
		title = "Modify order " + o.ticket.modify.OrderID
	case o.ticket.item != nil:
		title = "Basket order"
	case o.ticket.alert != nil:
		title = "Alert order"
	case o.ticket.closing:
		title = "Close position"
	}
	// The margin and charges share a row.
	result := []string{o.summary}
	if cost := strings.Join(slices.DeleteFunc([]string{o.margin, o.charges}, func(part string) bool { return part == "" }), styleFaint.Render("  ·  ")); cost != "" {
		result = append(result, cost)
	}
	// A short terminal loses the keys' help, then the gaps between parts;
	// the frame takes four lines.
	sections := []string{header, o.form.view(width - 4), strings.Join(append(result, help...), "\n")}
	gap := "\n\n"
	if lipgloss.Height(strings.Join(sections, gap))+4 > h {
		sections[2] = strings.Join(result, "\n")
	}
	if lipgloss.Height(strings.Join(sections, gap))+4 > h {
		gap = "\n"
	}
	return modal(title, modePill(o.mode), strings.Join(sections, gap), width)
}

func (a *app) handleOrders(msg tea.Msg) (tea.Cmd, bool) {
	if cmd, handled := a.handleTrade(msg); handled {
		return cmd, true
	}
	switch msg := msg.(type) {
	case orderDoneMsg:
		msg.form.placing = false
		if msg.err != nil {
			msg.form.summary = styleRed.Bold(true).Render("Order rejected: ") + styleRed.Render(quoteErrorText(msg.err))
			return nil, true
		}
		if a.overlay == msg.form {
			a.closeOverlay()
		}
		if msg.modified {
			return a.orderChanged(msg.userID, statusOK(fmt.Sprintf("Order %s modified · %s %d at %s", msg.orderID,
				msg.params.OrderType, msg.params.Quantity, priceOrMarket(msg.params.Price)))), true
		}
		more := ""
		for _, exit := range msg.exits {
			a.alerts.keep(exit)
		}
		if len(msg.exits) > 0 {
			more += statusInfo(" · then " + exitsText(msg.exits))
		}
		if msg.exitErr != nil {
			more += styleRed.Render(" · exits not set: " + msg.exitErr.Error())
		}
		placed := a.orderPlaced(msg.userID, msg.mode, msg.orderID, msg.params, more)
		if len(msg.exits) == 0 {
			return placed, true
		}
		return tea.Batch(placed, a.alertsChanged(msg.userID), a.checkAlerts(true)), true
	case orderMarginDueMsg:
		o := msg.form
		if a.overlay != o || msg.seq != o.marginSeq || a.deps.basketMargins == nil {
			return nil, true
		}
		margins, leg := a.deps.basketMargins, o.marginFor
		return a.task(func(ctx context.Context) tea.Msg {
			result, err := margins(ctx, []kite.OrderMarginParam{leg})
			return orderMarginMsg{form: o, leg: leg, margins: result, err: err}
		}), true
	case orderMarginMsg:
		o := msg.form
		if msg.leg != o.marginFor {
			return nil, true
		}
		if msg.err != nil {
			o.margin = styleFaint.Render("Margin unavailable: " + quoteErrorText(msg.err))
			return nil, true
		}
		o.margin = styleMuted.Render("Margin ") + styleYellow.Render("≈ ₹"+formatAmount(msg.margins.Final.Total))
		return nil, true
	case groupExitMsg:
		if len(msg.failures) > 0 {
			a.showError(fmt.Errorf("exit %s: %d of %d orders failed\n%s", msg.name, len(msg.failures), len(msg.failures)+len(msg.placed), strings.Join(msg.failures, "\n")))
			return a.orderChanged(msg.userID, statusWarn(fmt.Sprintf("Exit %s: %d placed, %d failed", msg.name, len(msg.placed), len(msg.failures)))), true
		}
		check := " · check their status in Kite"
		if msg.mode == broker.Paper {
			check = " in the paper account"
		}
		return a.orderChanged(msg.userID, statusOK(fmt.Sprintf("Exit %s · %d orders placed%s", msg.name, len(msg.placed), check))), true
	case orderCancelledMsg:
		if msg.err != nil {
			a.showError(fmt.Errorf("cancel order %s: %s", msg.order.OrderID, quoteErrorText(msg.err)))
			return nil, true
		}
		return a.orderChanged(msg.userID, statusOK("Order "+msg.order.OrderID+" cancelled")), true
	}
	return nil, false
}

type orderCancelledMsg struct {
	userID string
	order  kite.Order
	err    error
}

// confirmCancel asks before cancelling an open order of userID's.
func (a *app) confirmCancel(userID string, order kite.Order) tea.Cmd {
	if a.deps.cancel == nil {
		a.notify(statusWarn("Cancelling orders is unavailable"))
		return nil
	}
	pending := order.PendingQuantity
	if pending == 0 {
		pending = order.Quantity - order.FilledQuantity
	}
	what := fmt.Sprintf("%s %g %s · %s", order.TransactionType, pending, order.TradingSymbol, order.Status)
	cancel, mode := a.deps.cancel, a.portfolioMode()
	m := &menu{title: "Cancel order " + order.OrderID, info: modePill(mode) + " " + styleMuted.Render(userID), width: max(48, len(what)+8), keyFold: "YN",
		footer: "key or ↑↓ Enter · Esc keep"}
	m.items = []menuItem{
		{key: "Y", label: styleRed.Bold(true).Render("Y") + "  " + styleText.Render("Cancel ") + styleFaint.Render(what), run: func(a *app) tea.Cmd {
			a.notify(statusInfo("Cancelling order " + order.OrderID + "…"))
			return a.task(func(ctx context.Context) tea.Msg {
				return orderCancelledMsg{userID: userID, order: order, err: cancel(ctx, userID, mode, order)}
			})
		}},
		{key: "N", label: styleGreen.Bold(true).Render("N") + "  " + styleText.Render("Keep it"), run: func(*app) tea.Cmd { return nil }},
	}
	// Keep is the default, so a stray Enter cancels nothing.
	m.cursor = 1
	a.overlay = m
	return nil
}

// groupExitMsg reports exiting a group's positions: the orders placed and
// why the others failed.
type groupExitMsg struct {
	userID   string
	mode     broker.Mode
	name     string
	placed   []string
	failures []string
}

// confirmExitGroup offers to close every open position under underlying
// at market, in one go.
func (a *app) confirmExitGroup(underlying models.InstrumentID) tea.Cmd {
	p := a.portfolio
	if a.deps.place == nil {
		a.notify(statusWarn("Ordering is unavailable"))
		return nil
	}
	name := displayName(p.data.instruments, underlying, string(underlying))
	var exits []kite.OrderParams
	var legs []string
	var charged []kite.OrderChargesParam
	for _, group := range groupPositions(p.data.positions, p.data.instruments) {
		if group.underlying != underlying {
			continue
		}
		for _, position := range group.positions {
			ticket, err := closingTicket(p.positionTarget(position))
			if err != nil {
				continue
			}
			params, err := orderParams(ticket.instrument, orderInput{side: ticket.side, orderType: kite.OrderTypeMarket, product: ticket.product, quantity: strconv.Itoa(ticket.quantity)})
			if err != nil {
				a.showError(fmt.Errorf("exit %s: %w", position.Tradingsymbol, err))
				return nil
			}
			exits = append(exits, params)
			if position.LastPrice > 0 {
				charged = append(charged, chargesParam(params, position.LastPrice))
			}
		}
	}
	if len(exits) == 0 {
		a.notify(statusWarn("Nothing open under " + name))
		return nil
	}
	// Buy back shorts before selling longs, so a hedge isn't lifted while
	// the short it covers is still open.
	sort.SliceStable(exits, func(i, j int) bool {
		return exits[i].TransactionType == kite.TransactionTypeBuy && exits[j].TransactionType != kite.TransactionTypeBuy
	})
	width := 48
	for _, params := range exits {
		style := styleGreen
		if params.TransactionType == kite.TransactionTypeSell {
			style = styleRed
		}
		leg := fmt.Sprintf("%s %d", params.TransactionType, params.Quantity)
		legs = append(legs, style.Bold(true).Render(leg)+" "+styleText.Render(params.Tradingsymbol)+styleFaint.Render(" · "+params.Product))
		width = max(width, len(leg)+len(params.Tradingsymbol)+len(params.Product)+12)
	}
	userID, mode, place := p.user.UserID, a.portfolioMode(), a.deps.place
	m := &menu{title: "Exit " + name, info: modePill(mode) + " " + styleMuted.Render(userID), width: width, keyFold: "YN",
		body:   strings.Join(legs, "\n") + "\n" + styleFaint.Render("All at market, buys first.") + exitCharges(charged, len(exits)),
		footer: "key or ↑↓ Enter · Esc keep"}
	m.items = []menuItem{
		{key: "Y", label: styleRed.Bold(true).Render("Y") + "  " + styleText.Render(fmt.Sprintf("Exit all %d", len(exits))), run: func(a *app) tea.Cmd {
			a.notify(statusInfo(fmt.Sprintf("Exiting %d positions in %s…", len(exits), name)))
			return a.task(func(ctx context.Context) tea.Msg {
				msg := groupExitMsg{userID: userID, mode: mode, name: name}
				for _, params := range exits {
					orderID, err := place(ctx, userID, mode, params)
					if err != nil {
						msg.failures = append(msg.failures, params.Tradingsymbol+": "+quoteErrorText(err))
						continue
					}
					msg.placed = append(msg.placed, orderID)
				}
				return msg
			})
		}},
		{key: "N", label: styleGreen.Bold(true).Render("N") + "  " + styleText.Render("Keep them"), run: func(*app) tea.Cmd { return nil }},
	}
	// Keep is the default, so a stray Enter exits nothing.
	m.cursor = 1
	a.overlay = m
	return nil
}

// exitCharges estimates what exiting costs at the last prices, noting legs
// without one.
func exitCharges(params []kite.OrderChargesParam, legs int) string {
	charges, err := kite.EstimateOrderCharges(params)
	if err != nil || len(charges) == 0 {
		return ""
	}
	text := "\n" + styleMuted.Render("Charges ") + styleYellow.Render("≈ ₹"+formatAmount(chargesTotal(charges)))
	if len(charges) < legs {
		text += styleFaint.Render(fmt.Sprintf(" for %d of %d legs", len(charges), legs))
	}
	return text
}
