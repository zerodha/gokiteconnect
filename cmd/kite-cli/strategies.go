package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// B on the option chain offers baskets built from the chain: a straddle, a
// strangle or a call spread on the shown expiry. Each opens a form that
// prices its legs and adds them to a basket.

type strategyKind int

const (
	strategyStraddle strategyKind = iota
	strategyStrangle
	strategyCallSpread
)

var strategyNames = []string{"Straddle", "Strangle", "Call spread"}

const (
	strategyFieldSide    = "Side"
	strategyFieldStrike  = "Strike"
	strategyFieldWidth   = "Width"
	strategyFieldCall    = "Call strike"
	strategyFieldPut     = "Put strike"
	strategyFieldLower   = "Lower strike"
	strategyFieldUpper   = "Upper strike"
	strategyFieldLots    = "Lots"
	strategyFieldProduct = "Product"

	strategyButtonAdd    = "Add to basket"
	strategyButtonCancel = "Cancel"
)

// The strangle's Width slides each strike this many percent from spot.
const (
	strangleLow, strangleHigh, strangleStride = 0.5, 10, 0.5
	strangleWidth                             = 2
)

type strategyQuotesMsg struct {
	form   *strategyForm
	gen    int
	source quoteSource
	// down is whether source stands in for a ticker that is down.
	down   bool
	quotes kite.Quote
	err    error
}

// strategyLeg is one order of a strategy.
type strategyLeg struct {
	contract models.Instrument
	side     string
}

// strategyForm builds one strategy on an expiry's strikes, priced from the
// chain's quotes and its own fetches for legs the chain has not priced.
type strategyForm struct {
	kind       strategyKind
	underlying models.InstrumentID
	expiry     expiryChoice
	rows       []chainRow
	spot       float64
	quotes     map[string]kite.QuoteData
	form       *form
	// gen drops quotes a newer leg change asked for again.
	gen      int
	priceErr error
}

// openStrategyMenu lists the strategies to build on the chain, and the
// Baskets tab B opened before.
func (a *app) openStrategyMenu() tea.Cmd {
	e := a.explorer
	m := &menu{title: "Basket from the chain", info: styleMuted.Render(symbolOf(e.chain.underlying) + " · " + e.chain.expiry().date.Format("02 Jan")),
		width: 58, footer: "↑↓ Enter or 1–3 · B baskets · Esc close", keyFold: "b"}
	descriptions := []string{"call and put at one strike", "OTM call and put a % from spot", "buy a call, sell a higher one"}
	for index, name := range strategyNames {
		kind, key := strategyKind(index), strconv.Itoa(index+1)
		m.items = append(m.items, menuItem{key: key, label: styleKey.Render(key) + "  " + styleText.Render(padRight(name, 13)) + styleFaint.Render(descriptions[index]),
			run: func(a *app) tea.Cmd { return a.openStrategy(kind) }})
	}
	m.items = append(m.items, menuItem{key: "b", label: styleKey.Render("B") + "  " + styleMuted.Render("Open Baskets"), run: func(a *app) tea.Cmd { return a.openBaskets() }})
	a.overlay = m
	return nil
}

