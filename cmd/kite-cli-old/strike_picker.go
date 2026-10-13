package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const strikePickerPage = "strike-picker"

// Strike picker columns.
const (
	strikeColumn = iota
	strikeLTPColumn
	strikeChangeColumn
	strikeMoneynessColumn
	strikeBreakevenColumn
	strikeLotColumn
	strikeOIColumn
)

// Row backgrounds: in-the-money strikes are tinted, and the spot price sits
// between strikes on a band of its own.
var (
	itmColor  = tcell.NewHexColor(0x2a2a1c)
	spotColor = tcell.NewHexColor(0x16323f)
)

// strikePicker chooses an option to buy or sell: one expiry of an
// underlying's calls or puts at a time, priced live, with the at-the-money
// strike selected. Enter orders the selected strike.
type strikePicker struct {
	application *tview.Application
	pages       *tview.Pages
	parent      context.Context
	// open opens the instrument catalog; every reads interval prices refresh.
	open  catalogFactory
	every time.Duration
	// choose orders the chosen option; closed gives the screen its focus
	// back when the picker is cancelled.
	choose func(option models.Instrument, side string, ltp float64)
	closed func()

	visible    bool
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
	loading  bool
	fetching bool
	// atm selects the at-the-money strike once the spot price is known.
	atm bool
	// ctx is done once the picker closes, stopping its price requests.
	ctx    context.Context
	cancel context.CancelFunc

	header  *tview.TextView
	tabs    *tview.TextView
	table   *tview.Table
	footer  *tview.TextView
	content *tview.Flex
}

func newStrikePicker(parent context.Context, application *tview.Application, pages *tview.Pages, open catalogFactory) *strikePicker {
	picker := &strikePicker{application: application, pages: pages, parent: parent, open: open, every: 5 * time.Second}
	picker.header = tview.NewTextView().SetDynamicColors(true)
	picker.tabs = tview.NewTextView().SetDynamicColors(true)
	picker.footer = tview.NewTextView().SetDynamicColors(true)
	picker.table = tview.NewTable().SetFixed(1, 0).SetSelectable(true, false).
		SetSelectedStyle(tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite).Bold(true))
	picker.table.SetSelectionChangedFunc(func(int, int) { picker.renderFooter() })
	picker.table.SetSelectedFunc(func(row, _ int) { picker.chooseRow(row) })
	picker.content = modalFrame(tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(picker.header, 1, 0, false).
		AddItem(picker.tabs, 2, 0, false).
		AddItem(picker.table, 0, 1, true).
		AddItem(picker.footer, 2, 0, false))
	return picker
}

