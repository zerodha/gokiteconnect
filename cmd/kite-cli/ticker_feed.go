package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/nats-io/nats.go"
)

const (
	// tickerHeartbeatEvery is how often the wanted instruments are sent to
	// ticker-server, well inside its subscription TTL.
	tickerHeartbeatEvery = 15 * time.Second
	// tickerWantTTL is how long an instrument stays subscribed after it was
	// last asked for: off-screen instruments drop out of the heartbeat.
	tickerWantTTL = 30 * time.Second
	// tickerNotifyEvery throttles redraws on new ticks.
	tickerNotifyEvery    = 250 * time.Millisecond
	tickerConnectTimeout = 3 * time.Second
	// tickerStaleAfter is how long a tick prices its instrument: after it,
	// such as when ticker-server stops, prices come from quotes again.
	tickerStaleAfter = 30 * time.Second
)

// tickerTicksMsg redraws prices after new ticks, at most every
// tickerNotifyEvery.
type tickerTicksMsg struct{}

// tickBus is the NATS connection the ticker feed uses.
type tickBus interface {
	Publish(subject string, data []byte) error
	Subscribe(subject string, handler func(data []byte)) (unsubscribe func() error, err error)
	Connected() bool
	Close()
}

// tickerFeed streams ticks that ticker-server publishes to NATS. It keeps
// ticker-server subscribed to the instruments the screens ask for by
// sending their IDs as a heartbeat, adding new ones at once and dropping
// ones not asked for within tickerWantTTL.
type tickerFeed struct {
	bus   tickBus
	topic string
	now   func() time.Time

	mu     sync.Mutex
	wanted map[string]time.Time
	subs   map[string]func() error
	ticks  map[string]receivedTick
	notify func()
	// notifyAt is when the last redraw was sent; notifyPending marks one
	// scheduled for the end of the throttle window.
	notifyAt      time.Time
	notifyPending bool

	beat chan struct{}
	stop chan struct{}
	done chan struct{}
}

type receivedTick struct {
	tick producer.Tick
	at   time.Time
}

func newTickerFeed(bus tickBus, topic string) *tickerFeed {
	f := &tickerFeed{
		bus:    bus,
		topic:  topic,
		now:    time.Now,
		wanted: map[string]time.Time{},
		subs:   map[string]func() error{},
		ticks:  map[string]receivedTick{},
		beat:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go f.heartbeats()
	return f
}

// setNotify sets the function called, throttled, when ticks arrive.
func (f *tickerFeed) setNotify(notify func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notify = notify
}

func (f *tickerFeed) connected() bool { return f.bus.Connected() }

// want marks ids as wanted now, subscribing to new ones and sending the
// heartbeat at once when there are any.
func (f *tickerFeed) want(ids ...string) {
	f.mu.Lock()
	now, added := f.now(), false
	for _, id := range ids {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if _, ok := f.wanted[id]; !ok {
			added = true
		}
		f.wanted[id] = now
		if _, ok := f.subs[id]; ok {
			continue
		}
		subject, err := tickSubject(id)
		if err != nil {
			continue
		}
		unsubscribe, err := f.bus.Subscribe(subject, f.received)
		if err != nil {
			continue
		}
		f.subs[id] = unsubscribe
	}
	f.mu.Unlock()
	if added {
		select {
		case f.beat <- struct{}{}:
		default:
		}
	}
}

func (f *tickerFeed) received(data []byte) {
	tick, err := decodeTick(data)
	if err != nil || tick.ID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.wanted[tick.ID]; !ok {
		return
	}
	f.ticks[tick.ID] = receivedTick{tick: tick, at: f.now()}
	f.scheduleNotifyLocked()
}

// decodeTick reads a producer.Tick. Its times are decoded as plain
// time.Time: models.Time rejects the zero time index ticks carry for their
// last trade.
func decodeTick(data []byte) (producer.Tick, error) {
	var wire struct {
		producer.Tick
		Timestamp     time.Time
		LastTradeTime time.Time
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return producer.Tick{}, err
	}
	tick := wire.Tick
	tick.Timestamp, tick.LastTradeTime = models.Time{Time: wire.Timestamp}, models.Time{Time: wire.LastTradeTime}
	return tick, nil
}

// scheduleNotifyLocked redraws at most once per tickerNotifyEvery. Called
// with mu held.
func (f *tickerFeed) scheduleNotifyLocked() {
	if f.notify == nil || f.notifyPending {
		return
	}
	wait := tickerNotifyEvery - f.now().Sub(f.notifyAt)
	if wait <= 0 {
		f.notifyAt = f.now()
		go f.notify()
		return
	}
	f.notifyPending = true
	time.AfterFunc(wait, func() {
		f.mu.Lock()
		f.notifyPending, f.notifyAt = false, f.now()
		notify := f.notify
		f.mu.Unlock()
		notify()
	})
}

// heartbeats sends the wanted IDs every tickerHeartbeatEvery, and at once
// when want adds one.
func (f *tickerFeed) heartbeats() {
	defer close(f.done)
	timer := time.NewTicker(tickerHeartbeatEvery)
	defer timer.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-timer.C:
		case <-f.beat:
		}
		f.sendHeartbeat()
	}
}

