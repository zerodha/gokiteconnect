package kite

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
	"github.com/gocarina/gocsv"
	"github.com/google/go-querystring/query"
)

type quoteParams struct {
	Instruments []string `url:"i"`
}

// Quote represents the full quote response, keyed by `exchange:tradingsymbol`.
type Quote map[string]QuoteData

// QuoteData represents the full quote for a single instrument.
type QuoteData struct {
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

// QuoteOHLC represents OHLC quote response, keyed by `exchange:tradingsymbol`.
type QuoteOHLC map[string]QuoteOHLCData

// QuoteOHLCData represents the OHLC quote for a single instrument.
type QuoteOHLCData struct {
	InstrumentToken int         `json:"instrument_token"`
	LastPrice       float64     `json:"last_price"`
	OHLC            models.OHLC `json:"ohlc"`
}

// QuoteLTP represents last price quote response, keyed by `exchange:tradingsymbol`.
type QuoteLTP map[string]QuoteLTPData

// QuoteLTPData represents the last price quote for a single instrument.
type QuoteLTPData struct {
	InstrumentToken int     `json:"instrument_token"`
	LastPrice       float64 `json:"last_price"`
}

// HistoricalData represents individual historical data response.
type HistoricalData struct {
	Date   models.Time `json:"date"`
	Open   float64     `json:"open"`
	High   float64     `json:"high"`
	Low    float64     `json:"low"`
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

const historicalBatchDelay = 100 * time.Millisecond

// HistoricalInterval is Kite's historical-data interval for a candle
// interval, such as "minute" for 1m and "day" for 1d.
func HistoricalInterval(interval models.CandleInterval) (string, bool) {
	switch interval {
	case models.CandleInterval1m:
		return "minute", true
	case models.CandleInterval2m:
		return "2minute", true
	case models.CandleInterval3m:
		return "3minute", true
	case models.CandleInterval4m:
		return "4minute", true
	case models.CandleInterval5m:
		return "5minute", true
	case models.CandleInterval10m:
		return "10minute", true
	case models.CandleInterval15m:
		return "15minute", true
	case models.CandleInterval30m:
		return "30minute", true
	case models.CandleInterval1h:
		return "60minute", true
	case models.CandleInterval2h:
		return "2hour", true
	case models.CandleInterval3h:
		return "3hour", true
	case models.CandleInterval4h:
		return "4hour", true
	case models.CandleInterval1d:
		return "day", true
	case models.CandleInterval1W:
		return "week", true
	}
	return "", false
}

// HistoricalBatchDuration returns the maximum date range used for a single
// historical-data request. These limits mirror the interval-specific batching
// rules used by tradebot.
func HistoricalBatchDuration(interval string) (time.Duration, bool) {
	switch interval {
	case "minute", "2minute":
		return 60 * 24 * time.Hour, true
	case "3minute", "4minute", "5minute", "10minute":
		return 100 * 24 * time.Hour, true
	case "15minute", "30minute":
		return 200 * 24 * time.Hour, true
	case "60minute", "2hour", "3hour", "4hour":
		return 400 * 24 * time.Hour, true
	case "day", "week":
		return 2000 * 24 * time.Hour, true
	default:
		return 0, false
	}
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

// Kite Connect limits on the market quote endpoints, from
// https://kite.trade/docs/connect/v3/market-quotes/ and
// https://kite.trade/docs/connect/v3/exceptions/:
//   - /quote (full quote) returns at most 500 instruments per request.
//   - /quote/ohlc and /quote/ltp return at most 1000 instruments per request.
//   - All quote endpoints share a rate limit of 1 request per second.
const (
	quoteBatchSize       = 500
	quoteLTPBatchSize    = 1000
	quoteOHLCBatchSize   = 1000
	quoteRequestInterval = time.Second
)

// GetQuote gets map of quotes for given instruments in the format of `exchange:tradingsymbol`.
//
// Limits: Kite returns full quotes for at most 500 instruments per request and
// allows 1 quote request per second. Larger lists are split into batches of
// 500 that are requested one after another, each at least a second after the
// client's previous quote request, and merged; 1200 instruments therefore take
// three requests and about two seconds. Duplicate instruments are requested
// once. Not supported with an enctoken session.
func (c *Client) GetQuote(instruments ...string) (Quote, error) {
	return c.getQuote(context.Background(), instruments)
}

func (c *Client) getQuote(ctx context.Context, instruments []string) (Quote, error) {
	if c.enctoken != "" {
		return nil, NewError(InputError, "Endpoint not supported for enctoken", nil)
	}
	return getQuoteBatches[QuoteData](ctx, c, URIGetQuote, quoteBatchSize, instruments)
}

// GetLTP gets map of LTP quotes for given instruments in the format of `exchange:tradingsymbol`.
//
// Limits: Kite returns LTPs for at most 1000 instruments per request and
// allows 1 quote request per second. Larger lists are split into batches of
// 1000 that are requested one after another, each at least a second after the
// client's previous quote request, and merged. Duplicate instruments are
// requested once.
func (c *Client) GetLTP(instruments ...string) (QuoteLTP, error) {
	return getQuoteBatches[QuoteLTPData](context.Background(), c, URIGetLTP, quoteLTPBatchSize, instruments)
}

// GetOHLC gets map of OHLC quotes for given instruments in the format of `exchange:tradingsymbol`.
//
// Limits: Kite returns OHLC quotes for at most 1000 instruments per request
// and allows 1 quote request per second. Larger lists are split into batches
// of 1000 that are requested one after another, each at least a second after
// the client's previous quote request, and merged. Duplicate instruments are
// requested once.
func (c *Client) GetOHLC(instruments ...string) (QuoteOHLC, error) {
	return getQuoteBatches[QuoteOHLCData](context.Background(), c, URIGetOHLC, quoteOHLCBatchSize, instruments)
}

// getQuoteBatches requests instruments in batches of at most batchSize and
// merges the responses. Duplicate instruments are requested once. Like a
// single request, an empty instrument list still makes one request.
func getQuoteBatches[T any](ctx context.Context, c *Client, uri string, batchSize int, instruments []string) (map[string]T, error) {
	instruments = uniqueStrings(instruments)
	quotes := make(map[string]T, len(instruments))
	for start := 0; start == 0 || start < len(instruments); start += batchSize {
		end := min(start+batchSize, len(instruments))
		params, err := query.Values(quoteParams{Instruments: instruments[start:end]})
		if err != nil {
			return nil, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
		}
		if err := c.quotePacer.wait(ctx); err != nil {
			return nil, err
		}
		var batch map[string]T
		if err := c.doEnvelope(http.MethodGet, uri, params, nil, &batch); err != nil {
			return nil, err
		}
		maps.Copy(quotes, batch)
	}
	return quotes, nil
}

// uniqueStrings drops repeated values, keeping the first occurrence's order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			unique = append(unique, value)
		}
	}
	return unique
}

