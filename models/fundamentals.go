package models

import "time"

// Fundamentals are an instrument's company data from Kite's screener. A
// zero ratio means Kite has none, or that it is zero: banks have no debt to
// equity, and companies that pay nothing have no dividend yield.
//
// MarketCap, PE, DividendYield and the 52-week range follow the price, so
// they are as of UpdatedAt; the rest change with each quarter's results.
type Fundamentals struct {
	// InstrumentToken matches the catalog's instrument_token. Join on it
	// rather than on the trading symbol: a stock in the BE series is
	// HFCL-BE here while the index lists it as HFCL.
	InstrumentToken int64  `json:"instrument_token"`
	Exchange        string `json:"exchange"`
	TradingSymbol   string `json:"trading_symbol"`
	ISIN            string `json:"isin"`
	Name            string `json:"name"`
	Sector          string `json:"sector"`
	// Tags are the indices the instrument belongs to, such as "Nifty 500"
	// and "Nifty F&O Stocks".
	Tags []string `json:"tags"`

	// LastPrice is the price when the fundamentals were read.
	LastPrice float64 `json:"last_price"`
	// MarketCap and FreeCashFlow are in ₹ crore; the growth rates, ROE
	// and dividend yield in percent.
	MarketCap        float64 `json:"market_cap"`
	PE               float64 `json:"pe"`
	DividendYield    float64 `json:"dividend_yield"`
	ROE              float64 `json:"roe"`
	DebtToEquity     float64 `json:"debt_to_equity"`
	Week52High       float64 `json:"week_52_high"`
	Week52Low        float64 `json:"week_52_low"`
	RevenueGrowthYOY float64 `json:"revenue_growth_yoy"`
	PATGrowthYOY     float64 `json:"pat_growth_yoy"`
	FreeCashFlow     float64 `json:"free_cash_flow"`

	// UpdatedAt is when Kite last updated the fundamentals, zero for an
	// instrument without any, such as an ETF.
	UpdatedAt time.Time `json:"updated_at"`
}

// ID is the instrument's EXCHANGE:TRADING_SYMBOL.
func (f Fundamentals) ID() InstrumentID {
	return InstrumentID(f.Exchange + ":" + f.TradingSymbol)
}
