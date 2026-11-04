package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeStore records each write, failing while err is set.
type fakeStore struct {
	mu     sync.Mutex
	err    error
	writes [][]repository.LiveCandle
	wrote  chan struct{}
}

func newFakeStore() *fakeStore { return &fakeStore{wrote: make(chan struct{}, 16)} }

func (s *fakeStore) UpsertLiveCandles(_ context.Context, candles []repository.LiveCandle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.wrote <- struct{}{} }()
	if s.err != nil {
		return s.err
	}
	s.writes = append(s.writes, candles)
	return nil
}

func (s *fakeStore) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeStore) stored() [][]repository.LiveCandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

func (s *fakeStore) waitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-s.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a write")
	}
}

func at(clock string) time.Time {
	ts, err := time.ParseInLocation("15:04:05", clock, models.ExchangeLocation)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, time.October, 27, ts.Hour(), ts.Minute(), ts.Second(), 0, models.ExchangeLocation)
}

func tick(id, clock string, price float64, volume uint32) producer.Tick {
	t := producer.Tick{ID: id, BroadcastType: producer.BroadcastNew}
	t.InstrumentToken, t.LastPrice, t.VolumeTraded = 408065, price, volume
	t.Timestamp = models.Time{Time: at(clock)}
	return t
}

// startWriter runs the writer until the test stops it, and returns its
// result.
func startWriter(t *testing.T, store candleStore, ticks chan producer.Tick, batchSize int, every time.Duration) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWriter(ctx, store, ticks, batchSize, every, quiet) }()
	t.Cleanup(cancel)
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for the writer to stop")
			return nil
		}
	}
}

