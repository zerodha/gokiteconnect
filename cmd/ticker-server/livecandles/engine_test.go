package livecandles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/models"
)

type publication struct {
	subject string
	candle  Candle
}

type fakePublisher struct {
	mu           sync.Mutex
	publications []publication
	err          error
}

func (p *fakePublisher) Publish(subject string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	var candle Candle
	if err := json.Unmarshal(payload, &candle); err != nil {
		return err
	}
	p.publications = append(p.publications, publication{subject: subject, candle: candle})
	return nil
}

// take returns and forgets what has been published.
func (p *fakePublisher) take() []publication {
	p.mu.Lock()
	defer p.mu.Unlock()
	taken := p.publications
	p.publications = nil
	return taken
}

type fakeTicks struct {
	mu        sync.Mutex
	refreshed [][]string
}

func (t *fakeTicks) Refresh(ids []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshed = append(t.refreshed, ids)
	return nil
}

func at(clock string) time.Time {
	ts, err := time.ParseInLocation("15:04:05", clock, models.ExchangeLocation)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, time.November, 3, ts.Hour(), ts.Minute(), ts.Second(), 0, models.ExchangeLocation)
}

func tick(clock string, price float64, volume uint32) producer.Tick {
	t := producer.Tick{ID: "NSE:INFY", BroadcastType: producer.BroadcastNew}
	t.InstrumentToken, t.LastPrice, t.VolumeTraded = 408065, price, volume
	t.Timestamp = models.Time{Time: at(clock)}
	return t
}

// newEngine returns an engine whose clock reads *clock.
func newEngine(t *testing.T, clock *time.Time) (*Engine, *fakePublisher, *fakeTicks) {
	t.Helper()
	publisher, ticks := &fakePublisher{}, &fakeTicks{}
	engine, err := New(Config{TTL: time.Minute}, publisher, ticks, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time { return *clock }
	return engine, publisher, ticks
}

func heartbeat(t *testing.T, engine *Engine, payload string) {
	t.Helper()
	if err := engine.HandleHeartbeat([]byte(payload)); err != nil {
		t.Fatal(err)
	}
}

// step publishes whatever is due at the engine's clock.
func step(engine *Engine) {
	now := engine.now().UTC()
	engine.expire(now)
	engine.publish(engine.collect(now))
}

func TestSubjectsCarryTheMode(t *testing.T) {
	for _, test := range []struct {
		mode     Mode
		interval int
		want     string
	}{
		{ModeContinuous, 0, "candles.nse.infy.continuous"},
		{ModePeriodic, 5, "candles.nse.infy.periodic.5"},
		{ModeOnComplete, 0, "candles.nse.infy.on_complete"},
	} {
		got, err := Subject("NSE:INFY", test.mode, test.interval)
		if err != nil || got != test.want {
			t.Errorf("%s: subject = %q, %v, want %q", test.mode, got, err, test.want)
		}
	}
}

func TestHeartbeatsAreValidatedAndKeepTicksSubscribed(t *testing.T) {
	clock := at("10:00:00")
	engine, _, ticks := newEngine(t, &clock)
	for name, payload := range map[string]string{
		"unknown mode":      `{"ids":["NSE:INFY"],"mode":"full"}`,
		"periodic, no n":    `{"ids":["NSE:INFY"],"mode":"periodic"}`,
		"periodic, too big": `{"ids":["NSE:INFY"],"mode":"periodic","interval_seconds":61}`,
		"not JSON":          `{`,
	} {
		if err := engine.HandleHeartbeat([]byte(payload)); err == nil {
			t.Errorf("%s: heartbeat accepted", name)
		}
	}
	if len(ticks.refreshed) != 0 || len(engine.Subscriptions()) != 0 {
		t.Fatalf("rejected heartbeats subscribed: %v, %v", ticks.refreshed, engine.Subscriptions())
	}

	err := engine.HandleHeartbeat([]byte(`{"ids":["nse:infy","NSE:INFY","bad"],"mode":"periodic","interval_seconds":5}`))
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("invalid ID error = %v", err)
	}
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"on_complete"}`)
	if want := [][]string{{"NSE:INFY"}, {"NSE:INFY"}}; !reflect.DeepEqual(ticks.refreshed, want) {
		t.Fatalf("refreshed = %v, want %v", ticks.refreshed, want)
	}
	var subjects []string
	for _, subscription := range engine.Subscriptions() {
		subjects = append(subjects, subscription.Subject)
		if !subscription.ExpiresAt.Equal(clock.Add(time.Minute)) {
			t.Errorf("%s expires %s", subscription.Subject, subscription.ExpiresAt)
		}
	}
	if want := []string{"candles.nse.infy.on_complete", "candles.nse.infy.periodic.5"}; !reflect.DeepEqual(subjects, want) {
		t.Fatalf("subjects = %v, want %v", subjects, want)
	}
}

func TestTicksFoldIntoMinuteCandles(t *testing.T) {
	clock := at("09:16:30")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"on_complete"}`)
	for _, tick := range []producer.Tick{
		tick("09:15:02", 1500, 1000),
		tick("09:15:30", 1504, 1800),
		tick("09:15:59", 1498, 2600),
		tick("09:16:00", 1499, 2700),
		tick("09:15:58", 1600, 2650), // late: 09:15 has closed
		tick("09:16:10", 1497, 3000),
	} {
		engine.Fold(tick)
	}
	clock = at("09:17:02")
	step(engine)
	got := publisher.take()
	if len(got) != 2 {
		t.Fatalf("publications = %+v", got)
	}
	for index, want := range []struct {
		minute                 string
		open, high, low, close float64
		volume                 int
	}{
		{"09:15:00", 1500, 1504, 1498, 1498, 1600},
		{"09:16:00", 1499, 1499, 1497, 1497, 400},
	} {
		candle := got[index].candle
		if !candle.Final || !candle.Timestamp.Equal(at(want.minute)) || candle.Open != want.open || candle.High != want.high ||
			candle.Low != want.low || candle.Close != want.close || candle.Volume != want.volume ||
			candle.InstrumentID != "NSE:INFY" || candle.Interval != models.CandleInterval1m {
			t.Errorf("candle %d = %+v, want %+v", index, candle, want)
		}
	}
	if engine.LateTicks() != 1 {
		t.Errorf("late ticks = %d, want 1", engine.LateTicks())
	}
}

