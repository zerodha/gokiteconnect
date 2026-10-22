package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/paper"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// paperMarket is the data user's market for a paper book: INFY at 1500
// with a little depth, and the index ticking at the clock.
type paperMarket struct{ now time.Time }

func (m paperMarket) GetQuote(ids ...string) (kite.Quote, error) {
	quotes := kite.Quote{}
	for _, id := range ids {
		switch id {
		case "NSE:NIFTY 50":
			quotes[id] = kite.QuoteData{LastPrice: 25000, Timestamp: models.Time{Time: m.now}}
		case "NSE:INFY":
			quote := kite.QuoteData{InstrumentToken: 408065, LastPrice: 1500, Timestamp: models.Time{Time: m.now}}
			quote.OHLC.Close = 1480
			quote.Depth.Sell[0] = models.DepthItem{Price: 1500.5, Quantity: 100}
			quote.Depth.Buy[0] = models.DepthItem{Price: 1499.5, Quantity: 100}
			quotes[id] = quote
		}
	}
	return quotes, nil
}

func (m paperMarket) GetLTP(ids ...string) (kite.QuoteLTP, error) { return kite.QuoteLTP{}, nil }

func (m paperMarket) GetOHLC(ids ...string) (kite.QuoteOHLC, error) {
	quotes, _ := m.GetQuote(ids...)
	ohlc := kite.QuoteOHLC{}
	for id, quote := range quotes {
		ohlc[id] = kite.QuoteOHLCData{InstrumentToken: quote.InstrumentToken, LastPrice: quote.LastPrice, OHLC: quote.OHLC}
	}
	return ohlc, nil
}

func (paperMarket) GetHistoricalData(int, string, time.Time, time.Time, bool, bool) ([]kite.HistoricalData, error) {
	return nil, nil
}

func (paperMarket) GetBasketMargins(kite.GetBasketParams) (kite.BasketMargins, error) {
	return kite.BasketMargins{}, nil
}

func TestPaperOrdersFillIntoThePaperPortfolio(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, ist)
	infy := models.Instrument{ID: "NSE:INFY", Exchange: "NSE", TradingSymbol: "INFY", InstrumentToken: 408065,
		TickSize: 0.05, LotSize: 1, InstrumentType: "EQ", Segment: "NSE"}
	book, err := paper.NewBook(paper.Config{
		Dir:  t.TempDir(),
		Now:  func() time.Time { return now },
		Data: func(context.Context) (paper.Data, error) { return paperMarket{now: now}, nil },
		Instruments: func(_ context.Context, id models.InstrumentID) (models.Instrument, error) {
			return infy, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = book.Close() })
	accounts := broker.Accounts{Paper: book.Open}
	ctx := context.Background()

	params, err := orderParams(infy, orderInput{side: kite.TransactionTypeBuy, orderType: kite.OrderTypeMarket, product: kite.ProductCNC, quantity: "10"})
	if err != nil {
		t.Fatal(err)
	}
	orderID, err := accountOrderPlacer(accounts)(ctx, "AB1234", broker.Paper, params)
	if err != nil || orderID == "" {
		t.Fatalf("place = %q, %v", orderID, err)
	}

	data := accountPortfolioLoader(accounts, nil)(ctx, credentials.Credentials{UserID: "AB1234"}, broker.Paper)
	if data.ordersErr != nil || data.positionsErr != nil || data.marginsErr != nil {
		t.Fatalf("load errors: orders %v, positions %v, margins %v", data.ordersErr, data.positionsErr, data.marginsErr)
	}
	if data.mode != broker.Paper || data.matcher == nil || data.loggedIn {
		t.Errorf("mode %q, matcher %v, logged in %v", data.mode, data.matcher, data.loggedIn)
	}
	if len(data.orders) != 1 || data.orders[0].OrderID != orderID || data.orders[0].Status != kite.OrderStatusComplete || data.orders[0].Tag != orderTag {
		t.Fatalf("orders = %+v", data.orders)
	}
	if len(data.positions) != 1 || data.positions[0].Quantity != 10 || data.positions[0].AveragePrice != 1500.5 {
		t.Fatalf("positions = %+v", data.positions)
	}
	if data.pricesErr != nil || data.pricedAt.IsZero() {
		t.Errorf("not priced from the data user: %v", data.pricesErr)
	}
	if net := data.margins.Equity.Net; net <= 0 || net >= paper.DefaultCapital-15005 {
		t.Errorf("net margin %v after paying 15,005 for the shares", net)
	}

	// Live trading is off without a live opener.
	live := accountPortfolioLoader(accounts, nil)(ctx, credentials.Credentials{UserID: "AB1234"}, broker.Live)
	if live.ordersErr == nil || !strings.Contains(live.ordersErr.Error(), "live trading is unavailable") || live.mode != broker.Live {
		t.Errorf("live load = %v, mode %q", live.ordersErr, live.mode)
	}
}

