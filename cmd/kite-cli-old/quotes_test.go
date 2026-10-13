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

// fakeQuotes prices every instrument from prices and records each request.
type fakeQuotes struct {
	mu       sync.Mutex
	prices   map[string]float64
	requests [][]string
	margins  []string
}

func (f *fakeQuotes) GetQuote(instruments ...string) (kite.Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, instruments)
	quotes := make(kite.Quote, len(instruments))
	for _, id := range instruments {
		if price, ok := f.prices[id]; ok {
			quote := kite.QuoteData{LastPrice: price, OI: 1234567}
			quote.OHLC.Close = price / 1.02
			quotes[id] = quote
		}
	}
	return quotes, nil
}

// GetOrderMargins prices every order at 1.5 lakh and records the symbols.
func (f *fakeQuotes) GetOrderMargins(params kite.GetMarginParams) ([]kite.OrderMargins, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var margins []kite.OrderMargins
	for _, order := range params.OrderParams {
		f.margins = append(f.margins, order.TransactionType+" "+order.Tradingsymbol)
		margins = append(margins, kite.OrderMargins{TradingSymbol: order.Tradingsymbol, Total: 150000})
	}
	return margins, nil
}

func (f *fakeQuotes) marginRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.margins...)
}

func (f *fakeQuotes) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func TestInstrumentAppShowsPricesAndClampsChainToATM(t *testing.T) {
	expiry := cliDate(2026, time.October, 27)
	store := &fakeCatalog{
		underlyings: []models.Instrument{{ID: "NSE:NIFTY 50", TradingSymbol: "NIFTY 50", InstrumentType: "INDICES", OptionsCount: 6}},
		options: []models.Instrument{
			option("NFO:N25000CE", models.OptionTypeCall, 25000, 0, expiry),
			option("NFO:N25000PE", models.OptionTypePut, 25000, 0, expiry),
			option("NFO:N25100CE", models.OptionTypeCall, 25100, 0, expiry),
			option("NFO:N25100PE", models.OptionTypePut, 25100, 0, expiry),
			option("NFO:N25200CE", models.OptionTypeCall, 25200, 0, expiry),
			option("NFO:N25200PE", models.OptionTypePut, 25200, 0, expiry),
		},
	}
	source := &fakeQuotes{prices: map[string]float64{"NSE:NIFTY 50": 25120, "NFO:N25100CE": 95.5}}
	var loads int
	var loadsMu sync.Mutex
	ui := newInstrumentApp(context.Background(), ":memory:", func(context.Context) (catalog, listStore, error) {
		return store, nil, nil
	}, nil)
	ui.loadQuotes = func(context.Context) (quoteSource, string, error) {
		loadsMu.Lock()
		defer loadsMu.Unlock()
		loads++
		return source, "DA0001", nil
	}
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(140, 40)
	ui.application.SetScreen(screen)

	done := make(chan error, 1)
	go func() { done <- ui.run() }()
	defer ui.stop()

	waitForApp(t, ui, func() bool { return ui.results.GetCell(1, resultsLTPColumn).Text == "25120.00" })
	if got := ui.results.GetCell(1, resultsLTPColumn+1).Text; got != "+2.00%" {
		t.Fatalf("change = %q, want +2.00%%", got)
	}
	if header := ui.header.GetText(true); !strings.Contains(header, "prices DA0001") {
		t.Fatalf("header = %q, want the data user", header)
	}
	if detail := ui.detail.GetText(true); !strings.Contains(detail, "Market") {
		t.Fatalf("detail = %q, want a market section", detail)
	}

	// R reconnects the data user and fetches again.
	before := source.requestCount()
	pressRune(ui, 'r')
	waitForApp(t, ui, func() bool { return source.requestCount() > before })
	loadsMu.Lock()
	if loads != 2 {
		t.Fatalf("loader calls = %d, want 2 after R", loads)
	}
	loadsMu.Unlock()

	// The chain quotes the underlying and its legs, then selects the ATM row.
	ui.application.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	waitForApp(t, ui, func() bool {
		row, _ := ui.chain.GetSelection()
		return ui.mode == viewChain && ui.chain.GetCell(2, chainCallLTPColumn).Text == "95.50" && row == 2
	})
	if got := ui.chain.GetCell(2, chainCallOIColumn).Text; !strings.Contains(got, "12.35L") || !strings.Contains(got, "[:"+callOIBarColor+"]") {
		t.Fatalf("call OI = %q, want 12.35L over a bar", got)
	}
	if got := ui.chain.GetCell(1, chainCallLTPColumn).Text; got != "—" {
		t.Fatalf("unpriced call = %q, want a dash", got)
	}
	if got := ui.chain.GetCell(2, chainCallBEColumn).Text; got != "+0.30%" {
		t.Fatalf("call breakeven = %q, want the move from spot by default", got)
	}

	// The summary bar shows the selected call's premium and, once fetched,
	// its sell margin per lot.
	waitForApp(t, ui, func() bool {
		status := ui.status.GetText(true)
		return strings.Contains(status, "premium/lot ₹95.50") && strings.Contains(status, "sell margin/lot ₹1,50,000.00")
	})
	if got := source.marginRequests(); len(got) != 1 || got[0] != "SELL NFO:N25100CE" {
		t.Fatalf("margin requests = %v, want only the settled ATM call sold", got)
	}

	// % switches breakevens to prices: strike + premium.
	pressRune(ui, '%')
	waitForApp(t, ui, func() bool { return ui.chain.GetCell(2, chainCallBEColumn).Text == "25195.50" })
	if got := ui.chain.GetCell(0, chainCallBEColumn).Text; got != "CALL BE" {
		t.Fatalf("breakeven header = %q", got)
	}

	ui.application.QueueEvent(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModCtrl))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("app did not stop")
	}
}