func TestContinuousPublishesEachChangeThenTheFinal(t *testing.T) {
	clock := at("10:00:00")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"continuous"}`)
	engine.Fold(tick("10:00:01", 1500, 10))
	step(engine)
	step(engine) // nothing changed
	engine.Fold(tick("10:00:02", 1502, 20))
	step(engine)
	engine.Fold(tick("10:01:00", 1501, 30))
	step(engine)

	var got []string
	for _, item := range publisher.take() {
		if item.subject != "candles.nse.infy.continuous" {
			t.Fatalf("published on %s", item.subject)
		}
		got = append(got, item.candle.Timestamp.In(models.ExchangeLocation).Format("15:04")+" "+
			map[bool]string{true: "final", false: "open"}[item.candle.Final]+" "+
			strconv.FormatFloat(item.candle.Close, 'f', -1, 64))
	}
	want := []string{"10:00 open 1500", "10:00 open 1502", "10:00 final 1502", "10:01 open 1501"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

func TestPeriodicPublishesOnItsIntervalWhenChanged(t *testing.T) {
	clock := at("10:00:01")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"periodic","interval_seconds":5}`)
	engine.Fold(tick("10:00:01", 1500, 10))
	step(engine)
	if got := publisher.take(); len(got) != 0 {
		t.Fatalf("published before the interval: %+v", got)
	}
	clock = at("10:00:05")
	step(engine)
	if got := publisher.take(); len(got) != 1 || got[0].subject != "candles.nse.infy.periodic.5" || got[0].candle.Final {
		t.Fatalf("at the interval: %+v", got)
	}
	clock = at("10:00:10")
	step(engine)
	if got := publisher.take(); len(got) != 0 {
		t.Fatalf("published an unchanged candle: %+v", got)
	}
	engine.Fold(tick("10:00:11", 1503, 20))
	clock = at("10:00:12")
	step(engine)
	if got := publisher.take(); len(got) != 0 {
		t.Fatalf("published between intervals: %+v", got)
	}
	clock = at("10:00:15")
	step(engine)
	if got := publisher.take(); len(got) != 1 || got[0].candle.Close != 1503 {
		t.Fatalf("after a change: %+v", got)
	}
}

func TestOnCompleteClosesAfterTheGraceWithoutATick(t *testing.T) {
	clock := at("10:00:30")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"on_complete"}`)
	engine.Fold(tick("10:00:30", 1500, 10))
	clock = at("10:01:01")
	step(engine)
	if got := publisher.take(); len(got) != 0 {
		t.Fatalf("published inside the grace: %+v", got)
	}
	clock = at("10:01:02")
	step(engine)
	got := publisher.take()
	if len(got) != 1 || got[0].subject != "candles.nse.infy.on_complete" || !got[0].candle.Final {
		t.Fatalf("after the grace: %+v", got)
	}
}

func TestAFailedFinalIsPublishedLater(t *testing.T) {
	clock := at("10:00:30")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"on_complete"}`)
	engine.Fold(tick("10:00:30", 1500, 10))
	engine.Fold(tick("10:01:00", 1501, 20))
	publisher.err = errors.New("NATS down")
	step(engine)
	publisher.err = nil
	step(engine)
	if got := publisher.take(); len(got) != 1 || !got[0].candle.Timestamp.Equal(at("10:00:00")) {
		t.Fatalf("after recovery: %+v", got)
	}
	step(engine)
	if got := publisher.take(); len(got) != 0 {
		t.Fatalf("final published twice: %+v", got)
	}
}

func TestSubscriptionsExpireWithoutHeartbeats(t *testing.T) {
	clock := at("10:00:00")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"continuous"}`)
	clock = clock.Add(time.Minute)
	engine.Fold(tick("10:01:00", 1500, 10))
	step(engine)
	if len(engine.Subscriptions()) != 0 || len(publisher.take()) != 0 {
		t.Fatal("an expired subscription was kept or published to")
	}
}

func TestEngineLifecycle(t *testing.T) {
	clock := at("10:00:00")
	engine, publisher, _ := newEngine(t, &clock)
	heartbeat(t, engine, `{"ids":["NSE:INFY"],"mode":"continuous"}`)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err == nil {
		t.Fatal("started twice")
	}
	engine.Fold(tick("10:00:00", 1500, 10))
	deadline := time.After(2 * time.Second)
	for len(publisher.take()) == 0 {
		select {
		case <-deadline:
			t.Fatal("the running engine published nothing")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := engine.Close(context.Background()); err != nil || engine.Running() {
		t.Fatalf("close = %v, running = %v", err, engine.Running())
	}
}
