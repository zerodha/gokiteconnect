package paper

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
	"github.com/devshoe/gokiteconnect/repository"
)

// Thursday 22 October 2026, IST.
func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, ist)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// fakeData is the market: quotes by instrument, candles by token and
// interval, and a margin calculator charging 15% of value, plus premium
// for option buys.
type fakeData struct {
	mu        sync.Mutex
	clock     *clock
	quotes    kite.Quote
	candles   map[int]map[string][]kite.HistoricalData
	shut      bool
	basketErr error
	candleErr error
	baskets   int
}

func newFakeData(c *clock) *fakeData {
	return &fakeData{clock: c, quotes: kite.Quote{}, candles: map[int]map[string][]kite.HistoricalData{}}
}

func (d *fakeData) setQuote(id string, q kite.QuoteData) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.quotes[id] = q
}

func (d *fakeData) addCandles(token int, interval string, candles ...kite.HistoricalData) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.candles[token] == nil {
		d.candles[token] = map[string][]kite.HistoricalData{}
	}
	d.candles[token][interval] = append(d.candles[token][interval], candles...)
}

func candle(t time.Time, open, high, low, close float64) kite.HistoricalData {
	return kite.HistoricalData{Date: models.Time{Time: t}, Open: open, High: high, Low: low, Close: close}
}

func (d *fakeData) GetQuote(ids ...string) (kite.Quote, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := kite.Quote{}
	for _, id := range ids {
		if id == marketIndex {
			ticked := d.clock.Now()
			if d.shut {
				ticked = ticked.Add(-time.Hour)
			}
			out[id] = kite.QuoteData{LastPrice: 25000, Timestamp: models.Time{Time: ticked}}
			continue
		}
		if q, ok := d.quotes[id]; ok {
			out[id] = q
		}
	}
	return out, nil
}

func (d *fakeData) GetLTP(...string) (kite.QuoteLTP, error)   { return kite.QuoteLTP{}, nil }
func (d *fakeData) GetOHLC(...string) (kite.QuoteOHLC, error) { return kite.QuoteOHLC{}, nil }

func (d *fakeData) GetHistoricalData(token int, interval string, from, to time.Time, _, _ bool) ([]kite.HistoricalData, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.candleErr != nil {
		return nil, d.candleErr
	}
	var out []kite.HistoricalData
	for _, c := range d.candles[token][interval] {
		if !c.Date.Before(from) && !c.Date.After(to) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (d *fakeData) GetBasketMargins(params kite.GetBasketParams) (kite.BasketMargins, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baskets++
	if d.basketErr != nil {
		return kite.BasketMargins{}, d.basketErr
	}
	if params.ConsiderPositions {
		return kite.BasketMargins{}, errors.New("paper must not net against the data user's positions")
	}
	var final kite.OrderMargins
	for _, p := range params.OrderParams {
		value := p.Quantity * d.quotes[p.Exchange+":"+p.Tradingsymbol].LastPrice
		option := strings.HasSuffix(p.Tradingsymbol, "CE") || strings.HasSuffix(p.Tradingsymbol, "PE")
		if option && p.TransactionType == kite.TransactionTypeBuy {
			final.OptionPremium += value
			final.Total += value
			continue
		}
		final.SPAN += 0.15 * value
		final.Total += 0.15 * value
	}
	return kite.BasketMargins{Final: final}, nil
}

var (
	infy = models.Instrument{ID: "NSE:INFY", Exchange: "NSE", TradingSymbol: "INFY", InstrumentToken: 408065,
		TickSize: 0.05, LotSize: 1, InstrumentType: "EQ", Segment: "NSE"}
	nifty   = models.Instrument{ID: marketIndex, Exchange: "NSE", TradingSymbol: "NIFTY 50", InstrumentToken: 256265, InstrumentType: "EQ"}
	niftyID = models.InstrumentID(marketIndex)
	expiry  = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	call    = models.Instrument{ID: "NFO:NIFTY26OCT25000CE", Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000CE",
		InstrumentToken: 9001, TickSize: 0.05, LotSize: 75, InstrumentType: "CE", Segment: "NFO-OPT",
		Expiry: &expiry, Strike: 25000, UnderlyingID: &niftyID}
)

func catalog(_ context.Context, id models.InstrumentID) (models.Instrument, error) {
	for _, instrument := range []models.Instrument{infy, nifty, call} {
		if instrument.ID == id {
			return instrument, nil
		}
	}
	return models.Instrument{}, models.ErrNotFound
}

type fixture struct {
	t     *testing.T
	dir   string
	clock *clock
	data  *fakeData
	book  *Book
}

func newFixture(t *testing.T, capital float64) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), clock: &clock{now: at(22, 10, 0)}}
	f.data = newFakeData(f.clock)
	f.data.setQuote("NSE:INFY", infyQuote(1500))
	f.open(capital)
	return f
}

