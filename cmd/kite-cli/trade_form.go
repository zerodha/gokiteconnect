package main

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// tradeTab is a tab of the order form + opens: the selected instrument
// itself, or its underlying's calls, puts or futures.
type tradeTab int

const (
	tradeOrder tradeTab = iota
	tradeCall
	tradePut
	tradeFut
)

// tradeTabs name the tabs and the keys that show them.
var tradeTabs = []struct{ key, name string }{{"o", "Order"}, {"c", "Call"}, {"p", "Put"}, {"f", "Fut"}}

// The derivative tabs' contract fields.
const (
	orderFieldExpiry = "Expiry"
	orderFieldStrike = "Strike"
)

// tradeLegs are what the order form's tabs order: the target itself, or
// its underlying's unexpired contracts, priced from source.
type tradeLegs struct {
	target     tradeTarget
	underlying models.InstrumentID
	tab        tradeTab
	loading    bool
	err        error
	options    []models.Instrument
	futures    []models.Instrument
	source     quoteSource
	quotes     map[string]kite.QuoteData
	fetching   bool
	priceErr   error
	// expiries and contracts are what the Expiry and Strike fields list.
	expiries  []time.Time
	contracts []models.Instrument
	// atm moves the strike to the money once the spot is known, until a
	// strike is picked.
	atm bool
	// missing says why the tab has nothing to order.
	missing string
}

type (
	tradeLoadedMsg struct {
		form             *orderForm
		options, futures []models.Instrument
		source           quoteSource
		err              error
	}
	tradeQuotesMsg struct {
		form *orderForm
		// contract is the one on the form when its quote was asked for.
		contract models.InstrumentID
		quotes   kite.Quote
		err      error
	}
	tradeTickMsg struct{ form *orderForm }
)

// openTradeForm opens the order form for target: a buy of it on the Order
// tab, or, for an index, of its nearest call at the money. Its underlying's
// calls, puts and futures load behind the other tabs.
func (a *app) openTradeForm(target tradeTarget) tea.Cmd {
	return a.openTradeTicket(target, nil)
}

// openTradeTicket opens the order form for target, for alert's orders
// when alert is set.
func (a *app) openTradeTicket(target tradeTarget, alert *alertForm) tea.Cmd {
	instrument := target.instrument
	underlying, derivatives := optionUnderlying(instrument)
	raw := instrument.Segment != "INDICES"
	if !raw && !derivatives {
		a.notify(statusWarn("Nothing to order for " + string(instrument.ID)))
		return nil
	}
	ticket := newOrderTicket(instrument, kite.TransactionTypeBuy, target.ltp)
	if position, ok := target.held.(kite.Position); ok {
		ticket.product = position.Product
	}
	if alert != nil {
		ticket.alert, ticket.userID = alert, alert.user
	}
	o, err := a.newOrderForm(ticket)
	if err != nil {
		a.showError(err)
		return nil
	}
	a.overlay = o
	o.target = &target
	if !derivatives {
		return a.orderMargin(o)
	}
	legs := &tradeLegs{target: target, underlying: underlying, loading: true, atm: true,
		source: a.explorer.quoteSource, quotes: map[string]kite.QuoteData{}}
	for _, id := range []string{string(instrument.ID), string(underlying)} {
		if quote, ok := a.explorer.quotes[id]; ok {
			legs.quotes[id] = quote
		}
	}
	if target.ltp > 0 {
		quote := legs.quotes[string(instrument.ID)]
		quote.LastPrice = target.ltp
		legs.quotes[string(instrument.ID)] = quote
	}
	o.legs = legs
	o.form.insertSelect(0, orderFieldExpiry, nil, 0).hidden = true
	o.form.insertSelect(1, orderFieldStrike, nil, 0).hidden = true
	tab := tradeOrder
	if !raw {
		tab = tradeCall
	}
	o.showTab(tab)
	return tea.Batch(a.orderMargin(o), a.loadTradeLegs(o))
}

