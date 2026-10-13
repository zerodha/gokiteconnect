package main

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/rivo/tview"
)

// marginSource prices orders with Kite's margin calculator. The data user's
// *kite.Client implements it; a quote source without it shows no margins.
type marginSource interface {
	GetOrderMargins(kite.GetMarginParams) ([]kite.OrderMargins, error)
}

// marginTTL is how long a contract's margin is reused before it is fetched
// again. Margins follow the price, so they go stale with it.
const marginTTL = time.Minute

// marginDelay is how long a contract stays selected before its margin is
// fetched, so scrolling through the chain does not request every row.
const marginDelay = 300 * time.Millisecond

// lotMargin is the margin Kite blocks for one lot of a contract.
type lotMargin struct {
	total     float64
	err       error
	fetchedAt time.Time
}

// breakeven is the underlying price at expiry at which buying option for
// premium neither gains nor loses: strike plus premium for a call, strike
// less premium for a put.
func breakeven(option models.Instrument, premium float64) float64 {
	if option.InstrumentType == string(models.OptionTypePut) {
		return option.Strike - premium
	}
	return option.Strike + premium
}

// formatBreakeven shows a breakeven as a price or, in percent, as the move
// from spot it needs. A percentage needs the underlying's price.
func formatBreakeven(value, spot float64, percent bool) string {
	if !percent {
		return formatPrice(value)
	}
	if spot <= 0 {
		return "—"
	}
	move := (value - spot) / spot * 100
	if move > 0 {
		return fmt.Sprintf("+%.2f%%", move)
	}
	return fmt.Sprintf("%.2f%%", move)
}

func (ui *instrumentApp) chainHeaders() []string {
	breakevenHeader := "BE"
	if ui.breakevenPercent {
		breakevenHeader = "BE%"
	}
	return []string{"CALL OI", "CALL " + breakevenHeader, "CALL LTP", "STRIKE", "PUT LTP", "PUT " + breakevenHeader, "PUT OI"}
}

// toggleBreakevenFormat switches the chain's breakevens between prices and
// percentages from spot.
func (ui *instrumentApp) toggleBreakevenFormat() {
	if ui.mode != viewChain || ui.chainData == nil {
		return
	}
	ui.breakevenPercent = !ui.breakevenPercent
	setHeaders(ui.chain, ui.chainHeaders())
	ui.applyChainQuotes()
}

// spotPrice is the chain underlying's last price, or 0 before it is quoted.
func (ui *instrumentApp) spotPrice() float64 {
	if ui.chainData == nil {
		return 0
	}
	return ui.quotes[string(ui.chainData.underlying)].LastPrice
}

// updateChainSummary shows the selected contract's per-lot premium and
// margin in the status bar, fetching the margin when it is not cached.
func (ui *instrumentApp) updateChainSummary() {
	if ui.mode != viewChain || ui.chainData == nil || ui.busy {
		return
	}
	hint := fmt.Sprintf(" · [yellow]%%[-] BE format · [yellow]%s[-] expiry", tview.Escape("[ / ]"))
	instrument, ok := ui.selectedInstrument()
	if !ok {
		ui.setStatus("  Select a contract to see its premium and margin per lot" + hint)
		return
	}
	lot := max(instrument.LotSize, 1)
	price := math.NaN()
	if quote, ok := ui.quotes[string(instrument.ID)]; ok && quote.LastPrice > 0 {
		price = quote.LastPrice
	}
	value := "—"
	if !math.IsNaN(price) {
		value = "₹" + formatAmount(price*float64(lot))
	}
	text := fmt.Sprintf("  [aqua::b]%s[-:-:-] · lot %d", tview.Escape(instrument.TradingSymbol), lot)
	switch instrument.InstrumentType {
	case string(models.OptionTypeCall), string(models.OptionTypePut):
		text += " · premium/lot [white::b]" + value + "[-:-:-]"
		text += " · sell margin/lot " + ui.marginText(instrument)
		if !math.IsNaN(price) {
			text += " · BE " + formatPrice(breakeven(instrument, price))
			if spot := ui.spotPrice(); spot > 0 {
				text += " (" + formatBreakeven(breakeven(instrument, price), spot, true) + ")"
			}
		}
	default:
		text += " · value/lot [white::b]" + value + "[-:-:-]"
		text += " · margin/lot " + ui.marginText(instrument)
	}
	ui.setStatus(text + hint)
}

// marginText is the cached margin for one lot of instrument, starting a
// fetch when there is none or it has gone stale.
func (ui *instrumentApp) marginText(instrument models.Instrument) string {
	source, ok := ui.quoteSource.(marginSource)
	if !ok {
		return "[darkgray]—[-]"
	}
	margin, cached := ui.margins[instrument.ID]
	if !cached || time.Since(margin.fetchedAt) > marginTTL {
		ui.fetchMargin(source, instrument)
	}
	switch {
	case !cached:
		return "[darkgray]loading…[-]"
	case margin.err != nil:
		return "[red]" + tview.Escape(quoteErrorText(margin.err)) + "[-]"
	}
	return "[white::b]₹" + formatAmount(margin.total) + "[-:-:-]"
}

