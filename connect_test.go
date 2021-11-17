package kiteconnect

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
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
	customHTTPClient := &http.Client{
		Timeout: customHTTPClientTimeout,
	}

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
	[]string{http.MethodGet, URIUserProfile, "profile.json"},
	[]string{http.MethodGet, URIUserMargins, "margins.json"},
	[]string{http.MethodGet, URIUserMarginsSegment, "margins_equity.json"},
	[]string{http.MethodGet, URIGetOrders, "orders.json"},
	[]string{http.MethodGet, URIGetTrades, "trades.json"},
	[]string{http.MethodGet, URIGetOrderHistory, "order_info.json"},
	[]string{http.MethodGet, URIGetOrderTrades, "order_trades.json"},
	[]string{http.MethodGet, URIGetPositions, "positions.json"},
	[]string{http.MethodGet, URIGetHoldings, "holdings.json"},
	[]string{http.MethodGet, URIGetMFOrders, "mf_orders.json"},
	[]string{http.MethodGet, URIGetMFOrderInfo, "mf_orders_info.json"},
	[]string{http.MethodGet, URIGetMFSIPs, "mf_sips.json"},
	[]string{http.MethodGet, URIGetMFSIPInfo, "mf_sip_info.json"},
	[]string{http.MethodGet, URIGetMFHoldings, "mf_holdings.json"},
	[]string{http.MethodGet, fmt.Sprintf(URIGetGTT, 123), "gtt_get_order.json"},
	[]string{http.MethodGet, URIGetGTTs, "gtt_get_orders.json"},
	[]string{http.MethodGet, URIGetInstruments, "instruments_all.csv"},
	[]string{http.MethodGet, URIGetMFInstruments, "mf_instruments.csv"},
	[]string{http.MethodGet, uriGetInstrumentsExchangeTest, "instruments_nse.csv"},
	[]string{http.MethodGet, uriGetHistoricalTest, "historical_minute.json"},
	[]string{http.MethodGet, uriGetHistoricalWithOITest, "historical_oi.json"},
	[]string{http.MethodGet, URIGetTriggerRange, "trigger_range.json"},
	[]string{http.MethodGet, URIGetQuote, "quote.json"},
	[]string{http.MethodGet, URIGetLTP, "ltp.json"},
	[]string{http.MethodGet, URIGetOHLC, "ohlc.json"},

	// PUT endpoints
	[]string{http.MethodPut, URIModifyMFSIP, "mf_sip_info.json"},
	[]string{http.MethodPut, URIModifyOrder, "order_modify.json"},
	[]string{http.MethodPut, URIConvertPosition, "positions.json"},
	[]string{http.MethodPut, fmt.Sprintf(URIModifyGTT, 123), "gtt_modify_order.json"},

	// POST endpoints
	[]string{http.MethodPost, URIPlaceOrder, "order_response.json"},
	[]string{http.MethodPost, URIPlaceMFOrder, "order_response.json"},
	[]string{http.MethodPost, URIPlaceMFSIP, "mf_order_response.json"},
	[]string{http.MethodPost, URIPlaceGTT, "gtt_place_order.json"},
	[]string{http.MethodPost, URIOrderMargins, "order_margins.json"},
	[]string{http.MethodPost, URIBasketMargins, "basket_margins.json"},
	[]string{http.MethodPost, URIInitHoldingsAuth, "holdings_auth.json"},

	// DELETE endpoints
	[]string{http.MethodDelete, URICancelOrder, "order_response.json"},
	[]string{http.MethodDelete, URICancelMFSIP, "mf_order_response.json"},
	[]string{http.MethodDelete, fmt.Sprintf(URIDeleteGTT, 123), "gtt_modify_order.json"},
	[]string{http.MethodDelete, URIUserSessionInvalidate, "session_logout.json"},
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

		resp, err := ioutil.ReadFile(path.Join(mockBaseDir, filePath))
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
