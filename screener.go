package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/zerodha/gokiteconnect/v4/models"
)

// URI constants for the Screener API.
const (
	URIScreenerMeta        = "/screener/meta"
	URIScreenerInstruments = "/screener/instruments"
	URIScreenerSearch      = "/screener/instruments/search"
	URIScreenerPresets     = "/screener/presets"
	URIScreenerPreset      = "/screener/presets/%s" // "/screener/presets/{preset_name}"
	URIScreenerQueries     = "/screener/queries"
	URIScreenerQuery       = "/screener/queries/%s" // "/screener/queries/{query_id}"
)

// ScreenerInstrument is a single instrument row returned by a screen.
// Fields with no data (e.g. fundamentals for an instrument without them) are zero.
type ScreenerInstrument struct {
	Segment         string `json:"segment"`
	InstrumentToken string `json:"instrument_token"`
	TradingSymbol   string `json:"tradingsymbol"`
	ExchangeToken   string `json:"exchange_token"`
	ISIN            string `json:"isin"`
	Name            string `json:"name"`

	Timestamp         models.Time `json:"timestamp"`
	LastTradeTime     models.Time `json:"last_trade_time"`
	LastPrice         float64     `json:"last_price"`
	BuyQuantity       float64     `json:"buy_quantity"`
	SellQuantity      float64     `json:"sell_quantity"`
	Volume            float64     `json:"volume"`
	Change            float64     `json:"change"`
	ChangePercent     float64     `json:"change_percent"`
	Open              float64     `json:"open"`
	High              float64     `json:"high"`
	Low               float64     `json:"low"`
	Close             float64     `json:"close"`
	LowerCircuitLimit float64     `json:"lower_circuit_limit"`
	UpperCircuitLimit float64     `json:"upper_circuit_limit"`
	AveragePrice      float64     `json:"average_price"`
	TradeValue        float64     `json:"trade_value"`

	Sector               string  `json:"sector"`
	MarketCap            float64 `json:"market_cap"`
	PE                   float64 `json:"pe"`
	DividendYield        float64 `json:"dividend_yield"`
	ROE                  float64 `json:"roe"`
	DebtToEquity         float64 `json:"debt_to_equity"`
	Week52High           float64 `json:"week_52_high"`
	Week52Low            float64 `json:"week_52_low"`
	RevenueGrowthYoY     float64 `json:"revenue_growth_yoy"`
	PATGrowthYoY         float64 `json:"pat_growth_yoy"`
	FreeCashFlow         float64 `json:"free_cash_flow"`
	FundamentalUpdatedAt string  `json:"fundamental_updated_at"`

	Tags []string `json:"tags"`
}

// ScreenerResults is a page of instruments matching a screen.
type ScreenerResults struct {
	Results    []ScreenerInstrument `json:"results"`
	Total      int                  `json:"total"`
	Page       int                  `json:"page"`
	PerPage    int                  `json:"per_page"`
	TotalPages int                  `json:"total_pages"`
}

// ScreenerParams are the optional filtering, sorting and pagination parameters
// for running a screen or a preset.
type ScreenerParams struct {
	// Tags limits results to instruments in any of these index universes (e.g. "Nifty 50").
	Tags    []string
	OrderBy string
	// Order is "asc" or "desc".
	Order   string
	Page    int
	PerPage int
	// All returns every matching instrument in one response, ignoring Page and PerPage.
	All bool
}

// ScreenerSearchResult is an instrument returned by SearchScreenerInstruments.
type ScreenerSearchResult struct {
	Segment         string  `json:"segment"`
	InstrumentToken string  `json:"instrument_token"`
	TradingSymbol   string  `json:"tradingsymbol"`
	Name            string  `json:"name"`
	ISIN            string  `json:"isin"`
	LastPrice       float64 `json:"last_price"`
	ChangePercent   float64 `json:"change_percent"`
}

// ScreenerFilter describes one field that screens can filter on.
type ScreenerFilter struct {
	Name string `json:"name"`
	// Type is "range" or "distinct".
	Type string `json:"type"`
	// Values is [min, max] for a range filter and a list of strings for a distinct filter.
	Values interface{} `json:"values"`
}

// ScreenerMeta lists the filterable fields, keyed by field name, and the default filters.
type ScreenerMeta struct {
	Filters map[string]ScreenerFilter `json:"filters"`
	Default []string                  `json:"default"`
}

// ScreenerPreset is a built-in screen.
type ScreenerPreset struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Query          string `json:"query"`
	OrderBy        string `json:"order_by"`
	Order          string `json:"order"`
	Type           string `json:"type"`
	HasComputation bool   `json:"has_computation"`
}

