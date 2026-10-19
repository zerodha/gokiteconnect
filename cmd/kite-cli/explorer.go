package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/nse"
)

// explorerScreen explores the instrument catalog: F&O underlyings and
// search results, an underlying's futures and option chain, and lists, all
// priced from the data user's quotes.
type explorerScreen struct {
	started bool
	catalog catalog
	lists   listStore
	mode    viewMode
	status  string
	// focus is the focused table of the mode's grids.
	focus int

	search        textinput.Model
	searchFocused bool
	// searchGen drops searches a newer keystroke or view replaced.
	searchGen int

	results         grid
	resultItems     []models.Instrument
	underlyings     bool
	snapshot        dashboardSnapshot
	lastRefreshed   time.Time
	underlyingCount int

	chain         *chainData
	futures       grid
	chainGrid     grid
	chainNeedsATM bool
	// breakevenPercent shows breakevens as the move from spot.
	breakevenPercent bool

	listData      []models.List
	listsGrid     grid
	itemsGrid     grid
	masterIndices []nse.NSEIndexMetadata

	// Prices come from the data user's REST quotes.
	quoteSource quoteSource
	quoteLabel  string
	// tickerDown marks price mode ticker priced from quotes, the ticker
	// not running when prices connected.
	tickerDown    bool
	quoteErr      error
	quotes        map[string]kite.QuoteData
	quotesAt      time.Time
	quoteInFlight bool
	quoteAgain    bool
	connecting    bool

	// Margins are per lot, cached by contract and fetched once a contract
	// stays selected for marginDelay.
	margins         map[models.InstrumentID]lotMargin
	marginsInFlight map[models.InstrumentID]bool
	marginWanted    models.InstrumentID
}

func newExplorerScreen() *explorerScreen {
	search := newInput("", 40, false)
	search.Prompt = "⌕ "
	search.Placeholder = "Search symbol, name, token, underlying, call, put, futures…"
	styles := search.Styles()
	styles.Focused.Prompt = styleAccent
	styles.Blurred.Prompt = styleFaint
	search.SetStyles(styles)
	right := func(title string, width int) column { return column{title: title, width: width, align: alignRight} }
	e := &explorerScreen{
		search:           search,
		breakevenPercent: true,
		status:           statusInfo("Opening catalog…"),
		results: grid{columns: []column{
			{title: "SYMBOL", width: 22}, right("LTP", 10), right("CHG%", 8), {title: "TYPE", width: 5},
			{title: "EXPIRY", width: 11}, right("STRIKE", 9), right("OPT", 5), right("FUT", 4), right("LOT", 6),
			right("TOKEN", 10), {title: "NAME", width: 10, flex: 1},
		}},
		futures: grid{columns: []column{
			{title: "SYMBOL", width: 22}, {title: "EXPIRY", width: 11}, right("LTP", 10), right("CHG%", 8),
			right("LOT", 6), right("TOKEN", 10), {title: "DISPLAY NAME", width: 10, flex: 1},
		}, empty: styleMuted.Render("No futures for this expiry")},
		chainGrid: grid{groups: 2, group: 0, empty: styleYellow.Render("No contracts match these filters.")},
		listsGrid: grid{columns: []column{
			right("#", 4), {title: "NAME", width: 14, flex: 1}, right("ITEMS", 6), {title: "OWNER", width: 8},
			{title: "SOURCE", width: 10}, {title: "UPDATED", width: 17},
		}, empty: styleMuted.Render("No lists yet. Press N to create one or M to load NSE indices.")},
		itemsGrid: grid{columns: []column{
			{title: "INSTRUMENT", width: 16, flex: 1}, right("LTP", 10), right("CHG%", 8), {title: "ADDED", width: 17},
		}},
	}
	e.setChainColumns()
	return e
}

func (e *explorerScreen) setChainColumns() {
	be := "BE"
	if e.breakevenPercent {
		be = "BE%"
	}
	e.chainGrid.columns = []column{
		{title: "CALL OI", width: 10, flex: 1, align: alignRight, group: 0},
		{title: "CALL " + be, width: 9, align: alignRight, group: 0},
		{title: "CALL LTP", width: 9, align: alignRight, group: 0},
		{title: "STRIKE", width: 10, align: alignCenter, group: -1},
		{title: "PUT LTP", width: 9, group: 1},
		{title: "PUT " + be, width: 9, group: 1},
		{title: "PUT OI", width: 10, flex: 1, group: 1},
	}
}

func (e *explorerScreen) grids() []*grid {
	switch e.mode {
	case viewChain:
		return []*grid{&e.futures, &e.chainGrid}
	case viewLists:
		return []*grid{&e.listsGrid, &e.itemsGrid}
	}
	return []*grid{&e.results}
}

func (e *explorerScreen) focused() *grid {
	grids := e.grids()
	return grids[min(max(e.focus, 0), len(grids)-1)]
}

func (e *explorerScreen) focusGrid(index int) {
	e.focus = index
	e.blurSearch()
}

func (e *explorerScreen) blurSearch() {
	e.searchFocused = false
	e.search.Blur()
}

// modeName names what the explorer shows, for the status bar.
func (e *explorerScreen) modeName() string {
	switch {
	case e.mode == viewChain && e.chain != nil:
		return "OPTION CHAIN · " + string(e.chain.underlying)
	case e.mode == viewLists:
		return "LISTS"
	case !e.underlyings:
		return "SEARCH RESULTS"
	}
	return "F&O UNDERLYINGS"
}

