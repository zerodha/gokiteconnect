package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// portfolioScreen is one user's positions, grouped by underlying, the day's
// orders, holdings and mutual funds, repriced from quotes while it is shown.
type portfolioScreen struct {
	user credentials.Credentials
	data portfolio
	// positions, orders, holdings and funds are the tables; focus is the
	// one keys move.
	positions, orders, holdings, funds grid
	focus                              int
	// raw shows the selected order's payload in the details pane, scrolled
	// down rawOffset lines.
	raw       bool
	rawOffset int
	collapsed map[models.InstrumentID]bool
	// generation drops loads and repricing a newer load replaced.
	generation int
	loading    bool
	pricing    bool
	status     string
	// notice replaces the status after the next load.
	notice string
}

func newPortfolioScreen() *portfolioScreen {
	right := func(title string, width int) column { return column{title: title, width: width, align: alignRight} }
	return &portfolioScreen{
		positions: grid{columns: []column{
			{title: "INSTRUMENT", width: 16, flex: 1}, {title: "EXCH", width: 5}, {title: "PRODUCT", width: 7},
			right("QTY", 7), right("AVG", 10), right("LTP", 10), right("P&L", 12),
		}},
		orders: grid{columns: []column{
			{title: "TIME", width: 8}, {title: "INSTRUMENT", width: 16, flex: 1}, {title: "SIDE", width: 4},
			{title: "TYPE", width: 6}, {title: "PRODUCT", width: 7}, right("FILLED", 11), right("PRICE", 10),
			right("AVG", 10), {title: "STATUS", width: 16},
		}},
		holdings: grid{columns: []column{
			{title: "SYMBOL", width: 12, flex: 1}, right("QTY", 6), right("AVG", 10), right("LTP", 10),
			right("INVESTED", 12), right("CURRENT", 12), right("P&L", 12), right("P&L%", 8), right("DAY%", 7),
		}},
		funds: grid{columns: []column{
			{title: "FUND", width: 16, flex: 1}, {title: "FOLIO", width: 10}, right("UNITS", 10), right("AVG", 9),
			right("NAV", 9), right("INVESTED", 12), right("CURRENT", 12), right("P&L", 12), right("P&L%", 8),
		}},
	}
}

func (p *portfolioScreen) grids() []*grid {
	return []*grid{&p.positions, &p.holdings, &p.funds, &p.orders}
}

func (p *portfolioScreen) focused() *grid {
	return p.grids()[p.focus]
}

// openPortfolio shows user's portfolio, loading it afresh.
func (a *app) openPortfolio(user credentials.Credentials) tea.Cmd {
	p := a.portfolio
	if user.UserID != p.user.UserID {
		p.data = portfolio{}
		p.focus = 0
		p.collapsed = nil
		p.raw, p.rawOffset = false, 0
		for _, g := range p.grids() {
			g.cursor, g.offset = 0, 0
		}
		p.render()
	}
	p.user = user
	a.show(screenPortfolio)
	return a.reloadPortfolio()
}

type (
	portfolioLoadedMsg struct {
		generation int
		user       credentials.Credentials
		data       portfolio
	}
	portfolioPricedMsg struct {
		generation int
		data       portfolio
	}
)

func (a *app) reloadPortfolio() tea.Cmd {
	p := a.portfolio
	if p.user.UserID == "" || a.deps.loadPortfolio == nil {
		return nil
	}
	p.generation++
	p.loading = true
	p.status = statusInfo(fmt.Sprintf("Loading the portfolio of %s · logs in if the session has expired…", p.user.UserID))
	generation, user, load := p.generation, p.user, a.deps.loadPortfolio
	return a.task(func(ctx context.Context) tea.Msg {
		return portfolioLoadedMsg{generation: generation, user: user, data: load(ctx, user)}
	})
}

// refreshPortfolioPrices reprices the loaded portfolio in the background,
// skipping a tick while a request is still running.
func (a *app) refreshPortfolioPrices() tea.Cmd {
	p := a.portfolio
	if p.loading || p.pricing || p.data.prices == nil {
		return nil
	}
	p.pricing = true
	generation, data := p.generation, p.data
	return a.task(func(context.Context) tea.Msg {
		return portfolioPricedMsg{generation: generation, data: data.reprice()}
	})
}

