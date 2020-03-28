package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/go-querystring/query"
)

// MFHolding represents a individual mutualfund holding.
type MFHolding struct {
	Folio         string  `json:"folio"`
	Fund          string  `json:"fund"`
	Tradingsymbol string  `json:"tradingsymbol"`
	AveragePrice  float64 `json:"average_price"`
	LastPrice     float64 `json:"last_price"`
	LastPriceDate string  `json:"last_price_date"`
	Pnl           float64 `json:"pnl"`
	Quantity      float64 `json:"quantity"`
}

// MFTrade represents a individual trades of a mutualfund holding.
type MFTrade struct {
	Fund              string  `json:"fund"`
	Tradingsymbol     string  `json:"tradingsymbol"`
	AveragePrice      float64 `json:"average_price"`
	Variety           string  `json:"variety"`
	ExchangeTimestamp Time    `json:"exchange_timestamp"`
	Amount            float64 `json:"amount"`
	Folio             string  `json:"folio"`
	Quantity          float64 `json:"quantity"`
}

// MFHoldingBreakdown represents a list of mutualfund holdings.
type MFHoldingBreakdown []MFTrade

// MFHoldings represents a list of mutualfund holdings.
type MFHoldings []MFHolding

// MFOrder represents a individual mutualfund order response.
type MFOrder struct {
	OrderID           string `json:"order_id"`
	ExchangeOrderID   string `json:"exchange_order_id"`
	Tradingsymbol     string `json:"tradingsymbol"`
	Status            string `json:"status"`
	StatusMessage     string `json:"status_message"`
	Folio             string `json:"folio"`
	Fund              string `json:"fund"`
	OrderTimestamp    Time   `json:"order_timestamp"`
	ExchangeTimestamp Time   `json:"exchange_timestamp"`
	SettlementID      string `json:"settlement_id"`

	TransactionType string  `json:"transaction_type"`
	Variety         string  `json:"variety"`
	PurchaseType    string  `json:"purchase_type"`
	Quantity        float64 `json:"quantity"`
	Amount          float64 `json:"amount"`
	LastPrice       float64 `json:"last_price"`
	AveragePrice    float64 `json:"average_price"`
	PlacedBy        string  `json:"placed_by"`
	Tag             string  `json:"tag"`
}

// MFOrders represents a list of mutualfund orders.
type MFOrders []MFOrder

// MFAllottedISINs represents a list of all ISINs in which atleast one allotment is present.
type MFAllottedISINs []string

// MFSIP represents a individual mutualfund SIP response.
type MFSIP struct {
	ID              string `json:"sip_id"`
	Tradingsymbol   string `json:"tradingsymbol"`
	FundName        string `json:"fund"`
	DividendType    string `json:"dividend_type"`
	TransactionType string `json:"transaction_type"`

	Status               string  `json:"status"`
	SipType              string  `json:"sip_type"`
	Created              Time    `json:"created"`
	Frequency            string  `json:"frequency"`
	InstalmentAmount     float64 `json:"instalment_amount"`
	Instalments          int     `json:"instalments"`
	LastInstalment       Time    `json:"last_instalment"`
	PendingInstalments   int     `json:"pending_instalments"`
	InstalmentDay        int     `json:"instalment_day"`
	CompletedInstalments int     `json:"completed_instalments"`
	NextInstalment       string  `json:"next_instalment"`
	Tag                  string  `json:"tag"`
}

// MFSIPs represents a list of mutualfund SIPs.
type MFSIPs []MFSIP

// MFOrderResponse represents the successful order place response.
type MFOrderResponse struct {
	OrderID string `json:"order_id"`
}

// MFSIPResponse represents the successful SIP place response.
type MFSIPResponse struct {
	OrderID *string `json:"order_id"`
	SIPID   string  `json:"sip_id"`
}

// MFOrderParams represents parameters for placing an order.
type MFOrderParams struct {
	Tradingsymbol   string  `json:"tradingsymbol" url:"tradingsymbol"`
	TransactionType string  `json:"transaction_type" url:"transaction_type"`
	Quantity        float64 `json:"quantity" url:"quantity,omitempty"`
	Amount          float64 `json:"amount" url:"amount,omitempty"`
	Tag             string  `json:"tag" url:"tag,omitempty"`
}

// MFSIPParams represents parameters for placing a SIP.
type MFSIPParams struct {
	Tradingsymbol string  `json:"tradingsymbol" url:"tradingsymbol"`
	Amount        float64 `json:"amount" url:"amount"`
	Instalments   int     `json:"instalments" url:"instalments"`
	Frequency     string  `json:"frequency" url:"frequency"`
	InstalmentDay int     `json:"instalment_day" url:"instalment_day,omitempty"`
	InitialAmount float64 `json:"initial_amount" url:"initial_amount,omitempty"`
	Tag           string  `json:"tag" url:"tag,omitempty"`
}

