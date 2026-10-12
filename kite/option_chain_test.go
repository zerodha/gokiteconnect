package kite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

// quoteServer answers quote requests with a last price for every requested
// instrument except those in missing, and records each request's instruments.
type quoteServer struct {
	*httptest.Server
	mu       sync.Mutex
	paths    []string
	requests [][]string
}

func newQuoteServer(t *testing.T, prices map[string]float64, missing ...string) *quoteServer {
	t.Helper()
	skip := make(map[string]bool, len(missing))
	for _, id := range missing {
		skip[id] = true
	}
	server := &quoteServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["i"]
		server.mu.Lock()
		server.paths = append(server.paths, r.URL.Path)
		server.requests = append(server.requests, ids)
		server.mu.Unlock()
		data := make(map[string]map[string]float64, len(ids))
		for _, id := range ids {
			if !skip[id] {
				data[id] = map[string]float64{"last_price": prices[id], "oi": 10}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *quoteServer) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sizes := make([]int, len(s.requests))
	for i, request := range s.requests {
		sizes[i] = len(request)
	}
	return sizes
}

func newQuoteTestClient(server *quoteServer) *Client {
	client := New("key")
	client.SetBaseURI(server.URL)
	client.quotePacer = nil
	return client
}

func TestQuoteEndpointsBatchAtKiteLimits(t *testing.T) {
	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("NSE:S%d", i)
	}
	for _, test := range []struct {
		name  string
		fetch func(*Client) (int, error)
		path  string
		sizes []int
	}{
		{"quote", func(c *Client) (int, error) { q, err := c.GetQuote(ids...); return len(q), err }, URIGetQuote, []int{500, 500, 1}},
		{"ltp", func(c *Client) (int, error) { q, err := c.GetLTP(ids...); return len(q), err }, URIGetLTP, []int{1000, 1}},
		{"ohlc", func(c *Client) (int, error) { q, err := c.GetOHLC(ids...); return len(q), err }, URIGetOHLC, []int{1000, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newQuoteServer(t, nil)
			got, err := test.fetch(newQuoteTestClient(server))
			if err != nil {
				t.Fatalf("fetch error = %v", err)
			}
			if got != len(ids) {
				t.Fatalf("quotes = %d, want %d merged across batches", got, len(ids))
			}
			if sizes := server.batchSizes(); fmt.Sprint(sizes) != fmt.Sprint(test.sizes) {
				t.Fatalf("batch sizes = %v, want %v", sizes, test.sizes)
			}
			if server.paths[0] != test.path {
				t.Fatalf("path = %q, want %q", server.paths[0], test.path)
			}
		})
	}
}

func TestGetQuoteDeduplicatesInstruments(t *testing.T) {
	server := newQuoteServer(t, nil)
	if _, err := newQuoteTestClient(server).GetQuote("NSE:INFY", "NSE:TCS", "NSE:INFY"); err != nil {
		t.Fatalf("GetQuote() error = %v", err)
	}
	if sizes := server.batchSizes(); len(sizes) != 1 || sizes[0] != 2 {
		t.Fatalf("batch sizes = %v, want [2]", sizes)
	}
}

