// Package livecandles folds the ticker's ticks into live 1m candles and
// publishes them for the instruments candle heartbeats ask for.
//
// A candle heartbeat is JSON like the tick heartbeat, with a mode:
//
//	{"ids": ["NSE:INFY"], "mode": "periodic", "interval_seconds": 5}
//
// Each mode publishes on its own subject, so subscribers in different modes
// never see each other's candles:
//
//	candles.<exchange>.<symbol>.continuous   every change to the open candle
//	candles.<exchange>.<symbol>.periodic.<n> the open candle every n seconds, if it changed
//	candles.<exchange>.<symbol>.on_complete  only closed candles
//
// Every mode also gets each closed candle once, with final set. A minute's
// candle closes when a tick from a later minute arrives, or Grace after the
// minute ends; ticks for a minute that has closed are dropped.
package livecandles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
)

// Mode is how often a candle subscription is published to.
type Mode string

const (
	// ModeContinuous publishes every change to the open candle.
	ModeContinuous Mode = "continuous"
	// ModePeriodic publishes the open candle every interval, if it changed.
	ModePeriodic Mode = "periodic"
	// ModeOnComplete publishes only closed candles.
	ModeOnComplete Mode = "on_complete"
)

// MaxIntervalSeconds is the longest periodic interval, a candle's length.
const MaxIntervalSeconds = 60

// maxPendingFinals is how many closed candles a subscription keeps while
// publishing fails, oldest dropped first.
const maxPendingFinals = 10

// Candle is a live 1m candle as it is published. UpdatedAt is the exchange
// time of the last tick folded into it, and Final says it has closed.
type Candle struct {
	candles.Candle
	Final     bool      `json:"final"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Publisher sends a payload on a subject; *nats.Conn is one.
type Publisher interface {
	Publish(subject string, payload []byte) error
}

// TickSubscriber keeps instruments' ticks coming, as a tick heartbeat would.
type TickSubscriber interface {
	Refresh(ids []string) error
}

// Config controls candle subscription expiry and closing.
type Config struct {
	// TTL is how long a subscription lives without a heartbeat; 1m when
	// unset.
	TTL time.Duration
	// Grace is how long after a minute ends its candle waits for late ticks
	// before it closes without one from the next minute; 2s when unset.
	Grace time.Duration
}

// Subscription is the health representation of one candle subscription.
type Subscription struct {
	ID              string    `json:"id"`
	Mode            Mode      `json:"mode"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	Subject         string    `json:"subject"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	LastPublishedAt time.Time `json:"last_published_at,omitempty"`
}

type heartbeatMessage struct {
	IDs             []string `json:"ids"`
	Mode            Mode     `json:"mode"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
}

type key struct {
	id     string
	mode   Mode
	period int
}

// builder is one instrument's candle in the making.
type builder struct {
	candle    candles.Candle
	open      bool
	version   uint64
	updatedAt time.Time
	// closed is the start of the last minute that closed; its ticks are late.
	closed time.Time
	// baseVolume is the day's cumulative volume before the open candle, and
	// lastVolume the cumulative volume at the last tick.
	baseVolume, lastVolume int64
	lastTickAt             time.Time
}

type subscription struct {
	key             key
	subject         string
	lastHeartbeatAt time.Time
	expiresAt       time.Time
	nextAt          time.Time
	published       uint64
	finals          []Candle
	lastPublishedAt time.Time
}

type outgoing struct {
	key     key
	subject string
	candle  Candle
	version uint64
}