func (a *app) handlePortfolio(msg tea.Msg) (tea.Cmd, bool) {
	p := a.portfolio
	switch msg := msg.(type) {
	case portfolioLoadedMsg:
		// A newer load, or another user's, supersedes this one.
		if msg.generation != p.generation {
			return nil, true
		}
		p.loading = false
		p.data = msg.data
		p.render()
		status := ""
		if p.notice != "" {
			status, p.notice = p.notice, ""
		}
		data := msg.data
		if err := firstError(data.positionsErr, data.ordersErr, data.holdingsErr, data.fundsErr); err != nil {
			status = statusErr("Some sections failed:", quoteErrorText(err))
		} else if data.pricesErr != nil {
			status = statusErr("Live prices failed, showing Kite's portfolio prices:", quoteErrorText(data.pricesErr))
		} else if data.instrumentsErr != nil {
			status = statusWarn("Positions are not grouped: " + quoteErrorText(data.instrumentsErr))
		} else if data.tickerErr != nil {
			status = statusErr("Ticker unavailable, using quotes:", data.tickerErr.Error())
		}
		var cmd tea.Cmd
		if data.loggedIn {
			p.user.LastLogin = time.Now()
			status = statusOK(fmt.Sprintf("Session expired; %s was logged in again", msg.user.UserID)) + " · " + status
			cmd = a.loadUsers("", "")
		}
		if status == "" {
			status = statusInfo(fmt.Sprintf("Loaded %s", data.loadedAt.Format("15:04:05")))
		}
		p.status = status
		return cmd, true
	case portfolioPricedMsg:
		p.pricing = false
		// A reload replaced the portfolio meanwhile.
		if msg.generation != p.generation || p.loading {
			return nil, true
		}
		failedBefore := p.data.pricesErr != nil
		p.data = msg.data
		p.render()
		switch {
		case msg.data.pricesErr != nil:
			p.status = statusErr("Live prices failed:", quoteErrorText(msg.data.pricesErr))
		case failedBefore:
			p.status = statusOK("Live prices restored")
		}
		return nil, true
	}
	return nil, false
}

func (a *app) portfolioKey(msg tea.KeyPressMsg) tea.Cmd {
	p := a.portfolio
	key := msg.String()
	count := len(p.grids())
	g := p.focused()
	before := g.cursor
	if g.update(key) {
		p.rawOffset = 0
		// Up or Down past a table's edge moves into the next table.
		if g.cursor == before {
			switch {
			case (key == "down" || key == "j") && p.focus < count-1:
				p.focus++
				next := p.focused()
				next.cursor = 0
				next.settle(1)
			case (key == "up" || key == "k") && p.focus > 0:
				p.focus--
				next := p.focused()
				next.cursor = max(len(next.rows)-1, 0)
				next.settle(-1)
			}
		}
		return nil
	}
	switch key {
	case "tab":
		p.focus = (p.focus + 1) % count
	case "shift+tab":
		p.focus = (p.focus + count - 1) % count
	case "enter":
		switch reference := p.focused().selectedRef().(type) {
		case models.InstrumentID:
			p.toggleGroup(reference)
		case kite.Order:
			if a.width < detailMinWidth {
				a.overlay = newPayloadOverlay(reference)
				return nil
			}
			p.raw, p.rawOffset = !p.raw, 0
		}
	case "J", "shift+down":
		p.rawOffset++
	case "K", "shift+up":
		p.rawOffset = max(p.rawOffset-1, 0)
	case "esc", "q", "Q":
		if p.raw && p.focused() == &p.orders {
			p.raw = false
			return nil
		}
		a.show(screenUsers)
	case "r", "R":
		return a.reloadPortfolio()
	case "u", "U":
		a.openUserPicker()
	case "m", "M", "x", "X":
		if underlying, ok := p.focused().selectedRef().(models.InstrumentID); ok && (key == "x" || key == "X") {
			return a.confirmExitGroup(underlying)
		}
		order, ok := p.focused().selectedRef().(kite.Order)
		if !ok {
			return nil
		}
		if !orderOpen(order) {
			p.status = statusWarn(fmt.Sprintf("Order %s is %s; only open orders can be modified or cancelled", order.OrderID, strings.ToLower(fallback(order.Status, "unknown"))))
			return nil
		}
		if key == "x" || key == "X" {
			return a.confirmCancel(p.user.UserID, order)
		}
		return a.openOrder(modifyTicket(p.orderTarget(order).instrument, order, p.user.UserID))
	case "o", "O":
		if underlying, ok := p.focused().selectedRef().(models.InstrumentID); ok {
			return a.confirmExitGroup(underlying)
		}
		target, ok := p.selectedTarget()
		if !ok {
			p.status = statusWarn("Select a position or holding to order") + statusInfo(" · funds cannot be ordered here")
			return nil
		}
		return a.openOrderMenu(target)
	}
	return nil
}

