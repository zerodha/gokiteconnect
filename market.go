package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/google/go-querystring/query"
	"github.com/zerodha/gokiteconnect/v4/models"
)

type quoteParams struct {
	Instruments []string `url:"i"`
}

// Quote represents the full quote response.
type Quote map[string]struct {
	InstrumentToken   int          `json:"instrument_token"`
	Timestamp         models.Time  `json:"timestamp"`
	LastPrice         float64      `json:"last_price"`
	LastQuantity      int          `json:"last_quantity"`
	LastTradeTime     models.Time  `json:"last_trade_time"`
	AveragePrice      float64      `json:"average_price"`
	Volume            int          `json:"volume"`
	BuyQuantity       int          `json:"buy_quantity"`
	SellQuantity      int          `json:"sell_quantity"`
	OHLC              models.OHLC  `json:"ohlc"`
	NetChange         float64      `json:"net_change"`
	OI                float64      `json:"oi"`
	OIDayHigh         float64      `json:"oi_day_high"`
	OIDayLow          float64      `json:"oi_day_low"`
	LowerCircuitLimit float64      `json:"lower_circuit_limit"`
	UpperCircuitLimit float64      `json:"upper_circuit_limit"`
	Depth             models.Depth `json:"depth"`
}

// QuoteOHLC represents OHLC quote response.
type QuoteOHLC map[string]struct {
	InstrumentToken int         `json:"instrument_token"`
	LastPrice       float64     `json:"last_price"`
	OHLC            models.OHLC `json:"ohlc"`
}

// QuoteLTP represents last price quote response.
type QuoteLTP map[string]struct {
	InstrumentToken int     `json:"instrument_token"`
	LastPrice       float64 `json:"last_price"`
}

// HistoricalData represents individual historical data response.
type HistoricalData struct {
	Date   models.Time `json:"date"`
	Open   float64     `json:"open"`
	High   float64     `json:"high"`
	Low    float64     `json:"Low"`
	Close  float64     `json:"close"`
	Volume int         `json:"volume"`
	OI     int         `json:"oi"`
}

type historicalDataReceived struct {
	Candles [][]interface{} `json:"candles"`
}

type historicalDataParams struct {
	FromDate        string `url:"from"`
	ToDate          string `url:"to"`
	Continuous      int    `url:"continuous"`
	OI              int    `url:"oi"`
	InstrumentToken int    `url:"instrument_token"`
	Interval        string `url:"interval"`
}

// Instrument represents individual instrument response.
type Instrument struct {
	InstrumentToken int         `csv:"instrument_token"`
	ExchangeToken   int         `csv:"exchange_token"`
	Tradingsymbol   string      `csv:"tradingsymbol"`
	Name            string      `csv:"name"`
	LastPrice       float64     `csv:"last_price"`
	Expiry          models.Time `csv:"expiry"`
	StrikePrice     float64     `csv:"strike"`
	TickSize        float64     `csv:"tick_size"`
	LotSize         float64     `csv:"lot_size"`
	InstrumentType  string      `csv:"instrument_type"`
	Segment         string      `csv:"segment"`
	Exchange        string      `csv:"exchange"`
}

// Instruments represents list of instruments.
type Instruments []Instrument

// MFInstrument represents individual mutualfund instrument response.
type MFInstrument struct {
	Tradingsymbol string  `csv:"tradingsymbol"`
	Name          string  `csv:"name"`
	LastPrice     float64 `csv:"last_price"`
	AMC           string  `csv:"amc"`

	PurchaseAllowed                 bool        `csv:"purchase_allowed"`
	RedemtpionAllowed               bool        `csv:"redemption_allowed"`
	MinimumPurchaseAmount           float64     `csv:"minimum_purchase_amount"`
	PurchaseAmountMultiplier        float64     `csv:"purchase_amount_multiplier"`
	MinimumAdditionalPurchaseAmount float64     `csv:"additional_purchase_multiple"`
	MinimumRedemptionQuantity       float64     `csv:"minimum_redemption_quantity"`
	RedemptionQuantityMultiplier    float64     `csv:"redemption_quantity_multiplier"`
	DividendType                    string      `csv:"dividend_type"`
	SchemeType                      string      `csv:"scheme_type"`
	Plan                            string      `csv:"plan"`
	SettlementType                  string      `csv:"settlement_type"`
	LastPriceDate                   models.Time `csv:"last_price_date"`
}

