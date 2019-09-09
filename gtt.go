package kiteconnect

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type GTTOrderType string

const (
	GTTOrderTypeSingle GTTOrderType = "single"
	GTTOrderTypeOCO    GTTOrderType = "two-leg"
)

type GTTOrders []GTTOrder

type GTTMeta struct {
	RejectionReason string `json:"rejection_reason"`
}

type GTTCondition struct {
	Exchange      string    `json:"exchange"`
	Tradingsymbol string    `json:"tradingsymbol"`
	LastPrice     float64   `json:"last_price"`
	TriggerValues []float64 `json:"trigger_values"`
}

type GTTOrder struct {
	ID            int          `json:"id"`
	UserID        string       `json:"user_id"`
	ParentTrigger interface{}  `json:"parent_trigger"`
	Type          GTTOrderType `json:"type" url:""`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
	ExpiresAt     string       `json:"expires_at"`
	Status        string       `json:"status"`
	Condition     GTTCondition `json:"condition"`
	Orders        []Order      `json:"orders"`
	Meta          GTTMeta      `json:"meta"`
}

type GTTOrderParams struct {
	Tradingsymbol   string
	Exchange        string
	LastPrice       float64
	TransactionType string
	Type            GTTOrderType
	TriggerValues   []float64
	LimitPrices     []float64
	Quantities      []float64
}

func newGTT(o GTTOrderParams) GTTOrder {
	var orders Orders

	for i := range o.TriggerValues {
		orders = append(orders, Order{
			Exchange:        o.Exchange,
			TradingSymbol:   o.Tradingsymbol,
			TransactionType: o.TransactionType,
			Quantity:        o.Quantities[i],
			Price:           o.LimitPrices[i],
			OrderType:       OrderTypeLimit,
			Product:         ProductCNC,
		})
	}
	return GTTOrder{
		Type: o.Type,
		Condition: GTTCondition{
			Exchange:      o.Exchange,
			LastPrice:     o.LastPrice,
			Tradingsymbol: o.Tradingsymbol,
			TriggerValues: o.TriggerValues,
		},
		Orders: orders,
	}
}

type GTTOrderResponse struct {
	TriggerID int `json:"trigger_id"`
}

func (c *Client) PlaceGTTOrder(o GTTOrderParams) (GTTOrderResponse, error) {
	var (
		params    = url.Values{}
		gtt       = newGTT(o)
		orderResp GTTOrderResponse
	)

	condition, err := json.Marshal(gtt.Condition)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing condition: %v", err)
	}

	orders, err := json.Marshal(gtt.Orders)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing orders: %v", err)
	}

	params.Add("type", string(gtt.Type))
	params.Add("condition", string(condition))
	params.Add("orders", string(orders))

	err = c.doEnvelope(http.MethodPost, URIPlaceGTT, params, nil, &orderResp)
	return orderResp, err
}

func (c *Client) ModifyGTTOrder(triggerID int, o GTTOrderParams) (GTTOrderResponse, error) {
	var (
		params    = url.Values{}
		gtt       = newGTT(o)
		orderResp GTTOrderResponse
	)

	condition, err := json.Marshal(gtt.Condition)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing condition: %v", err)
	}

	orders, err := json.Marshal(gtt.Orders)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing orders: %v", err)
	}

	params.Add("type", string(gtt.Type))
	params.Add("condition", string(condition))
	params.Add("orders", string(orders))

	err = c.doEnvelope(http.MethodPut, fmt.Sprintf(URIModifyGTTOrder, triggerID), params, nil, &orderResp)
	return orderResp, err
}

func (c *Client) GetGTTOrders() (GTTOrders, error) {
	var orders GTTOrders
	err := c.doEnvelope(http.MethodGet, URIGetGTTOrders, nil, nil, &orders)
	return orders, err
}

func (c *Client) GetGTTOrder(triggerID int) (GTTOrder, error) {
	var order GTTOrder
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIGetGTTOrder, triggerID), nil, nil, &order)
	return order, err
}

func (c *Client) DeleteGTTOrder(triggerID int) (GTTOrderResponse, error) {
	var order GTTOrderResponse
	err := c.doEnvelope(http.MethodDelete, fmt.Sprintf(URIGetGTTOrder, triggerID), nil, nil, &order)
	return order, err
}
