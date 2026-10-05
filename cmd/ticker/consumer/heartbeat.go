// Package consumer receives subscription heartbeats and reconciles them with
// the producer's desired Kite subscriptions.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"
)

// Heartbeat is a Core NATS subscription that forwards heartbeat payloads to a handler.
type Heartbeat struct {
	conn    *nats.Conn
	subject string
	logger  *slog.Logger

	mu      sync.Mutex
	sub     *nats.Subscription
	running atomic.Bool
}

// NewHeartbeat constructs a heartbeat consumer on subject.
func NewHeartbeat(conn *nats.Conn, subject string, logger *slog.Logger) (*Heartbeat, error) {
	if conn == nil {
		return nil, errors.New("ticker consumer: NATS connection is required")
	}
	if subject == "" {
		return nil, errors.New("ticker consumer: heartbeat subject is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Heartbeat{conn: conn, subject: subject, logger: logger}, nil
}

// Start begins consuming new Core NATS heartbeat messages.
func (h *Heartbeat) Start(ctx context.Context, handler func([]byte) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sub != nil {
		return errors.New("ticker consumer: heartbeat is already running")
	}
	subscription, err := h.conn.Subscribe(h.subject, func(message *nats.Msg) {
		if handler == nil {
			return
		}
		if err := handler(append([]byte(nil), message.Data...)); err != nil {
			h.logger.Warn("invalid ticker heartbeat", "error", err)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe ticker heartbeat: %w", err)
	}
	if err := h.conn.Flush(); err != nil {
		_ = subscription.Unsubscribe()
		return fmt.Errorf("flush ticker heartbeat subscription: %w", err)
	}
	h.sub = subscription
	h.running.Store(true)
	go func() {
		<-ctx.Done()
		_ = h.Stop()
	}()
	return nil
}

// Running reports whether the Core NATS subscription is active.
func (h *Heartbeat) Running() bool { return h.running.Load() }

// Stop unsubscribes the heartbeat consumer. It is safe to call repeatedly.
func (h *Heartbeat) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running.Store(false)
	if h.sub == nil {
		return nil
	}
	err := h.sub.Unsubscribe()
	h.sub = nil
	return err
}