// loadTradeLegs reads the underlying's options and futures, and connects
// the data user to price them when the explorer has not.
func (a *app) loadTradeLegs(o *orderForm) tea.Cmd {
	catalog, open, load := a.explorer.catalog, a.deps.openCatalog, a.deps.loadQuotes
	source, underlying := o.legs.source, o.legs.underlying
	return a.task(func(ctx context.Context) tea.Msg {
		msg := tradeLoadedMsg{form: o, source: source}
		if catalog == nil {
			if open == nil {
				msg.err = fmt.Errorf("the instrument catalog is unavailable")
				return msg
			}
			if catalog, _, msg.err = open(ctx); msg.err != nil {
				return msg
			}
		}
		if msg.options, msg.err = catalog.GetOptions(ctx, models.OptionsFilter{UnderlyingID: underlying}); msg.err != nil {
			return msg
		}
		if msg.futures, msg.err = catalog.GetFutures(ctx, models.FuturesFilter{UnderlyingID: underlying}); msg.err != nil {
			return msg
		}
		if msg.source == nil && load != nil {
			// Unpriced contracts can still be ordered.
			msg.source, _, _ = load(ctx)
		}
		return msg
	})
}

func (a *app) handleTrade(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tradeLoadedMsg:
		o := msg.form
		legs := o.legs
		legs.loading, legs.err = false, msg.err
		if legs.source == nil {
			legs.source = msg.source
		}
		if msg.err == nil {
			legs.options = legs.unexpired(msg.options)
			legs.futures = legs.unexpired(msg.futures)
		}
		if legs.tab != tradeOrder {
			o.showTab(legs.tab)
		}
		if a.overlay != o {
			return nil, true
		}
		return tea.Batch(a.orderMargin(o), a.fetchTradeQuotes(o), a.tradeTick(o)), true
	case tradeTickMsg:
		if a.overlay != msg.form {
			return nil, true
		}
		return tea.Batch(a.fetchTradeQuotes(msg.form), a.tradeTick(msg.form)), true
	case tradeQuotesMsg:
		o := msg.form
		o.legs.fetching, o.legs.priceErr = false, msg.err
		if msg.err != nil {
			return nil, true
		}
		o.priced(msg.quotes)
		if a.overlay != o {
			return nil, true
		}
		// The contract may have changed while its quote was asked for.
		if o.ticket.instrument.ID != msg.contract {
			return tea.Batch(a.orderMargin(o), a.fetchTradeQuotes(o)), true
		}
		return a.orderMargin(o), true
	}
	return nil, false
}

func (a *app) tradeTick(o *orderForm) tea.Cmd {
	if a.deps.strikeEvery <= 0 {
		return nil
	}
	return tea.Tick(a.deps.strikeEvery, func(time.Time) tea.Msg { return tradeTickMsg{form: o} })
}

// fetchTradeQuotes prices the underlying and the contract on the form.
func (a *app) fetchTradeQuotes(o *orderForm) tea.Cmd {
	legs := o.legs
	if legs == nil || legs.source == nil || legs.fetching {
		return nil
	}
	ids := []string{string(legs.underlying)}
	if id := string(o.ticket.instrument.ID); id != ids[0] {
		ids = append(ids, id)
	}
	legs.fetching = true
	source, contract := legs.source, o.ticket.instrument.ID
	return a.task(func(context.Context) tea.Msg {
		quotes, err := source.GetQuote(ids...)
		return tradeQuotesMsg{form: o, contract: contract, quotes: quotes, err: err}
	})
}

// unexpired keeps the underlying's contracts that have not expired.
func (l *tradeLegs) unexpired(contracts []models.Instrument) []models.Instrument {
	today := expiryDay(time.Now())
	var kept []models.Instrument
	for _, contract := range contracts {
		if contract.Expiry == nil || expiryDay(*contract.Expiry).Before(today) {
			continue
		}
		if contract.UnderlyingID != nil && *contract.UnderlyingID != l.underlying {
			continue
		}
		kept = append(kept, contract)
	}
	slices.SortStableFunc(kept, func(left, right models.Instrument) int {
		if order := left.Expiry.Compare(*right.Expiry); order != 0 {
			return order
		}
		return cmp.Compare(left.Strike, right.Strike)
	})
	return kept
}

// tabContracts are the unexpired contracts tab orders.
func (l *tradeLegs) tabContracts(tab tradeTab) []models.Instrument {
	switch tab {
	case tradeCall, tradePut:
		optionType := string(models.OptionTypeCall)
		if tab == tradePut {
			optionType = string(models.OptionTypePut)
		}
		return slices.DeleteFunc(slices.Clone(l.options), func(option models.Instrument) bool { return option.InstrumentType != optionType })
	case tradeFut:
		// A copy, which callers may filter in place.
		return slices.Clone(l.futures)
	}
	return nil
}

