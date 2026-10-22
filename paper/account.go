package paper

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// Account is one user's paper account, as a broker.Account.
type Account struct {
	book *Book
	user *userBook
	ctx  context.Context
}

var (
	_ broker.Account = (*Account)(nil)
	_ broker.Matcher = (*Account)(nil)
)

// view is the account worked out at one instant.
type view struct {
	at        time.Time
	orders    kite.Orders
	trades    kite.Trades
	positions kite.Positions
	holdings  kite.Holdings
	margins   kite.AllMargins
}

// step runs fn as one locked step, after bringing the account up to the
// clock, and records what changed. prefetch names instruments fn quotes,
// so they come in the same call as the account's own.
func (a *Account) step(prefetch []string, fn func(*session) error) error {
	a.user.mu.Lock()
	defer a.user.mu.Unlock()
	s, err := a.book.begin(a.ctx, a.user)
	if err != nil {
		return err
	}
	if len(prefetch) > 0 {
		s.prefetch(prefetch...)
	}
	if err := s.advance(); err != nil {
		return err
	}
	fnErr := fn(s)
	if err := s.commit(); err != nil {
		return errors.Join(fnErr, err)
	}
	return fnErr
}

// read returns the account as of now, reusing one worked out within
// viewTTL unless fresh is set, and the orders that changed bringing it up
// to the clock.
func (a *Account) read(fresh bool) (*view, []kite.Order, error) {
	a.user.mu.Lock()
	defer a.user.mu.Unlock()
	if v := a.user.view; !fresh && v != nil && a.book.config.Now().Sub(v.at) < viewTTL {
		return v, nil, nil
	}
	s, err := a.book.begin(a.ctx, a.user)
	if err != nil {
		return nil, nil, err
	}
	if err := s.advance(); err != nil {
		return nil, nil, err
	}
	if err := s.commit(); err != nil {
		return nil, nil, err
	}
	s.prefetch()
	v := s.view()
	a.user.view = v
	return v, s.changedOrders(), nil
}

// view works out the account from the step's orders, fills and quotes.
func (s *session) view() *view {
	l := s.ledger()
	today := dayOf(s.now)
	v := &view{at: s.now, holdings: l.holdings, margins: s.margins(l),
		positions: kite.Positions{Net: l.positions, Day: dayPositions(l.positions)}}
	for _, o := range s.orders {
		if !o.OrderTimestamp.Before(today) {
			v.orders = append(v.orders, o)
		}
	}
	for _, f := range s.fills {
		if f.FilledAt.Before(today) {
			continue
		}
		at := models.Time{Time: f.FilledAt.In(ist)}
		v.trades = append(v.trades, kite.Trade{AveragePrice: f.Price, Quantity: float64(f.Quantity), TradeID: f.TradeID,
			Product: f.Product, FillTimestamp: at, ExchangeTimestamp: at, OrderID: f.OrderID, TransactionType: f.TransactionType,
			TradingSymbol: f.TradingSymbol, Exchange: f.Exchange, InstrumentToken: f.InstrumentToken})
	}
	return v
}

// dayPositions are the day's trades alone, as Kite's day positions show
// them.
func dayPositions(net []kite.Position) []kite.Position {
	var day []kite.Position
	for _, p := range net {
		if p.DayBuyQuantity == 0 && p.DaySellQuantity == 0 {
			continue
		}
		d := p
		d.OvernightQuantity = 0
		d.Quantity = p.DayBuyQuantity - p.DaySellQuantity
		d.BuyQuantity, d.BuyPrice, d.BuyValue, d.BuyM2MValue = p.DayBuyQuantity, p.DayBuyPrice, p.DayBuyValue, p.DayBuyValue
		d.SellQuantity, d.SellPrice, d.SellValue, d.SellM2MValue = p.DaySellQuantity, p.DaySellPrice, p.DaySellValue, p.DaySellValue
		d.AveragePrice = 0
		switch {
		case d.Quantity > 0:
			d.AveragePrice = d.BuyPrice
		case d.Quantity < 0:
			d.AveragePrice = d.SellPrice
		}
		d.Realised = float64(min(d.BuyQuantity, d.SellQuantity)) * (d.SellPrice - d.BuyPrice)
		d.Value, d.PnL, d.M2M, d.Unrealised = 0, 0, 0, 0
		day = append(day, pricedPosition(d, p.LastPrice, 0))
	}
	return day
}

