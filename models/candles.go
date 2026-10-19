package models

import "time"

// ExchangeLocation is the exchange's timezone, India Standard Time, which
// candle clocks are in.
var ExchangeLocation = time.FixedZone("IST", 5*60*60+30*60)

// ExchangeClockTime keeps ts's clock fields and puts them in
// ExchangeLocation. Candle storage keeps timezone-naive exchange clocks,
// which SQL drivers scan as UTC; this restores the instant without moving
// the hour and minute.
func ExchangeClockTime(ts time.Time) time.Time {
	if ts.IsZero() {
		return ts
	}
	return time.Date(ts.Year(), ts.Month(), ts.Day(), ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(), ExchangeLocation)
}

// CandleInterval is a candle's length.
type CandleInterval string

// Supported candle intervals.
const (
	CandleInterval1m  CandleInterval = "1m"
	CandleInterval2m  CandleInterval = "2m"
	CandleInterval3m  CandleInterval = "3m"
	CandleInterval4m  CandleInterval = "4m"
	CandleInterval5m  CandleInterval = "5m"
	CandleInterval10m CandleInterval = "10m"
	CandleInterval15m CandleInterval = "15m"
	CandleInterval30m CandleInterval = "30m"
	CandleInterval1h  CandleInterval = "1h"
	CandleInterval2h  CandleInterval = "2h"
	CandleInterval3h  CandleInterval = "3h"
	CandleInterval4h  CandleInterval = "4h"
	CandleInterval1d  CandleInterval = "1d"
	CandleInterval1W  CandleInterval = "1W"
)

var candleIntervalDurations = map[CandleInterval]time.Duration{
	CandleInterval1m:  time.Minute,
	CandleInterval2m:  2 * time.Minute,
	CandleInterval3m:  3 * time.Minute,
	CandleInterval4m:  4 * time.Minute,
	CandleInterval5m:  5 * time.Minute,
	CandleInterval10m: 10 * time.Minute,
	CandleInterval15m: 15 * time.Minute,
	CandleInterval30m: 30 * time.Minute,
	CandleInterval1h:  time.Hour,
	CandleInterval2h:  2 * time.Hour,
	CandleInterval3h:  3 * time.Hour,
	CandleInterval4h:  4 * time.Hour,
	CandleInterval1d:  24 * time.Hour,
	CandleInterval1W:  7 * 24 * time.Hour,
}

// Duration is the interval's length, and false for an unsupported interval.
func (i CandleInterval) Duration() (time.Duration, bool) {
	duration, ok := candleIntervalDurations[i]
	return duration, ok
}

// IsValid reports whether i is a supported interval.
func (i CandleInterval) IsValid() bool {
	_, ok := candleIntervalDurations[i]
	return ok
}

// IsIntraday reports whether i is a supported interval shorter than a day.
func (i CandleInterval) IsIntraday() bool {
	return i.IsValid() && i != CandleInterval1d && i != CandleInterval1W
}

// GetCandlesParams selects candles.
type GetCandlesParams struct {
	// InstrumentIDs are the instruments; empty means all of them.
	InstrumentIDs []InstrumentID
	// Intervals are the candle intervals; empty means 15m. 1m can't be mixed
	// with other intervals.
	Intervals []CandleInterval
	// Start and End bound the candles, End exclusive. A zero Start means
	// 2020-01-01 and a zero End means now.
	Start time.Time
	End   time.Time
	// Warmup also returns up to this many candles immediately before Start
	// for every instrument. It needs exactly one interval.
	Warmup int
}

// CandleAvailability is the range of stored candles of an instrument and
// interval: historical, and live when live candles are attached.
type CandleAvailability struct {
	InstrumentID    InstrumentID
	InstrumentToken int64
	Interval        CandleInterval
	HistStart       time.Time
	HistEnd         time.Time
	LiveStart       *time.Time
	LiveEnd         *time.Time
}

// LastClosePrice is the latest stored close of an instrument.
type LastClosePrice struct {
	InstrumentID    InstrumentID
	InstrumentToken int64
	Timestamp       time.Time
	LTP             float64
}

// ExpirySettlement is the final price of an expired derivative contract and
// when it expired.
type ExpirySettlement struct {
	Price     float64
	ExpiresAt time.Time
}
