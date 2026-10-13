package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/nse"
)

// harness drives the app as Bubble Tea would: commands run in goroutines
// and their messages go back through Update on the test goroutine.
type harness struct {
	t    *testing.T
	a    *app
	msgs chan tea.Msg
	done chan struct{}
}

func newHarness(t *testing.T, deps appDeps) *harness {
	t.Helper()
	h := &harness{t: t, msgs: make(chan tea.Msg, 256), done: make(chan struct{})}
	h.a = newApp(context.Background(), deps)
	h.a.send = h.post
	t.Cleanup(func() {
		close(h.done)
		if err := h.a.shutdown(); err != nil {
			t.Error(err)
		}
	})
	h.dispatch(tea.WindowSizeMsg{Width: 160, Height: 50})
	h.run(h.a.Init())
	return h
}

func (h *harness) post(msg tea.Msg) {
	select {
	case h.msgs <- msg:
	case <-h.done:
	}
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			h.post(msg)
		}
	}()
}

func (h *harness) dispatch(msg tea.Msg) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			h.run(cmd)
		}
		return
	}
	_, cmd := h.a.Update(msg)
	h.run(cmd)
}

// waitFor processes messages until ready holds.
func (h *harness) waitFor(what string, ready func() bool) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for !ready() {
		select {
		case msg := <-h.msgs:
			h.dispatch(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; screen:\n%s", what, h.screen())
		}
	}
}

// settle processes messages for a moment, for checks that nothing happens.
func (h *harness) settle() {
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case msg := <-h.msgs:
			h.dispatch(msg)
		case <-deadline:
			return
		}
	}
}

var namedKeys = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "backspace": tea.KeyBackspace,
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "delete": tea.KeyDelete,
}

// press sends keys: names such as "enter", or single characters.
func (h *harness) press(keys ...string) {
	for _, key := range keys {
		if code, ok := namedKeys[key]; ok {
			h.dispatch(tea.KeyPressMsg{Code: code})
			continue
		}
		h.typeText(key)
	}
}

