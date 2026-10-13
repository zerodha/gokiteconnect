package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// portfolio is one user's positions, holdings and mutual funds. Each section
// loads independently, so one failing (say, funds on a web session) does not
// hide the others.
type portfolio struct {
	positions    []kite.Position
	holdings     kite.Holdings
	funds        kite.MFHoldings
	positionsErr error
	holdingsErr  error
	fundsErr     error
	loadedAt     time.Time
	// loggedIn reports that loading the portfolio logged the user in again.
	loggedIn bool
	// instruments holds the catalog entries of the positions and of their
	// underlyings, by ID. Positions missing from it show their trading symbol
	// and group under themselves.
	instruments    map[models.InstrumentID]models.Instrument
	instrumentsErr error
	// prices reprices positions and holdings from quotes; pricedAt is when
	// they were last priced and pricesErr why that last failed.
	prices    priceSource
	pricedAt  time.Time
	pricesErr error
}

type portfolioLoader func(context.Context, credentials.Credentials) portfolio

// instrumentResolver looks up the catalog entries of positions and of their
// underlyings.
type instrumentResolver func(context.Context, []kite.Position) (map[models.InstrumentID]models.Instrument, error)

// kitePortfolioLoader reads the portfolio after the manager checks the
// user's stored session with a profile request, logging in and saving a new
// session only when there is none or Kite reports it expired. Positions are
// then named and grouped by the instruments resolve finds.
func kitePortfolioLoader(manager *credentials.UserManager, resolve instrumentResolver) portfolioLoader {
	return func(ctx context.Context, creds credentials.Credentials) portfolio {
		user, loggedIn, err := manager.RefreshUserSessionIfRequired(ctx, creds.UserID)
		if err != nil {
			return failedPortfolio(err)
		}
		client := user.KiteClient()
		result := readPortfolio(client)
		result.loggedIn = loggedIn
		result.prices = client
		result = result.reprice()
		if result.positionsErr == nil && len(result.positions) > 0 && resolve != nil {
			result.instruments, result.instrumentsErr = resolve(ctx, result.positions)
		}
		return result
	}
}

// catalogInstruments resolves instruments from the instrument catalog, which
// open returns (opening it on first use). Instruments the catalog does not
// know are left out.
func catalogInstruments(open catalogFactory) instrumentResolver {
	return func(ctx context.Context, positions []kite.Position) (map[models.InstrumentID]models.Instrument, error) {
		catalog, _, err := open(ctx)
		if err != nil {
			return nil, fmt.Errorf("instrument catalog: %w", err)
		}
		instruments := make(map[models.InstrumentID]models.Instrument, len(positions))
		lookup := func(id models.InstrumentID) (models.Instrument, bool, error) {
			if instrument, done := instruments[id]; done {
				return instrument, true, nil
			}
			instrument, err := catalog.Get(ctx, id)
			if err != nil {
				return models.Instrument{}, false, ctx.Err()
			}
			instruments[id] = instrument
			return instrument, true, nil
		}
		for _, position := range positions {
			instrument, found, err := lookup(positionID(position))
			if err != nil {
				return instruments, err
			}
			if !found || instrument.UnderlyingID == nil {
				continue
			}
			if _, _, err := lookup(*instrument.UnderlyingID); err != nil {
				return instruments, err
			}
		}
		return instruments, nil
	}
}

func positionID(position kite.Position) models.InstrumentID {
	return models.InstrumentID(position.Exchange + ":" + position.Tradingsymbol)
}

// displayName is the catalog's display name for id, or fallback.
func displayName(instruments map[models.InstrumentID]models.Instrument, id models.InstrumentID, fallback string) string {
	if instrument, ok := instruments[id]; ok && strings.TrimSpace(instrument.DisplayName) != "" {
		return instrument.DisplayName
	}
	return fallback
}

// positionGroup is the positions on one underlying.
type positionGroup struct {
	underlying models.InstrumentID
	positions  []kite.Position
	pnl        float64
}