type (
	catalogOpenedMsg struct {
		initial initialCatalog
		err     error
	}
	searchFireMsg struct{ generation int }
	searchDoneMsg struct {
		generation  int
		query       string
		underlyings bool
		results     []models.Instrument
		err         error
	}
	chainLoadedMsg struct {
		data chainData
		err  error
	}
	catalogRefreshedMsg struct {
		view refreshedView
		err  error
	}
	quotesConnectedMsg struct {
		source quoteSource
		label  string
		err    error
		fetch  bool
	}
	quotesMsg struct {
		quotes kite.Quote
		err    error
	}
	marginFireMsg struct{ id models.InstrumentID }
	marginMsg     struct {
		id     models.InstrumentID
		margin lotMargin
	}
	instrumentFoundMsg struct {
		instrument models.Instrument
		err        error
	}
)

// startExplorer opens the catalog and connects prices, the first time the
// explorer is shown.
func (a *app) startExplorer() tea.Cmd {
	a.explorer.started = true
	return tea.Batch(a.openCatalog(), a.connectQuotes(false))
}

func (a *app) openCatalog() tea.Cmd {
	open := a.deps.openCatalog
	return tea.Batch(a.busy("Opening the catalog and refreshing it if stale…"), a.task(func(ctx context.Context) tea.Msg {
		initial, err := loadInitialCatalog(ctx, open)
		return catalogOpenedMsg{initial: initial, err: err}
	}))
}

func (a *app) handleExplorer(msg tea.Msg) (tea.Cmd, bool) {
	e := a.explorer
	switch msg := msg.(type) {
	case catalogOpenedMsg:
		a.closeOverlay()
		if msg.err != nil {
			a.overlay = &dialog{
				title:    "Could not open the instrument catalog",
				text:     styleRed.Render(msg.err.Error()),
				danger:   true,
				noCancel: true,
				buttons: []dialogButton{
					{label: "Retry", run: func(a *app) tea.Cmd { return a.openCatalog() }},
					{label: "Quit", run: func(a *app) tea.Cmd { a.cancel(); return tea.Quit }},
				},
			}
			return nil, true
		}
		e.catalog, e.lists = msg.initial.catalog, msg.initial.lists
		e.lastRefreshed = msg.initial.lastRefreshed
		e.underlyingCount = len(msg.initial.underlyings)
		cmd := a.setResults(msg.initial.underlyings, true)
		e.status = styleGreen.Bold(true).Render("READY") + statusInfo(fmt.Sprintf(" · %d F&O underlyings", len(msg.initial.underlyings)))
		return cmd, true
	case searchFireMsg:
		if msg.generation != e.searchGen {
			return nil, true
		}
		return a.runSearch(), true
	case searchDoneMsg:
		if msg.generation != e.searchGen {
			return nil, true
		}
		if msg.err != nil {
			a.showError(msg.err)
			return nil, true
		}
		if msg.underlyings {
			e.underlyingCount = len(msg.results)
		}
		cmd := a.setResults(msg.results, msg.underlyings)
		if msg.underlyings {
			e.status = statusInfo(fmt.Sprintf("%d F&O underlyings", len(msg.results)))
		} else {
			e.status = statusInfo(fmt.Sprintf("%d ranked results for ", len(msg.results))) + styleYellow.Render(msg.query)
		}
		return cmd, true
	case chainLoadedMsg:
		a.closeOverlay()
		if msg.err != nil {
			a.showError(msg.err)
			return nil, true
		}
		data := msg.data
		e.chain = &data
		e.chainNeedsATM = true
		e.mode = viewChain
		e.focus = 1
		e.chainGrid.cursor, e.chainGrid.offset, e.chainGrid.group = 0, 0, 0
		return a.renderChain(), true
	case catalogRefreshedMsg:
		a.closeOverlay()
		if msg.err != nil {
			a.showError(msg.err)
			return nil, true
		}
		view := msg.view
		e.lastRefreshed = view.lastRefreshed
		e.underlyingCount = view.underlyingCount
		var cmd tea.Cmd
		if view.chain != nil {
			e.chain = view.chain
			cmd = a.renderChain()
		} else {
			e.search.SetValue(view.query)
			cmd = a.setResults(view.results, view.underlyings)
			e.selectResult(view.selectedID)
		}
		e.status = statusOK("Catalog refreshed from Kite.")
		return cmd, true
	case quotesConnectedMsg:
		e.connecting = false
		e.quoteSource, e.quoteLabel, e.quoteErr = msg.source, msg.label, msg.err
		e.tickerDown = false
		var unavailable tickerUnavailableError
		switch {
		case msg.source != nil && errors.As(msg.err, &unavailable):
			// Price mode ticker without a running ticker: quotes instead.
			e.quoteErr, e.tickerDown = nil, true
			e.status = statusErr("Ticker unavailable, using quotes:", unavailable.err.Error()+" · R retries")
		case msg.err != nil:
			e.status = statusErr("Prices off:", msg.err.Error())
			return nil, true
		}
		if msg.fetch || e.catalog != nil {
			return a.refreshQuotes(), true
		}
		return nil, true
	case quotesMsg:
		e.quoteInFlight = false
		e.quoteErr = msg.err
		if msg.err != nil {
			e.status = statusErr("Quote request failed:", quoteErrorText(msg.err))
		} else {
			if e.quotes == nil {
				e.quotes = make(map[string]kite.QuoteData, len(msg.quotes))
			}
			for id, quote := range msg.quotes {
				e.quotes[id] = quote
			}
			e.quotesAt = time.Now()
		}
		cmd := a.applyQuotes()
		if e.quoteAgain {
			e.quoteAgain = false
			cmd = tea.Batch(cmd, a.refreshQuotes())
		}
		return cmd, true
	case marginFireMsg:
		if e.marginWanted != msg.id {
			delete(e.marginsInFlight, msg.id)
			return nil, true
		}
		return a.fetchMargin(msg.id), true
	case marginMsg:
		delete(e.marginsInFlight, msg.id)
		if e.margins == nil {
			e.margins = make(map[models.InstrumentID]lotMargin)
		}
		e.margins[msg.id] = msg.margin
		if selected, ok := e.selectedInstrument(); ok && selected.ID == msg.id {
			return a.updateChainSummary(), true
		}
		return nil, true
	case instrumentFoundMsg:
		if msg.err != nil {
			e.status = statusErr("Cannot order:", msg.err.Error())
			return nil, true
		}
		return a.instrumentOrder(msg.instrument, e.quotes[string(msg.instrument.ID)].LastPrice), true
	}
	return a.handleLists(msg)
}

