package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	dashboardPage = "dashboard"
	chainPage     = "chain"
	modalPage     = "modal"
	busyPage      = "busy"
	filterPage    = "filter"
	expiryPage    = "expiry"
)

type viewMode int

const (
	viewDashboard viewMode = iota
	viewChain
)

type refreshedView struct {
	lastRefreshed   time.Time
	underlyingCount int
	query           string
	underlyings     bool
	results         []models.Instrument
	selectedID      models.InstrumentID
	chain           *chainData
}

type instrumentApp struct {
	application *tview.Application
	pages       *tview.Pages
	bodyPages   *tview.Pages
	header      *tview.TextView
	search      *tview.InputField
	results     *tview.Table
	detail      *tview.TextView
	futures     *tview.Table
	chain       *tview.Table
	status      *tview.TextView

	expiryButton *tview.Button
	filterButton *tview.Button
	backButton   *tview.Button

	ctx      context.Context
	cancel   context.CancelFunc
	factory  catalogFactory
	catalog  catalog
	database string
	searcher *searchCoordinator
	wg       sync.WaitGroup
	stopOnce sync.Once
	opMu     sync.Mutex
	stopping bool

	mode            viewMode
	overlay         string
	busy            bool
	suppressSearch  bool
	underlyings     bool
	resultItems     []models.Instrument
	snapshot        dashboardSnapshot
	chainData       *chainData
	lastRefreshed   time.Time
	underlyingCount int
}

func newInstrumentApp(parent context.Context, database string, factory catalogFactory) *instrumentApp {
	configureTheme()
	ctx, cancel := context.WithCancel(parent)
	ui := &instrumentApp{
		application: tview.NewApplication().EnableMouse(true),
		pages:       tview.NewPages(),
		bodyPages:   tview.NewPages(),
		ctx:         ctx,
		cancel:      cancel,
		factory:     factory,
		database:    database,
		searcher:    newSearchCoordinator(searchDelay),
	}
	ui.build()
	return ui
}

func (ui *instrumentApp) run() error {
	ui.showBusy("Opening the catalog and refreshing it if stale…")
	ui.beginInitialization()
	go func() {
		<-ui.ctx.Done()
		ui.stop()
	}()

	runErr := ui.application.Run()
	ui.cancel()
	ui.searcher.shutdown()
	ui.wg.Wait()
	ui.searcher.wait()
	var closeErr error
	if ui.catalog != nil {
		closeErr = ui.catalog.Close()
	}
	if errors.Is(runErr, context.Canceled) {
		runErr = nil
	}
	return errors.Join(runErr, closeErr)
}

func (ui *instrumentApp) build() {
	ui.header = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	ui.header.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)
	databaseView := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false).
		SetWordWrap(false).
		SetTextAlign(tview.AlignRight).
		SetText(fmt.Sprintf("[yellow::b]DB[-] %s", tview.Escape(ui.database)))
	databaseView.SetBorder(true).SetBorderColor(tcell.ColorLightYellow).SetTitle(" Catalog ").SetTitleColor(tcell.ColorLightYellow)
	headerRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(ui.header, 0, 3, false).
		AddItem(databaseView, 0, 2, false)

	ui.search = tview.NewInputField().
		SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(tcell.ColorWhite).
		SetLabelColor(tcell.ColorLightCyan).
		SetPlaceholder("Search symbol, name, token, underlying, call, put, futures…")
	ui.search.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(" Search [/]").SetTitleColor(tcell.ColorLightYellow)
	ui.search.SetChangedFunc(ui.searchChanged)
	ui.search.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyDown {
			ui.application.SetFocus(ui.results)
			return nil
		}
		return event
	})
	ui.search.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEscape:
			ui.application.SetFocus(ui.activeTable())
		case tcell.KeyEnter:
			ui.application.SetFocus(ui.results)
		}
	})

	ui.results = newInstrumentTable(" F&O underlyings ")
	ui.results.SetSelectionChangedFunc(func(row, column int) {
		if instrument, ok := tableInstrument(ui.results, row, column); ok {
			ui.updateDetail(instrument)
		}
	})
	ui.results.SetSelectedFunc(func(row, column int) {
		if instrument, ok := tableInstrument(ui.results, row, column); ok {
			ui.openChain(instrument)
		}
	})

	ui.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	ui.detail.SetBorder(true).SetBorderColor(tcell.ColorLightPink).SetTitle(" Instrument ").SetTitleColor(tcell.ColorLightPink)
	ui.detail.SetText("Select an instrument to inspect its normalized metadata.")

	dashboard := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(ui.results, 0, 3, true).
		AddItem(ui.detail, 43, 1, false)
	ui.bodyPages.AddPage(dashboardPage, dashboard, true, true)

	ui.futures = newInstrumentTable(" Futures ")
	ui.futures.SetSelectionChangedFunc(func(row, column int) {
		if instrument, ok := tableInstrument(ui.futures, row, column); ok {
			ui.updateDetail(instrument)
		}
	})
	ui.chain = newInstrumentTable(" Option chain ").SetSelectable(true, true)
	ui.chain.SetSelectionChangedFunc(func(row, column int) {
		if instrument, ok := tableInstrument(ui.chain, row, column); ok {
			ui.updateDetail(instrument)
		}
	})
	chainTables := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.futures, 6, 0, false).
		AddItem(ui.chain, 0, 1, true)
	chainView := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(chainTables, 0, 3, true).
		AddItem(ui.detail, 43, 1, false)
	ui.bodyPages.AddPage(chainPage, chainView, true, false)

	actions := ui.newActions()
	ui.status = tview.NewTextView().
		SetDynamicColors(true).
		SetTextColor(tcell.ColorLightCyan).
		SetText("  Opening catalog…")
	ui.status.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(headerRow, 3, 0, false).
		AddItem(ui.search, 3, 0, false).
		AddItem(ui.bodyPages, 0, 1, true).
		AddItem(actions, 3, 0, false).
		AddItem(ui.status, 2, 0, false)
	ui.pages.AddPage(dashboardPage, root, true, true)
	ui.application.SetRoot(ui.pages, true).SetInputCapture(ui.capture)
	ui.updateModeControls()
	ui.updateHeader()
}

