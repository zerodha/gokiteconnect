package kiteconnect

import (
	"fmt"
	"encoding/json"
	"net/http"
)

// OrderParam represent parameter for fetching Order margins
type OrderParam struct {
	Exchange        string  `json:"exchange"`
	Tradingsymbol   string  `json:"tradingsymbol"`
	TransactionType string  `json:"transaction_type"`
	Variety         string  `json:"variety"`
	Product         string  `json:"product"`
	OrderType       string  `json:"order_type"`
	Quantity        float64 `json:"quantity"`
	Price           float64 `json:"price,omitempty"`
	TriggerPrice    float64 `json:"trigger_price,omitempty"`
}

// MarginResponse represents the Order margin response structure
type MarginResponse struct {
	ContractType    string    `json:"type"`
	TradingSymbol   string    `json:"tradingsymbol"`
	Exchange        string    `json:"exchange"`
	Span            float64   `json:"span,omitempty"`
	Exposure        float64   `json:"exposure,omitempty"`
	OptionPremium   float64   `json:"option_premium,omitempty"`
	Additional      float64   `json:"additional,omitempty"`
	Bo              float64   `json:"bo,omitempty"`
	Cash            float64   `json:"cash,omitempty"`
	Var             float64   `json:"var,omitempty"`
	Pnl             struct {
		Realised    float64    `json:"realised,omitempty"`
		UnRealised  float64    `json:"unrealised,omitempty"` 
	} `json:"pnl,omitempty"`  
	Total           float64    `json:"total,omitempty"`
}


// MarginResponses is a list of order margin response
type MarginResponses []MarginResponse


// OrderMargin fetch margins for order/order list
func (c *Client) GetOrderMargin(orderParams []OrderParam) (MarginResponses, error) {
	var (
		marginResponses MarginResponses
		params []byte
		err error
	)
	params, err = json.Marshal(orderParams)
	if err != nil {
		return marginResponses, NewError(InputError, fmt.Sprintf("Error decoding order Order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodPost, URIOrderMargin, params, nil, &marginResponses)
	return marginResponses, err
}