// sendHeartbeat drops instruments no longer wanted and publishes the rest
// as {"ids": [...]}, the payload ticker-server's consumer reads.
func (f *tickerFeed) sendHeartbeat() {
	ids := f.prune()
	if len(ids) == 0 {
		return
	}
	payload, err := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: ids})
	if err != nil {
		return
	}
	_ = f.bus.Publish(f.topic, payload)
}

// prune forgets instruments not asked for within tickerWantTTL and
// returns the wanted IDs, sorted.
func (f *tickerFeed) prune() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := f.now().Add(-tickerWantTTL)
	ids := make([]string, 0, len(f.wanted))
	for id, at := range f.wanted {
		if at.Before(cutoff) {
			delete(f.wanted, id)
			delete(f.ticks, id)
			if unsubscribe, ok := f.subs[id]; ok {
				_ = unsubscribe()
				delete(f.subs, id)
			}
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// heartbeatIDs are the IDs the next heartbeat would send.
func (f *tickerFeed) heartbeatIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.wanted))
	for id := range f.wanted {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// quotes are the latest ticks of ids as quotes, and the ids without one.
func (f *tickerFeed) quotes(ids []string) (kite.Quote, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(kite.Quote, len(ids))
	var missing []string
	connected, cutoff := f.bus.Connected(), f.now().Add(-tickerStaleAfter)
	for _, id := range ids {
		received, ok := f.ticks[id]
		if !ok || !connected || received.at.Before(cutoff) {
			missing = append(missing, id)
			continue
		}
		out[id] = tickQuote(received.tick)
	}
	return out, missing
}

func (f *tickerFeed) close() {
	select {
	case <-f.stop:
		return
	default:
	}
	close(f.stop)
	<-f.done
	f.bus.Close()
}

func tickQuote(tick producer.Tick) kite.QuoteData {
	return kite.QuoteData{
		InstrumentToken: int(tick.InstrumentToken),
		Timestamp:       tick.Timestamp,
		LastPrice:       tick.LastPrice,
		LastQuantity:    int(tick.LastTradedQuantity),
		LastTradeTime:   tick.LastTradeTime,
		AveragePrice:    tick.AverageTradePrice,
		Volume:          int(tick.VolumeTraded),
		BuyQuantity:     int(tick.TotalBuyQuantity),
		SellQuantity:    int(tick.TotalSellQuantity),
		OHLC:            tick.OHLC,
		NetChange:       tick.NetChange,
		OI:              float64(tick.OI),
		OIDayHigh:       float64(tick.OIDayHigh),
		OIDayLow:        float64(tick.OIDayLow),
		Depth:           tick.Depth,
	}
}

// tickSubject is ticker-server's subject for an instrument's full ticks:
// NSE:NIFTY 50 is ticks.nse.nifty_50.
func tickSubject(id string) (string, error) {
	exchange, symbol, ok := strings.Cut(strings.TrimSpace(id), ":")
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.ReplaceAll(value, " ", "_")
		return strings.ReplaceAll(value, ":", ".")
	}
	exchange, symbol = normalize(exchange), normalize(symbol)
	if !ok || exchange == "" || symbol == "" {
		return "", fmt.Errorf("invalid instrument ID %q", id)
	}
	return "ticks." + exchange + "." + symbol, nil
}

