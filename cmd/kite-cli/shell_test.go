package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
)

func TestKiteAppMovesBetweenUsersPortfolioAndInstruments(t *testing.T) {
	manager, err := credentials.NewUserManager(fakeCredentials{users: []credentials.Credentials{
		{UserID: "AB1234", UserName: "Asha", IsAPIUser: true, AccessToken: "token"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeCatalog{underlyings: []models.Instrument{
		{ID: "NSE:RELIANCE", TradingSymbol: "RELIANCE", DisplayName: "RELIANCE", FuturesCount: 1},
	}}
	nifty := models.InstrumentID("NSE:NIFTY 50")
	var loadedMu sync.Mutex
	var loaded []string
	load := func(_ context.Context, user credentials.Credentials) portfolio {
		loadedMu.Lock()
		loaded = append(loaded, user.UserID)
		loadedMu.Unlock()
		return portfolio{
			positions: []kite.Position{
				{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", Product: "NRML", Quantity: 75, PnL: -1500, BuyQuantity: 75, BuyPrice: 25200, BuyValue: 1890000},
				{Tradingsymbol: "NIFTY26OCT25000PE", Exchange: "NFO", Product: "NRML", Quantity: 75, PnL: 500},
				{Tradingsymbol: "INFY", Exchange: "NSE", Product: "CNC", Quantity: 10, PnL: 20},
			},
			holdings: kite.Holdings{{Tradingsymbol: "RELIANCE", Quantity: 10, AveragePrice: 1000, LastPrice: 1100}},
			fundsErr: errors.New("funds unavailable"),
			loadedAt: time.Now(),
			loggedIn: true,
			instruments: map[models.InstrumentID]models.Instrument{
				"NFO:NIFTY26OCTFUT":     {ID: "NFO:NIFTY26OCTFUT", DisplayName: "NIFTY OCT FUT", LotSize: 75, UnderlyingID: &nifty},
				"NFO:NIFTY26OCT25000PE": {ID: "NFO:NIFTY26OCT25000PE", DisplayName: "NIFTY 25000 PE", LotSize: 75, UnderlyingID: &nifty},
				nifty:                   {ID: nifty, DisplayName: "NIFTY 50"},
			},
		}
	}
	instruments := newInstrumentApp(context.Background(), ":memory:", func(context.Context) (catalog, listStore, error) {
		return store, nil, nil
	}, nil)
	app := newKiteApp(context.Background(), manager, "credentials.sqlite3", instruments, load)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(160, 50)
	app.application.SetScreen(screen)

	done := make(chan error, 1)
	go func() { done <- app.run() }()
	defer instruments.stop()
	key := func(k tcell.Key) { app.application.QueueEvent(tcell.NewEventKey(k, 0, tcell.ModNone)) }

	// The users screen comes first; the catalog stays closed until I.
	waitForApp(t, instruments, func() bool { return app.screen == screenUsers && len(app.users.users) == 1 })
	if instruments.started {
		t.Fatal("instruments started before they were opened")
	}

	key(tcell.KeyEnter)
	waitForApp(t, instruments, func() bool {
		return app.screen == screenPortfolio && app.portfolio.positions.GetCell(3, 0).Text == "  NIFTY OCT FUT"
	})
	// Rows show display names. INFY sorts first and, alone, has no group
	// row; NIFTY's two do.
	positionsColumn := func(column int) []string {
		var cells []string
		for row := 1; row < app.portfolio.positions.GetRowCount(); row++ {
			cells = append(cells, app.portfolio.positions.GetCell(row, column).Text)
		}
		return cells
	}
	if got, want := strings.Join(positionsColumn(0), "|"), "INFY|▾ NIFTY 50 (2)|  NIFTY OCT FUT|  NIFTY 25000 PE"; got != want {
		t.Fatalf("positions = %q, want %q", got, want)
	}
	if got := strings.Join(positionsColumn(6), "|"); got != "+20.00|-1,000.00|-1,500.00|+500.00" {
		t.Fatalf("position P&L = %q", got)
	}

	// The details pane follows the selection: a position's instrument and
	// activity, then the group's legs.
	app.application.QueueUpdate(func() { app.portfolio.positions.Select(3, 0) })
	waitForApp(t, instruments, func() bool {
		detail := app.portfolio.detail.GetText(true)
		return strings.Contains(detail, "NFO:NIFTY26OCTFUT") && strings.Contains(detail, "75 @ 25,200.00 (18.90L)")
	})
	app.application.QueueUpdate(func() { app.portfolio.positions.Select(2, 0) })
	waitForApp(t, instruments, func() bool { return strings.Contains(app.portfolio.detail.GetText(true), "Legs") })

	// Enter on the group row collapses it and again expands it.
	key(tcell.KeyEnter)
	waitForApp(t, instruments, func() bool { return app.portfolio.positions.GetRowCount() == 3 })
	if got := app.portfolio.positions.GetCell(2, 0).Text; got != "▸ NIFTY 50 (2)" {
		t.Fatalf("collapsed group row = %q", got)
	}
	if row, _ := app.portfolio.positions.GetSelection(); row != 2 {
		t.Fatalf("selection moved to row %d", row)
	}
	key(tcell.KeyEnter)
	waitForApp(t, instruments, func() bool { return app.portfolio.positions.GetRowCount() == 5 })
	if got := app.portfolio.holdings.GetCell(1, 6).Text; got != "+1,000.00" {
		t.Fatalf("holding P&L = %q", got)
	}
	if got := app.portfolio.funds.GetCell(1, 0).Text; !strings.Contains(got, "funds unavailable") {
		t.Fatalf("funds error row = %q", got)
	}
	if status := app.portfolio.status.GetText(true); !strings.Contains(status, "logged in again") {
		t.Fatalf("status = %q, want the re-login noted", status)
	}
	if header := app.portfolio.header.GetText(true); !strings.Contains(header, "AB1234") {
		t.Fatalf("header = %q", header)
	}

	// I opens the instruments from the portfolio; Q comes back to it.
	pressRune(instruments, 'i')
	waitForApp(t, instruments, func() bool {
		return app.screen == screenInstruments && instruments.catalog == store && !instruments.busy
	})
	pressRune(instruments, 'q')
	waitForApp(t, instruments, func() bool { return app.screen == screenPortfolio })
	pressRune(instruments, 'q')
	waitForApp(t, instruments, func() bool { return app.screen == screenUsers })

	// From the users screen, Q from the instruments returns to the users.
	pressRune(instruments, 'i')
	waitForApp(t, instruments, func() bool { return app.screen == screenInstruments })
	pressRune(instruments, 'q')
	waitForApp(t, instruments, func() bool { return app.screen == screenUsers })

	key(tcell.KeyCtrlC)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("app did not stop")
	}
	if store.closeCalls != 1 {
		t.Fatalf("catalog closed %d times, want 1", store.closeCalls)
	}
}

func TestFormatAmountGroupsIndianStyle(t *testing.T) {
	for value, want := range map[float64]string{
		0: "0.00", 999.5: "999.50", 1234.5: "1,234.50", 1234567.891: "12,34,567.89", -12345678: "-1,23,45,678.00",
	} {
		if got := formatAmount(value); got != want {
			t.Errorf("formatAmount(%v) = %q, want %q", value, got, want)
		}
	}
	if got := formatSigned(1500); got != "+1,500.00" {
		t.Errorf("formatSigned(1500) = %q", got)
	}
}

func TestGroupPositionsSubtotalsByUnderlying(t *testing.T) {
	positions := []kite.Position{
		{Tradingsymbol: "NIFTY26OCT25000CE", Exchange: "NFO", PnL: 100},
		{Tradingsymbol: "BANKNIFTY26OCTFUT", Exchange: "NFO", PnL: -40},
		{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", PnL: 250},
		{Tradingsymbol: "INFY", Exchange: "NSE", PnL: 5},
	}
	nifty, bank := models.InstrumentID("NSE:NIFTY 50"), models.InstrumentID("NSE:NIFTY BANK")
	groups := groupPositions(positions, map[models.InstrumentID]models.Instrument{
		"NFO:NIFTY26OCT25000CE": {UnderlyingID: &nifty},
		"NFO:NIFTY26OCTFUT":     {UnderlyingID: &nifty},
		"NFO:BANKNIFTY26OCTFUT": {UnderlyingID: &bank},
		"NSE:INFY":              {DisplayName: "INFOSYS"},
	})
	type summary struct {
		underlying models.InstrumentID
		count      int
		pnl        float64
	}
	var got []summary
	for _, group := range groups {
		got = append(got, summary{group.underlying, len(group.positions), group.pnl})
	}
	want := []summary{{"NSE:INFY", 1, 5}, {"NSE:NIFTY 50", 2, 350}, {"NSE:NIFTY BANK", 1, -40}}
	if len(got) != len(want) {
		t.Fatalf("groups = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("groups = %+v, want %+v", got, want)
		}
	}
	if groups[1].positions[0].Tradingsymbol != "NIFTY26OCT25000CE" {
		t.Fatalf("positions reordered within a group: %+v", groups[1].positions)
	}
}

func TestCatalogInstrumentsAndSharedCatalog(t *testing.T) {
	nifty := models.InstrumentID("NSE:NIFTY 50")
	store := &fakeCatalog{
		underlyings: []models.Instrument{{ID: nifty, DisplayName: "NIFTY 50"}},
		futures:     []models.Instrument{{ID: "NFO:NIFTY26OCTFUT", DisplayName: "NIFTY OCT FUT", UnderlyingID: &nifty}},
	}
	opens := 0
	shared := &sharedCatalog{open: func(context.Context) (catalog, listStore, error) {
		opens++
		return store, nil, nil
	}}
	resolve := catalogInstruments(shared.get)
	instruments, err := resolve(context.Background(), []kite.Position{
		{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO"},
		{Tradingsymbol: "UNKNOWN", Exchange: "NSE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The position and its underlying are found; the unknown symbol is not.
	if len(instruments) != 2 || instruments["NFO:NIFTY26OCTFUT"].DisplayName != "NIFTY OCT FUT" || instruments[nifty].DisplayName != "NIFTY 50" {
		t.Fatalf("instruments = %v", instruments)
	}
	explorer, _, err := shared.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Fatalf("catalog opened %d times, want 1", opens)
	}
	if err := errors.Join(explorer.Close(), shared.close()); err != nil {
		t.Fatal(err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("catalog closed %d times, want 1", store.closeCalls)
	}
}

// fakePrices quotes every instrument at a fixed price, closing at 100.
type fakePrices struct {
	mu       sync.Mutex
	prices   map[string]float64
	requests int
}

func (f *fakePrices) GetOHLC(instruments ...string) (kite.QuoteOHLC, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	quotes := make(kite.QuoteOHLC)
	for _, id := range instruments {
		if price, ok := f.prices[id]; ok {
			quote := kite.QuoteOHLCData{LastPrice: price}
			quote.OHLC.Close = 100
			quotes[id] = quote
		}
	}
	return quotes, nil
}

func (f *fakePrices) set(id string, price float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prices[id] = price
}

func TestPortfolioRepricesFromQuotes(t *testing.T) {
	data := portfolio{
		positions: []kite.Position{
			// Bought 75 at 200 and sold 25 at 210: 50 open, 250 realised.
			{Tradingsymbol: "NIFTY26OCT25000CE", Exchange: "NFO", Quantity: 50, BuyQuantity: 75, BuyValue: 15000, SellQuantity: 25, SellValue: 5250,
				BuyM2MValue: 15000, SellM2MValue: 5250, Realised: 250, LastPrice: 190, PnL: -250},
			{Tradingsymbol: "UNQUOTED", Exchange: "NSE", Quantity: 1, LastPrice: 5, PnL: 1},
		},
		holdings: kite.Holdings{{Tradingsymbol: "RELIANCE", Exchange: "NSE", Quantity: 8, T1Quantity: 2, AveragePrice: 100, LastPrice: 90}},
		prices:   &fakePrices{prices: map[string]float64{"NFO:NIFTY26OCT25000CE": 220, "NSE:RELIANCE": 110}},
	}
	priced := data.reprice()
	if priced.pricesErr != nil || priced.pricedAt.IsZero() {
		t.Fatalf("reprice err = %v, at = %v", priced.pricesErr, priced.pricedAt)
	}
	position := priced.positions[0]
	// (5250 - 15000) + 50 x 220 = 1250.
	if position.LastPrice != 220 || position.PnL != 1250 || position.M2M != 1250 || position.Unrealised != 1000 {
		t.Fatalf("position = LTP %v P&L %v M2M %v unrealised %v", position.LastPrice, position.PnL, position.M2M, position.Unrealised)
	}
	if unquoted := priced.positions[1]; unquoted.LastPrice != 5 || unquoted.PnL != 1 {
		t.Fatalf("unquoted position changed: %+v", unquoted)
	}
	holding := priced.holdings[0]
	if holding.LastPrice != 110 || holding.PnL != 100 || holding.DayChange != 10 || holding.DayChangePercentage != 10 {
		t.Fatalf("holding = %+v", holding)
	}
	if data.positions[0].LastPrice != 190 {
		t.Fatal("reprice changed the original portfolio")
	}
}

func TestPortfolioViewRepricesWhileVisible(t *testing.T) {
	prices := &fakePrices{prices: map[string]float64{"NSE:INFY": 1500}}
	load := func(context.Context, credentials.Credentials) portfolio {
		data := portfolio{
			positions: []kite.Position{{Tradingsymbol: "INFY", Exchange: "NSE", Quantity: 10, BuyValue: 14000, LastPrice: 1400}},
			loadedAt:  time.Now(),
			prices:    prices,
		}
		return data.reprice()
	}
	instruments := newInstrumentApp(context.Background(), ":memory:", func(context.Context) (catalog, listStore, error) {
		return &fakeCatalog{}, nil, nil
	}, nil)
	app := newKiteApp(context.Background(), mustManager(t), "credentials.sqlite3", instruments, load)
	app.portfolio.priceEvery = 20 * time.Millisecond
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(160, 50)
	app.application.SetScreen(screen)
	done := make(chan error, 1)
	go func() { done <- app.run() }()
	defer func() {
		instruments.stop()
		<-done
	}()

	waitForApp(t, instruments, func() bool { return len(app.users.users) == 1 })
	app.application.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	waitForApp(t, instruments, func() bool { return app.portfolio.positions.GetCell(1, 6).Text == "+1,000.00" })

	prices.set("NSE:INFY", 1600)
	waitForApp(t, instruments, func() bool {
		return app.portfolio.positions.GetCell(1, 5).Text == "1,600.00" && app.portfolio.positions.GetCell(1, 6).Text == "+2,000.00"
	})
	if header := app.portfolio.header.GetText(true); !strings.Contains(header, "live") {
		t.Fatalf("header = %q, want the live price time", header)
	}

	// Off screen, the portfolio stops repricing.
	pressRune(instruments, 'q')
	waitForApp(t, instruments, func() bool { return app.screen == screenUsers })
	prices.mu.Lock()
	before := prices.requests
	prices.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	prices.mu.Lock()
	after := prices.requests
	prices.mu.Unlock()
	if after > before+1 {
		t.Fatalf("priced %d times while hidden", after-before)
	}
}

func mustManager(t *testing.T) *credentials.UserManager {
	t.Helper()
	manager, err := credentials.NewUserManager(fakeCredentials{users: []credentials.Credentials{
		{UserID: "AB1234", UserName: "Asha", IsAPIUser: true, AccessToken: "token"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