// groupPositions groups positions by underlying, ordered by underlying, with
// positions keeping Kite's order within a group.
func groupPositions(positions []kite.Position, instruments map[models.InstrumentID]models.Instrument) []positionGroup {
	var groups []positionGroup
	index := make(map[models.InstrumentID]int)
	for _, position := range positions {
		key := positionID(position)
		if instrument, ok := instruments[key]; ok && instrument.UnderlyingID != nil {
			key = *instrument.UnderlyingID
		}
		at, ok := index[key]
		if !ok {
			at = len(groups)
			index[key] = at
			groups = append(groups, positionGroup{underlying: key})
		}
		groups[at].positions = append(groups[at].positions, position)
		groups[at].pnl += position.PnL
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].underlying < groups[j].underlying })
	return groups
}

func readPortfolio(client *kite.Client) portfolio {
	var result portfolio
	client.SetAppName("kite-cli")
	positions, err := client.GetPositions()
	result.positions, result.positionsErr = positions.Net, err
	result.holdings, result.holdingsErr = client.GetHoldings()
	result.funds, result.fundsErr = client.GetMFHoldings()
	result.loadedAt = time.Now()
	return result
}

func failedPortfolio(err error) portfolio {
	return portfolio{positionsErr: err, holdingsErr: err, fundsErr: err}
}

type portfolioView struct {
	application *tview.Application
	ctx         context.Context
	load        portfolioLoader

	root      *tview.Flex
	header    *tview.TextView
	positions *tview.Table
	holdings  *tview.Table
	funds     *tview.Table
	detail    *tview.TextView
	status    *tview.TextView

	user       credentials.Credentials
	data       portfolio
	loading    bool
	generation int
	focus      int
	// collapsed holds the underlyings whose positions are hidden under their
	// group row. It lasts across reloads of the same user.
	collapsed map[models.InstrumentID]bool
	// The portfolio reprices every priceEvery while visible; pricing marks a
	// request in flight.
	priceEvery time.Duration
	visible    bool
	pricing    bool
	// notice replaces the help in the status bar once the next load
	// succeeds, such as the order that caused it.
	notice string

	back func()
	quit func()
	// loggedIn runs after a load logged the user in, so the users screen can
	// show the new session.
	loggedIn func()
	// orderMenu opens the order menu; pickUser opens the user picker.
	orderMenu func(tradeTarget)
	pickUser  func()
}

const portfolioHelp = "  [green]O[-] order · [yellow]Tab[-] next table · [yellow]R[-] reload · [yellow]U[-] switch user · [yellow]I[-] instruments · [yellow]Q[-] users · [yellow]Ctrl+C[-] quit"

func newPortfolioView(ctx context.Context, application *tview.Application, load portfolioLoader) *portfolioView {
	view := &portfolioView{application: application, ctx: ctx, load: load}
	view.header = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	view.header.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)
	view.positions = newInstrumentTable(" Positions ")
	view.positions.SetSelectedFunc(func(row, _ int) { view.toggleGroup(row) })
	view.holdings = newInstrumentTable(" Holdings ")
	view.funds = newInstrumentTable(" Mutual funds ")
	for _, table := range view.tables() {
		table.SetSelectionChangedFunc(func(int, int) { view.updateDetail() })
	}
	view.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	view.detail.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(" Details ").SetTitleColor(tcell.ColorLightCyan)

	actions := tview.NewFlex().SetDirection(tview.FlexColumn)
	actions.SetBorder(true).SetBorderColor(tcell.ColorDarkCyan).SetTitle(" Actions ").SetTitleColor(tcell.ColorLightYellow)
	actions.AddItem(actionButton("⇅ Order [O]", tcell.ColorLightGreen, view.openOrderMenu), 0, 1, false)
	actions.AddItem(actionButton("↻ Reload [R]", tcell.ColorLightGreen, view.reload), 0, 1, false)
	actions.AddItem(actionButton("⇄ User [U]", tcell.ColorLightSkyBlue, func() { view.pickUser() }), 0, 1, false)
	actions.AddItem(actionButton("⇥ Next table [Tab]", tcell.ColorLightCyan, func() { view.cycleFocus(1) }), 0, 1, false)
	actions.AddItem(actionButton("← Users [Q]", tcell.ColorLightGray, func() { view.back() }), 0, 1, false)
	actions.AddItem(actionButton("⏻ Quit [Ctrl+C]", tcell.ColorLightCoral, func() { view.quit() }), 0, 1, false)

	view.status = tview.NewTextView().SetDynamicColors(true).SetTextColor(tcell.ColorLightCyan).SetText(portfolioHelp)
	view.status.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)

	tables := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(view.positions, 0, 2, true).
		AddItem(view.holdings, 0, 3, false).
		AddItem(view.funds, 0, 2, false)
	body := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(tables, 0, 3, true).
		AddItem(view.detail, 43, 1, false)
	view.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(view.header, 3, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(actions, 3, 0, false).
		AddItem(view.status, 3, 0, false)
	return view
}