// selectedTarget is the focused table's selected position or holding.
// Group and fund rows are not tradable.
func (p *portfolioScreen) selectedTarget() (tradeTarget, bool) {
	switch reference := p.focused().selectedRef().(type) {
	case kite.Position:
		return p.positionTarget(reference), true
	case kite.Holding:
		return tradeTarget{instrument: holdingInstrument(reference), ltp: reference.LastPrice, held: reference}, true
	case kite.Order:
		return p.orderTarget(reference), true
	}
	return tradeTarget{}, false
}

// positionTarget is position's instrument, at its last price, to close.
func (p *portfolioScreen) positionTarget(position kite.Position) tradeTarget {
	id := positionID(position)
	instrument, ok := p.data.instruments[id]
	if !ok {
		instrument = models.Instrument{ID: id, Exchange: position.Exchange, TradingSymbol: position.Tradingsymbol, LotSize: 1}
	}
	return tradeTarget{instrument: instrument, ltp: position.LastPrice, held: position}
}

// toggleGroup collapses or expands a group, keeping its row selected.
func (p *portfolioScreen) toggleGroup(underlying models.InstrumentID) {
	if p.collapsed == nil {
		p.collapsed = make(map[models.InstrumentID]bool)
	}
	p.collapsed[underlying] = !p.collapsed[underlying]
	p.renderPositions()
	p.positions.selectWhere(func(r row) bool { return r.ref == underlying })
}

func (p *portfolioScreen) render() {
	p.renderPositions()
	p.renderOrders()
	p.renderHoldings()
	p.renderFunds()
}

// sectionEmpty is a table's message when it has no rows: its error, or
// that it is empty.
func (p *portfolioScreen) sectionEmpty(g *grid, err error, empty string) {
	switch {
	case err != nil:
		g.empty = styleRed.Render(quoteErrorText(err))
	case p.loading && p.data.loadedAt.IsZero():
		g.empty = styleMuted.Render("Loading…")
	default:
		g.empty = styleMuted.Render(empty)
	}
}

func amountCell(value float64) cell { return textCell(formatAmount(value), styleText) }

func signedCell(value float64) cell {
	return textCell(formatSigned(value), signStyle(sign(value)))
}

func percentCell(value, base float64) cell {
	return textCell(formatPercent(value, base), signStyle(sign(value)))
}

func (p *portfolioScreen) renderPositions() {
	p.sectionEmpty(&p.positions, p.data.positionsErr, "No open positions")
	var rows []row
	if p.data.positionsErr == nil {
		for _, group := range groupPositions(p.data.positions, p.data.instruments) {
			indent := ""
			// A lone position needs no group row.
			if len(group.positions) > 1 {
				marker := "▾"
				if p.collapsed[group.underlying] {
					marker = "▸"
				}
				name := fmt.Sprintf("%s %s (%d)", marker, displayName(p.data.instruments, group.underlying, string(group.underlying)), len(group.positions))
				var ltp cell
				if quote, ok := p.data.underlyings[string(group.underlying)]; ok {
					ltp = textCell(formatAmount(quote.LastPrice), styleYellow.Bold(true))
				}
				rows = append(rows, row{ref: group.underlying, cells: []cell{
					textCell(name, styleYellow.Bold(true)), {}, {}, {}, {}, ltp,
					textCell(formatSigned(group.pnl), signStyle(sign(group.pnl)).Bold(true)),
				}})
				if p.collapsed[group.underlying] {
					continue
				}
				indent = "  "
			}
			for _, position := range group.positions {
				rows = append(rows, row{ref: position, cells: []cell{
					textCell(indent+displayName(p.data.instruments, positionID(position), position.Tradingsymbol), styleCyan),
					textCell(position.Exchange, styleBlue),
					textCell(position.Product, styleMuted),
					textCell(fmt.Sprintf("%d", position.Quantity), quantityStyle(position.Quantity)),
					amountCell(position.AveragePrice),
					amountCell(position.LastPrice),
					signedCell(position.PnL),
				}})
			}
		}
	}
	p.positions.setRows(rows)
}

