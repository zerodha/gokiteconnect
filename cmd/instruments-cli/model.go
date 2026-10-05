package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

const (
	searchDelay = 200 * time.Millisecond
	searchLimit = 100
)

type catalog interface {
	Close() error
	Refresh(context.Context) error
	LastRefreshedAt(context.Context) (time.Time, error)
	ListFO(context.Context, ...string) ([]models.Instrument, error)
	Search(context.Context, string, int) ([]models.Instrument, error)
	GetFutures(context.Context, models.FuturesFilter) ([]models.Instrument, error)
	GetOptions(context.Context, models.OptionsFilter) ([]models.Instrument, error)
}

type catalogFactory func(context.Context) (catalog, error)

type initialCatalog struct {
	catalog       catalog
	underlyings   []models.Instrument
	lastRefreshed time.Time
}

func loadInitialCatalog(ctx context.Context, factory catalogFactory) (initialCatalog, error) {
	catalog, err := factory(ctx)
	if err != nil {
		return initialCatalog{}, err
	}
	lastRefreshed, err := catalog.LastRefreshedAt(ctx)
	if err != nil {
		_ = catalog.Close()
		return initialCatalog{}, err
	}
	underlyings, err := catalog.ListFO(ctx)
	if err != nil {
		_ = catalog.Close()
		return initialCatalog{}, err
	}
	return initialCatalog{catalog: catalog, underlyings: underlyings, lastRefreshed: lastRefreshed}, nil
}

type dashboardSnapshot struct {
	query      string
	results    []models.Instrument
	selectedID models.InstrumentID
}

type expiryChoice struct {
	number int
	date   time.Time
}

func collectExpiries(futures, options []models.Instrument) []expiryChoice {
	byNumber := make(map[int]time.Time)
	for _, contract := range append(append([]models.Instrument(nil), futures...), options...) {
		if contract.ExpiryNumber == nil || contract.Expiry == nil {
			continue
		}
		if existing, ok := byNumber[*contract.ExpiryNumber]; !ok || contract.Expiry.Before(existing) {
			byNumber[*contract.ExpiryNumber] = *contract.Expiry
		}
	}
	choices := make([]expiryChoice, 0, len(byNumber))
	for number, date := range byNumber {
		choices = append(choices, expiryChoice{number: number, date: date})
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].number != choices[j].number {
			return choices[i].number < choices[j].number
		}
		return choices[i].date.Before(choices[j].date)
	})
	return choices
}

type optionSide int

const (
	optionSideBoth optionSide = iota
	optionSideCalls
	optionSidePuts
)

func (s optionSide) String() string {
	switch s {
	case optionSideCalls:
		return "Calls"
	case optionSidePuts:
		return "Puts"
	default:
		return "Calls + puts"
	}
}

type chainFilter struct {
	expiryNumber int
	minStrike    *float64
	maxStrike    *float64
	side         optionSide
}

type chainRow struct {
	strike float64
	call   *models.Instrument
	put    *models.Instrument
}

type chainData struct {
	underlying models.InstrumentID
	futures    []models.Instrument
	options    []models.Instrument
	expiries   []expiryChoice
	filter     chainFilter
}

func filterFutures(values []models.Instrument, filter chainFilter) []models.Instrument {
	result := make([]models.Instrument, 0)
	for _, value := range values {
		if value.ExpiryNumber != nil && *value.ExpiryNumber == filter.expiryNumber {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if expiryLess(result[i], result[j]) {
			return true
		}
		if expiryLess(result[j], result[i]) {
			return false
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func buildChainRows(options []models.Instrument, filter chainFilter) []chainRow {
	rowsByStrike := make(map[float64]*chainRow)
	for _, option := range options {
		if option.ExpiryNumber == nil || *option.ExpiryNumber != filter.expiryNumber {
			continue
		}
		if filter.minStrike != nil && option.Strike < *filter.minStrike {
			continue
		}
		if filter.maxStrike != nil && option.Strike > *filter.maxStrike {
			continue
		}
		if filter.side == optionSideCalls && option.InstrumentType != string(models.OptionTypeCall) {
			continue
		}
		if filter.side == optionSidePuts && option.InstrumentType != string(models.OptionTypePut) {
			continue
		}
		row := rowsByStrike[option.Strike]
		if row == nil {
			row = &chainRow{strike: option.Strike}
			rowsByStrike[option.Strike] = row
		}
		contract := option
		switch option.InstrumentType {
		case string(models.OptionTypeCall):
			if row.call == nil || contract.ID < row.call.ID {
				row.call = &contract
			}
		case string(models.OptionTypePut):
			if row.put == nil || contract.ID < row.put.ID {
				row.put = &contract
			}
		}
	}
	strikes := make([]float64, 0, len(rowsByStrike))
	for strike := range rowsByStrike {
		strikes = append(strikes, strike)
	}
	sort.Float64s(strikes)
	rows := make([]chainRow, 0, len(strikes))
	for _, strike := range strikes {
		rows = append(rows, *rowsByStrike[strike])
	}
	return rows
}

func expiryLess(left, right models.Instrument) bool {
	if left.Expiry == nil {
		return right.Expiry != nil
	}
	if right.Expiry == nil {
		return false
	}
	return left.Expiry.Before(*right.Expiry)
}

func drilldownUnderlying(instrument models.Instrument) (models.InstrumentID, bool) {
	if instrument.UnderlyingID != nil {
		return *instrument.UnderlyingID, true
	}
	if instrument.OptionsCount > 0 || instrument.FuturesCount > 0 {
		return instrument.ID, true
	}
	return "", false
}

func parseStrikeRange(minimum, maximum string) (*float64, *float64, error) {
	min, err := parseOptionalStrike("minimum", minimum)
	if err != nil {
		return nil, nil, err
	}
	max, err := parseOptionalStrike("maximum", maximum)
	if err != nil {
		return nil, nil, err
	}
	if min != nil && max != nil && *min > *max {
		return nil, nil, fmt.Errorf("minimum strike exceeds maximum strike")
	}
	return min, max, nil
}

func parseOptionalStrike(label, value string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return nil, fmt.Errorf("%s strike must be a non-negative number", label)
	}
	return &parsed, nil
}

func formatExpiry(expiry *time.Time) string {
	if expiry == nil {
		return "—"
	}
	return expiry.Format("02 Jan 2006")
}

func formatStrike(strike float64) string {
	return strconv.FormatFloat(strike, 'f', -1, 64)
}

func pointerText[T ~string](value *T) string {
	if value == nil || *value == "" {
		return "—"
	}
	return string(*value)
}
