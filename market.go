package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/go-querystring/query"
)

type quoteParams struct {
	Instruments []string `url:"i"`
}

// Quote represents individual quote.
type Quote map[string]struct {
	InstrumentToken int     `json:"instrument_token"`
	Timestamp       Time    `json:"timestamp"`
	LastPrice       float64 `json:"last_price"`
	LastQuantity    int     `json:"last_quantity"`
	LastTradeTime   Time    `json:"last_trade_time"`
	AveragePrice    float64 `json:"average_price"`
	Volume          int     `json:"volume"`
	BuyQuantity     int     `json:"buy_quantity"`
	SellQuantity    int     `json:"sell_quantity"`
	OHLC            struct {
		Open  float64 `json:"open"`
		High  float64 `json:"high"`
		Low   float64 `json:"low"`
		Close float64 `json:"close"`
	} `json:"ohlc"`
	NetChange float64 `json:"net_change"`
	OI        float64 `json:"oi"`
	OIDayHigh float64 `json:"oi_day_high"`
	OIDayLow  float64 `json:"oi_day_low"`
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
	OHLC            struct {
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

// HistoricalData represents individual historical data point.
type HistoricalData struct {
	Date   time.Time `json:"date"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"Low"`
	Close  float64   `json:"close"`
	Volume int64     `json:"volume"`
}

type historicalDataReceived struct {
	Candles [][]interface{} `json:"candles"`
}

type historicalDataParams struct {
	fromDate        string `url:"from"`
	toDate          string `url:"to"`
	continuous      int    `url:"continuous"`
	instrumentToken int    `url:"instrument_token"`
	interval        string `url:"interval"`
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

func (c *Client) formatHistoricalData(inp historicalDataReceived) ([]HistoricalData, error) {
	var data []HistoricalData

	for _, i := range inp.Candles {
		var (
			ds     string
			open   float64
			high   float64
			low    float64
			close  float64
			volume int64
			ok     bool
		)

		if ds, ok = i[0].(string); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", ds), nil)
		}

		if open, ok = i[0].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", ds), nil)
		}

		if high, ok = i[0].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", ds), nil)
		}

		if low, ok = i[0].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", ds), nil)
		}

		if close, ok = i[0].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", ds), nil)
		}

		if volume, ok = i[0].(int64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", ds), nil)
		}

		d, err := time.Parse("2006-01-02T15:04:05-0700", ds)
		if err != nil {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", err), nil)
		}

		data = append(data, HistoricalData{
			Date:   d,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
		})
	}

	return data, nil
}

// GetHistoricalData gets list of historical data.
func (c *Client) GetHistoricalData(instrumentToken int, interval string, fromDate time.Time, toDate time.Time, continuous bool) ([]HistoricalData, error) {
	var (
		err       error
		data      []HistoricalData
		params    url.Values
		inpParams historicalDataParams
	)

	inpParams.instrumentToken = instrumentToken
	inpParams.interval = interval
	inpParams.fromDate = fromDate.Format("2006/01/02 15:04:05")
	inpParams.toDate = toDate.Format("2006/01/02 15:04:05")
	inpParams.continuous = 0

	if continuous {
		inpParams.continuous = 1
	}

	if params, err = query.Values(inpParams); err != nil {
		return data, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	var resp historicalDataReceived
	if c.doEnvelope(http.MethodGet, URIGetHistorical, params, nil, &resp); err != nil {
		return data, err
	}

	return c.formatHistoricalData(resp)
}