func (p *portfolioScreen) renderHoldings() {
	p.sectionEmpty(&p.holdings, p.data.holdingsErr, "No holdings")
	var rows []row
	if p.data.holdingsErr == nil {
		for _, holding := range p.data.holdings {
			quantity := float64(holding.Quantity + holding.T1Quantity)
			invested, current := holding.AveragePrice*quantity, holding.LastPrice*quantity
			rows = append(rows, row{ref: holding, cells: []cell{
				textCell(holding.Tradingsymbol, styleCyan),
				textCell(fmt.Sprintf("%.0f", quantity), styleText),
				amountCell(holding.AveragePrice),
				amountCell(holding.LastPrice),
				textCell(formatAmount(invested), styleMuted),
				amountCell(current),
				signedCell(current - invested),
				percentCell(current-invested, invested),
				percentCell(holding.DayChangePercentage, 100),
			}})
		}
	}
	p.holdings.setRows(rows)
}

func (p *portfolioScreen) renderFunds() {
	p.sectionEmpty(&p.funds, p.data.fundsErr, "No mutual fund holdings")
	var rows []row
	if p.data.fundsErr == nil {
		for _, fund := range p.data.funds {
			invested, current := fund.AveragePrice*fund.Quantity, fund.LastPrice*fund.Quantity
			rows = append(rows, row{ref: fund, cells: []cell{
				textCell(fallback(fund.Fund, fund.Tradingsymbol), styleCyan),
				textCell(fund.Folio, styleMuted),
				textCell(fmt.Sprintf("%.3f", fund.Quantity), styleText),
				amountCell(fund.AveragePrice),
				amountCell(fund.LastPrice),
				textCell(formatAmount(invested), styleMuted),
				amountCell(current),
				signedCell(current - invested),
				percentCell(current-invested, invested),
			}})
		}
	}
	p.funds.setRows(rows)
}

// portfolioPills are the status bar's user, mode and price state.
func (a *app) portfolioPills() []string {
	p := a.portfolio
	mode := "WEB"
	if p.user.IsAPIUser {
		mode = "API"
	}
	state := styleFaint.Render("not loaded")
	switch {
	case p.loading:
		state = a.spinner.View() + styleYellow.Render(" loading")
	case !p.data.pricedAt.IsZero():
		state = styleGreen.Render("● live " + p.data.pricedAt.Format("15:04:05"))
	case !p.data.loadedAt.IsZero():
		state = styleCyan.Render(p.data.loadedAt.Format("15:04:05"))
	}
	return []string{state, pill(p.user.UserID+" · "+mode, colorSelection, colorText)}
}

// statCard is a small boxed figure for the portfolio's summary row.
func statCard(title, value, sub string, w int) string {
	body := " " + value
	if sub != "" {
		body += "  " + sub
	}
	return panel(title, "", body, w, 3, false)
}

// marginCard shows the margin used of the total, equity and commodity
// together, coloured by how much is used, with what is free.
func marginCard(data portfolio, w int) string {
	switch {
	case data.marginsErr != nil:
		return statCard("Margin", styleRed.Render("failed"), "", w)
	case data.loadedAt.IsZero():
		return statCard("Margin", styleFaint.Render("—"), "", w)
	}
	used, free := marginUsed(data.margins)
	total := used + free
	style := styleGreen
	switch {
	case total > 0 && used/total >= 0.8:
		style = styleRed
	case total > 0 && used/total >= 0.5:
		style = styleYellow
	}
	value := style.Bold(true).Render("₹"+formatShort(used)) + styleMuted.Render("/"+formatShort(total))
	return statCard("Margin", value, styleFaint.Render("₹"+formatShort(free)+" free"), w)
}

// marginUsed is the margin blocked and the margin still available across
// segments.
func marginUsed(margins kite.AllMargins) (used, free float64) {
	for _, segment := range []kite.Margins{margins.Equity, margins.Commodity} {
		used += segment.Used.Debits
		free += segment.Net
	}
	return used, free
}