func TestRequestPacerSpacesRequests(t *testing.T) {
	pacer := newRequestPacer(40 * time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	for range 3 {
		if err := pacer.wait(ctx); err != nil {
			t.Fatalf("wait() error = %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("three requests took %v, want at least 80ms", elapsed)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := pacer.wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait(cancelled) error = %v, want context.Canceled", err)
	}
}

// chainRepository serves one underlying and its options from memory.
type chainRepository struct {
	InstrumentRepository
	underlying models.Instrument
	options    []models.Instrument
	filters    []models.OptionsFilter
}

func (r *chainRepository) Get(_ context.Context, id models.InstrumentID) (models.Instrument, error) {
	if id != r.underlying.ID {
		return models.Instrument{}, models.ErrNotFound
	}
	return r.underlying, nil
}

func (r *chainRepository) GetOptions(_ context.Context, filter models.OptionsFilter) ([]models.Instrument, error) {
	r.filters = append(r.filters, filter)
	var options []models.Instrument
	for _, option := range r.options {
		if filter.Strikes != nil && filter.Strikes.Max != nil && option.Strike > *filter.Strikes.Max {
			continue
		}
		options = append(options, option)
	}
	return options, nil
}

func newChainRepository() *chainRepository {
	expiry := time.Date(2026, time.October, 27, 0, 0, 0, 0, indiaLocation)
	number := 1
	underlyingID := models.InstrumentID("NSE:NIFTY 50")
	repository := &chainRepository{underlying: models.Instrument{ID: underlyingID, Exchange: "NSE", Segment: "INDICES"}}
	for _, strike := range []float64{24800, 24900, 25000, 25100, 25200} {
		for _, optionType := range []string{"CE", "PE"} {
			repository.options = append(repository.options, models.Instrument{
				ID:             models.InstrumentID(fmt.Sprintf("NFO:NIFTY26OCT%.0f%s", strike, optionType)),
				Strike:         strike,
				InstrumentType: optionType,
				Expiry:         &expiry,
				ExpiryNumber:   &number,
				UnderlyingID:   &underlyingID,
			})
		}
	}
	// A call without a matching put.
	repository.options = append(repository.options, models.Instrument{
		ID: "NFO:NIFTY26OCT25300CE", Strike: 25300, InstrumentType: "CE", Expiry: &expiry, ExpiryNumber: &number, UnderlyingID: &underlyingID,
	})
	return repository
}

func TestGetOptionChainClampsAroundATM(t *testing.T) {
	repository := newChainRepository()
	server := newQuoteServer(t, map[string]float64{"NSE:NIFTY 50": 25080, "NFO:NIFTY26OCT25100CE": 120}, "NFO:NIFTY26OCT25000PE")
	client := newQuoteTestClient(server)
	client.SetInstrumentRepository(repository)

	chain, err := client.GetOptionChain(context.Background(), OptionChainRequest{UnderlyingID: "nse:nifty 50", ExpiryNumber: 1, StrikesAroundATM: 1})
	if err != nil {
		t.Fatalf("GetOptionChain() error = %v", err)
	}
	if chain.UnderlyingPrice != 25080 || chain.ATMStrike != 25100 || chain.ExpiryNumber != 1 || chain.Expiry.Day() != 27 {
		t.Fatalf("chain = price %v, ATM %v, expiry #%d %v", chain.UnderlyingPrice, chain.ATMStrike, chain.ExpiryNumber, chain.Expiry)
	}
	if got := repository.filters[0]; len(got.ExpiryNumbers) != 1 || got.ExpiryNumbers[0] != 1 || len(got.ExpiryDates) != 0 {
		t.Fatalf("options filter = %#v, want expiry number 1", got)
	}
	var strikes []float64
	for _, row := range chain.Rows {
		strikes = append(strikes, row.Strike)
	}
	if fmt.Sprint(strikes) != "[25000 25100 25200]" {
		t.Fatalf("strikes = %v, want 25000-25200", strikes)
	}
	if call := chain.Rows[1].Call; call == nil || call.Quote == nil || call.Quote.LastPrice != 120 || call.Quote.OI != 10 {
		t.Fatalf("ATM call = %#v", call)
	}
	if put := chain.Rows[0].Put; put == nil || put.Quote != nil {
		t.Fatalf("unquoted put = %#v, want a leg without a quote", put)
	}
	// The underlying is quoted first, then only the six clamped contracts.
	if sizes := server.batchSizes(); fmt.Sprint(sizes) != "[1 6]" {
		t.Fatalf("quote batch sizes = %v, want [1 6]", sizes)
	}
}

func TestGetOptionChainByDateWithStrikeRange(t *testing.T) {
	repository := newChainRepository()
	server := newQuoteServer(t, map[string]float64{"NSE:NIFTY 50": 24910})
	client := newQuoteTestClient(server)
	client.SetInstrumentRepository(repository)

	maxStrike := 24900.0
	expiry := time.Date(2026, time.October, 27, 15, 30, 0, 0, time.UTC)
	chain, err := client.GetOptionChain(context.Background(), OptionChainRequest{
		UnderlyingID: "NSE:NIFTY 50",
		ExpiryDate:   expiry,
		Strikes:      &models.StrikeRange{Max: &maxStrike},
	})
	if err != nil {
		t.Fatalf("GetOptionChain() error = %v", err)
	}
	if got := repository.filters[0]; len(got.ExpiryDates) != 1 || len(got.ExpiryNumbers) != 0 || got.Strikes == nil {
		t.Fatalf("options filter = %#v, want the expiry date and strike range", got)
	}
	if len(chain.Rows) != 2 || chain.ATMStrike != 24900 || chain.UnderlyingPrice != 24910 {
		t.Fatalf("chain = %d rows, ATM %v, price %v", len(chain.Rows), chain.ATMStrike, chain.UnderlyingPrice)
	}
	// Without ATM clamping the underlying shares the contracts' request.
	if sizes := server.batchSizes(); fmt.Sprint(sizes) != "[5]" {
		t.Fatalf("quote batch sizes = %v, want [5]", sizes)
	}
}

func TestGetOptionChainErrors(t *testing.T) {
	repository := newChainRepository()
	server := newQuoteServer(t, nil)
	client := newQuoteTestClient(server)
	ctx := context.Background()

	if _, err := client.GetOptionChain(ctx, OptionChainRequest{UnderlyingID: "NSE:NIFTY 50"}); !errors.Is(err, ErrNoInstrumentRepository) {
		t.Fatalf("no catalog error = %v", err)
	}
	client.SetInstrumentRepository(repository)
	if _, err := client.GetOptionChain(ctx, OptionChainRequest{UnderlyingID: "NSE:MISSING"}); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("missing underlying error = %v", err)
	}
	if _, err := client.GetOptionChain(ctx, OptionChainRequest{UnderlyingID: "NSE:NIFTY 50", StrikesAroundATM: -1}); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("negative clamp error = %v", err)
	}
	// The quote server has no price for the underlying, so there is no ATM.
	if _, err := client.GetOptionChain(ctx, OptionChainRequest{UnderlyingID: "NSE:NIFTY 50", StrikesAroundATM: 2}); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("unpriced underlying error = %v", err)
	}
	repository.options = nil
	if _, err := client.GetOptionChain(ctx, OptionChainRequest{UnderlyingID: "NSE:NIFTY 50"}); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("no options error = %v", err)
	}
}
