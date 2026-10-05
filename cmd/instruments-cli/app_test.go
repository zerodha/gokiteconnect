package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
)

func TestInstrumentAppInitializesAndStopsCleanly(t *testing.T) {
	store := &fakeCatalog{
		lastRefreshed: time.Date(2026, time.September, 16, 9, 30, 0, 0, time.UTC),
		underlyings: []models.Instrument{
			{
				ID:             "NSE:NIFTY 50",
				TradingSymbol:  "NIFTY 50",
				DisplayName:    "NIFTY 50",
				InstrumentType: "INDICES",
				OptionsCount:   1,
			},
			{
				ID:             "NSE:SENSEX",
				TradingSymbol:  "SENSEX",
				DisplayName:    "SENSEX",
				InstrumentType: "INDICES",
				Strike:         123.45,
				FuturesCount:   2,
			},
		},
	}
	ui := newInstrumentApp(context.Background(), ":memory:", func(context.Context) (catalog, error) {
		return store, nil
	})
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(120, 40)
	ui.application.SetScreen(screen)

	done := make(chan error, 1)
	go func() { done <- ui.run() }()
	defer ui.stop()
	waitForApp(t, ui, func() bool {
		return ui.catalog == store && !ui.busy && len(ui.resultItems) == 2
	})
	if got := ui.results.GetCell(2, 3).Text; got != "123.45" {
		t.Fatalf("unselected strike cell = %q, want 123.45", got)
	}
	if got := ui.results.GetCell(2, 5).Text; got != "2" {
		t.Fatalf("unselected futures count = %q, want 2", got)
	}
	if header := ui.header.GetText(true); !strings.Contains(header, "2 underlyings") {
		t.Fatalf("header = %q, want underlying count", header)
	}

	ui.application.QueueEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone))
	waitForApp(t, ui, func() bool { return ui.application.GetFocus() == ui.search })
	ui.application.QueueEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	waitForApp(t, ui, func() bool {
		row, _ := ui.results.GetSelection()
		return ui.application.GetFocus() == ui.results && row == 1
	})

	ui.application.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
	if store.closeCalls != 1 {
		t.Fatalf("catalog close calls = %d, want 1", store.closeCalls)
	}
}

func waitForApp(t *testing.T, ui *instrumentApp, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var done bool
		ui.application.QueueUpdate(func() { done = ready() })
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("application did not reach the expected state")
}
