package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rivo/tview"
)

// quoteSource fetches full market quotes. *kite.Client implements it,
// batching large requests and pacing them to Kite's 1 request per second.
type quoteSource interface {
	GetQuote(instruments ...string) (kite.Quote, error)
}

// quoteLoader opens the data user's quote source and names the user. It runs
// at startup and again on every manual price refresh, so a session refreshed
// on the users screen is picked up without restarting.
type quoteLoader func(context.Context) (quoteSource, string, error)

const quoteRequestTimeout = 10 * time.Second

// credentialsQuoteLoader reads the data user's stored access token from the
// credentials database. It never logs in: a fresh login could invalidate a
// session another tool is using, so an expired session is reported instead.
// An empty userID picks the first API user with a data subscription.
func credentialsQuoteLoader(path, userID string) quoteLoader {
	return func(ctx context.Context) (quoteSource, string, error) {
		if _, err := os.Stat(path); err != nil {
			return nil, "", fmt.Errorf("credentials database %s: %w", path, err)
		}
		database, err := sql.Open("sqlite3", path)
		if err != nil {
			return nil, "", err
		}
		defer database.Close()
		store, err := credentialstore.NewUserSQLiteRepository(database)
		if err != nil {
			return nil, "", err
		}
		user, err := dataUser(ctx, store, userID)
		if err != nil {
			return nil, "", err
		}
		client := kite.New(user.APIKey)
		client.SetAppName("kite-cli")
		client.SetTimeout(quoteRequestTimeout)
		client.SetAccessToken(user.AccessToken)
		return client, user.UserID, nil
	}
}

func dataUser(ctx context.Context, store credentials.CredentialsRepository, userID string) (*credentials.Credentials, error) {
	var user *credentials.Credentials
	if userID != "" {
		found, err := store.GetUserByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("data user %s: %w", userID, err)
		}
		user = found
	} else {
		users, err := store.ListAvailableUsers(ctx)
		if err != nil {
			return nil, err
		}
		for i := range users {
			if users[i].IsAPIUser && users[i].DataSubscription {
				user = &users[i]
				break
			}
		}
		if user == nil {
			return nil, errors.New("no API user with a data subscription; set TRADEBOT_DATA_USER_ID")
		}
	}
	if !user.IsAPIUser {
		return nil, fmt.Errorf("data user %s is a web user; full quotes need an API user", user.UserID)
	}
	if user.AccessToken == "" {
		return nil, fmt.Errorf("data user %s has no session; refresh it on the users screen", user.UserID)
	}
	return user, nil
}

// startQuotes connects the data user and, when quoteEvery is positive,
// refreshes the visible prices on that interval.
func (ui *instrumentApp) startQuotes() {
	if ui.loadQuotes == nil {
		return
	}
	ui.connectQuotes(false)
	if ui.quoteEvery <= 0 || !ui.startOperation() {
		return
	}
	go func() {
		defer ui.wg.Done()
		ticker := time.NewTicker(ui.quoteEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ui.ctx.Done():
				return
			case <-ticker.C:
				ui.application.QueueUpdateDraw(func() {
					if !ui.busy && !ui.hidden && ui.overlay == "" {
						ui.refreshQuotes()
					}
				})
			}
		}
	}()
}

// reloadQuotes reconnects the data user and refreshes the visible prices.
func (ui *instrumentApp) reloadQuotes() {
	if ui.loadQuotes == nil {
		ui.setStatus("  Prices are off (started with [yellow]-no-quotes[-]).")
		return
	}
	ui.margins = nil
	ui.connectQuotes(true)
}

func (ui *instrumentApp) connectQuotes(fetch bool) {
	if !ui.startOperation() {
		return
	}
	load := ui.loadQuotes
	go func() {
		defer ui.wg.Done()
		source, label, err := load(ui.ctx)
		if ui.ctx.Err() != nil {
			return
		}
		ui.application.QueueUpdateDraw(func() {
			ui.quoteSource, ui.quoteLabel, ui.quoteErr = source, label, err
			ui.updateHeader()
			if err != nil {
				ui.setStatus("  [red::b]Prices off:[-] " + tview.Escape(err.Error()))
				return
			}
			if fetch || ui.catalog != nil {
				ui.refreshQuotes()
			}
		})
	}()
}

