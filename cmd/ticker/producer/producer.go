package producer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/models"
)

type tickState struct {
	latest           Tick
	hasLatest        bool
	sequence         uint64
	dirty            bool
	replay           bool
	lastReplayQueued time.Time
	lastRealTickAt   time.Time
	lastPublishedAt  time.Time
}

type orderInput struct {
	receivedAt time.Time
	order      kiteconnect.Order
}

type counters struct {
	upstreamTicks, publishedTicks, replayedTicks, coalescedTicks atomic.Uint64
	unknownTokens, orderEvents, orderEnqueued, orderRetries      atomic.Uint64
	orderDelivered, socketReconnects, tokenRefreshes             atomic.Uint64
}

// Producer owns the authenticated Kite socket and all socket-to-NATS delivery.
type Producer struct {
	config   Config
	sessions SessionProvider
	sockets  SocketFactory
	bus      MessageBus
	logger   *slog.Logger
	now      func() time.Time

	ctx       context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
	startErr  error
	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup

	mu                   sync.RWMutex
	desired              map[string]uint32
	reverse              map[uint32]string
	ticks                map[string]*tickState
	socket               Socket
	socketGeneration     uint64
	socketConnected      bool
	sessionLoaded        bool
	sessionFingerprint   string
	lastOperationalError string

	socketMu      sync.Mutex
	tickWake      chan struct{}
	authFailure   chan uint64
	orders        chan orderInput
	orderInFlight atomic.Int64
	accepting     atomic.Bool
	closing       atomic.Bool
	counters      counters
}

// New creates a producer. Start must be called before it can connect.
func New(config Config, sessions SessionProvider, sockets SocketFactory, bus MessageBus, logger *slog.Logger) (*Producer, error) {
	if strings.TrimSpace(config.UserID) == "" {
		return nil, errors.New("ticker producer: user ID is required")
	}
	if sessions == nil || sockets == nil || bus == nil {
		return nil, errors.New("ticker producer: session provider, socket factory, and message bus are required")
	}
	if config.TokenPollInterval <= 0 {
		config.TokenPollInterval = 5 * time.Minute
	}
	if config.ReplayInterval <= 0 {
		config.ReplayInterval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Producer{
		config:      config,
		sessions:    sessions,
		sockets:     sockets,
		bus:         bus,
		logger:      logger,
		now:         time.Now,
		desired:     make(map[string]uint32),
		reverse:     make(map[uint32]string),
		ticks:       make(map[string]*tickState),
		tickWake:    make(chan struct{}, 1),
		authFailure: make(chan uint64, 1),
		orders:      make(chan orderInput, 1024),
	}, nil
}

// Start starts durable order delivery, session polling, the socket, and tick publication.
func (p *Producer) Start(parent context.Context) error {
	p.startOnce.Do(func() {
		if parent == nil {
			parent = context.Background()
		}
		p.ctx, p.cancel = context.WithCancel(parent)
		if err := p.bus.StartOrderDelivery(p.ctx, p.orderDeliveryRetry, p.orderDelivered); err != nil {
			p.startErr = err
			p.cancel()
			return
		}
		p.accepting.Store(true)
		p.startLoop(p.sessionLoop)
		p.startLoop(p.tickLoop)
		p.startLoop(p.orderLoop)
	})
	return p.startErr
}

func (p *Producer) startLoop(loop func(context.Context)) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		loop(p.ctx)
	}()
}

// Upsert subscribes or remaps a canonical instrument ID. It implements the
// consumer's subscription-target contract.
func (p *Producer) Upsert(_ context.Context, id string, token uint32) error {
	if token == 0 {
		return errors.New("ticker producer: instrument token must be positive")
	}
	p.socketMu.Lock()
	defer p.socketMu.Unlock()

	p.mu.RLock()
	oldToken := p.desired[id]
	socket := p.socket
	connected := p.socketConnected && socket != nil
	p.mu.RUnlock()
	if oldToken == token {
		return nil
	}
	if connected {
		if err := socket.Subscribe([]uint32{token}); err != nil {
			return err
		}
	}

	p.mu.Lock()
	if oldToken != 0 {
		delete(p.reverse, oldToken)
	}
	p.desired[id] = token
	p.reverse[token] = id
	delete(p.ticks, id)
	p.mu.Unlock()

	if connected && oldToken != 0 {
		if err := socket.Unsubscribe([]uint32{oldToken}); err != nil {
			p.recordError("unsubscribe remapped token failed", err)
		}
	}
	return nil
}