// PlaceOrder places a regular order. One the risk checks reject is in the
// order book as REJECTED, and its ID comes back with the reason.
func (a *Account) PlaceOrder(variety string, params kite.OrderParams) (kite.OrderResponse, error) {
	var id string
	err := a.step([]string{params.Exchange + ":" + params.Tradingsymbol}, func(s *session) error {
		var err error
		id, err = s.place(variety, params)
		return err
	})
	return kite.OrderResponse{OrderID: id}, err
}

// ModifyOrder changes an open order's type, quantity, prices and validity.
func (a *Account) ModifyOrder(variety, orderID string, params kite.OrderParams) (kite.OrderResponse, error) {
	err := a.step(nil, func(s *session) error { return s.modify(orderID, params) })
	return kite.OrderResponse{OrderID: orderID}, err
}

// CancelOrder cancels an open order.
func (a *Account) CancelOrder(variety, orderID string, parentOrderID *string) (kite.OrderResponse, error) {
	err := a.step(nil, func(s *session) error { return s.cancelOrder(orderID) })
	return kite.OrderResponse{OrderID: orderID}, err
}

// GetOrders returns the day's orders.
func (a *Account) GetOrders() (kite.Orders, error) {
	v, _, err := a.read(false)
	if err != nil {
		return nil, err
	}
	return slices.Clone(v.orders), nil
}

// GetOrderHistory returns every state of an order.
func (a *Account) GetOrderHistory(orderID string) ([]kite.Order, error) {
	if _, _, err := a.read(false); err != nil {
		return nil, err
	}
	a.user.mu.Lock()
	defer a.user.mu.Unlock()
	return a.user.store.OrderHistory(a.ctx, orderID)
}

// GetTrades returns the day's fills.
func (a *Account) GetTrades() (kite.Trades, error) {
	v, _, err := a.read(false)
	if err != nil {
		return nil, err
	}
	return slices.Clone(v.trades), nil
}

// GetPositions returns the account's positions, priced.
func (a *Account) GetPositions() (kite.Positions, error) {
	v, _, err := a.read(false)
	if err != nil {
		return kite.Positions{}, err
	}
	return kite.Positions{Net: slices.Clone(v.positions.Net), Day: slices.Clone(v.positions.Day)}, nil
}

// GetHoldings returns the delivery bought on earlier days, priced.
func (a *Account) GetHoldings() (kite.Holdings, error) {
	v, _, err := a.read(false)
	if err != nil {
		return nil, err
	}
	return slices.Clone(v.holdings), nil
}

// GetMFHoldings returns none: paper accounts trade the exchanges only.
func (a *Account) GetMFHoldings() (kite.MFHoldings, error) { return kite.MFHoldings{}, nil }

// GetUserMargins returns the account's funds.
func (a *Account) GetUserMargins() (kite.AllMargins, error) {
	v, _, err := a.read(false)
	if err != nil {
		return kite.AllMargins{}, err
	}
	return v.margins, nil
}

// GetUserProfile names the account as the user's paper account.
func (a *Account) GetUserProfile() (kite.UserProfile, error) {
	id := a.user.userID
	return kite.UserProfile{
		UserID: id, UserName: id + " (paper)", UserShortName: id, UserType: "individual", Broker: "PAPER",
		Products:   []string{kite.ProductCNC, kite.ProductMIS, kite.ProductNRML},
		OrderTypes: []string{kite.OrderTypeMarket, kite.OrderTypeLimit, kite.OrderTypeSL, kite.OrderTypeSLM},
		Exchanges:  []string{kite.ExchangeNSE, kite.ExchangeBSE, kite.ExchangeNFO, kite.ExchangeBFO},
	}, nil
}

// Match matches the open orders and runs any day-end work due now, and
// returns the orders that changed.
func (a *Account) Match() ([]kite.Order, error) {
	_, changed, err := a.read(true)
	return changed, err
}

// GetQuote quotes from the market data.
func (a *Account) GetQuote(instruments ...string) (kite.Quote, error) {
	data, err := a.book.market(a.ctx)
	if err != nil {
		return nil, err
	}
	return data.GetQuote(instruments...)
}

// GetLTP gets last prices from the market data.
func (a *Account) GetLTP(instruments ...string) (kite.QuoteLTP, error) {
	data, err := a.book.market(a.ctx)
	if err != nil {
		return nil, err
	}
	return data.GetLTP(instruments...)
}

// GetOHLC gets the day's OHLC from the market data.
func (a *Account) GetOHLC(instruments ...string) (kite.QuoteOHLC, error) {
	data, err := a.book.market(a.ctx)
	if err != nil {
		return nil, err
	}
	return data.GetOHLC(instruments...)
}