type fakeCredentials struct {
	credentials.CredentialsRepository
	users []credentials.Credentials
}

func (f fakeCredentials) GetUserByID(_ context.Context, id string) (*credentials.Credentials, error) {
	for i := range f.users {
		if f.users[i].UserID == id {
			return &f.users[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (f fakeCredentials) ListAvailableUsers(context.Context) ([]credentials.Credentials, error) {
	return f.users, nil
}

func TestDataUserSelection(t *testing.T) {
	store := fakeCredentials{users: []credentials.Credentials{
		{UserID: "WEB01"},
		{UserID: "API01", IsAPIUser: true, AccessToken: "token"},
		{UserID: "DATA01", IsAPIUser: true, DataSubscription: true, AccessToken: "token"},
		{UserID: "DATA02", IsAPIUser: true, DataSubscription: true},
	}}
	ctx := context.Background()
	if user, err := dataUser(ctx, store, ""); err != nil || user.UserID != "DATA01" {
		t.Fatalf("default data user = %v, %v; want DATA01", user, err)
	}
	if user, err := dataUser(ctx, store, "API01"); err != nil || user.UserID != "API01" {
		t.Fatalf("named data user = %v, %v; want API01", user, err)
	}
	for _, id := range []string{"WEB01", "DATA02", "MISSING"} {
		if _, err := dataUser(ctx, store, id); err == nil {
			t.Fatalf("dataUser(%s) succeeded, want an error", id)
		}
	}
	if _, err := dataUser(ctx, fakeCredentials{}, ""); err == nil {
		t.Fatal("no users succeeded, want an error")
	}
}

func TestBreakevenFromStrikeAndPremium(t *testing.T) {
	call := models.Instrument{InstrumentType: string(models.OptionTypeCall), Strike: 25000}
	put := models.Instrument{InstrumentType: string(models.OptionTypePut), Strike: 25000}
	if got := breakeven(call, 120); got != 25120 {
		t.Errorf("call breakeven = %v, want 25120", got)
	}
	if got := breakeven(put, 120); got != 24880 {
		t.Errorf("put breakeven = %v, want 24880", got)
	}
	for _, test := range []struct {
		value, spot float64
		percent     bool
		want        string
	}{
		{24880, 25000, false, "24880.00"},
		{24880, 25000, true, "-0.48%"},
		{25250, 25000, true, "+1.00%"},
		{25250, 0, true, "—"},
	} {
		if got := formatBreakeven(test.value, test.spot, test.percent); got != test.want {
			t.Errorf("formatBreakeven(%v, %v, %v) = %q, want %q", test.value, test.spot, test.percent, got, test.want)
		}
	}
}

func TestOIBarScalesToLargest(t *testing.T) {
	if got, want := oiBar("5.00L", 500000, 1000000, true), "     [:"+callOIBarColor+"]5.00L[:-]"; got != want {
		t.Fatalf("call bar = %q, want %q", got, want)
	}
	if got, want := oiBar("10.00L", 1000000, 1000000, false), "[:"+putOIBarColor+"]10.00L    [:-]"; got != want {
		t.Fatalf("put bar = %q, want %q", got, want)
	}
	if got, want := oiBar("—", 0, 1000000, false), "—         "; got != want {
		t.Fatalf("empty bar = %q, want %q", got, want)
	}
}