// Engine owns the live candles and their subscriptions.
type Engine struct {
	config    Config
	publisher Publisher
	ticks     TickSubscriber
	logger    *slog.Logger
	now       func() time.Time

	mu            sync.Mutex
	builders      map[string]*builder
	subscriptions map[key]*subscription
	wake          chan struct{}
	running       atomic.Bool
	lateTicks     atomic.Uint64

	startMu sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

// New constructs an engine that publishes with publisher and keeps its
// instruments subscribed through ticks.
func New(config Config, publisher Publisher, ticks TickSubscriber, logger *slog.Logger) (*Engine, error) {
	if publisher == nil || ticks == nil {
		return nil, errors.New("live candles: publisher and tick subscriber are required")
	}
	if config.TTL <= 0 {
		config.TTL = time.Minute
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		config:        config,
		publisher:     publisher,
		ticks:         ticks,
		logger:        logger,
		now:           time.Now,
		builders:      make(map[string]*builder),
		subscriptions: make(map[key]*subscription),
		wake:          make(chan struct{}, 1),
	}, nil
}

// Subject is the subject a subscription to id in mode, every interval
// seconds for periodic, is published on.
func Subject(id string, mode Mode, intervalSeconds int) (string, error) {
	subject, err := producer.InstrumentSubject("candles", id)
	if err != nil {
		return "", err
	}
	if mode == ModePeriodic {
		return subject + "." + string(mode) + "." + strconv.Itoa(intervalSeconds), nil
	}
	return subject + "." + string(mode), nil
}

// HandleHeartbeat validates a candle heartbeat, refreshes its
// subscriptions, and keeps their instruments' ticks coming.
func (e *Engine) HandleHeartbeat(payload []byte) error {
	var heartbeat heartbeatMessage
	if err := json.Unmarshal(payload, &heartbeat); err != nil {
		return fmt.Errorf("decode candle heartbeat: %w", err)
	}
	period := 0
	switch heartbeat.Mode {
	case ModeContinuous, ModeOnComplete:
	case ModePeriodic:
		period = heartbeat.IntervalSeconds
		if period <= 0 || period > MaxIntervalSeconds {
			return fmt.Errorf("candle heartbeat: periodic interval_seconds must be 1 to %d, got %d", MaxIntervalSeconds, period)
		}
	default:
		return fmt.Errorf("candle heartbeat: mode must be %s, %s or %s, got %q", ModeContinuous, ModePeriodic, ModeOnComplete, heartbeat.Mode)
	}
	now := e.now().UTC()
	ids := make([]string, 0, len(heartbeat.IDs))
	seen := make(map[string]struct{}, len(heartbeat.IDs))
	var invalid []error
	e.mu.Lock()
	for _, rawID := range heartbeat.IDs {
		parsed, err := models.ParseInstrumentID(rawID)
		if err != nil {
			invalid = append(invalid, err)
			continue
		}
		id := string(parsed)
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		k := key{id: id, mode: heartbeat.Mode, period: period}
		state := e.subscriptions[k]
		if state == nil {
			subject, err := Subject(id, heartbeat.Mode, period)
			if err != nil {
				invalid = append(invalid, err)
				continue
			}
			state = &subscription{key: k, subject: subject}
			if period > 0 {
				every := time.Duration(period) * time.Second
				state.nextAt = now.Truncate(every).Add(every)
			}
			e.subscriptions[k] = state
		}
		ids = append(ids, id)
		state.lastHeartbeatAt = now
		state.expiresAt = now.Add(e.config.TTL)
	}
	e.mu.Unlock()
	if len(ids) > 0 {
		invalid = append(invalid, e.ticks.Refresh(ids))
	}
	return errors.Join(invalid...)
}

// Fold folds a tick into its instrument's candle. It is the producer's
// TickObserver, so it never blocks.
func (e *Engine) Fold(tick producer.Tick) {
	if !tick.InCandle() {
		return
	}
	at := tick.Timestamp.In(models.ExchangeLocation)
	minute := at.Truncate(time.Minute)
	volume := int64(tick.VolumeTraded)

	e.mu.Lock()
	b := e.builders[tick.ID]
	if b == nil {
		b = &builder{}
		e.builders[tick.ID] = b
	}
	if b.open && minute.After(b.candle.Timestamp) {
		e.close(tick.ID, b)
	}
	if (b.open && minute.Before(b.candle.Timestamp)) || (!b.open && !b.closed.IsZero() && !minute.After(b.closed)) {
		e.mu.Unlock()
		e.lateTicks.Add(1)
		return
	}
	if !b.open {
		// A candle's volume is what traded since the last tick before it,
		// or since its first tick when it opens the day.
		base := volume
		if !b.lastTickAt.IsZero() && sameDay(b.lastTickAt, at) && b.lastVolume <= volume {
			base = b.lastVolume
		}
		b.candle = candles.Candle{
			InstrumentID: models.InstrumentID(tick.ID), InstrumentToken: int64(tick.InstrumentToken),
			Interval: models.CandleInterval1m, Timestamp: minute,
			Open: tick.LastPrice, High: tick.LastPrice, Low: tick.LastPrice, Close: tick.LastPrice,
		}
		b.baseVolume = base
		b.open = true
	} else {
		b.candle.High = max(b.candle.High, tick.LastPrice)
		b.candle.Low = min(b.candle.Low, tick.LastPrice)
		b.candle.Close = tick.LastPrice
	}
	b.candle.Volume = int(max(volume-b.baseVolume, 0))
	b.candle.OI = int(tick.OI)
	b.lastVolume = volume
	b.lastTickAt = at
	b.updatedAt = at
	b.version++
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// close queues an open candle's final for every subscription to it. e.mu
// must be held.
func (e *Engine) close(id string, b *builder) {
	final := Candle{Candle: b.candle, Final: true, UpdatedAt: b.updatedAt}
	for k, state := range e.subscriptions {
		if k.id != id {
			continue
		}
		if len(state.finals) == maxPendingFinals {
			e.logger.Warn("dropping unpublished live candle", "subject", state.subject, "minute", state.finals[0].Timestamp)
			state.finals = state.finals[1:]
		}
		state.finals = append(state.finals, final)
	}
	b.open = false
	b.closed = b.candle.Timestamp
}

// Start runs the engine's closing, publishing and expiry until ctx is done
// or Close is called.
func (e *Engine) Start(parent context.Context) error {
	e.startMu.Lock()
	defer e.startMu.Unlock()
	if e.cancel != nil {
		return errors.New("live candles: engine is already running")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	e.cancel = cancel
	e.done = make(chan struct{})
	e.running.Store(true)
	go func() {
		defer close(e.done)
		defer e.running.Store(false)
		e.run(ctx)
	}()
	return nil
}

// Running reports whether the engine's loop is active.
func (e *Engine) Running() bool { return e.running.Load() }

// LateTicks counts ticks dropped because their minute had closed.
func (e *Engine) LateTicks() uint64 { return e.lateTicks.Load() }

// Close stops the engine after a last publish and waits for its loop.
func (e *Engine) Close(ctx context.Context) error {
	e.startMu.Lock()
	cancel, done := e.cancel, e.done
	e.cancel = nil
	e.startMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Subscriptions returns a sorted health snapshot.
func (e *Engine) Subscriptions() []Subscription {
	e.mu.Lock()
	result := make([]Subscription, 0, len(e.subscriptions))
	for _, state := range e.subscriptions {
		result = append(result, Subscription{
			ID: state.key.id, Mode: state.key.mode, IntervalSeconds: state.key.period, Subject: state.subject,
			LastHeartbeatAt: state.lastHeartbeatAt, ExpiresAt: state.expiresAt, LastPublishedAt: state.lastPublishedAt,
		})
	}
	e.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Subject < result[j].Subject })
	return result
}

func (e *Engine) run(ctx context.Context) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			e.publish(e.collect(e.now().UTC()))
			return
		case <-e.wake:
		case <-tick.C:
		}
		now := e.now().UTC()
		e.expire(now)
		e.publish(e.collect(now))
	}
}