// show opens the picker for side (buy or sell) of underlying's calls or
// puts. source prices the strikes, or load connects one; both nil leaves
// them unpriced.
func (picker *strikePicker) show(underlying models.InstrumentID, side string, optionType models.OptionType, source quoteSource, load quoteLoader) {
	ctx, cancel := context.WithCancel(picker.parent)
	picker.ctx, picker.cancel = ctx, cancel
	picker.visible, picker.loading, picker.fetching, picker.atm = true, true, false, true
	picker.side, picker.optionType, picker.source, picker.err = side, optionType, source, nil
	picker.underlying = models.Instrument{ID: underlying, TradingSymbol: symbolOf(underlying)}
	picker.options, picker.expiries, picker.expiry, picker.quotes = nil, nil, 0, nil
	picker.table.Clear()
	picker.content.SetTitle(fmt.Sprintf(" %s %s ", sideWord(side), optionWord(optionType)))
	picker.render()
	// Fill a small terminal, up to a comfortable size.
	_, _, width, height := picker.pages.GetRect()
	picker.pages.AddPage(strikePickerPage, centered(min(100, max(width-2, 60)), min(32, max(height-2, 16)), picker.content), true, true)
	picker.application.SetFocus(picker.table)

	open := picker.open
	go func() {
		catalog, _, err := open(ctx)
		var instrument models.Instrument
		var options []models.Instrument
		if err == nil {
			if instrument, err = catalog.Get(ctx, underlying); errors.Is(err, models.ErrNotFound) {
				instrument, err = models.Instrument{ID: underlying, TradingSymbol: symbolOf(underlying)}, nil
			}
		}
		if err == nil {
			options, err = catalog.GetOptions(ctx, models.OptionsFilter{UnderlyingID: underlying, Types: []models.OptionType{optionType}})
		}
		if err == nil && source == nil && load != nil {
			// Unpriced strikes are still worth picking from.
			source, _, _ = load(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		picker.application.QueueUpdateDraw(func() {
			if ctx.Err() != nil {
				return
			}
			picker.loading, picker.err, picker.source = false, err, source
			if err == nil {
				picker.underlying = instrument
				picker.setOptions(options)
			}
			picker.render()
			picker.fetch(ctx)
		})
		picker.poll(ctx)
	}()
}

func symbolOf(id models.InstrumentID) string {
	if _, symbol, found := strings.Cut(string(id), ":"); found {
		return symbol
	}
	return string(id)
}

func sideWord(side string) string {
	if side == kite.TransactionTypeSell {
		return "Sell"
	}
	return "Buy"
}

func optionWord(optionType models.OptionType) string {
	if optionType == models.OptionTypePut {
		return "put"
	}
	return "call"
}

// setOptions keeps the unexpired contracts of the picker's type and lists
// their expiries, showing the nearest.
func (picker *strikePicker) setOptions(options []models.Instrument) {
	today := expiryDay(time.Now())
	seen := map[time.Time]bool{}
	picker.options = picker.options[:0]
	for _, option := range options {
		if option.InstrumentType != string(picker.optionType) || option.Expiry == nil || expiryDay(*option.Expiry).Before(today) {
			continue
		}
		picker.options = append(picker.options, option)
		if day := expiryDay(*option.Expiry); !seen[day] {
			seen[day] = true
			picker.expiries = append(picker.expiries, day)
		}
	}
	sort.Slice(picker.expiries, func(i, j int) bool { return picker.expiries[i].Before(picker.expiries[j]) })
	sort.SliceStable(picker.options, func(i, j int) bool { return picker.options[i].Strike < picker.options[j].Strike })
}

func expiryDay(expiry time.Time) time.Time {
	return time.Date(expiry.Year(), expiry.Month(), expiry.Day(), 0, 0, 0, 0, time.UTC)
}

// strikes are the shown expiry's contracts, by strike.
func (picker *strikePicker) strikes() []models.Instrument {
	if picker.expiry >= len(picker.expiries) {
		return nil
	}
	day := picker.expiries[picker.expiry]
	var strikes []models.Instrument
	for _, option := range picker.options {
		if expiryDay(*option.Expiry).Equal(day) {
			strikes = append(strikes, option)
		}
	}
	return strikes
}

func (picker *strikePicker) spot() float64 {
	return picker.quotes[string(picker.underlying.ID)].LastPrice
}

// poll refreshes the prices every interval while the picker is open.
func (picker *strikePicker) poll(ctx context.Context) {
	if picker.every <= 0 {
		return
	}
	ticker := time.NewTicker(picker.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			picker.application.QueueUpdateDraw(func() {
				if ctx.Err() == nil {
					picker.fetch(ctx)
				}
			})
		}
	}
}

// fetch prices the underlying and the shown expiry's strikes.
func (picker *strikePicker) fetch(ctx context.Context) {
	if picker.source == nil || picker.fetching || picker.loading || picker.err != nil {
		return
	}
	ids := []string{string(picker.underlying.ID)}
	for _, option := range picker.strikes() {
		ids = append(ids, string(option.ID))
	}
	picker.fetching = true
	source := picker.source
	go func() {
		quotes, err := source.GetQuote(ids...)
		if ctx.Err() != nil {
			return
		}
		picker.application.QueueUpdateDraw(func() {
			if ctx.Err() != nil {
				return
			}
			picker.fetching = false
			if err != nil {
				picker.footer.SetText("[red::b]Prices failed:[-:-:-] [red]" + tview.Escape(quoteErrorText(err)) + "[-]")
				return
			}
			if picker.quotes == nil {
				picker.quotes = make(map[string]kite.QuoteData, len(quotes))
			}
			for id, quote := range quotes {
				picker.quotes[id] = quote
			}
			picker.render()
		})
	}()
}

// capture handles the picker's keys: Left, Right, [ and ] change expiry, R
// refreshes prices, Escape or Q goes back, and the table takes the rest.
func (picker *strikePicker) capture(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEscape:
		picker.close()
		return nil
	case tcell.KeyLeft:
		picker.cycleExpiry(-1)
		return nil
	case tcell.KeyRight:
		picker.cycleExpiry(1)
		return nil
	case tcell.KeyRune:
		switch event.Rune() {
		case '[':
			picker.cycleExpiry(-1)
		case ']':
			picker.cycleExpiry(1)
		case 'r', 'R':
			picker.fetch(picker.ctx)
		case 'q', 'Q':
			picker.close()
		default:
			return event
		}
		return nil
	}
	return event
}

