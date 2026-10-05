package main

import (
	"strings"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker/consumer"
	"github.com/devshoe/gokiteconnect/cmd/ticker/producer"
)

func TestTickerAppShowsSubscriptionSnapshot(t *testing.T) {
	now := time.Date(2026, time.September, 25, 10, 30, 0, 0, time.UTC)
	subscription := consumer.Subscription{
		ID:              "NSE:INFY",
		InstrumentToken: 408065,
		Mode:            "full",
		LastHeartbeatAt: now.Add(-2 * time.Second),
		ExpiresAt:       now.Add(58 * time.Second),
		MappedAt:        now.Add(-2 * time.Second),
		LastRealTickAt:  now.Add(-time.Second),
		LastPublishedAt: now.Add(-time.Second),
	}
	ready := producer.Status{
		SessionLoaded: true, SocketConnected: true, NATSConnected: true,
		StreamsReady: true, OrderDeliveryReady: true,
	}
	app := newTickerApp(
		t.Context(),
		tickerConfig{UserID: "U1", Port: "8082", HeartbeatTopic: "ticks.subscriptions"},
		fakeProducerStatus{status: ready},
		fakeSubscriptionStatus{running: true, subscriptions: []consumer.Subscription{subscription}},
		fakeHeartbeatStatus(true),
		newTickerLogBuffer(defaultTickerLogLimit),
	)
	app.applySnapshot(tickerCLISnapshot{
		producer: ready, manager: true, heartbeat: true,
		subscriptions: []consumer.Subscription{subscription}, refreshedAt: now,
	})

	if got := app.table.GetTitle(); got != " Subscriptions (1) " {
		t.Fatalf("table title=%q", got)
	}
	if got := app.table.GetCell(1, 0).Text; got != "NSE:INFY" {
		t.Fatalf("subscription ID=%q", got)
	}
	if got := app.table.GetCell(1, 1).Text; got != "408065" {
		t.Fatalf("instrument token=%q", got)
	}
	if got := app.table.GetCell(1, 3).Text; got != "MAPPED" {
		t.Fatalf("mapping status=%q", got)
	}
	if got := app.table.GetCell(1, 4).Text; got != "2s ago" {
		t.Fatalf("heartbeat age=%q", got)
	}
	if got := app.table.GetCell(1, 5).Text; got != "in 58s" {
		t.Fatalf("expiry=%q", got)
	}
	if health := app.health.GetText(true); !strings.Contains(health, "ONLINE") || !strings.Contains(health, "Kite socket") {
		t.Fatalf("health=%q", health)
	}
	if detail := app.detail.GetText(true); !strings.Contains(detail, "NSE:INFY") || !strings.Contains(detail, "408065") {
		t.Fatalf("detail=%q", detail)
	}
}

func TestTickerAppShowsEmptyAndFailedMappingStates(t *testing.T) {
	now := time.Date(2026, time.September, 25, 10, 30, 0, 0, time.UTC)
	app := newTickerApp(
		t.Context(),
		tickerConfig{UserID: "U1", Port: "8082", HeartbeatTopic: "custom.heartbeats"},
		fakeProducerStatus{},
		fakeSubscriptionStatus{},
		fakeHeartbeatStatus(false),
		newTickerLogBuffer(defaultTickerLogLimit),
	)
	app.applySnapshot(tickerCLISnapshot{refreshedAt: now})
	if got := app.table.GetTitle(); got != " Subscriptions (0) " {
		t.Fatalf("empty title=%q", got)
	}
	if detail := app.detail.GetText(true); !strings.Contains(detail, "custom.heartbeats") {
		t.Fatalf("empty detail=%q", detail)
	}

	failed := consumer.Subscription{
		ID:              "NSE:MISSING",
		Mode:            "full",
		LastHeartbeatAt: now,
		ExpiresAt:       now.Add(-time.Second),
		MappingError:    "instrument is absent from the catalog",
	}
	app.applySnapshot(tickerCLISnapshot{subscriptions: []consumer.Subscription{failed}, refreshedAt: now})
	if got := app.table.GetCell(1, 3).Text; got != "ERROR" {
		t.Fatalf("mapping status=%q", got)
	}
	if got := app.table.GetCell(1, 5).Text; got != "expired" {
		t.Fatalf("expiry=%q", got)
	}
	if detail := app.detail.GetText(true); !strings.Contains(detail, failed.MappingError) {
		t.Fatalf("failed detail=%q", detail)
	}
}

func TestTickerAppPreservesSelectedSubscription(t *testing.T) {
	app := newTickerApp(
		t.Context(),
		tickerConfig{HeartbeatTopic: defaultHeartbeatTopic},
		fakeProducerStatus{},
		fakeSubscriptionStatus{},
		fakeHeartbeatStatus(false),
		newTickerLogBuffer(defaultTickerLogLimit),
	)
	now := time.Now().UTC()
	first := []consumer.Subscription{{ID: "NSE:INFY"}, {ID: "NSE:TCS"}}
	app.applySnapshot(tickerCLISnapshot{subscriptions: first, refreshedAt: now})
	app.table.Select(2, 0)
	app.applySnapshot(tickerCLISnapshot{subscriptions: first, refreshedAt: now.Add(time.Second)})
	row, _ := app.table.GetSelection()
	if row != 2 || app.table.GetCell(row, 0).Text != "NSE:TCS" {
		t.Fatalf("selection row=%d id=%q", row, app.table.GetCell(row, 0).Text)
	}
}

func TestTickerTimeFormatting(t *testing.T) {
	now := time.Date(2026, time.September, 25, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "zero age", got: formatTickerAge(time.Time{}, now), want: "—"},
		{name: "seconds", got: formatTickerAge(now.Add(-5*time.Second), now), want: "5s ago"},
		{name: "minutes", got: formatTickerAge(now.Add(-2*time.Minute-4*time.Second), now), want: "2m 04s ago"},
		{name: "expiry", got: formatTickerExpiry(now.Add(45*time.Second), now), want: "in 45s"},
		{name: "expired", got: formatTickerExpiry(now.Add(-time.Second), now), want: "expired"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q want %q", test.got, test.want)
			}
		})
	}
}

func TestTickerLogBufferIsBounded(t *testing.T) {
	logs := newTickerLogBuffer(2)
	if _, err := logs.Write([]byte("first\nsecond\nthird\npartial")); err != nil {
		t.Fatal(err)
	}
	if got, want := logs.Text(), "second\nthird\npartial"; got != want {
		t.Fatalf("logs=%q want %q", got, want)
	}
}

func TestTickerAppTogglesLogView(t *testing.T) {
	logs := newTickerLogBuffer(defaultTickerLogLimit)
	_, _ = logs.Write([]byte("level=INFO msg=\"ticker started\"\n"))
	app := newTickerApp(
		t.Context(),
		tickerConfig{HeartbeatTopic: defaultHeartbeatTopic},
		fakeProducerStatus{},
		fakeSubscriptionStatus{},
		fakeHeartbeatStatus(false),
		logs,
	)

	app.toggleLogs()
	page, _ := app.pages.GetFrontPage()
	if page != tickerLogsPage || !app.logsVisible {
		t.Fatalf("front page=%q logsVisible=%t", page, app.logsVisible)
	}
	if got := app.logView.GetText(false); !strings.Contains(got, "ticker started") {
		t.Fatalf("log view=%q", got)
	}

	app.closeLogs()
	page, _ = app.pages.GetFrontPage()
	if page != tickerDashboardPage || app.logsVisible {
		t.Fatalf("front page=%q logsVisible=%t", page, app.logsVisible)
	}
}
