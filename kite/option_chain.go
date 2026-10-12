package kite

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

// OptionChainRequest selects one expiry of an underlying's options.
type OptionChainRequest struct {
	// UnderlyingID is the underlying's EXCHANGE:TRADING_SYMBOL ID, such as
	// NSE:NIFTY 50 or NSE:RELIANCE.
	UnderlyingID models.InstrumentID
	// ExpiryDate selects the expiry by date. When it is zero, ExpiryNumber is
	// used instead.
	ExpiryDate time.Time
	// ExpiryNumber selects the expiry by position, counting the underlying's
	// futures and options expiries from 0 for the nearest, as in
	// models.OptionsFilter.
	ExpiryNumber int
	// Strikes keeps only strikes within this range. A nil bound is open-ended.
	Strikes *models.StrikeRange
	// StrikesAroundATM keeps at most this many strikes on each side of the
	// at-the-money strike, the strike nearest the underlying's last price.
	// Zero keeps every strike. It is applied after Strikes.
	StrikesAroundATM int
}

// OptionChain is one expiry of an underlying's options with full quotes.
type OptionChain struct {
	Underlying      models.Instrument
	UnderlyingPrice float64
	Expiry          time.Time
	ExpiryNumber    int
	// ATMStrike is the strike nearest UnderlyingPrice. It is zero when the
	// underlying has no quote.
	ATMStrike float64
	// Rows are sorted by strike.
	Rows []OptionChainRow
}

// OptionChainRow holds the call and put at one strike. A side is nil when
// the exchange lists no contract for it.
type OptionChainRow struct {
	Strike float64
	Call   *OptionChainLeg
	Put    *OptionChainLeg
}

// OptionChainLeg is one option contract. Quote is nil when Kite returned no
// quote for it.
type OptionChainLeg struct {
	Instrument models.Instrument
	Quote      *QuoteData
}

// GetOptionChain builds an option chain for one expiry from the instrument
// catalog and full market quotes. It needs an instrument repository (see
// SetInstrumentRepository) and an access-token session, since full quotes are
// not available with an enctoken.
//
// Limits: contracts are quoted through GetQuote, so at most 500 instruments go
// in each request and requests are at least a second apart. A chain of N
// contracts takes ceil((N+1)/500) requests, or one more when StrikesAroundATM
// is set, because the underlying is quoted first to find the ATM strike. A
// full NIFTY expiry is a few hundred contracts and fits in one or two
// requests; clamp with Strikes or StrikesAroundATM to fetch fewer contracts.
func (c *Client) GetOptionChain(ctx context.Context, request OptionChainRequest) (OptionChain, error) {
	if request.StrikesAroundATM < 0 {
		return OptionChain{}, fmt.Errorf("%w: strikes around ATM cannot be negative", models.ErrInvalidInput)
	}
	catalog := c.Instruments()
	underlying, err := catalog.Get(ctx, request.UnderlyingID)
	if err != nil {
		return OptionChain{}, fmt.Errorf("option chain underlying %q: %w", request.UnderlyingID, err)
	}

	filter := models.OptionsFilter{UnderlyingID: underlying.ID, Strikes: request.Strikes}
	if request.ExpiryDate.IsZero() {
		filter.ExpiryNumbers = []int{request.ExpiryNumber}
	} else {
		filter.ExpiryDates = []time.Time{request.ExpiryDate}
	}
	options, err := catalog.GetOptions(ctx, filter)
	if err != nil {
		return OptionChain{}, err
	}
	if len(options) == 0 {
		return OptionChain{}, fmt.Errorf("%w: no options for %s at the requested expiry and strikes", models.ErrNotFound, underlying.ID)
	}

	chain := OptionChain{Underlying: underlying, Rows: optionChainRows(options)}
	if first := options[0]; first.Expiry != nil {
		chain.Expiry = *first.Expiry
	}
	if first := options[0]; first.ExpiryNumber != nil {
		chain.ExpiryNumber = *first.ExpiryNumber
	}

	// Clamping around ATM needs the underlying's price before the contracts
	// are chosen; otherwise its quote rides along with the contracts'.
	var quotes Quote
	if request.StrikesAroundATM > 0 {
		if quotes, err = c.getQuote(ctx, []string{string(underlying.ID)}); err != nil {
			return OptionChain{}, err
		}
		price, ok := quotes[string(underlying.ID)]
		if !ok || price.LastPrice <= 0 {
			return OptionChain{}, fmt.Errorf("%w: no price for underlying %s to find the ATM strike", models.ErrNotFound, underlying.ID)
		}
		chain.UnderlyingPrice = price.LastPrice
		chain.ATMStrike = atmStrike(chain.Rows, chain.UnderlyingPrice)
		chain.Rows = clampAroundStrike(chain.Rows, chain.ATMStrike, request.StrikesAroundATM)
	}

	ids := make([]string, 0, 2*len(chain.Rows)+1)
	if quotes == nil {
		ids = append(ids, string(underlying.ID))
	}
	for _, row := range chain.Rows {
		for _, leg := range []*OptionChainLeg{row.Call, row.Put} {
			if leg != nil {
				ids = append(ids, string(leg.Instrument.ID))
			}
		}
	}
	legQuotes, err := c.getQuote(ctx, ids)
	if err != nil {
		return OptionChain{}, err
	}
	if quotes == nil {
		if price, ok := legQuotes[string(underlying.ID)]; ok {
			chain.UnderlyingPrice = price.LastPrice
			chain.ATMStrike = atmStrike(chain.Rows, chain.UnderlyingPrice)
		}
	}
	for _, row := range chain.Rows {
		for _, leg := range []*OptionChainLeg{row.Call, row.Put} {
			if leg == nil {
				continue
			}
			if quote, ok := legQuotes[string(leg.Instrument.ID)]; ok {
				leg.Quote = &quote
			}
		}
	}
	return chain, nil
}

// optionChainRows groups options into rows sorted by strike.
func optionChainRows(options []models.Instrument) []OptionChainRow {
	byStrike := make(map[float64]*OptionChainRow)
	for _, option := range options {
		row, ok := byStrike[option.Strike]
		if !ok {
			row = &OptionChainRow{Strike: option.Strike}
			byStrike[option.Strike] = row
		}
		leg := &OptionChainLeg{Instrument: option}
		switch models.OptionType(option.InstrumentType) {
		case models.OptionTypeCall:
			row.Call = leg
		case models.OptionTypePut:
			row.Put = leg
		}
	}
	rows := make([]OptionChainRow, 0, len(byStrike))
	for _, row := range byStrike {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Strike < rows[j].Strike })
	return rows
}

// atmStrike returns the row strike nearest price, preferring the lower strike
// on a tie, or zero when price is not positive.
func atmStrike(rows []OptionChainRow, price float64) float64 {
	if price <= 0 || len(rows) == 0 {
		return 0
	}
	best := rows[0].Strike
	for _, row := range rows[1:] {
		if math.Abs(row.Strike-price) < math.Abs(best-price) {
			best = row.Strike
		}
	}
	return best
}

// clampAroundStrike keeps the row at strike and up to count rows on each side.
func clampAroundStrike(rows []OptionChainRow, strike float64, count int) []OptionChainRow {
	center := sort.Search(len(rows), func(i int) bool { return rows[i].Strike >= strike })
	if center == len(rows) {
		return rows
	}
	return rows[max(0, center-count):min(len(rows), center+count+1)]
}
