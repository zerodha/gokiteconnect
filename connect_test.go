package kiteconnect

import (
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	httpmock "gopkg.in/jarcoal/httpmock.v1"
)

// Test New Kite Connect instance
func TestNewClient(t *testing.T) {
	apiKey := "api_key"
	client := New(apiKey)

	if client.apiKey != apiKey {
		t.Errorf("Api key is not assigned properly.")
	}
}

// Test all client setters
func TestClientSetters(t *testing.T) {
	apiKey := "kitefront"
	client := New(apiKey)

	customDebug := true
	customBaseURI := "test"
	customTimeout := 1000 * time.Millisecond
	customAccessToken := "someaccesstoken"
	customHTTPClientTimeout := time.Duration(2000)
	customHTTPClient := &http.Client{
		Timeout: customHTTPClientTimeout,
	}

	// Set custom debug
	client.SetDebug(customDebug)
	if client.debug != customDebug {
		t.Errorf("Debug is not set properly.")
	}

	// Set custom base URI
	client.SetBaseURI(customBaseURI)
	if client.baseURI != customBaseURI {
		t.Errorf("Base URI is not set properly.")
	}

	// Set custom timeout for default http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}

	// Set access token
	client.SetAccessToken(customAccessToken)
	if client.accessToken != customAccessToken {
		t.Errorf("Access token is not set properly.")
	}

	// Set custom HTTP Client
	client.SetHTTPClient(customHTTPClient)
	if client.httpClient.GetClient() != customHTTPClient {
		t.Errorf("Custom HTTPClient is not set properly.")
	}

	// Set timeout for custo http client
	if client.httpClient.GetClient().Timeout != customHTTPClientTimeout {
		t.Errorf("Custom HTTPClient timeout is not set properly.")
	}

	// Set custom timeout for custom http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}
}

// Following boiler plate is used to implement setup/teardown using Go subtests feature
const mockBaseDir = "./mock_responses"

var MockResponses = map[string]string{
	URIUserProfile:            "profile.json",
	URIUserMargins:            "margins.json",
	URIGetOrders:              "orders.json",
	URIGetTrades:              "trades.json",
	URIGetOrderHistory:        "order_info.json",   // "/orders/{order_id}"
	URIGetOrderTrades:         "order_trades.json", // "/orders/{order_id}/trades"
	URIGetPositions:           "positions.json",
	URIGetHoldings:            "holdings.json",
	URIGetMFOrders:            "mf_orders.json",
	URIGetMFOrderInfo:         "mf_orders_info.json", // "/mf/orders/{order_id}"
	URIGetMFSIPs:              "mf_sips.json",
	URIGetMFSIPInfo:           "mf_sip_info.json", //  "/mf/sips/{sip_id}"
	URIGetMFHoldings:          "mf_holdings.json",
	URIGetInstruments:         "instruments_all.csv",
	URIGetMFInstruments:       "mf_instruments.csv",
	URIGetInstrumentsExchange: "instruments_nse.csv",    // "/instruments/{exchange}"
	URIGetHistorical:          "historical_minute.json", // "/instruments/historical/{instrument_token}/{interval}"
	URIGetTriggerRange:        "trigger_range.json",     // "/instruments/{exchange}/{tradingsymbol}/trigger_range"
	URIGetQuote:               "quote.json",
	URIGetLTP:                 "ltp.json",
	URIGetOHLC:                "ohlc.json",
}

// Test only function prefix with this
const suiteTestMethodPrefix = "Test"

// TestSuite is an interface where you define suite and test case preparation and tear down logic.
type TestSuite struct {
	KiteConnect *Client
}

// Setup the API suit
func (ts *TestSuite) SetupAPITestSuit() {
	ts.KiteConnect = New("test_api_key")
	httpmock.ActivateNonDefault(ts.KiteConnect.httpClient.GetClient())

	for route, f := range MockResponses {
		resp, err := ioutil.ReadFile(path.Join(mockBaseDir, f))
		if err != nil {
			panic("Error while reading mock response: " + f)
		}

		base, err := url.Parse(ts.KiteConnect.baseURI)
		if err != nil {
			panic("something went wrong")
		}
		base.Path = path.Join(base.Path, route)

		// endpoint := path.Join(ts.KiteConnect.baseURI, route)
		httpmock.RegisterResponder("GET", base.String(), httpmock.NewBytesResponder(200, resp))
	}
}

// TearDown API suit
func (ts *TestSuite) TearDownAPITestSuit() {
	// defer httpmock.DeactivateAndReset()
}

// Individual test setup
func (ts *TestSuite) SetupAPITest() {}

// Individual test teardown
func (ts *TestSuite) TearDownAPITest() {}

/*
Run sets up the suite, runs its test cases and tears it down:
    1. Calls `ts.SetUpSuite`
    2. Seeks for any methods that have `Test` prefix, for each of them it:
      a. Calls `SetUp`
      b. Calls the test method itself
      c. Calls `TearDown`
    3. Calls `ts.TearDownSuite`
*/
func RunAPITests(t *testing.T, ts *TestSuite) {
	ts.SetupAPITestSuit()
	defer ts.TearDownAPITestSuit()

	suiteType := reflect.TypeOf(ts)
	for i := 0; i < suiteType.NumMethod(); i++ {
		m := suiteType.Method(i)
		if strings.HasPrefix(m.Name, suiteTestMethodPrefix) {
			t.Run(m.Name, func(t *testing.T) {
				ts.SetupAPITest()
				defer ts.TearDownAPITest()

				in := []reflect.Value{reflect.ValueOf(ts), reflect.ValueOf(t)}
				m.Func.Call(in)
			})
		}
	}
}

func TestAPIMethods(t *testing.T) {
	s := &TestSuite{}
	RunAPITests(t, s)
}