// MFInstruments represents list of mutualfund instruments.
type MFInstruments []MFInstrument

// GetQuote gets map of quotes for given instruments in the format of `exchange:tradingsymbol`.
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

// GetLTP gets map of LTP quotes for given instruments in the format of `exchange:tradingsymbol`.
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

// GetOHLC gets map of OHLC quotes for given instruments in the format of `exchange:tradingsymbol`.
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
			volume int
			OI     int
			ok     bool
		)

		if ds, ok = i[0].(string); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `date`: %v", i[0]), nil)
		}

		if open, ok = i[1].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `open`: %v", i[1]), nil)
		}

		if high, ok = i[2].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `high`: %v", i[2]), nil)
		}

		if low, ok = i[3].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `low`: %v", i[3]), nil)
		}

		if close, ok = i[4].(float64); !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `close`: %v", i[4]), nil)
		}

		// Assert volume
		v, ok := i[5].(float64)
		if !ok {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `volume`: %v", i[5]), nil)
		}

		volume = int(v)
		// Did we get OI?
		if len(i) > 6 {
			// Assert OI
			OIT, ok := i[6].(float64)
			if !ok {
				return data, NewError(GeneralError, fmt.Sprintf("Error decoding response `oi`: %v", i[6]), nil)
			}
			OI = int(OIT)
		}

		// Parse string to date
		d, err := time.Parse("2006-01-02T15:04:05-0700", ds)
		if err != nil {
			return data, NewError(GeneralError, fmt.Sprintf("Error decoding response: %v", err), nil)
		}

		data = append(data, HistoricalData{
			Date:   models.Time{d},
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
			OI:     OI,
		})
	}

	return data, nil
}

// GetHistoricalData gets list of historical data.
func (c *Client) GetHistoricalData(instrumentToken int, interval string, fromDate time.Time, toDate time.Time, continuous bool, OI bool) ([]HistoricalData, error) {
	var (
		err       error
		data      []HistoricalData
		params    url.Values
		inpParams historicalDataParams
	)

	inpParams.InstrumentToken = instrumentToken
	inpParams.Interval = interval
	inpParams.FromDate = fromDate.Format("2006-01-02 15:04:05")
	inpParams.ToDate = toDate.Format("2006-01-02 15:04:05")
	inpParams.Continuous = 0
	inpParams.OI = 0

	if continuous {
		inpParams.Continuous = 1
	}

	if OI {
		inpParams.OI = 1
	}

	if params, err = query.Values(inpParams); err != nil {
		return data, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	var resp historicalDataReceived
	if err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIGetHistorical, instrumentToken, interval), params, nil, &resp); err != nil {
		return data, err
	}

	return c.formatHistoricalData(resp)
}

func (c *Client) parseInstruments(data interface{}, url string, params url.Values) error {
	var (
		err  error
		resp HTTPResponse
	)

	// Get CSV response
	if resp, err = c.do(http.MethodGet, url, params, nil); err != nil {
		return err
	}

	// Unmarshal CSV response to instruments
	if err = gocsv.UnmarshalBytes(resp.Body, data); err != nil {
		return NewError(GeneralError, fmt.Sprintf("Error parsing csv response: %v", err), nil)
	}

	return nil
}

// GetInstruments retrives list of instruments.
func (c *Client) GetInstruments() (Instruments, error) {
	var instruments Instruments
	err := c.parseInstruments(&instruments, URIGetInstruments, nil)
	return instruments, err
}

// GetInstrumentsByExchange retrives list of instruments for a given exchange.
func (c *Client) GetInstrumentsByExchange(exchange string) (Instruments, error) {
	var instruments Instruments
	err := c.parseInstruments(&instruments, fmt.Sprintf(URIGetInstrumentsExchange, exchange), nil)
	return instruments, err
}

// GetMFInstruments retrives list of mutualfund instruments.
func (c *Client) GetMFInstruments() (MFInstruments, error) {
	var instruments MFInstruments
	err := c.parseInstruments(&instruments, URIGetMFInstruments, nil)
	return instruments, err
}