// refreshQuotes fetches quotes for the instruments on screen. Calls made while
// a fetch is running are folded into one more fetch when it finishes.
func (ui *instrumentApp) refreshQuotes() {
	if ui.quoteSource == nil {
		return
	}
	ids := ui.visibleQuoteIDs()
	if len(ids) == 0 {
		return
	}
	if ui.quoteInFlight {
		ui.quoteAgain = true
		return
	}
	if !ui.startOperation() {
		return
	}
	ui.quoteInFlight = true
	source := ui.quoteSource
	go func() {
		defer ui.wg.Done()
		quotes, err := source.GetQuote(ids...)
		if ui.ctx.Err() != nil {
			return
		}
		ui.application.QueueUpdateDraw(func() {
			ui.quoteInFlight = false
			ui.quoteErr = err
			if err != nil {
				ui.setStatus("  [red::b]Quote request failed:[-] " + tview.Escape(quoteErrorText(err)))
			} else {
				if ui.quotes == nil {
					ui.quotes = make(map[string]kite.QuoteData, len(quotes))
				}
				for id, quote := range quotes {
					ui.quotes[id] = quote
				}
				ui.quotesAt = time.Now()
				ui.applyQuotes()
			}
			ui.updateHeader()
			if ui.quoteAgain {
				ui.quoteAgain = false
				ui.refreshQuotes()
			}
		})
	}()
}

func quoteErrorText(err error) string {
	var kiteErr kite.Error
	if errors.As(err, &kiteErr) && kiteErr.ErrorType == kite.TokenError {
		return err.Error() + " · refresh the session on the users screen (F), then press R"
	}
	return err.Error()
}

// visibleQuoteIDs lists the instruments the current view shows prices for.
func (ui *instrumentApp) visibleQuoteIDs() []string {
	var ids []string
	switch ui.mode {
	case viewDashboard:
		for _, instrument := range ui.resultItems {
			ids = append(ids, string(instrument.ID))
		}
	case viewChain:
		if ui.chainData == nil {
			return nil
		}
		ids = append(ids, string(ui.chainData.underlying))
		for _, future := range filterFutures(ui.chainData.futures, ui.chainData.filter) {
			ids = append(ids, string(future.ID))
		}
		for _, row := range buildChainRows(ui.chainData.options, ui.chainData.filter) {
			for _, leg := range []*models.Instrument{row.call, row.put} {
				if leg != nil {
					ids = append(ids, string(leg.ID))
				}
			}
		}
	case viewLists:
		if list, ok := ui.selectedList(); ok {
			ids = make([]string, 0, len(list.Items))
			for _, id := range list.InstrumentIDs() {
				ids = append(ids, string(id))
			}
		}
	}
	return ids
}

// applyQuotes redraws the price cells of the current view from ui.quotes.
func (ui *instrumentApp) applyQuotes() {
	switch ui.mode {
	case viewDashboard:
		for row := 1; row < ui.results.GetRowCount(); row++ {
			if instrument, ok := tableInstrument(ui.results, row, 0); ok {
				ui.setPriceCells(ui.results, row, resultsLTPColumn, instrument)
			}
		}
	case viewChain:
		ui.applyChainQuotes()
	case viewLists:
		for row := 1; row < ui.itemsTable.GetRowCount(); row++ {
			if id, ok := ui.itemsTable.GetCell(row, 0).GetReference().(models.InstrumentID); ok {
				ui.setPriceCells(ui.itemsTable, row, itemsLTPColumn, models.Instrument{ID: id})
			}
		}
	}
	if instrument, ok := ui.selectedInstrument(); ok {
		ui.updateDetail(instrument)
	}
}

