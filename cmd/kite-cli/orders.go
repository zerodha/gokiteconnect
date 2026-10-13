package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// orderAction is one entry of the order menu, run by its key.
type orderAction struct {
	key   string
	label string
	style func(string) string
	// detail describes what the action orders, after the label.
	detail string
	run    func(*app) tea.Cmd
}

var (
	buyStyle  = func(text string) string { return styleGreen.Bold(true).Render(text) }
	sellStyle = func(text string) string { return styleRed.Bold(true).Render(text) }
)

// orderActions lists what can be ordered for target: a raw buy or sell of
// the instrument itself (not of an index), closing the position or holding
// when one is held, and buying or selling its underlying's options.
func orderActions(target tradeTarget) []orderAction {
	instrument := target.instrument
	var actions []orderAction
	if instrument.Segment != "INDICES" {
		lot := "1 share"
		if instrument.LotSize > 1 {
			lot = fmt.Sprintf("1 lot of %d", instrument.LotSize)
		}
		raw := func(side string) func(*app) tea.Cmd {
			return func(a *app) tea.Cmd {
				ticket := newOrderTicket(instrument, side, target.ltp)
				if position, ok := target.held.(kite.Position); ok {
					ticket.product = position.Product
				}
				return a.openOrder(ticket)
			}
		}
		actions = append(actions,
			orderAction{key: "B", label: "Raw buy", style: buyStyle, detail: lot, run: raw(kite.TransactionTypeBuy)},
			orderAction{key: "S", label: "Raw sell", style: sellStyle, detail: lot, run: raw(kite.TransactionTypeSell)},
		)
	}
	if ticket, err := closingTicket(target); err == nil {
		actions = append(actions, orderAction{key: "X", label: "Close position", style: func(text string) string { return styleYellow.Bold(true).Render(text) },
			detail: fmt.Sprintf("%s %d at market", ticket.side, ticket.quantity),
			run:    func(a *app) tea.Cmd { return a.openOrder(ticket) }})
	}
	if underlying, ok := optionUnderlying(instrument); ok {
		option := func(side string, optionType models.OptionType) func(*app) tea.Cmd {
			return func(a *app) tea.Cmd { return a.openStrikePicker(underlying, side, optionType) }
		}
		name := symbolOf(underlying)
		actions = append(actions,
			orderAction{key: "c", label: "Buy call", style: buyStyle, detail: name, run: option(kite.TransactionTypeBuy, models.OptionTypeCall)},
			orderAction{key: "C", label: "Sell call", style: sellStyle, detail: name, run: option(kite.TransactionTypeSell, models.OptionTypeCall)},
			orderAction{key: "p", label: "Buy put", style: buyStyle, detail: name, run: option(kite.TransactionTypeBuy, models.OptionTypePut)},
			orderAction{key: "P", label: "Sell put", style: sellStyle, detail: name, run: option(kite.TransactionTypeSell, models.OptionTypePut)},
		)
	}
	return actions
}

// openOrderMenu lists the orders for target; each runs by its key or by
// Enter. B, S and X match either case; c, C, p and P pick calls and puts by
// case.
func (a *app) openOrderMenu(target tradeTarget) tea.Cmd {
	actions := orderActions(target)
	if len(actions) == 0 {
		a.notify(statusWarn("Nothing to order for " + string(target.instrument.ID)))
		return nil
	}
	name := fallback(strings.TrimSpace(target.instrument.DisplayName), target.instrument.TradingSymbol)
	info := ""
	if target.ltp > 0 {
		info = styleMuted.Render(formatAmount(target.ltp))
	}
	m := &menu{title: "Order · " + name, info: info, width: max(48, len([]rune(name))+24), keyFold: "BSX",
		footer: "key or ↑↓ Enter · Esc close"}
	for _, action := range actions {
		label := action.style(action.key) + "  " + styleText.Render(padRight(action.label, 15)) + " " + styleFaint.Render(action.detail)
		m.items = append(m.items, menuItem{key: action.key, label: label, run: action.run})
	}
	a.overlay = m
	return nil
}