func (ui *instrumentApp) newActions() *tview.Flex {
	actions := tview.NewFlex().SetDirection(tview.FlexColumn)
	actions.SetBorder(true).SetBorderColor(tcell.ColorDarkCyan).SetTitle(" Actions ").SetTitleColor(tcell.ColorLightYellow)
	actions.AddItem(actionButton("⌕ Search [/]", tcell.ColorLightCyan, ui.focusSearch), 0, 1, false)
	actions.AddItem(actionButton("◆ Underlyings [U]", tcell.ColorLightGreen, ui.showUnderlyings), 0, 1, false)
	ui.expiryButton = actionButton("◷ Expiry [E]", tcell.ColorLightYellow, ui.openExpiryPicker)
	actions.AddItem(ui.expiryButton, 0, 1, false)
	ui.filterButton = actionButton("≡ Filters [F]", tcell.ColorLightSkyBlue, ui.openFilters)
	actions.AddItem(ui.filterButton, 0, 1, false)
	actions.AddItem(actionButton("↻ Refresh [R]", tcell.ColorLightPink, ui.refresh), 0, 1, false)
	ui.backButton = actionButton("← Back [Esc]", tcell.ColorLightGray, ui.backToDashboard)
	actions.AddItem(ui.backButton, 0, 1, false)
	actions.AddItem(actionButton("⏻ Quit [Q]", tcell.ColorLightCoral, ui.stop), 0, 1, false)
	return actions
}

func (ui *instrumentApp) beginInitialization() {
	if !ui.startOperation() {
		return
	}
	go func() {
		defer ui.wg.Done()
		initial, err := loadInitialCatalog(ui.ctx, ui.factory)
		if ui.ctx.Err() != nil {
			if initial.catalog != nil {
				_ = initial.catalog.Close()
			}
			return
		}
		ui.application.QueueUpdateDraw(func() {
			if err != nil {
				ui.showStartupError(err)
				return
			}
			ui.catalog = initial.catalog
			ui.lastRefreshed = initial.lastRefreshed
			ui.underlyingCount = len(initial.underlyings)
			ui.closeOverlay()
			ui.setResults(initial.underlyings, true, "")
			ui.setStatus(fmt.Sprintf("  [green::b]READY[-] · %d F&O underlyings", len(initial.underlyings)))
		})
	}()
}