// setPriceCells writes the LTP and change cells at column and column+1.
func (ui *instrumentApp) setPriceCells(table *tview.Table, row, column int, instrument models.Instrument) {
	ltp, change, color := "—", "", tcell.ColorDarkGray
	if quote, ok := ui.quotes[string(instrument.ID)]; ok {
		ltp = formatPrice(quote.LastPrice)
		change, color = formatChange(quote)
	}
	table.SetCell(row, column, tview.NewTableCell(ltp).SetTextColor(tcell.ColorWhite).SetAlign(tview.AlignRight).SetReference(instrumentReference(table, row, instrument)))
	table.SetCell(row, column+1, tview.NewTableCell(change).SetTextColor(color).SetAlign(tview.AlignRight).SetReference(instrumentReference(table, row, instrument)))
}

// instrumentReference keeps a row's existing reference so selection still
// resolves to the full instrument or list item.
func instrumentReference(table *tview.Table, row int, instrument models.Instrument) any {
	if reference := table.GetCell(row, 0).GetReference(); reference != nil {
		return reference
	}
	return instrument
}

// applyChainQuotes fills the chain's OI, breakeven and LTP columns, shows the
// underlying price in the title, marks the at-the-money strike and refreshes
// the summary bar. The first priced render of a chain selects the ATM row.
func (ui *instrumentApp) applyChainQuotes() {
	data := ui.chainData
	if data == nil {
		return
	}
	underlying, priced := ui.quotes[string(data.underlying)]
	atm := math.NaN()
	if priced && underlying.LastPrice > 0 {
		atm = nearestStrike(ui.chain, underlying.LastPrice)
	}
	maxOI := ui.chainMaxOI()
	atmRow := 0
	for row := 1; row < ui.chain.GetRowCount(); row++ {
		strikeCell := ui.chain.GetCell(row, chainStrikeColumn)
		strike, _ := strikeCell.GetReference().(models.Instrument)
		for _, side := range []struct {
			ltp, oi, be int
			call        bool
		}{{chainCallLTPColumn, chainCallOIColumn, chainCallBEColumn, true}, {chainPutLTPColumn, chainPutOIColumn, chainPutBEColumn, false}} {
			contract, ok := tableInstrument(ui.chain, row, side.ltp)
			if !ok {
				continue
			}
			ltp, oi, be := "—", oiBar("—", 0, maxOI, side.call), "—"
			if quote, ok := ui.quotes[string(contract.ID)]; ok {
				ltp, oi = formatPrice(quote.LastPrice), oiBar(formatCount(quote.OI), quote.OI, maxOI, side.call)
				if quote.LastPrice > 0 {
					be = formatBreakeven(breakeven(contract, quote.LastPrice), underlying.LastPrice, ui.breakevenPercent)
				}
			}
			ui.chain.GetCell(row, side.ltp).SetText(ltp)
			ui.chain.GetCell(row, side.oi).SetText(oi)
			ui.chain.GetCell(row, side.be).SetText(be)
		}
		isATM := strike.Strike == atm
		style := tcell.StyleDefault.Foreground(tcell.ColorYellow)
		if isATM {
			atmRow = row
			style = style.Background(tcell.ColorDarkGoldenrod).Foreground(tcell.ColorBlack).Bold(true)
		}
		strikeCell.SetStyle(style)
	}
	if priced {
		change, _ := formatChange(underlying)
		ui.chain.SetTitle(fmt.Sprintf(" %s %s %s · %s ", data.underlying, formatPrice(underlying.LastPrice), change, ui.chainTitleSuffix()))
	}
	if ui.chainNeedsATM && atmRow > 0 {
		ui.chainNeedsATM = false
		_, column := ui.chain.GetSelection()
		ui.chain.Select(atmRow, column)
		_, _, _, height := ui.chain.GetInnerRect()
		ui.chain.SetOffset(max(0, atmRow-height/2), 0)
	}
	ui.updateChainSummary()
}

// nearestStrike returns the chain strike nearest price.
func nearestStrike(table *tview.Table, price float64) float64 {
	best := math.NaN()
	for row := 1; row < table.GetRowCount(); row++ {
		strike, ok := table.GetCell(row, chainStrikeColumn).GetReference().(models.Instrument)
		if !ok {
			continue
		}
		if math.IsNaN(best) || math.Abs(strike.Strike-price) < math.Abs(best-price) {
			best = strike.Strike
		}
	}
	return best
}