// setResults shows instruments on the dashboard, selecting the first.
func (a *app) setResults(values []models.Instrument, underlyings bool) tea.Cmd {
	e := a.explorer
	e.mode = viewDashboard
	e.focus = 0
	e.chain = nil
	e.underlyings = underlyings
	e.resultItems = append(e.resultItems[:0], values...)
	e.results.cursor, e.results.offset = 0, 0
	e.renderResults()
	return a.refreshQuotes()
}

func (e *explorerScreen) selectResult(id models.InstrumentID) {
	e.results.selectWhere(func(r row) bool {
		instrument, _ := r.ref.(models.Instrument)
		return instrument.ID == id
	})
}

// priceCells are an instrument's last price and change.
func (e *explorerScreen) priceCells(id models.InstrumentID) (cell, cell) {
	quote, ok := e.quotes[string(id)]
	if !ok {
		return textCell("—", styleFaint), textCell("", styleFaint)
	}
	change, direction := formatChange(quote)
	return textCell(formatPrice(quote.LastPrice), styleText), textCell(change, signStyle(direction))
}

func (e *explorerScreen) renderResults() {
	if e.underlyings {
		e.results.empty = styleMuted.Render("No F&O underlyings in the catalog · press C to refresh it")
	} else {
		e.results.empty = styleYellow.Render("No instruments found") + styleMuted.Render(" · try a symbol, company name, token, underlying, or contract description")
	}
	rows := make([]row, 0, len(e.resultItems))
	for _, instrument := range e.resultItems {
		ltp, change := e.priceCells(instrument.ID)
		strike := ""
		if instrument.Strike > 0 {
			strike = formatStrike(instrument.Strike)
		}
		expiry := ""
		if instrument.Expiry != nil {
			expiry = formatExpiry(instrument.Expiry)
		}
		rows = append(rows, row{ref: instrument, cells: []cell{
			textCell(instrument.TradingSymbol, styleCyan),
			ltp, change,
			textCell(instrument.InstrumentType, instrumentTypeStyle(instrument.InstrumentType)),
			textCell(expiry, styleMuted),
			textCell(strike, styleYellow),
			textCell(strconv.Itoa(instrument.OptionsCount), styleGreen),
			textCell(strconv.Itoa(instrument.FuturesCount), styleYellow),
			textCell(strconv.Itoa(instrument.LotSize), styleBlue),
			textCell(strconv.FormatInt(instrument.InstrumentToken, 10), styleKey.UnsetBold()),
			textCell(instrument.DisplayName, styleText),
		}})
	}
	e.results.setRows(rows)
}

// searchChanged debounces a search for the typed text.
func (a *app) searchChanged() tea.Cmd {
	e := a.explorer
	if e.catalog == nil {
		return nil
	}
	e.searchGen++
	query := strings.TrimSpace(e.search.Value())
	if query == "" {
		e.status = statusInfo("Loading F&O underlyings…")
	} else {
		e.status = statusInfo("Searching for ") + styleYellow.Render(query) + statusInfo("…")
	}
	generation := e.searchGen
	return tea.Tick(searchDelay, func(time.Time) tea.Msg { return searchFireMsg{generation: generation} })
}

// runSearch searches the catalog for the search text, or lists the F&O
// underlyings when it is empty.
func (a *app) runSearch() tea.Cmd {
	e := a.explorer
	catalog, generation := e.catalog, e.searchGen
	query := strings.TrimSpace(e.search.Value())
	return a.task(func(ctx context.Context) tea.Msg {
		done := searchDoneMsg{generation: generation, query: query, underlyings: query == ""}
		if done.underlyings {
			done.results, done.err = catalog.ListFO(ctx)
		} else {
			done.results, done.err = catalog.Search(ctx, query, searchLimit)
		}
		return done
	})
}

func (a *app) showUnderlyings() tea.Cmd {
	e := a.explorer
	if e.catalog == nil {
		return nil
	}
	e.chain = nil
	e.search.SetValue("")
	e.searchGen++
	e.status = statusInfo("Loading F&O underlyings…")
	return a.runSearch()
}

func (a *app) focusSearch() {
	e := a.explorer
	if e.mode != viewDashboard {
		a.backToDashboard()
	}
	e.searchFocused = true
	e.search.Focus()
}

func (a *app) backToDashboard() tea.Cmd {
	e := a.explorer
	e.chain = nil
	e.search.SetValue(e.snapshot.query)
	cmd := a.setResults(e.snapshot.results, strings.TrimSpace(e.snapshot.query) == "")
	e.selectResult(e.snapshot.selectedID)
	e.status = statusInfo("Returned to instrument results.")
	return cmd
}

func (e *explorerScreen) takeSnapshot() {
	e.snapshot = dashboardSnapshot{
		query:   e.search.Value(),
		results: append([]models.Instrument(nil), e.resultItems...),
	}
	if selected, ok := e.results.selectedRef().(models.Instrument); ok {
		e.snapshot.selectedID = selected.ID
	}
}