// available reports whether tab can be shown: the Order tab unless the
// target is an index, and the others while loading or once they have
// contracts.
func (l *tradeLegs) available(tab tradeTab) bool {
	if tab == tradeOrder {
		return l.target.instrument.Segment != "INDICES"
	}
	return l.loading || len(l.tabContracts(tab)) > 0
}

// spot is the underlying's last price, or 0.
func (l *tradeLegs) spot() float64 {
	return l.quotes[string(l.underlying)].LastPrice
}

// showTab shows tab: the target itself, or the tab's expiries and strikes,
// keeping the expiry and strike shown when the tab lists them.
func (o *orderForm) showTab(tab tradeTab) {
	legs, f := o.legs, o.form
	legs.tab, legs.missing = tab, ""
	f.field(orderFieldExpiry).hidden, f.field(orderFieldStrike).hidden = true, true
	switch {
	case tab == tradeOrder:
		o.setContract(legs.target.instrument)
	case legs.loading:
		legs.missing = "Loading contracts…"
	case legs.err != nil:
		legs.missing = legs.err.Error()
	default:
		o.fillExpiries()
	}
	o.refresh()
}

// fillExpiries lists the tab's expiries in the Expiry field, keeping the
// one shown or else the target's, then its strikes.
func (o *orderForm) fillExpiries() {
	legs, f := o.legs, o.form
	legs.expiries = legs.expiries[:0]
	for _, contract := range legs.tabContracts(legs.tab) {
		if day := expiryDay(*contract.Expiry); !slices.ContainsFunc(legs.expiries, day.Equal) {
			legs.expiries = append(legs.expiries, day)
		}
	}
	if len(legs.expiries) == 0 {
		legs.missing = fmt.Sprintf("No %s listed for %s", strings.ToLower(tradeTabs[legs.tab].name)+"s", symbolOf(legs.underlying))
		return
	}
	expiry := f.field(orderFieldExpiry)
	expiry.hidden, expiry.index, expiry.options = false, 0, nil
	today := expiryDay(time.Now())
	want, wanted := o.shownExpiry()
	for index, day := range legs.expiries {
		expiry.options = append(expiry.options, day.Format("02 Jan 2006")+" · "+daysLeft(day, today))
		if wanted && day.Equal(want) {
			expiry.index = index
		}
	}
	o.fillStrikes()
}

// shownExpiry is the expiry to keep: the form's contract's, or else the
// target's when it is one of the underlying's contracts.
func (o *orderForm) shownExpiry() (time.Time, bool) {
	for _, contract := range []models.Instrument{o.ticket.instrument, o.legs.target.instrument} {
		if contract.Expiry != nil {
			return expiryDay(*contract.Expiry), true
		}
	}
	return time.Time{}, false
}

// fillStrikes lists the shown expiry's strikes in the Strike field and
// picks one: the form's contract's or the target's strike, or else the
// one nearest spot. A futures expiry has one contract and no strikes.
func (o *orderForm) fillStrikes() {
	legs, f := o.legs, o.form
	day := legs.expiries[f.field(orderFieldExpiry).index]
	legs.contracts = slices.DeleteFunc(legs.tabContracts(legs.tab), func(contract models.Instrument) bool {
		return !expiryDay(*contract.Expiry).Equal(day)
	})
	if legs.tab == tradeFut {
		o.setContract(legs.contracts[0])
		return
	}
	strike := f.field(orderFieldStrike)
	strike.hidden, strike.options = false, nil
	for _, option := range legs.contracts {
		strike.options = append(strike.options, formatStrike(option.Strike))
	}
	at, picked := 0.0, false
	for _, contract := range []models.Instrument{o.ticket.instrument, legs.target.instrument} {
		if contract.Strike > 0 && (contract.InstrumentType == string(models.OptionTypeCall) || contract.InstrumentType == string(models.OptionTypePut)) {
			at, picked = contract.Strike, true
			break
		}
	}
	if !picked {
		at = legs.spot()
		legs.atm = at <= 0
		if legs.atm {
			at = legs.contracts[len(legs.contracts)/2].Strike
		}
	}
	strike.index = slices.Index(legs.contracts, nearestOption(legs.contracts, at))
	o.setContract(legs.contracts[strike.index])
}

