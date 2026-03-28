package kiteconnect

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/zerodha/gokiteconnect/v4/models"
)

// URI constants for Alerts API
const (
	URIAlerts       = "/alerts"
	URIAlert        = "/alerts/%s"
	URIAlertHistory = "/alerts/%s/history"
)

// AlertType represents the type of alert.
type AlertType string

const (
	AlertTypeSimple AlertType = "simple"
	AlertTypeATO    AlertType = "ato"
)

// AlertStatus represents the status of an alert.
type AlertStatus string

const (
	AlertStatusEnabled  AlertStatus = "enabled"
	AlertStatusDisabled AlertStatus = "disabled"
	AlertStatusDeleted  AlertStatus = "deleted"
)

// AlertOperator represents the comparison operator.
type AlertOperator string

const (
	AlertOperatorLE AlertOperator = "<="
	AlertOperatorGE AlertOperator = ">="
	AlertOperatorLT AlertOperator = "<"
	AlertOperatorGT AlertOperator = ">"
	AlertOperatorEQ AlertOperator = "=="
)

// Alert represents a market price alert.
type Alert struct {
	Type             AlertType     `json:"type"`
	UserID           string        `json:"user_id"`
	UUID             string        `json:"uuid"`
	Name             string        `json:"name"`
	Status           AlertStatus   `json:"status"`
	DisabledReason   string        `json:"disabled_reason"`
	LHSAttribute     string        `json:"lhs_attribute"`
	LHSExchange      string        `json:"lhs_exchange"`
	LHSTradingSymbol string        `json:"lhs_tradingsymbol"`
	Operator         AlertOperator `json:"operator"`
	RHSType          string        `json:"rhs_type"`
	RHSAttribute     string        `json:"rhs_attribute"`
	RHSExchange      string        `json:"rhs_exchange"`
	RHSTradingSymbol string        `json:"rhs_tradingsymbol"`
	RHSConstant      float64       `json:"rhs_constant"`
	AlertCount       int           `json:"alert_count"`
	CreatedAt        models.Time   `json:"created_at"`
	UpdatedAt        models.Time   `json:"updated_at"`
	Basket           *Basket       `json:"basket,omitempty"`
}

// AlertParams represents parameters for creating or modifying an alert.
type AlertParams struct {
	Name             string        // required
	Type             AlertType     // required
	LHSExchange      string        // required
	LHSTradingSymbol string        // required
	LHSAttribute     string        // required
	Operator         AlertOperator // required
	RHSType          string        // required ("constant" or "instrument")
	RHSConstant      float64       // required if RHSType == "constant"
	RHSExchange      string        // required if RHSType == "instrument"
	RHSTradingSymbol string        // required if RHSType == "instrument"
	RHSAttribute     string        // required if RHSType == "instrument"
	Basket           *Basket       // required if Type == AlertTypeATO
}

// Basket represents the basket structure for ATO alerts.
type Basket struct {
	Name  string       `json:"name"`
	Type  string       `json:"type"`
	Tags  []string     `json:"tags"`
	Items []BasketItem `json:"items"`
}

// BasketItem represents an item in the basket.
type BasketItem struct {
	Type            string           `json:"type"`
	TradingSymbol   string           `json:"tradingsymbol"`
	Exchange        string           `json:"exchange"`
	Weight          int              `json:"weight"`
	Params          AlertOrderParams `json:"params"`
	ID              int              `json:"id,omitempty"`
	InstrumentToken int              `json:"instrument_token,omitempty"`
}

// AlertOrderParams represents order parameters for a basket item in Alerts API.
type AlertOrderParams struct {
	TransactionType   string          `json:"transaction_type"`
	Product           string          `json:"product"`
	OrderType         string          `json:"order_type"`
	Validity          string          `json:"validity"`
	ValidityTTL       int             `json:"validity_ttl"`
	Quantity          int             `json:"quantity"`
	Price             float64         `json:"price"`
	TriggerPrice      float64         `json:"trigger_price"`
	DisclosedQuantity int             `json:"disclosed_quantity"`
	LastPrice         float64         `json:"last_price"`
	Variety           string          `json:"variety"`
	Tags              []string        `json:"tags"`
	Squareoff         float64         `json:"squareoff"`
	Stoploss          float64         `json:"stoploss"`
	TrailingStoploss  float64         `json:"trailing_stoploss"`
	IcebergLegs       int             `json:"iceberg_legs"`
	MarketProtection  float64         `json:"market_protection"`
	GTT               *OrderGTTParams `json:"gtt,omitempty"`
}

// OrderGTTParams represents GTT-specific params in order.
type OrderGTTParams struct {
	Target   float64 `json:"target"`
	Stoploss float64 `json:"stoploss"`
}

// AlertHistory represents a single alert trigger history entry.
type AlertHistory struct {
	UUID      string             `json:"uuid"`
	Type      AlertType          `json:"type"`
	Meta      []AlertHistoryMeta `json:"meta"`
	Condition string             `json:"condition"`
	CreatedAt models.Time        `json:"created_at"`
	OrderMeta interface{}        `json:"order_meta"`
}

