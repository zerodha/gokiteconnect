package producer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type recordedPublication struct {
	subject string
	payload []byte
}

type recordingCore struct {
	publications []recordedPublication
	errors       map[string]error
}

func (c *recordingCore) Publish(subject string, payload []byte) error {
	c.publications = append(c.publications, recordedPublication{subject: subject, payload: append([]byte(nil), payload...)})
	return c.errors[subject]
}

func (*recordingCore) FlushWithContext(context.Context) error { return nil }

type recordingAcknowledged struct {
	publications []recordedPublication
	err          error
}

func (p *recordingAcknowledged) Publish(_ context.Context, subject string, payload []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	p.publications = append(p.publications, recordedPublication{subject: subject, payload: append([]byte(nil), payload...)})
	return &jetstream.PubAck{}, p.err
}

func TestPublishTickEmitsPriceThenFullTick(t *testing.T) {
	core := &recordingCore{}
	bus := &NATSBus{core: core, publisher: &recordingAcknowledged{}, logger: discardLogger()}
	if err := bus.PublishTick(Tick{ID: "NSE:INFY", Tick: models.Tick{LastPrice: 123.5}}); err != nil {
		t.Fatal(err)
	}
	if len(core.publications) != 2 {
		t.Fatalf("publications=%v", core.publications)
	}
	if core.publications[0].subject != "prices.nse.infy" || len(core.publications[0].payload) != 8 {
		t.Fatalf("price publication=%+v", core.publications[0])
	}
	if price := math.Float64frombits(binary.BigEndian.Uint64(core.publications[0].payload)); price != 123.5 {
		t.Fatalf("price=%v", price)
	}
	if core.publications[1].subject != "ticks.nse.infy" {
		t.Fatalf("tick subject=%q", core.publications[1].subject)
	}
	var payload map[string]any
	if err := json.Unmarshal(core.publications[1].payload, &payload); err != nil || payload["id"] != "NSE:INFY" {
		t.Fatalf("tick payload=%s err=%v", core.publications[1].payload, err)
	}
}

func TestPublishTickJoinsIndependentPublishErrors(t *testing.T) {
	priceErr := errors.New("price failed")
	tickErr := errors.New("tick failed")
	core := &recordingCore{errors: map[string]error{
		"prices.nse.infy": priceErr,
		"ticks.nse.infy":  tickErr,
	}}
	bus := &NATSBus{core: core, publisher: &recordingAcknowledged{}, logger: discardLogger()}
	err := bus.PublishTick(Tick{ID: "NSE:INFY", Tick: models.Tick{LastPrice: 1}})
	if !errors.Is(err, priceErr) || !errors.Is(err, tickErr) || len(core.publications) != 2 {
		t.Fatalf("error=%v publications=%v", err, core.publications)
	}
}

func TestDurableOrderPublishesCompatibilityMessageBeforeAck(t *testing.T) {
	event, err := newOrderEvent("U1", time.Now(), kiteconnect.Order{AccountID: "U1", OrderID: "O1"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(event)
	steps := make([]string, 0, 2)
	publisher := &recordingAcknowledgedWithSteps{steps: &steps}
	message := &fakeJetStreamMessage{data: payload, steps: &steps, delivered: 1}
	bus := &NATSBus{publisher: publisher, logger: discardLogger()}
	delivered := false
	bus.deliverOrder(context.Background(), message, func(err error) {
		t.Errorf("unexpected retry: %v", err)
	}, func() { delivered = true })
	if !delivered || !message.acked || len(steps) != 2 || steps[0] != "publish" || steps[1] != "ack" {
		t.Fatalf("delivered=%v acked=%v steps=%v", delivered, message.acked, steps)
	}
	if publisher.subject != "accounts.U1.orders" {
		t.Fatalf("subject=%q", publisher.subject)
	}
	var order map[string]any
	if err := json.Unmarshal(publisher.payload, &order); err != nil || order["order_id"] != "O1" {
		t.Fatalf("order payload=%s err=%v", publisher.payload, err)
	}
}

func TestDurableOrderFailureNAKsWithBoundedBackoff(t *testing.T) {
	event, _ := newOrderEvent("U1", time.Now(), kiteconnect.Order{AccountID: "U1", OrderID: "O1"})
	payload, _ := json.Marshal(event)
	message := &fakeJetStreamMessage{data: payload, delivered: 10}
	bus := &NATSBus{publisher: &recordingAcknowledged{err: errors.New("publish failed")}, logger: discardLogger()}
	retries := 0
	bus.deliverOrder(context.Background(), message, func(error) { retries++ }, nil)
	if retries != 1 || message.acked || message.nakDelay != time.Minute {
		t.Fatalf("retries=%d acked=%v nakDelay=%s", retries, message.acked, message.nakDelay)
	}
}

func TestStreamContracts(t *testing.T) {
	configs := StreamConfigs()
	if len(configs) != 4 {
		t.Fatalf("stream count=%d", len(configs))
	}
	byName := make(map[string]jetstream.StreamConfig, len(configs))
	for _, config := range configs {
		byName[config.Name] = config
	}
	if got := byName[TickStreamName]; got.Storage != jetstream.MemoryStorage || got.MaxMsgsPerSubject != 1 {
		t.Fatalf("ticks=%+v", got)
	}
	if got := byName[PriceStreamName]; got.Storage != jetstream.MemoryStorage || got.MaxMsgsPerSubject != 1 {
		t.Fatalf("prices=%+v", got)
	}
	if got := byName[OrderDeliveryStreamName]; got.Storage != jetstream.FileStorage || got.Retention != jetstream.WorkQueuePolicy || got.Subjects[0] != orderDeliverySubject {
		t.Fatalf("order delivery=%+v", got)
	}
}

type recordingAcknowledgedWithSteps struct {
	steps   *[]string
	subject string
	payload []byte
}

func (p *recordingAcknowledgedWithSteps) Publish(_ context.Context, subject string, payload []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	*p.steps = append(*p.steps, "publish")
	p.subject = subject
	p.payload = append([]byte(nil), payload...)
	return &jetstream.PubAck{}, nil
}

type fakeJetStreamMessage struct {
	data      []byte
	steps     *[]string
	delivered uint64
	acked     bool
	nakDelay  time.Duration
}

func (m *fakeJetStreamMessage) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: m.delivered}, nil
}
func (m *fakeJetStreamMessage) Data() []byte         { return m.data }
func (m *fakeJetStreamMessage) Headers() nats.Header { return nil }
func (m *fakeJetStreamMessage) Subject() string      { return "user-service.order-events.U1" }
func (m *fakeJetStreamMessage) Reply() string        { return "" }
func (m *fakeJetStreamMessage) Ack() error           { m.acked = true; return nil }
func (m *fakeJetStreamMessage) DoubleAck(context.Context) error {
	m.acked = true
	if m.steps != nil {
		*m.steps = append(*m.steps, "ack")
	}
	return nil
}
func (m *fakeJetStreamMessage) Nak() error { return nil }
func (m *fakeJetStreamMessage) NakWithDelay(delay time.Duration) error {
	m.nakDelay = delay
	return nil
}
func (m *fakeJetStreamMessage) InProgress() error           { return nil }
func (m *fakeJetStreamMessage) Term() error                 { return nil }
func (m *fakeJetStreamMessage) TermWithReason(string) error { return nil }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var _ jetstream.Msg = (*fakeJetStreamMessage)(nil)
