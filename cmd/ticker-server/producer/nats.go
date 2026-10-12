package producer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// TickStreamName retains the latest full tick per instrument subject.
	TickStreamName = "ticks"
	// PriceStreamName retains the latest compact price per instrument subject.
	PriceStreamName = "prices"
	// OrderStreamName retains compatible account order updates.
	OrderStreamName = "orders"
	// OrderDeliveryStreamName durably buffers socket order callbacks.
	OrderDeliveryStreamName = "user_order_delivery"

	orderDeliverySubject  = "user-service.order-events.*"
	orderDeliveryConsumer = "ticker-user-service-order-hook-v1"
)

// MessageBus is the transport contract used by Producer.
type MessageBus interface {
	StartOrderDelivery(context.Context, func(error), func()) error
	PublishTick(Tick) error
	EnqueueOrder(context.Context, OrderEvent) error
	Connected() bool
	StreamsReady() bool
	OrderDeliveryReady() bool
	Flush(context.Context) error
	Close() error
}

type corePublisher interface {
	Publish(subject string, data []byte) error
	FlushWithContext(context.Context) error
}

type acknowledgedPublisher interface {
	Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// NATSBus owns the shared Core NATS and JetStream connection.
type NATSBus struct {
	nc        *nats.Conn
	js        jetstream.JetStream
	core      corePublisher
	publisher acknowledgedPublisher
	logger    *slog.Logger

	streamsReady  atomic.Bool
	deliveryReady atomic.Bool
	consume       jetstream.ConsumeContext
	closeOnce     sync.Once
	closeErr      error
}

// OpenNATS connects to NATS and creates or updates all ticker streams.
func OpenNATS(ctx context.Context, natsURL string, logger *slog.Logger) (*NATSBus, error) {
	if logger == nil {
		logger = slog.Default()
	}
	nc, err := nats.Connect(
		natsURL,
		nats.Name("gokiteconnect-ticker"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("NATS disconnected", "error", errorString(err))
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			logger.Info("NATS reconnected")
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logger.Warn("NATS connection closed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("open JetStream: %w", err)
	}
	bus := &NATSBus{nc: nc, js: js, core: nc, publisher: js, logger: logger}
	for _, config := range StreamConfigs() {
		if _, err := js.CreateOrUpdateStream(ctx, config); err != nil {
			nc.Close()
			return nil, fmt.Errorf("ensure JetStream stream %s: %w", config.Name, err)
		}
	}
	bus.streamsReady.Store(true)
	return bus, nil
}

// StreamConfigs returns the ticker's retained and durable stream contracts.
func StreamConfigs() []jetstream.StreamConfig {
	return []jetstream.StreamConfig{
		{
			Name: TickStreamName, Subjects: []string{"ticks.>"}, Storage: jetstream.MemoryStorage,
			Discard: jetstream.DiscardOld, MaxMsgsPerSubject: 1,
		},
		{
			Name: PriceStreamName, Subjects: []string{"prices.>"}, Storage: jetstream.MemoryStorage,
			Discard: jetstream.DiscardOld, MaxMsgsPerSubject: 1,
		},
		{
			Name: OrderStreamName, Subjects: []string{"accounts.>"}, Storage: jetstream.MemoryStorage,
			Discard: jetstream.DiscardOld, MaxMsgsPerSubject: 1000,
		},
		{
			Name: OrderDeliveryStreamName, Subjects: []string{orderDeliverySubject},
			Storage: jetstream.FileStorage, Retention: jetstream.WorkQueuePolicy,
			Discard: jetstream.DiscardOld, Duplicates: 24 * time.Hour,
		},
	}
}

// Conn returns the Core NATS connection shared with the heartbeat consumer.
func (b *NATSBus) Conn() *nats.Conn { return b.nc }

// StartOrderDelivery starts the durable event-to-compatible-order delivery loop.
func (b *NATSBus) StartOrderDelivery(ctx context.Context, onRetry func(error), onDelivered func()) error {
	consumer, err := b.js.CreateOrUpdateConsumer(ctx, OrderDeliveryStreamName, jetstream.ConsumerConfig{
		Name:          orderDeliveryConsumer,
		Durable:       orderDeliveryConsumer,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    -1,
		MaxAckPending: 256,
		FilterSubject: orderDeliverySubject,
	})
	if err != nil {
		return fmt.Errorf("ensure order delivery consumer: %w", err)
	}
	consume, err := consumer.Consume(func(message jetstream.Msg) {
		b.deliverOrder(ctx, message, onRetry, onDelivered)
	}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		b.logger.Error("order delivery consumer error", "error", errorString(err))
	}))
	if err != nil {
		return fmt.Errorf("start order delivery consumer: %w", err)
	}
	b.consume = consume
	b.deliveryReady.Store(true)
	go func() {
		<-ctx.Done()
		consume.Stop()
		b.deliveryReady.Store(false)
	}()
	return nil
}

// PublishTick emits compact price data before the corresponding full tick.
func (b *NATSBus) PublishTick(tick Tick) error {
	priceSubject, priceSubjectErr := instrumentSubject("prices", tick.ID)
	tickSubject, tickSubjectErr := instrumentSubject("ticks", tick.ID)
	pricePayload, pricePayloadErr := encodePrice(tick.LastPrice)
	tickPayload, tickPayloadErr := json.Marshal(tick)

	var pricePublishErr error
	if priceSubjectErr == nil && pricePayloadErr == nil {
		pricePublishErr = b.core.Publish(priceSubject, pricePayload)
	}
	var tickPublishErr error
	if tickSubjectErr == nil && tickPayloadErr == nil {
		tickPublishErr = b.core.Publish(tickSubject, tickPayload)
	}
	return errors.Join(
		priceSubjectErr,
		pricePayloadErr,
		pricePublishErr,
		tickSubjectErr,
		tickPayloadErr,
		tickPublishErr,
	)
}

// EnqueueOrder writes an order event to the durable delivery stream.
func (b *NATSBus) EnqueueOrder(ctx context.Context, event OrderEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	account, err := subjectToken(event.Order.AccountID)
	if err != nil {
		return err
	}
	_, err = b.publisher.Publish(ctx, "user-service.order-events."+account, payload, jetstream.WithMsgID(event.EventID))
	return err
}

func (b *NATSBus) deliverOrder(ctx context.Context, message jetstream.Msg, onRetry func(error), onDelivered func()) {
	var event OrderEvent
	if err := json.Unmarshal(message.Data(), &event); err != nil {
		b.retryOrder(message, fmt.Errorf("decode order event: %w", err), onRetry)
		return
	}
	account, err := subjectToken(event.Order.AccountID)
	if err != nil {
		b.retryOrder(message, err, onRetry)
		return
	}
	payload, err := event.encodedOrder()
	if err != nil {
		b.retryOrder(message, err, onRetry)
		return
	}
	publishCtx, publishCancel := context.WithTimeout(ctx, 20*time.Second)
	_, err = b.publisher.Publish(publishCtx, "accounts."+account+".orders", payload, jetstream.WithMsgID(event.EventID))
	publishCancel()
	if err != nil {
		b.retryOrder(message, err, onRetry)
		return
	}
	ackCtx, ackCancel := context.WithTimeout(ctx, 5*time.Second)
	err = message.DoubleAck(ackCtx)
	ackCancel()
	if err != nil {
		if onRetry != nil {
			onRetry(fmt.Errorf("ack order event: %w", err))
		}
		return
	}
	if onDelivered != nil {
		onDelivered()
	}
}

func (b *NATSBus) retryOrder(message jetstream.Msg, deliveryErr error, onRetry func(error)) {
	if onRetry != nil {
		onRetry(deliveryErr)
	}
	delay := time.Second
	if metadata, err := message.Metadata(); err == nil {
		shift := metadata.NumDelivered - 1
		if shift > 6 {
			shift = 6
		}
		delay *= time.Duration(1 << shift)
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	if err := message.NakWithDelay(delay); err != nil {
		b.logger.Error("failed to NAK order event", "error", err)
	}
}

// Connected reports the current Core NATS connection state.
func (b *NATSBus) Connected() bool { return b.nc != nil && b.nc.IsConnected() }

// StreamsReady reports whether all stream declarations succeeded.
func (b *NATSBus) StreamsReady() bool { return b.streamsReady.Load() }

// OrderDeliveryReady reports whether the durable consumer is running.
func (b *NATSBus) OrderDeliveryReady() bool { return b.deliveryReady.Load() }

// Flush waits for buffered Core NATS publications.
func (b *NATSBus) Flush(ctx context.Context) error {
	if b.core == nil {
		return nil
	}
	return b.core.FlushWithContext(ctx)
}

// Close stops consumers and drains the NATS connection.
func (b *NATSBus) Close() error {
	b.closeOnce.Do(func() {
		b.deliveryReady.Store(false)
		if b.consume != nil {
			b.consume.Stop()
		}
		if b.nc != nil {
			b.closeErr = b.nc.Drain()
			if !b.nc.IsClosed() {
				b.nc.Close()
			}
		}
	})
	return b.closeErr
}

func instrumentSubject(prefix, instrumentID string) (string, error) {
	parts := strings.SplitN(strings.TrimSpace(instrumentID), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid instrument ID %q", instrumentID)
	}
	exchange := normalizeSubjectToken(parts[0])
	symbol := normalizeSubjectToken(parts[1])
	if exchange == "" || symbol == "" {
		return "", fmt.Errorf("invalid instrument ID %q", instrumentID)
	}
	return prefix + "." + exchange + "." + symbol, nil
}

func normalizeSubjectToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	return strings.ReplaceAll(value, ":", ".")
}

func subjectToken(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, ".*> \t\r\n") {
		return "", fmt.Errorf("value %q is not a safe NATS subject token", value)
	}
	return value, nil
}

func encodePrice(price float64) ([]byte, error) {
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return nil, fmt.Errorf("invalid last price: %v", price)
	}
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, math.Float64bits(price))
	return payload, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ MessageBus = (*NATSBus)(nil)
var _ corePublisher = (*nats.Conn)(nil)