func (view *portfolioView) tables() []*tview.Table {
	return []*tview.Table{view.positions, view.holdings, view.funds}
}

// focused is the table that has, or last had, the focus.
func (view *portfolioView) focused() *tview.Table {
	return view.tables()[view.focus]
}

// open shows user's portfolio, loading it afresh.
func (view *portfolioView) open(user credentials.Credentials) {
	if user.UserID != view.user.UserID {
		view.data = portfolio{}
		view.focus = 0
		view.collapsed = nil
		for _, table := range view.tables() {
			table.Select(1, 0)
		}
	}
	view.user = user
	view.render()
	view.reload()
}

func (view *portfolioView) reload() {
	if view.user.UserID == "" {
		return
	}
	view.generation++
	generation, user, load := view.generation, view.user, view.load
	view.loading = true
	view.renderHeader()
	view.status.SetText(fmt.Sprintf("  Loading the portfolio of [yellow]%s[-] · logs in if the session has expired…", tview.Escape(user.UserID)))
	go func() {
		data := load(view.ctx, user)
		if view.ctx.Err() != nil {
			return
		}
		view.application.QueueUpdateDraw(func() {
			// A newer load, or another user's, supersedes this one.
			if generation != view.generation {
				return
			}
			view.loading = false
			view.data = data
			view.render()
			status := portfolioHelp
			if view.notice != "" {
				status, view.notice = view.notice, ""
			}
			if err := firstError(data.positionsErr, data.holdingsErr, data.fundsErr); err != nil {
				status = "  [red::b]Some sections failed:[-] " + tview.Escape(quoteErrorText(err))
			} else if data.pricesErr != nil {
				status = "  [red::b]Live prices failed, showing Kite's portfolio prices:[-] " + tview.Escape(quoteErrorText(data.pricesErr))
			} else if data.instrumentsErr != nil {
				status = "  [yellow::b]Positions are not grouped:[-] " + tview.Escape(quoteErrorText(data.instrumentsErr))
			}
			if data.loggedIn {
				view.user.LastLogin = time.Now()
				status = "  [green::b]✔[-] Session expired; [yellow]" + tview.Escape(user.UserID) + "[-] was logged in again ·" + status
				if view.loggedIn != nil {
					view.loggedIn()
				}
			}
			view.status.SetText(status)
		})
	}()
}

func (view *portfolioView) capture(event *tcell.EventKey) *tcell.EventKey {
	if page := pageKey(view.application, event); page != nil {
		return page
	}
	switch event.Key() {
	case tcell.KeyTab:
		view.cycleFocus(1)
		return nil
	case tcell.KeyBacktab:
		view.cycleFocus(-1)
		return nil
	case tcell.KeyEscape:
		view.back()
		return nil
	case tcell.KeyRune:
	default:
		return event
	}
	switch strings.ToLower(string(event.Rune())) {
	case "q":
		view.back()
		return nil
	case "r":
		view.reload()
		return nil
	case "o":
		view.openOrderMenu()
		return nil
	case "u":
		if view.pickUser != nil {
			view.pickUser()
		}
		return nil
	}
	return event
}

