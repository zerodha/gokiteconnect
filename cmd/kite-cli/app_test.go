package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/devshoe/gokiteconnect/config"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	marketdata "github.com/devshoe/gokiteconnect/market-data-service"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
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
	mode   broker.Mode
	params kite.OrderParams
	// orderID is the order modified or cancelled; cancelled marks a
	// cancellation.
	orderID   string
	cancelled bool
}

func (f *fakePlacer) modify(_ context.Context, userID string, mode broker.Mode, order kite.Order, params kite.OrderParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.orders = append(f.orders, placedOrder{userID: userID, mode: mode, params: params, orderID: order.OrderID})
	return nil
}

func (f *fakePlacer) cancel(_ context.Context, userID string, mode broker.Mode, order kite.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.orders = append(f.orders, placedOrder{userID: userID, mode: mode, orderID: order.OrderID, cancelled: true})
	return nil
}

func (f *fakePlacer) place(_ context.Context, userID string, mode broker.Mode, params kite.OrderParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.orders = append(f.orders, placedOrder{userID: userID, mode: mode, params: params})
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
			// Tradable instruments have five levels a tick apart; indices
			// have no depth.
			if !strings.Contains(id, "NIFTY 50") {
				for level := range quote.Depth.Buy {
					quote.Depth.Buy[level] = models.DepthItem{Price: price - 0.05*float64(level+1), Quantity: uint32(100 * (level + 1)), Orders: uint32(level + 1)}
					quote.Depth.Sell[level] = models.DepthItem{Price: price + 0.05*float64(level+1), Quantity: uint32(50 * (level + 1)), Orders: uint32(level + 2)}
				}
				quote.BuyQuantity, quote.SellQuantity = 3000, 1000
			}
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

// GetLists filters by creator and gives smart lists their parent's items,
// as the SQLite store does.
func (f *fakeListStore) GetLists(_ context.Context, createdBy string) ([]models.List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var lists []models.List
	for _, list := range f.lists {
		if createdBy != "" && list.CreatedBy != createdBy {
			continue
		}
		for _, parent := range f.lists {
			if list.IsSmart() && parent.ID == list.ParentID {
				list.Items = parent.Items
			}
		}
		lists = append(lists, list)
	}
	return lists, nil
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
		if f.lists[i].ID == id && f.lists[i].IsSmart() {
			return models.ErrSmartList
		}
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
		manager:      mustManager(t),
		openCatalog:  func(context.Context) (catalog, listStore, error) { return store, lists, nil },
		place:        placer.place,
		settingsPath: filepath.Join(t.TempDir(), "settings.json"),
		modify:       placer.modify,
		cancel:       placer.cancel,
		strikeEvery:  -1,
		loadPortfolio: func(context.Context, credentials.Credentials, broker.Mode) portfolio {
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
	deps.loadPortfolio = func(_ context.Context, user credentials.Credentials, _ broker.Mode) portfolio {
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

	// + on a position offers to close it.
	h.press("+")
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
	// Q no longer goes back; Esc does.
	h.press("q")
	if h.a.screen != screenInstruments {
		t.Fatalf("Q left the explorer for screen %d", h.a.screen)
	}
	h.press("esc")
	if h.a.screen != screenPortfolio {
		t.Fatalf("Esc went to screen %d, want the portfolio", h.a.screen)
	}
	h.press("esc")
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
	if screen := h.screen(); !strings.Contains(screen, "EXCH") || !regexp.MustCompile(`NSE +│ NIFTY 50 +│ +25040\.00 │ +\+2\.00% `).MatchString(ansi.Strip(screen)) {
		t.Fatalf("results do not show the exchange:\n%s", screen)
	}

	// Typing searches after the debounce.
	h.press("/")
	h.typeText("REL")
	if !e.searchFocused {
		t.Fatal("/ did not focus the search")
	}
	h.waitFor("search results", func() bool { return !e.underlyings && len(e.resultItems) == 1 })
	// Clearing the search brings the underlyings back.
	h.press("backspace", "backspace", "backspace", "esc")
	h.waitFor("underlyings again", func() bool { return e.underlyings && len(e.resultItems) == 2 })

	// O opens NIFTY's chain with the at-the-money strike selected.
	h.press("o")
	h.waitFor("the chain", func() bool { return e.mode == viewChain && h.idle() })
	strikeAt := func() string {
		selected, _ := e.chainGrid.selected()
		return selected.cells[chainStrike].text
	}
	h.waitFor("the ATM strike", func() bool { return strikeAt() == formatStrike(25000) })
	if e.chainGrid.group != 0 {
		t.Errorf("selected side = %d, want calls", e.chainGrid.group)
	}
	// Beside each contract's price, CHG shows its change since the previous
	// close and the percentage.
	call, put := chainCells[0], chainCells[1]
	h.waitFor("chain prices", func() bool { selected, _ := e.chainGrid.selected(); return selected.cells[call.ltp].text != "—" })
	if selected, _ := e.chainGrid.selected(); !regexp.MustCompile(`^\+\d+\.\d\d \+2\.00%$`).MatchString(selected.cells[call.change].text) ||
		selected.cells[call.ltp].text != "110.00" || selected.cells[put.ltp].text != "110.00" ||
		!regexp.MustCompile(`^\+\d+\.\d\d \+\d+\.\d\d%$`).MatchString(selected.cells[put.change].text) {
		t.Errorf("chain prices = %q %q and %q %q, want LTP and +change +percent", selected.cells[call.ltp].text, selected.cells[call.change].text,
			selected.cells[put.ltp].text, selected.cells[put.change].text)
	}
	// One band names each side, over plain column titles.
	if screen := ansi.Strip(h.screen()); !regexp.MustCompile(`CALLS ◂ +▸ PUTS`).MatchString(screen) ||
		!regexp.MustCompile(`OI +POP +BE% +CHG +LTP +STRIKE +LTP +CHG +BE% +POP +OI`).MatchString(screen) {
		t.Errorf("no CALLS/PUTS bands over percentage breakevens:\n%s", screen)
	}
	// POP is the model's chance a bought contract pays, from its implied
	// volatility.
	h.waitFor("POP", func() bool {
		selected, _ := e.chainGrid.selected()
		return strings.HasSuffix(selected.cells[call.pop].text, "%") && strings.HasSuffix(selected.cells[put.pop].text, "%")
	})
	if !strings.Contains(h.screen(), "Implied vol") {
		t.Errorf("no POP column or model section:\n%s", h.screen())
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
	if e.breakevenPercent || !regexp.MustCompile(`POP +BE +CHG +LTP`).MatchString(ansi.Strip(h.screen())) {
		t.Errorf("%% did not switch breakevens to prices")
	}

	// Filters narrow the chain by price, hiding contracts priced outside
	// the range and strikes with none left.
	shown := func() (legs int, prices []float64) {
		for _, r := range e.chainGrid.rows {
			for _, ref := range r.refs {
				if option, ok := ref.(models.Instrument); ok {
					legs++
					prices = append(prices, e.quotes[string(option.ID)].LastPrice)
				}
			}
		}
		return legs, prices
	}
	h.waitFor("chain prices", func() bool { _, prices := shown(); return !slices.Contains(prices, 0) })
	all, _ := shown()
	h.press("f", "tab", "tab", "tab")
	h.typeText("105")
	h.press("tab")
	h.typeText("200")
	h.press("tab", "tab", "tab", "enter")
	if _, open := h.a.overlay.(*filterOverlay); open {
		t.Fatalf("filters did not apply:\n%s", h.screen())
	}
	legs, prices := shown()
	if legs == 0 || legs >= all {
		t.Errorf("LTP filter shows %d of %d contracts", legs, all)
	}
	for _, price := range prices {
		if price < 105 || price > 200 {
			t.Errorf("LTP filter shows a contract at %g", price)
		}
	}
	h.press("f", "tab", "tab", "tab", "tab", "tab")
	h.typeText("1")
	h.press("tab")
	h.typeText("0.5")
	h.press("tab", "enter")
	if form, open := h.a.overlay.(*filterOverlay); !open || !strings.Contains(ansi.Strip(form.form.message), "minimum BE % exceeds maximum BE %") {
		t.Fatalf("a backwards BE range was accepted:\n%s", h.screen())
	}
	h.press("esc")

	// Reset clears every filter; then the side narrows the chain to puts.
	h.press("f", "tab", "tab", "tab", "tab", "tab", "tab", "tab", "right", "enter")
	if e.chain.filter.minLTP != nil || e.chain.filter.maxLTP != nil {
		t.Fatalf("reset kept the LTP filter: %+v", e.chain.filter)
	}
	h.press("f", "right", "right", "tab", "tab", "tab", "tab", "tab", "tab", "tab", "enter")
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

	h.press("esc")
	if e.mode != viewDashboard || !e.underlyings {
		t.Errorf("Esc returned to mode %d", e.mode)
	}
}

func TestListsViewShowsOnlyTheUsersLists(t *testing.T) {
	store, _ := niftyMarket()
	lists := &fakeListStore{lists: []models.List{
		{ID: "admin-banks", CreatedBy: models.AdminUser, Name: "BANKS", Items: []models.ListItem{{InstrumentID: "NSE:RELIANCE"}}},
		{ID: "other-user", CreatedBy: "ZZ9999", Name: "THEIRS"},
	}}
	h := newHarness(t, explorerDeps(t, store, nil, lists, &fakePlacer{}))
	openExplorer(h)
	e := h.a.explorer
	h.waitFor("the default watchlist", func() bool { return e.watching && h.idle() })

	// A offers only the user's own editable watchlists: the smart default
	// can't take items, so a new watchlist it is.
	h.press("a")
	h.waitFor("the list picker", func() bool { _, ok := h.a.overlay.(*menu); return ok })
	if picker := h.a.overlay.(*menu); len(picker.items) != 1 {
		t.Fatalf("picker offers %d items, want only + New watchlist", len(picker.items))
	}
	h.press("enter")
	h.typeText("Watch")
	h.press("enter")
	h.waitFor("the new watchlist", func() bool { return len(lists.byName("Watch").Items) == 1 && h.idle() })
	if owner := lists.byName("Watch").CreatedBy; owner != "AB1234" {
		t.Errorf("Watch belongs to %s", owner)
	}

	// L shows the user's lists, not ADMIN's or another user's.
	h.press("l")
	h.waitFor("lists", func() bool { return e.mode == viewLists && h.idle() })
	var names []string
	for _, r := range e.listsGrid.rows {
		list := r.ref.(models.List)
		if list.CreatedBy != "AB1234" {
			t.Errorf("lists view shows %s's %s", list.CreatedBy, list.Name)
		}
		names = append(names, list.Name)
	}
	if !slices.Contains(names, "Watch") || !slices.Contains(names, marketdata.DefaultSyncListName) {
		t.Errorf("lists view shows %v", names)
	}

	// D deletes the selected list after confirmation.
	e.listsGrid.selectWhere(func(r row) bool { return r.ref.(models.List).Name == "Watch" })
	h.press("d", "left", "enter")
	h.waitFor("the deletion", func() bool { return lists.byName("Watch").ID == "" && h.idle() })
}

func TestAdminListsDownloadRefreshAndDelete(t *testing.T) {
	store, _ := niftyMarket()
	lists := &fakeListStore{}
	deps := explorerDeps(t, store, nil, lists, &fakePlacer{})
	deps.indices = &fakeIndexSource{
		metadata:     []nse.NSEIndexMetadata{{IndexName: "Nifty 50", ListName: "NIFTY50", IndexPageURL: "nifty50"}},
		constituents: map[string][]models.InstrumentID{"nifty50": {"NSE:RELIANCE", "NSE:INFY"}},
	}
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })

	// H opens Admin on Historical Data; ] moves along the sub tabs.
	h.press("h")
	h.waitFor("Admin", func() bool { return h.a.screen == screenHistory && h.idle() })
	if screen := h.screen(); !strings.Contains(screen, "Admin") || !strings.Contains(screen, "Historical Data") || !strings.Contains(screen, "Admin Lists") {
		t.Fatalf("Admin's tabs are missing:\n%s", screen)
	}
	ad := h.a.admin
	h.press("]")
	h.waitFor("Instruments", func() bool { return ad.tab == adminInstruments && h.idle() })
	h.press("]")
	h.waitFor("ADMIN's lists", func() bool { return ad.tab == adminLists && len(ad.listsGrid.rows) == 3 && h.idle() })
	h.waitFor("the index row", h.shows("not downloaded"))
	if !strings.Contains(h.screen(), "Nifty 50") || !strings.Contains(h.screen(), marketdata.DefaultSyncListName) {
		t.Fatalf("Admin Lists:\n%s", h.screen())
	}

	// Enter downloads the selected index; A downloads every one.
	ad.listsGrid.selectWhere(func(r row) bool { return r.ref.(adminListRow).name == "NIFTY50" })
	h.press("enter")
	h.waitFor("the download", func() bool { return len(lists.byName("NIFTY50").Items) == 2 && h.idle() })
	h.press("a", "left", "enter")
	h.waitFor("the FO list", func() bool { return len(lists.byName("FO_LIST").Items) == 2 && h.idle() })
	if !strings.Contains(ansi.Strip(ad.listsStatus), "Saved 2") {
		t.Errorf("status = %q", ansi.Strip(ad.listsStatus))
	}

	// D deletes the selected list after confirmation; the index stays
	// listed, not downloaded.
	ad.listsGrid.selectWhere(func(r row) bool { return r.ref.(adminListRow).name == "NIFTY50" })
	h.press("d", "left", "enter")
	h.waitFor("the deletion", func() bool { return lists.byName("NIFTY50").ID == "" && h.idle() })
	if selected, _ := ad.listsGrid.selectedRef().(adminListRow); selected.name != "NIFTY50" || selected.list != nil {
		t.Errorf("selected %+v after deleting", selected)
	}

	// Esc goes back to where Admin was opened from.
	h.press("esc")
	if h.a.screen != screenUsers {
		t.Errorf("Esc went to screen %d", h.a.screen)
	}
}

// fakeFundamentals serves a fixed summary, and counts its refreshes.
type fakeFundamentals struct {
	mu        sync.Mutex
	summary   fundamentalsSummary
	refreshes int
}

func (f *fakeFundamentals) Summary(context.Context) (fundamentalsSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.summary, nil
}

func (f *fakeFundamentals) Refresh(_ context.Context, progress func(page, pages int)) (fundamentalsSummary, error) {
	progress(1, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	f.summary = fundamentalsSummary{count: 3274, indexCount: 497, refreshedAt: time.Now()}
	return f.summary, nil
}

func TestAdminInstrumentsSummaryAndRefresh(t *testing.T) {
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, &fakeListStore{}, &fakePlacer{})
	fundamentals := &fakeFundamentals{summary: fundamentalsSummary{count: 3000, indexCount: 490, refreshedAt: time.Now().Add(-40 * 24 * time.Hour)}}
	deps.fundamentals = fundamentals
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("h")
	h.waitFor("Admin", func() bool { return h.a.screen == screenHistory && h.idle() })
	h.press("]")
	h.waitFor("the summary", func() bool {
		return h.a.admin.tab == adminInstruments && len(h.a.admin.summaryGrid.rows) > 0 && h.idle()
	})
	screen := ansi.Strip(h.screen())
	if !strings.Contains(screen, "Instrument catalog") || !regexp.MustCompile(`F&O underlyings +2`).MatchString(screen) {
		t.Fatalf("summary:\n%s", screen)
	}
	// The refresh time is an instant, shown on the exchange's clock.
	h.a.admin.summary.lastRefreshed = time.Date(2026, 10, 21, 18, 37, 0, 0, time.UTC)
	if screen := ansi.Strip(h.screen()); !strings.Contains(screen, "22 Oct 2026 00:07") {
		t.Fatalf("refresh time not in IST:\n%s", screen)
	}

	h.press("r")
	h.waitFor("the refresh", func() bool {
		return h.idle() && strings.Contains(ansi.Strip(h.a.admin.summaryStatus), "Catalog refreshed from Kite")
	})
	if store.refreshCalls != 1 {
		t.Errorf("refreshed %d times", store.refreshCalls)
	}

	// Fundamentals over a month old ask for a refresh, which F makes.
	if screen := ansi.Strip(h.screen()); !regexp.MustCompile(`Fundamentals +3,000 instruments · 490 in the Nifty 500 · read .* 40d ago, press F to refresh`).MatchString(screen) {
		t.Fatalf("stale fundamentals:\n%s", screen)
	}
	h.press("f")
	h.waitFor("the fundamentals refresh", func() bool {
		return h.idle() && strings.Contains(ansi.Strip(h.a.admin.summaryStatus), "Fundamentals refreshed")
	})
	if screen := ansi.Strip(h.screen()); fundamentals.refreshes != 1 || !regexp.MustCompile(`Fundamentals +3,274 instruments · 497 in the Nifty 500 · read .* just now`).MatchString(screen) {
		t.Fatalf("after %d refreshes:\n%s", fundamentals.refreshes, screen)
	}
}

func TestOrderMenuStrikePickerAndForm(t *testing.T) {
	store, quotes := niftyMarket()
	placer := &fakePlacer{}
	h := newHarness(t, explorerDeps(t, store, quotes, nil, placer))
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))

	// NIFTY is an index: options only, no raw orders.
	h.press("+")
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
	if order.userID != "AB1234" || order.mode != broker.Paper || order.params.OrderType != kite.OrderTypeLimit || order.params.Price != 12.5 ||
		order.params.Quantity != 75 || order.params.Tradingsymbol != "NIFTYW125000CE" || order.params.Product != kite.ProductNRML {
		t.Errorf("placed %+v", order)
	}

	// A rejected order keeps the form open with the reason.
	placer.fail(errors.New("insufficient funds"))
	h.press("down", "+", "s")
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
	h.press("o")
	h.waitFor("the chain", func() bool { return h.a.explorer.mode == viewChain && h.idle() })
	h.press("+", "p")
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
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
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
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
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
	if got.orderID != "2001" || got.userID != "AB1234" || got.mode != broker.Paper || got.params.Quantity != 150 || got.params.Price != 110.6 || got.params.OrderType != "LIMIT" || got.params.Tradingsymbol != "" {
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
	if got := placer.placed()[1]; !got.cancelled || got.orderID != "2001" || got.mode != broker.Paper {
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

func TestSettingsTab(t *testing.T) {
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, nil, &fakePlacer{})
	h := newHarness(t, deps)
	shift := func(code rune) { h.dispatch(tea.KeyPressMsg{Code: code, Mod: tea.ModShift}) }
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })

	// s no longer opens settings; Settings is the tab after Admin.
	h.press("s")
	if h.a.screen != screenUsers || h.a.settings != nil {
		t.Fatalf("s: screen %v, settings %v", h.a.screen, h.a.settings != nil)
	}
	if bar := ansi.Strip(h.a.tabBar(160)); !regexp.MustCompile(`Admin +Settings`).MatchString(bar) {
		t.Fatalf("tab bar: %s", bar)
	}
	shift(tea.KeyLeft)
	if h.a.screen != screenSettings || h.a.settings == nil {
		t.Fatalf("Shift+← from Users: screen %v", h.a.screen)
	}
	s := h.a.settings
	screen := h.screen()
	for _, want := range []string{"Settings", "Base Path", "~/tradebot", "Where local files are stored", "Default User", "CC0006", "Price Mode", "quote", "NATS URL", "SETTINGS"} {
		if !strings.Contains(screen, want) {
			t.Errorf("settings lack %q:\n%s", want, screen)
		}
	}
	// Only the focused setting's help shows, and hidden settings don't.
	for _, unwanted := range []string{"The account whose portfolio", "Credentials Database", "Instruments Database", "environment"} {
		if strings.Contains(screen, unwanted) {
			t.Errorf("settings show %q:\n%s", unwanted, screen)
		}
	}

	// Edit the default user, switch the price mode to ticker, and save.
	h.press("down")
	if got := s.form.current().label; got != "Default User" {
		t.Fatalf("focused %q", got)
	}
	h.waitFor("the default user's help", h.shows("The account whose portfolio"))
	for range len("CC0006") {
		h.press("backspace")
	}
	// Letters are typed into the field, not taken as shortcuts.
	h.typeText("wh1234")
	if h.a.screen != screenSettings || s.form.current().value() != "wh1234" {
		t.Fatalf("typing: screen %v, value %q", h.a.screen, s.form.current().value())
	}
	// Unsaved changes survive switching tabs.
	shift(tea.KeyRight)
	shift(tea.KeyLeft)
	if h.a.screen != screenSettings || h.a.settings != s || !strings.Contains(h.screen(), "unsaved changes") {
		t.Fatalf("switching tabs lost the edits")
	}
	h.press("down", "right")
	h.waitFor("the ticker note", h.shows("Needs ticker-server running"))
	// Enter on a field saves once something changed, staying on the tab.
	h.press("enter")
	saved, err := config.ReadSettings(deps.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Get("TRADEBOT_LOCAL_STORAGE_ROOT") + "|" + saved.Get("TRADEBOT_DEFAULT_USER_ID") + "|" + saved.Get("TRADEBOT_PRICE_MODE"); got != "~/tradebot|WH1234|ticker" {
		t.Errorf("saved %s", got)
	}
	h.waitFor("the saved notice", h.shows("Settings saved"))
	s = h.a.settings
	if h.a.screen != screenSettings || s.edited() || s.form.current().label != "Price Mode" {
		t.Fatalf("after saving: screen %v, edited %v", h.a.screen, s.edited())
	}

	// Esc reverts unsaved changes, then goes back.
	h.press("up")
	for range len("WH1234") {
		h.press("backspace")
	}
	h.press("esc")
	s = h.a.settings
	if h.a.screen != screenSettings || s.edited() || s.form.field("Default User").value() != "WH1234" {
		t.Fatalf("Esc with changes: screen %v, default user %q", h.a.screen, s.form.field("Default User").value())
	}
	h.press("esc")
	if h.a.screen != screenUsers {
		t.Fatalf("Esc without changes went to %v", h.a.screen)
	}

	// An emptied field takes its default again.
	shift(tea.KeyLeft)
	s = h.a.settings
	s.form.focusField("Default User")
	for range len("WH1234") {
		h.press("backspace")
	}
	pressSave(h, s)
	if saved, err = config.ReadSettings(deps.settingsPath); err != nil {
		t.Fatal(err)
	}
	if got := saved.Get("TRADEBOT_DEFAULT_USER_ID"); got != "CC0006" {
		t.Errorf("emptied default user reads %q, want the default", got)
	}
	if hidden := saved[1]; hidden.Key != "TRADEBOT_CREDENTIALS_SQLITE_PATH" || hidden.Visible || hidden.Value != "" {
		t.Errorf("a hidden setting changed: %+v", hidden)
	}
}

// pressSave moves down to the Save button and presses it.
func pressSave(h *harness, s *settingsForm) {
	h.t.Helper()
	for s.form.button() != 0 {
		h.press("down")
	}
	h.press("enter")
}

func TestPositionGroupShowsUnderlyingLTP(t *testing.T) {
	nifty := models.InstrumentID("NSE:NIFTY 50")
	data := portfolio{
		positions: []kite.Position{
			{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", Quantity: 75},
			{Tradingsymbol: "NIFTY26OCT25000PE", Exchange: "NFO", Quantity: 75},
		},
		instruments: map[models.InstrumentID]models.Instrument{
			"NFO:NIFTY26OCTFUT":     {ID: "NFO:NIFTY26OCTFUT", UnderlyingID: &nifty},
			"NFO:NIFTY26OCT25000PE": {ID: "NFO:NIFTY26OCT25000PE", UnderlyingID: &nifty},
			nifty:                   {ID: nifty, DisplayName: "NIFTY 50"},
		},
	}
	if ids := data.priceIDs(); !slices.Contains(ids, string(nifty)) {
		t.Fatalf("the underlying is not priced: %v", ids)
	}
	data = data.withPrices(kite.QuoteOHLC{string(nifty): {LastPrice: 25100.5, OHLC: models.OHLC{Close: 25000}}}, time.Now())

	p := newPortfolioScreen()
	p.data = data
	p.render()
	group := p.positions.rows[0]
	if group.ref != nifty || len(group.cells) < 6 || group.cells[5].text != "25,100.50" {
		t.Fatalf("group row = %+v", group)
	}
	if detail := ansi.Strip(p.groupDetail(nifty)); !strings.Contains(detail, "25,100.50") || !strings.Contains(detail, "+100.50") {
		t.Errorf("group detail lacks the underlying's price:\n%s", detail)
	}
}

func TestGroupExitAndCharges(t *testing.T) {
	nifty := models.InstrumentID("NSE:NIFTY 50")
	store, _ := niftyMarket()
	placer := &fakePlacer{}
	deps := explorerDeps(t, store, nil, nil, placer)
	orders := kite.Orders{
		{OrderID: "1001", Status: "COMPLETE", Exchange: "NSE", TradingSymbol: "INFY", TransactionType: "BUY", OrderType: "MARKET", Product: "CNC", Quantity: 10, FilledQuantity: 10, AveragePrice: 1500},
		{OrderID: "1002", Status: "REJECTED", Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000PE", TransactionType: "SELL", OrderType: "LIMIT", Product: "NRML", Quantity: 75, Price: 120},
	}
	deps.loadPortfolio = func(context.Context, credentials.Credentials, broker.Mode) portfolio {
		data := portfolio{
			positions: []kite.Position{
				{Tradingsymbol: "NIFTY26OCTFUT", Exchange: "NFO", Product: "NRML", Quantity: 75, LastPrice: 25000},
				{Tradingsymbol: "NIFTY26OCT25000PE", Exchange: "NFO", Product: "NRML", Quantity: -75, LastPrice: 100},
			},
			orders: orders,
			instruments: map[models.InstrumentID]models.Instrument{
				"NFO:NIFTY26OCTFUT":     {ID: "NFO:NIFTY26OCTFUT", Exchange: "NFO", TradingSymbol: "NIFTY26OCTFUT", LotSize: 75, UnderlyingID: &nifty},
				"NFO:NIFTY26OCT25000PE": {ID: "NFO:NIFTY26OCT25000PE", Exchange: "NFO", TradingSymbol: "NIFTY26OCT25000PE", LotSize: 75, UnderlyingID: &nifty},
				nifty:                   {ID: nifty, DisplayName: "NIFTY 50"},
			},
			loadedAt: time.Now(),
			loggedIn: true,
		}
		data.orderCharges, data.chargesErr = readOrderCharges(data.orders)
		return data
	}
	deps.defaultUser = "ab1234"
	h := newHarness(t, deps)
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })

	// The orders table sums the day's executed orders' charges: 10 INFY
	// bought for delivery at 1,500 cost 17.81.
	h.waitFor("the day's charges", h.shows("charges ₹17.81"))

	// + on a position's order form estimates its charges.
	p := h.a.portfolio
	h.press("down", "+", "b")
	if _, ok := h.a.overlay.(*orderForm); !ok {
		t.Fatalf("o b opened %T", h.a.overlay)
	}
	h.waitFor("the order's charges", h.shows("Charges ≈ ₹"))
	h.press("esc")

	// X on the group header exits every leg at market, buying back the
	// short put before selling the future.
	h.press("up")
	if _, ok := p.focused().selectedRef().(models.InstrumentID); !ok {
		t.Fatalf("selected %T, want the group header", p.focused().selectedRef())
	}
	h.press("x")
	m, ok := h.a.overlay.(*menu)
	if !ok {
		t.Fatalf("X opened %T", h.a.overlay)
	}
	screen := h.screen()
	for _, want := range []string{"Exit NIFTY 50", "BUY 75", "SELL 75", "Charges ≈ ₹", "Exit all 2"} {
		if !strings.Contains(screen, want) {
			t.Errorf("exit dialog lacks %q:\n%s", want, screen)
		}
	}
	if m.cursor != 1 {
		t.Errorf("the exit dialog defaults to item %d, want Keep", m.cursor)
	}
	h.press("y")
	h.waitFor("the exit notice", h.shows("2 orders placed"))
	placer.mu.Lock()
	defer placer.mu.Unlock()
	if len(placer.orders) != 2 {
		t.Fatalf("placed %+v", placer.orders)
	}
	first, second := placer.orders[0].params, placer.orders[1].params
	if first.TransactionType != "BUY" || first.Tradingsymbol != "NIFTY26OCT25000PE" || first.Quantity != 75 || first.OrderType != "MARKET" || first.Product != "NRML" {
		t.Errorf("first exit = %+v", first)
	}
	if second.TransactionType != "SELL" || second.Tradingsymbol != "NIFTY26OCTFUT" || second.Quantity != 75 {
		t.Errorf("second exit = %+v", second)
	}
}

func TestInstrumentSidebarShowsMarketDepth(t *testing.T) {
	store, quotes := niftyMarket()
	h := newHarness(t, explorerDeps(t, store, quotes, &fakeListStore{}, &fakePlacer{}))
	openExplorer(h)
	h.waitFor("prices", h.shows("25040.00"))

	// NIFTY 50 is an index, without depth.
	if strings.Contains(h.screen(), "Depth") {
		t.Errorf("an index shows depth:\n%s", h.screen())
	}
	h.press("down")
	h.waitFor("RELIANCE's depth", h.shows("Depth"))
	screen := ansi.Strip(h.screen())
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`ORD +QTY +BID ASK +QTY +ORD`),
		regexp.MustCompile(`1 +100 +1399\.95 1400\.05 +50 +2`),
		regexp.MustCompile(`5 +500 +1399\.75 1400\.25 +250 +6`),
		regexp.MustCompile(`3000 bid · 75% +25% · 1000 offered`),
	} {
		if !want.MatchString(screen) {
			t.Errorf("no %s in the sidebar:\n%s", want, screen)
		}
	}
	// The sidebar is live data only; M shows the catalog metadata.
	if strings.Contains(screen, "Identity") || !strings.Contains(screen, "M metadata") {
		t.Errorf("the sidebar shows metadata:\n%s", screen)
	}
	h.press("m")
	if o, ok := h.a.overlay.(*textOverlay); !ok || !strings.Contains(o.text, "Identity") || !strings.Contains(ansi.Strip(o.text), "Lot size       1") {
		t.Fatalf("M opened %#v", h.a.overlay)
	}
	h.press("m")
	if h.a.overlay != nil {
		t.Errorf("M again left %T open", h.a.overlay)
	}
}