func (ui *instrumentApp) searchChanged(text string) {
	if ui.suppressSearch || ui.catalog == nil || ui.busy {
		return
	}
	query := strings.TrimSpace(text)
	underlyings := query == ""
	if underlyings {
		ui.setStatus("  Loading F&O underlyings…")
	} else {
		ui.setStatus(fmt.Sprintf("  Searching for [yellow]%s[-]…", tview.Escape(query)))
	}
	catalog := ui.catalog
	ui.searcher.schedule(ui.ctx, query, underlyings, func(ctx context.Context, query string, underlyings bool) ([]models.Instrument, error) {
		if underlyings {
			return catalog.ListFO(ctx)
		}
		return catalog.Search(ctx, query, searchLimit)
	}, func(result searchResult) {
		if ui.ctx.Err() != nil {
			return
		}
		ui.application.QueueUpdateDraw(func() {
			if result.err != nil {
				ui.showError(result.err)
				return
			}
			if result.underlyings {
				ui.underlyingCount = len(result.instruments)
			}
			keepSearchFocus := ui.application.GetFocus() == ui.search
			ui.setResults(result.instruments, result.underlyings, result.query)
			if keepSearchFocus {
				ui.application.SetFocus(ui.search)
			}
			if result.underlyings {
				ui.setStatus(fmt.Sprintf("  %d F&O underlyings", len(result.instruments)))
			} else {
				ui.setStatus(fmt.Sprintf("  %d ranked results for [yellow]%s[-]", len(result.instruments), tview.Escape(result.query)))
			}
		})
	})
}

func (ui *instrumentApp) setResults(values []models.Instrument, underlyings bool, query string) {
	ui.mode = viewDashboard
	ui.underlyings = underlyings
	ui.resultItems = append(ui.resultItems[:0], values...)
	ui.bodyPages.SwitchToPage(dashboardPage)
	ui.results.Clear()
	title := " Search results "
	if underlyings {
		title = " F&O underlyings "
	}
	ui.results.SetTitle(title)
	setHeaders(ui.results, []string{"SYMBOL", "TYPE", "EXPIRY", "STRIKE", "OPT", "FUT", "LOT", "TOKEN", "NAME"})
	for row, instrument := range values {
		cells := []struct {
			text  string
			color tcell.Color
			width int
		}{
			{instrument.TradingSymbol, tcell.ColorLightCyan, 24},
			{instrument.InstrumentType, instrumentTypeColor(instrument.InstrumentType), 8},
			{formatExpiry(instrument.Expiry), tcell.ColorLightGray, 11},
			{formatStrike(instrument.Strike), tcell.ColorYellow, 12},
			{strconv.Itoa(instrument.OptionsCount), tcell.ColorLightGreen, 5},
			{strconv.Itoa(instrument.FuturesCount), tcell.ColorLightYellow, 5},
			{strconv.Itoa(instrument.LotSize), tcell.ColorLightBlue, 7},
			{strconv.FormatInt(instrument.InstrumentToken, 10), tcell.ColorLightPink, 12},
			{instrument.DisplayName, tcell.ColorWhite, 30},
		}
		for column, cell := range cells {
			ui.results.SetCell(row+1, column, tview.NewTableCell(tview.Escape(cell.text)).
				SetTextColor(cell.color).
				SetMaxWidth(cell.width).
				SetReference(instrument))
		}
	}
	if len(values) > 0 {
		ui.results.Select(1, 0)
		ui.updateDetail(values[0])
	} else {
		ui.detail.SetText("[yellow::b]No instruments found[-]\n\nTry a symbol, company name, token, underlying, or contract description.")
	}
	ui.updateModeControls()
	ui.updateHeader()
	ui.application.SetFocus(ui.results)
	_ = query
}

func (ui *instrumentApp) openChain(instrument models.Instrument) {
	underlying, ok := drilldownUnderlying(instrument)
	if !ok {
		ui.setStatus("  This instrument has no futures or options to explore.")
		return
	}
	ui.snapshot = dashboardSnapshot{
		query:      ui.search.GetText(),
		results:    append([]models.Instrument(nil), ui.resultItems...),
		selectedID: instrument.ID,
	}
	ui.searcher.cancelPending()
	ui.showBusy(fmt.Sprintf("Loading contracts for %s…", underlying))
	catalog := ui.catalog
	if !ui.startOperation() {
		return
	}
	go func() {
		defer ui.wg.Done()
		data, err := loadChainData(ui.ctx, catalog, underlying)
		if ui.ctx.Err() != nil {
			return
		}
		ui.application.QueueUpdateDraw(func() {
			if err != nil {
				ui.showError(err)
				return
			}
			ui.chainData = &data
			ui.mode = viewChain
			ui.closeOverlay()
			ui.renderChain()
		})
	}()
}

func loadChainData(ctx context.Context, catalog catalog, underlying models.InstrumentID) (chainData, error) {
	futures, err := catalog.GetFutures(ctx, models.FuturesFilter{UnderlyingID: underlying})
	if err != nil {
		return chainData{}, err
	}
	options, err := catalog.GetOptions(ctx, models.OptionsFilter{UnderlyingID: underlying})
	if err != nil {
		return chainData{}, err
	}
	expiries := collectExpiries(futures, options)
	if len(expiries) == 0 {
		return chainData{}, fmt.Errorf("no dated contracts found for %s", underlying)
	}
	return chainData{
		underlying: underlying,
		futures:    futures,
		options:    options,
		expiries:   expiries,
		filter:     chainFilter{expiryNumber: expiries[0].number, side: optionSideBoth},
	}, nil
}

