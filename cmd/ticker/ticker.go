// Command ticker runs the standalone Zerodha market-data bridge.
//
// The consumer side listens for JSON heartbeats on Core NATS (by default
// ticks.subscriptions), maintains a TTL for each canonical instrument ID, and
// reconciles those IDs through the local DuckDB instrument catalog. Heartbeat
// mode and timestamp fields are accepted for compatibility; broker
// subscriptions always use full mode.
//
// The producer side owns one authenticated Kite WebSocket for the configured
// account. It publishes full JSON ticks to ticks.<exchange>.<symbol>, compact
// big-endian float64 prices to prices.<exchange>.<symbol>, and durably stages
// order updates on user-service.order-events.<account> before publishing the
// compatible accounts.<account>.orders message. Tick and price streams retain
// the latest message per subject.
//
// Required configuration:
//
//	TRADEBOT_TICKER_USER_ID  Zerodha account stored in the credentials database
//	TRADEBOT_NATS_URL        one or more NATS URLs
//
// Optional configuration:
//
//	TRADEBOT_LOCAL_STORAGE_ROOT=$HOME/tradebot
//	TRADEBOT_CREDENTIALS_SQLITE_PATH=<storage root>/credentials.sqlite3
//	TRADEBOT_INSTRUMENTS_DUCKDB_PATH=<storage root>/instruments.duckdb
//	TRADEBOT_HEARTBEAT_TOPIC=ticks.subscriptions
//	TRADEBOT_SUBSCRIPTION_TTL_SECONDS=60
//	TRADEBOT_TICKER_TOKEN_POLL_INTERVAL_SECONDS=300
//	TRADEBOT_TICKER_MAPPING_REFRESH_INTERVAL_SECONDS=900
//	TRADEBOT_TICKER_SERVICE_PORT=8082
//
// Run it with:
//
//	go run ./cmd/ticker
//
// Only one ticker process should run for a Zerodha account. GET /health returns
// whether the complete consumer/socket/NATS pipeline is ready; add ?subs=true
// for detailed subscriptions. An interactive terminal also shows a live
// subscription dashboard; press L to inspect its captured logs. SIGINT and
// SIGTERM stop heartbeat intake, drain accepted socket events, flush NATS, and
// then close local stores.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/cmd/ticker/consumer"
	"github.com/devshoe/gokiteconnect/cmd/ticker/producer"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	instrumentstore "github.com/devshoe/gokiteconnect/repository"
	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultHeartbeatTopic        = "ticks.subscriptions"
	defaultTickerPort            = "8082"
	defaultSubscriptionTTL       = 60 * time.Second
	defaultTokenPollInterval     = 300 * time.Second
	defaultMappingRefresh        = 900 * time.Second
	defaultShutdownTimeout       = 15 * time.Second
	defaultInitializationTimeout = 20 * time.Second
)

type tickerConfig struct {
	UserID                 string
	NATSURL                string
	HeartbeatTopic         string
	CredentialsPath        string
	InstrumentsPath        string
	Port                   string
	SubscriptionTTL        time.Duration
	TokenPollInterval      time.Duration
	MappingRefreshInterval time.Duration
}

type catalogIndexProvider struct {
	path     string
	userID   string
	sessions producer.SessionProvider
	mu       sync.Mutex
}

func (p *catalogIndexProvider) Snapshot(ctx context.Context) (consumer.TokenResolver, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	session, err := p.sessions.LoadSession(ctx, p.userID, false)
	if err != nil {
		return nil, err
	}
	kite := kiteconnect.New(session.APIKey)
	if session.IsAPIUser {
		kite.SetAccessToken(session.AccessToken)
	} else {
		kite.SetEncToken(session.EncToken)
	}
	repository, err := instrumentstore.NewInstrumentsDuckDBRepository(ctx, p.path)
	if err != nil {
		return nil, err
	}
	kite.SetInstrumentRepository(repository)
	catalog := kite.Instruments()
	defer catalog.Close()
	if _, err := catalog.RefreshIfStale(ctx); err != nil {
		return nil, err
	}
	return catalog.TokenIndex(ctx)
}

type healthResponse struct {
	Running       bool                     `json:"running"`
	Subscriptions *[]consumer.Subscription `json:"subscriptions,omitempty"`
}

type producerStatusProvider interface {
	Status() producer.Status
}

type subscriptionStatusProvider interface {
	Running() bool
	Subscriptions() []consumer.Subscription
}

type heartbeatStatusProvider interface {
	Running() bool
}

