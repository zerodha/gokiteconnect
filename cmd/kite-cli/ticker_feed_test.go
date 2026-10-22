package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/models"
)

// fakeBus is a tickBus in memory.
type fakeBus struct {
	mu        sync.Mutex
	connected bool
	handlers  map[string]func([]byte)
	published chan []byte
}

func newFakeBus() *fakeBus {
	return &fakeBus{connected: true, handlers: map[string]func([]byte){}, published: make(chan []byte, 64)}
}

func (b *fakeBus) Publish(subject string, data []byte) error {
	if subject != "ticks.subscriptions" {
		return errors.New("unexpected subject " + subject)
	}
	b.published <- data
	return nil
}

func (b *fakeBus) Subscribe(subject string, handler func([]byte)) (func() error, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[subject] = handler
	return func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.handlers, subject)
		return nil
	}, nil
}

func (b *fakeBus) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected
}

func (b *fakeBus) Close() {}

func (b *fakeBus) subjects() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var subjects []string
	for subject := range b.handlers {
		subjects = append(subjects, subject)
	}
	slices.Sort(subjects)
	return subjects
}

// tick delivers a tick the way ticker-server publishes it.
func (b *fakeBus) tick(t *testing.T, id string, price float64) {
	t.Helper()
	subject, err := tickSubject(id)
	if err != nil {
		t.Fatal(err)
	}
	var tick producer.Tick
	tick.ID, tick.LastPrice, tick.OHLC.Close = id, price, price/1.02
	data, err := json.Marshal(tick)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	handler := b.handlers[subject]
	b.mu.Unlock()
	if handler == nil {
		t.Fatalf("no subscription on %s", subject)
	}
	handler(data)
}

func (b *fakeBus) nextHeartbeat(t *testing.T) []string {
	t.Helper()
	select {
	case data := <-b.published:
		var payload struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		return payload.IDs
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat")
		return nil
	}
}

func TestTickSubject(t *testing.T) {
	for id, want := range map[string]string{
		"NSE:NIFTY 50":          "ticks.nse.nifty_50",
		"NFO:NIFTY25AUG25000CE": "ticks.nfo.nifty25aug25000ce",
	} {
		if got, err := tickSubject(id); err != nil || got != want {
			t.Errorf("tickSubject(%q) = %q, %v; want %q", id, got, err, want)
		}
	}
	if _, err := tickSubject("NIFTY"); err == nil {
		t.Error("an ID without an exchange has no subject")
	}
}

func TestTickerFeedHeartbeatFollowsWantedInstruments(t *testing.T) {
	bus := newFakeBus()
	feed := newTickerFeed(bus, "ticks.subscriptions")
	defer feed.close()
	now := time.Date(2026, time.October, 16, 10, 0, 0, 0, models.ExchangeLocation)
	feed.mu.Lock()
	feed.now = func() time.Time { return now }
	feed.mu.Unlock()
	notified := make(chan struct{}, 8)
	feed.setNotify(func() { notified <- struct{}{} })

	// New instruments are subscribed and sent at once.
	feed.want("NSE:NIFTY 50", "NSE:RELIANCE")
	if ids := bus.nextHeartbeat(t); !slices.Equal(ids, []string{"NSE:NIFTY 50", "NSE:RELIANCE"}) {
		t.Fatalf("heartbeat = %v", ids)
	}
	if subjects := bus.subjects(); !slices.Equal(subjects, []string{"ticks.nse.nifty_50", "ticks.nse.reliance"}) {
		t.Fatalf("subjects = %v", subjects)
	}
	// Asking again for known instruments sends nothing new.
	feed.want("NSE:NIFTY 50")
	select {
	case data := <-bus.published:
		t.Fatalf("unexpected heartbeat %s", data)
	case <-time.After(50 * time.Millisecond):
	}

	bus.tick(t, "NSE:NIFTY 50", 25100)
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("ticks did not redraw")
	}
	quotes, missing := feed.quotes([]string{"NSE:NIFTY 50", "NSE:RELIANCE"})
	if quotes["NSE:NIFTY 50"].LastPrice != 25100 || !slices.Equal(missing, []string{"NSE:RELIANCE"}) {
		t.Fatalf("quotes = %v, missing = %v", quotes, missing)
	}

	// A tick goes stale, and an instrument not asked for drops out of the
	// heartbeat and is unsubscribed.
	now = now.Add(tickerStaleAfter + time.Second)
	if _, missing := feed.quotes([]string{"NSE:NIFTY 50"}); len(missing) != 1 {
		t.Fatalf("stale tick still prices: missing = %v", missing)
	}
	feed.want("NSE:RELIANCE")
	now = now.Add(tickerWantTTL - 2*time.Second)
	feed.sendHeartbeat()
	if ids := bus.nextHeartbeat(t); !slices.Equal(ids, []string{"NSE:RELIANCE"}) {
		t.Fatalf("heartbeat after expiry = %v", ids)
	}
	if subjects := bus.subjects(); !slices.Equal(subjects, []string{"ticks.nse.reliance"}) {
		t.Fatalf("subjects after expiry = %v", subjects)
	}
}

