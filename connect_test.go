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

	"github.com/stretchr/testify/assert"
	httpmock "gopkg.in/jarcoal/httpmock.v1"
)

const (
	uriGetInstrumentsExchangeTest string = "/instruments/nse"
	uriGetHistoricalTest          string = "/instruments/historical/123/myinterval"
)

// Test New Kite Connect instance
func TestNewClient(t *testing.T) {
	apiKey := "api_key"
	client := New(apiKey)

	assert.Equal(t, apiKey, client.apiKey)
}

// Test all client setters
func TestClientSetters(t *testing.T) {
	assert := assert.New(t)
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
	assert.False(client.debug)
	assert.False(client.httpClient.GetClient().debug)

	// Set custom debug
	client.SetDebug(customDebug)
	assert.Equal(customDebug, client.debug)
	assert.Equal(customDebug, client.httpClient.GetClient().debug)

	// Test default base uri
	assert.Equal(baseURI, client.baseURI)

	// Set custom base URI
	client.SetBaseURI(customBaseURI)
	assert.Equal(customBaseURI, client.baseURI)

	// Test default timeout
	assert.Equal(requestTimeout, client.httpClient.GetClient().client.Timeout)

	// Set custom timeout for default http client
	client.SetTimeout(customTimeout)
	assert.Equal(customTimeout, client.httpClient.GetClient().client.Timeout)

	// Set access token
	client.SetAccessToken(customAccessToken)
	assert.Equal(customAccessToken, client.accessToken)

	// Set custom HTTP Client
	client.SetHTTPClient(customHTTPClient)
	assert.Equal(customHTTPClient, client.httpClient.GetClient().client)

	// Set timeout for custom http client
	assert.Equal(customHTTPClientTimeout, client.httpClient.GetClient().client.Timeout)

	// Set custom timeout for custom http client
	client.SetTimeout(customTimeout)
	assert.Equal(customTimeout, client.httpClient.GetClient().client.Timeout)
}

func TestGetURL(t *testing.T) {
	apiKey := "kitefront"
	client := New(apiKey)
	assert.Equal(t, fmt.Sprintf(loginURI, apiKey), client.GetLoginURL())
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
	[]string{http.MethodGet, URIGetInstruments, "instruments_all.csv"},
	[]string{http.MethodGet, URIGetMFInstruments, "mf_instruments.csv"},
	[]string{http.MethodGet, uriGetInstrumentsExchangeTest, "instruments_nse.csv"},
	[]string{http.MethodGet, uriGetHistoricalTest, "historical_minute.json"},
	[]string{http.MethodGet, URIGetTriggerRange, "trigger_range.json"},
	[]string{http.MethodGet, URIGetQuote, "quote.json"},
	[]string{http.MethodGet, URIGetLTP, "ltp.json"},
	[]string{http.MethodGet, URIGetOHLC, "ohlc.json"},

	// PUT endpoints
	[]string{http.MethodPut, URIModifyMFSIP, "mf_sip_info.json"},
	[]string{http.MethodPut, URIModifyOrder, "order_response.json"},
	[]string{http.MethodPut, URIConvertPosition, "positions.json"},

	// POST endpoints
	[]string{http.MethodPost, URIPlaceOrder, "order_response.json"},
	[]string{http.MethodPost, URIPlaceMFOrder, "order_response.json"},
	[]string{http.MethodPost, URIPlaceMFSIP, "mf_order_response.json"},
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
