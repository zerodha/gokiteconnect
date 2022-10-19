package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	httpmock "gopkg.in/jarcoal/httpmock.v1"
)

const (
	uriGetInstrumentsExchangeTest string = "/instruments/nse"
	uriGetHistoricalTest          string = "/instruments/historical/123/myinterval"
	uriGetHistoricalWithOITest    string = "/instruments/historical/456/myinterval"
)

// Test New Kite Connect instance
func TestNewClient(t *testing.T) {
	t.Parallel()

	apiKey := "api_key"
	client := New(apiKey)

	if client.apiKey != apiKey {
		t.Errorf("Api key is not assigned properly.")
	}
}

// Test all client setters
func TestClientSetters(t *testing.T) {
	t.Parallel()

	apiKey := "kitefront"
	client := New(apiKey)

	customDebug := true
	customBaseURI := "test"
	customTimeout := 1000 * time.Millisecond
	customAccessToken := "someaccesstoken"
	customHTTPClientTimeout := time.Duration(2000)
	customHTTPClient := &http.Client{Timeout: customHTTPClientTimeout}

	// Check if default debug is false
	if client.debug != false || client.httpClient.GetClient().debug != false {
		t.Errorf("Default debug is not false.")
	}

	// Set custom debug
	client.SetDebug(customDebug)
	if client.debug != customDebug || client.httpClient.GetClient().debug != customDebug {
		t.Errorf("Debug is not set properly.")
	}

	// Test default base uri
	if client.baseURI != baseURI {
		t.Errorf("Default base URI is not set properly.")
	}

	// Set custom base URI
	client.SetBaseURI(customBaseURI)
	if client.baseURI != customBaseURI {
		t.Errorf("Base URI is not set properly.")
	}

	// Test default timeout
	if client.httpClient.GetClient().client.Timeout != requestTimeout {
		t.Errorf("Default request timeout is not set properly.")
	}

	// Set custom timeout for default http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().client.Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}

	// Set access token
	client.SetAccessToken(customAccessToken)
	if client.accessToken != customAccessToken {
		t.Errorf("Access token is not set properly.")
	}

	// Set custom HTTP Client
	client.SetHTTPClient(customHTTPClient)
	if client.httpClient.GetClient().client != customHTTPClient {
		t.Errorf("Custom HTTPClient is not set properly.")
	}

	// Set timeout for custom http client
	if client.httpClient.GetClient().client.Timeout != customHTTPClientTimeout {
		t.Errorf("Custom HTTPClient timeout is not set properly.")
	}

	// Set custom timeout for custom http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().client.Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}
}

// Following boiler plate is used to implement setup/teardown using Go subtests feature
const mockBaseDir = "./mock_responses"

var MockResponders = [][]string{
	// Array of [<httpMethod>, <url>, <file_name>]

	// GET endpoints
	{http.MethodGet, URIUserProfile, "profile.json"},
	{http.MethodGet, URIUserMargins, "margins.json"},
	{http.MethodGet, URIUserMarginsSegment, "margins_equity.json"},
	{http.MethodGet, URIGetOrders, "orders.json"},
	{http.MethodGet, URIGetTrades, "trades.json"},
	{http.MethodGet, URIGetOrderHistory, "order_info.json"},
	{http.MethodGet, URIGetOrderTrades, "order_trades.json"},
	{http.MethodGet, URIGetPositions, "positions.json"},
	{http.MethodGet, URIGetHoldings, "holdings.json"},
	{http.MethodGet, URIGetMFOrders, "mf_orders.json"},
	{http.MethodGet, URIGetMFOrderInfo, "mf_orders_info.json"},
	{http.MethodGet, URIGetMFSIPs, "mf_sips.json"},
	{http.MethodGet, URIGetMFSIPInfo, "mf_sip_info.json"},
	{http.MethodGet, URIGetMFHoldings, "mf_holdings.json"},
	{http.MethodGet, fmt.Sprintf(URIGetGTT, 123), "gtt_get_order.json"},
	{http.MethodGet, URIGetGTTs, "gtt_get_orders.json"},
	{http.MethodGet, URIGetInstruments, "instruments_all.csv"},
	{http.MethodGet, URIGetMFInstruments, "mf_instruments.csv"},
	{http.MethodGet, uriGetInstrumentsExchangeTest, "instruments_nse.csv"},
	{http.MethodGet, uriGetHistoricalTest, "historical_minute.json"},
	{http.MethodGet, uriGetHistoricalWithOITest, "historical_oi.json"},
	{http.MethodGet, URIGetTriggerRange, "trigger_range.json"},
	{http.MethodGet, URIGetQuote, "quote.json"},
	{http.MethodGet, URIGetLTP, "ltp.json"},
	{http.MethodGet, URIGetOHLC, "ohlc.json"},

	// PUT endpoints
	{http.MethodPut, URIModifyMFSIP, "mf_sip_info.json"},
	{http.MethodPut, URIModifyOrder, "order_modify.json"},
	{http.MethodPut, URIConvertPosition, "positions.json"},
	{http.MethodPut, fmt.Sprintf(URIModifyGTT, 123), "gtt_modify_order.json"},

	// POST endpoints
	{http.MethodPost, URIPlaceOrder, "order_response.json"},
	{http.MethodPost, URIPlaceMFOrder, "order_response.json"},
	{http.MethodPost, URIPlaceMFSIP, "mf_sip_place.json"},
	{http.MethodPost, URIPlaceGTT, "gtt_place_order.json"},
	{http.MethodPost, URIOrderMargins, "order_margins.json"},
	{http.MethodPost, URIBasketMargins, "basket_margins.json"},
	{http.MethodPost, URIInitHoldingsAuth, "holdings_auth.json"},

	// DELETE endpoints
	{http.MethodDelete, URICancelOrder, "order_response.json"},
	{http.MethodDelete, URICancelMFSIP, "mf_sip_cancel.json"},
	{http.MethodDelete, fmt.Sprintf(URIDeleteGTT, 123), "gtt_modify_order.json"},
	{http.MethodDelete, URIUserSessionInvalidate, "session_logout.json"},
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
	httpmock.ActivateNonDefault(ts.KiteConnect.httpClient.GetClient().client)

	for _, v := range MockResponders {
		httpMethod := v[0]
		route := v[1]
		filePath := v[2]

		resp, err := os.ReadFile(path.Join(mockBaseDir, filePath))
		if err != nil {
			panic("Error while reading mock response: " + filePath)
		}

		base, err := url.Parse(ts.KiteConnect.baseURI)
		if err != nil {
			panic("Something went wrong")
		}
		// Replace all url variables with string "test"
		re := regexp.MustCompile("%s")
		formattedRoute := re.ReplaceAllString(route, "test")
		base.Path = path.Join(base.Path, formattedRoute)
		// fmt.Println(base.String())
		// endpoint := path.Join(ts.KiteConnect.baseURI, route)
		httpmock.RegisterResponder(httpMethod, base.String(), httpmock.NewBytesResponder(200, resp))
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
