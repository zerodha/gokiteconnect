package producer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/models"
)

type fakeBus struct {
	mu          sync.Mutex
	published   []Tick
	enqueued    []OrderEvent
	publishErr  error
	enqueueErr  error
	connected   atomic.Bool
	streams     atomic.Bool
	delivery    atomic.Bool
	closed      atomic.Bool
	onRetry     func(error)
	onDelivered func()
}

func newFakeBus() *fakeBus {
	bus := &fakeBus{}
	bus.connected.Store(true)
	bus.streams.Store(true)
	return bus
}

func (b *fakeBus) StartOrderDelivery(_ context.Context, retry func(error), delivered func()) error {
	b.onRetry = retry
	b.onDelivered = delivered
	b.delivery.Store(true)
	return nil
}
func (b *fakeBus) PublishTick(tick Tick) error {
	if b.publishErr != nil {
		return b.publishErr
	}
	b.mu.Lock()
	b.published = append(b.published, tick)
	b.mu.Unlock()
	return nil
}
func (b *fakeBus) EnqueueOrder(_ context.Context, event OrderEvent) error {
	if b.enqueueErr != nil {
		return b.enqueueErr
	}
	b.mu.Lock()
	b.enqueued = append(b.enqueued, event)
	b.mu.Unlock()
	return nil
}
func (b *fakeBus) Connected() bool             { return b.connected.Load() }
func (b *fakeBus) StreamsReady() bool          { return b.streams.Load() }
func (b *fakeBus) OrderDeliveryReady() bool    { return b.delivery.Load() }
func (b *fakeBus) Flush(context.Context) error { return nil }
func (b *fakeBus) Close() error {
	b.closed.Store(true)
	b.delivery.Store(false)
	return nil
}

type fakeSocket struct {
	callbacks SocketCallbacks
	mu        sync.Mutex
	events    []string
	done      chan struct{}
	closeOnce sync.Once
	connected atomic.Bool
}

func newFakeSocket(callbacks SocketCallbacks) *fakeSocket {
	return &fakeSocket{callbacks: callbacks, done: make(chan struct{})}
}

func (s *fakeSocket) Start(ctx context.Context) {
	s.connected.Store(true)
	if s.callbacks.OnConnect != nil {
		s.callbacks.OnConnect()
	}
	select {
	case <-ctx.Done():
	case <-s.done:
	}
	s.connected.Store(false)
}
func (s *fakeSocket) Subscribe(tokens []uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range tokens {
		s.events = append(s.events, event("subscribe", token))
	}
	return nil
}
func (s *fakeSocket) Unsubscribe(tokens []uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range tokens {
		s.events = append(s.events, event("unsubscribe", token))
	}
	return nil
}
func (s *fakeSocket) Connected() bool { return s.connected.Load() }
func (s *fakeSocket) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type fakeSocketFactory struct {
	mu      sync.Mutex
	sockets []*fakeSocket
}

func (f *fakeSocketFactory) New(_ Session, callbacks SocketCallbacks) Socket {
	socket := newFakeSocket(callbacks)
	f.mu.Lock()
	f.sockets = append(f.sockets, socket)
	f.mu.Unlock()
	return socket
}

func (f *fakeSocketFactory) snapshot() []*fakeSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeSocket(nil), f.sockets...)
}

type fakeSessions struct {
	mu     sync.Mutex
	forces []bool
	serial int
}

func (s *fakeSessions) LoadSession(_ context.Context, userID string, force bool) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forces = append(s.forces, force)
	if force {
		s.serial++
	}
	return Session{UserID: userID, EncToken: event("enc", uint32(s.serial+1))}, nil
}

func (s *fakeSessions) forced() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, force := range s.forces {
		if force {
			count++
		}
	}
	return count
}

