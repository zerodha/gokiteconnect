// Command candle-writer keeps live 1m candles in Postgres from ticker-server's
// ticks, for a list's instruments.
//
// It heartbeats the list's instruments on TRADEBOT_HEARTBEAT_TOPIC, so the
// ticker streams them, reads their ticks from ticks.<exchange>.<symbol>,
// folds them into minute candles, and upserts the candles into the live_data
// table in batches. CandlesDuckDBRepository merges that table into its reads
// when it is given the same database.
//
// It reads ~/tradebot/settings.json (see config.SettingSpecs). It needs:
//
//	TRADEBOT_NATS_URL        one or more NATS URLs
//	TRADEBOT_TIMESCALE_DSN   the Postgres database live_data is in
//
// Run it with:
//
//	go run ./cmd/candle-writer -list "DEFAULT SYNC"
//
// SIGINT and SIGTERM stop it after a last write.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/config"
	marketdata "github.com/devshoe/gokiteconnect/market-data-service"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nats-io/nats.go"
)

const defaultHeartbeatTopic = "ticks.subscriptions"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("candle-writer stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	listFlag := flag.String("list", marketdata.DefaultSyncListName, "list whose instruments get live candles, by ID or name")
	batchSize := flag.Int("batch-size", 250, "ticks folded before an early write")
	flushInterval := flag.Duration("flush-interval", time.Second, "longest a candle waits to be written")
	heartbeatInterval := flag.Duration("heartbeat-interval", 15*time.Second, "how often the list's subscriptions are renewed; under TRADEBOT_SUBSCRIPTION_TTL_SECONDS")
	flag.Parse()
	if *batchSize <= 0 || *flushInterval <= 0 || *heartbeatInterval <= 0 {
		return errors.New("-batch-size, -flush-interval and -heartbeat-interval must be positive")
	}
	natsURL := strings.TrimSpace(settings.Settings.Get("TRADEBOT_NATS_URL"))
	if natsURL == "" {
		return errors.New("TRADEBOT_NATS_URL is required")
	}
	dsn := strings.TrimSpace(settings.Settings.Get("TRADEBOT_TIMESCALE_DSN"))
	if dsn == "" {
		return errors.New("TRADEBOT_TIMESCALE_DSN is required")
	}
	topic := strings.TrimSpace(settings.Settings.Get("TRADEBOT_HEARTBEAT_TOPIC"))
	if topic == "" {
		topic = defaultHeartbeatTopic
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	list, err := findList(ctx, settings.ListsPath, settings.InstrumentsPath, *listFlag)
	if err != nil {
		return err
	}
	ids := list.InstrumentIDs()
	if len(ids) == 0 {
		return fmt.Errorf("list %q has no instruments", list.Name)
	}

	store, err := repository.NewLiveCandlesPostgresRepository(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()

	conn, err := nats.Connect(natsURL,
		nats.Name("gokiteconnect-candle-writer"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warn("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { logger.Info("NATS reconnected") }),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { logger.Warn("NATS error", "error", err) }),
	)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer conn.Close()

	ticks := make(chan producer.Tick, *batchSize*4)
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[string(id)] = true
	}
	subscription, err := conn.Subscribe("ticks.>", func(message *nats.Msg) {
		tick, err := decodeTick(message.Data)
		if err != nil || !wanted[tick.ID] {
			return
		}
		select {
		case ticks <- tick:
		case <-ctx.Done():
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe to ticks: %w", err)
	}
	defer subscription.Unsubscribe()

	go heartbeat(ctx, conn, topic, ids, *heartbeatInterval, logger)
	logger.Info("candle-writer started", "list", list.Name, "instruments", len(ids),
		"batch_size", *batchSize, "flush_interval", *flushInterval)
	return runWriter(ctx, store, ticks, *batchSize, *flushInterval, logger)
}

// findList reads the list named by value, an ID or a name in any case.
func findList(ctx context.Context, listsPath, instrumentsPath, value string) (models.List, error) {
	value = strings.TrimSpace(value)
	lists, err := repository.NewListsSQLiteRepository(ctx, listsPath, instrumentsPath)
	if err != nil {
		return models.List{}, err
	}
	defer lists.Close()
	all, err := lists.GetLists(ctx, "")
	if err != nil {
		return models.List{}, err
	}
	for _, list := range all {
		if list.ID == value {
			return list, nil
		}
	}
	for _, list := range all {
		if strings.EqualFold(list.Name, value) {
			return list, nil
		}
	}
	return models.List{}, fmt.Errorf("no list %q", value)
}

// heartbeat asks the ticker for ids now and every interval until ctx is
// done.
func heartbeat(ctx context.Context, conn *nats.Conn, topic string, ids []models.InstrumentID, interval time.Duration, logger *slog.Logger) {
	payload, err := json.Marshal(struct {
		IDs []models.InstrumentID `json:"ids"`
	}{IDs: ids})
	if err != nil {
		logger.Error("encode heartbeat", "error", err)
		return
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		if err := conn.Publish(topic, payload); err != nil {
			logger.Warn("publish heartbeat", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// decodeTick reads a tick as ticker-server publishes it, with RFC 3339
// times.
func decodeTick(data []byte) (producer.Tick, error) {
	var wire struct {
		producer.Tick
		Timestamp     time.Time
		LastTradeTime time.Time
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return producer.Tick{}, err
	}
	tick := wire.Tick
	tick.Timestamp, tick.LastTradeTime = models.Time{Time: wire.Timestamp}, models.Time{Time: wire.LastTradeTime}
	return tick, nil
}
