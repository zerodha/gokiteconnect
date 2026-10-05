package models

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidInput indicates that a query, filter, or source row is invalid.
	ErrInvalidInput = errors.New("instruments: invalid input")
	// ErrNotFound indicates that an instrument does not exist in the catalog.
	ErrNotFound = errors.New("instruments: instrument not found")
)

// InstrumentID is the canonical EXCHANGE:TRADING_SYMBOL identifier for an
// instrument. The first colon is the separator; a trading symbol may contain
// additional colons.
type InstrumentID string

// ParseInstrumentID validates and canonicalizes an instrument ID.
func ParseInstrumentID(value string) (InstrumentID, error) {
	exchange, tradingSymbol, ok := strings.Cut(strings.TrimSpace(value), ":")
	exchange = strings.TrimSpace(exchange)
	tradingSymbol = strings.TrimSpace(tradingSymbol)
	if !ok || exchange == "" || tradingSymbol == "" {
		return "", fmt.Errorf("%w: instrument ID %q must use EXCHANGE:TRADING_SYMBOL", ErrInvalidInput, value)
	}
	return InstrumentID(strings.ToUpper(exchange) + ":" + strings.ToUpper(tradingSymbol)), nil
}

// Instrument contains normalized metadata from the Zerodha instrument master.
type Instrument struct {
	ID            InstrumentID `json:"id"`
	Exchange      string       `json:"exchange"`
	TradingSymbol string       `json:"trading_symbol"`

	InstrumentToken int64  `json:"instrument_token"`
	ExchangeToken   string `json:"exchange_token"`

	Name         *string `json:"name"`
	DisplayName  string  `json:"display_name"`
	SearchString string  `json:"search_string"`

	Expiry             *time.Time    `json:"expiry"`
	ExpiryNumber       *int          `json:"expiry_number"`
	Strike             float64       `json:"strike"`
	TickSize           float64       `json:"tick_size"`
	LotSize            int           `json:"lot_size"`
	InstrumentType     string        `json:"instrument_type"`
	Segment            string        `json:"segment"`
	IsFO               bool          `json:"is_fo"`
	UnderlyingID       *InstrumentID `json:"underlying_id"`
	UnderlyingIsListed bool          `json:"underlying_is_listed"`
	OptionsCount       int           `json:"options_count"`
	FuturesCount       int           `json:"futures_count"`
}

// StrikeRange bounds option strikes. A nil bound is open-ended.
type StrikeRange struct {
	Min *float64
	Max *float64
}

// OptionType identifies a call or put contract.
type OptionType string

const (
	OptionTypeCall OptionType = "CE"
	OptionTypePut  OptionType = "PE"
)

// FuturesFilter selects futures for one underlying instrument.
type FuturesFilter struct {
	UnderlyingID  InstrumentID
	ExpiryNumbers []int
	ExpiryDates   []time.Time
}

// OptionsFilter selects options for one underlying instrument. An empty Types
// slice includes both calls and puts.
type OptionsFilter struct {
	UnderlyingID  InstrumentID
	ExpiryNumbers []int
	ExpiryDates   []time.Time
	Strikes       *StrikeRange
	Types         []OptionType
}