func (h *harness) typeText(text string) {
	for _, r := range text {
		h.dispatch(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (h *harness) screen() string {
	return ansi.Strip(h.a.render())
}

func (h *harness) shows(text string) func() bool {
	return func() bool { return strings.Contains(h.screen(), text) }
}

func (h *harness) idle() bool { return h.a.overlay == nil }

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
	// orderID is the order modified or cancelled; cancelled marks a
	// cancellation.
	orderID   string
	cancelled bool
}

func (f *fakePlacer) modify(_ context.Context, userID string, order kite.Order, params kite.OrderParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.orders = append(f.orders, placedOrder{userID: userID, params: params, orderID: order.OrderID})
	return nil
}

func (f *fakePlacer) cancel(_ context.Context, userID string, order kite.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.orders = append(f.orders, placedOrder{userID: userID, orderID: order.OrderID, cancelled: true})
	return nil
}

func (f *fakePlacer) place(_ context.Context, userID string, params kite.OrderParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.orders = append(f.orders, placedOrder{userID: userID, params: params})
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

type fakeQuotes struct {
	mu       sync.Mutex
	prices   map[string]float64
	requests int
	margins  []string
}

func (f *fakeQuotes) GetQuote(instruments ...string) (kite.Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
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

type fakeListStore struct {
	mu    sync.Mutex
	lists []models.List
}

func (f *fakeListStore) byName(name string) models.List {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range f.lists {
		if list.Name == name {
			return list
		}
	}
	return models.List{}
}

func (f *fakeListStore) GetLists(context.Context, string) ([]models.List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.List(nil), f.lists...), nil
}

func (f *fakeListStore) CreateList(_ context.Context, list models.List) (models.List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list.ID = fmt.Sprintf("list-%d", len(f.lists)+1)
	list.Number = len(f.lists) + 1
	list.UpdatedAt = time.Now()
	f.lists = append(f.lists, list)
	return list, nil
}

func (f *fakeListStore) ReplaceAutoGeneratedList(ctx context.Context, list models.List) (models.List, error) {
	f.mu.Lock()
	for i, existing := range f.lists {
		if existing.Name == list.Name && existing.CreatedBy == list.CreatedBy {
			list.ID, list.Number, list.AutoGenerated = existing.ID, existing.Number, true
			f.lists[i] = list
			f.mu.Unlock()
			return list, nil
		}
	}
	f.mu.Unlock()
	list.AutoGenerated = true
	return f.CreateList(ctx, list)
}

func (f *fakeListStore) RenameList(_ context.Context, id, name string) error {
	return f.update(id, func(list *models.List) { list.Name = name })
}

func (f *fakeListStore) DeleteList(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, list := range f.lists {
		if list.ID == id {
			f.lists = append(f.lists[:i], f.lists[i+1:]...)
			return nil
		}
	}
	return models.ErrNotFound
}

func (f *fakeListStore) AddListItems(_ context.Context, id string, ids []models.InstrumentID) error {
	return f.update(id, func(list *models.List) {
		for _, instrumentID := range ids {
			list.Items = append(list.Items, models.ListItem{InstrumentID: instrumentID, CreatedAt: time.Now()})
		}
	})
}

func (f *fakeListStore) RemoveListItems(_ context.Context, id string, ids []models.InstrumentID) error {
	return f.update(id, func(list *models.List) {
		kept := list.Items[:0]
		for _, item := range list.Items {
			remove := false
			for _, instrumentID := range ids {
				remove = remove || item.InstrumentID == instrumentID
			}
			if !remove {
				kept = append(kept, item)
			}
		}
		list.Items = kept
	})
}

func (f *fakeListStore) update(id string, change func(*models.List)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.lists {
		if f.lists[i].ID == id {
			list := f.lists[i]
			list.Items = append([]models.ListItem(nil), list.Items...)
			change(&list)
			f.lists[i] = list
			return nil
		}
	}
	return models.ErrNotFound
}

type fakeIndexSource struct {
	metadata     []nse.NSEIndexMetadata
	constituents map[string][]models.InstrumentID
}

func (f *fakeIndexSource) ListIndices(context.Context) ([]nse.NSEIndexMetadata, error) {
	return f.metadata, nil
}

func (f *fakeIndexSource) GetIndex(_ context.Context, metadata nse.NSEIndexMetadata) (models.List, error) {
	ids, ok := f.constituents[metadata.IndexPageURL]
	if !ok {
		return models.List{}, models.ErrNotFound
	}
	list := models.List{Name: metadata.ListName, IsIndex: true, AutoGenerated: true, URL: metadata.IndexPageURL}
	for _, id := range ids {
		list.Items = append(list.Items, models.ListItem{InstrumentID: id})
	}
	return list, nil
}

// niftyMarket is a NIFTY underlying with a future and calls and puts at
// three strikes for each of two expiries, and RELIANCE, priced with NIFTY
// at 25,040.
func niftyMarket() (*fakeCatalog, *fakeQuotes) {
	nifty := models.InstrumentID("NSE:NIFTY 50")
	near, far := time.Now().AddDate(0, 0, 7), time.Now().AddDate(0, 0, 35)
	store := &fakeCatalog{
		underlyings: []models.Instrument{
			{ID: nifty, Exchange: "NSE", Segment: "INDICES", TradingSymbol: "NIFTY 50", DisplayName: "NIFTY 50", InstrumentType: "EQ", OptionsCount: 12, FuturesCount: 2},
			{ID: "NSE:RELIANCE", Exchange: "NSE", TradingSymbol: "RELIANCE", DisplayName: "RELIANCE", InstrumentType: "EQ", LotSize: 1, FuturesCount: 1},
		},
		searchResults: []models.Instrument{{ID: "NSE:RELIANCE", Exchange: "NSE", TradingSymbol: "RELIANCE", DisplayName: "RELIANCE", InstrumentType: "EQ", LotSize: 1}},
	}
	prices := map[string]float64{string(nifty): 25040, "NSE:RELIANCE": 1400}
	for number, expiry := range []time.Time{near, far} {
		suffix := fmt.Sprintf("W%d", number+1)
		fut := future(models.InstrumentID("NFO:NIFTY"+suffix+"FUT"), number+1, expiry)
		fut.Exchange, fut.LotSize, fut.UnderlyingID = "NFO", 75, &nifty
		store.futures = append(store.futures, fut)
		for _, strike := range []float64{24900, 25000, 25100} {
			for _, optionType := range []models.OptionType{models.OptionTypeCall, models.OptionTypePut} {
				id := models.InstrumentID(fmt.Sprintf("NFO:NIFTY%s%.0f%s", suffix, strike, optionType))
				contract := option(id, optionType, strike, number+1, expiry)
				contract.Exchange, contract.LotSize, contract.UnderlyingID = "NFO", 75, &nifty
				contract.TradingSymbol = strings.TrimPrefix(string(id), "NFO:")
				store.options = append(store.options, contract)
				prices[string(id)] = 100 + (strike-24900)/10
			}
		}
	}
	return store, &fakeQuotes{prices: prices}
}

func explorerDeps(t *testing.T, store *fakeCatalog, quotes *fakeQuotes, lists listStore, placer *fakePlacer) appDeps {
	deps := appDeps{
		manager:     mustManager(t),
		openCatalog: func(context.Context) (catalog, listStore, error) { return store, lists, nil },
		place:       placer.place,
		modify:      placer.modify,
		cancel:      placer.cancel,
		strikeEvery: -1,
		loadPortfolio: func(context.Context, credentials.Credentials) portfolio {
			return portfolio{loadedAt: time.Now(), loggedIn: true}
		},
	}
	if quotes != nil {
		deps.loadQuotes = func(context.Context) (quoteSource, string, error) { return quotes, "TEST", nil }
	}
	return deps
}

func openExplorer(h *harness) {
	h.t.Helper()
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("i")
	h.waitFor("the catalog", func() bool { return h.a.explorer.catalog != nil && h.idle() })
	h.waitFor("underlyings", h.shows("NIFTY 50"))
}

func TestAppMovesBetweenUsersPortfolioAndInstruments(t *testing.T) {
	nifty := models.InstrumentID("NSE:NIFTY 50")
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, nil, &fakePlacer{})
	var loadedMu sync.Mutex
	var loaded []string
	deps.loadPortfolio = func(_ context.Context, user credentials.Credentials) portfolio {
		loadedMu.Lock()
		loaded = append(loaded, user.UserID)
		loadedMu.Unlock()
		return portfolio{
			positions: []kite.Position{
				{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", Product: "NRML", Quantity: 75, PnL: -1500},
				{Tradingsymbol: "NIFTY26OCT25000PE", Exchange: "NFO", Product: "NRML", Quantity: 75, PnL: 500},
				{Tradingsymbol: "INFY", Exchange: "NSE", Product: "CNC", Quantity: 10, PnL: 20},
			},
			holdings: kite.Holdings{{Tradingsymbol: "RELIANCE", Exchange: "NSE", Quantity: 10, AveragePrice: 1000, LastPrice: 1100}},
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
	h := newHarness(t, deps)

	// The users screen comes first; the catalog stays closed until I.
	h.waitFor("users", func() bool { return h.a.screen == screenUsers && len(h.a.users.users) == 1 })
	h.waitFor("the user row", h.shows("Asha"))
	if h.a.explorer.started {
		t.Fatal("the explorer started before it was opened")
	}

	h.press("enter")
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	h.waitFor("positions by display name", h.shows("NIFTY OCT FUT"))
	if !strings.Contains(h.screen(), "funds unavailable") {
		t.Errorf("the failed section is not reported:\n%s", h.screen())
	}

	// O on a position offers to close it.
	h.press("o")
	m, ok := h.a.overlay.(*menu)
	if !ok {
		t.Fatalf("O opened %T", h.a.overlay)
	}
	var labels []string
	for _, item := range m.items {
		labels = append(labels, ansi.Strip(item.label))
	}
	if joined := strings.Join(labels, "|"); !strings.Contains(joined, "Close position") || !strings.Contains(joined, "Raw buy") {
		t.Errorf("menu = %q", joined)
	}
	h.press("esc")

	h.press("i")
	h.waitFor("the explorer", func() bool { return h.a.screen == screenInstruments && h.a.explorer.catalog != nil && h.idle() })
	h.waitFor("underlyings", h.shows("RELIANCE"))
	h.press("q")
	if h.a.screen != screenPortfolio {
		t.Fatalf("Q went to screen %d, want the portfolio", h.a.screen)
	}
	h.press("q")
	if h.a.screen != screenUsers {
		t.Fatalf("Q went to screen %d, want users", h.a.screen)
	}
	loadedMu.Lock()
	defer loadedMu.Unlock()
	if len(loaded) != 1 || loaded[0] != "AB1234" {
		t.Errorf("loaded portfolios = %v", loaded)
	}
}

func TestDefaultUserOpensPortfolio(t *testing.T) {
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, nil, &fakePlacer{})
	deps.defaultUser = "ab1234"
	h := newHarness(t, deps)
	h.waitFor("the default portfolio", func() bool { return h.a.screen == screenPortfolio && h.a.portfolio.user.UserID == "AB1234" })
}

func TestExplorerSearchChainAndPrices(t *testing.T) {
	store, quotes := niftyMarket()
	h := newHarness(t, explorerDeps(t, store, quotes, nil, &fakePlacer{}))
	openExplorer(h)
	e := h.a.explorer
	h.waitFor("prices", h.shows("25040.00"))

	// Typing searches after the debounce.
	h.press("/")
	h.typeText("REL")
	if !e.searchFocused {
		t.Fatal("/ did not focus the search")
	}
	h.waitFor("search results", func() bool { return !e.underlyings && len(e.resultItems) == 1 })
	h.press("esc", "u")
	h.waitFor("underlyings again", func() bool { return e.underlyings && len(e.resultItems) == 2 })

	// Enter opens NIFTY's chain with the at-the-money strike selected.
	h.press("enter")
	h.waitFor("the chain", func() bool { return e.mode == viewChain && h.idle() })
	strikeAt := func() string {
		selected, _ := e.chainGrid.selected()
		return selected.cells[3].text
	}
	h.waitFor("the ATM strike", func() bool { return strikeAt() == formatStrike(25000) })
	if e.chainGrid.group != 0 {
		t.Errorf("selected side = %d, want calls", e.chainGrid.group)
	}
	if !strings.Contains(h.screen(), "CALL BE%") {
		t.Errorf("breakevens are not percentages by default")
	}

	// The status bar shows the margin per lot once fetched.
	h.waitFor("the margin", func() bool { return strings.Contains(ansi.Strip(e.status), "1,50,000") })
	if requests := quotes.marginRequests(); len(requests) == 0 || !strings.Contains(requests[0], "25000CE") {
		t.Errorf("margin requests = %v", requests)
	}

	// [ and ] cycle expiries and wrap.
	h.press("]")
	if e.chain.filter.expiryNumber != 2 {
		t.Fatalf("] showed expiry %d", e.chain.filter.expiryNumber)
	}
	h.press("]")
	if e.chain.filter.expiryNumber != 1 {
		t.Fatalf("] did not wrap: expiry %d", e.chain.filter.expiryNumber)
	}
	h.press("[")
	if e.chain.filter.expiryNumber != 2 {
		t.Fatalf("[ did not wrap: expiry %d", e.chain.filter.expiryNumber)
	}

	h.press("%")
	if e.breakevenPercent || !strings.Contains(h.screen(), "CALL BE ") {
		t.Errorf("%% did not switch breakevens to prices")
	}

	// Filters narrow the chain to puts.
	h.press("f", "right", "right", "tab", "tab", "tab", "enter")
	if _, open := h.a.overlay.(*filterOverlay); open {
		t.Fatalf("filters did not apply:\n%s", h.screen())
	}
	if e.chain.filter.side != optionSidePuts {
		t.Errorf("side = %v, want puts", e.chain.filter.side)
	}

	// The expiry picker picks the first expiry.
	h.press("e", "up", "enter")
	if e.chain.filter.expiryNumber != 1 {
		t.Errorf("expiry picker chose %d", e.chain.filter.expiryNumber)
	}

	h.press("q")
	if e.mode != viewDashboard || !e.underlyings {
		t.Errorf("Q returned to mode %d", e.mode)
	}
}

func TestListsAndNSEMasterList(t *testing.T) {
	store, _ := niftyMarket()
	lists := &fakeListStore{}
	deps := explorerDeps(t, store, nil, lists, &fakePlacer{})
	deps.indices = &fakeIndexSource{
		metadata:     []nse.NSEIndexMetadata{{IndexName: "Nifty 50", ListName: "NIFTY50", IndexPageURL: "nifty50"}},
		constituents: map[string][]models.InstrumentID{"nifty50": {"NSE:RELIANCE", "NSE:INFY"}},
	}
	h := newHarness(t, deps)
	openExplorer(h)
	e := h.a.explorer

	h.press("l")
	h.waitFor("lists", func() bool { return e.mode == viewLists && h.idle() })
	h.press("n")
	h.typeText("Watch")
	h.press("enter")
	h.waitFor("the new list", func() bool { return len(e.listData) == 1 && h.idle() })

	// A adds the selected instrument to a list from the picker.
	h.press("q")
	h.waitFor("the dashboard", func() bool { return e.mode == viewDashboard })
	h.press("down", "a")
	h.waitFor("the list picker", func() bool { _, ok := h.a.overlay.(*menu); return ok })
	h.press("down", "enter")
	h.waitFor("the item", func() bool { return len(lists.byName("Watch").Items) == 1 && h.idle() })
	if got := lists.byName("Watch").Items[0].InstrumentID; got != "NSE:RELIANCE" {
		t.Errorf("added %s", got)
	}
	if !strings.Contains(ansi.Strip(e.status), "Added NSE:RELIANCE to Watch") {
		t.Errorf("status = %q", ansi.Strip(e.status))
	}

	// M lists the indices; Enter downloads one, A every one.
	h.press("l")
	h.waitFor("lists", func() bool { return e.mode == viewLists && h.idle() })
	h.waitFor("items", h.shows("NSE:RELIANCE"))
	h.press("m")
	h.waitFor("the master list", func() bool { _, ok := h.a.overlay.(*masterOverlay); return ok })
	h.press("down", "enter")
	h.waitFor("the download", func() bool { return len(lists.byName("NIFTY50").Items) == 2 })
	h.waitFor("the master list again", func() bool { _, ok := h.a.overlay.(*masterOverlay); return ok })
	h.press("a")
	h.waitFor("the FO list", func() bool { return len(lists.byName("FO_LIST").Items) == 2 })
	h.waitFor("the master list again", func() bool { _, ok := h.a.overlay.(*masterOverlay); return ok })
	h.press("esc")

	// R refreshes the selected index list.
	h.waitFor("lists", func() bool { return len(e.listData) == 3 })
	e.listsGrid.selectWhere(func(r row) bool { return r.ref.(models.List).Name == "NIFTY50" })
	h.press("r")
	h.waitFor("the refresh", func() bool { return h.idle() && strings.Contains(ansi.Strip(e.status), "Saved 1") })

	// D deletes the selected list after confirmation.
	h.press("d", "left", "enter")
	h.waitFor("the deletion", func() bool { return len(e.listData) == 2 && h.idle() })
}

func TestOrderMenuStrikePickerAndForm(t *testing.T) {
	store, quotes := niftyMarket()
	placer := &fakePlacer{}
	h := newHarness(t, explorerDeps(t, store, quotes, nil, placer))
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))

	// NIFTY is an index: options only, no raw orders.
	h.press("o")
	m, ok := h.a.overlay.(*menu)
	if !ok {
		t.Fatalf("O opened %T", h.a.overlay)
	}
	if len(m.items) != 4 {
		t.Fatalf("index menu has %d items, want the four option orders", len(m.items))
	}

	// c buys a call from the strike picker, at the money by default.
	h.press("c")
	picker, ok := h.a.overlay.(*strikePicker)
	if !ok {
		t.Fatalf("c opened %T", h.a.overlay)
	}
	h.waitFor("priced strikes", func() bool {
		selected, ok := picker.selected()
		return ok && selected.Strike == 25000 && picker.spot() > 0
	})
	if !strings.Contains(h.screen(), "◀ spot") {
		t.Errorf("no spot row:\n%s", h.screen())
	}
	h.press("right")
	h.waitFor("the next expiry", func() bool {
		selected, ok := picker.selected()
		return picker.expiry == 1 && ok && selected.Strike == 25000
	})
	h.press("left", "enter")
	form, ok := h.a.overlay.(*orderForm)
	if !ok {
		t.Fatalf("Enter opened %T", h.a.overlay)
	}
	if form.ticket.instrument.TradingSymbol != "NIFTYW125000CE" || form.ticket.side != kite.TransactionTypeBuy {
		t.Fatalf("ticket = %+v", form.ticket)
	}

	// A limit order: backspace edits the price.
	h.press("down", "right", "down")
	if current := form.form.current(); current == nil || current.label != orderFieldPrice {
		t.Fatalf("focus is not on the price")
	}
	h.press("backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	h.typeText("12.5")
	if got := form.form.field(orderFieldPrice).value(); got != "12.5" {
		t.Fatalf("price = %q", got)
	}
	h.press("down", "down", "enter")
	h.waitFor("the order", func() bool { return len(placer.placed()) == 1 && h.idle() })
	order := placer.placed()[0]
	if order.userID != "AB1234" || order.params.OrderType != kite.OrderTypeLimit || order.params.Price != 12.5 ||
		order.params.Quantity != 75 || order.params.Tradingsymbol != "NIFTYW125000CE" || order.params.Product != kite.ProductNRML {
		t.Errorf("placed %+v", order)
	}

	// A rejected order keeps the form open with the reason.
	placer.fail(errors.New("insufficient funds"))
	h.press("down", "o", "s")
	form, ok = h.a.overlay.(*orderForm)
	if !ok {
		t.Fatalf("s opened %T", h.a.overlay)
	}
	if form.ticket.side != kite.TransactionTypeSell || form.ticket.instrument.ID != "NSE:RELIANCE" || form.ticket.product != kite.ProductCNC {
		t.Fatalf("ticket = %+v", form.ticket)
	}
	h.press("down", "down", "down", "enter")
	h.waitFor("the rejection", func() bool { return strings.Contains(ansi.Strip(form.summary), "insufficient funds") })
	if h.a.overlay != form {
		t.Fatal("the rejected order closed the form")
	}
	h.press("esc")
	if !h.idle() {
		t.Fatal("Esc did not close the form")
	}
}

func TestOrderValidation(t *testing.T) {
	future := models.Instrument{Exchange: "NFO", TradingSymbol: "NIFTYFUT", InstrumentType: "FUT", LotSize: 75}
	cases := []struct {
		input orderInput
		want  string
	}{
		{orderInput{side: "BUY", orderType: "MARKET", product: "NRML", quantity: "0"}, "whole number above 0"},
		{orderInput{side: "BUY", orderType: "MARKET", product: "NRML", quantity: "70"}, "multiple of the lot size, 75"},
		{orderInput{side: "BUY", orderType: "LIMIT", product: "NRML", quantity: "75"}, "price above 0"},
		{orderInput{side: "BUY", orderType: "SL-M", product: "NRML", quantity: "75"}, "trigger price above 0"},
	}
	for _, c := range cases {
		if _, err := orderParams(future, c.input); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.input, err, c.want)
		}
	}
	params, err := orderParams(future, orderInput{side: "SELL", orderType: "SL", product: "MIS", quantity: "150", price: "99", trigger: "100"})
	if err != nil || params.Quantity != 150 || params.Price != 99 || params.TriggerPrice != 100 || params.Tag != orderTag {
		t.Errorf("params = %+v, err = %v", params, err)
	}
}

func TestRendersInSmallTerminals(t *testing.T) {
	store, quotes := niftyMarket()
	h := newHarness(t, explorerDeps(t, store, quotes, &fakeListStore{}, &fakePlacer{}))
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))
	h.press("enter")
	h.waitFor("the chain", func() bool { return h.a.explorer.mode == viewChain && h.idle() })
	h.press("o", "p")
	h.waitFor("strikes", func() bool { p, ok := h.a.overlay.(*strikePicker); return ok && !p.loading })
	for _, size := range [][2]int{{160, 50}, {100, 30}, {80, 24}, {60, 16}, {30, 10}} {
		h.dispatch(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, screen := range []screenID{screenUsers, screenPortfolio, screenInstruments} {
			h.a.screen = screen
			if lines := strings.Split(h.screen(), "\n"); len(lines) > size[1] {
				t.Errorf("%dx%d screen %d drew %d lines", size[0], size[1], screen, len(lines))
			}
		}
	}
}

