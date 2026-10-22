package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/devshoe/gokiteconnect/credentials"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/devshoe/gokiteconnect/kite"
	instrumentstore "github.com/devshoe/gokiteconnect/repository"
)

// fundamentalsCredentials stores a data user, with a web session when web
// is set, and returns the database's path.
func fundamentalsCredentials(t *testing.T, web bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.sqlite3")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := credentialstore.NewUserSQLiteRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	user := credentials.Credentials{
		UserID: "CC0006", Password: "test", TOTPSecret: "test", IsAPIUser: true, DataSubscription: true,
		APIKey: "key", APISecret: "secret", AccessToken: "access",
	}
	if web {
		user.EncToken, user.KFSessionToken, user.PublicToken = "enc", "session", "public"
	}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalFundamentalsRefresh(t *testing.T) {
	const total = 150
	var pages []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "enctoken enc" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var request kite.ScreenerRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Query == "" || request.PerPage != kite.ScreenerMaxPerPage {
			t.Errorf("request = %+v", request)
		}
		pages = append(pages, request.Page)
		var results []kite.ScreenerInstrument
		for i := (request.Page - 1) * request.PerPage; i < min(request.Page*request.PerPage, total); i++ {
			result := kite.ScreenerInstrument{Segment: "NSE", InstrumentToken: strconv.Itoa(1000 + i), TradingSymbol: "S" + strconv.Itoa(i), MarketCap: float64(total - i)}
			if i < 120 {
				result.Tags = []string{"Nifty 500"}
				result.FundamentalUpdatedAt = "2026-10-22T07:00:14+05:30"
			}
			results = append(results, result)
		}
		_ = json.NewEncoder(w).Encode(kite.ScreenerResponse{Status: "success", Data: kite.ScreenerData{
			Page: request.Page, PerPage: request.PerPage, Total: total, TotalPages: 2, Results: results,
		}})
	}))
	defer server.Close()

	store := localFundamentals{
		path:            filepath.Join(t.TempDir(), "fundamentals.sqlite3"),
		credentialsPath: fundamentalsCredentials(t, true),
		screenerURI:     server.URL,
	}
	var progress []string
	summary, err := store.Refresh(context.Background(), func(page, pages int) {
		progress = append(progress, strconv.Itoa(page)+"/"+strconv.Itoa(pages))
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if summary.count != total || summary.indexCount != 120 || summary.refreshedAt.IsZero() {
		t.Fatalf("Refresh() = %+v; want %d instruments, 120 in the Nifty 500", summary, total)
	}
	if strings.Join(progress, " ") != "1/2 2/2" || len(pages) != 2 {
		t.Fatalf("progress = %v after pages %v", progress, pages)
	}
	if again, err := store.Summary(context.Background()); err != nil || again != summary {
		t.Fatalf("Summary() = %+v, %v; want %+v", again, err, summary)
	}

	repo, err := instrumentstore.NewFundamentalsSQLiteRepository(context.Background(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if got, err := repo.Get(context.Background(), 1000); err != nil || got.TradingSymbol != "S0" || got.UpdatedAt.IsZero() {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
}

func TestLocalFundamentalsNeedsWebSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the screener was called without a web session")
	}))
	defer server.Close()
	store := localFundamentals{
		path:            filepath.Join(t.TempDir(), "fundamentals.sqlite3"),
		credentialsPath: fundamentalsCredentials(t, false),
		screenerURI:     server.URL,
	}
	if _, err := store.Refresh(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "no web session") {
		t.Fatalf("Refresh() error = %v, want no web session", err)
	}
}