func (a *app) openChain(instrument models.Instrument) tea.Cmd {
	e := a.explorer
	underlying, ok := drilldownUnderlying(instrument)
	if !ok {
		e.status = statusWarn("This instrument has no futures or options to explore.")
		return nil
	}
	if e.mode == viewDashboard {
		e.takeSnapshot()
		e.snapshot.selectedID = instrument.ID
	}
	e.searchGen++
	catalog := e.catalog
	return tea.Batch(a.busy(fmt.Sprintf("Loading contracts for %s…", underlying)), a.task(func(ctx context.Context) tea.Msg {
		data, err := loadChainData(ctx, catalog, underlying)
		return chainLoadedMsg{data: data, err: err}
	}))
}

// renderChain fills the futures and the chain from the chain data and the
// quotes, then fetches fresh quotes.
func (a *app) renderChain() tea.Cmd {
	a.explorer.fillChain()
	return tea.Batch(a.updateChainSummary(), a.refreshQuotes())
}

func (e *explorerScreen) spot() float64 {
	if e.chain == nil {
		return 0
	}
	return e.quotes[string(e.chain.underlying)].LastPrice
}

var (
	styleATM     = lipgloss.NewStyle().Background(lipgloss.Color("#B8860B")).Foreground(colorInk).Bold(true)
	styleNoTrade = lipgloss.NewStyle().Foreground(colorFaint)
)

func (e *explorerScreen) fillChain() {
	data := e.chain
	if data == nil {
		return
	}
	var futures []row
	for _, future := range filterFutures(data.futures, data.filter) {
		ltp, change := e.priceCells(future.ID)
		futures = append(futures, row{ref: future, cells: []cell{
			textCell(future.TradingSymbol, styleCyan),
			textCell(formatExpiry(future.Expiry), styleMuted),
			ltp, change,
			textCell(strconv.Itoa(future.LotSize), styleBlue),
			textCell(strconv.FormatInt(future.InstrumentToken, 10), styleMuted),
			textCell(future.DisplayName, styleText),
		}})
	}
	e.futures.setRows(futures)

	chainRows := buildChainRows(data.options, data.filter)
	spot := e.spot()
	atm := math.NaN()
	if spot > 0 {
		for _, candidate := range chainRows {
			if math.IsNaN(atm) || math.Abs(candidate.strike-spot) < math.Abs(atm-spot) {
				atm = candidate.strike
			}
		}
	}
	var largest float64
	for _, candidate := range chainRows {
		for _, leg := range []*models.Instrument{candidate.call, candidate.put} {
			if leg != nil {
				largest = max(largest, e.quotes[string(leg.ID)].OI)
			}
		}
	}
	rows := make([]row, 0, len(chainRows))
	atmRow := -1
	for index, candidate := range chainRows {
		cells := make([]cell, 7)
		refs := make([]any, 2)
		for side, leg := range []*models.Instrument{candidate.call, candidate.put} {
			// The call's cells run OI, BE, LTP towards the strike; the put's
			// LTP, BE, OI away from it.
			ltpAt, beAt, oiAt := 2, 1, 0
			ltpStyle, barColor := styleGreen, colorCallBar
			if side == 1 {
				ltpAt, beAt, oiAt = 4, 5, 6
				ltpStyle, barColor = styleRed, colorPutBar
			}
			if leg == nil {
				for _, at := range []int{ltpAt, beAt, oiAt} {
					cells[at] = textCell("—", styleNoTrade)
				}
				continue
			}
			refs[side] = *leg
			cells[ltpAt], cells[beAt], cells[oiAt] = textCell("—", ltpStyle), textCell("—", styleBlue), textCell("—", styleMuted)
			if quote, ok := e.quotes[string(leg.ID)]; ok {
				cells[ltpAt].text = formatPrice(quote.LastPrice)
				cells[oiAt] = cell{text: formatCount(quote.OI), style: styleText, barColor: barColor, barRight: side == 0}
				if largest > 0 {
					cells[oiAt].bar = quote.OI / largest
				}
				if quote.LastPrice > 0 {
					cells[beAt].text = formatBreakeven(breakeven(*leg, quote.LastPrice), spot, e.breakevenPercent)
				}
			}
		}
		strikeStyle := styleYellow
		if candidate.strike == atm {
			strikeStyle = styleATM
			atmRow = index
		}
		cells[3] = textCell(formatStrike(candidate.strike), strikeStyle)
		rows = append(rows, row{cells: cells, refs: refs})
	}
	e.chainGrid.setRows(rows)
	if e.chainNeedsATM && atmRow >= 0 {
		e.chainNeedsATM = false
		e.chainGrid.cursor = atmRow
		e.chainGrid.settleGroup()
		e.chainGrid.centerNext = true
	}
	if len(rows) == 0 && len(futures) > 0 {
		e.focus = 0
	}
}

// chainTitle is the underlying with its price, the expiry and the side.
func (e *explorerScreen) chainTitle() string {
	data := e.chain
	title := string(data.underlying)
	if quote, ok := e.quotes[string(data.underlying)]; ok {
		change, direction := formatChange(quote)
		title += " " + formatPrice(quote.LastPrice) + " " + signStyle(direction).Render(change)
	}
	return title + styleMuted.Render(fmt.Sprintf(" · %s · %s", data.expiry().date.Format("02 Jan 2006"), data.filter.side))
}

func (a *app) cycleExpiry(delta int) tea.Cmd {
	e := a.explorer
	if e.mode != viewChain || e.chain == nil || len(e.chain.expiries) == 0 {
		return nil
	}
	index := 0
	for i, expiry := range e.chain.expiries {
		if expiry.number == e.chain.filter.expiryNumber {
			index = i
		}
	}
	index = (index + delta + len(e.chain.expiries)) % len(e.chain.expiries)
	return a.setExpiry(e.chain.expiries[index].number)
}

