package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/bsm"
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

	// watchlists are watchUser's lists; watching marks the instrument
	// table showing watchlists[watchIndex]. watchGen drops a watchlist
	// lookup a newer one replaced.
	watchUser  string
	watchlists []models.List
	watchIndex int
	watching   bool
	watchGen   int
	// watchNote replaces the status when the next watchlist shows.
	watchNote string

	listData  []models.List
	listsGrid grid
	itemsGrid grid

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
	// resultSort orders the instrument list; its zero value keeps the
	// search's or watchlist's own order.
	resultSort resultSort
	quoteAgain bool
	connecting bool

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
			{title: "EXCH", width: 6}, {title: "NAME", width: 26}, right("LTP", 10), right("CHG%", 8),
			right("STRIKE", 9), {title: "EXPIRY", width: 11, flex: 1},
		}, lines: true},
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

// chainLeg is where one side's cells sit in a chain row. The call's run
// OI, POP, BE, CHG, LTP towards the strike; the put's LTP, CHG, BE, POP,
// OI away from it.
type chainLeg struct{ oi, pop, be, change, ltp int }

const chainStrike = 5

var chainCells = [2]chainLeg{{oi: 0, pop: 1, be: 2, change: 3, ltp: 4}, {ltp: 6, change: 7, be: 8, pop: 9, oi: 10}}

func (e *explorerScreen) setChainColumns() {
	be := "BE"
	if e.breakevenPercent {
		be = "BE%"
	}
	e.chainGrid.columns = []column{
		{title: "OI", width: 8, flex: 1, align: alignRight, group: 0},
		{title: "POP", width: 6, align: alignRight, group: 0},
		{title: be, width: 8, align: alignRight, group: 0},
		{title: "CHG", width: 14, align: alignRight, group: 0},
		{title: "LTP", width: 8, align: alignRight, group: 0},
		{title: "STRIKE", width: 10, align: alignCenter, group: -1},
		{title: "LTP", width: 8, group: 1},
		{title: "CHG", width: 14, group: 1},
		{title: be, width: 8, group: 1},
		{title: "POP", width: 6, group: 1},
		{title: "OI", width: 8, flex: 1, group: 1},
	}
	e.chainGrid.bands = []cell{
		textCell("CALLS ◂", styleGreen.Bold(true)),
		textCell("▸ PUTS", styleRed.Bold(true)),
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
	case e.watching:
		if list, ok := e.currentWatchlist(); ok {
			return "WATCHLIST · " + list.Name
		}
		return "WATCHLIST"
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
		return tea.Batch(cmd, a.loadWatchlists("")), true
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
	case quotesConnectedMsg:
		e.connecting = false
		e.quoteSource, e.quoteLabel, e.quoteErr = msg.source, msg.label, msg.err
		e.tickerDown = false
		var unavailable tickerUnavailableError
		switch {
		case msg.source != nil && errors.As(msg.err, &unavailable):
			// Price mode ticker without a running ticker: quotes instead.
			e.quoteErr, e.tickerDown = nil, true
			e.status = statusErr("Ticker unavailable, using quotes:", unavailable.err.Error()+" · reopen Watchlists to retry")
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
	e.watching = false
	e.resultItems = append(e.resultItems[:0], values...)
	// A new list starts at the top, not on the old selection.
	e.results.rows, e.results.cursor, e.results.offset = nil, 0, 0
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
	switch {
	case e.watching:
		e.results.empty = styleMuted.Render("This watchlist is empty · search with / and press A to add an instrument")
	case e.underlyings:
		e.results.empty = styleMuted.Render("No F&O underlyings in the catalog")
	default:
		e.results.empty = styleYellow.Render("No instruments found") + styleMuted.Render(" · try a symbol, company name, token, underlying, or contract description")
	}
	selected, hadSelection := e.results.selected()
	for index := range e.results.columns {
		e.results.columns[index].title = resultTitles[index]
		if e.resultSort.column == index+1 {
			e.results.columns[index].title += e.resultSort.arrow()
		}
	}
	rows := make([]row, 0, len(e.resultItems))
	for _, instrument := range e.sortedResults() {
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
			textCell(instrument.Exchange, styleMuted),
			textCell(fallback(instrument.DisplayName, instrument.TradingSymbol), styleCyan),
			ltp, change,
			textCell(strike, styleYellow),
			textCell(expiry, styleMuted),
		}})
	}
	e.results.setRows(rows)
	// Prices reorder a sorted list, so the selection follows its instrument.
	if instrument, ok := selected.ref.(models.Instrument); hadSelection && ok {
		e.selectResult(instrument.ID)
	}
}

