package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
)

func TestKiteSourceStreamsOneBatchPerWindow(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		from, err := time.ParseInLocation(time.DateTime, r.URL.Query().Get("from"), models.ExchangeLocation)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"candles": [][]any{
				{from.Format("2006-01-02T15:04:05-0700"), 100.0, 102.0, 99.0, 101.0, 1000, 0},
			}},
		})
	}))
	defer server.Close()

	clients, sleeps := 0, 0
	source := &KiteSource{
		client: func(context.Context) (*kite.Client, error) {
			clients++
			client := kite.New("test_api_key")
			client.SetBaseURI(server.URL)
			return client, nil
		},
		token: func(_ context.Context, id models.InstrumentID) (int64, error) {
			if id != "NSE:INFY" {
				return 0, models.ErrNotFound
			}
			return 408065, nil
		},
		sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
	}

	from := time.Date(2026, time.January, 1, 9, 15, 0, 0, models.ExchangeLocation)
	to := from.Add(90 * 24 * time.Hour) // a minute window is 60 days: two batches
	var batches [][]candles.Candle
	err := source.StreamCandles(context.Background(), "NSE:INFY", models.CandleInterval1m, from, to, func(batch []candles.Candle) error {
		batches = append(batches, batch)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || sleeps != 1 {
		t.Fatalf("batches = %d, sleeps = %d, want 2 and 1", len(batches), sleeps)
	}
	got := batches[0][0]
	if got.InstrumentID != "NSE:INFY" || got.InstrumentToken != 408065 || got.Interval != models.CandleInterval1m || !got.Timestamp.Equal(from) || got.Close != 101 || got.Volume != 1000 {
		t.Fatalf("first candle = %#v", got)
	}
	if paths[0] != "/instruments/historical/408065/minute" {
		t.Fatalf("path = %s", paths[0])
	}

	// An unknown instrument fails before reading the data user's session.
	err = source.StreamCandles(context.Background(), "NSE:NOPE", models.CandleInterval1m, from, to, func([]candles.Candle) error { return nil })
	if !errors.Is(err, models.ErrNotFound) || clients != 1 {
		t.Fatalf("unknown instrument error = %v, clients = %d", err, clients)
	}
}