func (a *app) setExpiry(number int) tea.Cmd {
	e := a.explorer
	e.chain.filter.expiryNumber = number
	e.chainNeedsATM = true
	e.chainGrid.cursor, e.chainGrid.offset = 0, 0
	return a.renderChain()
}

func (a *app) openExpiryPicker() {
	e := a.explorer
	if e.mode != viewChain || e.chain == nil {
		return
	}
	picker := &menu{title: "Select expiry", width: 36}
	today := expiryDay(time.Now())
	for index, expiry := range e.chain.expiries {
		number := expiry.number
		label := styleYellow.Render(fmt.Sprintf("#%-2d", expiry.number)) + "  " + styleText.Render(expiry.date.Format("02 Jan 2006")) +
			"  " + styleFaint.Render(daysLeft(expiryDay(expiry.date), today))
		picker.items = append(picker.items, menuItem{label: label, run: func(a *app) tea.Cmd { return a.setExpiry(number) }})
		if number == e.chain.filter.expiryNumber {
			picker.cursor = index
		}
	}
	a.overlay = picker
}

// filterOverlay edits the chain's side and strike range.
type filterOverlay struct {
	form *form
}

func (a *app) openFilters() {
	e := a.explorer
	if e.mode != viewChain || e.chain == nil {
		return
	}
	filter := e.chain.filter
	f := newForm(16, 18, "Apply", "Reset", "Cancel")
	f.addSelect("Contracts", []string{optionSideBoth.String(), optionSideCalls.String(), optionSidePuts.String()}, int(filter.side))
	f.addInput("Minimum strike", optionalFloat(filter.minStrike), decimalNumber, false)
	f.addInput("Maximum strike", optionalFloat(filter.maxStrike), decimalNumber, false)
	f.focusFirst()
	a.overlay = &filterOverlay{form: f}
}

func optionalFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return formatStrike(*value)
}

func (o *filterOverlay) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		a.closeOverlay()
		return nil
	}
	pressed, _ := o.form.update(msg)
	e := a.explorer
	switch pressed {
	case 0:
		minStrike, maxStrike, err := parseStrikeRange(o.form.field("Minimum strike").value(), o.form.field("Maximum strike").value())
		if err != nil {
			o.form.message = styleRed.Render(err.Error())
			return nil
		}
		e.chain.filter.minStrike, e.chain.filter.maxStrike = minStrike, maxStrike
		e.chain.filter.side = optionSide(o.form.field("Contracts").index)
	case 1:
		e.chain.filter.minStrike, e.chain.filter.maxStrike, e.chain.filter.side = nil, nil, optionSideBoth
	case 2:
		a.closeOverlay()
		return nil
	default:
		return nil
	}
	a.closeOverlay()
	e.chainNeedsATM = true
	return a.renderChain()
}

func (o *filterOverlay) view(_ *app, w, _ int) string {
	width := min(56, w-2)
	return modal("Option filters", "", o.form.view(width-4), width)
}

func (a *app) toggleBreakevenFormat() tea.Cmd {
	e := a.explorer
	if e.mode != viewChain || e.chain == nil {
		return nil
	}
	e.breakevenPercent = !e.breakevenPercent
	e.setChainColumns()
	e.fillChain()
	return nil
}

// refreshCatalog downloads the whole catalog again and reloads the view; in
// lists it refreshes the selected list instead.
func (a *app) refreshCatalog() tea.Cmd {
	e := a.explorer
	if e.catalog == nil {
		return nil
	}
	if e.mode == viewLists {
		return a.refreshSelectedList()
	}
	e.searchGen++
	catalog, mode := e.catalog, e.mode
	query := strings.TrimSpace(e.search.Value())
	var selectedID models.InstrumentID
	if selected, ok := e.results.selectedRef().(models.Instrument); ok {
		selectedID = selected.ID
	}
	var underlying models.InstrumentID
	var filter chainFilter
	if e.chain != nil {
		underlying, filter = e.chain.underlying, e.chain.filter
	}
	return tea.Batch(a.busy("Refreshing the complete Kite instrument catalog…"), a.task(func(ctx context.Context) tea.Msg {
		view, err := reloadAfterRefresh(ctx, catalog, mode, query, selectedID, underlying, filter)
		return catalogRefreshedMsg{view: view, err: err}
	}))
}

// selectedInstrument is the dashboard's or the chain's selected instrument.
func (e *explorerScreen) selectedInstrument() (models.Instrument, bool) {
	if e.mode == viewLists {
		return models.Instrument{}, false
	}
	instrument, ok := e.focused().selectedRef().(models.Instrument)
	return instrument, ok
}

// explorerSelectionChanged follows a new selection: the chain's status, or
// a list's items.
func (a *app) explorerSelectionChanged() tea.Cmd {
	e := a.explorer
	switch {
	case e.mode == viewChain:
		return a.updateChainSummary()
	case e.mode == viewLists && e.focus == 0:
		e.renderListItems(true)
		return a.refreshQuotes()
	}
	return nil
}

