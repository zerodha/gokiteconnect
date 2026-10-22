package kite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

// screenerServer serves pages of total results, perPage at a time, and
// records each request it gets.
func screenerServer(t *testing.T, total int, requests *[]ScreenerRequest) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != URIScreenerInstruments {
			http.NotFound(w, r)
			return
		}
		for header, want := range map[string]string{
			"X-CSRFToken": "public", "X-Kite-UserID": "CC0006", "Content-Type": "application/json",
			"Authorization": "enctoken enc",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), name+"/"+version) {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		for cookie, want := range map[string]string{
			"kf_session": "session", "public_token": "public", "enctoken": "enc", "user_id": "CC0006",
		} {
			if got, err := r.Cookie(cookie); err != nil || got.Value != want {
				t.Errorf("cookie %s = %v, %v; want %q", cookie, got, err, want)
			}
		}
		var request ScreenerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		*requests = append(*requests, request)

		pages := (total + request.PerPage - 1) / request.PerPage
		var results []ScreenerInstrument
		for i := (request.Page - 1) * request.PerPage; i < min(request.Page*request.PerPage, total); i++ {
			results = append(results, ScreenerInstrument{Segment: "NSE", TradingSymbol: string(rune('A' + i)), MarketCap: float64(total - i)})
		}
		_ = json.NewEncoder(w).Encode(ScreenerResponse{Status: "success", Data: ScreenerData{
			Page: request.Page, PerPage: request.PerPage, Total: total, TotalPages: pages, Results: results,
		}})
	}))
	t.Cleanup(server.Close)
	return server
}

func testScreener(server *httptest.Server) *ScreenerClient {
	client := New("key")
	client.SetAppName("test")
	client.SetScreenerSession("CC0006", "enc", "session", "public")
	screener := client.ScreenerClient()
	screener.SetBaseURI(server.URL)
	return screener
}

func TestScreenerGetInstruments(t *testing.T) {
	var requests []ScreenerRequest
	screener := testScreener(screenerServer(t, 3, &requests))

	page, err := screener.GetInstruments(context.Background(), ScreenerRequest{
		Query: "market_cap>=0", OrderBy: "market_cap", Order: "desc",
	})
	if err != nil {
		t.Fatalf("GetInstruments() error = %v", err)
	}
	if page.Total != 3 || len(page.Results) != 3 || page.Results[0].TradingSymbol != "A" || page.Results[0].Exchange() != "NSE" {
		t.Fatalf("GetInstruments() = %+v", page)
	}
	// Defaults fill a zero page and page size, and tags are sent as [].
	got := requests[0]
	if got.Page != 1 || got.PerPage != ScreenerMaxPerPage || got.Tags == nil || got.Query != "market_cap>=0" || got.Order != "desc" {
		t.Fatalf("request = %+v", got)
	}
}

func TestScreenerAllInstrumentsPages(t *testing.T) {
	var requests []ScreenerRequest
	screener := testScreener(screenerServer(t, 5, &requests))

	all, err := screener.AllInstruments(context.Background(), ScreenerRequest{PerPage: 2}, 0)
	if err != nil || len(all) != 5 || all[4].TradingSymbol != "E" || len(requests) != 3 {
		t.Fatalf("AllInstruments() = %d results, %v after %d requests; want 5 after 3", len(all), err, len(requests))
	}
	requests = nil
	limited, err := screener.AllInstruments(context.Background(), ScreenerRequest{PerPage: 2}, 3)
	if err != nil || len(limited) != 3 || len(requests) != 2 {
		t.Fatalf("limited AllInstruments() = %d results, %v after %d requests; want 3 after 2", len(limited), err, len(requests))
	}
}

func TestScreenerDefaultsToKiteWebAPI(t *testing.T) {
	if got := NewScreenerClient("CC0006", "enc", "session", "public").baseURI; got != "https://kite.zerodha.com/oms" {
		t.Fatalf("baseURI = %q, want https://kite.zerodha.com/oms", got)
	}
}

func TestScreenerErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := New("key").ScreenerClient().GetInstruments(ctx, ScreenerRequest{}); !errors.Is(err, ErrScreenerSession) {
		t.Fatalf("sessionless GetInstruments() error = %v, want ErrScreenerSession", err)
	}

	for name, test := range map[string]struct {
		status  int
		body    string
		want    string
		session bool
	}{
		"expired session page": {http.StatusForbidden, "<html>login</html>", "HTTP 403: Forbidden", true},
		"error envelope":       {http.StatusBadRequest, `{"status":"error","error_type":"InputException","message":"bad query"}`, "HTTP 400: bad query", false},
		"failed status":        {http.StatusOK, `{"status":"error","error_type":"GeneralException","message":"try later"}`, "try later", false},
		"bad JSON":             {http.StatusOK, "nope", "decode screener response", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(test.body))
		}))
		_, err := testScreener(server).GetInstruments(ctx, ScreenerRequest{Query: "pe<0"})
		server.Close()
		if err == nil || !strings.Contains(err.Error(), test.want) || errors.Is(err, ErrScreenerSession) != test.session {
			t.Errorf("%s: error = %v, want %q (session error %v)", name, err, test.want, test.session)
		}
	}
}

func TestScreenerInstrumentFundamentals(t *testing.T) {
	result := ScreenerInstrument{
		Segment: "NSE", InstrumentToken: "738561", TradingSymbol: "RELIANCE", ISIN: "INE002A01018", Sector: "Oil & Gas",
		LastPrice: 1400, MarketCap: 1900000, PE: 25, DebtToEquity: 0.4, FundamentalUpdatedAt: "2026-10-22T07:00:14+05:30",
		Tags: []string{"Nifty 50"},
	}
	got, err := result.Fundamentals()
	if err != nil {
		t.Fatalf("Fundamentals() error = %v", err)
	}
	if got.InstrumentToken != 738561 || got.ID() != "NSE:RELIANCE" || got.MarketCap != 1900000 || got.DebtToEquity != 0.4 ||
		!got.UpdatedAt.Equal(time.Date(2026, 10, 22, 1, 30, 14, 0, time.UTC)) || len(got.Tags) != 1 {
		t.Fatalf("Fundamentals() = %+v", got)
	}
	// An ETF has no fundamentals or tags.
	if etf, err := (ScreenerInstrument{Segment: "NSE", InstrumentToken: "194809857", TradingSymbol: "GROWWPSE"}).Fundamentals(); err != nil || !etf.UpdatedAt.IsZero() || etf.Tags == nil {
		t.Fatalf("ETF Fundamentals() = %+v, %v", etf, err)
	}
	for _, bad := range []ScreenerInstrument{
		{TradingSymbol: "X", InstrumentToken: ""},
		{TradingSymbol: "X", InstrumentToken: "1", FundamentalUpdatedAt: "yesterday"},
	} {
		if _, err := bad.Fundamentals(); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("Fundamentals(%+v) error = %v, want ErrInvalidInput", bad, err)
		}
	}
}