// marginWanted is the contract whose margin the summary bar shows. Pending
// fetches for any other contract are dropped.
type marginWanted struct {
	mu sync.Mutex
	id models.InstrumentID
}

func (wanted *marginWanted) set(id models.InstrumentID) {
	wanted.mu.Lock()
	defer wanted.mu.Unlock()
	wanted.id = id
}

func (wanted *marginWanted) is(id models.InstrumentID) bool {
	wanted.mu.Lock()
	defer wanted.mu.Unlock()
	return wanted.id == id
}

// fetchMargin asks Kite for the margin of one lot: sold for an option, the
// writer's margin, and bought for a future. It waits marginDelay first and
// gives up if another contract was selected meanwhile.
func (ui *instrumentApp) fetchMargin(source marginSource, instrument models.Instrument) {
	ui.marginWanted.set(instrument.ID)
	if ui.marginsInFlight[instrument.ID] || !ui.startOperation() {
		return
	}
	if ui.marginsInFlight == nil {
		ui.marginsInFlight = make(map[models.InstrumentID]bool)
	}
	ui.marginsInFlight[instrument.ID] = true
	side := kite.TransactionTypeBuy
	if instrument.InstrumentType == string(models.OptionTypeCall) || instrument.InstrumentType == string(models.OptionTypePut) {
		side = kite.TransactionTypeSell
	}
	params := kite.GetMarginParams{OrderParams: []kite.OrderMarginParam{{
		Exchange:        instrument.Exchange,
		Tradingsymbol:   instrument.TradingSymbol,
		TransactionType: side,
		Variety:         kite.VarietyRegular,
		Product:         kite.ProductNRML,
		OrderType:       kite.OrderTypeMarket,
		Quantity:        float64(max(instrument.LotSize, 1)),
	}}}
	go func() {
		defer ui.wg.Done()
		select {
		case <-ui.ctx.Done():
			return
		case <-time.After(marginDelay):
		}
		if !ui.marginWanted.is(instrument.ID) {
			ui.application.QueueUpdateDraw(func() { delete(ui.marginsInFlight, instrument.ID) })
			return
		}
		margins, err := source.GetOrderMargins(params)
		if ui.ctx.Err() != nil {
			return
		}
		margin := lotMargin{err: err, fetchedAt: time.Now()}
		if err == nil {
			if len(margins) == 0 {
				margin.err = fmt.Errorf("no margin returned for %s", instrument.ID)
			} else {
				margin.total = margins[0].Total
			}
		}
		ui.application.QueueUpdateDraw(func() {
			delete(ui.marginsInFlight, instrument.ID)
			if ui.margins == nil {
				ui.margins = make(map[models.InstrumentID]lotMargin)
			}
			ui.margins[instrument.ID] = margin
			if selected, ok := ui.selectedInstrument(); ok && selected.ID == instrument.ID {
				ui.updateChainSummary()
			}
		})
	}()
}

// formatAmount groups digits the Indian way: 1234567.5 is 12,34,567.50.
func formatAmount(value float64) string {
	text := fmt.Sprintf("%.2f", math.Abs(value))
	whole, fraction, _ := strings.Cut(text, ".")
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		var groups []string
		for len(head) > 2 {
			groups = append([]string{head[len(head)-2:]}, groups...)
			head = head[:len(head)-2]
		}
		groups = append([]string{head}, groups...)
		whole = strings.Join(groups, ",") + "," + tail
	}
	if value < 0 && text != "0.00" {
		whole = "-" + whole
	}
	return whole + "." + fraction
}

// oiBarWidth is the width of an OI cell, which its bar fills at the chain's
// largest open interest.
const oiBarWidth = 10

// Bar colours behind call and put open interest.
const (
	callOIBarColor = "#1f4d2b"
	putOIBarColor  = "#5c2323"
)

// chainMaxOI is the largest open interest quoted for any contract in the
// chain, which scales every OI bar.
func (ui *instrumentApp) chainMaxOI() float64 {
	var largest float64
	for row := 1; row < ui.chain.GetRowCount(); row++ {
		for _, column := range []int{chainCallLTPColumn, chainPutLTPColumn} {
			if contract, ok := tableInstrument(ui.chain, row, column); ok {
				largest = max(largest, ui.quotes[string(contract.ID)].OI)
			}
		}
	}
	return largest
}

// oiBar draws open interest over a bar whose length is oi relative to
// largest. Both bars grow from the strike column: a call's from the right,
// a put's from the left.
func oiBar(text string, oi, largest float64, call bool) string {
	length := 0
	if largest > 0 && oi > 0 {
		length = min(max(int(math.Round(oi/largest*oiBarWidth)), 1), oiBarWidth)
	}
	format, color := "%-*s", putOIBarColor
	if call {
		format, color = "%*s", callOIBarColor
	}
	cell := []rune(fmt.Sprintf(format, oiBarWidth, text))
	if length == 0 || len(cell) > oiBarWidth {
		return tview.Escape(string(cell))
	}
	if call {
		split := len(cell) - length
		return tview.Escape(string(cell[:split])) + "[:" + color + "]" + tview.Escape(string(cell[split:])) + "[:-]"
	}
	return "[:" + color + "]" + tview.Escape(string(cell[:length])) + "[:-]" + tview.Escape(string(cell[length:]))
}