func (a *app) explorerKey(msg tea.KeyPressMsg) tea.Cmd {
	e := a.explorer
	key := msg.String()
	if e.searchFocused {
		switch key {
		case "esc":
			e.blurSearch()
			return nil
		case "enter", "down":
			e.blurSearch()
			e.focus = 0
			return nil
		}
		before := e.search.Value()
		var cmd tea.Cmd
		e.search, cmd = e.search.Update(msg)
		if e.search.Value() != before {
			return tea.Batch(cmd, a.searchChanged())
		}
		return cmd
	}
	if e.catalog == nil {
		if key == "esc" || key == "q" || key == "Q" {
			a.leaveInstruments()
		}
		return nil
	}
	if key == "esc" || key == "q" || key == "Q" {
		if e.mode == viewDashboard {
			a.leaveInstruments()
			return nil
		}
		return a.backToDashboard()
	}
	if e.mode == viewLists {
		if cmd, handled := a.listKey(key); handled {
			return cmd
		}
	}
	if e.focused().update(key) {
		return a.explorerSelectionChanged()
	}
	switch key {
	case "[":
		return a.cycleExpiry(-1)
	case "]":
		return a.cycleExpiry(1)
	case "%":
		return a.toggleBreakevenFormat()
	case "/":
		a.focusSearch()
		return nil
	case "tab", "shift+tab":
		if e.mode == viewChain {
			e.focus = 1 - e.focus
			return a.explorerSelectionChanged()
		}
		return nil
	case "enter":
		switch e.mode {
		case viewDashboard:
			if instrument, ok := e.selectedInstrument(); ok {
				return a.openChain(instrument)
			}
		case viewChain:
			return a.orderSelected()
		}
		return nil
	}
	switch strings.ToLower(key) {
	case "u":
		return a.showUnderlyings()
	case "l":
		return a.openLists()
	case "a":
		return a.addSelectedToList()
	case "e":
		a.openExpiryPicker()
	case "f":
		a.openFilters()
	case "r":
		return a.reloadQuotes()
	case "c":
		return a.refreshCatalog()
	case "o":
		return a.orderSelected()
	case "h":
		id, _ := e.selectedInstrumentID()
		return a.openHistory(id)
	}
	return nil
}

// orderSelected opens the order menu for the selected instrument: a search
// result, a chain contract or future, or a list item.
func (a *app) orderSelected() tea.Cmd {
	e := a.explorer
	if instrument, ok := e.selectedInstrument(); ok {
		return a.instrumentOrder(instrument, e.quotes[string(instrument.ID)].LastPrice)
	}
	if id, ok := e.itemsGrid.selectedRef().(models.InstrumentID); ok && e.mode == viewLists && e.focus == 1 {
		catalog := e.catalog
		return a.task(func(ctx context.Context) tea.Msg {
			instrument, err := catalog.Get(ctx, id)
			return instrumentFoundMsg{instrument: instrument, err: err}
		})
	}
	e.status = statusWarn("Select an instrument to trade")
	return nil
}

// connectQuotes opens the data user's quote source, then fetches prices
// when fetch is set or the catalog is open.
func (a *app) connectQuotes(fetch bool) tea.Cmd {
	load := a.deps.loadQuotes
	if load == nil {
		return nil
	}
	a.explorer.connecting = true
	return a.task(func(ctx context.Context) tea.Msg {
		source, label, err := load(ctx)
		return quotesConnectedMsg{source: source, label: label, err: err, fetch: fetch}
	})
}

// reloadQuotes reconnects the data user, picking up a refreshed session,
// and fetches prices.
func (a *app) reloadQuotes() tea.Cmd {
	if a.deps.loadQuotes == nil {
		a.explorer.status = statusInfo("Prices are off (started with ") + styleYellow.Render("-no-quotes") + statusInfo(").")
		return nil
	}
	a.explorer.margins = nil
	return a.connectQuotes(true)
}

// refreshQuotes fetches the visible instruments' quotes, once more after
// the request in flight when one is.
func (a *app) refreshQuotes() tea.Cmd {
	e := a.explorer
	if e.quoteSource == nil {
		return nil
	}
	ids := e.visibleQuoteIDs()
	if len(ids) == 0 {
		return nil
	}
	if e.quoteInFlight {
		e.quoteAgain = true
		return nil
	}
	e.quoteInFlight = true
	source := e.quoteSource
	return a.task(func(context.Context) tea.Msg {
		quotes, err := source.GetQuote(ids...)
		return quotesMsg{quotes: quotes, err: err}
	})
}

func (e *explorerScreen) visibleQuoteIDs() []string {
	var ids []string
	switch e.mode {
	case viewDashboard:
		for _, instrument := range e.resultItems {
			ids = append(ids, string(instrument.ID))
		}
	case viewChain:
		if e.chain == nil {
			return nil
		}
		ids = append(ids, string(e.chain.underlying))
		for _, future := range filterFutures(e.chain.futures, e.chain.filter) {
			ids = append(ids, string(future.ID))
		}
		for _, candidate := range buildChainRows(e.chain.options, e.chain.filter) {
			for _, leg := range []*models.Instrument{candidate.call, candidate.put} {
				if leg != nil {
					ids = append(ids, string(leg.ID))
				}
			}
		}
	case viewLists:
		if list, ok := e.selectedList(); ok {
			for _, id := range list.InstrumentIDs() {
				ids = append(ids, string(id))
			}
		}
	}
	return ids
}

// applyQuotes reprices the visible tables.
func (a *app) applyQuotes() tea.Cmd {
	e := a.explorer
	switch e.mode {
	case viewDashboard:
		e.renderResults()
	case viewChain:
		e.fillChain()
		return a.updateChainSummary()
	case viewLists:
		e.renderListItems(false)
	}
	return nil
}