// Remove forgets an instrument immediately and unsubscribes it upstream when connected.
func (p *Producer) Remove(_ context.Context, id string) error {
	p.socketMu.Lock()
	defer p.socketMu.Unlock()

	p.mu.Lock()
	token := p.desired[id]
	delete(p.desired, id)
	delete(p.reverse, token)
	delete(p.ticks, id)
	socket := p.socket
	connected := p.socketConnected && socket != nil
	p.mu.Unlock()
	if connected && token != 0 {
		return socket.Unsubscribe([]uint32{token})
	}
	return nil
}

// Activity returns tick activity for a subscribed instrument.
func (p *Producer) Activity(id string) (time.Time, time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	state := p.ticks[id]
	if state == nil {
		return time.Time{}, time.Time{}
	}
	return state.lastRealTickAt, state.lastPublishedAt
}

// Status returns the producer readiness components.
func (p *Producer) Status() Status {
	p.mu.RLock()
	status := Status{
		SessionLoaded:      p.sessionLoaded,
		SocketConnected:    p.socketConnected,
		NATSConnected:      p.bus.Connected(),
		StreamsReady:       p.bus.StreamsReady(),
		OrderDeliveryReady: p.bus.OrderDeliveryReady(),
	}
	p.mu.RUnlock()
	return status
}

func (p *Producer) sessionLoop(ctx context.Context) {
	force := false
	retryDelay := time.Second
	for {
		session, err := p.sessions.LoadSession(ctx, p.config.UserID, force)
		if err == nil {
			err = validateSession(session, p.config.UserID)
		}
		if err != nil {
			p.recordError("load ticker session failed", err)
			select {
			case <-ctx.Done():
				return
			case generation := <-p.authFailure:
				if p.isCurrentGeneration(generation) {
					force = true
				}
			case <-time.After(retryDelay):
			}
			if retryDelay < time.Minute {
				retryDelay *= 2
				if retryDelay > time.Minute {
					retryDelay = time.Minute
				}
			}
			continue
		}

		fingerprint := session.Fingerprint()
		p.mu.RLock()
		changed := !p.sessionLoaded || fingerprint != p.sessionFingerprint
		p.mu.RUnlock()
		if changed || force {
			if err := p.replaceSocket(session, fingerprint); err != nil {
				p.recordError("replace upstream socket failed", err)
			}
		}
		if force {
			p.counters.tokenRefreshes.Add(1)
		}
		force = false
		retryDelay = time.Second

		select {
		case <-ctx.Done():
			return
		case generation := <-p.authFailure:
			if p.isCurrentGeneration(generation) {
				force = true
			}
		case <-time.After(p.config.TokenPollInterval):
		}
	}
}

func validateSession(session Session, expectedUserID string) error {
	if session.UserID != expectedUserID {
		return fmt.Errorf("ticker credentials belong to %q, want %q", session.UserID, expectedUserID)
	}
	if session.IsAPIUser {
		if session.APIKey == "" || session.AccessToken == "" {
			return errors.New("API ticker session is incomplete")
		}
	} else if session.EncToken == "" {
		return errors.New("web ticker session has no encryption token")
	}
	return nil
}

func (p *Producer) replaceSocket(session Session, fingerprint string) error {
	p.socketMu.Lock()
	defer p.socketMu.Unlock()
	if p.closing.Load() {
		return context.Canceled
	}

	p.mu.RLock()
	old := p.socket
	p.mu.RUnlock()
	if old != nil {
		_ = old.Close()
	}

	p.mu.Lock()
	p.socketGeneration++
	generation := p.socketGeneration
	p.socketConnected = false
	p.sessionLoaded = true
	p.sessionFingerprint = fingerprint
	p.mu.Unlock()

	callbacks := SocketCallbacks{
		OnConnect:     func() { p.socketDidConnect(generation) },
		OnTick:        func(tick models.Tick) { p.socketTick(generation, tick) },
		OnOrder:       func(order kiteconnect.Order) { p.socketOrder(generation, order) },
		OnError:       func(err error) { p.socketError(generation, err) },
		OnReconnect:   func(attempt int, delay time.Duration) { p.socketReconnect(generation, attempt, delay) },
		OnClose:       func(code int, reason string) { p.socketClosed(generation, code, reason) },
		OnAuthFailure: func() { p.signalAuthFailure(generation) },
	}
	socket := p.sockets.New(session, callbacks)
	if socket == nil {
		return errors.New("socket factory returned nil")
	}
	p.mu.Lock()
	p.socket = socket
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		socket.Start(p.ctx)
		p.socketClosed(generation, 0, "socket stopped")
	}()
	return nil
}