func (view *portfolioView) cycleFocus(delta int) {
	count := len(view.tables())
	view.focus = ((view.focus+delta)%count + count) % count
	view.application.SetFocus(view.focused())
	view.updateDetail()
}

// render redraws the portfolio, keeping each table's selected row.
func (view *portfolioView) render() {
	selected := make([]int, len(view.tables()))
	for index, table := range view.tables() {
		selected[index], _ = table.GetSelection()
	}
	view.renderHeader()
	view.renderPositions()
	view.renderHoldings()
	view.renderFunds()
	for index, table := range view.tables() {
		if row := selected[index]; row > 1 && row < table.GetRowCount() {
			table.Select(row, 0)
		}
	}
	view.updateDetail()
}

func (view *portfolioView) renderHeader() {
	mode := "WEB"
	if view.user.IsAPIUser {
		mode = "API"
	}
	state := "[darkgray]not loaded[-]"
	switch {
	case view.loading:
		state = "[yellow]loading…[-]"
	case !view.data.pricedAt.IsZero():
		state = "[lightgreen]live " + view.data.pricedAt.Format("15:04:05") + "[-]"
	case !view.data.loadedAt.IsZero():
		state = "[lightcyan]" + view.data.loadedAt.Format("15:04:05") + "[-]"
	}
	positions := sumPositions(view.data.positions)
	invested, current := sumHoldings(view.data.holdings)
	fundsInvested, fundsCurrent := sumFunds(view.data.funds)
	view.header.SetText(fmt.Sprintf(
		"[aqua::b]◆[-] [white::b]PORTFOLIO[-] [lightblue::b]%s[-] %s [darkgray]%s[-]  ·  positions %s  ·  holdings %s  ·  funds %s  ·  %s",
		tview.Escape(view.user.UserID),
		tview.Escape(fallback(view.user.UserName, "")),
		mode,
		signedMarkup(positions),
		signedMarkup(current-invested),
		signedMarkup(fundsCurrent-fundsInvested),
		state,
	))
}

func (view *portfolioView) renderPositions() {
	table := view.positions
	table.Clear()
	setHeaders(table, []string{"INSTRUMENT", "EXCH", "PRODUCT", "QTY", "AVG", "LTP", "P&L"})
	if !sectionReady(table, " Positions ", view.data.positionsErr, len(view.data.positions), "No open positions") {
		return
	}
	groups := groupPositions(view.data.positions, view.data.instruments)
	row := 1
	for _, group := range groups {
		indent := ""
		// A lone position needs no group row.
		if len(group.positions) > 1 {
			// The group row carries the underlying and its subtotal P&L; Enter
			// on it collapses or expands the group.
			marker := "▾"
			if view.collapsed[group.underlying] {
				marker = "▸"
			}
			setRow(table, row, []*tview.TableCell{
				textCell(fmt.Sprintf("%s %s (%d)", marker, displayName(view.data.instruments, group.underlying, string(group.underlying)), len(group.positions)), tcell.ColorYellow).
					SetAttributes(tcell.AttrBold).SetReference(group.underlying),
				textCell("", tcell.ColorDefault),
				textCell("", tcell.ColorDefault),
				textCell("", tcell.ColorDefault),
				textCell("", tcell.ColorDefault),
				textCell("", tcell.ColorDefault),
				signedCell(group.pnl).SetAttributes(tcell.AttrBold),
			})
			row++
			if view.collapsed[group.underlying] {
				continue
			}
			indent = "  "
		}
		for _, position := range group.positions {
			setRow(table, row, []*tview.TableCell{
				textCell(indent+displayName(view.data.instruments, positionID(position), position.Tradingsymbol), tcell.ColorLightCyan).
					SetReference(position),
				textCell(position.Exchange, tcell.ColorLightBlue),
				textCell(position.Product, tcell.ColorLightGray),
				numberCell(fmt.Sprintf("%d", position.Quantity), quantityColor(position.Quantity)),
				numberCell(formatAmount(position.AveragePrice), tcell.ColorWhite),
				numberCell(formatAmount(position.LastPrice), tcell.ColorWhite),
				signedCell(position.PnL),
			})
			row++
		}
	}
	table.SetTitle(fmt.Sprintf(" Positions · %d in %d underlyings · P&L %s ",
		len(view.data.positions), len(groups), formatSigned(sumPositions(view.data.positions))))
}

