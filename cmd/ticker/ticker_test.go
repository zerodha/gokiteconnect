package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker/consumer"
	"github.com/devshoe/gokiteconnect/cmd/ticker/producer"
)

type fakeProducerStatus struct{ status producer.Status }

func (f fakeProducerStatus) Status() producer.Status { return f.status }

type fakeSubscriptionStatus struct {
	running       bool
	subscriptions []consumer.Subscription
}

func (f fakeSubscriptionStatus) Running() bool { return f.running }
func (f fakeSubscriptionStatus) Subscriptions() []consumer.Subscription {
	return f.subscriptions
}

type fakeHeartbeatStatus bool

func (f fakeHeartbeatStatus) Running() bool { return bool(f) }

func TestHealthContract(t *testing.T) {
	ready := producer.Status{
		SessionLoaded: true, SocketConnected: true, NATSConnected: true,
		StreamsReady: true, OrderDeliveryReady: true,
	}
	subscriptions := []consumer.Subscription{{ID: "NSE:INFY", InstrumentToken: 123, Mode: "full"}}
	handler := healthHandler(
		fakeProducerStatus{status: ready},
		fakeSubscriptionStatus{running: true, subscriptions: subscriptions},
		fakeHeartbeatStatus(true),
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var basic map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &basic); err != nil {
		t.Fatal(err)
	}
	if basic["running"] != true || basic["subscriptions"] != nil {
		t.Fatalf("body=%v", basic)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health?subs=true", nil))
	var detailed struct {
		Running       bool                    `json:"running"`
		Subscriptions []consumer.Subscription `json:"subscriptions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detailed); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !detailed.Running || len(detailed.Subscriptions) != 1 || detailed.Subscriptions[0].ID != "NSE:INFY" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHealthReportsUnavailableAndRejectsInvalidQuery(t *testing.T) {
	handler := healthHandler(
		fakeProducerStatus{},
		fakeSubscriptionStatus{running: true},
		fakeHeartbeatStatus(true),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"running\":false}\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health?subs=maybe", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestLoadTickerConfigDefaultsAndStorageRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRADEBOT_LOCAL_STORAGE_ROOT", filepath.Join(home, "tradebot-test"))
	t.Setenv("TRADEBOT_CREDENTIALS_SQLITE_PATH", "")
	t.Setenv("TRADEBOT_INSTRUMENTS_DUCKDB_PATH", "")
	t.Setenv("TRADEBOT_TICKER_USER_ID", "U1")
	t.Setenv("TRADEBOT_NATS_URL", "nats://127.0.0.1:4222")
	t.Setenv("TRADEBOT_HEARTBEAT_TOPIC", "")
	t.Setenv("TRADEBOT_SUBSCRIPTION_TTL_SECONDS", "")
	t.Setenv("TRADEBOT_TICKER_TOKEN_POLL_INTERVAL_SECONDS", "")
	t.Setenv("TRADEBOT_TICKER_MAPPING_REFRESH_INTERVAL_SECONDS", "")
	t.Setenv("TRADEBOT_TICKER_SERVICE_PORT", "")

	got, err := loadTickerConfig()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "tradebot-test")
	if got.CredentialsPath != filepath.Join(root, "credentials.sqlite3") || got.InstrumentsPath != filepath.Join(root, "instruments.duckdb") {
		t.Fatalf("paths credentials=%q instruments=%q", got.CredentialsPath, got.InstrumentsPath)
	}
	if got.HeartbeatTopic != defaultHeartbeatTopic || got.Port != defaultTickerPort || got.SubscriptionTTL != time.Minute {
		t.Fatalf("defaults=%+v", got)
	}
}

func TestLoadTickerConfigValidation(t *testing.T) {
	setValidTickerEnvironment(t)
	t.Setenv("TRADEBOT_TICKER_USER_ID", "")
	if _, err := loadTickerConfig(); err == nil {
		t.Fatal("missing user ID was accepted")
	}
	setValidTickerEnvironment(t)
	t.Setenv("TRADEBOT_NATS_URL", "http://127.0.0.1:4222")
	if _, err := loadTickerConfig(); err == nil {
		t.Fatal("unsupported NATS URL was accepted")
	}
	setValidTickerEnvironment(t)
	t.Setenv("TRADEBOT_SUBSCRIPTION_TTL_SECONDS", "0")
	if _, err := loadTickerConfig(); err == nil {
		t.Fatal("invalid TTL was accepted")
	}
}

func setValidTickerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TRADEBOT_LOCAL_STORAGE_ROOT", "")
	t.Setenv("TRADEBOT_CREDENTIALS_SQLITE_PATH", "")
	t.Setenv("TRADEBOT_INSTRUMENTS_DUCKDB_PATH", "")
	t.Setenv("TRADEBOT_TICKER_USER_ID", "U1")
	t.Setenv("TRADEBOT_NATS_URL", "nats://127.0.0.1:4222")
	t.Setenv("TRADEBOT_HEARTBEAT_TOPIC", "")
	t.Setenv("TRADEBOT_SUBSCRIPTION_TTL_SECONDS", "")
	t.Setenv("TRADEBOT_TICKER_TOKEN_POLL_INTERVAL_SECONDS", "")
	t.Setenv("TRADEBOT_TICKER_MAPPING_REFRESH_INTERVAL_SECONDS", "")
	t.Setenv("TRADEBOT_TICKER_SERVICE_PORT", "")
}