// pickStrike orders the Strike field's contract, picked by hand.
func (o *orderForm) pickStrike() {
	o.legs.atm = false
	o.setContract(o.legs.contracts[o.form.field(orderFieldStrike).index])
	o.refresh()
}

// setContract puts instrument on the form at its last price, keeping the
// lots between derivatives and starting anything else at one lot.
func (o *orderForm) setContract(instrument models.Instrument) {
	ticket, f := &o.ticket, o.form
	if instrument.ID == ticket.instrument.ID {
		return
	}
	quantity := f.field(orderFieldQuantity)
	lots := 1
	if current, err := strconv.Atoi(quantity.value()); err == nil && ticket.instrument.LotSize > 1 && instrument.LotSize > 1 {
		lots = max(current/ticket.instrument.LotSize, 1)
	}
	ticket.instrument = instrument
	ticket.ltp = o.legs.quotes[string(instrument.ID)].LastPrice
	quantity.input.SetValue(strconv.Itoa(lots * max(instrument.LotSize, 1)))
	quantity.input.CursorEnd()

	product := f.field(orderFieldProduct)
	chosen, products := product.value(), orderProducts(instrument)
	if position, ok := o.legs.target.held.(kite.Position); ok && instrument.ID == o.legs.target.instrument.ID && !slices.Contains(products, position.Product) {
		products = append(products, position.Product)
	}
	product.options, product.index = products, max(slices.Index(products, chosen), 0)

	price := ""
	if ticket.ltp > 0 {
		price = strconv.FormatFloat(ticket.ltp, 'f', -1, 64)
	}
	f.field(orderFieldPrice).input.SetValue(price)
	// The trigger and exits were prices of the contract before.
	for _, label := range []string{orderFieldTrigger, orderFieldStoploss, orderFieldTarget} {
		f.field(label).input.SetValue("")
	}
}

// priced takes fresh quotes: the strike moves to the money once the spot
// is first known, and the price follows the last price until edited.
func (o *orderForm) priced(quotes kite.Quote) {
	legs := o.legs
	for id, quote := range quotes {
		legs.quotes[id] = quote
	}
	if spot := legs.spot(); legs.atm && spot > 0 && legs.missing == "" && (legs.tab == tradeCall || legs.tab == tradePut) {
		legs.atm = false
		strike := o.form.field(orderFieldStrike)
		strike.index = slices.Index(legs.contracts, nearestOption(legs.contracts, spot))
		o.setContract(legs.contracts[strike.index])
	}
	quote, ok := legs.quotes[string(o.ticket.instrument.ID)]
	if !ok || quote.LastPrice <= 0 || quote.LastPrice == o.ticket.ltp {
		o.refresh()
		return
	}
	price := o.form.field(orderFieldPrice)
	if old := price.value(); old == "" || old == strconv.FormatFloat(o.ticket.ltp, 'f', -1, 64) {
		price.input.SetValue(strconv.FormatFloat(quote.LastPrice, 'f', -1, 64))
		price.input.CursorEnd()
	}
	o.ticket.ltp = quote.LastPrice
	o.refresh()
}