func (p *Producer) socketDidConnect(generation uint64) {
	if p.closing.Load() {
		return
	}
	p.mu.Lock()
	if generation != p.socketGeneration {
		p.mu.Unlock()
		return
	}
	p.socketConnected = true
	p.mu.Unlock()
	p.logger.Info("upstream ticker connected", "user_id", p.config.UserID)
	go func() {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			p.restoreSubscriptions(generation)
		case <-p.ctx.Done():
		}
	}()
}

func (p *Producer) restoreSubscriptions(generation uint64) {
	p.socketMu.Lock()
	defer p.socketMu.Unlock()
	p.mu.RLock()
	if generation != p.socketGeneration || p.socket == nil || !p.socketConnected {
		p.mu.RUnlock()
		return
	}
	socket := p.socket
	tokens := make([]uint32, 0, len(p.desired))
	for _, token := range p.desired {
		tokens = append(tokens, token)
	}
	p.mu.RUnlock()
	sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
	for _, batch := range tokenBatches(tokens, 500) {
		if err := socket.Subscribe(batch); err != nil {
			p.recordError("restore upstream subscriptions failed", err)
			return
		}
	}
}

func (p *Producer) socketTick(generation uint64, tick models.Tick) {
	now := p.now().UTC()
	p.mu.Lock()
	if generation != p.socketGeneration {
		p.mu.Unlock()
		return
	}
	id, found := p.reverse[tick.InstrumentToken]
	if !found {
		p.mu.Unlock()
		p.counters.unknownTokens.Add(1)
		return
	}
	state := p.ticks[id]
	if state == nil {
		state = &tickState{}
		p.ticks[id] = state
	}
	if state.dirty {
		p.counters.coalescedTicks.Add(1)
	}
	state.sequence++
	state.latest = Tick{ID: id, Tick: tick}
	state.hasLatest = true
	state.dirty = true
	state.replay = false
	state.lastReplayQueued = time.Time{}
	state.lastRealTickAt = now
	p.mu.Unlock()
	p.counters.upstreamTicks.Add(1)
	p.signal(p.tickWake)
}

func (p *Producer) socketOrder(generation uint64, order kiteconnect.Order) {
	if !p.isCurrentGeneration(generation) || !p.accepting.Load() {
		return
	}
	if strings.TrimSpace(order.AccountID) == "" {
		order.AccountID = p.config.UserID
	}
	p.counters.orderEvents.Add(1)
	input := orderInput{receivedAt: p.now().UTC(), order: order}
	select {
	case p.orders <- input:
	case <-p.ctx.Done():
	}
}

func (p *Producer) socketError(generation uint64, err error) {
	if p.isCurrentGeneration(generation) {
		p.recordError("upstream socket error", err)
	}
}

func (p *Producer) socketReconnect(generation uint64, attempt int, delay time.Duration) {
	p.mu.Lock()
	if generation == p.socketGeneration {
		p.socketConnected = false
		p.counters.socketReconnects.Add(1)
	}
	p.mu.Unlock()
	p.logger.Info("upstream ticker reconnecting", "attempt", attempt, "delay", delay)
}

func (p *Producer) socketClosed(generation uint64, code int, reason string) {
	p.mu.Lock()
	if generation == p.socketGeneration {
		p.socketConnected = false
	}
	p.mu.Unlock()
	if code != 0 || reason != "socket stopped" {
		p.logger.Info("upstream ticker closed", "code", code, "reason", reason)
	}
}

func (p *Producer) signalAuthFailure(generation uint64) {
	if !p.isCurrentGeneration(generation) {
		return
	}
	select {
	case p.authFailure <- generation:
	default:
	}
}

func (p *Producer) tickLoop(ctx context.Context) {
	retry := time.NewTicker(200 * time.Millisecond)
	replay := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	defer replay.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.tickWake:
			p.publishDirtyTicks()
		case <-retry.C:
			p.publishDirtyTicks()
		case <-replay.C:
			p.markReplays()
		}
	}
}

type dirtyTick struct {
	id       string
	tick     Tick
	sequence uint64
	replay   bool
}