func TestTickerQuotesFillFromRESTAtMostEveryInterval(t *testing.T) {
	bus := newFakeBus()
	feed := newTickerFeed(bus, "ticks.subscriptions")
	defer feed.close()
	rest := &fakeQuotes{prices: map[string]float64{"NSE:NIFTY 50": 25040, "NSE:RELIANCE": 1400}}
	source := newTickerQuotes(feed, rest, time.Hour)

	quotes, err := source.GetQuote("NSE:NIFTY 50", "NSE:RELIANCE")
	if err != nil || quotes["NSE:NIFTY 50"].LastPrice != 25040 || rest.requests != 1 {
		t.Fatalf("first quotes = %v, %v, requests %d", quotes, err, rest.requests)
	}
	bus.tick(t, "NSE:NIFTY 50", 25100)
	quotes, err = source.GetQuote("NSE:NIFTY 50", "NSE:RELIANCE")
	if err != nil || quotes["NSE:NIFTY 50"].LastPrice != 25100 || quotes["NSE:RELIANCE"].LastPrice != 1400 || rest.requests != 1 {
		t.Fatalf("ticked quotes = %v, %v, requests %d", quotes, err, rest.requests)
	}

	// Without the NATS connection everything comes from REST again.
	bus.mu.Lock()
	bus.connected = false
	bus.mu.Unlock()
	source.fill.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	quotes, _ = source.GetQuote("NSE:NIFTY 50")
	if quotes["NSE:NIFTY 50"].LastPrice != 25040 || rest.requests != 2 {
		t.Fatalf("disconnected quotes = %v, requests %d", quotes, rest.requests)
	}
}

func TestTickerQuoteLoaderFallsBackToQuotes(t *testing.T) {
	rest := &fakeQuotes{}
	load := func(context.Context) (quoteSource, string, error) { return rest, "DA0001", nil }
	down := &tickerConnector{connect: func(context.Context) (*tickerFeed, error) {
		return nil, errors.New("ticker-server isn't running")
	}}
	source, label, err := tickerQuoteLoader(load, down, time.Second)(context.Background())
	var unavailable tickerUnavailableError
	if source != rest || label != "DA0001" || !errors.As(err, &unavailable) {
		t.Fatalf("fallback = %v, %q, %v", source, label, err)
	}

	bus := newFakeBus()
	up := &tickerConnector{connect: func(context.Context) (*tickerFeed, error) {
		return newTickerFeed(bus, "ticks.subscriptions"), nil
	}}
	defer up.close()
	source, label, err = tickerQuoteLoader(load, up, time.Second)(context.Background())
	if _, ok := source.(*tickerQuotes); !ok || label != "TICKER DA0001" || err != nil {
		t.Fatalf("ticker = %T, %q, %v", source, label, err)
	}
	if _, ok := source.(marginSource); !ok {
		t.Fatal("ticker quotes lost margins")
	}
}

func TestExplorerShowsTickerFallbackAndLiveTicks(t *testing.T) {
	store, quotes := niftyMarket()
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	down := &tickerConnector{connect: func(context.Context) (*tickerFeed, error) {
		return nil, errors.New("ticker-server isn't running at http://localhost:8082/health")
	}}
	deps.loadQuotes = tickerQuoteLoader(deps.loadQuotes, down, time.Hour)
	h := newHarness(t, deps)
	openExplorer(h)
	h.waitFor("the fallback note", h.shows("ticker down"))
	h.waitFor("quoted prices", h.shows("25040.00"))

	// With ticker-server up, reopening Watchlists reconnects and ticks
	// reprice the screen.
	bus := newFakeBus()
	up := &tickerConnector{connect: func(context.Context) (*tickerFeed, error) {
		return newTickerFeed(bus, "ticks.subscriptions"), nil
	}}
	defer up.close()
	up.setNotify(func() { h.post(tickerTicksMsg{}) })
	h.a.deps.loadQuotes = tickerQuoteLoader(explorerDeps(t, store, quotes, nil, &fakePlacer{}).loadQuotes, up, time.Hour)
	h.press("esc", "w")
	h.waitFor("the ticker label", h.shows("prices TICKER TEST"))
	h.waitFor("the subscription", func() bool { return slices.Contains(bus.subjects(), "ticks.nse.nifty_50") })
	bus.tick(t, "NSE:NIFTY 50", 25111)
	h.waitFor("the ticked price", h.shows("25111.00"))
}

func TestDecodeTickAcceptsZeroTimes(t *testing.T) {
	tick, err := decodeTick([]byte(`{"id":"NSE:NIFTY 50","IsIndex":true,"Timestamp":"2026-10-16T10:00:01+05:30","LastTradeTime":"0001-01-01T00:00:00Z","LastPrice":25100.5,"OHLC":{"close":25000}}`))
	if err != nil {
		t.Fatal(err)
	}
	if tick.ID != "NSE:NIFTY 50" || tick.LastPrice != 25100.5 || tick.OHLC.Close != 25000 || tick.Timestamp.Second() != 1 || !tick.LastTradeTime.IsZero() {
		t.Fatalf("tick = %+v", tick)
	}
}
