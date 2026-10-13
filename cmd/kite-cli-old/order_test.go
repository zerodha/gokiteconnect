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
	"github.com/rivo/tview"
)

// fakePlacer records orders instead of sending them to Kite, failing while
// err is set.
type fakePlacer struct {
	mu     sync.Mutex
	err    error
	orders []placedOrder
}

type placedOrder struct {
	userID string
	params kite.OrderParams
}

func (f *fakePlacer) place(_ context.Context, userID string, params kite.OrderParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.orders = append(f.orders, placedOrder{userID, params})
	return "2510120001", nil
}

func (f *fakePlacer) placed() []placedOrder {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]placedOrder(nil), f.orders...)
}

func (f *fakePlacer) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func TestOrdersFromPortfolioAndInstruments(t *testing.T) {
	manager, err := credentials.NewUserManager(fakeCredentials{users: []credentials.Credentials{
		{UserID: "AB1234", UserName: "Asha", IsAPIUser: true, AccessToken: "token"},
		{UserID: "CC0006", UserName: "Chandra", EncToken: "token"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	nifty := models.InstrumentID("NSE:NIFTY 50")
	var loadsMu sync.Mutex
	var loads []string
	load := func(_ context.Context, user credentials.Credentials) portfolio {
		loadsMu.Lock()
		loads = append(loads, user.UserID)
		loadsMu.Unlock()
		return portfolio{
			positions: []kite.Position{
				{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", Product: "NRML", Quantity: -150, LastPrice: 25210},
			},
			holdings: kite.Holdings{{Tradingsymbol: "RELIANCE", Exchange: "NSE", Quantity: 10, T1Quantity: 2, LastPrice: 1100}},
			loadedAt: time.Now(),
			instruments: map[models.InstrumentID]models.Instrument{
				"NFO:NIFTY26OCTFUT": {ID: "NFO:NIFTY26OCTFUT", Exchange: "NFO", TradingSymbol: "NIFTY26OCTFUT", DisplayName: "NIFTY OCT FUT", LotSize: 75, InstrumentType: "FUT", UnderlyingID: &nifty},
			},
		}
	}
	loadCount := func() int {
		loadsMu.Lock()
		defer loadsMu.Unlock()
		return len(loads)
	}
	// NIFTY calls over two expiries, the nearer with three strikes.
	near, far := time.Now().AddDate(0, 0, 7), time.Now().AddDate(0, 0, 14)
	call := func(id models.InstrumentID, strike float64, number int, expiry time.Time) models.Instrument {
		contract := option(id, models.OptionTypeCall, strike, number, expiry)
		contract.Exchange, contract.LotSize, contract.UnderlyingID = "NFO", 75, &nifty
		return contract
	}
	store := &fakeCatalog{
		underlyings: []models.Instrument{
			{ID: "NSE:RELIANCE", Exchange: "NSE", TradingSymbol: "RELIANCE", DisplayName: "RELIANCE", Segment: "NSE", LotSize: 1, FuturesCount: 1},
		},
		options: []models.Instrument{
			call("NFO:N1C25000", 25000, 1, near), call("NFO:N1C25200", 25200, 1, near), call("NFO:N1C25400", 25400, 1, near),
			call("NFO:N2C25200", 25200, 2, far),
			option("NFO:N1P25200", models.OptionTypePut, 25200, 1, near),
		},
	}
	instruments := newInstrumentApp(context.Background(), ":memory:", func(context.Context) (catalog, listStore, error) {
		return store, nil, nil
	}, nil)
	prices := &fakeQuotes{prices: map[string]float64{
		"NSE:NIFTY 50": 25210, "NFO:N1C25000": 300, "NFO:N1C25200": 150, "NFO:N1C25400": 60, "NFO:N2C25200": 250,
	}}
	instruments.loadQuotes = func(context.Context) (quoteSource, string, error) { return prices, "AB1234", nil }
	placer := &fakePlacer{}
	app := newKiteApp(context.Background(), manager, "credentials.sqlite3", instruments, load, placer.place)
	app.defaultUser = "cc0006"
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(160, 50)
	app.application.SetScreen(screen)
	done := make(chan error, 1)
	go func() { done <- app.run() }()
	defer func() {
		instruments.stop()
		<-done
	}()
	key := func(k tcell.Key) { app.application.QueueEvent(tcell.NewEventKey(k, 0, tcell.ModNone)) }
	// value reads a field on the event loop; field queues the read.
	value := func(label string) string {
		switch item := app.order.form.GetFormItemByLabel(label).(type) {
		case *tview.InputField:
			return item.GetText()
		case *tview.DropDown:
			_, text := item.GetCurrentOption()
			return text
		}
		return ""
	}
	field := func(label string) string {
		var text string
		app.application.QueueUpdate(func() { text = value(label) })
		return text
	}

	// The default user's portfolio opens at start.
	waitForApp(t, instruments, func() bool {
		return app.screen == screenPortfolio && app.portfolio.user.UserID == "CC0006" && app.portfolio.positions.GetRowCount() == 2
	})

	// O lists the orders for a future: raw buy and sell, closing it, and
	// the underlying's options.
	pressRune(instruments, 'o')
	waitForApp(t, instruments, func() bool { return len(app.menuActions) == 7 })
	key(tcell.KeyEscape)
	waitForApp(t, instruments, func() bool { return app.menuActions == nil && app.application.GetFocus() == app.portfolio.positions })

	// O then X on a short position buys it back at market, its quantity
	// filled in.
	pressRune(instruments, 'o')
	pressRune(instruments, 'x')
	waitForApp(t, instruments, func() bool { return app.order.visible })
	for label, want := range map[string]string{
		orderFieldAccount: "CC0006 · Chandra", orderFieldSide: "BUY", orderFieldQuantity: "150",
		orderFieldType: "MARKET", orderFieldProduct: "NRML",
	} {
		if got := field(label); got != want {
			t.Fatalf("%s = %q, want %q", label, got, want)
		}
	}
	// A rejected order keeps the modal open with Kite's reason.
	placer.fail(errors.New("Insufficient funds"))
	app.application.QueueUpdate(app.order.submit)
	waitForApp(t, instruments, func() bool { return strings.Contains(app.order.summary.GetText(true), "Insufficient funds") })
	placer.fail(nil)
	app.application.QueueUpdate(app.order.submit)
	waitForApp(t, instruments, func() bool { return !app.order.visible && len(placer.placed()) == 1 })
	order := placer.placed()[0]
	want := kite.OrderParams{
		Exchange: "NFO", Tradingsymbol: "NIFTY26OCTFUT", TransactionType: "BUY", OrderType: "MARKET",
		Product: "NRML", Validity: "DAY", Quantity: 150, MarketProtection: -1, Tag: orderTag,
	}
	if order.userID != "CC0006" || order.params != want {
		t.Fatalf("placed %s %+v, want CC0006 %+v", order.userID, order.params, want)
	}
	// The portfolio reloads to show the order's effect.
	waitForApp(t, instruments, func() bool {
		return loadCount() == 2 && strings.Contains(app.portfolio.status.GetText(true), "Order 2510120001 placed")
	})

	// Sell call picks a strike: the nearest expiry opens at the money,
	// arrows change expiry and strike, and Enter orders a lot.
	pressRune(instruments, 'o')
	pressRune(instruments, 'C')
	selectedStrike := func(strike float64) func() bool {
		return func() bool {
			option, ok := app.strikes.selected()
			return app.strikes.visible && ok && option.Strike == strike && app.strikes.quotes[string(option.ID)].LastPrice > 0
		}
	}
	waitForApp(t, instruments, selectedStrike(25200))
	if got := len(app.strikes.expiries); got != 2 {
		t.Fatalf("strike picker expiries = %d, want 2 (puts left out)", got)
	}
	key(tcell.KeyRight)
	waitForApp(t, instruments, func() bool {
		option, ok := app.strikes.selected()
		return ok && option.ID == "NFO:N2C25200"
	})
	key(tcell.KeyLeft)
	waitForApp(t, instruments, selectedStrike(25200))
	key(tcell.KeyDown) // past the spot row
	waitForApp(t, instruments, selectedStrike(25400))
	key(tcell.KeyEnter)
	waitForApp(t, instruments, func() bool { return app.order.visible && !app.strikes.visible })
	if side, quantity, product := field(orderFieldSide), field(orderFieldQuantity), field(orderFieldProduct); side != "SELL" || quantity != "75" || product != "NRML" ||
		app.order.ticket.instrument.ID != "NFO:N1C25400" || app.order.ticket.ltp != 60 {
		t.Fatalf("call order = %s %s %s %+v", side, quantity, product, app.order.ticket)
	}
	key(tcell.KeyEscape)
	waitForApp(t, instruments, func() bool { return !app.order.visible && app.application.GetFocus() == app.portfolio.positions })

	// Raw buy on a holding buys one share for delivery; a bad quantity
	// blocks the order and Escape cancels it.
	key(tcell.KeyTab)
	waitForApp(t, instruments, func() bool { return app.portfolio.focus == 1 })
	pressRune(instruments, 'o')
	pressRune(instruments, 'B')
	waitForApp(t, instruments, func() bool { return app.order.visible })
	if side, quantity, product := field(orderFieldSide), field(orderFieldQuantity), field(orderFieldProduct); side != "BUY" || quantity != "1" || product != "CNC" {
		t.Fatalf("holding buy = %s %s %s", side, quantity, product)
	}
	app.application.QueueUpdate(func() { app.order.input(orderFieldQuantity).SetText("0") })
	waitForApp(t, instruments, func() bool { return strings.Contains(app.order.summary.GetText(true), "above 0") })
	// Arrows move between fields and change options; editing a price keeps
	// the cursor in it.
	focused := func(label string) func() bool {
		return func() bool { return app.application.GetFocus() == app.order.form.GetFormItemByLabel(label) }
	}
	waitForApp(t, instruments, focused(orderFieldQuantity))
	key(tcell.KeyDown)
	waitForApp(t, instruments, focused(orderFieldType))
	key(tcell.KeyRight)
	waitForApp(t, instruments, func() bool { return value(orderFieldType) == kite.OrderTypeLimit })
	key(tcell.KeyDown)
	waitForApp(t, instruments, focused(orderFieldPrice))
	key(tcell.KeyBackspace2)
	key(tcell.KeyBackspace2)
	pressRune(instruments, '5')
	waitForApp(t, instruments, func() bool { return value(orderFieldPrice) == "115" && focused(orderFieldPrice)() })
	key(tcell.KeyDown)
	waitForApp(t, instruments, focused(orderFieldProduct))
	key(tcell.KeyUp)
	key(tcell.KeyUp)
	waitForApp(t, instruments, focused(orderFieldType))
	key(tcell.KeyEscape)
	waitForApp(t, instruments, func() bool { return !app.order.visible && app.application.GetFocus() == app.portfolio.holdings })

	// Close position on a holding sells all of it.
	pressRune(instruments, 'o')
	pressRune(instruments, 'X')
	waitForApp(t, instruments, func() bool { return app.order.visible })
	if side, quantity := field(orderFieldSide), field(orderFieldQuantity); side != "SELL" || quantity != "12" {
		t.Fatalf("holding exit = %s %s", side, quantity)
	}
	key(tcell.KeyEscape)
	waitForApp(t, instruments, func() bool { return !app.order.visible })

	// U switches user.
	pressRune(instruments, 'u')
	waitForApp(t, instruments, func() bool { return app.picking })
	key(tcell.KeyUp)
	key(tcell.KeyEnter)
	waitForApp(t, instruments, func() bool { return !app.picking && app.portfolio.user.UserID == "AB1234" })

	// Raw sell in the explorer sells the selected instrument for the active
	// user.
	pressRune(instruments, 'i')
	waitForApp(t, instruments, func() bool { return app.screen == screenInstruments && instruments.results.GetRowCount() > 1 })
	key(tcell.KeyTab)
	pressRune(instruments, 'o')
	pressRune(instruments, 's')
	waitForApp(t, instruments, func() bool { return app.order.visible })
	if account, side, product := field(orderFieldAccount), field(orderFieldSide), field(orderFieldProduct); account != "AB1234 · Asha" || side != "SELL" || product != "CNC" {
		t.Fatalf("explorer order = %s %s %s", account, side, product)
	}
	key(tcell.KeyEscape)
	waitForApp(t, instruments, func() bool { return !app.order.visible && app.screen == screenInstruments })
	if got := len(placer.placed()); got != 1 {
		t.Fatalf("placed %d orders, want only the one submitted", got)
	}
}

func TestOrderValidation(t *testing.T) {
	application := tview.NewApplication()
	modal := newOrderModal(context.Background(), application, tview.NewPages(), (&fakePlacer{}).place)
	modal.accounts = func() []credentials.Credentials { return []credentials.Credentials{{UserID: "AB1234"}} }
	option := models.Instrument{ID: "NFO:N25100CE", Exchange: "NFO", TradingSymbol: "N25100CE", LotSize: 75, InstrumentType: "CE"}
	if err := modal.show(newOrderTicket(option, kite.TransactionTypeSell, 95.5), "AB1234"); err != nil {
		t.Fatal(err)
	}
	set := func(label, text string) { modal.input(label).SetText(text) }
	setType := func(orderType string) {
		modal.form.GetFormItemByLabel(orderFieldType).(*tview.DropDown).SetCurrentOption(indexOf(orderTypes, orderType))
	}
	check := func(wantErr string) kite.OrderParams {
		t.Helper()
		_, params, err := modal.order()
		if wantErr == "" && err != nil || wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)) {
			t.Fatalf("order() error = %v, want %q", err, wantErr)
		}
		return params
	}

	if params := check(""); params.Quantity != 75 || params.Product != "NRML" || params.MarketProtection != -1 {
		t.Fatalf("default option order = %+v", params)
	}
	set(orderFieldQuantity, "100")
	check("multiple of the lot size, 75")
	set(orderFieldQuantity, "150")
	setType(kite.OrderTypeLimit)
	if params := check(""); params.Price != 95.5 || params.MarketProtection != 0 {
		t.Fatalf("limit defaults to the last price: %+v", params)
	}
	if !strings.Contains(modal.summary.GetText(true), "SELL 150 (2 lots) limit 95.50") {
		t.Fatalf("summary = %q", modal.summary.GetText(true))
	}
	setType(kite.OrderTypeSL)
	check("trigger price")
	set(orderFieldTrigger, "96")
	if params := check(""); params.TriggerPrice != 96 || params.Price != 95.5 {
		t.Fatalf("stop-loss order = %+v", params)
	}
	setType(kite.OrderTypeSLM)
	if params := check(""); params.Price != 0 || params.TriggerPrice != 96 || params.MarketProtection != -1 {
		t.Fatalf("stop-loss market order = %+v", params)
	}
	if err := modal.show(newOrderTicket(option, kite.TransactionTypeBuy, 0), "AB1234"); err == nil {
		t.Fatal("opened a second order over the first")
	}
}