// Order form labels.
const (
	orderFieldAccount  = "Account"
	orderFieldSide     = "Side"
	orderFieldQuantity = "Quantity"
	orderFieldType     = "Order type"
	orderFieldPrice    = "Price"
	orderFieldTrigger  = "Trigger price"
	orderFieldProduct  = "Product"
)

// orderForm is the buy and sell form.
type orderForm struct {
	form    *form
	ticket  orderTicket
	users   []string
	placing bool
	// summary shows the order as it will be placed, or why it cannot be.
	summary string
}

type orderDoneMsg struct {
	form    *orderForm
	userID  string
	orderID string
	params  kite.OrderParams
	// modified reports a change to an open order rather than a new one.
	modified bool
	err      error
}

// openOrder opens the order form for ticket, placing for the active user by
// default.
func (a *app) openOrder(ticket orderTicket) tea.Cmd {
	accounts := a.users.users
	if len(accounts) == 0 {
		a.showError(errors.New("add a user before placing orders"))
		return nil
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
	o := &orderForm{ticket: ticket}
	account, active := 0, a.activeUser()
	if ticket.modify != nil {
		// An order is modified with the session that placed it.
		active = ticket.userID
		accounts = slices.DeleteFunc(slices.Clone(accounts), func(user credentials.Credentials) bool { return user.UserID != active })
		if len(accounts) == 0 {
			accounts = []credentials.Credentials{{UserID: active}}
		}
	}
	labels := make([]string, len(accounts))
	for index, user := range accounts {
		o.users = append(o.users, user.UserID)
		labels[index] = user.UserID
		if user.UserName != "" {
			labels[index] += " · " + user.UserName
		}
		if user.UserID == active {
			account = index
		}
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
	button := "Place"
	if ticket.modify != nil {
		button = "Modify"
	}
	f := newForm(14, 14, button, "Cancel")
	f.addSelect(orderFieldAccount, labels, account)
	f.addSelect(orderFieldSide, orderSides, max(slices.Index(orderSides, ticket.side), 0))
	f.addInput(orderFieldQuantity, strconv.Itoa(ticket.quantity), wholeNumber, false)
	f.addSelect(orderFieldType, orderTypes, max(slices.Index(orderTypes, ticket.orderType), 0))
	f.addInput(orderFieldPrice, price, decimalNumber, false)
	f.addInput(orderFieldTrigger, trigger, decimalNumber, false)
	f.addSelect(orderFieldProduct, products, max(slices.Index(products, ticket.product), 0))
	if ticket.modify != nil {
		// Kite changes only an order's quantity, type and prices.
		f.field(orderFieldAccount).disabled = true
		f.field(orderFieldSide).disabled = true
		f.field(orderFieldProduct).disabled = true
	}
	o.form = f
	o.refresh()
	// Start on the quantity, the field most orders change.
	f.focusField(orderFieldQuantity)
	a.overlay = o
	return nil
}

// order reads the form into the account and the order to place.
func (o *orderForm) order() (string, kite.OrderParams, error) {
	f := o.form
	index := f.field(orderFieldAccount).index
	if index < 0 || index >= len(o.users) {
		return "", kite.OrderParams{}, errors.New("choose an account")
	}
	params, err := orderParams(o.ticket.instrument, orderInput{
		side:      f.field(orderFieldSide).value(),
		orderType: f.field(orderFieldType).value(),
		product:   f.field(orderFieldProduct).value(),
		quantity:  f.field(orderFieldQuantity).value(),
		price:     f.field(orderFieldPrice).value(),
		trigger:   f.field(orderFieldTrigger).value(),
	})
	return o.users[index], params, err
}

// refresh enables the price fields the order type uses, names the Place
// button after the side and describes the order.
func (o *orderForm) refresh() {
	f := o.form
	orderType := f.field(orderFieldType).value()
	f.field(orderFieldPrice).disabled = !usesPrice(orderType)
	f.field(orderFieldTrigger).disabled = !usesTrigger(orderType)
	side := f.field(orderFieldSide).value()
	if o.ticket.modify != nil {
		f.buttons[0] = "Modify " + o.ticket.modify.OrderID
	} else {
		f.buttons[0] = "Place " + side
	}
	f.syncFocus()

	userID, params, err := o.order()
	if err != nil {
		o.summary = styleRed.Render(err.Error())
		return
	}
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
}

func (o *orderForm) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if o.placing {
		return nil
	}
	if msg.String() == "esc" {
		a.closeOverlay()
		return nil
	}
	if o.step(msg.String()) {
		o.refresh()
		return nil
	}
	pressed, changed := o.form.update(msg)
	if changed {
		o.refresh()
	}
	switch pressed {
	case 0:
		if o.ticket.modify != nil {
			return a.modifyOrder(o)
		}
		return a.placeOrder(o)
	case 1:
		a.closeOverlay()
	}
	return nil
}

// step handles the form's steppers: + and - change the quantity by a lot
// from any field, and Left and Right move the price or trigger by a tick.
func (o *orderForm) step(key string) bool {
	f := o.form
	switch key {
	case "+", "=", "-", "_":
		lot := max(o.ticket.instrument.LotSize, 1)
		quantity := f.field(orderFieldQuantity)
		current, _ := strconv.Atoi(quantity.value())
		next := (current/lot + 1) * lot
		if key == "-" || key == "_" {
			next = max((current+lot-1)/lot*lot-lot, lot)
		}
		quantity.input.SetValue(strconv.Itoa(next))
		quantity.input.CursorEnd()
		return true
	case "left", "right":
		current := f.current()
		if current == nil || current.disabled || (current.label != orderFieldPrice && current.label != orderFieldTrigger) {
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
	place := a.deps.place
	return a.task(func(ctx context.Context) tea.Msg {
		orderID, err := place(ctx, userID, params)
		return orderDoneMsg{form: o, userID: userID, orderID: orderID, params: params, err: err}
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
	modify, params := a.deps.modify, modifyParams(params)
	return a.task(func(ctx context.Context) tea.Msg {
		err := modify(ctx, userID, order, params)
		return orderDoneMsg{form: o, userID: userID, orderID: order.OrderID, params: params, modified: true, err: err}
	})
}

func (o *orderForm) view(_ *app, w, _ int) string {
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
	title := "Place order"
	switch {
	case o.ticket.modify != nil:
		title = "Modify order " + o.ticket.modify.OrderID
	case o.ticket.closing:
		title = "Close position"
	}
	body := header + "\n\n" + o.form.view(width-4) + "\n\n" + o.summary + "\n" +
		styleFaint.Render("↑↓ field · ←→ option/tick · +/- lot · Enter next · Esc cancel")
	return modal(title, "", body, width)
}

// strikePicker chooses an option to buy or sell: one expiry of an
// underlying's calls or puts at a time, priced live, with the at-the-money
// strike selected. Enter orders the selected strike.
type strikePicker struct {
	side       string
	optionType models.OptionType
	underlying models.Instrument
	// options are every expiry's contracts, by strike; expiries are their
	// dates in order and expiry the one shown.
	options  []models.Instrument
	expiries []time.Time
	expiry   int
	quotes   map[string]kite.QuoteData
	source   quoteSource
	err      error
	priceErr error
	loading  bool
	fetching bool
	// atm selects the at-the-money strike once the spot price is known.
	atm  bool
	grid grid
}

type (
	strikesLoadedMsg struct {
		picker     *strikePicker
		underlying models.Instrument
		options    []models.Instrument
		source     quoteSource
		err        error
	}
	strikeQuotesMsg struct {
		picker *strikePicker
		quotes kite.Quote
		err    error
	}
	strikeTickMsg struct{ picker *strikePicker }
)

// openStrikePicker opens the picker for side (buy or sell) of underlying's
// calls or puts, pricing them from the explorer's quotes or by connecting
// the data user.
func (a *app) openStrikePicker(underlying models.InstrumentID, side string, optionType models.OptionType) tea.Cmd {
	p := &strikePicker{
		side: side, optionType: optionType, loading: true, atm: true,
		underlying: models.Instrument{ID: underlying, TradingSymbol: symbolOf(underlying)},
		source:     a.explorer.quoteSource,
		grid: grid{columns: []column{
			{title: "STRIKE", width: 10, align: alignRight},
			{title: "LTP", width: 9, align: alignRight},
			{title: "CHG", width: 8, align: alignRight},
			{title: "MONEYNESS", width: 14},
			{title: "BREAKEVEN", width: 18, align: alignRight},
			{title: "PER LOT", width: 10, align: alignRight},
			{title: "OI", width: 10, flex: 1, align: alignRight},
		}},
	}
	p.render()
	a.overlay = p
	catalog, open, load := a.explorer.catalog, a.deps.openCatalog, a.deps.loadQuotes
	source := p.source
	return a.task(func(ctx context.Context) tea.Msg {
		msg := strikesLoadedMsg{picker: p, source: source}
		if catalog == nil {
			if catalog, _, msg.err = open(ctx); msg.err != nil {
				return msg
			}
		}
		instrument, err := catalog.Get(ctx, underlying)
		if errors.Is(err, models.ErrNotFound) {
			instrument, err = models.Instrument{ID: underlying, TradingSymbol: symbolOf(underlying)}, nil
		}
		if err != nil {
			msg.err = err
			return msg
		}
		msg.underlying = instrument
		msg.options, msg.err = catalog.GetOptions(ctx, models.OptionsFilter{UnderlyingID: underlying, Types: []models.OptionType{optionType}})
		if msg.err == nil && msg.source == nil && load != nil {
			// Unpriced strikes are still worth picking from.
			msg.source, _, _ = load(ctx)
		}
		return msg
	})
}

func (a *app) handleOrders(msg tea.Msg) (tea.Cmd, bool) {
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
		return a.orderPlaced(msg.userID, msg.orderID, msg.params), true
	case orderCancelledMsg:
		if msg.err != nil {
			a.showError(fmt.Errorf("cancel order %s: %s", msg.order.OrderID, quoteErrorText(msg.err)))
			return nil, true
		}
		return a.orderChanged(msg.userID, statusOK("Order "+msg.order.OrderID+" cancelled")), true
	case strikesLoadedMsg:
		p := msg.picker
		if a.overlay != p {
			return nil, true
		}
		p.loading, p.err, p.source = false, msg.err, msg.source
		if msg.err == nil {
			p.underlying = msg.underlying
			p.setOptions(msg.options)
		}
		p.render()
		return tea.Batch(a.fetchStrikes(p), a.strikeTick(p)), true
	case strikeTickMsg:
		if a.overlay != msg.picker {
			return nil, true
		}
		return tea.Batch(a.fetchStrikes(msg.picker), a.strikeTick(msg.picker)), true
	case strikeQuotesMsg:
		p := msg.picker
		p.fetching = false
		p.priceErr = msg.err
		if msg.err == nil {
			if p.quotes == nil {
				p.quotes = make(map[string]kite.QuoteData, len(msg.quotes))
			}
			for id, quote := range msg.quotes {
				p.quotes[id] = quote
			}
		}
		p.render()
		return nil, true
	}
	return nil, false
}

func (a *app) strikeTick(p *strikePicker) tea.Cmd {
	if a.deps.strikeEvery <= 0 {
		return nil
	}
	return tea.Tick(a.deps.strikeEvery, func(time.Time) tea.Msg { return strikeTickMsg{picker: p} })
}

// fetchStrikes prices the underlying and the shown expiry's strikes.
func (a *app) fetchStrikes(p *strikePicker) tea.Cmd {
	if p.source == nil || p.fetching || p.loading || p.err != nil {
		return nil
	}
	ids := []string{string(p.underlying.ID)}
	for _, option := range p.strikes() {
		ids = append(ids, string(option.ID))
	}
	p.fetching = true
	source := p.source
	return a.task(func(context.Context) tea.Msg {
		quotes, err := source.GetQuote(ids...)
		return strikeQuotesMsg{picker: p, quotes: quotes, err: err}
	})
}

// setOptions keeps the unexpired contracts of the picker's type and lists
// their expiries, showing the nearest.
func (p *strikePicker) setOptions(options []models.Instrument) {
	today := expiryDay(time.Now())
	seen := map[time.Time]bool{}
	p.options = p.options[:0]
	for _, option := range options {
		if option.InstrumentType != string(p.optionType) || option.Expiry == nil || expiryDay(*option.Expiry).Before(today) {
			continue
		}
		p.options = append(p.options, option)
		if day := expiryDay(*option.Expiry); !seen[day] {
			seen[day] = true
			p.expiries = append(p.expiries, day)
		}
	}
	sort.Slice(p.expiries, func(i, j int) bool { return p.expiries[i].Before(p.expiries[j]) })
	sort.SliceStable(p.options, func(i, j int) bool { return p.options[i].Strike < p.options[j].Strike })
}

// strikes are the shown expiry's contracts, by strike.
func (p *strikePicker) strikes() []models.Instrument {
	if p.expiry >= len(p.expiries) {
		return nil
	}
	day := p.expiries[p.expiry]
	var strikes []models.Instrument
	for _, option := range p.options {
		if expiryDay(*option.Expiry).Equal(day) {
			strikes = append(strikes, option)
		}
	}
	return strikes
}

func (p *strikePicker) spot() float64 {
	return p.quotes[string(p.underlying.ID)].LastPrice
}

func (p *strikePicker) selected() (models.Instrument, bool) {
	option, ok := p.grid.selectedRef().(models.Instrument)
	return option, ok
}

func nearestOption(strikes []models.Instrument, price float64) models.Instrument {
	best := strikes[0]
	for _, option := range strikes[1:] {
		if math.Abs(option.Strike-price) < math.Abs(best.Strike-price) {
			best = option
		}
	}
	return best
}

// render fills the table, keeping the selected strike, or selecting the
// at-the-money strike once the spot price is known.
func (p *strikePicker) render() {
	g := &p.grid
	switch {
	case p.loading:
		g.empty = styleMuted.Render("Loading strikes…")
	case p.err != nil:
		g.empty = styleRed.Render(p.err.Error())
	default:
		g.empty = styleMuted.Render(fmt.Sprintf("No %ss listed for %s", optionWord(p.optionType), p.underlying.ID))
	}
	previous, hadSelection := p.selected()
	strikes := p.strikes()
	if p.loading || p.err != nil {
		strikes = nil
	}
	spot := p.spot()
	var largestOI float64
	for _, option := range strikes {
		largestOI = max(largestOI, p.quotes[string(option.ID)].OI)
	}
	call := p.optionType == models.OptionTypeCall
	atm := math.NaN()
	if len(strikes) > 0 && spot > 0 {
		atm = nearestOption(strikes, spot).Strike
	}
	spotShown := spot <= 0
	var rows []row
	spotRow := func() row {
		return row{skip: true, bg: colorSpot, cells: []cell{
			textCell(formatAmount(spot), styleCyan.Bold(true)), textCell("◀ spot", styleCyan),
		}}
	}
	for _, option := range strikes {
		if !spotShown && option.Strike > spot {
			rows = append(rows, spotRow())
			spotShown = true
		}
		rows = append(rows, p.strikeRow(option, spot, atm, largestOI, call))
	}
	if !spotShown && len(strikes) > 0 {
		rows = append(rows, spotRow())
	}
	g.setRows(rows)
	at := func(strike float64) {
		g.selectWhere(func(r row) bool {
			option, ok := r.ref.(models.Instrument)
			return ok && option.Strike == strike
		})
		g.centerNext = true
	}
	switch {
	case p.atm && !math.IsNaN(atm):
		at(atm)
		p.atm = false
	case hadSelection:
		g.selectWhere(func(r row) bool {
			option, ok := r.ref.(models.Instrument)
			return ok && option.Strike == previous.Strike
		})
	case len(strikes) > 0:
		at(strikes[len(strikes)/2].Strike)
	}
}

func (p *strikePicker) strikeRow(option models.Instrument, spot, atm, largestOI float64, call bool) row {
	quote, priced := p.quotes[string(option.ID)]
	itm := spot > 0 && ((call && option.Strike < spot) || (!call && option.Strike > spot))
	r := row{ref: option}
	if itm {
		r.bg = colorITM
	}
	strikeStyle := styleBold
	if option.Strike == atm {
		strikeStyle = styleYellow.Bold(true)
	}
	ltp, change, breakevenText, lot := textCell("—", styleFaint), textCell("", styleFaint), textCell("—", styleFaint), textCell("—", styleFaint)
	if priced && quote.LastPrice > 0 {
		ltp = textCell(formatPrice(quote.LastPrice), styleText)
		text, direction := formatChange(quote)
		change = textCell(text, signStyle(direction))
		value := breakeven(option, quote.LastPrice)
		text = formatPrice(value)
		if spot > 0 {
			text += " " + formatBreakeven(value, spot, true)
		}
		breakevenText = textCell(text, styleText)
		lot = textCell(formatAmount(quote.LastPrice*float64(max(option.LotSize, 1))), styleText)
	}
	oi := textCell("", styleMuted)
	if priced {
		barColor := colorCallBar
		if !call {
			barColor = colorPutBar
		}
		oi = cell{text: formatCount(quote.OI), style: styleText, barColor: barColor, barRight: true}
		if largestOI > 0 {
			oi.bar = quote.OI / largestOI
		}
	}
	r.cells = []cell{
		textCell(formatStrike(option.Strike), strikeStyle), ltp, change,
		moneyness(option.Strike, spot, atm, itm), breakevenText, lot, oi,
	}
	return r
}

// moneyness names a strike ATM, ITM or OTM with its distance from spot.
func moneyness(strike, spot, atm float64, itm bool) cell {
	if spot <= 0 {
		return textCell("", styleFaint)
	}
	distance := fmt.Sprintf(" %.2f%%", math.Abs(strike-spot)/spot*100)
	switch {
	case strike == atm:
		return textCell("ATM"+distance, styleYellow.Bold(true))
	case itm:
		return textCell("ITM"+distance, styleYellow)
	}
	return textCell("OTM"+distance, styleMuted)
}

func (p *strikePicker) cycleExpiry(a *app, delta int) tea.Cmd {
	if len(p.expiries) == 0 {
		return nil
	}
	next := min(max(p.expiry+delta, 0), len(p.expiries)-1)
	if next == p.expiry {
		return nil
	}
	p.expiry, p.atm = next, true
	p.grid.cursor, p.grid.offset = 0, 0
	p.render()
	return a.fetchStrikes(p)
}

// update handles the picker's keys: Left, Right, [ and ] change expiry, R
// refreshes prices, Enter orders the strike and Esc or Q goes back.
func (p *strikePicker) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	switch key := msg.String(); key {
	case "esc", "q", "Q":
		a.closeOverlay()
		return nil
	case "left", "[":
		return p.cycleExpiry(a, -1)
	case "right", "]":
		return p.cycleExpiry(a, 1)
	case "r", "R":
		return a.fetchStrikes(p)
	case "enter":
		option, ok := p.selected()
		if !ok {
			return nil
		}
		a.closeOverlay()
		return a.openOrder(newOrderTicket(option, p.side, p.quotes[string(option.ID)].LastPrice))
	default:
		p.grid.update(key)
	}
	return nil
}

func (p *strikePicker) view(_ *app, w, h int) string {
	width := min(100, max(w-2, 60))
	height := min(32, max(h-2, 16))
	side := strings.ToUpper(sideWord(p.side)) + " " + strings.ToUpper(optionWord(p.optionType))
	header := sidePillText(p.side, side)
	name := fallback(strings.TrimSpace(p.underlying.DisplayName), p.underlying.TradingSymbol)
	header += " " + styleBold.Render(name)
	if quote, ok := p.quotes[string(p.underlying.ID)]; ok && quote.LastPrice > 0 {
		change, direction := formatChange(quote)
		header += "  " + styleMuted.Render("spot ") + styleCyan.Bold(true).Render(formatAmount(quote.LastPrice)) + " " + signStyle(direction).Render(change)
	}
	if strikes := p.strikes(); len(strikes) > 0 {
		header += styleFaint.Render(fmt.Sprintf("  · lot %d", max(strikes[0].LotSize, 1)))
	}
	lines := []string{header, p.tabs(), ""}
	tableH := height - 10
	lines = append(lines, p.grid.view(width-4, tableH, true), "")
	footer := ""
	if p.priceErr != nil {
		footer = styleRed.Bold(true).Render("Prices failed: ") + styleRed.Render(quoteErrorText(p.priceErr))
	} else if option, ok := p.selected(); ok {
		footer = styleBold.Render(option.TradingSymbol)
		if quote := p.quotes[string(option.ID)]; quote.LastPrice > 0 {
			lots := max(option.LotSize, 1)
			footer += styleMuted.Render(fmt.Sprintf(" · %d × %s = ", lots, formatPrice(quote.LastPrice))) +
				styleYellow.Render("₹"+formatAmount(quote.LastPrice*float64(lots))) +
				styleMuted.Render(" per lot · breakeven "+formatPrice(breakeven(option, quote.LastPrice)))
		}
	}
	lines = append(lines, footer, help("↑↓", "strike", "←→ [ ]", "expiry", "Enter", "order", "R", "refresh", "Esc", "back"))
	return modal(sideWord(p.side)+" "+optionWord(p.optionType), "", strings.Join(lines, "\n"), width)
}

// sidePillText is a side's pill with its own text.
func sidePillText(side, text string) string {
	if side == kite.TransactionTypeSell {
		return pill(text, colorRed, colorInk)
	}
	return pill(text, colorGreen, colorInk)
}

// tabs lists the expiries, the shown one highlighted, with how many days
// each has left.
func (p *strikePicker) tabs() string {
	if len(p.expiries) == 0 {
		return ""
	}
	const window = 6
	first := min(max(p.expiry-window/2, 0), max(len(p.expiries)-window, 0))
	last := min(first+window, len(p.expiries))
	var text strings.Builder
	if first > 0 {
		text.WriteString(styleFaint.Render("◀ "))
	} else {
		text.WriteString("  ")
	}
	today := expiryDay(time.Now())
	for index := first; index < last; index++ {
		day := p.expiries[index]
		label := fmt.Sprintf(" %s · %s ", day.Format("02 Jan"), daysLeft(day, today))
		if index == p.expiry {
			text.WriteString(lipgloss.NewStyle().Background(colorCyan).Foreground(colorInk).Bold(true).Render(label))
		} else {
			text.WriteString(styleMuted.Render(label))
		}
		text.WriteString(" ")
	}
	if last < len(p.expiries) {
		text.WriteString(styleFaint.Render("▶"))
	}
	text.WriteString(styleFaint.Render(fmt.Sprintf("  expiry %d of %d", p.expiry+1, len(p.expiries))))
	return text.String()
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
	cancel := a.deps.cancel
	m := &menu{title: "Cancel order " + order.OrderID, info: styleMuted.Render(userID), width: max(48, len(what)+8), keyFold: "YN",
		footer: "key or ↑↓ Enter · Esc keep"}
	m.items = []menuItem{
		{key: "Y", label: styleRed.Bold(true).Render("Y") + "  " + styleText.Render("Cancel ") + styleFaint.Render(what), run: func(a *app) tea.Cmd {
			a.notify(statusInfo("Cancelling order " + order.OrderID + "…"))
			return a.task(func(ctx context.Context) tea.Msg {
				return orderCancelledMsg{userID: userID, order: order, err: cancel(ctx, userID, order)}
			})
		}},
		{key: "N", label: styleGreen.Bold(true).Render("N") + "  " + styleText.Render("Keep it"), run: func(*app) tea.Cmd { return nil }},
	}
	// Keep is the default, so a stray Enter cancels nothing.
	m.cursor = 1
	a.overlay = m
	return nil
}