// toggleGroup collapses or expands the group whose header is at row,
// keeping that header selected.
func (view *portfolioView) toggleGroup(row int) {
	underlying, ok := view.positions.GetCell(row, 0).GetReference().(models.InstrumentID)
	if !ok {
		return
	}
	if view.collapsed == nil {
		view.collapsed = make(map[models.InstrumentID]bool)
	}
	view.collapsed[underlying] = !view.collapsed[underlying]
	view.renderPositions()
	for candidate := 1; candidate < view.positions.GetRowCount(); candidate++ {
		if view.positions.GetCell(candidate, 0).GetReference() == underlying {
			view.positions.Select(candidate, 0)
			break
		}
	}
	view.updateDetail()
}

func (view *portfolioView) renderHoldings() {
	table := view.holdings
	table.Clear()
	setHeaders(table, []string{"SYMBOL", "QTY", "AVG", "LTP", "INVESTED", "CURRENT", "P&L", "P&L%", "DAY%"})
	if !sectionReady(table, " Holdings ", view.data.holdingsErr, len(view.data.holdings), "No holdings") {
		return
	}
	for index, holding := range view.data.holdings {
		quantity := float64(holding.Quantity + holding.T1Quantity)
		invested, current := holding.AveragePrice*quantity, holding.LastPrice*quantity
		setRow(table, index+1, []*tview.TableCell{
			textCell(holding.Tradingsymbol, tcell.ColorLightCyan).SetReference(holding),
			numberCell(fmt.Sprintf("%.0f", quantity), tcell.ColorWhite),
			numberCell(formatAmount(holding.AveragePrice), tcell.ColorWhite),
			numberCell(formatAmount(holding.LastPrice), tcell.ColorWhite),
			numberCell(formatAmount(invested), tcell.ColorLightGray),
			numberCell(formatAmount(current), tcell.ColorWhite),
			signedCell(current - invested),
			percentCell(current-invested, invested),
			percentCell(holding.DayChangePercentage, 100),
		})
	}
	invested, current := sumHoldings(view.data.holdings)
	table.SetTitle(fmt.Sprintf(" Holdings · %d · invested %s · current %s · P&L %s (%s) ",
		len(view.data.holdings), formatAmount(invested), formatAmount(current), formatSigned(current-invested), formatPercent(current-invested, invested)))
}

func (view *portfolioView) renderFunds() {
	table := view.funds
	table.Clear()
	setHeaders(table, []string{"FUND", "FOLIO", "UNITS", "AVG", "NAV", "INVESTED", "CURRENT", "P&L", "P&L%"})
	if !sectionReady(table, " Mutual funds ", view.data.fundsErr, len(view.data.funds), "No mutual fund holdings") {
		return
	}
	for index, fund := range view.data.funds {
		invested, current := fund.AveragePrice*fund.Quantity, fund.LastPrice*fund.Quantity
		setRow(table, index+1, []*tview.TableCell{
			textCell(fallback(fund.Fund, fund.Tradingsymbol), tcell.ColorLightCyan).SetMaxWidth(40).SetReference(fund),
			textCell(fund.Folio, tcell.ColorLightGray),
			numberCell(fmt.Sprintf("%.3f", fund.Quantity), tcell.ColorWhite),
			numberCell(formatAmount(fund.AveragePrice), tcell.ColorWhite),
			numberCell(formatAmount(fund.LastPrice), tcell.ColorWhite),
			numberCell(formatAmount(invested), tcell.ColorLightGray),
			numberCell(formatAmount(current), tcell.ColorWhite),
			signedCell(current - invested),
			percentCell(current-invested, invested),
		})
	}
	invested, current := sumFunds(view.data.funds)
	table.SetTitle(fmt.Sprintf(" Mutual funds · %d · invested %s · current %s · P&L %s (%s) ",
		len(view.data.funds), formatAmount(invested), formatAmount(current), formatSigned(current-invested), formatPercent(current-invested, invested)))
}