// requestPacer spaces requests at least interval apart. A nil pacer does not
// wait.
type requestPacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRequestPacer(interval time.Duration) *requestPacer {
	return &requestPacer{interval: interval}
}

// wait blocks until the next request may be sent, or ctx is done.
func (p *requestPacer) wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if delay := time.Until(p.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.next = time.Now().Add(p.interval)
	return nil
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
			Date:   models.Time{Time: d},
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

// GetHistoricalData gets historical OHLCV candles for an instrument token.
// Large ranges are split into interval-specific batches internally and the
// results are returned in request order. Adjacent requests use half-open
// [from, to) bounds and are delayed by 100ms to respect upstream rate limits.
// Unknown interval strings retain the legacy single-request behavior.
func (c *Client) GetHistoricalData(instrumentToken int, interval string, fromDate time.Time, toDate time.Time, continuous bool, OI bool) ([]HistoricalData, error) {
	batchSize, batchable := HistoricalBatchDuration(interval)
	if !batchable || !toDate.After(fromDate) || !fromDate.Add(batchSize).Before(toDate) {
		return c.getHistoricalDataBatch(instrumentToken, interval, fromDate, toDate, continuous, OI)
	}

	var data []HistoricalData
	for batchStart := fromDate; batchStart.Before(toDate); {
		batchEnd := batchStart.Add(batchSize)
		if batchEnd.After(toDate) {
			batchEnd = toDate
		}

		batch, err := c.getHistoricalDataBatch(instrumentToken, interval, batchStart, batchEnd, continuous, OI)
		if err != nil {
			return nil, err
		}
		data = append(data, batch...)

		batchStart = batchEnd
		if batchStart.Before(toDate) {
			time.Sleep(historicalBatchDelay)
		}
	}

	return data, nil
}

// GetHistoricalDataBySymbol gets historical OHLCV candles for an instrument ID
// in the format of `exchange:tradingsymbol`. The ID is resolved to a token
// through the instrument catalog's token index, then fetched with
// GetHistoricalData.
func (c *Client) GetHistoricalDataBySymbol(ctx context.Context, id string, interval string, fromDate time.Time, toDate time.Time, continuous bool, OI bool) (candles.Candles, error) {
	tokens, err := c.Instruments().TokenIndex(ctx)
	if err != nil {
		return candles.Candles{}, fmt.Errorf("resolve instrument %q: %w", id, err)
	}
	token, ok := tokens.Token(models.InstrumentID(id))
	if !ok {
		return candles.Candles{}, fmt.Errorf("resolve instrument %q: %w", id, models.ErrNotFound)
	}

	data, err := c.GetHistoricalData(int(token), interval, fromDate, toDate, continuous, OI)
	if err != nil {
		return candles.Candles{}, err
	}

	series := candles.Candles{Data: make([]candles.Candle, len(data))}
	for i, d := range data {
		series.Data[i] = candles.Candle{
			Timestamp: d.Date.Time,
			Open:      d.Open,
			High:      d.High,
			Low:       d.Low,
			Close:     d.Close,
			Volume:    d.Volume,
			OI:        d.OI,
		}
	}
	return series, nil
}

// getHistoricalDataBatch makes exactly one historical-data request.
func (c *Client) getHistoricalDataBatch(instrumentToken int, interval string, fromDate time.Time, toDate time.Time, continuous bool, OI bool) ([]HistoricalData, error) {
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
