package paper

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// lot is an open quantity at its average price: positive long, negative
// short.
type lot struct {
	quantity int
	average  float64
}

// apply adds a fill to the lot and returns the P&L it realises: a fill
// against the lot closes at the lot's average, and one past it opens the
// rest at its own price.
func (l *lot) apply(side string, quantity int, price float64) float64 {
	signed := quantity
	if side == kite.TransactionTypeSell {
		signed = -quantity
	}
	if l.quantity == 0 || (l.quantity > 0) == (signed > 0) {
		total := abs(l.quantity) + quantity
		l.average = (float64(abs(l.quantity))*l.average + float64(quantity)*price) / float64(total)
		l.quantity += signed
		return 0
	}
	closed := min(abs(signed), abs(l.quantity))
	realised := float64(closed) * (price - l.average)
	if l.quantity < 0 {
		realised = -realised
	}
	l.quantity += signed
	switch {
	case l.quantity == 0:
		l.average = 0
	case (l.quantity > 0) == (signed > 0):
		// Crossed through flat: the remainder opened at this price.
		l.average = price
	}
	return realised
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// positionKey is what Kite keeps a position by.
type positionKey struct {
	exchange, symbol, product string
}

func fillKey(f models.PaperFill) positionKey {
	return positionKey{f.Exchange, f.TradingSymbol, f.Product}
}

func (k positionKey) id() models.InstrumentID {
	return models.InstrumentID(k.exchange + ":" + k.symbol)
}

func isOption(instrumentType string) bool {
	return instrumentType == string(models.OptionTypeCall) || instrumentType == string(models.OptionTypePut)
}

// paysCash reports whether a fill moves its whole value in cash, as a
// delivery trade or an option's premium does. Futures and intraday equity
// move only the P&L they realise.
func paysCash(f models.PaperFill) bool {
	return f.Product == kite.ProductCNC || isOption(f.InstrumentType)
}

// cashFlow is what a fill adds to cash, before charges, given the P&L it
// realised.
func cashFlow(f models.PaperFill, realised float64) float64 {
	if !paysCash(f) {
		return realised
	}
	value := float64(f.Quantity) * f.Price
	if f.TransactionType == kite.TransactionTypeBuy {
		return -value
	}
	return value
}

// ledger is a paper account worked out from its fills as of a day.
type ledger struct {
	positions []kite.Position
	holdings  kite.Holdings
	// terms are the contract terms of each position, from its fills.
	terms map[positionKey]models.PaperFill
	// cash is the cash balance now and openingCash at the day's start.
	cash, openingCash float64
	// realisedToday is the P&L realised today, chargesToday the day's
	// charges, premiumToday the option premium paid net of premium
	// received, and deliveryToday the value of delivery bought.
	realisedToday, chargesToday, premiumToday, deliveryToday float64
}

// positionState follows one position key through the fills.
type positionState struct {
	// carried is the position at the day's start (a holding, for CNC)
	// and current the position now.
	carried, current lot
	traded           bool
	buy, sell        lot // today's buys and sells, as quantity and average
	realised         float64
}

// buildLedger works out the account from capital and fills as of today,
// midnight IST. Fills of earlier days carry into the day's positions and,
// for delivery, holdings; closed positions of earlier days drop out.
func buildLedger(capital float64, fills []models.PaperFill, today time.Time) ledger {
	l := ledger{cash: capital, openingCash: capital, terms: map[positionKey]models.PaperFill{}}
	states := map[positionKey]*positionState{}
	var keys []positionKey
	for _, f := range fills {
		key := fillKey(f)
		state, ok := states[key]
		if !ok {
			state = &positionState{}
			states[key] = state
			keys = append(keys, key)
		}
		l.terms[key] = f
		realised := state.current.apply(f.TransactionType, f.Quantity, f.Price)
		flow := cashFlow(f, realised) - f.Charges
		l.cash += flow
		if f.FilledAt.Before(today) {
			state.carried = state.current
			l.openingCash += flow
			continue
		}
		state.traded = true
		state.realised += realised
		l.realisedToday += realised
		l.chargesToday += f.Charges
		side := &state.buy
		if f.TransactionType == kite.TransactionTypeSell {
			side = &state.sell
		}
		side.apply(kite.TransactionTypeBuy, f.Quantity, f.Price)
		value := float64(f.Quantity) * f.Price
		if isOption(f.InstrumentType) {
			if f.TransactionType == kite.TransactionTypeBuy {
				l.premiumToday += value
			} else {
				l.premiumToday -= value
			}
		}
		if f.Product == kite.ProductCNC && f.TransactionType == kite.TransactionTypeBuy {
			l.deliveryToday += value
		}
	}
	for _, key := range keys {
		state := states[key]
		terms := l.terms[key]
		if key.product == kite.ProductCNC {
			if state.carried.quantity > 0 {
				l.holdings = append(l.holdings, holdingOf(key, terms, state))
			}
			if state.traded {
				l.positions = append(l.positions, deliveryPosition(key, terms, state))
			}
			continue
		}
		if state.traded || state.carried.quantity != 0 {
			l.positions = append(l.positions, carriedPosition(key, terms, state))
		}
	}
	return l
}

func holdingOf(key positionKey, terms models.PaperFill, state *positionState) kite.Holding {
	return kite.Holding{
		Tradingsymbol:   key.symbol,
		Exchange:        key.exchange,
		InstrumentToken: terms.InstrumentToken,
		Product:         kite.ProductCNC,
		Quantity:        state.carried.quantity,
		OpeningQuantity: state.carried.quantity,
		UsedQuantity:    min(state.sell.quantity, state.carried.quantity),
		AveragePrice:    state.carried.average,
	}
}

// deliveryPosition is the day's delivery trades: Kite shows them as a
// position until they settle into holdings the next day.
func deliveryPosition(key positionKey, terms models.PaperFill, state *positionState) kite.Position {
	p := basePosition(key, terms)
	p.BuyQuantity, p.BuyPrice = state.buy.quantity, state.buy.average
	p.SellQuantity, p.SellPrice = abs(state.sell.quantity), state.sell.average
	p.Quantity = p.BuyQuantity - p.SellQuantity
	switch {
	case p.Quantity > 0:
		p.AveragePrice = p.BuyPrice
	case p.Quantity < 0:
		p.AveragePrice = p.SellPrice
	}
	// What the day's buys and sells closed against each other.
	matched := min(p.BuyQuantity, p.SellQuantity)
	p.Realised = float64(matched) * (p.SellPrice - p.BuyPrice)
	return withDayValues(p, state)
}

// carriedPosition is a futures, options or intraday position, with any
// quantity carried from earlier days counted as bought or sold at its
// average.
func carriedPosition(key positionKey, terms models.PaperFill, state *positionState) kite.Position {
	p := basePosition(key, terms)
	carried := state.carried
	buyQuantity, buyValue := state.buy.quantity, float64(state.buy.quantity)*state.buy.average
	sellQuantity, sellValue := state.sell.quantity, float64(state.sell.quantity)*state.sell.average
	if carried.quantity > 0 {
		buyQuantity += carried.quantity
		buyValue += float64(carried.quantity) * carried.average
	} else if carried.quantity < 0 {
		sellQuantity -= carried.quantity
		sellValue -= float64(carried.quantity) * carried.average
	}
	p.OvernightQuantity = carried.quantity
	p.Quantity = state.current.quantity
	p.AveragePrice = state.current.average
	p.BuyQuantity, p.BuyValue = buyQuantity, buyValue
	p.SellQuantity, p.SellValue = sellQuantity, sellValue
	if buyQuantity > 0 {
		p.BuyPrice = buyValue / float64(buyQuantity)
	}
	if sellQuantity > 0 {
		p.SellPrice = sellValue / float64(sellQuantity)
	}
	p.Realised = state.realised
	p = withDayValues(p, state)
	p.BuyValue, p.SellValue = buyValue, sellValue
	return p
}

func basePosition(key positionKey, terms models.PaperFill) kite.Position {
	return kite.Position{
		Tradingsymbol:   key.symbol,
		Exchange:        key.exchange,
		InstrumentToken: terms.InstrumentToken,
		Product:         key.product,
		Multiplier:      1,
	}
}

func withDayValues(p kite.Position, state *positionState) kite.Position {
	p.DayBuyQuantity, p.DayBuyPrice = state.buy.quantity, state.buy.average
	p.DayBuyValue = float64(state.buy.quantity) * state.buy.average
	p.DaySellQuantity, p.DaySellPrice = state.sell.quantity, state.sell.average
	p.DaySellValue = float64(state.sell.quantity) * state.sell.average
	p.BuyValue = float64(p.BuyQuantity) * p.BuyPrice
	p.SellValue = float64(p.SellQuantity) * p.SellPrice
	p.BuyM2MValue, p.SellM2MValue = p.BuyValue, p.SellValue
	return p
}

// priced fills in a position's last price and P&L the way Kite does: P&L
// is the sell value less the buy value plus the open quantity at the last
// price, and M2M the same from the previous close for what was carried.
func pricedPosition(p kite.Position, ltp, close float64) kite.Position {
	if close > 0 {
		p.ClosePrice = close
		p.BuyM2MValue, p.SellM2MValue = p.DayBuyValue, p.DaySellValue
		if p.OvernightQuantity > 0 {
			p.BuyM2MValue += float64(p.OvernightQuantity) * close
		} else if p.OvernightQuantity < 0 {
			p.SellM2MValue -= float64(p.OvernightQuantity) * close
		}
	}
	if ltp <= 0 {
		return p
	}
	open := float64(p.Quantity) * ltp * p.Multiplier
	p.LastPrice = ltp
	p.Value = p.SellValue - p.BuyValue
	p.PnL = p.SellValue - p.BuyValue + open
	p.M2M = p.SellM2MValue - p.BuyM2MValue + open
	p.Unrealised = p.PnL - p.Realised
	return p
}

func pricedHolding(h kite.Holding, ltp, close float64) kite.Holding {
	if ltp <= 0 {
		return h
	}
	h.LastPrice = ltp
	h.PnL = (ltp - h.AveragePrice) * float64(h.Quantity)
	if close > 0 {
		h.ClosePrice = close
		h.DayChange = ltp - close
		h.DayChangePercentage = h.DayChange / close * 100
	}
	return h
}

// openAt is each position open at the instant at, by key, with its contract
// terms: what day-end work squares off or settles.
func openAt(fills []models.PaperFill, at time.Time) map[positionKey]lot {
	lots := map[positionKey]lot{}
	for _, f := range fills {
		if f.FilledAt.After(at) {
			continue
		}
		key := fillKey(f)
		current := lots[key]
		current.apply(f.TransactionType, f.Quantity, f.Price)
		lots[key] = current
	}
	for key, current := range lots {
		if current.quantity == 0 {
			delete(lots, key)
		}
	}
	return lots
}

// sortedKeys orders keys for stable work.
func sortedKeys(lots map[positionKey]lot) []positionKey {
	keys := make([]positionKey, 0, len(lots))
	for key := range lots {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b positionKey) int {
		return cmp.Or(cmp.Compare(a.exchange, b.exchange), cmp.Compare(a.symbol, b.symbol), cmp.Compare(a.product, b.product))
	})
	return keys
}

func round2(value float64) float64 { return math.Round(value*100) / 100 }
