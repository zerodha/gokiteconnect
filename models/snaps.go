package models

// OHLC represents OHLC packets.
type OHLC struct {
	InstrumentToken uint32  `json:"-"`
	Open            float64 `json:"open"`
	High            float64 `json:"high"`
	Low             float64 `json:"low"`
	Close           float64 `json:"close"`
}

// DepthItem represents a single market depth entry.
type DepthItem struct {
	Price    float64 `json:"price"`
	Quantity uint32  `json:"quantity"`
	Orders   uint32  `json:"orders"`
}

// Depth represents a group of buy/sell market depths.
type Depth struct {
	Buy  [5]DepthItem `json:"buy"`
	Sell [5]DepthItem `json:"sell"`
}

// Tick represents a single packet in the market feed.
type Tick struct {
	Mode            string
	InstrumentToken uint32
	IsTradable      bool
	IsIndex         bool

	Timestamp          Time
	LastTradeTime      Time
	LastPrice          float64
	LastTradedQuantity uint32
	TotalBuyQuantity   uint32
	TotalSellQuantity  uint32
	VolumeTraded       uint32
	TotalBuy           uint32
	TotalSell          uint32
	AverageTradePrice  float64
	OI                 uint32
	OIDayHigh          uint32
	OIDayLow           uint32
	NetChange          float64

	OHLC  OHLC
	Depth Depth
}