func (ui *instrumentApp) renderChain() {
	if ui.chainData == nil {
		return
	}
	ui.mode = viewChain
	ui.bodyPages.SwitchToPage(chainPage)
	data := ui.chainData
	expiry := data.expiries[0]
	for _, candidate := range data.expiries {
		if candidate.number == data.filter.expiryNumber {
			expiry = candidate
			break
		}
	}

	ui.futures.Clear()
	ui.futures.SetTitle(fmt.Sprintf(" Futures · %s ", expiry.date.Format("02 Jan 2006")))
	setHeaders(ui.futures, []string{"SYMBOL", "EXPIRY", "LOT", "TOKEN"})
	visibleFutures := filterFutures(data.futures, data.filter)
	for row, future := range visibleFutures {
		values := []string{future.TradingSymbol, formatExpiry(future.Expiry), strconv.Itoa(future.LotSize), strconv.FormatInt(future.InstrumentToken, 10)}
		for column, value := range values {
			ui.futures.SetCell(row+1, column, tview.NewTableCell(tview.Escape(value)).SetTextColor(tcell.ColorLightCyan).SetReference(future))
		}
	}

	ui.chain.Clear()
	ui.chain.SetTitle(fmt.Sprintf(" %s · %s · %s ", data.underlying, expiry.date.Format("02 Jan 2006"), data.filter.side))
	setHeaders(ui.chain, []string{"CALL SYMBOL", "CALL TOKEN", "STRIKE", "PUT TOKEN", "PUT SYMBOL"})
	rows := buildChainRows(data.options, data.filter)
	for rowIndex, row := range rows {
		rowNumber := rowIndex + 1
		strikeReference := any(nil)
		if row.call != nil {
			strikeReference = *row.call
			ui.chain.SetCell(rowNumber, 0, contractCell(row.call.TradingSymbol, *row.call, tcell.ColorLightGreen))
			ui.chain.SetCell(rowNumber, 1, contractCell(strconv.FormatInt(row.call.InstrumentToken, 10), *row.call, tcell.ColorLightGreen))
		} else {
			ui.chain.SetCell(rowNumber, 0, emptyCell())
			ui.chain.SetCell(rowNumber, 1, emptyCell())
		}
		if row.put != nil {
			if strikeReference == nil {
				strikeReference = *row.put
			}
			ui.chain.SetCell(rowNumber, 3, contractCell(strconv.FormatInt(row.put.InstrumentToken, 10), *row.put, tcell.ColorLightCoral))
			ui.chain.SetCell(rowNumber, 4, contractCell(row.put.TradingSymbol, *row.put, tcell.ColorLightCoral))
		} else {
			ui.chain.SetCell(rowNumber, 3, emptyCell())
			ui.chain.SetCell(rowNumber, 4, emptyCell())
		}
		ui.chain.SetCell(rowNumber, 2, tview.NewTableCell(formatStrike(row.strike)).
			SetTextColor(tcell.ColorYellow).
			SetAlign(tview.AlignCenter).
			SetReference(strikeReference))
	}

	if len(rows) > 0 {
		column := 0
		if rows[0].call == nil {
			column = 3
		}
		ui.chain.Select(1, column)
		if instrument, ok := tableInstrument(ui.chain, 1, column); ok {
			ui.updateDetail(instrument)
		}
	} else if len(visibleFutures) > 0 {
		ui.futures.Select(1, 0)
		ui.updateDetail(visibleFutures[0])
	} else {
		ui.detail.SetText("[yellow::b]No contracts match these filters.[-]")
	}
	ui.updateModeControls()
	ui.updateHeaderWithCount(len(rows) + len(visibleFutures))
	ui.setStatus(fmt.Sprintf("  %d option strikes · %d futures · [yellow]%s[-] previous/next expiry", len(rows), len(visibleFutures), tview.Escape("[ / ]")))
	ui.application.SetFocus(ui.chain)
}

func (ui *instrumentApp) openExpiryPicker() {
	if ui.mode != viewChain || ui.chainData == nil || ui.busy {
		return
	}
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(" Select expiry ").SetTitleColor(tcell.ColorLightYellow)
	selected := 0
	for index, expiry := range ui.chainData.expiries {
		expiry := expiry
		label := fmt.Sprintf("#%d  %s", expiry.number, expiry.date.Format("02 Jan 2006"))
		list.AddItem(label, "", 0, func() {
			ui.chainData.filter.expiryNumber = expiry.number
			ui.closeOverlay()
			ui.renderChain()
		})
		if expiry.number == ui.chainData.filter.expiryNumber {
			selected = index
		}
	}
	list.SetCurrentItem(selected)
	list.SetDoneFunc(ui.closeOverlay)
	ui.showOverlay(expiryPage, centered(38, minInt(len(ui.chainData.expiries)+2, 16), list), list)
}