// formatShort abbreviates an amount the Indian way: 1.4K, 6.4L, 1.25Cr.
func formatShort(value float64) string {
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	trim := func(text string) string { return strings.TrimSuffix(strings.TrimRight(text, "0"), ".") }
	switch {
	case value >= 1e7:
		return sign + trim(fmt.Sprintf("%.2f", value/1e7)) + "Cr"
	case value >= 1e5:
		return sign + trim(fmt.Sprintf("%.1f", value/1e5)) + "L"
	case value >= 1e3:
		return sign + trim(fmt.Sprintf("%.1f", value/1e3)) + "K"
	}
	return sign + fmt.Sprintf("%.0f", value)
}

func (a *app) portfolioView(w, h int) string {
	p := a.portfolio
	data := p.data
	positions := sumPositions(data.positions)
	invested, current := sumHoldings(data.holdings)
	fundsInvested, fundsCurrent := sumFunds(data.funds)
	day := sumDayChange(data.holdings)
	widths := splitWidth(w, 1, 1, 1, 1, 1)
	name := p.user.UserID
	if p.user.UserName != "" {
		name += " · " + p.user.UserName
	}
	big := func(value float64) string { return signStyle(sign(value)).Bold(true).Render(formatSigned(value)) }
	cards := joinColumns(
		statCard("Positions P&L", big(positions), styleFaint.Render(fmt.Sprintf("%d open", len(data.positions))), widths[0]),
		statCard("Holdings P&L", big(current-invested), signStyle(sign(current-invested)).Render(formatPercent(current-invested, invested)), widths[1]),
		statCard("Day change", big(day), signStyle(sign(day)).Render(formatPercent(day, current-day)), widths[2]),
		statCard("Funds P&L", big(fundsCurrent-fundsInvested), signStyle(sign(fundsCurrent-fundsInvested)).Render(formatPercent(fundsCurrent-fundsInvested, fundsInvested)), widths[3]),
		marginCard(data, widths[4]),
	)
	rest := h - 3
	detailW := 43
	if w < detailMinWidth {
		detailW = 0
	}
	tableW := w - detailW
	heights := splitWidth(rest, 3, 3, 2, 3)
	top := 1 + 3 // tab bar and cards
	var tables []string
	groups := groupPositions(data.positions, data.instruments)
	titles := []struct{ title, info string }{
		{"Positions", fmt.Sprintf("%d in %d underlyings · P&L %s", len(data.positions), len(groups), signed(positions))},
		{"Holdings", fmt.Sprintf("%d · invested %s · current %s · P&L %s (%s)", len(data.holdings), formatAmount(invested), formatAmount(current), signed(current-invested), formatPercent(current-invested, invested))},
		{"Mutual funds", fmt.Sprintf("%d · invested %s · current %s · P&L %s", len(data.funds), formatAmount(fundsInvested), formatAmount(fundsCurrent), signed(fundsCurrent-fundsInvested))},
		{"Orders", ordersInfo(data)},
	}
	errs := []error{data.positionsErr, data.holdingsErr, data.fundsErr, data.ordersErr}
	for index, g := range p.grids() {
		g.x, g.y = 1, top+2
		focused := index == p.focus
		info := styleMuted.Render(titles[index].info)
		if errs[index] != nil {
			info = styleRed.Render("failed")
		}
		tables = append(tables, panel(titles[index].title, info, g.view(tableW-2, heights[index]-2, focused), tableW, heights[index], focused))
		top += heights[index]
	}
	left := strings.Join(tables, "\n")
	body := left
	if detailW > 0 {
		title, info, detail := "Details", styleFaint.Render(name), p.detail()
		if order, ok := p.focused().selectedRef().(kite.Order); ok && p.raw {
			title, info, detail = p.rawDetail(order, detailW-4, rest-2)
		}
		body = joinColumns(left, textPanel(title, info, detail, detailW, rest, false))
	}
	return cards + "\n" + body
}

// detail describes the focused table's selected row: a position, a group
// of positions, a holding or a fund.
func (p *portfolioScreen) detail() string {
	switch reference := p.focused().selectedRef().(type) {
	case kite.Position:
		return p.positionDetail(reference)
	case models.InstrumentID:
		return p.groupDetail(reference)
	case kite.Holding:
		return holdingDetail(reference)
	case kite.Order:
		return orderDetail(reference, p.data.orderCharges)
	case kite.MFHolding:
		return fundDetail(reference)
	}
	return styleFaint.Render("Select a row to see its details.")
}

