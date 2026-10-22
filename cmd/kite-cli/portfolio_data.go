package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// portfolio is one user's positions, holdings and mutual funds. Each section
// loads independently, so one failing (say, funds on a web session) does not
// hide the others.
type portfolio struct {
	positions    []kite.Position
	holdings     kite.Holdings
	funds        kite.MFHoldings
	orders       kite.Orders
	margins      kite.AllMargins
	marginsErr   error
	positionsErr error
	holdingsErr  error
	fundsErr     error
	ordersErr    error
	loadedAt     time.Time
	// mode is the account the portfolio is of.
	mode broker.Mode
	// loggedIn reports that loading the portfolio logged the user in again.
	loggedIn bool
	// matcher fills a paper account's open orders between loads; nil for a
	// live account.
	matcher broker.Matcher
	// instruments holds the catalog entries of the positions and of their
	// underlyings, by ID. Positions missing from it show their trading symbol
	// and group under themselves.
	instruments    map[models.InstrumentID]models.Instrument
	instrumentsErr error
	// prices reprices positions and holdings from quotes; pricedAt is when
	// they were last priced and pricesErr why that last failed.
	prices    priceSource
	pricedAt  time.Time
	pricesErr error
	// tickerErr is why price mode ticker fell back to quotes.
	tickerErr error
	// underlyings are the quotes of the positions' underlyings, by ID.
	underlyings kite.QuoteOHLC
	// orderCharges are the estimated charges of each order's executed part,
	// by order ID; chargesErr is why they are missing.
	orderCharges map[string]kite.Charges
	chargesErr   error
}

// portfolioLoader reads a user's portfolio from their account in mode.
type portfolioLoader func(context.Context, credentials.Credentials, broker.Mode) portfolio

// instrumentResolver looks up the catalog entries of positions and of their
// underlyings.
type instrumentResolver func(context.Context, []kite.Position) (map[models.InstrumentID]models.Instrument, error)

// accountPortfolioLoader reads the portfolio of the account accounts opens.
// A live account is the user's stored session, checked with a profile
// request and logged in again only when there is none or Kite reports it
// expired. Positions are then named and grouped by the instruments resolve
// finds.
func accountPortfolioLoader(accounts broker.Accounts, resolve instrumentResolver) portfolioLoader {
	return func(ctx context.Context, creds credentials.Credentials, mode broker.Mode) portfolio {
		account, err := accounts.Open(ctx, creds.UserID, mode)
		if err != nil {
			result := failedPortfolio(err)
			result.mode = mode
			return result
		}
		result := readPortfolio(account)
		result.mode = mode
		if live, ok := account.(broker.KiteAccount); ok {
			result.loggedIn = live.LoggedIn
		}
		result.matcher, _ = account.(broker.Matcher)
		result.prices = account
		// Orders are resolved too, for their lot and tick sizes.
		lookups := slices.Clone(result.positions)
		for _, order := range result.orders {
			lookups = append(lookups, kite.Position{Exchange: order.Exchange, Tradingsymbol: order.TradingSymbol})
		}
		if len(lookups) > 0 && resolve != nil {
			result.instruments, result.instrumentsErr = resolve(ctx, lookups)
		}
		// Priced once resolved, so the underlyings are priced too.
		return result.reprice()
	}
}

// catalogInstruments resolves instruments from the instrument catalog, which
// open returns (opening it on first use). Instruments the catalog does not
// know are left out.
func catalogInstruments(open catalogFactory) instrumentResolver {
	return func(ctx context.Context, positions []kite.Position) (map[models.InstrumentID]models.Instrument, error) {
		catalog, _, err := open(ctx)
		if err != nil {
			return nil, fmt.Errorf("instrument catalog: %w", err)
		}
		instruments := make(map[models.InstrumentID]models.Instrument, len(positions))
		lookup := func(id models.InstrumentID) (models.Instrument, bool, error) {
			if instrument, done := instruments[id]; done {
				return instrument, true, nil
			}
			instrument, err := catalog.Get(ctx, id)
			if err != nil {
				return models.Instrument{}, false, ctx.Err()
			}
			instruments[id] = instrument
			return instrument, true, nil
		}
		for _, position := range positions {
			instrument, found, err := lookup(positionID(position))
			if err != nil {
				return instruments, err
			}
			if !found || instrument.UnderlyingID == nil {
				continue
			}
			if _, _, err := lookup(*instrument.UnderlyingID); err != nil {
				return instruments, err
			}
		}
		return instruments, nil
	}
}

func positionID(position kite.Position) models.InstrumentID {
	return models.InstrumentID(position.Exchange + ":" + position.Tradingsymbol)
}

func holdingID(holding kite.Holding) models.InstrumentID {
	return models.InstrumentID(holding.Exchange + ":" + holding.Tradingsymbol)
}

// displayName is the catalog's display name for id, or fallback.
func displayName(instruments map[models.InstrumentID]models.Instrument, id models.InstrumentID, fallback string) string {
	if instrument, ok := instruments[id]; ok && strings.TrimSpace(instrument.DisplayName) != "" {
		return instrument.DisplayName
	}
	return fallback
}

// positionGroup is the positions on one underlying.
type positionGroup struct {
	underlying models.InstrumentID
	positions  []kite.Position
	pnl        float64
}

// groupPositions groups positions by underlying, ordered by underlying, with
// positions keeping Kite's order within a group.
func groupPositions(positions []kite.Position, instruments map[models.InstrumentID]models.Instrument) []positionGroup {
	var groups []positionGroup
	index := make(map[models.InstrumentID]int)
	for _, position := range positions {
		key := positionID(position)
		if instrument, ok := instruments[key]; ok && instrument.UnderlyingID != nil {
			key = *instrument.UnderlyingID
		}
		at, ok := index[key]
		if !ok {
			at = len(groups)
			index[key] = at
			groups = append(groups, positionGroup{underlying: key})
		}
		groups[at].positions = append(groups[at].positions, position)
		groups[at].pnl += position.PnL
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].underlying < groups[j].underlying })
	return groups
}