// restFill caches REST prices for instruments without ticks, so redraws on
// ticks don't poll Kite for them more than once per every.
type restFill[T any] struct {
	every time.Duration
	now   func() time.Time

	mu     sync.Mutex
	values map[string]T
	at     map[string]time.Time
}

func newRestFill[T any](every time.Duration) *restFill[T] {
	if every <= 0 {
		every = 5 * time.Second
	}
	return &restFill[T]{every: every, now: time.Now, values: map[string]T{}, at: map[string]time.Time{}}
}

// get puts the cached values of ids in out and fetches the stale rest.
func (r *restFill[T]) get(ids []string, out map[string]T, fetch func(ids ...string) (map[string]T, error)) error {
	r.mu.Lock()
	now := r.now()
	var stale []string
	for _, id := range ids {
		if at, ok := r.at[id]; ok && now.Sub(at) < r.every {
			if value, ok := r.values[id]; ok {
				out[id] = value
			}
			continue
		}
		stale = append(stale, id)
	}
	r.mu.Unlock()
	if len(stale) == 0 {
		return nil
	}
	fetched, err := fetch(stale...)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range stale {
		r.at[id] = now
		if value, ok := fetched[id]; ok {
			r.values[id] = value
			out[id] = value
		} else {
			delete(r.values, id)
		}
	}
	return nil
}

// tickerQuotes prices from ticks, filling instruments without one, such as
// before the first tick or outside market hours, from REST quotes.
type tickerQuotes struct {
	feed *tickerFeed
	rest quoteSource
	fill *restFill[kite.QuoteData]
}

func newTickerQuotes(feed *tickerFeed, rest quoteSource, every time.Duration) *tickerQuotes {
	return &tickerQuotes{feed: feed, rest: rest, fill: newRestFill[kite.QuoteData](every)}
}

func (t *tickerQuotes) GetQuote(ids ...string) (kite.Quote, error) {
	t.feed.want(ids...)
	quotes, missing := t.feed.quotes(ids)
	if len(missing) == 0 || t.rest == nil {
		return quotes, nil
	}
	err := t.fill.get(missing, quotes, func(ids ...string) (map[string]kite.QuoteData, error) {
		return t.rest.GetQuote(ids...)
	})
	if err != nil && len(quotes) == 0 {
		return nil, err
	}
	return quotes, nil
}

// GetOrderMargins passes through to the REST client, for lot margins.
func (t *tickerQuotes) GetOrderMargins(params kite.GetMarginParams) ([]kite.OrderMargins, error) {
	source, ok := t.rest.(marginSource)
	if !ok {
		return nil, errors.New("margins need a Kite session")
	}
	return source.GetOrderMargins(params)
}

// tickerPrices prices the portfolio from ticks, filling the rest from the
// user's REST quotes.
type tickerPrices struct {
	feed *tickerFeed
	rest priceSource
	fill *restFill[kite.QuoteOHLCData]
}

func newTickerPrices(feed *tickerFeed, rest priceSource, every time.Duration) *tickerPrices {
	return &tickerPrices{feed: feed, rest: rest, fill: newRestFill[kite.QuoteOHLCData](every)}
}

func (t *tickerPrices) GetOHLC(ids ...string) (kite.QuoteOHLC, error) {
	t.feed.want(ids...)
	quotes, missing := t.feed.quotes(ids)
	out := make(kite.QuoteOHLC, len(ids))
	for id, quote := range quotes {
		out[id] = kite.QuoteOHLCData{InstrumentToken: quote.InstrumentToken, LastPrice: quote.LastPrice, OHLC: quote.OHLC}
	}
	if len(missing) == 0 || t.rest == nil {
		return out, nil
	}
	err := t.fill.get(missing, out, func(ids ...string) (map[string]kite.QuoteOHLCData, error) {
		return t.rest.GetOHLC(ids...)
	})
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// tickerConnector connects the ticker feed for price mode ticker, reusing
// it while ticker-server stays up.
type tickerConnector struct {
	// connect checks ticker-server and opens the feed.
	connect func(ctx context.Context) (*tickerFeed, error)

	mu     sync.Mutex
	feed   *tickerFeed
	notify func()
}

// get is the live feed, connecting when there is none or it dropped.
func (c *tickerConnector) get(ctx context.Context) (*tickerFeed, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.feed != nil && c.feed.connected() {
		return c.feed, nil
	}
	if c.feed != nil {
		c.feed.close()
		c.feed = nil
	}
	feed, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	feed.setNotify(c.notify)
	c.feed = feed
	return feed, nil
}

// setNotify sets the redraw function of this and later feeds.
func (c *tickerConnector) setNotify(notify func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify = notify
	if c.feed != nil {
		c.feed.setNotify(notify)
	}
}

func (c *tickerConnector) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.feed != nil {
		c.feed.close()
		c.feed = nil
	}
}

