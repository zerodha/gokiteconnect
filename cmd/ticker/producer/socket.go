package producer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/models"
	kiteticker "github.com/devshoe/gokiteconnect/ticker"
)

// SocketCallbacks receives events from a broker socket.
type SocketCallbacks struct {
	OnConnect     func()
	OnTick        func(models.Tick)
	OnOrder       func(kiteconnect.Order)
	OnError       func(error)
	OnReconnect   func(int, time.Duration)
	OnClose       func(int, string)
	OnAuthFailure func()
}

// Socket is the serialized, context-aware broker connection used by Producer.
type Socket interface {
	Start(context.Context)
	Subscribe([]uint32) error
	Unsubscribe([]uint32) error
	Connected() bool
	Close() error
}

// SocketFactory creates sockets from refreshable credentials.
type SocketFactory interface {
	New(Session, SocketCallbacks) Socket
}

// ZerodhaSocketFactory creates sockets backed by the local Kite ticker package.
type ZerodhaSocketFactory struct{}

// NewZerodhaSocketFactory constructs a Zerodha socket factory.
func NewZerodhaSocketFactory() *ZerodhaSocketFactory { return &ZerodhaSocketFactory{} }

// New constructs a socket for either API or web-session credentials.
func (f *ZerodhaSocketFactory) New(session Session, callbacks SocketCallbacks) Socket {
	ticker := kiteticker.New(session.APIKey, session.AccessToken)
	if !session.IsAPIUser {
		ticker.SetEncToken(session.UserID, session.EncToken)
	}
	ticker.SetReconnectMaxRetries(6)
	// Keep the ticker's non-context-aware reconnect sleep below the command's
	// shutdown deadline.
	_ = ticker.SetReconnectMaxDelay(5 * time.Second)
	socket := &zerodhaSocket{ticker: ticker, callbacks: callbacks}
	socket.installCallbacks()
	return socket
}

type zerodhaSocket struct {
	ticker    *kiteticker.Ticker
	callbacks SocketCallbacks
	connected atomic.Bool
	writes    sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (s *zerodhaSocket) installCallbacks() {
	s.ticker.OnConnect(func() {
		s.connected.Store(true)
		if s.callbacks.OnConnect != nil {
			s.callbacks.OnConnect()
		}
	})
	s.ticker.OnTick(func(tick models.Tick) {
		if s.callbacks.OnTick != nil {
			s.callbacks.OnTick(tick)
		}
	})
	s.ticker.OnOrderUpdate(func(order kiteconnect.Order) {
		if s.callbacks.OnOrder != nil {
			s.callbacks.OnOrder(order)
		}
	})
	s.ticker.OnError(func(err error) {
		if s.callbacks.OnError != nil {
			s.callbacks.OnError(err)
		}
		if isAuthenticationError(err) && s.callbacks.OnAuthFailure != nil {
			s.callbacks.OnAuthFailure()
		}
	})
	s.ticker.OnReconnect(func(attempt int, delay time.Duration) {
		s.connected.Store(false)
		if s.callbacks.OnReconnect != nil {
			s.callbacks.OnReconnect(attempt, delay)
		}
	})
	s.ticker.OnNoReconnect(func(_ int) {
		s.connected.Store(false)
		if s.callbacks.OnAuthFailure != nil {
			s.callbacks.OnAuthFailure()
		}
	})
	s.ticker.OnClose(func(code int, reason string) {
		s.connected.Store(false)
		if s.callbacks.OnClose != nil {
			s.callbacks.OnClose(code, reason)
		}
	})
}

func (s *zerodhaSocket) Start(ctx context.Context) {
	s.ticker.ServeWithContext(ctx)
	s.connected.Store(false)
}

func (s *zerodhaSocket) Subscribe(tokens []uint32) error {
	if len(tokens) == 0 {
		return nil
	}
	if !s.connected.Load() {
		return errors.New("ticker producer: upstream socket is not connected")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if err := s.ticker.Subscribe(tokens); err != nil {
		return err
	}
	return s.ticker.SetMode(kiteticker.ModeFull, tokens)
}

func (s *zerodhaSocket) Unsubscribe(tokens []uint32) error {
	if len(tokens) == 0 || !s.connected.Load() {
		return nil
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	return s.ticker.Unsubscribe(tokens)
}

func (s *zerodhaSocket) Connected() bool { return s.connected.Load() }

func (s *zerodhaSocket) Close() error {
	s.closeOnce.Do(func() {
		s.connected.Store(false)
		s.writes.Lock()
		defer s.writes.Unlock()
		s.ticker.Stop()
		if s.ticker.Conn != nil {
			s.closeErr = s.ticker.Close()
		}
	})
	return s.closeErr
}

func isAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"401", "403", "unauthorized", "forbidden", "token", "authentication"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

var _ SocketFactory = (*ZerodhaSocketFactory)(nil)
var _ Socket = (*zerodhaSocket)(nil)
