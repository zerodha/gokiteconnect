package kiteconnect

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/zerodha/gokiteconnect/v4/models"
)

// GTTType represents the available GTT order types.
type GTTType string

const (
	// GTTTypeSingle is used to monitor a single trigger value
	GTTTypeSingle GTTType = "single"
	// GTTTypeOCO is used to monitor two trigger values
	// where executing one cancels the other.
	GTTTypeOCO GTTType = "two-leg"
)

// GTTs represents a list of GTT orders.
type GTTs []GTT

// GTTMeta contains information about the rejection reason
// received after GTT order was triggered.
type GTTMeta struct {
	RejectionReason string `json:"rejection_reason"`
}

// GTTCondition represents the condition inside a GTT order.
type GTTCondition struct {
	Exchange      string    `json:"exchange"`
	Tradingsymbol string    `json:"tradingsymbol"`
	LastPrice     float64   `json:"last_price"`
	TriggerValues []float64 `json:"trigger_values"`
}

// GTT represents a single GTT order.
type GTT struct {
	ID        int          `json:"id"`
	UserID    string       `json:"user_id"`
	Type      GTTType      `json:"type" url:""`
	CreatedAt models.Time  `json:"created_at"`
	UpdatedAt models.Time  `json:"updated_at"`
	ExpiresAt models.Time  `json:"expires_at"`
	Status    string       `json:"status"`
	Condition GTTCondition `json:"condition"`
	Orders    []Order      `json:"orders"`
	Meta      GTTMeta      `json:"meta"`
}

// Trigger is an abstraction over multiple GTT types.
type Trigger interface {
	TriggerValues() []float64
	LimitPrices() []float64
	Quantities() []float64
	Type() GTTType
}

type TriggerParams struct {
	TriggerValue float64
	LimitPrice   float64
	Quantity     float64
}

// GTTSingleLegTrigger implements Trigger interface for the SingleLegTrigger.
type GTTSingleLegTrigger struct {
	TriggerParams
}

func (t *GTTSingleLegTrigger) TriggerValues() []float64 {
	return []float64{t.TriggerValue}
}

func (t *GTTSingleLegTrigger) LimitPrices() []float64 {
	return []float64{t.LimitPrice}
}

func (t *GTTSingleLegTrigger) Quantities() []float64 {
	return []float64{t.Quantity}
}

func (t *GTTSingleLegTrigger) Type() GTTType {
	return GTTTypeSingle
}

// GTTOneCancelsOtherTrigger implements Trigger interface for the GTTOneCancelsOtherTrigger.
type GTTOneCancelsOtherTrigger struct {
	Upper TriggerParams
	Lower TriggerParams
}

func (t *GTTOneCancelsOtherTrigger) TriggerValues() []float64 {
	return []float64{t.Lower.TriggerValue, t.Upper.TriggerValue}
}

func (t *GTTOneCancelsOtherTrigger) LimitPrices() []float64 {
	return []float64{t.Lower.LimitPrice, t.Upper.LimitPrice}
}

func (t *GTTOneCancelsOtherTrigger) Quantities() []float64 {
	return []float64{t.Lower.Quantity, t.Upper.Quantity}
}

func (t *GTTOneCancelsOtherTrigger) Type() GTTType {
	return GTTTypeOCO
}

// GTTParams is a helper struct used to populate an
// actual GTT before sending it to the API.
type GTTParams struct {
	Tradingsymbol   string
	Exchange        string
	LastPrice       float64
	TransactionType string
	Trigger         Trigger
}

func newGTT(o GTTParams) GTT {
	orders := make(Orders, 0, len(o.Trigger.TriggerValues()))
	for i := range o.Trigger.TriggerValues() {
		orders = append(orders, Order{
			Exchange:        o.Exchange,
			TradingSymbol:   o.Tradingsymbol,
			TransactionType: o.TransactionType,
			Quantity:        o.Trigger.Quantities()[i],
			Price:           o.Trigger.LimitPrices()[i],
			OrderType:       OrderTypeLimit,
			Product:         ProductCNC,
		})
	}
	return GTT{
		Type: o.Trigger.Type(),
		Condition: GTTCondition{
			Exchange:      o.Exchange,
			LastPrice:     o.LastPrice,
			Tradingsymbol: o.Tradingsymbol,
			TriggerValues: o.Trigger.TriggerValues(),
		},
		Orders: orders,
	}
}

// GTTResponse is returned by the API calls to GTT API.
type GTTResponse struct {
	TriggerID int `json:"trigger_id"`
}

// PlaceGTT constructs and places a GTT order using GTTParams.
func (c *Client) PlaceGTT(o GTTParams) (GTTResponse, error) {
	gtt := newGTT(o)

	var orderResp GTTResponse
	condition, err := json.Marshal(gtt.Condition)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing condition: %v", err)
	}

	orders, err := json.Marshal(gtt.Orders)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing orders: %v", err)
	}

	params := make(url.Values, 3)
	params.Add("type", string(gtt.Type))
	params.Add("condition", string(condition))
	params.Add("orders", string(orders))

	err = c.doEnvelope(http.MethodPost, URIPlaceGTT, params, nil, &orderResp)
	return orderResp, err
}

// ModifyGTT modifies the condition or orders inside an already created GTT order.
func (c *Client) ModifyGTT(triggerID int, o GTTParams) (GTTResponse, error) {
	gtt := newGTT(o)

	var orderResp GTTResponse
	condition, err := json.Marshal(gtt.Condition)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing condition: %v", err)
	}

	orders, err := json.Marshal(gtt.Orders)
	if err != nil {
		return orderResp, fmt.Errorf("error while parsing orders: %v", err)
	}

	params := make(url.Values, 3)
	params.Add("type", string(gtt.Type))
	params.Add("condition", string(condition))
	params.Add("orders", string(orders))

	err = c.doEnvelope(http.MethodPut, fmt.Sprintf(URIModifyGTT, triggerID), params, nil, &orderResp)
	return orderResp, err
}

// GetGTTs returns the current GTTs for the user.
func (c *Client) GetGTTs() (GTTs, error) {
	var orders GTTs
	err := c.doEnvelope(http.MethodGet, URIGetGTTs, nil, nil, &orders)
	return orders, err
}

// GetGTT returns a specific GTT for the user.
func (c *Client) GetGTT(triggerID int) (GTT, error) {
	var order GTT
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIGetGTT, triggerID), nil, nil, &order)
	return order, err
}

// DeleteGTT deletes a GTT order.
func (c *Client) DeleteGTT(triggerID int) (GTTResponse, error) {
	var order GTTResponse
	err := c.doEnvelope(http.MethodDelete, fmt.Sprintf(URIGetGTT, triggerID), nil, nil, &order)
	return order, err
}
