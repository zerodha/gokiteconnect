package kiteconnect

import (
	"fmt"
	"net/url"
)

type Order struct {
	TransactionType string `json:"transaction_type"`
	InstrumentToken int    `json:"instrument_token"`
	Tradingsymbol   string `json:"tradingsymbol"`
	Exchange        string `json:"exchange"`

	OrderID         string `json:"order_id"`
	ParentOrderID   string `json:"parent_order_id"`
	ExchangeOrderID string `json:"exchange_order_id"`

	OrderTimestamp    string `json:"order_timestamp"`
	ExchangeTimestamp string `json:"exchange_timestamp"`

	Price        float64 `json:"price"`
	AveragePrice float64 `json:"average_price"`
	TriggerPrice float64 `json:"trigger_price"`

	Quantity          int `json:"quantity"`
	CancelledQuantity int `json:"cancelled_quantity"`
	DisclosedQuantity int `json:"disclosed_quantity"`
	FilledQuantity    int `json:"filled_quantity"`
	PendingQuantity   int `json:"pending_quantity"`

	OrderType string `json:"order_type"`
	Validity  string `json:"validity"`
	Variety   string `json:"variety"`
	Product   string `json:"product"`

	Status        string `json:"status"`
	StatusMessage string `json:"status_message"`

	MarketProtection float64 `json:"market_protection"`
	PlacedBy         string  `json:"placed_by"`
	Tag              string  `json:"tag"`
}

type Orders []Order

type OrderDetail struct {
	Exchange        string `json:"exchange"`
	TransactionType string `json:"transaction_type"`

	OrderID         string `json:"order_id"`
	OrderTimestamp  string `json:"order_timestamp"`
	ExchangeOrderID string `json:"exchange_order_id"`

	Product   string `json:"product"`
	OrderType string `json:"order_type"`
	Validity  string `json:"validity"`

	Quantity          int `json:"quantity"`
	DisclosedQuantity int `json:"disclosed_quantity"`
	PendingQuantity   int `json:"pending_quantity"`

	Price        float64 `json:"price"`
	TriggerPrice float64 `json:"trigger_price"`
	AveragePrice float64 `json:"average_price"`

	Status        string `json:"status"`
	StatusMessage string `json:"status_message"`
}

type OrderInfo []OrderDetail

type Trade struct {
	TradeID         int    `json:"trade_id"`
	OrderID         string `json:"order_id"`
	ExchangeOrderID string `json:"exchange_order_id"`

	Tradingsymbol  string `json:"tradingsymbol"`
	Exchange       string `json:"exchange"`
	IntrumentToken int    `json:"instrument_token"`

	TransactionType string `json:"transaction_type"`
	Product         string `json:"product"`

	AveragePrice float64 `json:"average_price"`
	Quantity     int     `json:"quantity"`

	OrderTimestamp    string `json:"order_timestamp"`
	ExchangeTimestamp string `json:"exchange_timestamp"`
}

type Trades []Trade

type OrderSuccessResponse struct {
	orderID string `json:"order_id"`
}

func (client *Client) GetOrders() (*Orders, error) {
	orders := &Orders{}
	err := client.get(URIOrders, client.makeParams(nil), orders)
	return orders, err
}

func (client *Client) GetOrderInfo(orderID string) (*OrderInfo, error) {
	order := &OrderInfo{}
	err := client.get(fmt.Sprintf(URIOrderInfo, orderID), client.makeParams(nil), order)
	return order, err
}

func (client *Client) PlaceOrder(variety string, p url.Values) (*OrderSuccessResponse, error) {
	resp := &OrderSuccessResponse{}
	params := client.makeParams(p)
	err := client.post(fmt.Sprintf(URIPlaceOrder, variety), params, resp)
	return resp, err
}

func (client *Client) ModifyOrder(variety string, orderID string, p url.Values) (*OrderSuccessResponse, error) {
	resp := &OrderSuccessResponse{}
	params := client.makeParams(p)
	err := client.put(fmt.Sprintf(URIModifyOrder, variety, orderID), params, resp)
	return resp, err
}

func (client *Client) CancelOrder(variety string, orderID string, p url.Values) (*OrderSuccessResponse, error) {
	resp := &OrderSuccessResponse{}
	params := client.makeParams(p)
	err := client.delete(fmt.Sprintf(URICancelOrder, variety, orderID), params, resp)
	return resp, err
}

func (client *Client) GetTrades() (*Trades, error) {
	trades := &Trades{}
	err := client.get(URITrades, client.makeParams(nil), trades)
	return trades, err
}

func (client *Client) GetOrderTrades(orderID string) (*Trades, error) {
	trades := &Trades{}
	err := client.get(fmt.Sprintf(URIOrderTrades, orderID), client.makeParams(nil), trades)
	return trades, err
}