func TestPortfolioOrdersAndRawPayload(t *testing.T) {
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, nil, &fakePlacer{})
	morning := time.Date(2026, 10, 13, 9, 20, 0, 0, time.Local)
	deps.loadPortfolio = func(context.Context, credentials.Credentials) portfolio {
		return portfolio{
			orders: kite.Orders{
				{OrderID: "1001", ExchangeOrderID: "EX1001", Status: "COMPLETE", Exchange: "NSE", TradingSymbol: "INFY", TransactionType: "BUY", OrderType: "MARKET", Product: "CNC", Quantity: 10, FilledQuantity: 10, AveragePrice: 1500, OrderTimestamp: models.Time{Time: morning}},
				{OrderID: "1002", Status: "REJECTED", StatusMessage: "Insufficient funds", Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000PE", TransactionType: "SELL", OrderType: "LIMIT", Product: "NRML", Quantity: 75, Price: 120, OrderTimestamp: models.Time{Time: morning.Add(time.Hour)}, Tags: []string{"kite-cli"}},
			},
			loadedAt: time.Now(),
			loggedIn: true,
		}
	}
	deps.defaultUser = "ab1234"
	h := newHarness(t, deps)
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	h.waitFor("orders", h.shows("REJECTED"))
	if screen := h.screen(); !strings.Contains(screen, "10/10") || !strings.Contains(screen, "0/75") || !strings.Contains(screen, "2 today") {
		t.Fatalf("order summaries missing:\n%s", screen)
	}

	// Down past each table's last row (the empty tables have none) moves
	// into the next, down to the orders at the bottom; Up at the first
	// order goes back.
	p := h.a.portfolio
	h.press("down", "down", "down")
	if p.focused() != &p.orders || p.orders.cursor != 0 {
		t.Fatalf("Down reached table %d row %d, want the first order", p.focus, p.orders.cursor)
	}
	h.press("up")
	if p.focused() != &p.funds {
		t.Fatalf("Up at the first order reached table %d, want funds", p.focus)
	}
	h.press("down", "down")
	if p.focused() != &p.orders || p.orders.cursor != 1 {
		t.Fatalf("Down stopped at table %d row %d, want the second order", p.focus, p.orders.cursor)
	}
	h.press("down")
	if p.focused() != &p.orders || p.orders.cursor != 1 {
		t.Fatalf("Down past the last table moved to table %d row %d", p.focus, p.orders.cursor)
	}
	h.press("up")

	// The details pane summarises the newest order.
	h.waitFor("the order detail", h.shows("Insufficient funds"))

	// Enter swaps the details for the raw payload, keys in Kite's order.
	h.press("enter")
	h.waitFor("the raw payload", h.shows("Raw order"))
	screen := h.screen()
	for _, want := range []string{"order_id", "1002", "transaction_type", "kite-cli"} {
		if !strings.Contains(screen, want) {
			t.Errorf("raw payload lacks %q:\n%s", want, screen)
		}
	}
	h.press("J")
	if h.a.portfolio.rawOffset != 1 {
		t.Errorf("J scrolled to %d", h.a.portfolio.rawOffset)
	}
	h.press("esc")
	if h.a.portfolio.raw || h.a.screen != screenPortfolio {
		t.Fatalf("Esc left raw %v on screen %d", h.a.portfolio.raw, h.a.screen)
	}

	// Narrow screens hide the details pane and show the payload in a dialog.
	h.dispatch(tea.WindowSizeMsg{Width: 90, Height: 30})
	h.press("enter")
	if _, ok := h.a.overlay.(*payloadOverlay); !ok {
		t.Fatalf("Enter opened %T on a narrow screen", h.a.overlay)
	}
	h.waitFor("the payload dialog", h.shows("Order 1002"))
	h.press("esc")
	if h.a.overlay != nil {
		t.Errorf("Esc left %T open", h.a.overlay)
	}
}