func (ui *instrumentApp) cycleExpiry(delta int) {
	if ui.mode != viewChain || ui.chainData == nil || len(ui.chainData.expiries) == 0 {
		return
	}
	index := 0
	for i, expiry := range ui.chainData.expiries {
		if expiry.number == ui.chainData.filter.expiryNumber {
			index = i
			break
		}
	}
	index += delta
	if index < 0 {
		index = len(ui.chainData.expiries) - 1
	}
	if index >= len(ui.chainData.expiries) {
		index = 0
	}
	ui.chainData.filter.expiryNumber = ui.chainData.expiries[index].number
	ui.renderChain()
}

func (ui *instrumentApp) openFilters() {
	if ui.mode != viewChain || ui.chainData == nil || ui.busy {
		return
	}
	filter := ui.chainData.filter
	form := tview.NewForm().SetItemPadding(1).SetButtonsAlign(tview.AlignCenter)
	form.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(" Option filters ").SetTitleColor(tcell.ColorLightYellow)
	form.AddDropDown("Contracts", []string{optionSideBoth.String(), optionSideCalls.String(), optionSidePuts.String()}, int(filter.side), nil)
	form.AddInputField("Minimum strike", optionalFloat(filter.minStrike), 18, nil, nil)
	form.AddInputField("Maximum strike", optionalFloat(filter.maxStrike), 18, nil, nil)
	form.AddButton("Apply", func() {
		minimum := form.GetFormItemByLabel("Minimum strike").(*tview.InputField).GetText()
		maximum := form.GetFormItemByLabel("Maximum strike").(*tview.InputField).GetText()
		minStrike, maxStrike, err := parseStrikeRange(minimum, maximum)
		if err != nil {
			form.SetTitle(" " + tview.Escape(err.Error()) + " ").
				SetTitleColor(tcell.ColorRed).
				SetBorderColor(tcell.ColorRed)
			return
		}
		side, _ := form.GetFormItemByLabel("Contracts").(*tview.DropDown).GetCurrentOption()
		ui.chainData.filter.minStrike = minStrike
		ui.chainData.filter.maxStrike = maxStrike
		ui.chainData.filter.side = optionSide(side)
		ui.closeOverlay()
		ui.renderChain()
	})
	form.AddButton("Reset", func() {
		ui.chainData.filter.minStrike = nil
		ui.chainData.filter.maxStrike = nil
		ui.chainData.filter.side = optionSideBoth
		ui.closeOverlay()
		ui.renderChain()
	})
	form.AddButton("Cancel", ui.closeOverlay)
	form.SetCancelFunc(ui.closeOverlay)
	ui.showOverlay(filterPage, centered(54, 16, form), form)
}

func (ui *instrumentApp) refresh() {
	if ui.catalog == nil || ui.busy {
		return
	}
	ui.searcher.cancelPending()
	ui.showBusy("Refreshing the complete Kite instrument catalog…")
	catalog := ui.catalog
	mode := ui.mode
	query := strings.TrimSpace(ui.search.GetText())
	var selectedID models.InstrumentID
	if selected, ok := ui.selectedResult(); ok {
		selectedID = selected.ID
	}
	var underlying models.InstrumentID
	var filter chainFilter
	if ui.chainData != nil {
		underlying = ui.chainData.underlying
		filter = ui.chainData.filter
	}
	if !ui.startOperation() {
		return
	}
	go func() {
		defer ui.wg.Done()
		view, err := reloadAfterRefresh(ui.ctx, catalog, mode, query, selectedID, underlying, filter)
		ui.finishRefresh(view, err)
	}()
}

func reloadAfterRefresh(
	ctx context.Context,
	catalog catalog,
	mode viewMode,
	query string,
	selectedID models.InstrumentID,
	underlying models.InstrumentID,
	filter chainFilter,
) (refreshedView, error) {
	if err := catalog.Refresh(ctx); err != nil {
		return refreshedView{}, err
	}
	lastRefreshed, err := catalog.LastRefreshedAt(ctx)
	if err != nil {
		return refreshedView{}, err
	}
	underlyings, err := catalog.ListFO(ctx)
	if err != nil {
		return refreshedView{}, err
	}
	view := refreshedView{
		lastRefreshed:   lastRefreshed,
		underlyingCount: len(underlyings),
		query:           query,
		underlyings:     query == "",
		selectedID:      selectedID,
	}
	if mode == viewChain {
		data, err := loadChainData(ctx, catalog, underlying)
		if err != nil {
			return refreshedView{}, err
		}
		data.filter = preserveChainFilter(data, filter)
		view.chain = &data
		return view, nil
	}
	if query == "" {
		view.results = underlyings
	} else {
		view.results, err = catalog.Search(ctx, query, searchLimit)
	}
	return view, err
}

