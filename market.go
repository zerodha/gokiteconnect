package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/go-querystring/query"
)

type quoteParams struct {
	Instruments []string `url:"i"`
}

// Quote represents individual quote.
type Quote map[string]struct {
	InstrumentToken int     `json:"instrument_token"`
	Timestamp       string  `json:"timestamp"`
	LastPrice       float64 `json:"last_price"`
	LastQuantity    int     `json:"last_quantity"`
	LastTradeTime   string  `json:"last_trade_time"`
	AveragePrice    float64 `json:"average_price"`
	Volume          int     `json:"volume"`
	BuyQuantity     int     `json:"buy_quantity"`
	SellQuantity    int     `json:"sell_quantity"`
	Ohlc            struct {
		Open  float64 `json:"open"`
		High  float64 `json:"high"`
		Low   float64 `json:"low"`
		Close float64 `json:"close"`
	} `json:"ohlc"`
	NetChange float64 `json:"net_change"`
	Oi        float64 `json:"oi"`
	OiDayHigh float64 `json:"oi_day_high"`
	OiDayLow  float64 `json:"oi_day_low"`
	Depth     struct {
		Buy []struct {
			Price    float64 `json:"price"`
			Quantity int     `json:"quantity"`
			Orders   int     `json:"orders"`
		} `json:"buy"`
		Sell []struct {
			Price    float64 `json:"price"`
			Quantity int     `json:"quantity"`
			Orders   int     `json:"orders"`
		} `json:"sell"`
	} `json:"depth"`
}

// QuoteOHLC represents OHLC quote response.
type QuoteOHLC map[string]struct {
	InstrumentToken int     `json:"instrument_token"`
	LastPrice       float64 `json:"last_price"`
	Ohlc            struct {
		Open  float64 `json:"open"`
		High  float64 `json:"high"`
		Low   float64 `json:"low"`
		Close float64 `json:"close"`
	} `json:"ohlc"`
}

// QuoteLTP represents last price quote response.
type QuoteLTP map[string]struct {
	InstrumentToken int     `json:"instrument_token"`
	LastPrice       float64 `json:"last_price"`
}

// GetQuote gets map of quotes.
func (c *Client) GetQuote(instruments ...string) (Quote, error) {
	var (
		err     error
		quotes  Quote
		params  url.Values
		qParams quoteParams
	)

	qParams = quoteParams{
		Instruments: instruments,
	}

	if params, err = query.Values(qParams); err != nil {
		return quotes, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodGet, URIGetQuote, params, nil, &quotes)
	return quotes, err
}

// GetLTP gets map of quotes.
func (c *Client) GetLTP(instruments ...string) (QuoteLTP, error) {
	var (
		err     error
		quotes  QuoteLTP
		params  url.Values
		qParams quoteParams
	)

	qParams = quoteParams{
		Instruments: instruments,
	}

	if params, err = query.Values(qParams); err != nil {
		return quotes, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodGet, URIGetQuote, params, nil, &quotes)
	return quotes, err
}

// GetOHLC gets map of quotes.
func (c *Client) GetOHLC(instruments ...string) (QuoteOHLC, error) {
	var (
		err     error
		quotes  QuoteOHLC
		params  url.Values
		qParams quoteParams
	)

	qParams = quoteParams{
		Instruments: instruments,
	}

	if params, err = query.Values(qParams); err != nil {
		return quotes, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	err = c.doEnvelope(http.MethodGet, URIGetQuote, params, nil, &quotes)
	return quotes, err
}