// openStrategy opens kind's form on the chain's expiry, centred on spot.
func (a *app) openStrategy(kind strategyKind) tea.Cmd {
	e := a.explorer
	if a.activeUser() == "" {
		a.closeOverlay()
		a.notify(statusWarn("Select a user first · baskets are each user's own"))
		return nil
	}
	rows := buildChainRows(e.chain.options, chainFilter{expiryNumber: e.chain.filter.expiryNumber, side: optionSideBoth})
	if len(rows) == 0 {
		a.closeOverlay()
		a.notify(statusWarn("No strikes listed for this expiry"))
		return nil
	}
	o := &strategyForm{kind: kind, underlying: e.chain.underlying, expiry: e.chain.expiry(), rows: rows, spot: e.spot(),
		quotes: map[string]kite.QuoteData{}}
	for _, row := range rows {
		for _, leg := range []*models.Instrument{row.call, row.put} {
			if leg != nil {
				if quote, ok := e.quotes[string(leg.ID)]; ok {
					o.quotes[string(leg.ID)] = quote
				}
			}
		}
	}
	strikes := make([]string, len(rows))
	for index, row := range rows {
		strikes[index] = formatStrike(row.strike)
	}
	atm := o.atm()
	f := newForm(12, 10, strategyButtonAdd, strategyButtonCancel)
	switch kind {
	case strategyStraddle:
		f.addSelect(strategyFieldSide, []string{kite.TransactionTypeSell, kite.TransactionTypeBuy}, 0)
		f.addSelect(strategyFieldStrike, strikes, atm)
	case strategyStrangle:
		f.addSelect(strategyFieldSide, []string{kite.TransactionTypeSell, kite.TransactionTypeBuy}, 0)
		f.addSlider(strategyFieldWidth, strangleWidth, strangleLow, strangleHigh, strangleStride).disabled = o.spot <= 0
		f.addInput(strategyFieldCall, "", decimalNumber, false)
		f.addInput(strategyFieldPut, "", decimalNumber, false)
	case strategyCallSpread:
		f.addSelect(strategyFieldSide, []string{"Bull · debit", "Bear · credit"}, 0)
		f.addSelect(strategyFieldLower, strikes, atm)
		f.addSelect(strategyFieldUpper, strikes, min(atm+1, len(rows)-1))
	}
	f.addCounter(strategyFieldLots, 1, 1, 1)
	f.addSelect(strategyFieldProduct, []string{kite.ProductNRML, kite.ProductMIS}, 0)
	o.form = f
	if kind == strategyStrangle {
		o.slid()
		if o.spot <= 0 {
			// Without spot the strangle starts a strike either side of the
			// middle one.
			o.setStrike(strategyFieldCall, rows[min(atm+1, len(rows)-1)].strike)
			o.setStrike(strategyFieldPut, rows[max(atm-1, 0)].strike)
		}
	}
	f.focusFirst()
	o.sync()
	a.overlay = o
	return a.priceStrategy(o)
}

// atm is the index of the strike nearest spot, or the middle one.
func (o *strategyForm) atm() int {
	if o.spot <= 0 {
		return len(o.rows) / 2
	}
	best := 0
	for index, row := range o.rows {
		if math.Abs(row.strike-o.spot) < math.Abs(o.rows[best].strike-o.spot) {
			best = index
		}
	}
	return best
}

// row is the expiry's row at strike.
func (o *strategyForm) row(strike float64) (chainRow, bool) {
	index := slices.IndexFunc(o.rows, func(row chainRow) bool { return row.strike == strike })
	if index < 0 {
		return chainRow{}, false
	}
	return o.rows[index], true
}

func (o *strategyForm) strikeOf(label string) float64 {
	f := o.form.field(label)
	if f.kind == fieldSelect {
		if f.index >= 0 && f.index < len(o.rows) {
			return o.rows[f.index].strike
		}
		return 0
	}
	strike, _ := strconv.ParseFloat(f.value(), 64)
	return strike
}

func (o *strategyForm) setStrike(label string, strike float64) {
	input := &o.form.field(label).input
	input.SetValue(strconv.FormatFloat(strike, 'f', -1, 64))
	input.CursorEnd()
}

// slid moves the strangle's strikes to the nearest listed ones Width from
// spot: the call's at or above, the put's at or below.
func (o *strategyForm) slid() {
	if o.spot <= 0 {
		return
	}
	width := o.form.field(strategyFieldWidth).amount / 100
	callAt, putAt := o.spot*(1+width), o.spot*(1-width)
	// Past the listed strikes, each takes the farthest one.
	call, put, highest, lowest := 0.0, 0.0, 0.0, 0.0
	for _, row := range o.rows {
		if row.call != nil {
			highest = max(highest, row.strike)
			if row.strike >= callAt && (call == 0 || row.strike < call) {
				call = row.strike
			}
		}
		if row.put != nil {
			if lowest == 0 || row.strike < lowest {
				lowest = row.strike
			}
			if row.strike <= putAt && row.strike > put {
				put = row.strike
			}
		}
	}
	if call == 0 {
		call = highest
	}
	if put == 0 {
		put = lowest
	}
	o.setStrike(strategyFieldCall, call)
	o.setStrike(strategyFieldPut, put)
}