func (ui *instrumentApp) finishRefresh(view refreshedView, err error) {
	if ui.ctx.Err() != nil {
		return
	}
	ui.application.QueueUpdateDraw(func() {
		if err != nil {
			ui.showError(err)
			return
		}
		ui.lastRefreshed = view.lastRefreshed
		ui.underlyingCount = view.underlyingCount
		ui.closeOverlay()
		if view.chain != nil {
			ui.chainData = view.chain
			ui.renderChain()
		} else {
			ui.setResults(view.results, view.underlyings, view.query)
			ui.selectResult(view.selectedID)
		}
		ui.setStatus("  [green::b]✔[-] Catalog refreshed from Kite.")
	})
}

func preserveChainFilter(data chainData, previous chainFilter) chainFilter {
	filter := data.filter
	for _, expiry := range data.expiries {
		if expiry.number == previous.expiryNumber {
			filter.expiryNumber = previous.expiryNumber
			break
		}
	}
	filter.minStrike = previous.minStrike
	filter.maxStrike = previous.maxStrike
	filter.side = previous.side
	return filter
}

func (ui *instrumentApp) backToDashboard() {
	if ui.mode != viewChain || ui.busy {
		return
	}
	ui.chainData = nil
	ui.suppressSearch = true
	ui.search.SetText(ui.snapshot.query)
	ui.suppressSearch = false
	ui.setResults(ui.snapshot.results, strings.TrimSpace(ui.snapshot.query) == "", ui.snapshot.query)
	ui.selectResult(ui.snapshot.selectedID)
	ui.setStatus("  Returned to instrument results.")
}

func (ui *instrumentApp) selectResult(id models.InstrumentID) {
	for row := 1; row < ui.results.GetRowCount(); row++ {
		if instrument, ok := tableInstrument(ui.results, row, 0); ok && instrument.ID == id {
			ui.results.Select(row, 0)
			ui.updateDetail(instrument)
			return
		}
	}
}

func (ui *instrumentApp) selectedResult() (models.Instrument, bool) {
	row, column := ui.results.GetSelection()
	return tableInstrument(ui.results, row, column)
}

func (ui *instrumentApp) showUnderlyings() {
	if ui.catalog == nil || ui.busy {
		return
	}
	if ui.mode == viewChain {
		ui.chainData = nil
	}
	ui.suppressSearch = true
	ui.search.SetText("")
	ui.suppressSearch = false
	ui.searchChanged("")
}

func (ui *instrumentApp) focusSearch() {
	if ui.busy {
		return
	}
	if ui.mode == viewChain {
		ui.backToDashboard()
	}
	ui.application.SetFocus(ui.search)
}

func (ui *instrumentApp) updateDetail(instrument models.Instrument) {
	expiryNumber := "—"
	if instrument.ExpiryNumber != nil {
		expiryNumber = strconv.Itoa(*instrument.ExpiryNumber)
	}
	underlying := pointerText(instrument.UnderlyingID)
	listed := "No"
	if instrument.UnderlyingIsListed {
		listed = "Yes"
	}
	fo := "No"
	if instrument.IsFO {
		fo = "Yes"
	}
	ui.detail.SetText(fmt.Sprintf(
		"[aqua::b]%s[-]\n[white]%s[-]\n\n"+
			"[lightskyblue]Identity[-]\n[white]Exchange[-]       %s\n[white]Symbol[-]         %s\n[white]Name[-]           %s\n[white]Token[-]          %d\n[white]Exchange token[-] %s\n\n"+
			"[lightskyblue]Contract[-]\n[white]Type[-]           %s\n[white]Segment[-]        %s\n[white]Expiry[-]         %s\n[white]Expiry number[-]  %s\n[white]Strike[-]         %s\n[white]Tick size[-]      %s\n[white]Lot size[-]       %d\n[white]F&O contract[-]   %s\n\n"+
			"[lightskyblue]Underlying[-]\n[white]ID[-]             %s\n[white]Listed[-]         %s\n[white]Options[-]        %d\n[white]Futures[-]        %d",
		tview.Escape(string(instrument.ID)),
		tview.Escape(instrument.DisplayName),
		tview.Escape(instrument.Exchange),
		tview.Escape(instrument.TradingSymbol),
		tview.Escape(pointerText(instrument.Name)),
		instrument.InstrumentToken,
		tview.Escape(instrument.ExchangeToken),
		tview.Escape(instrument.InstrumentType),
		tview.Escape(instrument.Segment),
		tview.Escape(formatExpiry(instrument.Expiry)),
		expiryNumber,
		formatStrike(instrument.Strike),
		formatStrike(instrument.TickSize),
		instrument.LotSize,
		fo,
		tview.Escape(underlying),
		listed,
		instrument.OptionsCount,
		instrument.FuturesCount,
	))
}