// updateChainSummary shows the selected contract's lot, premium and margin
// in the status bar, fetching the margin when it is missing or stale.
func (a *app) updateChainSummary() tea.Cmd {
	e := a.explorer
	if e.mode != viewChain || e.chain == nil {
		return nil
	}
	hint := styleFaint.Render(" · ") + styleKey.Render("%") + styleMuted.Render(" BE format · ") + styleKey.Render("[ / ]") + styleMuted.Render(" expiry")
	instrument, ok := e.selectedInstrument()
	if !ok {
		e.status = statusInfo("Select a contract to see its premium and margin per lot") + hint
		return nil
	}
	lot := max(instrument.LotSize, 1)
	price := math.NaN()
	if quote, ok := e.quotes[string(instrument.ID)]; ok && quote.LastPrice > 0 {
		price = quote.LastPrice
	}
	value := styleFaint.Render("—")
	if !math.IsNaN(price) {
		value = styleBold.Render("₹" + formatAmount(price*float64(lot)))
	}
	margin, cmd := a.marginText(instrument)
	dot := styleFaint.Render(" · ")
	text := styleCyan.Bold(true).Render(instrument.TradingSymbol) + dot + styleMuted.Render(fmt.Sprintf("lot %d", lot))
	if isOption(instrument) {
		text += dot + styleMuted.Render("premium/lot ") + value + dot + styleMuted.Render("sell margin/lot ") + margin
		if !math.IsNaN(price) {
			text += dot + styleMuted.Render("BE ") + styleText.Render(formatPrice(breakeven(instrument, price)))
			if spot := e.spot(); spot > 0 {
				text += styleMuted.Render(" (" + formatBreakeven(breakeven(instrument, price), spot, true) + ")")
			}
		}
	} else {
		text += dot + styleMuted.Render("value/lot ") + value + dot + styleMuted.Render("margin/lot ") + margin
	}
	e.status = text + hint
	return cmd
}

// marginText is the cached margin of a lot of instrument, and the command
// that fetches it when it is missing or stale.
func (a *app) marginText(instrument models.Instrument) (string, tea.Cmd) {
	e := a.explorer
	if _, ok := e.quoteSource.(marginSource); !ok {
		return styleFaint.Render("—"), nil
	}
	margin, cached := e.margins[instrument.ID]
	var cmd tea.Cmd
	if !cached || time.Since(margin.fetchedAt) > marginTTL {
		e.marginWanted = instrument.ID
		if !e.marginsInFlight[instrument.ID] {
			if e.marginsInFlight == nil {
				e.marginsInFlight = make(map[models.InstrumentID]bool)
			}
			e.marginsInFlight[instrument.ID] = true
			id := instrument.ID
			cmd = tea.Tick(marginDelay, func(time.Time) tea.Msg { return marginFireMsg{id: id} })
		}
	}
	switch {
	case !cached:
		return styleFaint.Render("loading…"), cmd
	case margin.err != nil:
		return styleRed.Render(quoteErrorText(margin.err)), cmd
	}
	return styleBold.Render("₹" + formatAmount(margin.total)), cmd
}

// fetchMargin asks Kite for the margin of a lot of the selected contract.
func (a *app) fetchMargin(id models.InstrumentID) tea.Cmd {
	e := a.explorer
	source, ok := e.quoteSource.(marginSource)
	instrument, selected := e.selectedInstrument()
	if !ok || !selected || instrument.ID != id {
		delete(e.marginsInFlight, id)
		return nil
	}
	params := marginParams(instrument)
	return a.task(func(context.Context) tea.Msg {
		margins, err := source.GetOrderMargins(params)
		margin := lotMargin{err: err, fetchedAt: time.Now()}
		if err == nil {
			if len(margins) == 0 {
				margin.err = fmt.Errorf("no margin returned for %s", id)
			} else {
				margin.total = margins[0].Total
			}
		}
		return marginMsg{id: id, margin: margin}
	})
}

// quoteHeader describes the price feed for the status bar.
func (a *app) quoteHeader() string {
	e := a.explorer
	switch {
	case a.deps.loadQuotes == nil:
		return styleFaint.Render("prices off")
	case e.quoteSource == nil && e.quoteErr != nil:
		return styleRed.Render("prices off")
	case e.quoteSource == nil:
		return styleFaint.Render("prices connecting…")
	case e.quoteErr != nil:
		return styleRed.Render("prices " + e.quoteLabel + " · failed")
	}
	header := "● prices " + e.quoteLabel
	if !e.quotesAt.IsZero() {
		header += " · " + e.quotesAt.Format("15:04:05")
	}
	if e.tickerDown {
		return styleGreen.Render(header) + styleYellow.Render(" · ticker down")
	}
	return styleGreen.Render(header)
}

