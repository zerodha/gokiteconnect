package kiteconnect

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

// screenerServer returns a client pointed at a test server that checks the
// request method and path, hands the parsed params to check, and replies with data.
func screenerServer(t *testing.T, method, path string, check func(url.Values), data string) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method || r.URL.EscapedPath() != path {
			t.Errorf("got %s %s, want %s %s", r.Method, r.URL.EscapedPath(), method, path)
		}

		params := r.URL.Query()
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("got Content-Type %q, want form-encoded", ct)
			}
			body, _ := ioutil.ReadAll(r.Body)
			params, _ = url.ParseQuery(string(body))
		}
		if check != nil {
			check(params)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"success","data":%s}`, data)
	}))
	t.Cleanup(server.Close)

	kc := New("test_api_key")
	kc.SetBaseURI(server.URL)
	kc.SetAccessToken("test_access_token")
	return kc
}

func expectParams(t *testing.T, want url.Values) func(url.Values) {
	return func(got url.Values) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got params %v, want %v", got, want)
		}
	}
}

const screenerResultsJSON = `{
	"results": [{
		"segment": "NSE",
		"instrument_token": "738561",
		"tradingsymbol": "RELIANCE",
		"name": "RELIANCE INDUSTRIES",
		"last_trade_time": "2026-11-19T15:29:59+05:30",
		"last_price": 1450.5,
		"volume": 1200000,
		"change_percent": 1.25,
		"sector": "Energy",
		"pe": null,
		"tags": ["Nifty 50"]
	}],
	"total": 41,
	"page": 2,
	"per_page": 20,
	"total_pages": 3
}`

func TestGetScreenerMeta(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodGet, URIScreenerMeta, nil, `{
		"filters": {
			"pe": {"name": "P/E", "type": "range", "values": [0, 250.5]},
			"sector": {"name": "Sector", "type": "distinct", "values": ["Energy", "IT"]}
		},
		"default": ["pe"]
	}`)

	meta, err := kc.GetScreenerMeta()
	if err != nil {
		t.Fatalf("GetScreenerMeta: %v", err)
	}
	if meta.Filters["pe"].Type != "range" || meta.Filters["sector"].Name != "Sector" {
		t.Errorf("filters not parsed: %+v", meta.Filters)
	}
	if !reflect.DeepEqual(meta.Default, []string{"pe"}) {
		t.Errorf("got default %v", meta.Default)
	}
}

func TestRunScreener(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodPost, URIScreenerInstruments, expectParams(t, url.Values{
		"query":    {"volume>10000&pe<15"},
		"tags":     {"Nifty 50", "Nifty Bank"},
		"order_by": {"change_percent"},
		"order":    {"desc"},
		"page":     {"2"},
		"per_page": {"20"},
	}), screenerResultsJSON)

	res, err := kc.RunScreener("volume>10000&pe<15", ScreenerParams{
		Tags:    []string{"Nifty 50", "Nifty Bank"},
		OrderBy: "change_percent",
		Order:   "desc",
		Page:    2,
		PerPage: 20,
	})
	if err != nil {
		t.Fatalf("RunScreener: %v", err)
	}
	if res.Total != 41 || res.Page != 2 || res.TotalPages != 3 || len(res.Results) != 1 {
		t.Fatalf("results not parsed: %+v", res)
	}
	inst := res.Results[0]
	if inst.TradingSymbol != "RELIANCE" || inst.LastPrice != 1450.5 || inst.PE != 0 || inst.LastTradeTime.IsZero() {
		t.Errorf("instrument not parsed: %+v", inst)
	}
}

func TestRunScreenerAll(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodPost, URIScreenerInstruments, expectParams(t, url.Values{
		"query": {"pe<15"},
		"all":   {"true"},
	}), screenerResultsJSON)

	if _, err := kc.RunScreener("pe<15", ScreenerParams{All: true, Page: 3, PerPage: 50}); err != nil {
		t.Fatalf("RunScreener: %v", err)
	}
}

func TestSearchScreenerInstruments(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodGet, URIScreenerSearch, expectParams(t, url.Values{
		"q":     {"REL"},
		"limit": {"5"},
	}), `{"results": [{"segment": "NSE", "instrument_token": "738561", "tradingsymbol": "RELIANCE", "last_price": 1450.5}]}`)

	res, err := kc.SearchScreenerInstruments("REL", 5)
	if err != nil {
		t.Fatalf("SearchScreenerInstruments: %v", err)
	}
	if len(res) != 1 || res[0].TradingSymbol != "RELIANCE" || res[0].LastPrice != 1450.5 {
		t.Errorf("results not parsed: %+v", res)
	}
}

func TestGetScreenerPresets(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodGet, URIScreenerPresets, nil, `{"presets": [{
		"key": "top_gainers", "name": "Top gainers", "query": "change_percent>0",
		"order_by": "change_percent", "order": "desc", "type": "price_action"
	}]}`)

	presets, err := kc.GetScreenerPresets()
	if err != nil {
		t.Fatalf("GetScreenerPresets: %v", err)
	}
	if len(presets) != 1 || presets[0].Key != "top_gainers" || presets[0].Order != "desc" {
		t.Errorf("presets not parsed: %+v", presets)
	}
}

func TestRunScreenerPreset(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodGet, fmt.Sprintf(URIScreenerPreset, "top_gainers"), expectParams(t, url.Values{
		"tags":     {"Nifty 50"},
		"per_page": {"20"},
	}), screenerResultsJSON)

	res, err := kc.RunScreenerPreset("top_gainers", ScreenerParams{Tags: []string{"Nifty 50"}, PerPage: 20})
	if err != nil {
		t.Fatalf("RunScreenerPreset: %v", err)
	}
	if len(res.Results) != 1 {
		t.Errorf("results not parsed: %+v", res)
	}
}

const screenerQueryJSON = `{
	"id": "550e8400-e29b-41d4-a716-446655440000",
	"name": "High volume",
	"query": "volume>100000",
	"tags": ["Nifty 50"],
	"created_at": "2026-11-19T10:00:00+05:30",
	"updated_at": "2026-11-19T10:00:00+05:30"
}`

func TestGetScreenerQueries(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodGet, URIScreenerQueries, nil, "["+screenerQueryJSON+"]")

	queries, err := kc.GetScreenerQueries()
	if err != nil {
		t.Fatalf("GetScreenerQueries: %v", err)
	}
	if len(queries) != 1 || queries[0].ID != testUUID || queries[0].CreatedAt.IsZero() {
		t.Errorf("queries not parsed: %+v", queries)
	}
}

func TestCreateScreenerQuery(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodPost, URIScreenerQueries, expectParams(t, url.Values{
		"name":  {"High volume"},
		"query": {"volume>100000"},
		"tags":  {"Nifty 50"},
	}), screenerQueryJSON)

	q, err := kc.CreateScreenerQuery(ScreenerQueryParams{Name: "High volume", Query: "volume>100000", Tags: []string{"Nifty 50"}})
	if err != nil {
		t.Fatalf("CreateScreenerQuery: %v", err)
	}
	if q.ID != testUUID || !reflect.DeepEqual(q.Tags, []string{"Nifty 50"}) {
		t.Errorf("query not parsed: %+v", q)
	}
}

func TestModifyScreenerQuery(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodPut, fmt.Sprintf(URIScreenerQuery, testUUID), expectParams(t, url.Values{
		"name":  {"High volume"},
		"query": {"volume>500000"},
	}), screenerQueryJSON)

	if _, err := kc.ModifyScreenerQuery(testUUID, ScreenerQueryParams{Name: "High volume", Query: "volume>500000"}); err != nil {
		t.Fatalf("ModifyScreenerQuery: %v", err)
	}
}

func TestDeleteScreenerQuery(t *testing.T) {
	t.Parallel()
	kc := screenerServer(t, http.MethodDelete, fmt.Sprintf(URIScreenerQuery, testUUID), nil, `{"status": "deleted"}`)

	if err := kc.DeleteScreenerQuery(testUUID); err != nil {
		t.Fatalf("DeleteScreenerQuery: %v", err)
	}
}

func TestScreenerError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"status":"error","error_type":"InputException","message":"invalid field: foo"}`)
	}))
	defer server.Close()

	kc := New("test_api_key")
	kc.SetBaseURI(server.URL)

	_, err := kc.RunScreener("foo>1", ScreenerParams{})
	e, ok := err.(Error)
	if !ok || e.ErrorType != InputError || e.Message != "invalid field: foo" {
		t.Errorf("got error %#v, want InputError", err)
	}
}