func TestUpsertSubscribesReplacementBeforeUnsubscribingOldToken(t *testing.T) {
	bus := newFakeBus()
	p, _ := New(Config{UserID: "U1"}, &fakeSessions{}, &fakeSocketFactory{}, bus, discardLogger())
	socket := newFakeSocket(SocketCallbacks{})
	socket.connected.Store(true)
	p.socket = socket
	p.socketConnected = true
	if err := p.Upsert(context.Background(), "NSE:INFY", 100); err != nil {
		t.Fatal(err)
	}
	if err := p.Upsert(context.Background(), "NSE:INFY", 200); err != nil {
		t.Fatal(err)
	}
	socket.mu.Lock()
	events := append([]string(nil), socket.events...)
	socket.mu.Unlock()
	want := []string{"subscribe:100", "subscribe:200", "unsubscribe:100"}
	if len(events) != len(want) {
		t.Fatalf("events=%v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
}

func TestLatestTickWinsAndReplaysAfterQuietInterval(t *testing.T) {
	base := time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC)
	now := base
	bus := newFakeBus()
	bus.connected.Store(false)
	p, _ := New(Config{UserID: "U1", ReplayInterval: time.Second}, &fakeSessions{}, &fakeSocketFactory{}, bus, discardLogger())
	p.now = func() time.Time { return now }
	p.socketGeneration = 1
	p.desired["NSE:INFY"] = 123
	p.reverse[123] = "NSE:INFY"
	p.socketTick(1, models.Tick{InstrumentToken: 123, LastPrice: 100})
	p.socketTick(1, models.Tick{InstrumentToken: 123, LastPrice: 101})
	p.publishDirtyTicks()
	if len(bus.published) != 0 {
		t.Fatalf("published while disconnected=%v", bus.published)
	}
	bus.connected.Store(true)
	p.publishDirtyTicks()
	if len(bus.published) != 1 || bus.published[0].LastPrice != 101 {
		t.Fatalf("published=%+v", bus.published)
	}
	now = base.Add(time.Second)
	p.markReplays()
	p.publishDirtyTicks()
	if len(bus.published) != 2 || bus.published[1].LastPrice != 101 {
		t.Fatalf("replayed=%+v", bus.published)
	}
	realAt, publishedAt := p.Activity("NSE:INFY")
	if !realAt.Equal(base) || !publishedAt.Equal(now) {
		t.Fatalf("activity real=%s published=%s", realAt, publishedAt)
	}
}

func TestStaleSocketCallbacksAreIgnored(t *testing.T) {
	bus := newFakeBus()
	p, _ := New(Config{UserID: "U1"}, &fakeSessions{}, &fakeSocketFactory{}, bus, discardLogger())
	p.socketGeneration = 2
	p.desired["NSE:INFY"] = 123
	p.reverse[123] = "NSE:INFY"
	p.socketTick(1, models.Tick{InstrumentToken: 123, LastPrice: 100})
	p.socketOrder(1, kiteconnect.Order{AccountID: "U1", OrderID: "O1"})
	if len(p.ticks) != 0 || len(p.orders) != 0 {
		t.Fatalf("stale callbacks changed state: ticks=%v orders=%d", p.ticks, len(p.orders))
	}
}

func TestAuthenticationFailureRefreshesAndReplacesSocket(t *testing.T) {
	bus := newFakeBus()
	sessions := &fakeSessions{}
	factory := &fakeSocketFactory{}
	p, _ := New(Config{UserID: "U1", TokenPollInterval: time.Hour}, sessions, factory, bus, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitForProducer(t, func() bool { return len(factory.snapshot()) == 1 && p.Status().Ready() })
	first := factory.snapshot()[0]
	first.callbacks.OnAuthFailure()
	waitForProducer(t, func() bool { return sessions.forced() > 0 && len(factory.snapshot()) == 2 })
	select {
	case <-first.done:
	default:
		t.Fatal("old socket was not closed")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := p.Close(closeCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !bus.closed.Load() {
		t.Fatal("message bus was not closed")
	}
}

func TestCloseDrainsAcceptedOrderBeforeClosingBus(t *testing.T) {
	bus := newFakeBus()
	factory := &fakeSocketFactory{}
	p, _ := New(Config{UserID: "U1", TokenPollInterval: time.Hour}, &fakeSessions{}, factory, bus, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitForProducer(t, func() bool { return len(factory.snapshot()) == 1 && p.Status().Ready() })
	factory.snapshot()[0].callbacks.OnOrder(kiteconnect.Order{AccountID: "U1", OrderID: "O1"})
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := p.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	bus.mu.Lock()
	enqueued := append([]OrderEvent(nil), bus.enqueued...)
	bus.mu.Unlock()
	if len(enqueued) != 1 || enqueued[0].Order.OrderID != "O1" {
		t.Fatalf("enqueued=%+v", enqueued)
	}
	if !bus.closed.Load() {
		t.Fatal("bus was not closed after draining")
	}
}

func event(prefix string, token uint32) string {
	return prefix + ":" + fmtUint(token)
}

func fmtUint(value uint32) string {
	if value == 0 {
		return "0"
	}
	buffer := [10]byte{}
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

func waitForProducer(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		time.Sleep(time.Millisecond)
	}
}