func TestMarginModifyAndCancelOrders(t *testing.T) {
	store, _ := niftyMarket()
	placer := &fakePlacer{}
	deps := explorerDeps(t, store, nil, nil, placer)
	contract := models.InstrumentID("NFO:NIFTY26OCT25000PE")
	open := kite.Order{OrderID: "2001", Variety: "regular", Status: "OPEN", Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000PE", TransactionType: "BUY",
		OrderType: "LIMIT", Product: "NRML", Validity: "DAY", Quantity: 75, PendingQuantity: 75, Price: 110.5, OrderTimestamp: models.Time{Time: time.Now()}}
	done := kite.Order{OrderID: "2002", Status: "COMPLETE", Exchange: "NSE", TradingSymbol: "INFY", TransactionType: "BUY", OrderType: "MARKET", Product: "CNC", Quantity: 1, FilledQuantity: 1}
	deps.loadPortfolio = func(context.Context, credentials.Credentials) portfolio {
		return portfolio{
			orders:  kite.Orders{open, done},
			margins: kite.AllMargins{Equity: kite.Margins{Net: 500000, Used: kite.UsedMargins{Debits: 140000}}},
			instruments: map[models.InstrumentID]models.Instrument{
				contract: {ID: contract, Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000PE", LotSize: 75, TickSize: 0.05, InstrumentType: "PE"},
			},
			loadedAt: time.Now(),
			loggedIn: true,
		}
	}
	deps.defaultUser = "ab1234"
	h := newHarness(t, deps)
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	h.waitFor("the margin card", h.shows("₹1.4L/6.4L"))
	if !strings.Contains(h.screen(), "₹5L free") {
		t.Errorf("free margin missing:\n%s", h.screen())
	}
	for h.a.portfolio.focused() != &h.a.portfolio.orders {
		h.press("tab")
	}

	// M opens the form on the open order, its account, side and product
	// fixed; + adds a lot and Right adds a tick to the price.
	h.press("m")
	o, ok := h.a.overlay.(*orderForm)
	if !ok {
		t.Fatalf("M opened %T", h.a.overlay)
	}
	if !o.form.field(orderFieldSide).disabled || !o.form.field(orderFieldProduct).disabled {
		t.Error("modify left the side or product editable")
	}
	h.press("+", "+", "-")
	if got := o.form.field(orderFieldQuantity).value(); got != "150" {
		t.Errorf("quantity after + + - = %s, want 150", got)
	}
	h.press("down", "down")
	if got := o.form.current().label; got != orderFieldPrice {
		t.Fatalf("focused %s, want the price", got)
	}
	h.press("right", "right", "left", "right")
	if got := o.form.field(orderFieldPrice).value(); got != "110.60" {
		t.Errorf("price after ticks = %s, want 110.60", got)
	}
	h.press("down", "enter")
	h.waitFor("the modification", func() bool { return len(placer.placed()) == 1 && h.a.overlay == nil })
	got := placer.placed()[0]
	if got.orderID != "2001" || got.userID != "AB1234" || got.params.Quantity != 150 || got.params.Price != 110.6 || got.params.OrderType != "LIMIT" || got.params.Tradingsymbol != "" {
		t.Errorf("modified %+v", got)
	}
	h.waitFor("the reload", func() bool { return !h.a.portfolio.loading })

	// X asks first; Enter on the default keeps the order, Y cancels it.
	h.a.portfolio.orders.selectWhere(func(r row) bool { order, ok := r.ref.(kite.Order); return ok && order.OrderID == "2001" })
	h.press("x")
	h.waitFor("the confirmation", h.shows("Cancel order 2001"))
	h.press("enter")
	if len(placer.placed()) != 1 {
		t.Fatal("Enter on the confirmation cancelled the order")
	}
	h.press("x", "y")
	h.waitFor("the cancellation", func() bool { return len(placer.placed()) == 2 })
	if got := placer.placed()[1]; !got.cancelled || got.orderID != "2001" {
		t.Errorf("cancelled %+v", got)
	}
	h.waitFor("the reload", func() bool { return !h.a.portfolio.loading })

	// A completed order cannot be changed.
	h.a.portfolio.orders.selectWhere(func(r row) bool { order, ok := r.ref.(kite.Order); return ok && order.OrderID == "2002" })
	h.press("x")
	if h.a.overlay != nil {
		t.Fatalf("X on a complete order opened %T", h.a.overlay)
	}
	h.waitFor("the warning", h.shows("only open orders"))
}

func TestOrderFormSteppers(t *testing.T) {
	if got := formatTicked(stepPrice(0, 1500, 0.05, 1), 0.05); got != "1500.05" {
		t.Errorf("first tick up from LTP = %s", got)
	}
	if got := formatTicked(stepPrice(0.05, 0, 0.05, -1), 0.05); got != "0.05" {
		t.Errorf("tick below the minimum = %s", got)
	}
	if got := formatTicked(stepPrice(100, 0, 1, 1), 1); got != "101.00" {
		t.Errorf("whole tick = %s", got)
	}
	for value, want := range map[float64]string{999: "999", 1400: "1.4K", 140000: "1.4L", 640000: "6.4L", 10500000: "1.05Cr", 20000000: "2Cr", -150000: "-1.5L"} {
		if got := formatShort(value); got != want {
			t.Errorf("formatShort(%v) = %s, want %s", value, got, want)
		}
	}
}
