package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	marketdata "github.com/devshoe/gokiteconnect/market-data-service"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
)

// fakeCandles stores what syncs report, and blocks each sync until release
// gets its result.
type fakeCandles struct {
	mu      sync.Mutex
	stored  map[models.InstrumentID]bool
	started chan models.InstrumentID
	release chan error
}

func newFakeCandles() *fakeCandles {
	return &fakeCandles{stored: make(map[models.InstrumentID]bool), started: make(chan models.InstrumentID, 4), release: make(chan error)}
}

var (
	historyStart = time.Date(2015, time.January, 1, 9, 15, 0, 0, time.UTC)
	historyEnd   = time.Date(2026, time.October, 16, 15, 29, 0, 0, time.UTC)
)

func (f *fakeCandles) Availability(_ context.Context, ids []models.InstrumentID) ([]models.CandleAvailability, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []models.CandleAvailability
	for _, id := range ids {
		if f.stored[id] {
			rows = append(rows,
				models.CandleAvailability{InstrumentID: id, Interval: models.CandleInterval1m, HistStart: historyStart, HistEnd: historyEnd},
				models.CandleAvailability{InstrumentID: id, Interval: models.CandleInterval1d, HistStart: historyStart.AddDate(-20, 0, 0), HistEnd: historyEnd})
		}
	}
	return rows, nil
}

func (f *fakeCandles) Recent(_ context.Context, id models.InstrumentID, interval models.CandleInterval, _ int) ([]candles.Candle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stored[id] {
		return nil, nil
	}
	return []candles.Candle{{InstrumentID: id, Interval: interval, Timestamp: historyEnd, Open: 100, High: 110, Low: 95, Close: 105.25, Volume: 1000}}, nil
}

func (f *fakeCandles) SyncStandard(ctx context.Context, id models.InstrumentID, progress func(marketdata.SyncProgress)) ([]marketdata.SyncResult, error) {
	// Report a 1m sync from 2015 a little under half done.
	progress(marketdata.SyncProgress{
		InstrumentID: id, Interval: models.CandleInterval1m, Candles: 123456,
		From:    time.Date(2015, time.January, 1, 0, 0, 0, 0, models.ExchangeLocation),
		To:      time.Date(2025, time.January, 1, 0, 0, 0, 0, models.ExchangeLocation),
		Through: time.Date(2019, time.July, 2, 15, 29, 0, 0, models.ExchangeLocation),
	})
	f.started <- id
	var err error
	select {
	case err = <-f.release:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.stored[id] = true
	f.mu.Unlock()
	return []marketdata.SyncResult{
		{InstrumentID: id, Interval: models.CandleInterval1m, Candles: 1200},
		{InstrumentID: id, Interval: models.CandleInterval1d, Skipped: true},
	}, nil
}

// next waits for a sync to start, processing messages meanwhile: the
// command starting it may still be queued.
func (f *fakeCandles) next(h *harness) models.InstrumentID {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case id := <-f.started:
			return id
		case msg := <-h.msgs:
			h.dispatch(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting for a sync to start; screen:\n%s", h.screen())
		}
	}
}

func historyHarness(t *testing.T) (*harness, *fakeCatalog, *fakeListStore, *fakeCandles) {
	t.Helper()
	store, _ := niftyMarket()
	lists := &fakeListStore{}
	db := newFakeCandles()
	deps := explorerDeps(t, store, nil, lists, &fakePlacer{})
	deps.candles, deps.candlesPath = db, "candles.duckdb"
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	return h, store, lists, db
}

func TestHistoryBuildsTheDefaultSyncListAndSyncsOneAtATime(t *testing.T) {
	h, _, lists, db := historyHarness(t)

	// H builds the list the first time: the indices first, then the NSE
	// F&O underlyings, without repeating NIFTY 50.
	h.press("h")
	h.waitFor("the list", h.shows("Built DEFAULT SYNC · 2 instruments"))
	if got := lists.byName(marketdata.DefaultSyncListName).InstrumentIDs(); len(got) != 2 || got[0] != "NSE:NIFTY 50" || got[1] != "NSE:RELIANCE" {
		t.Fatalf("saved list = %v", got)
	}

	// s syncs the selected NIFTY 50; RELIANCE waits behind it.
	h.press("s")
	if id := db.next(h); id != "NSE:NIFTY 50" {
		t.Fatalf("first sync = %s", id)
	}
	h.press("down", "s", "s")
	if hs := h.a.history; len(hs.queue) != 1 || hs.queue[0] != "NSE:RELIANCE" {
		t.Fatalf("queue = %v", hs.queue)
	}
	if !strings.Contains(h.screen(), "Already syncing NSE:RELIANCE") {
		t.Errorf("a second s on a queued instrument wasn't refused:\n%s", h.screen())
	}

	// A failure is marked and counted while the queue carries on.
	db.release <- errors.New("no session")
	h.waitFor("the failure", h.shows("1 failed"))
	if id := db.next(h); id != "NSE:RELIANCE" {
		t.Fatalf("second sync = %s", id)
	}
	if !strings.Contains(h.screen(), "failed") || h.a.history.results["NSE:NIFTY 50"] == "" {
		t.Errorf("NIFTY 50 isn't marked failed:\n%s", h.screen())
	}
	db.release <- nil
	h.waitFor("the summary", h.shows("Synced 1 of 2 · 1 failed"))
	h.waitFor("the stored range", h.shows("16 Oct 2026 15:29"))
	h.waitFor("the latest candles", h.shows("105.25"))
	if hs := h.a.history; hs.syncing != "" || len(hs.queue) != 0 {
		t.Fatalf("syncing = %q, queue = %v after the queue emptied", hs.syncing, hs.queue)
	}
}