func TestExplorerSortsTheInstrumentList(t *testing.T) {
	store, quotes := niftyMarket()
	h := newHarness(t, explorerDeps(t, store, quotes, nil, &fakePlacer{}))
	openExplorer(h)
	h.waitFor("prices", h.shows("1400.00"))
	first := func() models.InstrumentID {
		instrument, _ := h.a.explorer.results.rows[0].ref.(models.Instrument)
		return instrument.ID
	}
	selected := func() models.InstrumentID {
		instrument, _ := h.a.explorer.selectedInstrument()
		return instrument.ID
	}
	h.waitFor("a settled list", h.idle)
	listFirst := first()
	if selected() != "NSE:NIFTY 50" {
		h.press("down")
	}
	if selected() != "NSE:NIFTY 50" {
		t.Fatalf("selected %s", selected())
	}

	// LTP ascending puts RELIANCE first and descending NIFTY, with the
	// selection following NIFTY.
	h.press("S")
	h.waitFor("the sort menu", h.shows("Sort by"))
	h.press("down", "down", "down", "enter")
	h.waitFor("the sorted header", h.shows("LTP ▲"))
	if first() != "NSE:RELIANCE" || selected() != "NSE:NIFTY 50" {
		t.Fatalf("ascending LTP: first %s, selected %s", first(), selected())
	}

	// Picking LTP again reverses it.
	h.press("S", "enter")
	h.waitFor("the reversed header", h.shows("LTP ▼"))
	if first() != "NSE:NIFTY 50" || selected() != "NSE:NIFTY 50" {
		t.Fatalf("descending LTP: first %s, selected %s", first(), selected())
	}

	// List order restores the catalog's order and the plain header.
	h.press("S", "up", "up", "up", "enter")
	h.waitFor("the plain header", func() bool { return !strings.Contains(h.screen(), "LTP ▼") })
	if first() != listFirst {
		t.Fatalf("list order starts with %s, want %s", first(), listFirst)
	}
}

func TestUsersOpensKiteWeb(t *testing.T) {
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, nil, &fakePlacer{})
	opened := make(chan string, 1)
	deps.openWeb = func(_ context.Context, user credentials.Credentials) error {
		opened <- user.UserID
		return nil
	}
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })

	h.press("b")
	h.waitFor("Kite web", h.shows("Kite web opened for AB1234"))
	if got := <-opened; got != "AB1234" {
		t.Fatalf("opened Kite web for %q, want AB1234", got)
	}
	if !h.idle() {
		t.Fatal("busy overlay still open")
	}

	deps.openWeb = nil
	h.a.deps.openWeb = nil
	h.press("b")
	h.waitFor("unavailable notice", h.shows("Kite web is not available"))
}