func (o *strategyForm) lots() int {
	lots, _ := strconv.Atoi(o.form.field(strategyFieldLots).value())
	return lots
}

// legs are the strategy's orders, or why it has none: a straddle's or
// strangle's call then put, a spread's buy first so it margins the sell.
func (o *strategyForm) legs() ([]strategyLeg, error) {
	side := o.form.field(strategyFieldSide).value()
	contract := func(label string, call bool) (models.Instrument, error) {
		strike := o.strikeOf(label)
		row, ok := o.row(strike)
		leg := row.put
		if call {
			leg = row.call
		}
		if !ok || leg == nil {
			kind := "put"
			if call {
				kind = "call"
			}
			return models.Instrument{}, fmt.Errorf("no %s listed at %s", kind, formatStrike(strike))
		}
		return *leg, nil
	}
	var legs []strategyLeg
	switch o.kind {
	case strategyStraddle, strategyStrangle:
		callLabel, putLabel := strategyFieldStrike, strategyFieldStrike
		if o.kind == strategyStrangle {
			callLabel, putLabel = strategyFieldCall, strategyFieldPut
		}
		call, err := contract(callLabel, true)
		if err != nil {
			return nil, err
		}
		put, err := contract(putLabel, false)
		if err != nil {
			return nil, err
		}
		if o.kind == strategyStrangle && put.Strike >= call.Strike {
			return nil, fmt.Errorf("the put strike must be below the call's")
		}
		legs = []strategyLeg{{call, side}, {put, side}}
	case strategyCallSpread:
		lower, err := contract(strategyFieldLower, true)
		if err != nil {
			return nil, err
		}
		upper, err := contract(strategyFieldUpper, true)
		if err != nil {
			return nil, err
		}
		if upper.Strike <= lower.Strike {
			return nil, fmt.Errorf("the upper strike must be above the lower")
		}
		legs = []strategyLeg{{lower, kite.TransactionTypeBuy}, {upper, kite.TransactionTypeSell}}
		if o.form.field(strategyFieldSide).index == 1 {
			legs = []strategyLeg{{upper, kite.TransactionTypeBuy}, {lower, kite.TransactionTypeSell}}
		}
	}
	return legs, nil
}

// sync labels the fields: Width's percent, and strikes not listed.
func (o *strategyForm) sync() {
	f := o.form
	if o.kind == strategyStrangle {
		width := f.field(strategyFieldWidth)
		width.hint = styleFaint.Render("needs spot")
		if o.spot > 0 {
			width.hint = styleText.Render(fmt.Sprintf("%g%% from spot", width.amount))
		}
		for _, label := range []string{strategyFieldCall, strategyFieldPut} {
			strike, field := o.strikeOf(label), f.field(label)
			field.hint = ""
			row, ok := o.row(strike)
			switch {
			case !ok || (label == strategyFieldCall && row.call == nil) || (label == strategyFieldPut && row.put == nil):
				field.hint = statusWarn("not listed")
			case o.spot > 0:
				field.hint = styleFaint.Render(formatPercent(strike-o.spot, o.spot))
			}
		}
	}
	if f.field(strategyFieldLots).value() == "" || o.lots() <= 0 {
		f.field(strategyFieldLots).hint = statusWarn("at least 1")
	} else {
		f.field(strategyFieldLots).hint = ""
	}
}

// priceStrategy fetches the legs' quotes.
func (a *app) priceStrategy(o *strategyForm) tea.Cmd {
	legs, err := o.legs()
	if err != nil || len(legs) == 0 {
		return nil
	}
	o.gen++
	gen := o.gen
	var ids []string
	for _, leg := range legs {
		ids = append(ids, string(leg.contract.ID))
	}
	ids = append(ids, string(o.underlying))
	source, load := a.explorer.quoteSource, a.deps.loadQuotes
	if source == nil && load == nil {
		return nil
	}
	return a.task(func(ctx context.Context) tea.Msg {
		source, quotes, down, err := quoteOn(ctx, source, load, ids...)
		return strategyQuotesMsg{form: o, gen: gen, source: source, down: down, quotes: quotes, err: err}
	})
}