func TestHistoryRebuildsTheListOnR(t *testing.T) {
	h, store, lists, _ := historyHarness(t)
	h.press("h")
	h.waitFor("the list", h.shows("2 instruments"))
	first := lists.byName(marketdata.DefaultSyncListName).ID

	store.underlyings = append(store.underlyings, models.Instrument{ID: "NSE:TCS", Exchange: "NSE"})
	h.press("r")
	h.waitFor("the rebuilt list", h.shows("Built DEFAULT SYNC · 3 instruments"))
	if rebuilt := lists.byName(marketdata.DefaultSyncListName); rebuilt.ID != first || len(rebuilt.Items) != 3 {
		t.Fatalf("rebuilt list = %+v, first ID %s", rebuilt, first)
	}

	// Leaving and coming back keeps the stored list.
	h.press("q", "h")
	h.waitFor("the history screen", func() bool { return h.a.screen == screenHistory && h.idle() })
	if !strings.Contains(h.screen(), "3 instruments") {
		t.Errorf("history lost the list:\n%s", h.screen())
	}
}

func TestExplorerHOpensHistoryOnTheSelectedInstrument(t *testing.T) {
	h, _, _, _ := historyHarness(t)
	h.press("i")
	h.waitFor("the catalog", func() bool { return h.a.explorer.catalog != nil && h.idle() })
	h.waitFor("underlyings", h.shows("NIFTY 50"))
	h.a.explorer.results.setRows([]row{{ref: models.Instrument{ID: "NSE:TCS"}}})

	h.press("h")
	h.waitFor("the list", func() bool { return h.a.screen == screenHistory && h.idle() && len(h.a.history.list.Items) == 2 })
	if id, _ := h.a.history.grid.selectedRef().(models.InstrumentID); id != "NSE:TCS" {
		t.Fatalf("selected %q, want NSE:TCS", id)
	}
	if !strings.Contains(h.screen(), "NSE:TCS · not in list") {
		t.Errorf("TCS isn't marked as outside the list:\n%s", h.screen())
	}
	h.press("q")
	if h.a.screen != screenInstruments {
		t.Errorf("Q went to screen %d, want the explorer", h.a.screen)
	}
}

func TestHistorySyncAllShowsLiveProgressAndStops(t *testing.T) {
	h, store, _, db := historyHarness(t)
	store.underlyings = append(store.underlyings, models.Instrument{ID: "NSE:TCS", Exchange: "NSE"})
	h.press("h")
	h.waitFor("the list", h.shows("3 instruments"))

	// A asks first, then queues every instrument.
	h.press("a")
	if d, ok := h.a.overlay.(*dialog); !ok || !strings.Contains(d.text, "3 instruments") {
		t.Fatalf("A didn't ask: %#v", h.a.overlay)
	}
	h.press("enter")
	if id := db.next(h); id != "NSE:NIFTY 50" {
		t.Fatalf("first sync = %s", id)
	}
	h.waitFor("live progress", h.shows("Syncing NSE:NIFTY 50 · 1m 45% · through 02 Jul 2019 15:29 · +1.23L candles"))
	if !strings.Contains(h.screen(), "1/3") {
		t.Errorf("no batch count:\n%s", h.screen())
	}

	db.release <- nil
	h.waitFor("the next sync's progress", h.shows("2/3"))
	if id := db.next(h); id != "NSE:RELIANCE" {
		t.Fatalf("second sync = %s", id)
	}

	// X drops the queue and stops the running sync.
	h.press("x")
	h.waitFor("the stop", h.shows("Stopped syncing NSE:RELIANCE"))
	if hs := h.a.history; hs.syncing != "" || len(hs.queue) != 0 {
		t.Fatalf("syncing = %q, queue = %v after X", hs.syncing, hs.queue)
	}
	if !strings.Contains(h.screen(), "stopped") {
		t.Errorf("RELIANCE isn't marked stopped:\n%s", h.screen())
	}
}