func (f *fixture) open(capital float64) {
	book, err := NewBook(Config{Dir: f.dir, Capital: capital, Now: f.clock.Now, Instruments: catalog,
		Data: func(context.Context) (Data, error) { return f.data, nil }})
	if err != nil {
		f.t.Fatal(err)
	}
	f.book = book
	f.t.Cleanup(func() { _ = book.Close() })
}

func (f *fixture) account() *Account {
	f.t.Helper()
	account, err := f.book.Open(context.Background(), "cc0006")
	if err != nil {
		f.t.Fatal(err)
	}
	return account.(*Account)
}

// infyQuote is INFY at ltp with five rupees of book either side.
func infyQuote(ltp float64) kite.QuoteData {
	q := kite.QuoteData{InstrumentToken: 408065, LastPrice: ltp, OHLC: models.OHLC{Close: 1490},
		LowerCircuitLimit: 1350, UpperCircuitLimit: 1650}
	q.Depth.Sell[0] = models.DepthItem{Price: ltp, Quantity: 10}
	q.Depth.Sell[1] = models.DepthItem{Price: ltp + 0.5, Quantity: 10}
	q.Depth.Buy[0] = models.DepthItem{Price: ltp - 0.05, Quantity: 10}
	q.Depth.Buy[1] = models.DepthItem{Price: ltp - 0.5, Quantity: 10}
	return q
}

func order(side, product, orderType string, quantity int, price, trigger float64) kite.OrderParams {
	return kite.OrderParams{Exchange: "NSE", Tradingsymbol: "INFY", TransactionType: side, Product: product,
		OrderType: orderType, Quantity: quantity, Price: price, TriggerPrice: trigger, Validity: kite.ValidityDay}
}

func (f *fixture) place(a *Account, params kite.OrderParams) string {
	f.t.Helper()
	response, err := a.PlaceOrder(kite.VarietyRegular, params)
	if err != nil {
		f.t.Fatalf("PlaceOrder(%+v) error = %v", params, err)
	}
	return response.OrderID
}

