package models

import "time"

// PaperAccount is a user's paper account: the cash it opened with and how
// far its day-end work has run.
type PaperAccount struct {
	Capital   float64
	CreatedAt time.Time
	// SettledThrough is the last trading day whose day-end work (cancelling
	// day orders, squaring off intraday positions, settling expiries) is
	// done, as midnight IST.
	SettledThrough time.Time
}

// Paper fill kinds: an order's trade, or a contract settled at expiry.
const (
	PaperFillTrade  = "trade"
	PaperFillExpiry = "expiry"
)

// PaperFill is one execution in a paper account. Positions, holdings and
// funds are all worked out from the fills, so they are the account's
// record; each carries its contract's terms, so an expired contract still
// settles once the catalog has dropped it.
type PaperFill struct {
	TradeID         string
	OrderID         string
	Kind            string
	Exchange        string
	TradingSymbol   string
	InstrumentToken uint32
	Product         string
	TransactionType string
	Quantity        int
	Price           float64
	// Charges are the fill's share of its order's charges.
	Charges  float64
	FilledAt time.Time

	// InstrumentType is EQ, FUT, CE or PE.
	InstrumentType string
	Expiry         time.Time
	Strike         float64
	// Underlying is the derivative's underlying, priced to settle it.
	Underlying      InstrumentID
	UnderlyingToken uint32
}

// ID is the fill's EXCHANGE:TRADINGSYMBOL.
func (f PaperFill) ID() InstrumentID {
	return InstrumentID(f.Exchange + ":" + f.TradingSymbol)
}
