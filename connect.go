package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PlainResponse is a helper for receiving blank HTTP
// envelop responses without any payloads.
type PlainResponse struct {
	Code    int    `json:"code"`
	Message string `json:"string"`
}

// Client represents interface for Kite Connect client.
type Client struct {
	apiKey      string
	accessToken string
	debug       bool
	baseURI     string
	httpClient  HTTPClient
}

const (
	name           string        = "gokiteconnect"
	version        string        = "3.0.0"
	requestTimeout time.Duration = 7000 * time.Millisecond
	baseURI        string        = "https://api.kite.trade"
	loginURI       string        = "https://kite.trade/connect/login?api_key=%s&v=3"
	// Kite connect header version
	kiteHeaderVersion string = "3"
)

// Useful public constants
const (
	// Varieties
	VarietyRegular = "regular"
	VarietyAMO     = "amo"
	VarietyBO      = "bo"
	VarietyCO      = "co"

	// Products
	ProductBO   = "BO"
	ProductCO   = "CO"
	ProductMIS  = "MIS"
	ProductCNC  = "CNC"
	ProductNRML = "NRML"

	// Order types
	OrderTypeMarket = "MARKET"
	OrderTypeLimit  = "LIMIT"
	OrderTypeSL     = "SL"
	OrderTypeSLM    = "SL-M"

	// Validities
	ValidityDay = "DAY"
	ValidityIOC = "IOC"

	// Transaction type
	TransactionTypeBuy  = "BUY"
	TransactionTypeSell = "SELL"

	// Exchanges
	ExchangeNSE = "NSE"
	ExchangeBSE = "BSE"
	ExchangeMCX = "MCX"
	ExchangeNFO = "NFO"
	ExchangeBFO = "BFO"
	ExchangeCDS = "CDS"

	// Margins segments
	MarginsEquity    = "equity"
	MarginsCommodity = "commodity"

	// Order status
	OrderStatusComplete  = "COMPLETE"
	OrderStatusRejected  = "REJECTED"
	OrderStatusCancelled = "CANCELLED"
)

// API endpoints
const (
	URIUserSession           string = "/session/token"
	URIUserSessionInvalidate string = "/session/token"
	URIUserSessionRenew      string = "/session/refresh_token"
	URIUserProfile           string = "/user/profile"
	URIUserMargins           string = "/user/margins"
	URIUserMarginsSegment    string = "/user/margins/%s" // "/user/margins/{segment}"

	URIGetOrders       string = "/orders"
	URIGetTrades       string = "/trades"
	URIGetOrderHistory string = "/orders/%s"        // "/orders/{order_id}"
	URIGetOrderTrades  string = "/orders/%s/trades" // "/orders/{order_id}/trades"
	URIPlaceOrder      string = "/orders/%s"        // "/orders/{variety}"
	URIModifyOrder     string = "/orders/%s/%s"     // "/orders/{variety}/{order_id}"
	URICancelOrder     string = "/orders/%s/%s"     // "/orders/{variety}/{order_id}"

	URIGetPositions    string = "/portfolio/positions"
	URIGetHoldings     string = "/portfolio/holdings"
	URIConvertPosition string = "/portfolio/positions"

	// MF endpoints
	URIGetMFOrders      string = "/mf/orders"
	URIGetMFOrderInfo   string = "/mf/orders/%s" // "/mf/orders/{order_id}"
	URIPlaceMFOrder     string = "/mf/orders"
	URICancelMFOrder    string = "/mf/orders/%s" // "/mf/orders/{order_id}"
	URIGetMFSIPs        string = "/mf/sips"
	URIGetMFSIPInfo     string = "/mf/sips/%s" //  "/mf/sips/{sip_id}"
	URIPlaceMFSIP       string = "/mf/sips"
	URIModifyMFSIP      string = "/mf/sips/%s" //  "/mf/sips/{sip_id}"
	URICancelMFSIP      string = "/mf/sips/%s" //  "/mf/sips/{sip_id}"
	URIGetMFHoldings    string = "/mf/holdings"
	URIGetMFHoldingInfo string = "/mf/holdings/%s" //  "/mf/holdings/{isin}"
	URIGetAllotedISINs  string = "/mf/allotments"

	// GTT endpoints
	URIPlaceGTT  string = "/gtt/triggers"
	URIGetGTTs   string = "/gtt/triggers"
	URIGetGTT    string = "/gtt/triggers/%d"
	URIModifyGTT string = "/gtt/triggers/%d"
	URIDeleteGTT string = "/gtt/triggers/%d"

	URIGetInstruments         string = "/instruments"
	URIGetMFInstruments       string = "/mf/instruments"
	URIGetInstrumentsExchange string = "/instruments/%s"                  // "/instruments/{exchange}"
	URIGetHistorical          string = "/instruments/historical/%d/%s"    // "/instruments/historical/{instrument_token}/{interval}"
	URIGetTriggerRange        string = "/instruments/%s/%s/trigger_range" // "/instruments/{exchange}/{tradingsymbol}/trigger_range"

	URIGetQuote string = "/quote"
	URIGetLTP   string = "/quote/ltp"
	URIGetOHLC  string = "/quote/ohlc"

	// Order Margin computation
	URIOrderMargin string = "/margins/orders"
)