func findOrder(t *testing.T, orders kite.Orders, id string) kite.Order {
	t.Helper()
	for _, o := range orders {
		if o.OrderID == id {
			return o
		}
	}
	t.Fatalf("order %s not in %+v", id, orders)
	return kite.Order{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestMarketOrderWalksTheDepthAndSettlesIntoHoldings(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	id := f.place(a, order(kite.TransactionTypeBuy, kite.ProductCNC, kite.OrderTypeMarket, 15, 0, 0))
	if !strings.HasPrefix(id, "P261022") {
		t.Fatalf("order ID = %q, want the day in it", id)
	}
	trades, err := a.GetTrades()
	if err != nil || len(trades) != 2 || trades[0].AveragePrice != 1500 || trades[0].Quantity != 10 ||
		trades[1].AveragePrice != 1500.5 || trades[1].Quantity != 5 {
		t.Fatalf("GetTrades() = %+v, %v; want 10 at 1500 then 5 at 1500.5", trades, err)
	}
	orders, _ := a.GetOrders()
	placed := findOrder(t, orders, id)
	if placed.Status != kite.OrderStatusComplete || placed.FilledQuantity != 15 || placed.AveragePrice != 1500.17 {
		t.Fatalf("order = %+v; want complete, 15 at 1500.17", placed)
	}
	fills, _ := a.user.store.Fills(context.Background())
	charges := fills[0].Charges + fills[1].Charges
	if charges <= 0 {
		t.Fatalf("fills carry no charges: %+v", fills)
	}
	margins, err := a.GetUserMargins()
	want := 1_00_000 - 22_502.5 - charges
	if err != nil || !near(margins.Equity.Net, want) || margins.Equity.Available.OpeningBalance != 1_00_000 ||
		!near(margins.Equity.Used.Delivery, 22_502.5) {
		t.Fatalf("GetUserMargins() = %+v, %v; want net %.2f", margins.Equity, err, want)
	}
	positions, _ := a.GetPositions()
	if len(positions.Net) != 1 || positions.Net[0].Quantity != 15 || positions.Net[0].Product != kite.ProductCNC {
		t.Fatalf("positions = %+v; want today's 15 INFY CNC", positions.Net)
	}
	if holdings, _ := a.GetHoldings(); len(holdings) != 0 {
		t.Fatalf("holdings on the day of buying = %+v; want none until it settles", holdings)
	}

	f.clock.set(at(23, 10, 0))
	holdings, err := a.GetHoldings()
	if err != nil || len(holdings) != 1 || holdings[0].Quantity != 15 || !near(holdings[0].AveragePrice, 1500.1667) ||
		holdings[0].LastPrice != 1500 || holdings[0].ClosePrice != 1490 {
		t.Fatalf("next day GetHoldings() = %+v, %v; want 15 INFY at 1500.17", holdings, err)
	}
	if positions, _ := a.GetPositions(); len(positions.Net) != 0 {
		t.Fatalf("next day positions = %+v; want none", positions.Net)
	}
	if orders, _ := a.GetOrders(); len(orders) != 0 {
		t.Fatalf("next day orders = %+v; want only the day's", orders)
	}
	if margins, _ := a.GetUserMargins(); !near(margins.Equity.Net, want) || !near(margins.Equity.Available.OpeningBalance, want) {
		t.Fatalf("next day margins = %+v; want %.2f carried", margins.Equity, want)
	}
	if account, _ := a.user.store.Account(context.Background()); !account.SettledThrough.Equal(at(22, 0, 0)) {
		t.Fatalf("settled through %v; want 22 October", account.SettledThrough)
	}
}

func TestRestingLimitFillsWhenThePriceReachesIt(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	id := f.place(a, order(kite.TransactionTypeBuy, kite.ProductCNC, kite.OrderTypeLimit, 10, 1490, 0))
	orders, _ := a.GetOrders()
	if o := findOrder(t, orders, id); o.Status != statusOpen {
		t.Fatalf("limit below the market = %+v; want open", o)
	}
	// The cash is set aside while the order waits.
	if margins, _ := a.GetUserMargins(); !near(margins.Equity.Net, 1_00_000-14_900) {
		t.Fatalf("margins with a pending buy = %+v; want 14,900 blocked", margins.Equity)
	}
	f.clock.set(at(22, 10, 0).Add(20 * time.Second))
	f.data.setQuote("NSE:INFY", infyQuote(1489))
	changed, err := a.Match()
	if err != nil || len(changed) != 1 || changed[0].Status != kite.OrderStatusComplete || changed[0].AveragePrice != 1490 {
		t.Fatalf("Match() = %+v, %v; want the order filled at its limit", changed, err)
	}
	if again, err := a.Match(); err != nil || len(again) != 0 {
		t.Fatalf("Match() again = %+v, %v; want nothing new", again, err)
	}
}

func TestCandlesFillOrdersBetweenReads(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	f.clock.set(at(22, 10, 0).Add(30 * time.Second))
	limit := f.place(a, order(kite.TransactionTypeBuy, kite.ProductCNC, kite.OrderTypeLimit, 10, 1490, 0))
	stop := f.place(a, order(kite.TransactionTypeSell, kite.ProductMIS, kite.OrderTypeSLM, 5, 0, 1480))
	f.data.addCandles(408065, "minute",
		// Before the orders: must not count.
		candle(at(22, 10, 0), 1500, 1500, 1470, 1499),
		candle(at(22, 10, 5), 1495, 1496, 1488, 1492),
		candle(at(22, 10, 9), 1482, 1484, 1470, 1475),
	)
	f.clock.set(at(22, 10, 30).Add(10 * time.Second))
	changed, err := a.Match()
	if err != nil || len(changed) != 2 {
		t.Fatalf("Match() = %+v, %v; want both orders changed", changed, err)
	}
	trades, _ := a.GetTrades()
	byOrder := map[string]kite.Trade{}
	for _, trade := range trades {
		byOrder[trade.OrderID] = trade
	}
	if trade := byOrder[limit]; trade.AveragePrice != 1490 || !trade.FillTimestamp.Equal(at(22, 10, 5)) {
		t.Fatalf("limit trade = %+v; want 1490 at 10:05", trade)
	}
	// The stop opened below its trigger: it fills at the open.
	if trade := byOrder[stop]; trade.AveragePrice != 1480 || !trade.FillTimestamp.Equal(at(22, 10, 9)) {
		t.Fatalf("stop trade = %+v; want 1480 at 10:09", trade)
	}
}

func TestOrdersAreCheckedLikeKite(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	cases := []struct {
		name   string
		when   time.Time
		shut   bool
		params kite.OrderParams
		want   string
	}{
		{"weekend", at(24, 11, 0), false, order("BUY", "CNC", "MARKET", 1, 0, 0), msgMarketClosed},
		{"before the open", at(22, 9, 0), false, order("BUY", "CNC", "MARKET", 1, 0, 0), msgMarketClosed},
		{"holiday", at(22, 11, 0), true, order("BUY", "CNC", "MARKET", 1, 0, 0), msgMarketClosed},
		{"intraday after the square-off", at(22, 15, 21), false, order("BUY", "MIS", "MARKET", 1, 0, 0), "MIS orders are blocked"},
		{"off the tick", at(22, 11, 0), false, order("BUY", "CNC", "LIMIT", 1, 1490.03, 0), "tick size"},
		{"stop past the price", at(22, 11, 0), false, order("BUY", "CNC", "SL-M", 1, 0, 1490), "should be higher than the last traded price"},
		{"unknown", at(22, 11, 0), false, kite.OrderParams{Exchange: "NSE", Tradingsymbol: "NOPE", TransactionType: "BUY",
			Product: "CNC", OrderType: "MARKET", Quantity: 1}, msgNoInstrument},
		{"delivery derivative", at(22, 11, 0), false, kite.OrderParams{Exchange: "NFO", Tradingsymbol: call.TradingSymbol,
			TransactionType: "BUY", Product: "CNC", OrderType: "MARKET", Quantity: 75}, "Use NRML or MIS"},
		{"part lot", at(22, 11, 0), false, kite.OrderParams{Exchange: "NFO", Tradingsymbol: call.TradingSymbol,
			TransactionType: "BUY", Product: "NRML", OrderType: "MARKET", Quantity: 50}, "lot size 75"},
		{"expired", at(28, 11, 0), false, kite.OrderParams{Exchange: "NFO", Tradingsymbol: call.TradingSymbol,
			TransactionType: "BUY", Product: "NRML", OrderType: "MARKET", Quantity: 75}, msgNoInstrument},
	}
	for _, tc := range cases {
		f.clock.set(tc.when)
		f.data.mu.Lock()
		f.data.shut = tc.shut
		f.data.mu.Unlock()
		response, err := a.PlaceOrder(kite.VarietyRegular, tc.params)
		if err == nil || !strings.Contains(err.Error(), tc.want) || response.OrderID != "" {
			t.Errorf("%s: PlaceOrder() = %+v, %v; want %q", tc.name, response, err, tc.want)
		}
	}
	if orders, _ := a.user.store.Orders(context.Background()); len(orders) != 0 {
		t.Fatalf("checked orders were recorded: %+v", orders)
	}
}

func TestRiskChecksRejectIntoTheOrderBook(t *testing.T) {
	f := newFixture(t, 10_000)
	a := f.account()
	response, err := a.PlaceOrder(kite.VarietyRegular, order("BUY", "CNC", "MARKET", 10, 0, 0))
	if err == nil || !strings.Contains(err.Error(), "Insufficient funds") || response.OrderID == "" {
		t.Fatalf("PlaceOrder(too big) = %+v, %v; want a rejection with its order ID", response, err)
	}
	orders, _ := a.GetOrders()
	if o := findOrder(t, orders, response.OrderID); o.Status != kite.OrderStatusRejected || !strings.Contains(o.StatusMessage, "Required margin is 15000.00") {
		t.Fatalf("rejected order = %+v", o)
	}
	if _, err := a.PlaceOrder(kite.VarietyRegular, order("SELL", "CNC", "MARKET", 1, 0, 0)); err == nil || !strings.Contains(err.Error(), "Insufficient holdings") {
		t.Fatalf("PlaceOrder(sell unheld) error = %v; want insufficient holdings", err)
	}
	if _, err := a.PlaceOrder(kite.VarietyRegular, order("BUY", "CNC", "LIMIT", 1, 1700, 0)); err == nil || !strings.Contains(err.Error(), "upper circuit") {
		t.Fatalf("PlaceOrder(past the circuit) error = %v; want the circuit", err)
	}
}

func TestModifyAndCancel(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	id := f.place(a, order("BUY", "CNC", "LIMIT", 10, 1480, 0))
	modified := order("BUY", "CNC", "LIMIT", 12, 1485, 0)
	if _, err := a.ModifyOrder(kite.VarietyRegular, id, modified); err != nil {
		t.Fatalf("ModifyOrder() error = %v", err)
	}
	orders, _ := a.GetOrders()
	if o := findOrder(t, orders, id); o.Quantity != 12 || o.Price != 1485 || !o.Modified || o.PendingQuantity != 12 {
		t.Fatalf("modified order = %+v", o)
	}
	// Moving the limit through the market fills it.
	if _, err := a.ModifyOrder(kite.VarietyRegular, id, order("BUY", "CNC", "LIMIT", 12, 1500.5, 0)); err != nil {
		t.Fatalf("ModifyOrder(through the market) error = %v", err)
	}
	orders, _ = a.GetOrders()
	if o := findOrder(t, orders, id); o.Status != kite.OrderStatusComplete || o.FilledQuantity != 12 {
		t.Fatalf("order moved through the market = %+v; want complete", o)
	}
	if _, err := a.CancelOrder(kite.VarietyRegular, id, nil); err == nil {
		t.Fatal("CancelOrder(complete) succeeded")
	}
	open := f.place(a, order("BUY", "CNC", "LIMIT", 1, 1400, 0))
	if _, err := a.CancelOrder(kite.VarietyRegular, open, nil); err != nil {
		t.Fatalf("CancelOrder() error = %v", err)
	}
	history, err := a.GetOrderHistory(open)
	if err != nil || len(history) != 2 || history[1].Status != kite.OrderStatusCancelled || history[1].CancelledQuantity != 1 {
		t.Fatalf("GetOrderHistory() = %+v, %v; want open then cancelled", history, err)
	}
}

func TestDayEndSquaresOffIntradayAndCancelsTheRest(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	f.place(a, order("BUY", "MIS", "MARKET", 10, 0, 0))
	resting := f.place(a, order("BUY", "CNC", "LIMIT", 1, 1400, 0))
	f.data.addCandles(408065, "minute",
		candle(at(22, 11, 0), 1500, 1505, 1495, 1502),
		candle(at(22, 15, 19), 1508, 1511, 1507, 1510),
		candle(at(22, 15, 25), 1510, 1512, 1495, 1500),
	)
	f.clock.set(at(23, 9, 0))
	if _, err := a.GetPositions(); err != nil {
		t.Fatalf("GetPositions() error = %v", err)
	}
	orders, _ := a.user.store.Orders(context.Background())
	if o := findOrder(t, orders, resting); o.Status != kite.OrderStatusCancelled || o.StatusMessage != "Cancelled at the close." {
		t.Fatalf("resting order after the close = %+v", o)
	}
	var squaredOff *kite.Order
	for i := range orders {
		if orders[i].Tag == systemTag {
			squaredOff = &orders[i]
		}
	}
	if squaredOff == nil || squaredOff.TransactionType != "SELL" || squaredOff.AveragePrice != 1510 ||
		squaredOff.Status != kite.OrderStatusComplete || !squaredOff.OrderTimestamp.Equal(at(22, 15, 20)) {
		t.Fatalf("square-off = %+v; want SELL 10 at the 15:19 close", squaredOff)
	}
	fills, _ := a.user.store.Fills(context.Background())
	charges := 0.0
	for _, fill := range fills {
		charges += fill.Charges
	}
	margins, _ := a.GetUserMargins()
	if want := 1_00_000 + 100 - charges; !near(margins.Equity.Net, want) {
		t.Fatalf("margins after the square-off = %+v; want %.2f", margins.Equity, want)
	}
}

func TestLiveSquareOffAtTheQuote(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	f.place(a, order("SELL", "MIS", "MARKET", 10, 0, 0))
	f.data.candleErr = errors.New("candles lag")
	f.clock.set(at(22, 15, 21))
	f.data.setQuote("NSE:INFY", infyQuote(1490))
	positions, err := a.GetPositions()
	if err != nil || len(positions.Net) != 1 || positions.Net[0].Quantity != 0 || !near(positions.Net[0].Realised, 99.5) {
		t.Fatalf("GetPositions() after 15:20 = %+v, %v; want closed at 1490 for 99.50", positions.Net, err)
	}
	// Settling the day does not square off again.
	f.data.candleErr = nil
	f.clock.set(at(23, 9, 0))
	if _, err := a.GetPositions(); err != nil {
		t.Fatal(err)
	}
	fills, _ := a.user.store.Fills(context.Background())
	if len(fills) != 2 {
		t.Fatalf("fills = %+v; want the sale and one square-off", fills)
	}
}

func TestSettlingAClosedDayNeedsItsCandles(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	f.place(a, order("BUY", "CNC", "LIMIT", 1, 1400, 0))
	f.data.candleErr = errors.New("no session")
	f.clock.set(at(23, 9, 0))
	if _, err := a.GetPositions(); err == nil || !strings.Contains(err.Error(), "settle 2026-10-22") {
		t.Fatalf("GetPositions() error = %v; want the settlement to fail", err)
	}
	f.data.candleErr = nil
	if _, err := a.GetPositions(); err != nil {
		t.Fatalf("GetPositions() once candles are back error = %v", err)
	}
}

func TestOptionsSettleAtExpiry(t *testing.T) {
	f := newFixture(t, 1_00_000)
	f.clock.set(at(27, 10, 0))
	q := kite.QuoteData{InstrumentToken: 9001, LastPrice: 100}
	q.Depth.Sell[0] = models.DepthItem{Price: 100, Quantity: 1500}
	f.data.setQuote(string(call.ID), q)
	a := f.account()
	params := kite.OrderParams{Exchange: "NFO", Tradingsymbol: call.TradingSymbol, TransactionType: "BUY",
		Product: "NRML", OrderType: "MARKET", Quantity: 75}
	f.place(a, params)
	margins, _ := a.GetUserMargins()
	if !near(margins.Equity.Used.OptionPremium, 7500) {
		t.Fatalf("margins after buying = %+v; want 7,500 premium", margins.Equity)
	}
	f.data.addCandles(256265, "day", candle(at(27, 0, 0), 25050, 25150, 25000, 25120))
	f.clock.set(at(28, 10, 0))
	if positions, err := a.GetPositions(); err != nil || len(positions.Net) != 0 {
		t.Fatalf("positions after expiry = %+v, %v; want none", positions.Net, err)
	}
	fills, _ := a.user.store.Fills(context.Background())
	last := fills[len(fills)-1]
	if last.Kind != models.PaperFillExpiry || last.Price != 120 || last.TransactionType != "SELL" || last.Quantity != 75 || last.Charges != 0 {
		t.Fatalf("expiry fill = %+v; want SELL 75 at 120 intrinsic", last)
	}
	margins, _ = a.GetUserMargins()
	if want := 1_00_000 - 7500 + 9000 - fills[0].Charges; !near(margins.Equity.Net, want) {
		t.Fatalf("margins after expiry = %+v; want %.2f", margins.Equity, want)
	}
}

func TestMarginsFallBackWithoutTheCalculator(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	f.place(a, order("SELL", "MIS", "MARKET", 10, 0, 0))
	margins, _ := a.GetUserMargins()
	if !near(margins.Equity.Used.Span, 0.15*15_000) {
		t.Fatalf("calculator margins = %+v; want 15%% of value", margins.Equity.Used)
	}
	f.data.basketErr = errors.New("down")
	f.clock.set(at(22, 10, 1))
	margins, _ = a.GetUserMargins()
	if !near(margins.Equity.Used.Span, 0.20*15_000) {
		t.Fatalf("fallback margins = %+v; want 20%% of value", margins.Equity.Used)
	}
}

func TestReadsWithoutMarketData(t *testing.T) {
	f := newFixture(t, 1_00_000)
	f.place(f.account(), order("BUY", "CNC", "MARKET", 1, 0, 0))
	book, err := NewBook(Config{Dir: f.dir, Now: f.clock.Now, Instruments: catalog,
		Data: func(context.Context) (Data, error) { return nil, errors.New("data user is logged out") }})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.book.Close()
	defer book.Close()
	account, _ := book.Open(context.Background(), "CC0006")
	positions, err := account.GetPositions()
	if err != nil || len(positions.Net) != 1 || positions.Net[0].LastPrice != 0 {
		t.Fatalf("GetPositions() without data = %+v, %v; want the position unpriced", positions.Net, err)
	}
	if _, err := account.PlaceOrder(kite.VarietyRegular, order("BUY", "CNC", "MARKET", 1, 0, 0)); err == nil || !strings.Contains(err.Error(), "logged out") {
		t.Fatalf("PlaceOrder() without data error = %v", err)
	}
}

func TestReopeningKeepsTheAccount(t *testing.T) {
	f := newFixture(t, 1_00_000)
	first := f.place(f.account(), order("BUY", "CNC", "MARKET", 1, 0, 0))
	if err := f.book.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Open(context.Background(), "CC0006"); err == nil {
		t.Fatal("Open() on a closed book succeeded")
	}
	f.open(5)
	a := f.account()
	second := f.place(a, order("BUY", "CNC", "MARKET", 1, 0, 0))
	if second <= first {
		t.Fatalf("order IDs %s then %s; want them to keep counting", first, second)
	}
	if a.user.account.Capital != 1_00_000 {
		t.Fatalf("capital = %v; want the account as first opened", a.user.account.Capital)
	}
	// The account is in the user's database, not a file of its own.
	path, _ := repository.PortfolioDatabasePath(f.dir, "CC0006")
	legacy, _ := repository.LegacyPaperDatabasePath(f.dir, "CC0006")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no user database: %v", err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a separate paper database exists: %v", err)
	}
	var _ broker.Opener = f.book.Open
}

func TestResetOpensTheAccountAgain(t *testing.T) {
	f := newFixture(t, 1_00_000)
	a := f.account()
	first := f.place(a, order("BUY", "CNC", "MARKET", 1, 0, 0))
	f.clock.set(f.clock.Now().Add(time.Hour))
	if err := f.book.Reset(context.Background(), "cc0006"); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	// An account opened before the reset sees it too.
	orders, err := a.GetOrders()
	if err != nil || len(orders) != 0 {
		t.Fatalf("GetOrders() after reset = %+v, %v", orders, err)
	}
	positions, err := a.GetPositions()
	if err != nil || len(positions.Net) != 0 {
		t.Fatalf("GetPositions() after reset = %+v, %v", positions.Net, err)
	}
	if got := a.user.account; got.Capital != 1_00_000 || !got.CreatedAt.Equal(f.clock.Now()) {
		t.Fatalf("account after reset = %+v", got)
	}
	if again := f.place(f.account(), order("BUY", "CNC", "MARKET", 1, 0, 0)); again != first {
		t.Fatalf("first order after reset = %s; want numbering to start over at %s", again, first)
	}
}