// resultTitles are the instrument list's column titles; resultSort.column
// is one past the index of the sorted one.
var resultTitles = []string{"EXCH", "NAME", "LTP", "CHG%", "STRIKE", "EXPIRY"}

type resultSort struct {
	column     int
	descending bool
}

func (s resultSort) arrow() string {
	if s.descending {
		return " ▼"
	}
	return " ▲"
}

// sortedResults is the instrument list in the chosen order. Instruments
// without a value for the column, such as an unpriced LTP, go last either
// way, and ties keep the list's own order.
func (e *explorerScreen) sortedResults() []models.Instrument {
	if e.resultSort.column == 0 {
		return e.resultItems
	}
	sorted := append([]models.Instrument(nil), e.resultItems...)
	value := func(instrument models.Instrument) (float64, bool) {
		quote, ok := e.quotes[string(instrument.ID)]
		switch e.resultSort.column {
		case 3:
			return quote.LastPrice, ok && quote.LastPrice > 0
		case 4:
			if !ok || quote.LastPrice <= 0 || quote.OHLC.Close <= 0 {
				return 0, false
			}
			return (quote.LastPrice - quote.OHLC.Close) / quote.OHLC.Close, true
		case 5:
			return instrument.Strike, instrument.Strike > 0
		case 6:
			if instrument.Expiry == nil {
				return 0, false
			}
			return float64(instrument.Expiry.Unix()), true
		}
		return 0, false
	}
	slices.SortStableFunc(sorted, func(left, right models.Instrument) int {
		var order int
		switch e.resultSort.column {
		case 1:
			order = strings.Compare(left.Exchange, right.Exchange)
		case 2:
			order = strings.Compare(
				strings.ToUpper(fallback(left.DisplayName, left.TradingSymbol)),
				strings.ToUpper(fallback(right.DisplayName, right.TradingSymbol)))
		default:
			leftValue, leftOK := value(left)
			rightValue, rightOK := value(right)
			if leftOK != rightOK {
				if leftOK {
					return -1
				}
				return 1
			}
			order = cmp.Compare(leftValue, rightValue)
		}
		if e.resultSort.descending {
			return -order
		}
		return order
	})
	return sorted
}

// openSortMenu picks the instrument list's sort column. Picking the sorted
// column again reverses it.
func (a *app) openSortMenu() {
	e := a.explorer
	picker := &menu{title: "Sort by", width: 30, footer: "Pick the sorted column again to reverse it"}
	picker.items = append(picker.items, menuItem{label: styleText.Render("List order"), run: func(a *app) tea.Cmd {
		return a.setResultSort(resultSort{})
	}})
	for index, title := range resultTitles {
		column := index + 1
		label := styleText.Render(title)
		if e.resultSort.column == column {
			label += styleYellow.Render(e.resultSort.arrow())
			picker.cursor = column
		}
		picker.items = append(picker.items, menuItem{label: label, run: func(a *app) tea.Cmd {
			next := resultSort{column: column}
			if current := a.explorer.resultSort; current.column == column {
				next.descending = !current.descending
			}
			return a.setResultSort(next)
		}})
	}
	a.overlay = picker
}

func (a *app) setResultSort(sort resultSort) tea.Cmd {
	a.explorer.resultSort = sort
	a.explorer.renderResults()
	return a.explorerSelectionChanged()
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
	if query == "" && len(e.watchlists) > 0 {
		return a.showWatchlist(e.watchIndex, "")
	}
	return a.searchTask(catalog, generation, query)
}

// searchTask searches the catalog for query, or lists the F&O underlyings
// when it is empty.
func (a *app) searchTask(catalog catalog, generation int, query string) tea.Cmd {
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
	if e.snapshot.watching && len(e.watchlists) > 0 {
		e.watching = true
		e.renderResults()
		// The lists view may have changed the watchlists: read it again.
		cmd = tea.Batch(cmd, a.showWatchlist(e.watchIndex, e.snapshot.selectedID))
	}
	e.selectResult(e.snapshot.selectedID)
	e.status = statusInfo("Returned to instrument results.")
	return cmd
}