func main() {
	interactive := tickerCLIAvailable()
	var logs *tickerLogBuffer
	var logger *slog.Logger
	if interactive {
		logs = newTickerLogBuffer(defaultTickerLogLimit)
		logger = slog.New(slog.NewTextHandler(logs, nil))
	} else {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	slog.SetDefault(logger)
	if err := run(logger, logs); err != nil {
		if interactive {
			fmt.Fprintf(os.Stderr, "ticker stopped: %v\n", err)
		} else {
			logger.Error("ticker stopped", "error", err)
		}
		os.Exit(1)
	}
}

func run(logger *slog.Logger, logs *tickerLogBuffer) error {
	config, err := loadTickerConfig()
	if err != nil {
		return err
	}
	for _, path := range []string{config.CredentialsPath, config.InstrumentsPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create ticker storage directory: %w", err)
		}
	}

	credentialsDB, err := sql.Open("sqlite3", config.CredentialsPath)
	if err != nil {
		return fmt.Errorf("open credentials database: %w", err)
	}
	credentialsDB.SetMaxOpenConns(1)
	defer credentialsDB.Close()
	credentialsRepository, err := credentialstore.NewUserSQLiteRepository(credentialsDB)
	if err != nil {
		return err
	}
	sessions, err := producer.NewCredentialsSessionProvider(credentialsRepository)
	if err != nil {
		return err
	}

	initCtx, initCancel := context.WithTimeout(context.Background(), defaultInitializationTimeout)
	bus, err := producer.OpenNATS(initCtx, config.NATSURL, logger)
	initCancel()
	if err != nil {
		return err
	}
	busOwned := true
	defer func() {
		if busOwned {
			_ = bus.Close()
		}
	}()

	tickProducer, err := producer.New(producer.Config{
		UserID:            config.UserID,
		TokenPollInterval: config.TokenPollInterval,
		ReplayInterval:    time.Second,
	}, sessions, producer.NewZerodhaSocketFactory(), bus, logger)
	if err != nil {
		return err
	}
	indexProvider := &catalogIndexProvider{path: config.InstrumentsPath, userID: config.UserID, sessions: sessions}
	manager, err := consumer.NewManager(consumer.Config{
		TTL:                    config.SubscriptionTTL,
		MappingRefreshInterval: config.MappingRefreshInterval,
	}, indexProvider, tickProducer, logger)
	if err != nil {
		return err
	}
	heartbeat, err := consumer.NewHeartbeat(bus.Conn(), config.HeartbeatTopic, logger)
	if err != nil {
		return err
	}

	serviceCtx, serviceCancel := context.WithCancel(context.Background())
	defer serviceCancel()
	if err := manager.Start(serviceCtx); err != nil {
		return err
	}
	managerStarted := true
	if err := tickProducer.Start(serviceCtx); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		_ = manager.Close(shutdownCtx)
		return err
	}
	producerStarted := true
	if err := heartbeat.Start(serviceCtx, manager.HandleHeartbeat); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		_ = manager.Close(shutdownCtx)
		_ = tickProducer.Close(shutdownCtx)
		busOwned = false
		return err
	}
	heartbeatStarted := true

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           healthHandler(tickProducer, manager, heartbeat),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()
	logger.Info("ticker started", "address", server.Addr, "user_id", config.UserID, "heartbeat_topic", config.HeartbeatTopic)

	var dashboard *tickerApp
	var dashboardErr chan error
	if logs != nil {
		dashboard = newTickerApp(serviceCtx, config, tickProducer, manager, heartbeat, logs)
		dashboardErr = make(chan error, 1)
		go func() {
			dashboardErr <- dashboard.run()
		}()
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var runErr error
	dashboardExited := false
	select {
	case signalValue := <-signals:
		logger.Info("shutting down ticker", "signal", signalValue.String())
	case err := <-serverErr:
		if err != nil {
			runErr = fmt.Errorf("serve ticker health: %w", err)
		}
	case err := <-dashboardErr:
		dashboardExited = true
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("run ticker dashboard: %w", err)
		}
	}
	if dashboard != nil {
		dashboard.stop()
		if !dashboardExited {
			select {
			case err := <-dashboardErr:
				if err != nil && !errors.Is(err, context.Canceled) {
					runErr = errors.Join(runErr, fmt.Errorf("stop ticker dashboard: %w", err))
				}
			case <-time.After(2 * time.Second):
				runErr = errors.Join(runErr, errors.New("stop ticker dashboard: timed out"))
			}
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("shutdown health server: %w", err))
	}
	if heartbeatStarted {
		runErr = errors.Join(runErr, heartbeat.Stop())
	}
	if managerStarted {
		runErr = errors.Join(runErr, manager.Close(shutdownCtx))
	}
	if producerStarted {
		runErr = errors.Join(runErr, tickProducer.Close(shutdownCtx))
		busOwned = false
	}
	serviceCancel()
	return runErr
}