// MFSIPModifyParams represents parameters for modifying a SIP.
type MFSIPModifyParams struct {
	Amount        float64 `json:"amount" url:"amount,omitempty"`
	Frequency     string  `json:"frequency" url:"frequency,omitempty"`
	InstalmentDay int     `json:"instalment_day" url:"instalment_day,omitempty"`
	Instalments   int     `json:"instalments" url:"instalments,omitempty"`
	Status        string  `json:"status" url:"status,omitempty"`
}

// GetMFOrders gets list of mutualfund orders.
func (c *Client) GetMFOrders() (MFOrders, error) {
	var orders MFOrders
	err := c.doEnvelope(http.MethodGet, URIGetMFOrders, nil, nil, &orders)
	return orders, err
}

// GetMFOrderInfo get individual mutualfund order info.
func (c *Client) GetMFOrderInfo(OrderID string) (MFOrder, error) {
	var orderInfo MFOrder
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIGetMFOrderInfo, OrderID), nil, nil, &orderInfo)
	return orderInfo, err
}

// PlaceMFOrder places an mutualfund order.
func (c *Client) PlaceMFOrder(orderParams MFOrderParams) (MFOrderResponse, error) {
	var (
		orderResponse MFOrderResponse
		params        url.Values
		err           error
	)

	if params, err = query.Values(orderParams); err != nil {
		return orderResponse, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodPost, URIPlaceMFOrder, params, nil, &orderResponse)
	return orderResponse, err
}

// GetMFSIPs gets list of mutualfund SIPs.
func (c *Client) GetMFSIPs() (MFSIPs, error) {
	var sips MFSIPs
	err := c.doEnvelope(http.MethodGet, URIGetMFSIPs, nil, nil, &sips)
	return sips, err
}

// GetMFSIPInfo get individual SIP info.
func (c *Client) GetMFSIPInfo(sipID string) (MFSIP, error) {
	var sip MFSIP
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIGetMFSIPInfo, sipID), nil, nil, &sip)
	return sip, err
}

// PlaceMFSIP places an mutualfund SIP order.
func (c *Client) PlaceMFSIP(sipParams MFSIPParams) (MFSIPResponse, error) {
	var (
		sipResponse MFSIPResponse
		params      url.Values
		err         error
	)

	if params, err = query.Values(sipParams); err != nil {
		return sipResponse, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodPost, URIPlaceMFSIP, params, nil, &sipResponse)
	return sipResponse, err
}

// ModifyMFSIP modifies an mutualfund SIP.
func (c *Client) ModifyMFSIP(sipID string, sipParams MFSIPModifyParams) (MFSIPResponse, error) {
	var (
		sipResponse MFSIPResponse
		params      url.Values
		err         error
	)

	if params, err = query.Values(sipParams); err != nil {
		return sipResponse, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodPut, fmt.Sprintf(URIModifyMFSIP, sipID), params, nil, &sipResponse)
	return sipResponse, err
}

// CancelMFSIP cancels an mutualfund SIP.
func (c *Client) CancelMFSIP(sipID string) (MFSIPResponse, error) {
	var (
		sipResponse MFSIPResponse
	)

	err := c.doEnvelope(http.MethodDelete, fmt.Sprintf(URICancelMFSIP, sipID), nil, nil, &sipResponse)
	return sipResponse, err
}

// CancelMFOrder cancels an mutualfund SIP.
func (c *Client) CancelMFOrder(orderID string) (MFOrderResponse, error) {
	var orderResponse MFOrderResponse
	err := c.doEnvelope(http.MethodDelete, fmt.Sprintf(URICancelMFOrder, orderID), nil, nil, &orderResponse)
	return orderResponse, err
}

// GetMFHoldings gets list of user mutualfund holdings.
func (c *Client) GetMFHoldings() (MFHoldings, error) {
	var holdings MFHoldings
	err := c.doEnvelope(http.MethodGet, URIGetMFHoldings, nil, nil, &holdings)
	return holdings, err
}

// GetMFHoldingInfo get individual Holding info.
func (c *Client) GetMFHoldingInfo(isin string) (MFHoldingBreakdown, error) {
	var holdingBreakdown MFHoldingBreakdown
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIGetMFHoldingInfo, isin), nil, nil, &holdingBreakdown)
	return holdingBreakdown, err
}

// GetMFAllottedISINs gets list of user mutualfund holdings.
func (c *Client) GetMFAllottedISINs() (MFAllottedISINs, error) {
	var isins MFAllottedISINs
	err := c.doEnvelope(http.MethodGet, URIGetAllotedISINs, nil, nil, &isins)
	return isins, err
}
