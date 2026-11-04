package main

import (
	"context"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

func TestPortfolioOpensTheOptionChain(t *testing.T) {
	nifty := models.InstrumentID("NSE:NIFTY 50")
	store, quotes := niftyMarket()
	// AB1234's watchlist would replace the chain if it showed after it.
	lists := &fakeListStore{lists: []models.List{{ID: "mine", CreatedBy: "AB1234", Name: "MINE", Items: []models.ListItem{{InstrumentID: "NSE:RELIANCE"}}}}}
	deps := explorerDeps(t, store, quotes, lists, &fakePlacer{})
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
		return portfolio{
			positions: []kite.Position{
				{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", Product: "NRML", Quantity: 75, LastPrice: 25000},
				{Tradingsymbol: "NIFTY26OCT25000PE", Exchange: "NFO", Product: "NRML", Quantity: -75, LastPrice: 100},
			},
			holdings: []kite.Holding{{Tradingsymbol: "INFY", Exchange: "NSE", Quantity: 10, LastPrice: 1500}},
			instruments: map[models.InstrumentID]models.Instrument{
				"NFO:NIFTY26OCTFUT":     {ID: "NFO:NIFTY26OCTFUT", Exchange: "NFO", TradingSymbol: "NIFTY26OCTFUT", LotSize: 75, UnderlyingID: &nifty},
				"NFO:NIFTY26OCT25000PE": {ID: "NFO:NIFTY26OCT25000PE", Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000PE", LotSize: 75, UnderlyingID: &nifty},
				nifty:                   {ID: nifty, DisplayName: "NIFTY 50"},
			},
			loadedAt: time.Now(),
			loggedIn: true,
		}
	}
	deps.defaultUser = "ab1234"
	h := newHarness(t, deps)
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	e := h.a.explorer
	chainOf := func(underlying models.InstrumentID) func() bool {
		return func() bool {
			return h.a.screen == screenInstruments && e.mode == viewChain && e.chain != nil && e.chain.underlying == underlying && h.idle()
		}
	}

	// O on the group opens NIFTY's chain, opening the catalog first; the
	// watchlists loading with it leave the chain alone.
	if _, ok := h.a.portfolio.focused().selectedRef().(models.InstrumentID); !ok {
		t.Fatalf("selected %T, want the group", h.a.portfolio.focused().selectedRef())
	}
	h.press("o")
	h.waitFor("NIFTY's chain", chainOf(nifty))
	h.waitFor("the watchlists", func() bool { return len(e.watchlists) == 1 && h.idle() })
	if e.mode != viewChain {
		t.Fatalf("the watchlists replaced the chain: mode %v", e.mode)
	}
	// Esc goes back to the portfolio.
	h.press("esc")
	if h.a.screen != screenPortfolio {
		t.Fatalf("Esc showed screen %v", h.a.screen)
	}

	// O on a leg opens its underlying's chain, now from the open catalog.
	h.press("down", "O")
	h.waitFor("NIFTY's chain again", chainOf(nifty))
	h.press("esc")
	if h.a.screen != screenPortfolio {
		t.Fatalf("Esc showed screen %v", h.a.screen)
	}

	// From the explorer's own dashboard, Esc on a chain still goes back to
	// the dashboard.
	h.run(h.a.openInstruments())
	h.waitFor("the dashboard", func() bool { return e.mode == viewDashboard && h.idle() })
	h.press("o")
	h.waitFor("a chain", func() bool { return e.mode == viewChain && h.idle() })
	h.press("esc")
	if h.a.screen != screenInstruments || e.mode != viewDashboard {
		t.Fatalf("Esc from the explorer's chain: screen %v, mode %v", h.a.screen, e.mode)
	}
}
