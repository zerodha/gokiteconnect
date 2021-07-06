package kiteconnect

import (
	"encoding/json"
	"net/http"
)

// OrderMarginParam represents an order in the Margin Calculator API
type OrderMarginParam struct {
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

// PNL represents the PNL
type PNL struct {
	Realised   float64 `json:"realised"`
	Unrealised float64 `json:"unrealised"`
}

// OrdersMargins represents response from the Margin Calculator API.
type OrderMargins struct {
	Type          string `json:"type"`
	TradingSymbol string `json:"tradingsymbol"`
	Exchange      string `json:"exchange"`

	SPAN          float64 `json:"span"`
	Exposure      float64 `json:"exposure"`
	OptionPremium float64 `json:"option_premium"`
	Additional    float64 `json:"additional"`
	BO            float64 `json:"bo"`
	Cash          float64 `json:"cash"`
	VAR           float64 `json:"var"`
	PNL           PNL     `json:"pnl"`
	Total         float64 `json:"total"`
}

// BaksetMargins represents response from the Margin Calculator API for Basket orders
type BaksetMargins struct {
	Initial OrderMargins   `json:"initial"`
	Final   OrderMargins   `json:"final"`
	Orders  []OrderMargins `json:"orders"`
}

func (c *Client) GetOrderMargins(orderParams []OrderMarginParam, compactMode bool) ([]OrderMargins, error) {
	body, err := json.Marshal(orderParams)
	if err != nil {
		return []OrderMargins{}, err
	}

	var headers http.Header = map[string][]string{}
	headers.Add("Content-Type", "application/json")

	uri := URIOrderMargins
	if compactMode {
		uri += "?mode=compact"
	}

	resp, err := c.doRaw(http.MethodPost, uri, body, headers)
	if err != nil {
		return []OrderMargins{}, err
	}

	var out []OrderMargins
	if err := readEnvelope(resp, &out); err != nil {
		return []OrderMargins{}, err
	}

	return out, nil
}

func (c *Client) GetBasketMargins(orderParams []OrderMarginParam, considerPositions bool, compactMode bool) (BaksetMargins, error) {
	body, err := json.Marshal(orderParams)
	if err != nil {
		return BaksetMargins{}, err
	}

	var headers http.Header = map[string][]string{}
	headers.Add("Content-Type", "application/json")

	uri := URIBasketMargins
	if compactMode || considerPositions {
		uri += "?"
	}
	if considerPositions {
		uri += "consider_positions=true"
	}
	if compactMode && considerPositions {
		uri += "&mode=compact"
	} else if compactMode {
		uri += "mode=compact"
	}

	resp, err := c.doRaw(http.MethodPost, uri, body, headers)
	if err != nil {
		return BaksetMargins{}, err
	}

	var out BaksetMargins
	if err := readEnvelope(resp, &out); err != nil {
		return BaksetMargins{}, err
	}

	return out, nil
}
