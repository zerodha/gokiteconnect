package models

// TickField names one value of an instrument's tick: a price, a change,
// a quantity or open interest. Triggers watch one, and ticker-server
// publishes them all with each tick under these names.
type TickField string

const (
	FieldLastPrice             TickField = "last_price"
	FieldHigh                  TickField = "high"
	FieldLow                   TickField = "low"
	FieldOpen                  TickField = "open"
	FieldClose                 TickField = "close"
	FieldDayChange             TickField = "day_change"
	FieldDayChangePercent      TickField = "day_change_percent"
	FieldIntradayChange        TickField = "intraday_change"
	FieldIntradayChangePercent TickField = "intraday_change_percent"
	FieldLastTradedQty         TickField = "last_traded_qty"
	FieldAvgTradedPrice        TickField = "avg_traded_price"
	FieldVolume                TickField = "volume"
	FieldTotalBuyQty           TickField = "total_buy_qty"
	FieldTotalSellQty          TickField = "total_sell_qty"
	FieldOI                    TickField = "oi"
	FieldOIDayHigh             TickField = "oi_day_high"
	FieldOIDayLow              TickField = "oi_day_low"
)

// TickFieldKind is what a field measures, which says how to show it.
type TickFieldKind int

const (
	// KindPrice is a price, in rupees.
	KindPrice TickFieldKind = iota
	// KindChange is a change in price, in rupees, which can be negative.
	KindChange
	// KindPercent is a change in percent, which can be negative.
	KindPercent
	// KindQuantity is a count: shares, lots' worth or contracts.
	KindQuantity
)

// TickFieldInfo describes a field: its name, its label as Kite shows it,
// and what it measures.
type TickFieldInfo struct {
	Field TickField
	Label string
	Kind  TickFieldKind
}

// TickFields are every field, in the order Kite lists them.
var TickFields = []TickFieldInfo{
	{FieldLastPrice, "Last price", KindPrice},
	{FieldHigh, "High price", KindPrice},
	{FieldLow, "Low price", KindPrice},
	{FieldOpen, "Open price", KindPrice},
	{FieldClose, "Close price", KindPrice},
	{FieldDayChange, "Day change", KindChange},
	{FieldDayChangePercent, "Day change %", KindPercent},
	{FieldIntradayChange, "Intraday change", KindChange},
	{FieldIntradayChangePercent, "Intraday change %", KindPercent},
	{FieldLastTradedQty, "Last traded qty.", KindQuantity},
	{FieldAvgTradedPrice, "Avg. traded price", KindPrice},
	{FieldVolume, "Volume", KindQuantity},
	{FieldTotalBuyQty, "Total buy qty.", KindQuantity},
	{FieldTotalSellQty, "Total sell qty.", KindQuantity},
	{FieldOI, "OI", KindQuantity},
	{FieldOIDayHigh, "OI day high", KindQuantity},
	{FieldOIDayLow, "OI day low", KindQuantity},
}

// Info describes the field; ok is false for a name no field has.
func (f TickField) Info() (TickFieldInfo, bool) {
	for _, info := range TickFields {
		if info.Field == f {
			return info, true
		}
	}
	return TickFieldInfo{}, false
}

// Value is the tick's value of field. ok is false when the tick has no
// price yet, or the field needs a price the tick lacks: the changes need
// the previous close or the day's open. Day change is measured from the
// close, not taken from NetChange, which quotes leave at zero.
func (t Tick) Value(field TickField) (value float64, ok bool) {
	if !(t.LastPrice > 0) {
		return 0, false
	}
	positive := func(value float64) (float64, bool) { return value, value > 0 }
	switch field {
	case FieldLastPrice:
		return t.LastPrice, true
	case FieldHigh:
		return positive(t.OHLC.High)
	case FieldLow:
		return positive(t.OHLC.Low)
	case FieldOpen:
		return positive(t.OHLC.Open)
	case FieldClose:
		return positive(t.OHLC.Close)
	case FieldDayChange, FieldDayChangePercent:
		if !(t.OHLC.Close > 0) {
			return 0, false
		}
		change := t.LastPrice - t.OHLC.Close
		if field == FieldDayChangePercent {
			return change / t.OHLC.Close * 100, true
		}
		return change, true
	case FieldIntradayChange, FieldIntradayChangePercent:
		if !(t.OHLC.Open > 0) {
			return 0, false
		}
		change := t.LastPrice - t.OHLC.Open
		if field == FieldIntradayChangePercent {
			return change / t.OHLC.Open * 100, true
		}
		return change, true
	case FieldLastTradedQty:
		return float64(t.LastTradedQuantity), true
	case FieldAvgTradedPrice:
		return positive(t.AverageTradePrice)
	case FieldVolume:
		return float64(t.VolumeTraded), true
	case FieldTotalBuyQty:
		return float64(t.TotalBuyQuantity), true
	case FieldTotalSellQty:
		return float64(t.TotalSellQuantity), true
	case FieldOI:
		return float64(t.OI), true
	case FieldOIDayHigh:
		return float64(t.OIDayHigh), true
	case FieldOIDayLow:
		return float64(t.OIDayLow), true
	}
	return 0, false
}

// Values are the tick's fields that it has values for, by name.
func (t Tick) Values() map[TickField]float64 {
	values := make(map[TickField]float64, len(TickFields))
	for _, info := range TickFields {
		if value, ok := t.Value(info.Field); ok {
			values[info.Field] = value
		}
	}
	return values
}