func (a *app) handleStrategy(msg tea.Msg) (tea.Cmd, bool) {
	priced, ok := msg.(strategyQuotesMsg)
	if !ok {
		return nil, false
	}
	o := priced.form
	if priced.gen != o.gen {
		return nil, true
	}
	if a.explorer.quoteSource == nil && priced.source != nil {
		a.explorer.quoteSource, a.explorer.tickerDown = priced.source, priced.down
	}
	o.priceErr = priced.err
	for id, quote := range priced.quotes {
		if id == string(o.underlying) {
			if quote.LastPrice > 0 && o.spot <= 0 {
				o.spot = quote.LastPrice
				o.spotArrived()
			}
			continue
		}
		o.quotes[id] = quote
	}
	o.sync()
	return nil, true
}

// spotArrived lets the strangle slide once spot is known.
func (o *strategyForm) spotArrived() {
	if o.kind == strategyStrangle {
		o.form.field(strategyFieldWidth).disabled = false
		o.slid()
	}
}

func (o *strategyForm) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		a.closeOverlay()
		return nil
	}
	current := o.form.current()
	pressed, changed := o.form.update(msg)
	if changed {
		if current != nil && current.label == strategyFieldWidth {
			o.slid()
		}
		o.sync()
		if current != nil && current.label != strategyFieldLots && current.label != strategyFieldProduct {
			return a.priceStrategy(o)
		}
	}
	if pressed < 0 {
		return nil
	}
	if o.form.buttons[pressed] == strategyButtonCancel {
		a.closeOverlay()
		return nil
	}
	return a.addStrategy(o)
}

// items are the strategy's basket orders: market orders of lots lots.
func (o *strategyForm) items() ([]models.BasketItem, error) {
	legs, err := o.legs()
	if err != nil {
		return nil, err
	}
	lots := o.lots()
	if lots <= 0 {
		return nil, fmt.Errorf("enter at least 1 lot")
	}
	product := o.form.field(strategyFieldProduct).value()
	items := make([]models.BasketItem, 0, len(legs))
	for _, leg := range legs {
		items = append(items, models.BasketItem{Exchange: leg.contract.Exchange, TradingSymbol: leg.contract.TradingSymbol,
			TransactionType: leg.side, OrderType: kite.OrderTypeMarket, Product: product,
			Quantity: lots * max(leg.contract.LotSize, 1)})
	}
	return items, nil
}

// name is the strategy's suggested basket name.
func (o *strategyForm) name() string {
	legs, _ := o.legs()
	var strikes []string
	for _, leg := range legs {
		if strike := formatStrike(leg.contract.Strike); !slices.Contains(strikes, strike) {
			strikes = append(strikes, strike)
		}
	}
	slices.Sort(strikes)
	return fmt.Sprintf("%s %s %s %s", symbolOf(o.underlying), o.expiry.date.Format("02 Jan"), strings.Join(strikes, "/"), strings.ToLower(strategyNames[o.kind]))
}

// addStrategy adds the strategy's orders to a new draft, named for it; S on
// the Baskets tab names and saves it.
func (a *app) addStrategy(o *strategyForm) tea.Cmd {
	items, err := o.items()
	if err != nil {
		o.form.message = statusWarn(err.Error())
		return nil
	}
	owner := a.activeUser()
	o.form.message = statusInfo("Loading " + owner + "'s baskets…")
	return a.basketOperation(owner, "", nil, func(a *app, _ string, stored []models.Basket) tea.Cmd {
		if a.overlay != o {
			return nil
		}
		taken := a.baskets.names(owner, "", stored)
		name := o.name()
		for n := 2; slices.Contains(taken, name); n++ {
			name = fmt.Sprintf("%s %d", o.name(), n)
		}
		a.baskets.newDraft(owner, name, items...)
		a.closeOverlay()
		a.notify(statusOK("Added " + name + " to Baskets, unsaved"))
		return nil
	})
}

func (o *strategyForm) ltp(contract models.Instrument) float64 {
	return o.quotes[string(contract.ID)].LastPrice
}

