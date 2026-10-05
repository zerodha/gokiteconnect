package producer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/models"
)

// Session contains the broker credentials needed to construct a Kite socket.
type Session struct {
	UserID      string
	APIKey      string
	AccessToken string
	EncToken    string
	IsAPIUser   bool
}

// Fingerprint identifies the connection-relevant part of a session without
// exposing any token in logs or status responses.
func (s Session) Fingerprint() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		s.UserID,
		s.APIKey,
		s.AccessToken,
		s.EncToken,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// SessionProvider loads the latest stored session and can force a new login.
type SessionProvider interface {
	LoadSession(ctx context.Context, userID string, force bool) (Session, error)
}

// Tick is a broker tick annotated with its canonical instrument ID.
type Tick struct {
	ID string `json:"id"`
	models.Tick
}

// OrderEvent is the durable envelope used between the socket callback and the
// compatibility accounts.<account>.orders publication.
type OrderEvent struct {
	EventID    string            `json:"event_id"`
	ReceivedAt time.Time         `json:"received_at"`
	Order      kiteconnect.Order `json:"-"`
	orderJSON  json.RawMessage
}

func newOrderEvent(userID string, receivedAt time.Time, order kiteconnect.Order) (OrderEvent, error) {
	payload, err := json.Marshal(order)
	if err != nil {
		return OrderEvent{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(strings.TrimSpace(userID)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return OrderEvent{
		EventID:    hex.EncodeToString(hash.Sum(nil)),
		ReceivedAt: receivedAt.UTC(),
		Order:      order,
		orderJSON:  payload,
	}, nil
}

// MarshalJSON preserves the original Kite order representation. This avoids a
// lossy decode/encode cycle while an event waits in JetStream.
func (e OrderEvent) MarshalJSON() ([]byte, error) {
	orderJSON := e.orderJSON
	if len(orderJSON) == 0 {
		var err error
		orderJSON, err = json.Marshal(e.Order)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		EventID    string          `json:"event_id"`
		ReceivedAt time.Time       `json:"received_at"`
		Order      json.RawMessage `json:"order"`
	}{EventID: e.EventID, ReceivedAt: e.ReceivedAt, Order: orderJSON})
}

// UnmarshalJSON extracts the routing account while retaining the raw order.
// models.Time intentionally rejects its zero value, which may appear on valid
// partial order updates, so the durable envelope must not require a complete
// kiteconnect.Order decode.
func (e *OrderEvent) UnmarshalJSON(data []byte) error {
	var wire struct {
		EventID    string          `json:"event_id"`
		ReceivedAt time.Time       `json:"received_at"`
		Order      json.RawMessage `json:"order"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var routing struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(wire.Order, &routing); err != nil {
		return err
	}
	e.EventID = wire.EventID
	e.ReceivedAt = wire.ReceivedAt
	e.Order = kiteconnect.Order{AccountID: routing.AccountID}
	e.orderJSON = append(e.orderJSON[:0], wire.Order...)
	return nil
}

func (e OrderEvent) encodedOrder() ([]byte, error) {
	if len(e.orderJSON) != 0 {
		return append([]byte(nil), e.orderJSON...), nil
	}
	return json.Marshal(e.Order)
}

// Activity reports the latest real and successfully published tick times for
// an instrument.
type Activity struct {
	LastRealTickAt  time.Time
	LastPublishedAt time.Time
}

// Status is the readiness state owned by the producer.
type Status struct {
	SessionLoaded      bool
	SocketConnected    bool
	NATSConnected      bool
	StreamsReady       bool
	OrderDeliveryReady bool
}

// Ready reports whether the complete socket-to-NATS path is usable.
func (s Status) Ready() bool {
	return s.SessionLoaded && s.SocketConnected && s.NATSConnected && s.StreamsReady && s.OrderDeliveryReady
}

// Config controls producer retry and polling behavior.
type Config struct {
	UserID            string
	TokenPollInterval time.Duration
	ReplayInterval    time.Duration
}