// AlertHistoryMeta represents meta info for alert history.
type AlertHistoryMeta struct {
	InstrumentToken   int     `json:"instrument_token"`
	TradingSymbol     string  `json:"tradingsymbol"`
	Timestamp         string  `json:"timestamp"`
	LastPrice         float64 `json:"last_price"`
	OHLC              models.OHLC `json:"ohlc"`
	NetChange         float64 `json:"net_change"`
	Exchange          string  `json:"exchange"`
	LastTradeTime     string  `json:"last_trade_time"`
	LastQuantity      int     `json:"last_quantity"`
	BuyQuantity       int     `json:"buy_quantity"`
	SellQuantity      int     `json:"sell_quantity"`
	Volume            int     `json:"volume"`
	VolumeTick        int     `json:"volume_tick"`
	AveragePrice      float64 `json:"average_price"`
	OI                int     `json:"oi"`
	OIDayHigh         int     `json:"oi_day_high"`
	OIDayLow          int     `json:"oi_day_low"`
	LowerCircuitLimit float64 `json:"lower_circuit_limit"`
	UpperCircuitLimit float64 `json:"upper_circuit_limit"`
}

// CreateAlert creates a new alert.
func (c *Client) CreateAlert(params AlertParams) (Alert, error) {
	var (
		alert  Alert
		values = make(url.Values)
	)

	values.Set("name", params.Name)
	values.Set("type", string(params.Type))
	values.Set("lhs_exchange", params.LHSExchange)
	values.Set("lhs_tradingsymbol", params.LHSTradingSymbol)
	values.Set("lhs_attribute", params.LHSAttribute)
	values.Set("operator", string(params.Operator))
	values.Set("rhs_type", params.RHSType)

	if params.RHSType == "constant" {
		values.Set("rhs_constant", fmt.Sprintf("%v", params.RHSConstant))
	} else if params.RHSType == "instrument" {
		values.Set("rhs_exchange", params.RHSExchange)
		values.Set("rhs_tradingsymbol", params.RHSTradingSymbol)
		values.Set("rhs_attribute", params.RHSAttribute)
	}

	if params.Type == AlertTypeATO && params.Basket != nil {
		basketJSON, err := json.Marshal(params.Basket)
		if err != nil {
			return alert, fmt.Errorf("error marshaling basket: %v", err)
		}
		values.Set("basket", string(basketJSON))
	}

	err := c.doEnvelope(http.MethodPost, URIAlerts, values, nil, &alert)
	return alert, err
}

// GetAlerts retrieves all alerts for a user, with optional filters.
func (c *Client) GetAlerts(filters map[string]string) ([]Alert, error) {
	var (
		alerts []Alert
		params = url.Values{}
	)
	for k, v := range filters {
		params.Set(k, v)
	}
	err := c.doEnvelope(http.MethodGet, URIAlerts, params, nil, &alerts)
	return alerts, err
}

// GetAlert retrieves a specific alert by UUID.
func (c *Client) GetAlert(uuid string) (Alert, error) {
	var alert Alert
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIAlert, uuid), nil, nil, &alert)
	return alert, err
}

// ModifyAlert modifies an existing alert by UUID.
func (c *Client) ModifyAlert(uuid string, params AlertParams) (Alert, error) {
	var (
		alert  Alert
		values = make(url.Values)
	)

	values.Set("name", params.Name)
	values.Set("type", string(params.Type))
	values.Set("lhs_exchange", params.LHSExchange)
	values.Set("lhs_tradingsymbol", params.LHSTradingSymbol)
	values.Set("lhs_attribute", params.LHSAttribute)
	values.Set("operator", string(params.Operator))
	values.Set("rhs_type", params.RHSType)

	if params.RHSType == "constant" {
		values.Set("rhs_constant", fmt.Sprintf("%v", params.RHSConstant))
	} else if params.RHSType == "instrument" {
		values.Set("rhs_exchange", params.RHSExchange)
		values.Set("rhs_tradingsymbol", params.RHSTradingSymbol)
		values.Set("rhs_attribute", params.RHSAttribute)
	}

	if params.Type == AlertTypeATO && params.Basket != nil {
		basketJSON, err := json.Marshal(params.Basket)
		if err != nil {
			return alert, fmt.Errorf("error marshaling basket: %v", err)
		}
		values.Set("basket", string(basketJSON))
	}

	err := c.doEnvelope(http.MethodPut, fmt.Sprintf(URIAlert, uuid), values, nil, &alert)
	return alert, err
}

// DeleteAlerts deletes one or more alerts by UUID.
func (c *Client) DeleteAlerts(uuids ...string) error {
	if len(uuids) == 0 {
		return fmt.Errorf("at least one uuid must be provided")
	}
	params := url.Values{}
	for _, uuid := range uuids {
		params.Add("uuid", uuid)
	}
	// The API returns {"status":"success","data":null}
	var resp struct {
		Status string      `json:"status"`
		Data   interface{} `json:"data"`
	}
	deleteURL := URIAlerts + "?" + params.Encode()
	err := c.doEnvelope(http.MethodDelete, deleteURL, nil, nil, &resp)
	return err
}

// GetAlertHistory retrieves the history of a specific alert.
func (c *Client) GetAlertHistory(uuid string) ([]AlertHistory, error) {
	var history []AlertHistory
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIAlertHistory, uuid), nil, nil, &history)
	return history, err
}