func TestTicksFoldIntoMinuteCandles(t *testing.T) {
	pending := newPendingCandles()
	for _, tick := range []producer.Tick{
		tick("NSE:INFY", "09:15:02", 1500, 1000),
		tick("NSE:TCS", "09:15:03", 4000, 50),
		tick("NSE:INFY", "09:15:30", 1504, 1800),
		tick("NSE:INFY", "09:15:59", 1498, 2600),
		tick("NSE:INFY", "09:16:00", 1499, 2700),
	} {
		pending.add(tick)
	}
	got := pending.list()
	want := []repository.LiveCandle{
		{Token: 408065, InstrumentID: "NSE:INFY", Minute: at("09:15:00"), Open: 1500, High: 1504, Low: 1498, Close: 1498, FirstVolume: 1000, LastVolume: 2600},
		{Token: 408065, InstrumentID: "NSE:TCS", Minute: at("09:15:00"), Open: 4000, High: 4000, Low: 4000, Close: 4000, FirstVolume: 50, LastVolume: 50},
		{Token: 408065, InstrumentID: "NSE:INFY", Minute: at("09:16:00"), Open: 1499, High: 1499, Low: 1499, Close: 1499, FirstVolume: 2700, LastVolume: 2700},
	}
	if len(got) != len(want) {
		t.Fatalf("candles = %+v", got)
	}
	for index := range want {
		if !got[index].Minute.Equal(want[index].Minute) {
			t.Fatalf("candle %d starts %s, want %s", index, got[index].Minute, want[index].Minute)
		}
		got[index].Minute = want[index].Minute
		if got[index] != want[index] {
			t.Fatalf("candle %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestOnlyNewMarketTicksAreWritable(t *testing.T) {
	repeat := tick("NSE:INFY", "10:00:00", 1500, 1)
	repeat.BroadcastType = producer.BroadcastRepeat
	unpriced := tick("NSE:INFY", "10:00:00", 0, 1)
	undated := tick("NSE:INFY", "10:00:00", 1500, 1)
	undated.Timestamp = models.Time{}
	inUTC := tick("NSE:INFY", "09:20:00", 1500, 1)
	inUTC.Timestamp.Time = inUTC.Timestamp.UTC()
	for name, test := range map[string]struct {
		tick producer.Tick
		want bool
	}{
		"at the open":   {tick("NSE:INFY", "09:15:00", 1500, 1), true},
		"in UTC":        {inUTC, true},
		"pre-open":      {tick("NSE:INFY", "09:14:59", 1500, 1), false},
		"a repeat":      {repeat, false},
		"without price": {unpriced, false},
		"without time":  {undated, false},
	} {
		if got := test.tick.InCandle(); got != test.want {
			t.Errorf("%s: InCandle = %v, want %v", name, got, test.want)
		}
	}
}

func TestTheWriterFlushesOnBatchSizeAndStop(t *testing.T) {
	store := newFakeStore()
	ticks := make(chan producer.Tick, 8)
	stop := startWriter(t, store, ticks, 2, time.Hour)

	ticks <- tick("NSE:INFY", "09:14:00", 1490, 1) // pre-open, skipped
	ticks <- tick("NSE:INFY", "09:15:01", 1500, 10)
	ticks <- tick("NSE:INFY", "09:15:02", 1501, 20)
	store.waitWrite(t)
	ticks <- tick("NSE:TCS", "09:15:05", 4000, 5)
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	writes := store.stored()
	if len(writes) != 2 || len(writes[0]) != 1 || writes[0][0].Close != 1501 || len(writes[1]) != 1 || writes[1][0].InstrumentID != "NSE:TCS" {
		t.Fatalf("writes = %+v", writes)
	}
}

func TestTheWriterFlushesOnTheInterval(t *testing.T) {
	store := newFakeStore()
	ticks := make(chan producer.Tick, 8)
	stop := startWriter(t, store, ticks, 1000, 20*time.Millisecond)

	ticks <- tick("NSE:INFY", "09:15:01", 1500, 10)
	store.waitWrite(t)
	if writes := store.stored(); len(writes) != 1 || writes[0][0].InstrumentID != "NSE:INFY" {
		t.Fatalf("writes = %+v", writes)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestAFailedWriteIsRetriedWithLaterTicks(t *testing.T) {
	store := newFakeStore()
	store.fail(errors.New("connection refused"))
	ticks := make(chan producer.Tick, 8)
	stop := startWriter(t, store, ticks, 1, time.Hour)

	// Each tick fills a batch, whose write fails.
	ticks <- tick("NSE:INFY", "09:15:01", 1500, 10)
	store.waitWrite(t)
	ticks <- tick("NSE:INFY", "09:15:40", 1510, 30)
	store.waitWrite(t)
	store.fail(nil)
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	writes := store.stored()
	if len(writes) != 1 || len(writes[0]) != 1 {
		t.Fatalf("writes = %+v", writes)
	}
	if candle := writes[0][0]; candle.Open != 1500 || candle.Close != 1510 || candle.FirstVolume != 10 || candle.LastVolume != 30 {
		t.Fatalf("after the failures stored %+v", candle)
	}
}

func TestTheLastWriteErrorIsReturned(t *testing.T) {
	store := newFakeStore()
	store.fail(errors.New("connection refused"))
	ticks := make(chan producer.Tick, 1)
	stop := startWriter(t, store, ticks, 1000, time.Hour)
	ticks <- tick("NSE:INFY", "09:15:01", 1500, 10)
	if err := stop(); err == nil {
		t.Fatal("stopped without reporting the unstored candles")
	}
}

func TestDecodeTickReadsTheTickerFormat(t *testing.T) {
	data := []byte(`{"id":"NSE:INFY","broadcast_type":"NEW","InstrumentToken":408065,"Timestamp":"2026-10-27T09:15:01+05:30","LastPrice":1500.5,"VolumeTraded":42}`)
	got, err := decodeTick(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "NSE:INFY" || got.LastPrice != 1500.5 || got.VolumeTraded != 42 || !got.Timestamp.Equal(at("09:15:01")) {
		t.Fatalf("decoded %+v", got)
	}
}
