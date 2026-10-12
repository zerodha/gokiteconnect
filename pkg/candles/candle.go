package candles

import "time"

type Candle struct {
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