func tickerCLIAvailable() bool {
	input, inputErr := os.Stdin.Stat()
	output, outputErr := os.Stdout.Stat()
	return inputErr == nil && outputErr == nil &&
		input.Mode()&os.ModeCharDevice != 0 && output.Mode()&os.ModeCharDevice != 0
}

func healthHandler(tickProducer producerStatusProvider, manager subscriptionStatusProvider, heartbeat heartbeatStatusProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, request *http.Request) {
		includeSubscriptions := false
		if raw, present := request.URL.Query()["subs"]; present {
			if len(raw) != 1 {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "subs must be a boolean"})
				return
			}
			parsed, err := strconv.ParseBool(raw[0])
			if err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "subs must be a boolean"})
				return
			}
			includeSubscriptions = parsed
		}
		running := tickProducer.Status().Ready() && manager.Running() && heartbeat.Running()
		response := healthResponse{Running: running}
		if includeSubscriptions {
			subscriptions := manager.Subscriptions()
			response.Subscriptions = &subscriptions
		}
		status := http.StatusOK
		if !running {
			status = http.StatusServiceUnavailable
		}
		writeJSON(writer, status, response)
	})
	return mux
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func loadTickerConfig() (tickerConfig, error) {
	base, err := kiteconnect.LoadConfig()
	if err != nil {
		return tickerConfig{}, err
	}
	userID := base.TickerUserID
	if userID == "" {
		return tickerConfig{}, errors.New("TRADEBOT_TICKER_USER_ID is required")
	}
	if strings.ContainsAny(userID, ".*> \t\r\n") {
		return tickerConfig{}, errors.New("TRADEBOT_TICKER_USER_ID must be a safe NATS subject token")
	}
	natsURL := strings.TrimSpace(os.Getenv("TRADEBOT_NATS_URL"))
	if natsURL == "" {
		return tickerConfig{}, errors.New("TRADEBOT_NATS_URL is required")
	}
	if err := validateNATSURLs(natsURL); err != nil {
		return tickerConfig{}, err
	}
	port := envDefault("TRADEBOT_TICKER_SERVICE_PORT", defaultTickerPort)
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return tickerConfig{}, errors.New("TRADEBOT_TICKER_SERVICE_PORT must be a valid port")
	}
	ttl, err := durationFromSeconds("TRADEBOT_SUBSCRIPTION_TTL_SECONDS", defaultSubscriptionTTL)
	if err != nil {
		return tickerConfig{}, err
	}
	tokenPoll, err := durationFromSeconds("TRADEBOT_TICKER_TOKEN_POLL_INTERVAL_SECONDS", defaultTokenPollInterval)
	if err != nil {
		return tickerConfig{}, err
	}
	mappingRefresh, err := durationFromSeconds("TRADEBOT_TICKER_MAPPING_REFRESH_INTERVAL_SECONDS", defaultMappingRefresh)
	if err != nil {
		return tickerConfig{}, err
	}
	return tickerConfig{
		UserID:                 userID,
		NATSURL:                natsURL,
		HeartbeatTopic:         envDefault("TRADEBOT_HEARTBEAT_TOPIC", defaultHeartbeatTopic),
		CredentialsPath:        base.CredentialsPath,
		InstrumentsPath:        base.InstrumentsPath,
		Port:                   port,
		SubscriptionTTL:        ttl,
		TokenPollInterval:      tokenPoll,
		MappingRefreshInterval: mappingRefresh,
	}, nil
}

func durationFromSeconds(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return time.Duration(seconds) * time.Second, nil
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func validateNATSURLs(value string) error {
	for _, server := range strings.Split(value, ",") {
		parsed, err := url.Parse(strings.TrimSpace(server))
		if err != nil || parsed.Host == "" {
			return errors.New("TRADEBOT_NATS_URL contains an invalid server URL")
		}
		switch parsed.Scheme {
		case "nats", "tls", "ws", "wss":
		default:
			return errors.New("TRADEBOT_NATS_URL contains an unsupported URL scheme")
		}
	}
	return nil
}