func (p *Producer) publishDirtyTicks() {
	if !p.bus.Connected() {
		return
	}
	p.mu.RLock()
	dirty := make([]dirtyTick, 0)
	for id, state := range p.ticks {
		if state.dirty && state.hasLatest {
			dirty = append(dirty, dirtyTick{id: id, tick: state.latest, sequence: state.sequence, replay: state.replay})
		}
	}
	p.mu.RUnlock()
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].id < dirty[j].id })
	for _, pending := range dirty {
		if err := p.bus.PublishTick(pending.tick); err != nil {
			p.recordError("NATS tick publish failed", err)
			return
		}
		now := p.now().UTC()
		p.mu.Lock()
		if state := p.ticks[pending.id]; state != nil {
			state.lastPublishedAt = now
			if state.sequence == pending.sequence {
				state.dirty = false
				state.replay = false
			}
		}
		p.mu.Unlock()
		p.counters.publishedTicks.Add(1)
		if pending.replay {
			p.counters.replayedTicks.Add(1)
		}
	}
}

func (p *Producer) markReplays() {
	now := p.now().UTC()
	marked := false
	p.mu.Lock()
	for _, state := range p.ticks {
		if !state.hasLatest || now.Sub(state.lastRealTickAt) < p.config.ReplayInterval {
			continue
		}
		if !state.lastReplayQueued.IsZero() && now.Sub(state.lastReplayQueued) < p.config.ReplayInterval {
			continue
		}
		if state.dirty {
			p.counters.coalescedTicks.Add(1)
		}
		state.dirty = true
		state.replay = true
		state.lastReplayQueued = now
		marked = true
	}
	p.mu.Unlock()
	if marked {
		p.signal(p.tickWake)
	}
}

func (p *Producer) orderLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case input := <-p.orders:
			p.orderInFlight.Add(1)
			p.enqueueOrder(ctx, input)
			p.orderInFlight.Add(-1)
		}
	}
}

func (p *Producer) enqueueOrder(ctx context.Context, input orderInput) {
	event, err := newOrderEvent(p.config.UserID, input.receivedAt, input.order)
	if err != nil {
		p.recordError("encode upstream order event failed", err)
		return
	}
	delay := time.Second
	for {
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := p.bus.EnqueueOrder(publishCtx, event)
		cancel()
		if err == nil {
			p.counters.orderEnqueued.Add(1)
			return
		}
		p.recordError("enqueue order event failed", err)
		p.counters.orderRetries.Add(1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
			if delay > time.Minute {
				delay = time.Minute
			}
		}
	}
}

func (p *Producer) orderDeliveryRetry(err error) {
	p.counters.orderRetries.Add(1)
	p.recordError("durable order delivery failed", err)
}

func (p *Producer) orderDelivered() {
	p.counters.orderDelivered.Add(1)
}

// Close stops ingress, drains accepted work, flushes NATS, and releases resources.
func (p *Producer) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		p.closing.Store(true)
		p.accepting.Store(false)

		p.socketMu.Lock()
		p.mu.RLock()
		socket := p.socket
		p.mu.RUnlock()
		if socket != nil {
			p.closeErr = errors.Join(p.closeErr, socket.Close())
		}
		p.mu.Lock()
		p.socketConnected = false
		p.mu.Unlock()
		p.socketMu.Unlock()

		drainTicker := time.NewTicker(10 * time.Millisecond)
		defer drainTicker.Stop()
		for len(p.orders) > 0 || p.orderInFlight.Load() > 0 {
			select {
			case <-ctx.Done():
				p.closeErr = errors.Join(p.closeErr, ctx.Err())
				goto stop
			case <-drainTicker.C:
			}
		}
		p.publishDirtyTicks()
	stop:
		if p.cancel != nil {
			p.cancel()
		}
		waitDone := make(chan struct{})
		go func() {
			p.wg.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-ctx.Done():
			p.closeErr = errors.Join(p.closeErr, ctx.Err())
		}
		p.closeErr = errors.Join(p.closeErr, p.bus.Flush(ctx), p.bus.Close())
	})
	return p.closeErr
}

func (p *Producer) isCurrentGeneration(generation uint64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return generation == p.socketGeneration
}

func (p *Producer) recordError(message string, err error) {
	p.mu.Lock()
	p.lastOperationalError = message
	p.mu.Unlock()
	p.logger.Warn(message, "user_id", p.config.UserID, "error", errorString(err))
}

func (p *Producer) signal(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func tokenBatches(tokens []uint32, size int) [][]uint32 {
	if len(tokens) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(tokens)
	}
	result := make([][]uint32, 0, (len(tokens)+size-1)/size)
	for len(tokens) > 0 {
		length := size
		if len(tokens) < length {
			length = len(tokens)
		}
		result = append(result, tokens[:length])
		tokens = tokens[length:]
	}
	return result
}