// summary prices the legs: each one's LTP, the premium the strategy takes
// in or pays, what it can make or lose, and where it breaks even.
func (o *strategyForm) summary() []string {
	legs, err := o.legs()
	if err != nil {
		return []string{statusWarn(err.Error())}
	}
	lots := max(o.lots(), 1)
	var lines []string
	priced := true
	for _, leg := range legs {
		price := styleFaint.Render("—")
		if ltp := o.ltp(leg.contract); ltp > 0 {
			price = styleText.Render(formatPrice(ltp))
		} else {
			priced = false
		}
		lines = append(lines, sidePill(leg.side)+" "+styleText.Render(fmt.Sprintf("%d × %s", lots*max(leg.contract.LotSize, 1), leg.contract.TradingSymbol))+"  "+price)
	}
	lines = append(lines, "")
	if !priced {
		text := styleFaint.Render("Pricing the legs…")
		if o.priceErr != nil {
			text = styleRed.Render("Prices failed: " + quoteErrorText(o.priceErr))
		}
		return append(lines, text)
	}
	quantity := float64(lots * max(legs[0].contract.LotSize, 1))
	label := func(name, value string) string { return styleMuted.Render(padRight(name, 12)) + value }
	amount := func(points float64) string {
		return styleText.Render(formatPrice(points)) + styleFaint.Render(" · ₹"+formatAmount(points*quantity))
	}
	switch o.kind {
	case strategyStraddle, strategyStrangle:
		call, put := legs[0].contract, legs[1].contract
		premium := o.ltp(call) + o.ltp(put)
		low, high := put.Strike-premium, call.Strike+premium
		if legs[0].side == kite.TransactionTypeSell {
			lines = append(lines, label("Credit", amount(premium)), label("Max profit", amount(premium)), label("Max loss", styleRed.Render("unlimited")))
		} else {
			lines = append(lines, label("Debit", amount(premium)), label("Max loss", amount(premium)), label("Max profit", styleGreen.Render("unlimited")))
		}
		breakevens := formatPrice(low) + " – " + formatPrice(high)
		if o.spot > 0 {
			breakevens += styleFaint.Render(fmt.Sprintf("  (%s / %s)", formatPercent(low-o.spot, o.spot), formatPercent(high-o.spot, o.spot)))
		}
		lines = append(lines, label("Breakevens", styleText.Render(breakevens)))
	case strategyCallSpread:
		var lower, upper models.Instrument
		for _, leg := range legs {
			if lower.ID == "" || leg.contract.Strike < lower.Strike {
				lower = leg.contract
			}
			if leg.contract.Strike > upper.Strike {
				upper = leg.contract
			}
		}
		net, width := o.ltp(lower)-o.ltp(upper), upper.Strike-lower.Strike
		if o.form.field(strategyFieldSide).index == 0 {
			lines = append(lines, label("Debit", amount(net)), label("Max profit", amount(width-net)), label("Max loss", amount(net)))
		} else {
			lines = append(lines, label("Credit", amount(net)), label("Max profit", amount(net)), label("Max loss", amount(width-net)))
		}
		breakeven := styleText.Render(formatPrice(lower.Strike + net))
		if o.spot > 0 {
			breakeven += styleFaint.Render("  (" + formatPercent(lower.Strike+net-o.spot, o.spot) + ")")
		}
		lines = append(lines, label("Breakeven", breakeven))
	}
	return lines
}

func (o *strategyForm) view(_ *app, w, _ int) string {
	width := min(66, w-2)
	spot := "—"
	if o.spot > 0 {
		spot = formatPrice(o.spot)
	}
	header := styleBold.Render(symbolOf(o.underlying)) + styleFaint.Render(fmt.Sprintf(" · spot %s · %s", spot, o.expiry.date.Format("02 Jan 2006")))
	help := "↑↓ field · ←→ change · Esc cancel"
	if o.kind == strategyStrangle {
		help = fmt.Sprintf("↑↓ field · ←→ %g%% · Shift+←→ %g%% · type a strike · Esc cancel", strangleStride, strangleStride*sliderShift)
	}
	body := header + "\n\n" + strings.Join(o.summary(), "\n") + "\n\n" + o.form.view(width-4) + "\n\n" + styleFaint.Render(help)
	return modal(strategyNames[o.kind], styleMuted.Render("market orders"), body, width)
}