func (picker *strikePicker) cycleExpiry(delta int) {
	if len(picker.expiries) == 0 {
		return
	}
	next := min(max(picker.expiry+delta, 0), len(picker.expiries)-1)
	if next == picker.expiry {
		return
	}
	picker.expiry, picker.atm = next, true
	picker.render()
	picker.fetch(picker.ctx)
}

func (picker *strikePicker) close() {
	if !picker.visible {
		return
	}
	picker.hide()
	if picker.closed != nil {
		picker.closed()
	}
}

func (picker *strikePicker) hide() {
	picker.visible = false
	if picker.cancel != nil {
		picker.cancel()
	}
	picker.pages.RemovePage(strikePickerPage)
}

// chooseRow orders the strike on row.
func (picker *strikePicker) chooseRow(row int) {
	option, ok := picker.table.GetCell(row, strikeColumn).GetReference().(models.Instrument)
	if !ok {
		return
	}
	picker.hide()
	if picker.choose != nil {
		picker.choose(option, picker.side, picker.quotes[string(option.ID)].LastPrice)
	}
}

func (picker *strikePicker) selected() (models.Instrument, bool) {
	row, _ := picker.table.GetSelection()
	option, ok := picker.table.GetCell(row, strikeColumn).GetReference().(models.Instrument)
	return option, ok
}

