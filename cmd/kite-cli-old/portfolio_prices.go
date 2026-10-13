package main

import (
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/rivo/tview"
)

// priceSource fetches last prices with the previous close. The user's
// *kite.Client implements it, batching and pacing large requests.
type priceSource interface {
	GetOHLC(instruments ...string) (kite.QuoteOHLC, error)
}

// priceIDs lists the EXCHANGE:TRADINGSYMBOL of every position and holding.
func (data portfolio) priceIDs() []string {
	ids := make([]string, 0, len(data.positions)+len(data.holdings))
	for _, position := range data.positions {
		ids = append(ids, string(positionID(position)))
	}
	for _, holding := range data.holdings {
		ids = append(ids, holding.Exchange+":"+holding.Tradingsymbol)
	}
	return ids
}

// reprice fetches current prices and applies them. The LTP and P&L in Kite's
// positions and holdings responses lag the market, so the portfolio is
// priced from quotes instead.
func (data portfolio) reprice() portfolio {
	ids := data.priceIDs()
	if data.prices == nil || len(ids) == 0 {
		return data
	}
	quotes, err := data.prices.GetOHLC(ids...)
	data.pricesErr = err
	if err != nil {
		return data
	}
	return data.withPrices(quotes, time.Now())
}

// withPrices recomputes positions and holdings at the quoted prices, using
// Kite's formulas: P&L is (sell value - buy value) + net quantity x LTP x
// multiplier, and M2M the same over the M2M values.
func (data portfolio) withPrices(quotes kite.QuoteOHLC, at time.Time) portfolio {
	positions := make([]kite.Position, len(data.positions))
	for index, position := range data.positions {
		if quote, ok := quotes[string(positionID(position))]; ok && quote.LastPrice > 0 {
			multiplier := position.Multiplier
			if multiplier == 0 {
				multiplier = 1
			}
			open := float64(position.Quantity) * quote.LastPrice * multiplier
			position.LastPrice = quote.LastPrice
			position.PnL = position.SellValue - position.BuyValue + open
			position.M2M = position.SellM2MValue - position.BuyM2MValue + open
			position.Unrealised = position.PnL - position.Realised
		}
		positions[index] = position
	}
	holdings := make(kite.Holdings, len(data.holdings))
	for index, holding := range data.holdings {
		if quote, ok := quotes[holding.Exchange+":"+holding.Tradingsymbol]; ok && quote.LastPrice > 0 {
			holding.LastPrice = quote.LastPrice
			holding.PnL = (quote.LastPrice - holding.AveragePrice) * float64(holding.Quantity+holding.T1Quantity)
			if close := quote.OHLC.Close; close > 0 {
				holding.ClosePrice = close
				holding.DayChange = quote.LastPrice - close
				holding.DayChangePercentage = holding.DayChange / close * 100
			}
		}
		holdings[index] = holding
	}
	data.positions, data.holdings = positions, holdings
	data.pricedAt = at
	return data
}

// startPricing reprices the portfolio every priceEvery while it is shown.
func (view *portfolioView) startPricing() {
	if view.priceEvery <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(view.priceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-view.ctx.Done():
				return
			case <-ticker.C:
				view.application.QueueUpdateDraw(func() {
					if view.visible && !view.loading {
						view.refreshPrices()
					}
				})
			}
		}
	}()
}

// refreshPrices reprices the loaded portfolio in the background, skipping a
// tick while a request is still running.
func (view *portfolioView) refreshPrices() {
	if view.pricing || view.data.prices == nil {
		return
	}
	view.pricing = true
	generation, data := view.generation, view.data
	go func() {
		priced := data.reprice()
		if view.ctx.Err() != nil {
			return
		}
		view.application.QueueUpdateDraw(func() {
			view.pricing = false
			// A reload replaced the portfolio meanwhile.
			if generation != view.generation || view.loading {
				return
			}
			failedBefore := view.data.pricesErr != nil
			view.data = priced
			view.render()
			switch {
			case priced.pricesErr != nil:
				view.status.SetText("  [red::b]Live prices failed:[-] " + tview.Escape(quoteErrorText(priced.pricesErr)))
			case failedBefore:
				view.status.SetText(portfolioHelp)
			}
		})
	}()
}
