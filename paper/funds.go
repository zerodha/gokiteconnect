package paper

import (
	"cmp"
	"slices"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// Margin rates for when the data user's margin calculator cannot be
// reached: of a future's value, of a short option's strike value, and of
// an intraday equity position's value.
const (
	futuresMarginRate     = 0.12
	shortOptionMarginRate = 0.12
	intradayMarginRate    = 0.20
)

// leg is a position or a pending order that margin is blocked for.
type leg struct {
	exchange, symbol, product, side, orderType string
	quantity                                   int
	// price is the leg's price for valuing it: the order's price, else the
	// last price, else the average.
	price, limit, trigger float64
	instrumentType        string
	strike                float64
	pending               bool
}

// positionLegs are the account's open positions, delivery aside: delivery
// is paid for in full.
func positionLegs(l ledger) []leg {
	var legs []leg
	for _, p := range l.positions {
		if p.Quantity == 0 || p.Product == kite.ProductCNC {
			continue
		}
		side := kite.TransactionTypeBuy
		if p.Quantity < 0 {
			side = kite.TransactionTypeSell
		}
		t := l.terms[positionKey{p.Exchange, p.Tradingsymbol, p.Product}]
		legs = append(legs, leg{exchange: p.Exchange, symbol: p.Tradingsymbol, product: p.Product, side: side,
			orderType: kite.OrderTypeMarket, quantity: abs(p.Quantity), price: cmp.Or(p.LastPrice, p.AveragePrice),
			instrumentType: t.InstrumentType, strike: t.Strike})
	}
	return legs
}

// orderLegs are what pending orders have left.
func (s *session) orderLegs(orders []kite.Order) []leg {
	var legs []leg
	for _, o := range orders {
		t := termsOf(o)
		price := cmp.Or(o.Price, o.TriggerPrice, s.quotes[orderID(o)].LastPrice)
		legs = append(legs, leg{exchange: o.Exchange, symbol: o.TradingSymbol, product: o.Product, side: o.TransactionType,
			orderType: o.OrderType, quantity: int(o.PendingQuantity), price: price, limit: o.Price, trigger: o.TriggerPrice,
			instrumentType: t.InstrumentType, strike: t.Strike, pending: true})
	}
	return legs
}

// blocked is what a set of legs takes from the account's funds.
type blocked struct {
	// margin is the span and exposure blocked, apart from option premium.
	margin, span, exposure float64
	// cash is what pending delivery and option buys will pay.
	cash float64
}

func (b blocked) used() float64 { return b.margin + b.cash }

// block works out what legs block: margin from the data user's basket
// calculator, which nets hedges, or by flat rates without it, and cash
// for pending buys that pay in full.
func (s *session) block(legs []leg) blocked {
	var b blocked
	var basket []kite.OrderMarginParam
	for _, g := range legs {
		if g.pending && g.side == kite.TransactionTypeBuy && (g.product == kite.ProductCNC || isOption(g.instrumentType)) {
			b.cash += float64(g.quantity) * g.price
		}
		if g.product == kite.ProductCNC || g.quantity == 0 {
			continue
		}
		basket = append(basket, kite.OrderMarginParam{Exchange: g.exchange, Tradingsymbol: g.symbol,
			TransactionType: g.side, Variety: kite.VarietyRegular, Product: g.product, OrderType: g.orderType,
			Quantity: float64(g.quantity), Price: g.limit, TriggerPrice: g.trigger})
	}
	if len(basket) == 0 {
		return b
	}
	if data, err := s.market(); err == nil {
		// The data user's own positions must not net against these.
		margins, err := data.GetBasketMargins(kite.GetBasketParams{OrderParams: basket})
		if err == nil {
			b.span, b.exposure = margins.Final.SPAN, margins.Final.Exposure
			b.margin = max(margins.Final.Total-margins.Final.OptionPremium, 0)
			return b
		}
	}
	for _, g := range legs {
		if g.product != kite.ProductCNC {
			b.margin += fallbackMargin(g)
		}
	}
	b.span = b.margin
	return b
}

func fallbackMargin(g leg) float64 {
	value := float64(g.quantity) * g.price
	switch {
	case isOption(g.instrumentType):
		if g.side == kite.TransactionTypeBuy {
			return 0
		}
		return shortOptionMarginRate * float64(g.quantity) * cmp.Or(g.strike, g.price)
	case g.instrumentType == "FUT":
		return futuresMarginRate * value
	}
	return intradayMarginRate * value
}

// unrealisedLoss is the open loss on positions not paid for in full:
// futures, short options and intraday equity. Open profit is not lent
// against.
func unrealisedLoss(l ledger) float64 {
	total := 0.0
	for _, p := range l.positions {
		t := l.terms[positionKey{p.Exchange, p.Tradingsymbol, p.Product}]
		if p.Product == kite.ProductCNC || (isOption(t.InstrumentType) && p.Quantity > 0) {
			continue
		}
		total += p.Unrealised
	}
	return min(total, 0)
}

// availableMargin is what is left to trade with once b is blocked.
func availableMargin(l ledger, b blocked) float64 {
	return l.cash - b.used() + unrealisedLoss(l)
}

// ledger is the account as of now, priced at whatever quotes this step
// has.
func (s *session) ledger() ledger {
	slices.SortStableFunc(s.fills, func(a, b models.PaperFill) int { return a.FilledAt.Compare(b.FilledAt) })
	l := buildLedger(s.user.account.Capital, s.fills, dayOf(s.now))
	for i, p := range l.positions {
		if q, ok := s.quotes[string(positionKey{p.Exchange, p.Tradingsymbol, p.Product}.id())]; ok {
			l.positions[i] = pricedPosition(p, q.LastPrice, q.OHLC.Close)
		}
	}
	for i, h := range l.holdings {
		if q, ok := s.quotes[h.Exchange+":"+h.Tradingsymbol]; ok {
			l.holdings[i] = pricedHolding(h, q.LastPrice, q.OHLC.Close)
		}
	}
	return l
}

// margins are the account's funds the way Kite reports them: cash at the
// day's open, what is blocked and the balance left.
func (s *session) margins(l ledger) kite.AllMargins {
	var pending []kite.Order
	for _, o := range s.orders {
		if isOpen(o) {
			pending = append(pending, o)
		}
	}
	b := s.block(append(positionLegs(l), s.orderLegs(pending)...))
	net := availableMargin(l, b)
	return kite.AllMargins{
		Equity: kite.Margins{
			Category: kite.MarginsEquity, Enabled: true, Net: round2(net),
			Available: kite.AvailableMargins{Cash: round2(l.openingCash), OpeningBalance: round2(l.openingCash), LiveBalance: round2(net)},
			Used: kite.UsedMargins{
				Debits: round2(l.openingCash - net), Span: round2(b.span), Exposure: round2(b.exposure),
				OptionPremium: round2(l.premiumToday), M2MRealised: round2(l.realisedToday),
				M2MUnrealised: round2(unrealisedLoss(l)), Delivery: round2(l.deliveryToday),
			},
		},
		Commodity: kite.Margins{Category: kite.MarginsCommodity},
	}
}