// sectionReady titles a table and fills it with an error, loading or empty
// row. It reports whether the table should be filled with rows.
func sectionReady(table *tview.Table, title string, err error, count int, empty string) bool {
	table.SetTitle(title)
	message, color := "", tcell.ColorLightGray
	switch {
	case err != nil:
		message, color = quoteErrorText(err), tcell.ColorRed
		table.SetTitle(title + "· failed ")
	case count == 0:
		message = empty
	default:
		table.Select(1, 0)
		return true
	}
	table.SetCell(1, 0, tview.NewTableCell(tview.Escape(message)).SetTextColor(color).SetSelectable(false).SetExpansion(1))
	return false
}

func setRow(table *tview.Table, row int, cells []*tview.TableCell) {
	for column, cell := range cells {
		table.SetCell(row, column, cell)
	}
}

func textCell(text string, color tcell.Color) *tview.TableCell {
	return tview.NewTableCell(tview.Escape(text)).SetTextColor(color)
}

func numberCell(text string, color tcell.Color) *tview.TableCell {
	return tview.NewTableCell(text).SetTextColor(color).SetAlign(tview.AlignRight)
}

func signedCell(value float64) *tview.TableCell {
	return numberCell(formatSigned(value), signColor(value))
}

func percentCell(value, base float64) *tview.TableCell {
	return numberCell(formatPercent(value, base), signColor(value))
}

func quantityColor(quantity int) tcell.Color {
	switch {
	case quantity > 0:
		return tcell.ColorLightGreen
	case quantity < 0:
		return tcell.ColorLightCoral
	}
	return tcell.ColorDarkGray
}

func signColor(value float64) tcell.Color {
	switch {
	case value > 0.005:
		return tcell.ColorLightGreen
	case value < -0.005:
		return tcell.ColorLightCoral
	}
	return tcell.ColorLightGray
}

func signedMarkup(value float64) string {
	return fmt.Sprintf("[%s]%s[-]", colorName(signColor(value)), formatSigned(value))
}

func sumPositions(positions []kite.Position) float64 {
	var pnl float64
	for _, position := range positions {
		pnl += position.PnL
	}
	return pnl
}

func sumHoldings(holdings kite.Holdings) (invested, current float64) {
	for _, holding := range holdings {
		quantity := float64(holding.Quantity + holding.T1Quantity)
		invested += holding.AveragePrice * quantity
		current += holding.LastPrice * quantity
	}
	return invested, current
}

func sumFunds(funds kite.MFHoldings) (invested, current float64) {
	for _, fund := range funds {
		invested += fund.AveragePrice * fund.Quantity
		current += fund.LastPrice * fund.Quantity
	}
	return invested, current
}

func formatSigned(value float64) string {
	if formatted := formatAmount(value); value > 0 && formatted != "0.00" {
		return "+" + formatted
	} else {
		return formatted
	}
}

func formatPercent(value, base float64) string {
	if base == 0 {
		return "—"
	}
	percent := value / base * 100
	if percent > 0 {
		return fmt.Sprintf("+%.2f%%", percent)
	}
	return fmt.Sprintf("%.2f%%", percent)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