// tickerPortfolioLoader prices portfolios from the ticker while it runs,
// noting why when it doesn't.
func tickerPortfolioLoader(load portfolioLoader, connector *tickerConnector, every time.Duration) portfolioLoader {
	return func(ctx context.Context, creds credentials.Credentials) portfolio {
		result := load(ctx, creds)
		if result.prices == nil {
			return result
		}
		feed, err := connector.get(ctx)
		if err != nil {
			result.tickerErr = err
			return result
		}
		result.prices = newTickerPrices(feed, result.prices, every)
		// Subscribe now, so ticks arrive before the next refresh.
		feed.want(result.priceIDs()...)
		return result
	}
}

// tickerUnavailableError is why price mode ticker fell back to quotes.
type tickerUnavailableError struct{ err error }

func (e tickerUnavailableError) Error() string {
	return "ticker unavailable, using quotes: " + e.err.Error()
}

func (e tickerUnavailableError) Unwrap() error { return e.err }

// tickerQuoteLoader is load with prices from the ticker. When the ticker
// isn't running it returns load's quotes with a tickerUnavailableError.
func tickerQuoteLoader(load quoteLoader, connector *tickerConnector, every time.Duration) quoteLoader {
	return func(ctx context.Context) (quoteSource, string, error) {
		rest, label, err := load(ctx)
		if err != nil {
			rest = nil
		}
		feed, feedErr := connector.get(ctx)
		if feedErr != nil {
			if err != nil {
				return nil, "", err
			}
			return rest, label, tickerUnavailableError{err: feedErr}
		}
		if label == "" {
			label = "TICKER"
		} else {
			label = "TICKER " + label
		}
		return newTickerQuotes(feed, rest, every), label, nil
	}
}

// localTicker connects to ticker-server through the NATS WebSocket at
// wsURL, after its health endpoint at healthURL reports it running.
func localTicker(wsURL, healthURL, topic string) func(context.Context) (*tickerFeed, error) {
	return func(ctx context.Context) (*tickerFeed, error) {
		if err := checkTickerHealth(ctx, healthURL); err != nil {
			return nil, err
		}
		conn, err := nats.Connect(wsURL,
			nats.Name("kite-cli"),
			nats.Timeout(tickerConnectTimeout),
			nats.MaxReconnects(-1),
		)
		if err != nil {
			return nil, fmt.Errorf("connect to %s: %w", wsURL, err)
		}
		return newTickerFeed(natsBus{conn: conn}, topic), nil
	}
}

func checkTickerHealth(ctx context.Context, healthURL string) error {
	ctx, cancel := context.WithTimeout(ctx, tickerConnectTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("ticker-server isn't running at %s", healthURL)
	}
	defer response.Body.Close()
	var health struct {
		Running bool `json:"running"`
	}
	_ = json.NewDecoder(response.Body).Decode(&health)
	if !health.Running {
		return errors.New("ticker-server is up but not streaming yet")
	}
	return nil
}

// natsBus is a tickBus over a NATS connection.
type natsBus struct{ conn *nats.Conn }

func (b natsBus) Publish(subject string, data []byte) error {
	return b.conn.Publish(subject, data)
}

func (b natsBus) Subscribe(subject string, handler func([]byte)) (func() error, error) {
	subscription, err := b.conn.Subscribe(subject, func(message *nats.Msg) { handler(message.Data) })
	if err != nil {
		return nil, err
	}
	return subscription.Unsubscribe, nil
}

func (b natsBus) Connected() bool { return b.conn.IsConnected() }

func (b natsBus) Close() { b.conn.Close() }