// quoteSection is the detail pane's market section for instrument.
func (ui *instrumentApp) quoteSection(instrument models.Instrument) string {
	if ui.quoteSource == nil {
		return ""
	}
	quote, ok := ui.quotes[string(instrument.ID)]
	if !ok {
		return "\n\n[lightskyblue]Market[-]\n[darkgray]No quote yet · R to refresh[-]"
	}
	change, color := formatChange(quote)
	bid, ask := "—", "—"
	if level := quote.Depth.Buy[0]; level.Price > 0 {
		bid = fmt.Sprintf("%s × %d", formatPrice(level.Price), level.Quantity)
	}
	if level := quote.Depth.Sell[0]; level.Price > 0 {
		ask = fmt.Sprintf("%s × %d", formatPrice(level.Price), level.Quantity)
	}
	section := fmt.Sprintf("\n\n[lightskyblue]Market[-] [darkgray]%s[-]\n"+
		"[white]LTP[-]            [white::b]%s[-:-:-] [%s]%s[-]\n"+
		"[white]Open / High[-]    %s / %s\n"+
		"[white]Low / Prev[-]     %s / %s\n"+
		"[white]Volume[-]         %s\n"+
		"[white]Bid[-]            %s\n"+
		"[white]Ask[-]            %s",
		ui.quotesAt.Format("15:04:05"),
		formatPrice(quote.LastPrice), colorName(color), change,
		formatPrice(quote.OHLC.Open), formatPrice(quote.OHLC.High),
		formatPrice(quote.OHLC.Low), formatPrice(quote.OHLC.Close),
		formatCount(float64(quote.Volume)), bid, ask)
	if quote.OI > 0 {
		section += fmt.Sprintf("\n[white]OI[-]             %s", formatCount(quote.OI))
	}
	return section
}

// quoteHeader summarises the price feed for the header.
func (ui *instrumentApp) quoteHeader() string {
	switch {
	case ui.loadQuotes == nil:
		return "[darkgray]prices off[-]"
	case ui.quoteSource == nil && ui.quoteErr != nil:
		return "[red]prices off[-]"
	case ui.quoteSource == nil:
		return "[darkgray]prices connecting…[-]"
	case ui.quoteErr != nil:
		return fmt.Sprintf("[red]prices %s · failed[-]", tview.Escape(ui.quoteLabel))
	case ui.quotesAt.IsZero():
		return fmt.Sprintf("[lightgreen]prices %s[-]", tview.Escape(ui.quoteLabel))
	}
	return fmt.Sprintf("[lightgreen]prices %s · %s[-]", tview.Escape(ui.quoteLabel), ui.quotesAt.Format("15:04:05"))
}

// formatChange returns the change from the previous close as a percentage.
func formatChange(quote kite.QuoteData) (string, tcell.Color) {
	previous := quote.OHLC.Close
	if previous <= 0 || quote.LastPrice <= 0 {
		return "", tcell.ColorDarkGray
	}
	change := (quote.LastPrice - previous) / previous * 100
	switch {
	case change > 0:
		return fmt.Sprintf("+%.2f%%", change), tcell.ColorLightGreen
	case change < 0:
		return fmt.Sprintf("%.2f%%", change), tcell.ColorLightCoral
	}
	return "0.00%", tcell.ColorLightGray
}

func formatPrice(price float64) string {
	return fmt.Sprintf("%.2f", price)
}

// formatCount shortens large counts: 1234567 becomes 12.35L, Indian style.
func formatCount(value float64) string {
	switch {
	case value >= 1e7:
		return fmt.Sprintf("%.2fCr", value/1e7)
	case value >= 1e5:
		return fmt.Sprintf("%.2fL", value/1e5)
	}
	return fmt.Sprintf("%.0f", value)
}

func colorName(color tcell.Color) string {
	switch color {
	case tcell.ColorLightGreen:
		return "lightgreen"
	case tcell.ColorLightCoral:
		return "lightcoral"
	}
	return "lightgray"
}
