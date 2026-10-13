package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

func TestSearchCoordinatorDiscardsSupersededResults(t *testing.T) {
	coordinator := newSearchCoordinator(0)
	defer func() {
		coordinator.shutdown()
		coordinator.wait()
	}()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan searchResult, 2)
	var once sync.Once
	run := func(_ context.Context, query string, _ bool) ([]models.Instrument, error) {
		if query == "first" {
			once.Do(func() { close(firstStarted) })
			<-releaseFirst
		}
		return []models.Instrument{{ID: models.InstrumentID("NSE:" + query)}}, nil
	}

	coordinator.schedule(context.Background(), "first", false, run, func(result searchResult) { delivered <- result })
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first search did not start")
	}
	coordinator.schedule(context.Background(), "second", false, run, func(result searchResult) { delivered <- result })
	select {
	case result := <-delivered:
		if result.query != "second" {
			t.Fatalf("delivered query = %q, want second", result.query)
		}
	case <-time.After(time.Second):
		t.Fatal("second search was not delivered")
	}
	close(releaseFirst)
	select {
	case result := <-delivered:
		t.Fatalf("stale search was delivered: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSearchCoordinatorDebouncesAndMarksUnderlyings(t *testing.T) {
	coordinator := newSearchCoordinator(25 * time.Millisecond)
	defer func() {
		coordinator.shutdown()
		coordinator.wait()
	}()
	delivered := make(chan searchResult, 2)
	runs := make(chan string, 2)
	run := func(_ context.Context, query string, underlyings bool) ([]models.Instrument, error) {
		if underlyings {
			runs <- "underlyings"
		} else {
			runs <- query
		}
		return nil, nil
	}

	coordinator.schedule(context.Background(), "nif", false, run, func(result searchResult) { delivered <- result })
	coordinator.schedule(context.Background(), "nifty", false, run, func(result searchResult) { delivered <- result })
	select {
	case query := <-runs:
		if query != "nifty" {
			t.Fatalf("debounced query = %q, want nifty", query)
		}
	case <-time.After(time.Second):
		t.Fatal("debounced search did not run")
	}
	select {
	case result := <-delivered:
		if result.query != "nifty" || result.underlyings {
			t.Fatalf("search result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("debounced result was not delivered")
	}

	coordinator.schedule(context.Background(), "", true, run, func(result searchResult) { delivered <- result })
	select {
	case query := <-runs:
		if query != "underlyings" {
			t.Fatalf("empty query mode = %q", query)
		}
	case <-time.After(time.Second):
		t.Fatal("underlyings query did not run immediately")
	}
	select {
	case result := <-delivered:
		if !result.underlyings || result.query != "" {
			t.Fatalf("underlyings result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("underlyings result was not delivered")
	}
}