// detailText builds a detail pane: a title, a subtitle and labelled
// sections.
type detailText struct {
	strings.Builder
}

func newDetailText(title, subtitle string) *detailText {
	text := &detailText{}
	text.WriteString(styleCyan.Bold(true).Render(title) + "\n" + styleMuted.Render(subtitle))
	return text
}

func (text *detailText) section(name string) {
	text.WriteString("\n\n" + styleBlue.Bold(true).Render(name))
}

// field adds a label and an already styled value.
func (text *detailText) field(label, value string) {
	text.WriteString("\n" + styleMuted.Render(padRight(label, 14)) + " " + value)
}

func (text *detailText) plain(label, value string) {
	text.field(label, styleText.Render(fallback(value, "—")))
}

func (text *detailText) line(value string) {
	text.WriteString("\n" + value)
}

func (p *portfolioScreen) positionDetail(position kite.Position) string {
	id := positionID(position)
	instrument, known := p.data.instruments[id]
	text := newDetailText(displayName(p.data.instruments, id, position.Tradingsymbol), string(id))

	text.section("Instrument")
	text.plain("ID", string(id))
	text.plain("Token", fmt.Sprintf("%d", position.InstrumentToken))
	if known {
		text.plain("Type", instrument.InstrumentType)
		text.plain("Segment", instrument.Segment)
		if instrument.Expiry != nil {
			text.plain("Expiry", formatExpiry(instrument.Expiry))
		}
		if instrument.Strike > 0 {
			text.plain("Strike", formatStrike(instrument.Strike))
		}
		if instrument.LotSize > 0 {
			text.plain("Lot size", fmt.Sprintf("%d", instrument.LotSize))
			if lots := float64(position.Quantity) / float64(instrument.LotSize); lots != 0 {
				text.plain("Lots", fmt.Sprintf("%g", lots))
			}
		}
		if instrument.UnderlyingID != nil {
			underlying := *instrument.UnderlyingID
			text.plain("Underlying", displayName(p.data.instruments, underlying, string(underlying)))
		}
	} else {
		text.field("Catalog", styleFaint.Render("not found"))
	}

	text.section("Position")
	text.plain("Product", position.Product)
	text.field("Net qty", quantityStyle(position.Quantity).Render(fmt.Sprintf("%d", position.Quantity)))
	text.plain("Overnight qty", fmt.Sprintf("%d", position.OvernightQuantity))
	if position.Multiplier != 0 && position.Multiplier != 1 {
		text.plain("Multiplier", fmt.Sprintf("%g", position.Multiplier))
	}
	text.plain("Avg price", formatAmount(position.AveragePrice))
	text.plain("LTP", formatAmount(position.LastPrice))
	text.plain("Prev close", formatAmount(position.ClosePrice))
	text.plain("Value", formatAmount(position.Value))
	text.field("P&L", signed(position.PnL))
	text.field("M2M", signed(position.M2M))
	text.field("Realised", signed(position.Realised))
	text.field("Unrealised", signed(position.Unrealised))

	text.section("Activity")
	text.field("Bought", trades(position.BuyQuantity, position.BuyPrice, position.BuyValue))
	text.field("Sold", trades(position.SellQuantity, position.SellPrice, position.SellValue))
	text.field("Bought today", trades(position.DayBuyQuantity, position.DayBuyPrice, position.DayBuyValue))
	text.field("Sold today", trades(position.DaySellQuantity, position.DaySellPrice, position.DaySellValue))
	return text.String()
}