func (e *explorerScreen) takeSnapshot() {
	e.snapshot = dashboardSnapshot{
		query:    e.search.Value(),
		results:  append([]models.Instrument(nil), e.resultItems...),
		watching: e.watching,
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
	now := time.Now()
	for _, candidate := range chainRows {
		cells := make([]cell, len(e.chainGrid.columns))
		refs := make([]any, 2)
		for side, leg := range []*models.Instrument{candidate.call, candidate.put} {
			at := chainCells[side]
			ltpStyle, barColor := styleGreen, colorCallBar
			if side == 1 {
				ltpStyle, barColor = styleRed, colorPutBar
			}
			quote, quoted := kite.QuoteData{}, false
			if leg != nil {
				quote, quoted = e.quotes[string(leg.ID)]
			}
			if leg == nil || data.filter.hidesByPrice(*leg, quote.LastPrice, spot) {
				for _, index := range []int{at.ltp, at.change, at.be, at.pop, at.oi} {
					cells[index] = textCell("—", styleNoTrade)
				}
				continue
			}
			refs[side] = *leg
			cells[at.ltp], cells[at.change], cells[at.be] = textCell("—", ltpStyle), textCell("—", styleMuted), textCell("—", styleBlue)
			cells[at.pop], cells[at.oi] = textCell("—", styleMuted), textCell("—", styleMuted)
			if quoted {
				// The price and its change are coloured by direction.
				change, direction := formatMove(quote)
				cells[at.ltp] = textCell(formatPrice(quote.LastPrice), signStyle(direction))
				if change != "" {
					cells[at.change] = textCell(change, signStyle(direction))
				}
				cells[at.oi] = cell{text: formatCount(quote.OI), style: styleText, barColor: barColor, barRight: side == 0}
				if largest > 0 {
					cells[at.oi].bar = quote.OI / largest
				}
				if quote.LastPrice > 0 {
					cells[at.be].text = formatBreakeven(breakeven(*leg, quote.LastPrice), spot, e.breakevenPercent)
					if pop, ok := probabilityOfProfit(*leg, quote.LastPrice, spot, now); ok {
						cells[at.pop] = textCell(formatProbability(pop), styleYellow)
					}
				}
			}
		}
		// A strike whose contracts are all hidden is dropped.
		if refs[0] == nil && refs[1] == nil {
			continue
		}
		strikeStyle := styleYellow
		if candidate.strike == atm {
			strikeStyle = styleATM
			atmRow = len(rows)
		}
		cells[chainStrike] = textCell(formatStrike(candidate.strike), strikeStyle)
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
		text, direction := formatPriceChange(quote)
		title += " " + signStyle(direction).Render(text)
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
	f.addInput("Minimum LTP", optionalFloat(filter.minLTP), decimalNumber, false)
	f.addInput("Maximum LTP", optionalFloat(filter.maxLTP), decimalNumber, false)
	f.addInput("Minimum BE %", optionalFloat(filter.minBreakeven), decimalNumber, false)
	f.addInput("Maximum BE %", optionalFloat(filter.maxBreakeven), decimalNumber, false)
	f.message = styleMuted.Render("BE % is the breakeven's distance from spot either way.")
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
		filter := e.chain.filter
		var err error
		for _, r := range []struct {
			noun     string
			min, max **float64
		}{
			{"strike", &filter.minStrike, &filter.maxStrike},
			{"LTP", &filter.minLTP, &filter.maxLTP},
			{"BE %", &filter.minBreakeven, &filter.maxBreakeven},
		} {
			if *r.min, *r.max, err = parseRange(r.noun, o.form.field("Minimum "+r.noun).value(), o.form.field("Maximum "+r.noun).value()); err != nil {
				o.form.message = styleRed.Render(err.Error())
				return nil
			}
		}
		filter.side = optionSide(o.form.field("Contracts").index)
		e.chain.filter = filter
	case 1:
		e.chain.filter = chainFilter{expiryNumber: e.chain.filter.expiryNumber, side: optionSideBoth}
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
		if key == "esc" {
			a.leaveInstruments()
		}
		return nil
	}
	if key == "esc" {
		if e.mode == viewDashboard {
			if !e.watching && len(e.watchlists) > 0 {
				e.search.SetValue("")
				return a.showWatchlist(e.watchIndex, "")
			}
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
	case "[", "]":
		step := 1
		if key == "[" {
			step = -1
		}
		if e.mode == viewDashboard {
			return a.cycleWatchlist(step)
		}
		return a.cycleExpiry(step)
	case "d", "D":
		if e.mode == viewDashboard {
			return a.removeFromWatchlist()
		}
		return nil
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
	case "o", "O":
		if e.mode == viewDashboard {
			if instrument, ok := e.selectedInstrument(); ok {
				return a.openChain(instrument)
			}
		}
		return nil
	case "S":
		if e.mode == viewDashboard {
			a.openSortMenu()
		}
		return nil
	case "+":
		return a.orderSelected()
	case "enter":
		if e.mode == viewChain {
			return a.orderSelected()
		}
		return nil
	}
	switch strings.ToLower(key) {
	case "b":
		return a.openBaskets()
	case "l":
		return a.openLists()
	case "a":
		return a.addSelectedToList()
	case "n":
		if e.mode == viewDashboard {
			return a.newWatchlist()
		}
	case "e":
		a.openExpiryPicker()
	case "f":
		a.openFilters()
	case "m":
		a.showMetadata()
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
	text := newDetailText(string(instrument.ID), instrument.DisplayName)
	e.marketDetail(text, instrument)
	text.line("\n" + styleFaint.Render("M metadata"))
	return text.String()
}

// instrumentMetadata is the catalog's record of the instrument, shown by M.
func instrumentMetadata(instrument models.Instrument) string {
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
	return text.String()
}

// showMetadata opens the selected instrument's metadata.
func (a *app) showMetadata() {
	instrument, ok := a.explorer.selectedInstrument()
	if !ok {
		a.explorer.status = statusWarn("Select an instrument to see its metadata")
		return
	}
	a.overlay = &textOverlay{title: "Metadata", text: instrumentMetadata(instrument), close: "m"}
}

// marketDetail adds the instrument's quote, market depth and, for an
// option, its model values.
func (e *explorerScreen) marketDetail(text *detailText, instrument models.Instrument) {
	if e.quoteSource == nil {
		return
	}
	quote, ok := e.quotes[string(instrument.ID)]
	if !ok {
		text.section("Market")
		text.line(styleFaint.Render("No quote yet"))
		return
	}
	change, direction := formatChange(quote)
	text.section("Market")
	text.field("LTP", styleBold.Render(formatPrice(quote.LastPrice))+" "+signStyle(direction).Render(change))
	text.plain("Open / High", formatPrice(quote.OHLC.Open)+" / "+formatPrice(quote.OHLC.High))
	text.plain("Low / Prev", formatPrice(quote.OHLC.Low)+" / "+formatPrice(quote.OHLC.Close))
	text.plain("Volume", formatCount(float64(quote.Volume)))
	if quote.OI > 0 {
		text.plain("OI", formatCount(quote.OI))
	}
	depthDetail(text, quote)
	if params, ok := optionParams(instrument, e.underlyingPrice(instrument), time.Now()); ok && quote.LastPrice > 0 {
		text.section("Model " + styleFaint.Render(fmt.Sprintf("BSM · r %.1f%%", riskFreeRate*100)))
		if volatility, err := bsm.ImpliedVolatility(params, quote.LastPrice); err == nil {
			pop, _ := bsm.ProbabilityOfProfit(params, quote.LastPrice)
			text.plain("Implied vol", formatProbability(volatility))
			text.field("POP", styleYellow.Render(formatProbability(pop)))
		} else {
			text.line(styleFaint.Render("No volatility fits this price"))
		}
	}
}

// detailWidth is the explorer's details pane. depthWidth is its text: each
// depth side is orders, quantity and price in 21 cells, wide enough for a
// six-digit price, bids on the left and asks on the right.
const (
	detailWidth = 47
	depthWidth  = detailWidth - 4
)

// depthDetail adds the five-level market depth, best prices at the top,
// with the total quantity bid and offered under it as a bar. Instruments
// without depth, such as indices, get none.
func depthDetail(text *detailText, quote kite.QuoteData) {
	depth := quote.Depth
	if depth.Buy[0].Price <= 0 && depth.Sell[0].Price <= 0 {
		return
	}
	level := func(item models.DepthItem) (orders, quantity, price string) {
		if item.Price <= 0 {
			return "", "", "—"
		}
		return formatCount(float64(item.Orders)), formatCount(float64(item.Quantity)), formatPrice(item.Price)
	}
	text.section("Depth")
	text.line(styleMuted.Render(fmt.Sprintf("%3s %7s %9s %-9s %7s %3s", "ORD", "QTY", "BID", "ASK", "QTY", "ORD")))
	for index := range depth.Buy {
		bidOrders, bidQuantity, bid := level(depth.Buy[index])
		askOrders, askQuantity, ask := level(depth.Sell[index])
		text.line(styleMuted.Render(fmt.Sprintf("%3s ", bidOrders)) + styleText.Render(fmt.Sprintf("%7s ", bidQuantity)) +
			styleGreen.Render(fmt.Sprintf("%9s", bid)) + " " + styleRed.Render(fmt.Sprintf("%-9s", ask)) +
			styleText.Render(fmt.Sprintf(" %7s", askQuantity)) + styleMuted.Render(fmt.Sprintf(" %3s", askOrders)))
	}
	buy, sell := float64(quote.BuyQuantity), float64(quote.SellQuantity)
	if buy+sell <= 0 {
		return
	}
	bids := int(math.Round(buy / (buy + sell) * depthWidth))
	text.line(lipgloss.NewStyle().Foreground(colorCallBar).Render(strings.Repeat("━", bids)) +
		lipgloss.NewStyle().Foreground(colorPutBar).Render(strings.Repeat("━", depthWidth-bids)))
	totals := fmt.Sprintf("%s bid · %.0f%%", formatCount(buy), buy/(buy+sell)*100)
	offered := fmt.Sprintf("%.0f%% · %s offered", sell/(buy+sell)*100, formatCount(sell))
	text.line(styleGreen.Render(totals) + strings.Repeat(" ", max(depthWidth-lipgloss.Width(totals)-lipgloss.Width(offered), 1)) + styleRed.Render(offered))
}

// underlyingPrice is the last price of an option's underlying, or 0.
func (e *explorerScreen) underlyingPrice(option models.Instrument) float64 {
	if option.UnderlyingID == nil {
		return 0
	}
	return e.quotes[string(*option.UnderlyingID)].LastPrice
}

func (a *app) explorerHelp() string {
	e := a.explorer
	switch {
	case e.searchFocused:
		return help("type", "to search", "Enter/↓", "results", "Esc", "table", "^C", "quit")
	case e.mode == viewChain:
		return help("←→", "call/put", "Tab", "futures", "E", "expiry", "[ ]", "cycle", "F", "filters", "%", "BE", "+", "order", "B", "basket", "A", "add to list", "M", "metadata", "Esc", "back")
	case e.mode == viewLists:
		return help("N", "new watchlist", "E", "rename", "A", "add instrument", "D", "delete", "+", "order", "Tab", "items", "Esc", "back")
	}
	if e.watching {
		return help("[ ]", "watchlist", "N", "new", "D", "remove", "L", "lists", "/", "search", "O", "chain", "⇧S", "sort", "A", "add to list", "+", "order", "B", "baskets", "M", "metadata", "Esc", "back")
	}
	if len(e.watchlists) > 0 {
		return help("Esc", "watchlist", "[ ]", "watchlist", "N", "new", "/", "search", "O", "chain", "⇧S", "sort", "L", "lists", "A", "add to list", "+", "order", "B", "baskets", "M", "metadata")
	}
	return help("/", "search", "O", "chain", "⇧S", "sort", "L", "lists", "A", "add to list", "+", "order", "B", "baskets", "M", "metadata", "Esc", "back")
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
		return searchPanel + "\n" + textPanel("Watchlists", "", styleMuted.Render("Opening the instrument catalog…"), w, rest, false)
	}
	detailW := detailWidth
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
		if list, ok := e.currentWatchlist(); ok && e.watching {
			title, info = list.Name, styleMuted.Render(e.watchlistInfo())
		}
		focused := !e.searchFocused
		left = panel(title, info, e.results.view(leftW-2, rest-2, focused), leftW, rest, focused)
	}
	if detailW == 0 {
		return searchPanel + "\n" + left
	}
	detail := styleFaint.Render("Select an instrument to inspect its normalized metadata.")
	if instrument, ok := e.selectedInstrument(); ok {
		detail = e.instrumentDetail(instrument)
	} else if e.mode == viewDashboard && len(e.resultItems) == 0 && !e.watching {
		detail = styleYellow.Bold(true).Render("No instruments found") + "\n\n" + styleMuted.Render("Try a symbol, company name, token, underlying, or contract description.")
	}
	return searchPanel + "\n" + joinColumns(left, textPanel("Instrument", "", detail, detailW, rest, false))
}

var errNoCatalog = errors.New("the instrument catalog is not loaded")