// render redraws the picker, keeping the selected strike, or selecting the
// at-the-money strike once the spot price is known.
func (picker *strikePicker) render() {
	picker.renderHeader()
	picker.renderTabs()
	previous, hadSelection := picker.selected()
	picker.renderTable()
	strikes := picker.strikes()
	switch spot := picker.spot(); {
	case picker.atm && spot > 0 && len(strikes) > 0:
		picker.selectStrike(nearestOption(strikes, spot).Strike)
		picker.atm = false
	case hadSelection:
		picker.selectStrike(previous.Strike)
	case len(strikes) > 0:
		picker.selectStrike(strikes[len(strikes)/2].Strike)
	}
	picker.renderFooter()
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

func (picker *strikePicker) selectStrike(strike float64) {
	for row := 1; row < picker.table.GetRowCount(); row++ {
		if option, ok := picker.table.GetCell(row, strikeColumn).GetReference().(models.Instrument); ok && option.Strike == strike {
			picker.table.Select(row, 0)
			// Keep a few strikes visible either side of the selection.
			_, _, _, height := picker.table.GetInnerRect()
			picker.table.SetOffset(max(row-max(height/2, 1), 0), 0)
			return
		}
	}
}

func (picker *strikePicker) renderHeader() {
	color := "green"
	if picker.side == kite.TransactionTypeSell {
		color = "red"
	}
	name := fallback(strings.TrimSpace(picker.underlying.DisplayName), picker.underlying.TradingSymbol)
	text := fmt.Sprintf("[black:%s:b] %s %s [-:-:-] [white::b]%s[-:-:-]", color, strings.ToUpper(sideWord(picker.side)),
		strings.ToUpper(optionWord(picker.optionType)), tview.Escape(name))
	if quote, ok := picker.quotes[string(picker.underlying.ID)]; ok && quote.LastPrice > 0 {
		change, changeColor := formatChange(quote)
		text += fmt.Sprintf("  spot [aqua::b]%s[-:-:-] [%s]%s[-]", formatAmount(quote.LastPrice), colorName(changeColor), change)
	}
	if strikes := picker.strikes(); len(strikes) > 0 {
		text += fmt.Sprintf("  [darkgray]· lot %d[-]", max(strikes[0].LotSize, 1))
	}
	picker.header.SetText(text)
}

// renderTabs lists the expiries, the shown one highlighted, with how many
// days each has left.
func (picker *strikePicker) renderTabs() {
	if len(picker.expiries) == 0 {
		picker.tabs.SetText("")
		return
	}
	const window = 6
	first := min(max(picker.expiry-window/2, 0), max(len(picker.expiries)-window, 0))
	last := min(first+window, len(picker.expiries))
	var text strings.Builder
	if first > 0 {
		text.WriteString("[darkgray]◀[-] ")
	} else {
		text.WriteString("  ")
	}
	today := expiryDay(time.Now())
	for index := first; index < last; index++ {
		day := picker.expiries[index]
		label := fmt.Sprintf(" %s · %s ", day.Format("02 Jan"), daysLeft(day, today))
		if index == picker.expiry {
			fmt.Fprintf(&text, "[black:aqua:b]%s[-:-:-] ", label)
		} else {
			fmt.Fprintf(&text, "[lightgray]%s[-] ", label)
		}
	}
	if last < len(picker.expiries) {
		text.WriteString("[darkgray]▶[-]")
	}
	fmt.Fprintf(&text, "\n[darkgray]  expiry %d of %d[-]", picker.expiry+1, len(picker.expiries))
	picker.tabs.SetText(text.String())
}

func daysLeft(expiry, today time.Time) string {
	days := int(expiry.Sub(today).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "1d"
	}
	return fmt.Sprintf("%dd", days)
}

func (picker *strikePicker) renderTable() {
	picker.table.Clear()
	headers := []string{"Strike", "LTP", "Chg", "Moneyness", "Breakeven", "Per lot", "OI"}
	for column, title := range headers {
		align := tview.AlignRight
		if column == strikeMoneynessColumn {
			align = tview.AlignLeft
		}
		picker.table.SetCell(0, column, tview.NewTableCell(" "+title+" ").SetTextColor(tcell.ColorLightSkyBlue).
			SetAttributes(tcell.AttrBold).SetAlign(align).SetSelectable(false).SetExpansion(1))
	}
	switch {
	case picker.loading:
		picker.message("Loading strikes…")
		return
	case picker.err != nil:
		picker.message("[red]" + tview.Escape(picker.err.Error()) + "[-]")
		return
	}
	strikes := picker.strikes()
	if len(strikes) == 0 {
		picker.message(fmt.Sprintf("No %ss listed for %s", optionWord(picker.optionType), tview.Escape(string(picker.underlying.ID))))
		return
	}
	spot := picker.spot()
	var largestOI float64
	for _, option := range strikes {
		largestOI = max(largestOI, picker.quotes[string(option.ID)].OI)
	}
	call := picker.optionType == models.OptionTypeCall
	atm := nearestOption(strikes, spot).Strike
	spotShown := spot <= 0
	row := 1
	for _, option := range strikes {
		if !spotShown && option.Strike > spot {
			picker.spotRow(row, spot)
			row++
			spotShown = true
		}
		picker.strikeRow(row, option, spot, atm, largestOI, call)
		row++
	}
	if !spotShown {
		picker.spotRow(row, spot)
	}
}

func (picker *strikePicker) message(text string) {
	picker.table.SetCell(1, 0, tview.NewTableCell(" "+text).SetSelectable(false).SetExpansion(1))
}

// spotRow marks where the spot price falls between strikes.
func (picker *strikePicker) spotRow(row int, spot float64) {
	for column := strikeColumn; column <= strikeOIColumn; column++ {
		text := ""
		switch column {
		case strikeColumn:
			text = "[aqua::b]" + formatAmount(spot) + "[-:-:-] "
		case strikeLTPColumn:
			text = "[aqua]◀ spot[-]"
		}
		picker.table.SetCell(row, column, tview.NewTableCell(text).SetAlign(tview.AlignRight).
			SetBackgroundColor(spotColor).SetSelectable(false).SetExpansion(1))
	}
}

func (picker *strikePicker) strikeRow(row int, option models.Instrument, spot, atm, largestOI float64, call bool) {
	quote, priced := picker.quotes[string(option.ID)]
	itm := spot > 0 && ((call && option.Strike < spot) || (!call && option.Strike > spot))
	background := tcell.ColorDefault
	if itm {
		background = itmColor
	}
	cell := func(column int, text string) *tview.TableCell {
		align := tview.AlignRight
		if column == strikeMoneynessColumn {
			align = tview.AlignLeft
		}
		return tview.NewTableCell(text).SetAlign(align).SetExpansion(1).SetBackgroundColor(background)
	}
	strike := formatStrike(option.Strike) + " "
	if option.Strike == atm && spot > 0 {
		strike = "[yellow::b]" + strike + "[-:-:-]"
	} else {
		strike = "[white::b]" + strike + "[-:-:-]"
	}
	picker.table.SetCell(row, strikeColumn, cell(strikeColumn, strike).SetReference(option))

	ltp, change, changeColor, breakevenText, lot, oi := "—", "", tcell.ColorDarkGray, "—", "—", ""
	if priced && quote.LastPrice > 0 {
		ltp = formatPrice(quote.LastPrice)
		change, changeColor = formatChange(quote)
		value := breakeven(option, quote.LastPrice)
		breakevenText = formatPrice(value)
		if spot > 0 {
			breakevenText += " [darkgray]" + formatBreakeven(value, spot, true) + "[-]"
		}
		lot = formatAmount(quote.LastPrice * float64(max(option.LotSize, 1)))
	}
	if priced {
		oi = oiBar(formatCount(quote.OI), quote.OI, largestOI, call)
	}
	picker.table.SetCell(row, strikeLTPColumn, cell(strikeLTPColumn, ltp))
	picker.table.SetCell(row, strikeChangeColumn, cell(strikeChangeColumn, change).SetTextColor(changeColor))
	picker.table.SetCell(row, strikeMoneynessColumn, cell(strikeMoneynessColumn, " "+moneyness(option.Strike, spot, atm, itm)))
	picker.table.SetCell(row, strikeBreakevenColumn, cell(strikeBreakevenColumn, breakevenText))
	picker.table.SetCell(row, strikeLotColumn, cell(strikeLotColumn, lot))
	picker.table.SetCell(row, strikeOIColumn, cell(strikeOIColumn, oi))
}

// moneyness names a strike ATM, ITM or OTM with its distance from spot.
func moneyness(strike, spot, atm float64, itm bool) string {
	if spot <= 0 {
		return ""
	}
	distance := math.Abs(strike-spot) / spot * 100
	switch {
	case strike == atm:
		return fmt.Sprintf("[yellow::b]ATM[-:-:-] [darkgray]%.2f%%[-]", distance)
	case itm:
		return fmt.Sprintf("[khaki]ITM[-] [darkgray]%.2f%%[-]", distance)
	}
	return fmt.Sprintf("[lightgray]OTM[-] [darkgray]%.2f%%[-]", distance)
}

// renderFooter describes the selected strike and lists the keys.
func (picker *strikePicker) renderFooter() {
	keys := "[darkgray]↑↓ strike · ←→ or [ ] expiry · Enter order · R refresh · Esc back[-]"
	option, ok := picker.selected()
	if !ok {
		picker.footer.SetText("\n" + keys)
		return
	}
	text := "[white::b]" + tview.Escape(option.TradingSymbol) + "[-:-:-]"
	if quote := picker.quotes[string(option.ID)]; quote.LastPrice > 0 {
		lots := max(option.LotSize, 1)
		text += fmt.Sprintf(" · %d × %s = [yellow]₹%s[-] per lot · breakeven %s", lots, formatPrice(quote.LastPrice),
			formatAmount(quote.LastPrice*float64(lots)), formatPrice(breakeven(option, quote.LastPrice)))
	}
	picker.footer.SetText(text + "\n" + keys)
}