// New creates a new Kite Connect client.
func New(apiKey string) *Client {
	client := &Client{
		apiKey:  apiKey,
		baseURI: baseURI,
	}

	// Create a default http handler with default timeout.
	client.SetHTTPClient(&http.Client{
		Timeout: requestTimeout,
	})

	return client
}

// SetHTTPClient overrides default http handler with a custom one.
// This can be used to set custom timeouts and transport.
func (c *Client) SetHTTPClient(h *http.Client) {
	c.httpClient = NewHTTPClient(h, nil, c.debug)
}

// SetDebug sets debug mode to enable HTTP logs.
func (c *Client) SetDebug(debug bool) {
	c.debug = debug
	c.httpClient.GetClient().debug = debug
}

// SetBaseURI overrides the base Kiteconnect API endpoint with custom url.
func (c *Client) SetBaseURI(baseURI string) {
	c.baseURI = baseURI
}

// SetTimeout sets request timeout for default http client.
func (c *Client) SetTimeout(timeout time.Duration) {
	hClient := c.httpClient.GetClient().client
	hClient.Timeout = timeout
}

// SetAccessToken sets the access token to the Kite Connect instance.
func (c *Client) SetAccessToken(accessToken string) {
	c.accessToken = accessToken
}

// GetLoginURL gets Kite Connect login endpoint.
func (c *Client) GetLoginURL() string {
	return fmt.Sprintf(loginURI, c.apiKey)
}

func (c *Client) doEnvelope(method, uri string, params interface{}, headers http.Header, v interface{}) error {
	if params == nil {
		params = url.Values{}
	}

	// Send custom headers set
	if headers == nil {
		headers = map[string][]string{}
	}

	// Add Kite Connect version to header
	headers.Add("X-Kite-Version", kiteHeaderVersion)
	headers.Add("User-Agent", name+"/"+version)

	if c.apiKey != "" && c.accessToken != "" {
		authHeader := fmt.Sprintf("token %s:%s", c.apiKey, c.accessToken)
		headers.Add("Authorization", authHeader)
	}

	return c.httpClient.DoEnvelope(method, c.baseURI+uri, params, headers, v)
}

func (c *Client) do(method, uri string, params url.Values, headers http.Header) (HTTPResponse, error) {
	if params == nil {
		params = url.Values{}
	}

	if headers == nil {
		headers = map[string][]string{}
	}

	headers.Add("X-Kite-Version", kiteHeaderVersion)
	headers.Add("User-Agent", name+"/"+version)

	if c.apiKey != "" && c.accessToken != "" {
		authHeader := fmt.Sprintf("token %s:%s", c.apiKey, c.accessToken)
		headers.Add("Authorization", authHeader)
	}

	return c.httpClient.Do(method, c.baseURI+uri, params, headers)
}