func (ui *instrumentApp) capture(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyCtrlC {
		ui.stop()
		return nil
	}
	if ui.busy {
		if event.Key() == tcell.KeyRune && strings.EqualFold(string(event.Rune()), "q") {
			ui.stop()
		}
		return nil
	}
	if ui.overlay != "" {
		if ui.overlay == modalPage && event.Key() == tcell.KeyRune && strings.EqualFold(string(event.Rune()), "q") {
			ui.stop()
			return nil
		}
		return event
	}
	if ui.application.GetFocus() == ui.search {
		return event
	}
	if event.Key() == tcell.KeyEscape {
		ui.backToDashboard()
		return nil
	}
	if event.Key() != tcell.KeyRune {
		return event
	}
	switch strings.ToLower(string(event.Rune())) {
	case "q":
		ui.stop()
		return nil
	case "/":
		ui.focusSearch()
		return nil
	case "u":
		ui.showUnderlyings()
		return nil
	case "e":
		ui.openExpiryPicker()
		return nil
	case "f":
		ui.openFilters()
		return nil
	case "r":
		ui.refresh()
		return nil
	case "[":
		ui.cycleExpiry(-1)
		return nil
	case "]":
		ui.cycleExpiry(1)
		return nil
	}
	return event
}

func (ui *instrumentApp) showBusy(message string) {
	ui.busy = true
	view := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	view.SetText("\n\n[aqua::b]◆[-]\n\n" + tview.Escape(message) + "\n\n[lightcyan]Please wait…[-]")
	view.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(" Working ").SetTitleColor(tcell.ColorLightCyan)
	ui.showOverlay(busyPage, centered(58, 11, view), view)
}

func (ui *instrumentApp) showStartupError(err error) {
	ui.busy = false
	ui.removeOverlay()
	modal := tview.NewModal().
		SetText("[red::b]Could not open the instrument catalog[-]\n\n" + tview.Escape(err.Error())).
		AddButtons([]string{"Retry", "Quit"}).
		SetDoneFunc(func(index int, _ string) {
			if index == 0 {
				ui.closeOverlay()
				ui.showBusy("Retrying catalog initialization…")
				ui.beginInitialization()
				return
			}
			ui.stop()
		})
	ui.showOverlay(modalPage, modal, modal)
}

func (ui *instrumentApp) showError(err error) {
	ui.busy = false
	ui.removeOverlay()
	modal := tview.NewModal().
		SetText("[red::b]Operation failed[-]\n\n" + tview.Escape(err.Error())).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) { ui.closeOverlay() })
	ui.showOverlay(modalPage, modal, modal)
}

func (ui *instrumentApp) showOverlay(name string, primitive tview.Primitive, focus tview.Primitive) {
	ui.removeOverlay()
	ui.overlay = name
	ui.pages.AddAndSwitchToPage(name, primitive, true)
	ui.application.SetFocus(focus)
}

func (ui *instrumentApp) removeOverlay() {
	if ui.overlay != "" {
		ui.pages.RemovePage(ui.overlay)
	}
	ui.overlay = ""
}

func (ui *instrumentApp) closeOverlay() {
	ui.removeOverlay()
	ui.busy = false
	ui.application.SetFocus(ui.activeTable())
}

func (ui *instrumentApp) activeTable() tview.Primitive {
	if ui.mode == viewChain {
		return ui.chain
	}
	return ui.results
}

func (ui *instrumentApp) updateHeader() {
	ui.updateHeaderWithCount(len(ui.resultItems))
}