// fakeMatcher reports changed orders on its first Match.
type fakeMatcher struct {
	mu      sync.Mutex
	changed []kite.Order
	matches int
}

func (f *fakeMatcher) Match() ([]kite.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matches++
	changed := f.changed
	f.changed = nil
	return changed, nil
}

type noPrices struct{}

func (noPrices) GetOHLC(...string) (kite.QuoteOHLC, error) { return kite.QuoteOHLC{}, nil }

func TestModeToggleBadgeAndPaperMatching(t *testing.T) {
	store, _ := niftyMarket()
	placer := &fakePlacer{}
	deps := explorerDeps(t, store, nil, nil, placer)
	open := kite.Order{OrderID: "P261022000001", Status: "OPEN", Exchange: "NSE", TradingSymbol: "INFY", TransactionType: "BUY",
		OrderType: "LIMIT", Product: "CNC", Quantity: 10, PendingQuantity: 10, Price: 1490}
	filled := open
	filled.Status, filled.FilledQuantity, filled.PendingQuantity, filled.AveragePrice = kite.OrderStatusComplete, 10, 0, 1490
	matcher := &fakeMatcher{changed: []kite.Order{filled}}
	var mu sync.Mutex
	var modes []broker.Mode
	deps.loadPortfolio = func(_ context.Context, _ credentials.Credentials, mode broker.Mode) portfolio {
		mu.Lock()
		defer mu.Unlock()
		modes = append(modes, mode)
		data := portfolio{mode: mode, loadedAt: time.Now(), prices: noPrices{}}
		if mode == broker.Paper {
			data.matcher = matcher
			data.orders = kite.Orders{open}
		}
		return data
	}
	loads := func() []broker.Mode {
		mu.Lock()
		defer mu.Unlock()
		return append([]broker.Mode(nil), modes...)
	}
	deps.defaultUser = "ab1234"
	h := newHarness(t, deps)
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	if h.a.mode != broker.Paper || !strings.Contains(h.screen(), "PAPER") {
		t.Fatalf("kite-cli did not start in paper mode:\n%s", h.screen())
	}

	// A refresh matches the open order; its fill reloads the portfolio.
	h.run(h.a.periodic())
	h.waitFor("the fill", h.shows("Paper orders · P261022000001 BUY 10 INFY complete at"))
	if got := loads(); len(got) != 2 || got[1] != broker.Paper {
		t.Fatalf("loads = %v", got)
	}

	// P switches to live: the badge turns, and the live portfolio loads in
	// the paper one's place.
	h.press("P")
	h.waitFor("the live portfolio", func() bool { return !h.a.portfolio.loading && len(loads()) == 3 })
	if h.a.mode != broker.Live || !strings.Contains(h.screen(), "LIVE") || strings.Contains(h.screen(), "PAPER") {
		t.Fatalf("P did not switch to live:\n%s", h.screen())
	}
	if got := loads(); got[2] != broker.Live {
		t.Fatalf("loads = %v", got)
	}
	// A live account has nothing to match.
	h.run(h.a.periodic())
	h.settle()
	matcher.mu.Lock()
	matches := matcher.matches
	matcher.mu.Unlock()
	if matches != 1 {
		t.Errorf("matched %d times", matches)
	}

	// Ctrl+P switches back from any screen.
	h.press("esc")
	h.dispatch(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if h.a.mode != broker.Paper || !strings.Contains(h.screen(), "PAPER") {
		t.Fatalf("Ctrl+P did not switch to paper:\n%s", h.screen())
	}
}