// collect closes candles past their grace and returns what is due to
// publish, finals first.
func (e *Engine) collect(now time.Time) []outgoing {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, b := range e.builders {
		if b.open && !now.Before(b.candle.Timestamp.Add(time.Minute+e.config.Grace)) {
			e.close(id, b)
		}
	}
	var due []outgoing
	for k, state := range e.subscriptions {
		for _, final := range state.finals {
			due = append(due, outgoing{key: k, subject: state.subject, candle: final})
		}
		switch k.mode {
		case ModeContinuous:
		case ModePeriodic:
			if now.Before(state.nextAt) {
				continue
			}
			every := time.Duration(k.period) * time.Second
			state.nextAt = now.Truncate(every).Add(every)
		default:
			continue
		}
		b := e.builders[k.id]
		if b == nil || !b.open || b.version == state.published {
			continue
		}
		due = append(due, outgoing{key: k, subject: state.subject, version: b.version,
			candle: Candle{Candle: b.candle, UpdatedAt: b.updatedAt}})
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].subject < due[j].subject })
	return due
}

// publish sends what collect returned. A failed final stays queued for
// the next round; a failed open candle waits for the next change or period.
func (e *Engine) publish(due []outgoing) {
	for _, item := range due {
		payload, err := json.Marshal(item.candle)
		if err == nil {
			err = e.publisher.Publish(item.subject, payload)
		}
		if err != nil {
			e.logger.Warn("publish live candle failed", "subject", item.subject, "error", err)
			continue
		}
		e.mu.Lock()
		if state := e.subscriptions[item.key]; state != nil {
			state.lastPublishedAt = e.now().UTC()
			if item.candle.Final {
				for index, final := range state.finals {
					if final.Timestamp.Equal(item.candle.Timestamp) {
						state.finals = append(state.finals[:index], state.finals[index+1:]...)
						break
					}
				}
			} else {
				state.published = item.version
			}
		}
		e.mu.Unlock()
	}
}

func (e *Engine) expire(now time.Time) {
	e.mu.Lock()
	for k, state := range e.subscriptions {
		if !now.Before(state.expiresAt) {
			delete(e.subscriptions, k)
		}
	}
	e.mu.Unlock()
}

func sameDay(a, b time.Time) bool {
	a, b = a.In(models.ExchangeLocation), b.In(models.ExchangeLocation)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