func readPortfolio(client broker.Account) portfolio {
	var result portfolio
	positions, err := client.GetPositions()
	result.positions, result.positionsErr = positions.Net, err
	result.holdings, result.holdingsErr = client.GetHoldings()
	result.funds, result.fundsErr = client.GetMFHoldings()
	result.orders, result.ordersErr = client.GetOrders()
	result.margins, result.marginsErr = client.GetUserMargins()
	if result.ordersErr == nil {
		result.orderCharges, result.chargesErr = readOrderCharges(result.orders)
	}
	result.loadedAt = time.Now()
	return result
}

func failedPortfolio(err error) portfolio {
	return portfolio{positionsErr: err, holdingsErr: err, fundsErr: err, ordersErr: err, marginsErr: err}
}

// priceSource fetches last prices with the previous close. The user's
// *kite.Client implements it, batching and pacing large requests.
type priceSource interface {
	GetOHLC(instruments ...string) (kite.QuoteOHLC, error)
}

// priceIDs lists the EXCHANGE:TRADINGSYMBOL of every position, holding and
// position underlying.
func (data portfolio) priceIDs() []string {
	ids := make([]string, 0, len(data.positions)+len(data.holdings))
	for _, position := range data.positions {
		ids = append(ids, string(positionID(position)))
	}
	for _, holding := range data.holdings {
		ids = append(ids, string(holdingID(holding)))
	}
	for _, group := range groupPositions(data.positions, data.instruments) {
		if !slices.Contains(ids, string(group.underlying)) {
			ids = append(ids, string(group.underlying))
		}
	}
	return ids
}

// reprice fetches current prices and applies them. The LTP and P&L in Kite's
// positions and holdings responses lag the market, so the portfolio is
// priced from quotes instead.
func (data portfolio) reprice() portfolio {
	ids := data.priceIDs()
	if data.prices == nil || len(ids) == 0 {
		return data
	}
	quotes, err := data.prices.GetOHLC(ids...)
	data.pricesErr = err
	if err != nil {
		return data
	}
	return data.withPrices(quotes, time.Now())
}

// withPrices recomputes positions and holdings at the quoted prices, using
// Kite's formulas: P&L is (sell value - buy value) + net quantity x LTP x
// multiplier, and M2M the same over the M2M values.
func (data portfolio) withPrices(quotes kite.QuoteOHLC, at time.Time) portfolio {
	positions := make([]kite.Position, len(data.positions))
	for index, position := range data.positions {
		if quote, ok := quotes[string(positionID(position))]; ok && quote.LastPrice > 0 {
			multiplier := position.Multiplier
			if multiplier == 0 {
				multiplier = 1
			}
			open := float64(position.Quantity) * quote.LastPrice * multiplier
			position.LastPrice = quote.LastPrice
			position.PnL = position.SellValue - position.BuyValue + open
			position.M2M = position.SellM2MValue - position.BuyM2MValue + open
			position.Unrealised = position.PnL - position.Realised
		}
		positions[index] = position
	}
	holdings := make(kite.Holdings, len(data.holdings))
	for index, holding := range data.holdings {
		if quote, ok := quotes[string(holdingID(holding))]; ok && quote.LastPrice > 0 {
			holding.LastPrice = quote.LastPrice
			holding.PnL = (quote.LastPrice - holding.AveragePrice) * float64(holding.Quantity+holding.T1Quantity)
			if close := quote.OHLC.Close; close > 0 {
				holding.ClosePrice = close
				holding.DayChange = quote.LastPrice - close
				holding.DayChangePercentage = holding.DayChange / close * 100
			}
		}
		holdings[index] = holding
	}
	underlyings := kite.QuoteOHLC{}
	for _, group := range groupPositions(data.positions, data.instruments) {
		if quote, ok := quotes[string(group.underlying)]; ok && quote.LastPrice > 0 {
			underlyings[string(group.underlying)] = quote
		}
	}
	data.positions, data.holdings, data.underlyings = positions, holdings, underlyings
	data.pricedAt = at
	return data
}

// held finds the open position, else the holding, in instrument id.
func (data portfolio) held(id models.InstrumentID) any {
	for _, position := range data.positions {
		if positionID(position) == id && position.Quantity != 0 {
			return position
		}
	}
	for _, holding := range data.holdings {
		if holdingID(holding) == id && holding.Quantity+holding.T1Quantity > 0 {
			return holding
		}
	}
	return nil
}

func sumPositions(positions []kite.Position) float64 {
	var pnl float64
	for _, position := range positions {
		pnl += position.PnL
	}
	return pnl
}

func sumHoldings(holdings kite.Holdings) (invested, current float64) {
	for _, holding := range holdings {
		quantity := float64(holding.Quantity + holding.T1Quantity)
		invested += holding.AveragePrice * quantity
		current += holding.LastPrice * quantity
	}
	return invested, current
}

// sumDayChange is the holdings' change since the previous close.
func sumDayChange(holdings kite.Holdings) float64 {
	var change float64
	for _, holding := range holdings {
		change += holding.DayChange * float64(holding.Quantity+holding.T1Quantity)
	}
	return change
}

func sumFunds(funds kite.MFHoldings) (invested, current float64) {
	for _, fund := range funds {
		invested += fund.AveragePrice * fund.Quantity
		current += fund.LastPrice * fund.Quantity
	}
	return invested, current
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