// ScreenerQuery is a screen saved by the user.
type ScreenerQuery struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Query     string      `json:"query"`
	Tags      []string    `json:"tags"`
	CreatedAt models.Time `json:"created_at"`
	UpdatedAt models.Time `json:"updated_at"`
}

// ScreenerQueryParams are the parameters for saving a screen.
type ScreenerQueryParams struct {
	Name  string
	Query string
	// Tags is the index universe the screen runs against. Leaving it empty clears it.
	Tags []string
}

// GetScreenerMeta returns the fields that screens can filter on.
func (c *Client) GetScreenerMeta() (ScreenerMeta, error) {
	var meta ScreenerMeta
	err := c.doEnvelope(http.MethodGet, URIScreenerMeta, nil, nil, &meta)
	return meta, err
}

// RunScreener runs a screen and returns the matching instruments.
// query separates conditions with "&", e.g. "volume>10000&pe<15".
func (c *Client) RunScreener(query string, params ScreenerParams) (ScreenerResults, error) {
	var (
		res    ScreenerResults
		values = params.values()
	)
	values.Set("query", query)

	err := c.doEnvelope(http.MethodPost, URIScreenerInstruments, values, nil, &res)
	return res, err
}

// SearchScreenerInstruments finds instruments by trading symbol prefix or name.
// q must be at least 2 characters. A zero limit uses the server default.
func (c *Client) SearchScreenerInstruments(q string, limit int) ([]ScreenerSearchResult, error) {
	var (
		res struct {
			Results []ScreenerSearchResult `json:"results"`
		}
		values = url.Values{}
	)
	values.Set("q", q)
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}

	err := c.doEnvelope(http.MethodGet, URIScreenerSearch, values, nil, &res)
	return res.Results, err
}

// GetScreenerPresets returns the built-in screens.
func (c *Client) GetScreenerPresets() ([]ScreenerPreset, error) {
	var res struct {
		Presets []ScreenerPreset `json:"presets"`
	}
	err := c.doEnvelope(http.MethodGet, URIScreenerPresets, nil, nil, &res)
	return res.Presets, err
}

// RunScreenerPreset runs a built-in screen. OrderBy and Order in params
// override the preset's own sort.
func (c *Client) RunScreenerPreset(key string, params ScreenerParams) (ScreenerResults, error) {
	var res ScreenerResults
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIScreenerPreset, url.PathEscape(key)), params.values(), nil, &res)
	return res, err
}

// GetScreenerQueries returns the user's saved screens.
func (c *Client) GetScreenerQueries() ([]ScreenerQuery, error) {
	var queries []ScreenerQuery
	err := c.doEnvelope(http.MethodGet, URIScreenerQueries, nil, nil, &queries)
	return queries, err
}

// CreateScreenerQuery saves a new screen for the user.
func (c *Client) CreateScreenerQuery(params ScreenerQueryParams) (ScreenerQuery, error) {
	var query ScreenerQuery
	err := c.doEnvelope(http.MethodPost, URIScreenerQueries, params.values(), nil, &query)
	return query, err
}

// ModifyScreenerQuery replaces the name, query and tags of a saved screen.
func (c *Client) ModifyScreenerQuery(queryID string, params ScreenerQueryParams) (ScreenerQuery, error) {
	var query ScreenerQuery
	err := c.doEnvelope(http.MethodPut, fmt.Sprintf(URIScreenerQuery, url.PathEscape(queryID)), params.values(), nil, &query)
	return query, err
}

// DeleteScreenerQuery deletes a saved screen.
func (c *Client) DeleteScreenerQuery(queryID string) error {
	return c.doEnvelope(http.MethodDelete, fmt.Sprintf(URIScreenerQuery, url.PathEscape(queryID)), nil, nil, nil)
}

func (p ScreenerParams) values() url.Values {
	values := url.Values{}
	for _, t := range p.Tags {
		values.Add("tags", t)
	}
	if p.OrderBy != "" {
		values.Set("order_by", p.OrderBy)
	}
	if p.Order != "" {
		values.Set("order", p.Order)
	}
	if p.All {
		values.Set("all", "true")
		return values
	}
	if p.Page > 0 {
		values.Set("page", strconv.Itoa(p.Page))
	}
	if p.PerPage > 0 {
		values.Set("per_page", strconv.Itoa(p.PerPage))
	}
	return values
}

func (p ScreenerQueryParams) values() url.Values {
	values := url.Values{}
	values.Set("name", p.Name)
	values.Set("query", p.Query)
	for _, t := range p.Tags {
		values.Add("tags", t)
	}
	return values
}