func (ui *instrumentApp) updateHeaderWithCount(count int) {
	mode := "F&O UNDERLYINGS"
	if ui.mode == viewChain && ui.chainData != nil {
		mode = "OPTION CHAIN · " + string(ui.chainData.underlying)
	} else if !ui.underlyings {
		mode = "SEARCH RESULTS"
	}
	refreshed := "not refreshed"
	if !ui.lastRefreshed.IsZero() {
		refreshed = ui.lastRefreshed.Local().Format("02 Jan 2006 15:04 MST")
	}
	ui.header.SetText(fmt.Sprintf(
		"[aqua::b]◆[-] [white::b]INSTRUMENTS[-] [lightblue::b]%s[-]  [lightcyan]%d rows · %d underlyings · %s[-]",
		tview.Escape(mode),
		count,
		ui.underlyingCount,
		tview.Escape(refreshed),
	))
}

func (ui *instrumentApp) updateModeControls() {
	chain := ui.mode == viewChain
	ui.expiryButton.SetDisabled(!chain)
	ui.filterButton.SetDisabled(!chain)
	ui.backButton.SetDisabled(!chain)
}

func (ui *instrumentApp) setStatus(text string) {
	ui.status.SetText(text)
}

func (ui *instrumentApp) stop() {
	ui.stopOnce.Do(func() {
		ui.opMu.Lock()
		ui.stopping = true
		ui.cancel()
		ui.searcher.shutdown()
		ui.opMu.Unlock()
		go func() {
			ui.wg.Wait()
			ui.searcher.wait()
			ui.application.Stop()
		}()
	})
}

func (ui *instrumentApp) startOperation() bool {
	ui.opMu.Lock()
	defer ui.opMu.Unlock()
	if ui.stopping {
		return false
	}
	ui.wg.Add(1)
	return true
}

func configureTheme() {
	tview.Styles = tview.Theme{
		PrimitiveBackgroundColor:    tcell.ColorBlack,
		ContrastBackgroundColor:     tcell.ColorDarkBlue,
		MoreContrastBackgroundColor: tcell.ColorDarkCyan,
		BorderColor:                 tcell.ColorLightCyan,
		TitleColor:                  tcell.ColorLightYellow,
		GraphicsColor:               tcell.ColorLightCyan,
		PrimaryTextColor:            tcell.ColorWhite,
		SecondaryTextColor:          tcell.ColorLightYellow,
		TertiaryTextColor:           tcell.ColorLightSkyBlue,
		InverseTextColor:            tcell.ColorBlack,
		ContrastSecondaryTextColor:  tcell.ColorWhite,
	}
}

func newInstrumentTable(title string) *tview.Table {
	table := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0).
		SetEvaluateAllRows(false).
		SetSelectedStyle(tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite).Bold(true))
	table.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(title).SetTitleColor(tcell.ColorLightCyan)
	return table
}

func setHeaders(table *tview.Table, headers []string) {
	for column, header := range headers {
		table.SetCell(0, column, tview.NewTableCell(header).
			SetTextColor(tcell.ColorLightCyan).
			SetAttributes(tcell.AttrBold).
			SetAlign(tview.AlignCenter).
			SetSelectable(false))
	}
}

func tableInstrument(table *tview.Table, row, column int) (models.Instrument, bool) {
	if row <= 0 || column < 0 {
		return models.Instrument{}, false
	}
	reference := table.GetCell(row, column).GetReference()
	instrument, ok := reference.(models.Instrument)
	return instrument, ok
}

func contractCell(text string, instrument models.Instrument, color tcell.Color) *tview.TableCell {
	return tview.NewTableCell(tview.Escape(text)).SetTextColor(color).SetReference(instrument)
}

func emptyCell() *tview.TableCell {
	return tview.NewTableCell("—").SetTextColor(tcell.ColorDarkGray).SetSelectable(false)
}

func instrumentTypeColor(instrumentType string) tcell.Color {
	switch instrumentType {
	case string(models.OptionTypeCall):
		return tcell.ColorLightGreen
	case string(models.OptionTypePut):
		return tcell.ColorLightCoral
	case "FUT":
		return tcell.ColorLightYellow
	default:
		return tcell.ColorLightBlue
	}
}

func actionButton(label string, color tcell.Color, selected func()) *tview.Button {
	return tview.NewButton(tview.Escape(label)).
		SetLabelColor(color).
		SetLabelColorActivated(tcell.ColorBlack).
		SetActivatedStyle(tcell.StyleDefault.Background(color).Foreground(tcell.ColorBlack).Bold(true)).
		SetDisabledStyle(tcell.StyleDefault.Foreground(tcell.ColorDarkGray)).
		SetSelectedFunc(selected)
}

func centered(width, height int, primitive tview.Primitive) tview.Primitive {
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().
			AddItem(nil, 0, 1, false).
			AddItem(primitive, width, 0, true).
			AddItem(nil, 0, 1, false), height, 0, true).
		AddItem(nil, 0, 1, false)
}

func optionalFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return formatStrike(*value)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
