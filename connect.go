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

const (
	name    string        = "gokiteconnect"
	version string        = "3.0.0"
	timeout time.Duration = time.Duration(7000)
	baseURI string        = "https://api.kite.trade"
	// Kite connect header version
	kiteHeaderVersion string = "3"
)

// URI's
const (
	URIUserSession           string = "/session/token"
	URIUserSessionInvalidate string = "/session/token"
	URIUserSessionRenew      string = "/session/refresh_token"
	URIUserProfile           string = "/user/profile"
	URIUserMargins           string = "/user/margins"
	URIUserMarginsSegment    string = "/user/margins/%s" // "/user/margins/{segment}"

	URIOrders      string = "/orders"
	URITrades      string = "/trades"
	URIOrderInfo   string = "/orders/%s"        // "/orders/{order_id}"
	URIOrderTrades string = "/orders/%s/trades" // "/orders/{order_id}/trades"
	URIPlaceOrder  string = "/orders/%s"        // "/orders/{variety}"
	URIModifyOrder string = "/orders/%s/%s"     // "/orders/{variety}/{order_id}"
	URICancelOrder string = "/orders/%s/%s"     // "/orders/{variety}/{order_id}"

	URIPositions     string = "/portfolio/positions"
	URIProductModify string = "/portfolio/positions"
	URIHoldings      string = "/portfolio/holdings"

	URIInstruments         string = "/instruments"
	URIInstrumentsExchange string = "/instruments/%s"                  // "/instruments/{exchange}"
	URIQuote               string = "/instruments/%s/%s"               // "/instruments/{exchange}/{tradingsymbol}"
	URIHistorical          string = "/instruments/historical/%s/%s"    // "/instruments/historical/{instrument_token}/{interval}"
	URITriggerRange        string = "/instruments/%s/%s/trigger_range" // "/instruments/{exchange}/{tradingsymbol}/trigger_range"
)

// New creates a new kiteconnect Client instance
func New(apiKey string) *Client {
	client := &Client{
		apiKey:  apiKey,
		baseURI: baseURI,
	}

	// Create a default http handler with default timeout
	client.SetHTTPHandler(&http.Client{
		Timeout: timeout * time.Millisecond,
	})

	return client
}

// SetHTTPHandler sets a custom http handler. Can be used to set custom timeouts and transport.
func (c *Client) SetHTTPHandler(h *http.Client) {
	c.httpClient = NewHTTPClient(h, nil)
}

// SetDebug sets debug mode to enable HTTP logs
func (c *Client) SetDebug(debug bool) {
	c.debug = debug
}

// SetBaseURI overrides base Kiteconnect API endpoint
func (c *Client) SetBaseURI(baseURI string) {
	c.baseURI = baseURI
}

// SetTimeout sets request timeout for http client
func (c *Client) SetTimeout(timeout time.Duration) {
	httpClient := c.httpClient.GetClient()
	httpClient.Timeout = timeout * time.Millisecond
}

// SetAccessToken sets field accessToken in Kiteconnect instance
func (c *Client) SetAccessToken(accessToken string) {
	c.accessToken = accessToken
}

func (c *Client) doEnvelope(method, uri string, params url.Values, headers http.Header, v interface{}) error {
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

	return c.httpClient.DoEnvelope(method, c.baseURI+uri, params, headers, v)
}