func (p *portfolioScreen) groupDetail(underlying models.InstrumentID) string {
	var group positionGroup
	for _, candidate := range groupPositions(p.data.positions, p.data.instruments) {
		if candidate.underlying == underlying {
			group = candidate
		}
	}
	text := newDetailText(displayName(p.data.instruments, underlying, string(underlying)), string(underlying))
	var m2m, realised, unrealised float64
	for _, position := range group.positions {
		m2m += position.M2M
		realised += position.Realised
		unrealised += position.Unrealised
	}
	text.section("Underlying")
	text.plain("ID", string(underlying))
	if instrument, ok := p.data.instruments[underlying]; ok {
		if instrument.InstrumentType != "" {
			text.plain("Type", instrument.InstrumentType)
		}
		if instrument.Segment != "" {
			text.plain("Segment", instrument.Segment)
		}
		if instrument.LotSize > 1 {
			text.plain("Lot size", fmt.Sprintf("%d", instrument.LotSize))
		}
	}
	if quote, ok := p.data.underlyings[string(underlying)]; ok {
		text.plain("LTP", formatAmount(quote.LastPrice))
		if close := quote.OHLC.Close; close > 0 {
			change := quote.LastPrice - close
			text.field("Day change", signed(change)+" "+signStyle(sign(change)).Render(formatPercent(change, close)))
			text.plain("Prev close", formatAmount(close))
		}
		if ohlc := quote.OHLC; ohlc.Open > 0 {
			text.plain("Open", formatAmount(ohlc.Open))
			text.plain("High / Low", formatAmount(ohlc.High)+" / "+formatAmount(ohlc.Low))
		}
	}

	text.section("Positions")
	text.plain("Count", fmt.Sprintf("%d", len(group.positions)))
	text.field("P&L", signed(group.pnl))
	text.field("M2M", signed(m2m))
	text.field("Realised", signed(realised))
	text.field("Unrealised", signed(unrealised))

	text.section("Legs")
	for _, position := range group.positions {
		text.line(quantityStyle(position.Quantity).Render(fmt.Sprintf("%+d", position.Quantity)) + " " +
			styleText.Render(displayName(p.data.instruments, positionID(position), position.Tradingsymbol)) + "  " + signed(position.PnL))
	}
	text.WriteString("\n\n" + styleFaint.Render("Enter collapses or expands the group; X exits all of it."))
	return text.String()
}

func holdingDetail(holding kite.Holding) string {
	id := string(holdingID(holding))
	text := newDetailText(holding.Tradingsymbol, id)
	quantity := float64(holding.Quantity + holding.T1Quantity)
	invested, current := holding.AveragePrice*quantity, holding.LastPrice*quantity

	text.section("Instrument")
	text.plain("ID", id)
	text.plain("Token", fmt.Sprintf("%d", holding.InstrumentToken))
	text.plain("ISIN", holding.ISIN)
	text.plain("Product", holding.Product)

	text.section("Holding")
	text.plain("Quantity", fmt.Sprintf("%d", holding.Quantity))
	text.plain("T1 quantity", fmt.Sprintf("%d", holding.T1Quantity))
	if holding.CollateralQuantity > 0 {
		text.plain("Collateral", fmt.Sprintf("%d %s", holding.CollateralQuantity, holding.CollateralType))
	}
	text.plain("Avg price", formatAmount(holding.AveragePrice))
	text.plain("LTP", formatAmount(holding.LastPrice))
	text.plain("Prev close", formatAmount(holding.ClosePrice))
	text.plain("Invested", formatAmount(invested))
	text.plain("Current", formatAmount(current))
	text.field("P&L", fmt.Sprintf("%s (%s)", signed(current-invested), formatPercent(current-invested, invested)))
	text.field("Day change", fmt.Sprintf("%s (%s)", signed(holding.DayChange*quantity), formatPercent(holding.DayChangePercentage, 100)))
	return text.String()
}

func fundDetail(fund kite.MFHolding) string {
	text := newDetailText(fallback(fund.Fund, fund.Tradingsymbol), fund.Tradingsymbol)
	invested, current := fund.AveragePrice*fund.Quantity, fund.LastPrice*fund.Quantity
	text.section("Fund")
	text.plain("Symbol", fund.Tradingsymbol)
	text.plain("Folio", fund.Folio)
	text.plain("Units", fmt.Sprintf("%.3f", fund.Quantity))
	text.plain("Avg NAV", formatAmount(fund.AveragePrice))
	text.plain("NAV", formatAmount(fund.LastPrice))
	text.plain("NAV date", fund.LastPriceDate)
	text.plain("Invested", formatAmount(invested))
	text.plain("Current", formatAmount(current))
	text.field("P&L", fmt.Sprintf("%s (%s)", signed(current-invested), formatPercent(current-invested, invested)))
	return text.String()
}

// trades formats a quantity bought or sold at an average price, with the
// value shortened to fit the pane.
func trades(quantity int, price, value float64) string {
	if quantity == 0 {
		return styleFaint.Render("—")
	}
	return styleText.Render(fmt.Sprintf("%d @ %s (%s)", quantity, formatAmount(price), formatCount(value)))
}
