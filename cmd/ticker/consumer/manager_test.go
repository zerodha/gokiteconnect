package consumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

type fakeResolver map[models.InstrumentID]int64

func (r fakeResolver) Token(id models.InstrumentID) (int64, bool) {
	token, ok := r[id]
	return token, ok
}

type fakeIndexProvider struct {
	mu       sync.Mutex
	resolver TokenResolver
	err      error
	calls    int
}

func (p *fakeIndexProvider) Snapshot(context.Context) (TokenResolver, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.resolver, p.err
}

type fakeTarget struct {
	mu        sync.Mutex
	mappings  map[string]uint32
	events    []string
	upsertErr error
	removeErr error
	realAt    time.Time
	publishAt time.Time
}

func newFakeTarget() *fakeTarget { return &fakeTarget{mappings: make(map[string]uint32)} }

func (t *fakeTarget) Upsert(_ context.Context, id string, token uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, "upsert:"+id)
	if t.upsertErr != nil {
		return t.upsertErr
	}
	t.mappings[id] = token
	return nil
}

func (t *fakeTarget) Remove(_ context.Context, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, "remove:"+id)
	delete(t.mappings, id)
	return t.removeErr
}

func (t *fakeTarget) Activity(string) (time.Time, time.Time) { return t.realAt, t.publishAt }

func TestHeartbeatCanonicalizesDeduplicatesAndMapsFullMode(t *testing.T) {
	base := time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC)
	provider := &fakeIndexProvider{resolver: fakeResolver{"NSE:INFY": 123}}
	target := newFakeTarget()
	manager, err := NewManager(Config{TTL: time.Minute}, provider, target, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return base }
	manager.resolver = provider.resolver

	err = manager.HandleHeartbeat([]byte(`{"ids":[" nse:infy ","NSE:INFY","bad"],"mode":"ltp"}`))
	if err == nil {
		t.Fatal("invalid ID was not reported")
	}
	manager.reconcile(context.Background(), false)
	subscriptions := manager.Subscriptions()
	if len(subscriptions) != 1 {
		t.Fatalf("subscriptions=%+v", subscriptions)
	}
	got := subscriptions[0]
	if got.ID != "NSE:INFY" || got.Mode != "full" || got.InstrumentToken != 123 {
		t.Fatalf("subscription=%+v", got)
	}
	if !got.LastHeartbeatAt.Equal(base) || !got.ExpiresAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("heartbeat times=%+v", got)
	}
}

func TestRefreshFailureRetainsExistingMapping(t *testing.T) {
	provider := &fakeIndexProvider{resolver: fakeResolver{"NSE:INFY": 123}}
	target := newFakeTarget()
	manager, _ := NewManager(Config{TTL: time.Minute}, provider, target, testLogger())
	manager.resolver = provider.resolver
	if err := manager.HandleHeartbeat([]byte(`{"ids":["NSE:INFY"]}`)); err != nil {
		t.Fatal(err)
	}
	manager.reconcile(context.Background(), false)

	provider.mu.Lock()
	provider.err = errors.New("database unavailable")
	provider.mu.Unlock()
	manager.reconcile(context.Background(), true)
	got := manager.Subscriptions()[0]
	if got.InstrumentToken != 123 || got.MappingError != "instrument catalog unavailable" {
		t.Fatalf("subscription=%+v", got)
	}
	if target.mappings["NSE:INFY"] != 123 {
		t.Fatalf("target mappings=%v", target.mappings)
	}
}

func TestExpiryRemovesSubscriptionsInSortedOrder(t *testing.T) {
	base := time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC)
	provider := &fakeIndexProvider{resolver: fakeResolver{"NSE:TCS": 2, "NSE:INFY": 1}}
	target := newFakeTarget()
	manager, _ := NewManager(Config{TTL: time.Second}, provider, target, testLogger())
	manager.resolver = provider.resolver
	manager.now = func() time.Time { return base }
	if err := manager.HandleHeartbeat([]byte(`{"ids":["NSE:TCS","NSE:INFY"]}`)); err != nil {
		t.Fatal(err)
	}
	manager.reconcile(context.Background(), false)
	manager.now = func() time.Time { return base.Add(time.Second) }
	manager.expire(context.Background())
	if got := manager.Subscriptions(); len(got) != 0 {
		t.Fatalf("subscriptions=%+v", got)
	}
	target.mu.Lock()
	events := append([]string(nil), target.events...)
	target.mu.Unlock()
	wantSuffix := []string{"remove:NSE:INFY", "remove:NSE:TCS"}
	if !reflect.DeepEqual(events[len(events)-2:], wantSuffix) {
		t.Fatalf("events=%v", events)
	}
}

func TestManagerLifecycleAndActivitySnapshot(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	provider := &fakeIndexProvider{resolver: fakeResolver{"NSE:INFY": 123}}
	target := newFakeTarget()
	target.realAt = base
	target.publishAt = base.Add(time.Second)
	manager, _ := NewManager(Config{TTL: time.Minute, MappingRefreshInterval: time.Hour}, provider, target, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !manager.Running() {
		t.Fatal("manager is not running")
	}
	if err := manager.HandleHeartbeat([]byte(`{"ids":["NSE:INFY"]}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return len(manager.Subscriptions()) == 1 && manager.Subscriptions()[0].InstrumentToken == 123
	})
	got := manager.Subscriptions()[0]
	if !got.LastRealTickAt.Equal(base) || !got.LastPublishedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("activity=%+v", got)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if manager.Running() {
		t.Fatal("manager is still running")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		time.Sleep(time.Millisecond)
	}
}
