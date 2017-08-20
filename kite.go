package kiteconnect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	apiKey      string
	apiSecret   string
	accessToken string
	debug       bool
	timeout     time.Duration
	baseURI     string
	httpClient  *http.Client
}

type Error struct {
	Code      int
	Message   string
	ErrorType string
	Data      interface{}
}

func (e Error) Error() string {
	return e.Message
}

type successResponse struct {
	Status string      `json:"status"`
	Data   interface{} `json:"data"`
}

type errorResponse struct {
	Status    string      `json:"status"`
	Message   string      `json:"message"`
	ErrorType string      `json:"error_type"`
	Data      interface{} `json:"data"`
}

type Session struct {
	Status string `json:"status"`
	Data   struct {
		AccessToken string   `json:"access_token"`
		PublicToken string   `json:"public_token"`
		UserID      string   `json:"user_id"`
		UserType    string   `json:"user_type"`
		Email       string   `json:"email"`
		UserName    string   `json:"user_name"`
		LoginTime   string   `json:"login_time"`
		Broker      string   `json:"broker"`
		Exchange    []string `json:"exchange"`
		Product     []string `json:"product"`
		OrderType   []string `json:"order_type"`
	} `json:"data"`
}

const (
	timeout = 7
	baseURI = "https://api.kite.trade"

	// Method constants
	mGET    = "GET"
	mPOST   = "POST"
	mPUT    = "PUT"
	mDELETE = "DELETE"
)

// API constants
const (
	GeneralError    = "GeneralException"
	TokenError      = "TokenException"
	PermissionError = "PermissionError"
	UserError       = "UserException"
	TwoFAError      = "TwoFAException"
	OrderError      = "OrderException"
	InputError      = "InputException"
	DataError       = "DataException"
	NetworkError    = "NetworkException"
)

// URI's
const (
	URIParams        string = "/parameters"
	URIAPIValidate   string = "/session/token"
	URIAPIInvalidate string = "/session/token"
	URIMargins       string = "/user/margins/%s" // "/user/margins/{segment}"

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

func New(apiKey string, apiSecret string) *Client {
	client := &Client{
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		baseURI:    baseURI,
		timeout:    timeout,
		httpClient: &http.Client{},
	}

	return client
}

func (client *Client) SetDebug(debug bool) {
	client.debug = debug
}

func (client *Client) SetBaseURI(baseURI string) {
	client.baseURI = baseURI
}

func (client *Client) SetTimeout(timeout time.Duration) {
	client.timeout = timeout
}

func (client *Client) makeParams(params url.Values) url.Values {
	if params == nil {
		params = url.Values{}
	}

	params.Add("api_key", client.apiKey)
	params.Add("access_token", client.accessToken)
	return params
}

func (client *Client) GetAccessToken(requestToken string) (*Session, error) {
	// Get SHA256 checksum
	h := sha256.New()
	h.Write([]byte(client.apiKey + requestToken + client.apiSecret))
	checksum := hex.EncodeToString(h.Sum(nil))
	session := &Session{}

	// construct url values
	params := url.Values{}
	params.Add("api_key", client.apiKey)
	params.Add("request_token", requestToken)
	params.Add("checksum", checksum)

	err := client.post(URIAPIValidate, params, &session)
	return session, err
}

func (client *Client) get(url string, params url.Values, result interface{}) error {
	return client.request(mGET, url, params, result)
}

func (client *Client) post(url string, params url.Values, result interface{}) error {
	return client.request(mPOST, url, params, result)
}

func (client *Client) put(url string, params url.Values, result interface{}) error {
	return client.request(mPUT, url, params, result)
}

func (client *Client) delete(url string, params url.Values, result interface{}) error {
	return client.request(mDELETE, url, params, result)
}

func (client *Client) request(method string, url string, params url.Values, result interface{}) error {
	fullURL := baseURI + url
	req, err := http.NewRequest(method, string(fullURL), strings.NewReader(params.Encode()))
	if err != nil {
		return NewError(GeneralError, "Error preparing request", nil)
	}

	// Set content type to form for put and post methods
	if method == mPOST || method == mPUT {
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	}

	response, err := client.httpClient.Do(req)
	if err != nil {
		return NewError(GeneralError, "Request failed", nil)
	}

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return NewError(GeneralError, "Error reading response", nil)
	}
	defer response.Body.Close()

	// Check if its an error
	if response.StatusCode >= http.StatusBadRequest {
		var e errorResponse
		if err = json.Unmarshal(body, &e); err != nil {
			return NewError(DataError, "Error parsing error response.", nil)
		}

		return NewError(e.ErrorType, e.Message, e.Data)
	}

	if err := json.Unmarshal([]byte(body), result); err != nil {
		return NewError(GeneralError, "Error while parsing response", nil)
	}

	return nil
}

func NewError(etype string, message string, data interface{}) error {
	err := Error{}
	err.Message = message
	err.ErrorType = etype
	err.Data = data

	switch etype {
	case GeneralError:
		err.Code = http.StatusInternalServerError
	case TokenError:
		err.Code = http.StatusForbidden
	case PermissionError:
		err.Code = http.StatusForbidden
	case UserError:
		err.Code = http.StatusInternalServerError
	case TwoFAError:
		err.Code = http.StatusForbidden
	case OrderError:
		err.Code = http.StatusBadRequest
	case InputError:
		err.Code = http.StatusBadRequest
	case DataError:
		err.Code = http.StatusGatewayTimeout
	case NetworkError:
		err.Code = http.StatusServiceUnavailable
	}

	return err
}
