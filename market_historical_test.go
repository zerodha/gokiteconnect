package kiteconnect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/candles"
	"github.com/devshoe/gokiteconnect/models"
)

func TestHistoricalBatchDuration(t *testing.T) {
	tests := []struct {
		interval string
		days     int
		ok       bool
	}{
		{interval: "minute", days: 60, ok: true},
		{interval: "2minute", days: 60, ok: true},
		{interval: "3minute", days: 100, ok: true},
		{interval: "4minute", days: 100, ok: true},
		{interval: "5minute", days: 100, ok: true},
		{interval: "10minute", days: 100, ok: true},
		{interval: "15minute", days: 200, ok: true},
		{interval: "30minute", days: 200, ok: true},
		{interval: "60minute", days: 400, ok: true},
		{interval: "2hour", days: 400, ok: true},
		{interval: "3hour", days: 400, ok: true},
		{interval: "4hour", days: 400, ok: true},
		{interval: "day", days: 2000, ok: true},
		{interval: "week", days: 2000, ok: true},
		{interval: "custom", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.interval, func(t *testing.T) {
			got, ok := historicalBatchDuration(tt.interval)
			if ok != tt.ok {
				t.Fatalf("historicalBatchDuration(%q) ok = %t, want %t", tt.interval, ok, tt.ok)
			}
			want := time.Duration(tt.days) * 24 * time.Hour
			if got != want {
				t.Fatalf("historicalBatchDuration(%q) = %s, want %s", tt.interval, got, want)
			}
		})
	}
}

func TestGetHistoricalDataBatchesLargeRangesInternally(t *testing.T) {
	const dateTimeLayout = "2006-01-02 15:04:05"
	location := time.FixedZone("IST", 5*60*60+30*60)
	start := time.Date(2026, time.January, 1, 9, 15, 0, 0, location)
	batchBoundary := start.Add(60 * 24 * time.Hour)
	end := batchBoundary.Add(24 * time.Hour)

	type request struct {
		path  string
		query url.Values
	}
	var (
		mu       sync.Mutex
		requests []request
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, request{path: r.URL.Path, query: r.URL.Query()})
		mu.Unlock()

		batchStart, err := time.ParseInLocation(dateTimeLayout, r.URL.Query().Get("from"), location)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"candles": [][]interface{}{{
					batchStart.Format("2006-01-02T15:04:05-0700"),
					100.0,
					102.0,
					99.0,
					101.0,
					1000,
					25,
				}},
			},
		})
	}))
	defer server.Close()

	client := New("test_api_key")
	client.SetBaseURI(server.URL)

	candles, err := client.GetHistoricalData(123, "minute", start, end, true, true)
	if err != nil {
		t.Fatalf("GetHistoricalData() error = %v", err)
	}

	mu.Lock()
	gotRequests := append([]request(nil), requests...)
	mu.Unlock()

	if len(gotRequests) != 2 {
		t.Fatalf("request count = %d, want 2", len(gotRequests))
	}
	wantBounds := [][2]time.Time{
		{start, batchBoundary},
		{batchBoundary, end},
	}
	for i, got := range gotRequests {
		if got.path != "/instruments/historical/123/minute" {
			t.Errorf("request %d path = %q, want historical-data path", i, got.path)
		}
		if got.query.Get("from") != wantBounds[i][0].Format(dateTimeLayout) {
			t.Errorf("request %d from = %q, want %q", i, got.query.Get("from"), wantBounds[i][0].Format(dateTimeLayout))
		}
		if got.query.Get("to") != wantBounds[i][1].Format(dateTimeLayout) {
			t.Errorf("request %d to = %q, want %q", i, got.query.Get("to"), wantBounds[i][1].Format(dateTimeLayout))
		}
		if got.query.Get("continuous") != "1" || got.query.Get("oi") != "1" {
			t.Errorf("request %d flags = continuous:%q oi:%q, want both 1", i, got.query.Get("continuous"), got.query.Get("oi"))
		}
	}

	if len(candles) != 2 {
		t.Fatalf("candle count = %d, want 2 combined batches", len(candles))
	}
	if !candles[0].Date.Equal(start) || !candles[1].Date.Equal(batchBoundary) {
		t.Fatalf("candle dates = [%s, %s], want [%s, %s]", candles[0].Date.Time, candles[1].Date.Time, start, batchBoundary)
	}
}

func TestGetHistoricalDataKeepsUnknownIntervalsAsSingleRequest(t *testing.T) {
	var (
		mu           sync.Mutex
		requestCount int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"candles":[]}}`))
	}))
	defer server.Close()

	client := New("test_api_key")
	client.SetBaseURI(server.URL)
	start := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

	if _, err := client.GetHistoricalData(123, "custom", start, start.Add(5000*24*time.Hour), false, false); err != nil {
		t.Fatalf("GetHistoricalData() error = %v", err)
	}

	mu.Lock()
	got := requestCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("request count = %d, want 1 for an unknown interval", got)
	}
}

// symbolRepository lists a fixed catalog; other repository methods panic
// through the nil embedded interface if called.
type symbolRepository struct {
	InstrumentRepository
	instruments []models.Instrument
}

func (r symbolRepository) List(context.Context, []string) ([]models.Instrument, error) {
	return r.instruments, nil
}

func TestGetHistoricalDataBySymbol(t *testing.T) {
	location := time.FixedZone("IST", 5*60*60+30*60)
	start := time.Date(2026, time.January, 1, 9, 15, 0, 0, location)

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"candles":[["2026-01-01T09:15:00+0530",100,102,99,101,1000,25]]}}`))
	}))
	defer server.Close()

	client := New("test_api_key")
	client.SetBaseURI(server.URL)
	client.SetInstrumentRepository(symbolRepository{instruments: []models.Instrument{
		{ID: "NSE:INFY", InstrumentToken: 408065},
	}})
	ctx := context.Background()

	series, err := client.GetHistoricalDataBySymbol(ctx, "nse:infy", "minute", start, start.Add(time.Hour), false, true)
	if err != nil {
		t.Fatalf("GetHistoricalDataBySymbol() error = %v", err)
	}
	if gotPath != "/instruments/historical/408065/minute" {
		t.Fatalf("request path = %q, want token 408065", gotPath)
	}
	want := candles.Candle{Timestamp: start, Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000, OI: 25}
	if len(series.Data) != 1 || !series.Data[0].Timestamp.Equal(want.Timestamp) || series.Data[0].Close != want.Close || series.Data[0].OI != want.OI {
		t.Fatalf("candles = %#v, want [%#v]", series.Data, want)
	}

	if _, err := client.GetHistoricalDataBySymbol(ctx, "NSE:MISSING", "minute", start, start.Add(time.Hour), false, false); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("unknown symbol error = %v, want ErrNotFound", err)
	}
	if _, err := New("test_api_key").GetHistoricalDataBySymbol(ctx, "NSE:INFY", "minute", start, start.Add(time.Hour), false, false); !errors.Is(err, ErrNoInstrumentRepository) {
		t.Fatalf("no repository error = %v, want ErrNoInstrumentRepository", err)
	}
}