// tradeKey handles the form's letter keys: B and S pick the side, and on a
// form + opened, O, C, P and F show a tab, [ and ] change the expiry and X
// closes the position held.
func (a *app) tradeKey(o *orderForm, key string) (tea.Cmd, bool) {
	switch key {
	case "b", "B", "s", "S":
		side := o.form.field(orderFieldSide)
		if side.disabled {
			return nil, true
		}
		want := kite.TransactionTypeBuy
		if strings.EqualFold(key, "s") {
			want = kite.TransactionTypeSell
		}
		side.index = max(slices.Index(side.options, want), 0)
		o.refresh()
		return a.orderMargin(o), true
	case "x", "X":
		if o.target == nil {
			return nil, false
		}
		ticket, err := closingTicket(*o.target)
		if err != nil {
			o.summary = styleYellow.Render(err.Error())
			return nil, true
		}
		return a.openOrder(ticket), true
	}
	legs := o.legs
	if legs == nil {
		return nil, false
	}
	before := o.ticket.instrument.ID
	switch key {
	case "o", "O", "c", "C", "p", "P", "f", "F":
		key = strings.ToLower(key)
		tab := tradeTab(slices.IndexFunc(tradeTabs, func(t struct{ key, name string }) bool { return t.key == key }))
		if !legs.available(tab) {
			return nil, true
		}
		o.showTab(tab)
	case "shift+left", "shift+right":
		// The form has the keys, so Shift+←/→ switch its tabs instead of
		// the screen's.
		step := 1
		if key == "shift+left" {
			step = -1
		}
		tab := legs.tab
		for range tradeTabs {
			tab = tradeTab((int(tab) + step + len(tradeTabs)) % len(tradeTabs))
			if legs.available(tab) {
				break
			}
		}
		if tab == legs.tab {
			return nil, true
		}
		o.showTab(tab)
	case "[", "]":
		// On the Order tab a contract's own tab shows its expiries, the
		// contract's selected, to step from.
		if legs.tab == tradeOrder {
			tab, ok := contractTab(o.ticket.instrument)
			if !ok {
				return nil, true
			}
			o.showTab(tab)
		}
		expiry := o.form.field(orderFieldExpiry)
		if expiry.hidden || legs.missing != "" {
			break
		}
		next := expiry.index + 1
		if key == "[" {
			next = expiry.index - 1
		}
		if next < 0 || next >= len(expiry.options) {
			break
		}
		expiry.index = next
		o.fillStrikes()
		o.refresh()
	default:
		return nil, false
	}
	if o.ticket.instrument.ID != before {
		return tea.Batch(a.orderMargin(o), a.fetchTradeQuotes(o)), true
	}
	return a.orderMargin(o), true
}

// contractTab is the tab that lists a contract: its option type's or the
// futures'. Other instruments have no expiry.
func contractTab(instrument models.Instrument) (tradeTab, bool) {
	if instrument.Expiry == nil {
		return tradeOrder, false
	}
	switch instrument.InstrumentType {
	case string(models.OptionTypeCall):
		return tradeCall, true
	case string(models.OptionTypePut):
		return tradePut, true
	case "FUT":
		return tradeFut, true
	}
	return tradeOrder, false
}

// changedContract follows the Expiry and Strike fields after the form
// changed one of them.
func (o *orderForm) changedContract() bool {
	legs, current := o.legs, o.form.current()
	if legs == nil || current == nil || legs.missing != "" {
		return false
	}
	switch current.label {
	case orderFieldExpiry:
		o.fillStrikes()
		o.refresh()
	case orderFieldStrike:
		o.pickStrike()
	default:
		return false
	}
	return true
}

// tabRow shows the tabs, the shown one highlighted and the ones with
// nothing to order faint, then the underlying's spot.
func (l *tradeLegs) tabRow() string {
	var parts []string
	for index, tab := range tradeTabs {
		label := " " + tab.name + " "
		switch {
		case tradeTab(index) == l.tab:
			parts = append(parts, lipgloss.NewStyle().Background(colorCyan).Foreground(colorOnPill).Bold(true).Render(label))
		case l.available(tradeTab(index)):
			parts = append(parts, styleMuted.Render(label))
		default:
			parts = append(parts, styleFaint.Render(label))
		}
	}
	row := strings.Join(parts, " ")
	if spot := l.spot(); spot > 0 {
		row += "   " + styleMuted.Render(symbolOf(l.underlying)+" ") + styleCyan.Bold(true).Render(formatAmount(spot))
	}
	return row
}

// contractInfo describes an option on the form against spot: its
// moneyness and breakeven.
func (l *tradeLegs) contractInfo(option models.Instrument, ltp float64) string {
	if option.Strike <= 0 || (option.InstrumentType != string(models.OptionTypeCall) && option.InstrumentType != string(models.OptionTypePut)) {
		return ""
	}
	spot := l.spot()
	var parts []string
	if spot > 0 {
		call := option.InstrumentType == string(models.OptionTypeCall)
		atm := math.NaN()
		if l.tab != tradeOrder && len(l.contracts) > 0 {
			atm = nearestOption(l.contracts, spot).Strike
		}
		itm := (call && option.Strike < spot) || (!call && option.Strike > spot)
		money := moneyness(option.Strike, spot, atm, itm)
		parts = append(parts, money.style.Render(money.text))
	}
	if ltp > 0 {
		parts = append(parts, styleMuted.Render("breakeven "+formatPrice(breakeven(option, ltp))))
	}
	return strings.Join(parts, styleFaint.Render(" · "))
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