// instrumentDetail describes an instrument and, with prices on, its quote.
func (e *explorerScreen) instrumentDetail(instrument models.Instrument) string {
	expiryNumber := "—"
	if instrument.ExpiryNumber != nil {
		expiryNumber = strconv.Itoa(*instrument.ExpiryNumber)
	}
	yes := func(value bool) string {
		if value {
			return "Yes"
		}
		return "No"
	}
	text := newDetailText(string(instrument.ID), instrument.DisplayName)
	text.section("Identity")
	text.plain("Exchange", instrument.Exchange)
	text.plain("Symbol", instrument.TradingSymbol)
	text.plain("Display name", instrument.DisplayName)
	text.plain("Name", pointerText(instrument.Name))
	text.plain("Token", strconv.FormatInt(instrument.InstrumentToken, 10))
	text.plain("Exchange token", instrument.ExchangeToken)
	text.section("Contract")
	text.field("Type", instrumentTypeStyle(instrument.InstrumentType).Render(fallback(instrument.InstrumentType, "—")))
	text.plain("Segment", instrument.Segment)
	text.plain("Expiry", formatExpiry(instrument.Expiry))
	text.plain("Expiry number", expiryNumber)
	text.plain("Strike", formatStrike(instrument.Strike))
	text.plain("Tick size", formatStrike(instrument.TickSize))
	text.plain("Lot size", strconv.Itoa(instrument.LotSize))
	text.plain("F&O contract", yes(instrument.IsFO))
	text.section("Underlying")
	text.plain("ID", pointerText(instrument.UnderlyingID))
	text.plain("Listed", yes(instrument.UnderlyingIsListed))
	text.plain("Options", strconv.Itoa(instrument.OptionsCount))
	text.plain("Futures", strconv.Itoa(instrument.FuturesCount))
	if e.quoteSource == nil {
		return text.String()
	}
	quote, ok := e.quotes[string(instrument.ID)]
	if !ok {
		text.section("Market")
		text.line(styleFaint.Render("No quote yet · R to refresh"))
		return text.String()
	}
	change, direction := formatChange(quote)
	bid, ask := "—", "—"
	if level := quote.Depth.Buy[0]; level.Price > 0 {
		bid = fmt.Sprintf("%s × %d", formatPrice(level.Price), level.Quantity)
	}
	if level := quote.Depth.Sell[0]; level.Price > 0 {
		ask = fmt.Sprintf("%s × %d", formatPrice(level.Price), level.Quantity)
	}
	text.section("Market " + styleFaint.Render(e.quotesAt.Format("15:04:05")))
	text.field("LTP", styleBold.Render(formatPrice(quote.LastPrice))+" "+signStyle(direction).Render(change))
	text.plain("Open / High", formatPrice(quote.OHLC.Open)+" / "+formatPrice(quote.OHLC.High))
	text.plain("Low / Prev", formatPrice(quote.OHLC.Low)+" / "+formatPrice(quote.OHLC.Close))
	text.plain("Volume", formatCount(float64(quote.Volume)))
	text.field("Bid", styleGreen.Render(bid))
	text.field("Ask", styleRed.Render(ask))
	if quote.OI > 0 {
		text.plain("OI", formatCount(quote.OI))
	}
	return text.String()
}

func (a *app) explorerHelp() string {
	e := a.explorer
	switch {
	case e.searchFocused:
		return help("type", "to search", "Enter/↓", "results", "Esc", "table", "^C", "quit")
	case e.mode == viewChain:
		return help("←→", "call/put", "Tab", "futures", "E", "expiry", "[ ]", "cycle", "F", "filters", "%", "BE", "O", "order", "A", "add to list", "H", "history", "R", "prices", "C", "catalog", "Q", "back")
	case e.mode == viewLists:
		return help("N", "new", "E", "rename", "A", "add instrument", "D", "delete", "R", "refresh NSE list", "M", "NSE master list", "O", "order", "H", "history", "Tab", "items", "Q", "back")
	}
	return help("/", "search", "Enter", "chain", "U", "underlyings", "L", "lists", "A", "add to list", "O", "order", "H", "history", "R", "prices", "C", "catalog", "s", "settings", "Q", "back")
}

func (a *app) explorerView(w, h int) string {
	e := a.explorer
	e.search.SetWidth(max(w-8, 10))
	searchInfo := styleFaint.Render("/")
	if e.searchFocused {
		searchInfo = styleFaint.Render("Enter results · Esc table")
	}
	searchPanel := panel("Search", searchInfo, " "+e.search.View(), w, 3, e.searchFocused)
	rest := h - 3
	if e.catalog == nil {
		return searchPanel + "\n" + textPanel("Instruments", "", styleMuted.Render("Opening the instrument catalog…"), w, rest, false)
	}
	detailW := 43
	if w < 100 || e.mode == viewLists {
		detailW = 0
	}
	leftW := w - detailW
	const top = 4 // tab bar and search panel
	var left string
	switch e.mode {
	case viewChain:
		futuresH := 6
		e.futures.x, e.futures.y = 1, top+2
		e.chainGrid.x, e.chainGrid.y = 1, top+futuresH+2
		data := e.chain
		futures := panel("Futures", styleMuted.Render(data.expiry().date.Format("02 Jan 2006")), e.futures.view(leftW-2, futuresH-2, e.focus == 0), leftW, futuresH, e.focus == 0)
		be := "BE as price"
		if e.breakevenPercent {
			be = "BE as % of spot"
		}
		chain := panel(e.chainTitle(), styleFaint.Render(be), e.chainGrid.view(leftW-2, rest-futuresH-2, e.focus == 1), leftW, rest-futuresH, e.focus == 1)
		left = futures + "\n" + chain
	case viewLists:
		return searchPanel + "\n" + a.listsView(w, rest, top)
	default:
		e.results.x, e.results.y = 1, top+2
		refreshed := "not refreshed"
		if !e.lastRefreshed.IsZero() {
			refreshed = e.lastRefreshed.Local().Format("02 Jan 15:04")
		}
		title := "F&O underlyings"
		if !e.underlyings {
			title = "Search results"
		}
		info := styleMuted.Render(fmt.Sprintf("%d rows · %d underlyings · catalog %s", len(e.resultItems), e.underlyingCount, refreshed))
		focused := !e.searchFocused
		left = panel(title, info, e.results.view(leftW-2, rest-2, focused), leftW, rest, focused)
	}
	if detailW == 0 {
		return searchPanel + "\n" + left
	}
	detail := styleFaint.Render("Select an instrument to inspect its normalized metadata.")
	if instrument, ok := e.selectedInstrument(); ok {
		detail = e.instrumentDetail(instrument)
	} else if e.mode == viewDashboard && len(e.resultItems) == 0 {
		detail = styleYellow.Bold(true).Render("No instruments found") + "\n\n" + styleMuted.Render("Try a symbol, company name, token, underlying, or contract description.")
	}
	return searchPanel + "\n" + joinColumns(left, textPanel("Instrument", "", detail, detailW, rest, false))
}

var errNoCatalog = errors.New("the instrument catalog is not loaded")
