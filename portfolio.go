package kiteconnect

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/go-querystring/query"
	"github.com/zerodha/gokiteconnect/v4/models"
)

const (
	HolAuthTypeMF     = "mf"
	HolAuthTypeEquity = "equity"

	HolAuthTransferTypePreTrade  = "pre"
	HolAuthTransferTypePostTrade = "post"
	HolAuthTransferTypeOffMarket = "off"
	HolAuthTransferTypeGift      = "gift"
)

// Holding is an individual holdings response.
type Holding struct {
	Tradingsymbol   string `json:"tradingsymbol"`
	Exchange        string `json:"exchange"`
	InstrumentToken uint32 `json:"instrument_token"`
	ISIN            string `json:"isin"`
	Product         string `json:"product"`

	Price              float64     `json:"price"`
	UsedQuantity       int         `json:"used_quantity"`
	Quantity           int         `json:"quantity"`
	T1Quantity         int         `json:"t1_quantity"`
	RealisedQuantity   int         `json:"realised_quantity"`
	AuthorisedQuantity int         `json:"authorised_quantity"`
	AuthorisedDate     models.Time `json:"authorised_date"`
	OpeningQuantity    int         `json:"opening_quantity"`
	CollateralQuantity int         `json:"collateral_quantity"`
	CollateralType     string      `json:"collateral_type"`

	Discrepancy         bool    `json:"discrepancy"`
	AveragePrice        float64 `json:"average_price"`
	LastPrice           float64 `json:"last_price"`
	ClosePrice          float64 `json:"close_price"`
	PnL                 float64 `json:"pnl"`
	DayChange           float64 `json:"day_change"`
	DayChangePercentage float64 `json:"day_change_percentage"`
}

// Holdings is a list of holdings
type Holdings []Holding

// Position represents an individual position response.
type Position struct {
	Tradingsymbol   string `json:"tradingsymbol"`
	Exchange        string `json:"exchange"`
	InstrumentToken uint32 `json:"instrument_token"`
	Product         string `json:"product"`

	Quantity          int     `json:"quantity"`
	OvernightQuantity int     `json:"overnight_quantity"`
	Multiplier        float64 `json:"multiplier"`

	AveragePrice float64 `json:"average_price"`
	ClosePrice   float64 `json:"close_price"`
	LastPrice    float64 `json:"last_price"`
	Value        float64 `json:"value"`
	PnL          float64 `json:"pnl"`
	M2M          float64 `json:"m2m"`
	Unrealised   float64 `json:"unrealised"`
	Realised     float64 `json:"realised"`

	BuyQuantity int     `json:"buy_quantity"`
	BuyPrice    float64 `json:"buy_price"`
	BuyValue    float64 `json:"buy_value"`
	BuyM2MValue float64 `json:"buy_m2m"`

	SellQuantity int     `json:"sell_quantity"`
	SellPrice    float64 `json:"sell_price"`
	SellValue    float64 `json:"sell_value"`
	SellM2MValue float64 `json:"sell_m2m"`

	DayBuyQuantity int     `json:"day_buy_quantity"`
	DayBuyPrice    float64 `json:"day_buy_price"`
	DayBuyValue    float64 `json:"day_buy_value"`

	DaySellQuantity int     `json:"day_sell_quantity"`
	DaySellPrice    float64 `json:"day_sell_price"`
	DaySellValue    float64 `json:"day_sell_value"`
}

// Positions represents a list of net and day positions.
type Positions struct {
	Net []Position `json:"net"`
	Day []Position `json:"day"`
}

// ConvertPositionParams represents the input params for a position conversion.
type ConvertPositionParams struct {
	Exchange        string `url:"exchange"`
	TradingSymbol   string `url:"tradingsymbol"`
	OldProduct      string `url:"old_product"`
	NewProduct      string `url:"new_product"`
	PositionType    string `url:"position_type"`
	TransactionType string `url:"transaction_type"`
	Quantity        int    `url:"quantity"`
}

// GetHoldings gets a list of holdings.
func (c *Client) GetHoldings() (Holdings, error) {
	var holdings Holdings
	err := c.doEnvelope(http.MethodGet, URIGetHoldings, nil, nil, &holdings)
	return holdings, err
}

// GetPositions gets user positions.
func (c *Client) GetPositions() (Positions, error) {
	var positions Positions
	err := c.doEnvelope(http.MethodGet, URIGetPositions, nil, nil, &positions)
	return positions, err
}

// ConvertPosition converts postion's product type.
func (c *Client) ConvertPosition(positionParams ConvertPositionParams) (bool, error) {
	params, err := query.Values(positionParams)
	if err != nil {
		return false, NewError(InputError, fmt.Sprintf("Error decoding order params: %v", err), nil)
	}

	var b bool
	if err = c.doEnvelope(http.MethodPut, URIConvertPosition, params, nil, nil); err == nil {
		b = true
	}

	return b, err
}

// HoldingsAuthInstruments represents the instruments and respective quantities for
// use within the holdings auth initialization.
type HoldingsAuthInstruments struct {
	ISIN     string
	Quantity float64
}

// HoldingAuthParams represents the inputs for initiating holdings authorization.
type HoldingAuthParams struct {
	Type         string
	TransferType string
	ExecDate     string

	// Instruments are optional
	Instruments []HoldingsAuthInstruments
}

// HoldingsAuthParams represents the response from initiating holdings authorization
type HoldingsAuthResp struct {
	RequestID   string `json:"request_id"`
	RedirectURL string
}

// InitiateHoldingsAuth initiates the holdings authorization flow. It accepts an optional
// list of HoldingsAuthInstruments which can be used to specify a set of ISINs with their
// respective quantities. Since, the isin and quantity pairs here are optional, you can
// provide it as nil. If they're provided, authorisation is sought only
// for those instruments and otherwise, the entire holdings is presented for
// authorisation. The response contains the RequestID which can then be used to
// redirect the user in a web view. The client forms and returns the
// formed RedirectURL as well.
func (c *Client) InitiateHoldingsAuth(haps HoldingAuthParams) (HoldingsAuthResp, error) {
	params := make(url.Values, len(haps.Instruments)+3)

	if haps.Type != "" {
		params.Set("type", haps.Type)
	}

	if haps.TransferType != "" {
		params.Set("transfer_type", haps.TransferType)
	}

	if haps.ExecDate != "" {
		params.Set("exec_date", haps.ExecDate)
	}

	for _, hap := range haps.Instruments {
		params.Add("isin", hap.ISIN)

		// NOTE: Why prec for FormatFloat is 6?
		// https://github.com/golang/go/issues/46118#issuecomment-839537854
		params.Add("quantity", strconv.FormatFloat(hap.Quantity, 'f', 6, 64))
	}

	var resp HoldingsAuthResp
	if err := c.doEnvelope(http.MethodPost, URIInitHoldingsAuth, params, nil, &resp); err != nil {
		return resp, err
	}

	// Form and set the URL in the response.
	resp.RedirectURL = genHolAuthURL(c.apiKey, resp.RequestID)

	return resp, nil
}

func genHolAuthURL(apiKey, reqID string) string {
	return kiteBaseURI + "/connect/portfolio/authorize/holdings/" + apiKey + "/" + reqID
}
