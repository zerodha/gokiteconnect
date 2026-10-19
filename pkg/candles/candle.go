package candles

import (
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

type Candle struct {
	// InstrumentID, InstrumentToken and Interval say what the candle is of.
	// Kite's historical data leaves them empty, as a series is of one
	// instrument; the candles repository fills them.
	InstrumentID    models.InstrumentID   `json:"instrument_id,omitempty"`
	InstrumentToken int64                 `json:"instrument_token,omitempty"`
	Interval        models.CandleInterval `json:"interval,omitempty"`

	Timestamp time.Time `json:"date"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    int       `json:"volume"`
	OI        int       `json:"oi"`
}

// Candles is a time-ordered series of candles.
type Candles struct {
	Data []Candle
}
