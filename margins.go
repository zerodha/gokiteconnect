package kiteconnect

import (
	"encoding/json"
	"net/http"
	"net/url"
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
	Leverage      float64 `json:"leverage"`
	Charges       Charges `json:"charges"`
	Total         float64 `json:"total"`
}

// Charges represents breakdown of various charges that are applied to an order
type Charges struct {
	TransactionTax         float64 `json:"transaction_tax"`
	TransactionTaxType     string  `json:"transaction_tax_type"`
	ExchangeTurnoverCharge float64 `json:"exchange_turnover_charge"`
	SEBITurnoverCharge     float64 `json:"sebi_turnover_charge"`
	Brokerage              float64 `json:"brokerage"`
	StampDuty              float64 `json:"stamp_duty"`
	GST                    GST     `json:"gst"`
	Total                  float64 `json:"total"`
}

// GST represents the various GST charges
type GST struct {
	IGST  float64 `json:"igst"`
	CGST  float64 `json:"cgst"`
	SGST  float64 `json:"sgst"`
	Total float64 `json:"total"`
}

// BasketMargins represents response from the Margin Calculator API for Basket orders
type BasketMargins struct {
	Initial OrderMargins   `json:"initial"`
	Final   OrderMargins   `json:"final"`
	Orders  []OrderMargins `json:"orders"`
}

type GetMarginParams struct {
	OrderParams []OrderMarginParam
	Compact     bool
}

type GetBasketParams struct {
	OrderParams       []OrderMarginParam
	Compact           bool
	ConsiderPositions bool
}

func (c *Client) GetOrderMargins(marparam GetMarginParams) ([]OrderMargins, error) {
	body, err := json.Marshal(marparam.OrderParams)
	if err != nil {
		return []OrderMargins{}, err
	}

	var headers http.Header = map[string][]string{}
	headers.Add("Content-Type", "application/json")

	uri := URIOrderMargins
	if marparam.Compact {
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

func (c *Client) GetBasketMargins(baskparam GetBasketParams) (BasketMargins, error) {
	body, err := json.Marshal(baskparam.OrderParams)
	if err != nil {
		return BasketMargins{}, err
	}

	var headers http.Header = map[string][]string{}
	headers.Add("Content-Type", "application/json")

	uri := URIBasketMargins
	v := url.Values{}

	if baskparam.Compact {
		v.Set("mode", "compact")
	}
	if baskparam.ConsiderPositions {
		v.Set("consider_positions", "true")
	}
	if qp := v.Encode(); qp != "" {
		uri += "?" + qp
	}

	resp, err := c.doRaw(http.MethodPost, uri, body, headers)
	if err != nil {
		return BasketMargins{}, err
	}

	var out BasketMargins
	if err := readEnvelope(resp, &out); err != nil {
		return BasketMargins{}, err
	}

	return out, nil
}
