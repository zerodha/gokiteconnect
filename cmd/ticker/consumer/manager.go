package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

// TokenResolver resolves canonical instrument IDs in one catalog snapshot.
type TokenResolver interface {
	Token(models.InstrumentID) (int64, bool)
}

// IndexProvider returns a current local instrument-token snapshot.
type IndexProvider interface {
	Snapshot(context.Context) (TokenResolver, error)
}

// SubscriptionTarget applies desired ID/token mappings to a ticker producer.
type SubscriptionTarget interface {
	Upsert(context.Context, string, uint32) error
	Remove(context.Context, string) error
	Activity(string) (lastRealTickAt, lastPublishedAt time.Time)
}

// Config controls heartbeat expiry and instrument mapping refresh.
type Config struct {
	TTL                    time.Duration
	MappingRefreshInterval time.Duration
}

// Subscription is the detailed health representation of one desired instrument.
type Subscription struct {
	ID              string    `json:"id"`
	InstrumentToken uint32    `json:"instrument_token,omitempty"`
	Mode            string    `json:"mode"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	MappedAt        time.Time `json:"mapped_at,omitempty"`
	LastRealTickAt  time.Time `json:"last_real_tick_at,omitempty"`
	LastPublishedAt time.Time `json:"last_published_at,omitempty"`
	MappingError    string    `json:"mapping_error,omitempty"`
}

type heartbeatMessage struct {
	IDs       []string   `json:"ids"`
	Mode      *string    `json:"mode,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

type subscriptionState struct {
	status Subscription
}

// Manager owns desired subscriptions, their heartbeat TTL, and ID/token mapping.
type Manager struct {
	config  Config
	indexes IndexProvider
	target  SubscriptionTarget
	logger  *slog.Logger
	now     func() time.Time

	mu            sync.RWMutex
	resolver      TokenResolver
	subscriptions map[string]*subscriptionState
	reconcileWake chan struct{}
	running       atomic.Bool

	startMu sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewManager constructs a subscription manager.
func NewManager(config Config, indexes IndexProvider, target SubscriptionTarget, logger *slog.Logger) (*Manager, error) {
	if indexes == nil || target == nil {
		return nil, errors.New("ticker consumer: index provider and subscription target are required")
	}
	if config.TTL <= 0 {
		config.TTL = time.Minute
	}
	if config.MappingRefreshInterval <= 0 {
		config.MappingRefreshInterval = 15 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		config:        config,
		indexes:       indexes,
		target:        target,
		logger:        logger,
		now:           time.Now,
		subscriptions: make(map[string]*subscriptionState),
		reconcileWake: make(chan struct{}, 1),
	}, nil
}

// Start loads the initial instrument index and starts expiry/mapping reconciliation.
func (m *Manager) Start(parent context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.cancel != nil {
		return errors.New("ticker consumer: subscription manager is already running")
	}
	if parent == nil {
		parent = context.Background()
	}
	resolver, err := m.indexes.Snapshot(parent)
	if err != nil {
		return fmt.Errorf("load instrument token index: %w", err)
	}
	if resolver == nil {
		return errors.New("ticker consumer: index provider returned nil")
	}
	m.mu.Lock()
	m.resolver = resolver
	m.mu.Unlock()
	m.ctx, m.cancel = context.WithCancel(parent)
	m.running.Store(true)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.running.Store(false)
		m.run(m.ctx)
	}()
	return nil
}

// HandleHeartbeat validates a heartbeat and refreshes the desired IDs it contains.
func (m *Manager) HandleHeartbeat(payload []byte) error {
	var heartbeat heartbeatMessage
	if err := json.Unmarshal(payload, &heartbeat); err != nil {
		return fmt.Errorf("decode heartbeat: %w", err)
	}
	now := m.now().UTC()
	seen := make(map[models.InstrumentID]struct{}, len(heartbeat.IDs))
	var invalid []error
	m.mu.Lock()
	for _, rawID := range heartbeat.IDs {
		id, err := models.ParseInstrumentID(rawID)
		if err != nil {
			invalid = append(invalid, err)
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		key := string(id)
		state := m.subscriptions[key]
		if state == nil {
			state = &subscriptionState{status: Subscription{ID: key, Mode: "full"}}
			m.subscriptions[key] = state
		}
		state.status.LastHeartbeatAt = now
		state.status.ExpiresAt = now.Add(m.config.TTL)
	}
	m.mu.Unlock()
	if len(seen) > 0 {
		m.signalReconcile()
	}
	return errors.Join(invalid...)
}

// Running reports whether the manager reconciliation loop is active.
func (m *Manager) Running() bool { return m.running.Load() }

// Subscriptions returns a sorted, race-free health snapshot.
func (m *Manager) Subscriptions() []Subscription {
	m.mu.RLock()
	result := make([]Subscription, 0, len(m.subscriptions))
	for _, state := range m.subscriptions {
		item := state.status
		item.LastRealTickAt, item.LastPublishedAt = m.target.Activity(item.ID)
		result = append(result, item)
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Close stops the manager and waits for its loop to exit.
func (m *Manager) Close(ctx context.Context) error {
	m.startMu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.startMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) run(ctx context.Context) {
	cleanupEvery := m.config.TTL / 2
	if cleanupEvery > 500*time.Millisecond {
		cleanupEvery = 500 * time.Millisecond
	}
	if cleanupEvery < 10*time.Millisecond {
		cleanupEvery = 10 * time.Millisecond
	}
	cleanup := time.NewTicker(cleanupEvery)
	refresh := time.NewTicker(m.config.MappingRefreshInterval)
	defer cleanup.Stop()
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.reconcileWake:
			m.reconcile(ctx, false)
		case <-cleanup.C:
			m.expire(ctx)
		case <-refresh.C:
			m.reconcile(ctx, true)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context, refresh bool) {
	if refresh {
		resolver, err := m.indexes.Snapshot(ctx)
		if err != nil {
			m.setAllMappingErrors("instrument catalog unavailable")
			m.logger.Warn("refresh instrument token index failed", "error", err)
			return
		}
		if resolver == nil {
			m.setAllMappingErrors("instrument catalog unavailable")
			return
		}
		m.mu.Lock()
		m.resolver = resolver
		m.mu.Unlock()
	}

	m.mu.RLock()
	resolver := m.resolver
	ids := make([]string, 0, len(m.subscriptions))
	for id, state := range m.subscriptions {
		if refresh || state.status.InstrumentToken == 0 {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	now := m.now().UTC()
	for _, id := range ids {
		instrumentID := models.InstrumentID(id)
		token, ok := resolver.Token(instrumentID)
		if !ok || token <= 0 || token > math.MaxUint32 {
			m.setMappingError(id, "instrument not found")
			continue
		}
		if err := m.target.Upsert(ctx, id, uint32(token)); err != nil {
			m.setMappingError(id, "upstream subscription failed")
			m.logger.Warn("apply ticker subscription failed", "id", id, "error", err)
			continue
		}
		m.mu.Lock()
		if state := m.subscriptions[id]; state != nil {
			state.status.InstrumentToken = uint32(token)
			state.status.MappedAt = now
			state.status.MappingError = ""
		}
		m.mu.Unlock()
	}
}

func (m *Manager) expire(ctx context.Context) {
	now := m.now().UTC()
	m.mu.Lock()
	ids := make([]string, 0)
	for id, state := range m.subscriptions {
		if now.Before(state.status.ExpiresAt) {
			continue
		}
		ids = append(ids, id)
		delete(m.subscriptions, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		if err := m.target.Remove(ctx, id); err != nil {
			m.logger.Warn("expire ticker subscription failed", "id", id, "error", err)
		}
	}
}

func (m *Manager) setAllMappingErrors(message string) {
	m.mu.Lock()
	for _, state := range m.subscriptions {
		state.status.MappingError = message
	}
	m.mu.Unlock()
}

func (m *Manager) setMappingError(id, message string) {
	m.mu.Lock()
	if state := m.subscriptions[id]; state != nil {
		state.status.MappingError = message
	}
	m.mu.Unlock()
}

func (m *Manager) signalReconcile() {
	select {
	case m.reconcileWake <- struct{}{}:
	default:
	}
}
