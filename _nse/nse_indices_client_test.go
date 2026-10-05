package nse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestIndicesClientDiscoversMetadataAndConstituents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mapping.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Trading_Index_Name":"NIFTY 50","Index_long_name":"Nifty 50"}]`))
		case "/indices/equity/broad-based-indices",
			"/indices/equity/sectoral-indices",
			"/indices/equity/thematic-indices",
			"/indices/equity/strategy-indices":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<div class="indicesTopics"><a href="/indices/equity/nifty-50">Nifty 50</a></div>`))
		case "/indices/equity/nifty-50":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<a href="../IndexConstituent/ind_nifty50list.csv">Download Constituents</a>`))
		case "/IndexConstituent/ind_nifty50list.csv":
			w.Header().Set("Content-Type", "text/csv")
			w.Write([]byte("Company Name,Symbol\nReliance Industries,RELIANCE\nTCS,TCS\nTCS Duplicate,TCS\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewIndicesClient(
		WithIndicesBaseURL(server.URL),
		WithIndexMappingURL(server.URL+"/mapping.json"),
		WithIndicesHTTPClient(server.Client()),
	)

	metadata, err := client.GetIndicesMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetIndicesMetadata() error = %v", err)
	}
	if len(metadata) != 1 {
		t.Fatalf("metadata length = %d, want 1", len(metadata))
	}

	item := metadata[0]
	if item.TradingSymbol != "nifty 50" {
		t.Fatalf("trading symbol = %q, want nifty 50", item.TradingSymbol)
	}
	if item.CollectionName != "NSE:NIFTY 50" {
		t.Fatalf("collection name = %q, want NSE:NIFTY 50", item.CollectionName)
	}

	instrumentIDs, err := client.GetConstituentIDs(context.Background(), item)
	if err != nil {
		t.Fatalf("GetConstituentIDs() error = %v", err)
	}
	wantIDs := []string{"NSE:RELIANCE", "NSE:TCS"}
	if !reflect.DeepEqual(instrumentIDs, wantIDs) {
		t.Fatalf("instrument IDs = %#v, want %#v", instrumentIDs, wantIDs)
	}
}
