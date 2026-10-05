package kiteconnect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

// DefaultInstrumentSearchLimit is used when Search receives a non-positive limit.
const DefaultInstrumentSearchLimit = 50

// ErrNoInstrumentRepository is returned by catalog methods when no repository
// has been attached with SetInstrumentRepository.
var ErrNoInstrumentRepository = errors.New("kiteconnect: instrument repository is not set")

var indiaLocation = time.FixedZone("Asia/Kolkata", 5*60*60+30*60)

// InstrumentRepository stores and queries a normalized instrument catalog.
// Implementations must replace snapshots atomically: a failed Replace must
// leave the previous catalog and refresh time unchanged.
type InstrumentRepository interface {
	Replace(ctx context.Context, instruments []models.Instrument, refreshedAt time.Time) error
	LastRefreshedAt(ctx context.Context) (time.Time, error)
	List(ctx context.Context, exchanges []string) ([]models.Instrument, error)
	Get(ctx context.Context, id models.InstrumentID) (models.Instrument, error)
	GetByToken(ctx context.Context, token int64) (models.Instrument, error)
	Search(ctx context.Context, query string, limit int) ([]models.Instrument, error)
	ListFO(ctx context.Context, exchanges []string) ([]models.Instrument, error)
	ListUnderlyingIDs(ctx context.Context, exchanges []string) ([]models.InstrumentID, error)
	GetFutures(ctx context.Context, filter models.FuturesFilter) ([]models.Instrument, error)
	GetOptions(ctx context.Context, filter models.OptionsFilter) ([]models.Instrument, error)
	Close() error
}

// InstrumentCatalog validates instrument queries, delegates storage to an
// InstrumentRepository, and refreshes it from the Kite instrument master. It
// keeps an in-memory ID/token index that is rebuilt on every refresh.
type InstrumentCatalog struct {
	kite       *Client
	repository InstrumentRepository
	tokens     atomic.Pointer[InstrumentTokenIndex]
	// refreshMu serializes refreshes and token index loads.
	refreshMu sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// SetInstrumentRepository attaches an instrument repository to the Kite Connect
// instance. Ownership of repository transfers to the catalog, which closes it
// on InstrumentCatalog.Close. Passing nil detaches the catalog.
func (c *Client) SetInstrumentRepository(repository InstrumentRepository) {
	if repository == nil {
		c.instruments = nil
		return
	}
	c.instruments = &InstrumentCatalog{kite: c, repository: repository}
}

// Instruments returns the instrument catalog. Its methods return
// ErrNoInstrumentRepository until SetInstrumentRepository is called.
func (c *Client) Instruments() *InstrumentCatalog {
	return c.instruments
}

// Close closes the repository. It is safe to call more than once.
func (c *InstrumentCatalog) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() { c.closeErr = c.repository.Close() })
	return c.closeErr
}

// Refresh fetches the instrument master, normalizes it, and atomically
// replaces the catalog.
func (c *InstrumentCatalog) Refresh(ctx context.Context) error {
	if err := c.ready(ctx); err != nil {
		return err
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refreshLocked(ctx)
}

// RefreshIfStale refreshes the catalog if it has not been refreshed today in
// Asia/Kolkata, and otherwise loads the token index from the repository. The
// returned boolean reports whether a refresh was performed.
func (c *InstrumentCatalog) RefreshIfStale(ctx context.Context) (bool, error) {
	if err := c.ready(ctx); err != nil {
		return false, err
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	lastRefreshed, err := c.repository.LastRefreshedAt(ctx)
	if err != nil {
		return false, err
	}
	if !lastRefreshed.IsZero() && dateKey(lastRefreshed) == dateKey(time.Now()) {
		_, err := c.loadTokenIndexLocked(ctx)
		return false, err
	}
	if err := c.refreshLocked(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (c *InstrumentCatalog) refreshLocked(ctx context.Context) error {
	source, err := c.kite.GetInstruments()
	if err != nil {
		return fmt.Errorf("instruments: fetch master list: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := source.Normalize()
	if err != nil {
		return err
	}
	if err := c.repository.Replace(ctx, normalized, time.Now()); err != nil {
		return err
	}
	c.tokens.Store(newInstrumentTokenIndex(normalized))
	return nil
}

// loadTokenIndexLocked returns the cached token index, building it from the
// repository if this catalog has not loaded or refreshed yet.
func (c *InstrumentCatalog) loadTokenIndexLocked(ctx context.Context) (*InstrumentTokenIndex, error) {
	if index := c.tokens.Load(); index != nil {
		return index, nil
	}
	instruments, err := c.repository.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	index := newInstrumentTokenIndex(instruments)
	c.tokens.Store(index)
	return index, nil
}

// LastRefreshedAt returns the time of the last successful refresh, or the
// zero time if the catalog has never been populated.
func (c *InstrumentCatalog) LastRefreshedAt(ctx context.Context) (time.Time, error) {
	if err := c.ready(ctx); err != nil {
		return time.Time{}, err
	}
	return c.repository.LastRefreshedAt(ctx)
}

// Get returns one instrument by its EXCHANGE:TRADING_SYMBOL ID.
func (c *InstrumentCatalog) Get(ctx context.Context, id models.InstrumentID) (models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return models.Instrument{}, err
	}
	normalizedID, err := models.ParseInstrumentID(string(id))
	if err != nil {
		return models.Instrument{}, err
	}
	return c.repository.Get(ctx, normalizedID)
}

// GetByToken returns one instrument by its Zerodha instrument token.
func (c *InstrumentCatalog) GetByToken(ctx context.Context, token int64) (models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return models.Instrument{}, err
	}
	if token <= 0 {
		return models.Instrument{}, fmt.Errorf("%w: instrument token must be positive", models.ErrInvalidInput)
	}
	return c.repository.GetByToken(ctx, token)
}

// Search returns instruments ranked by exact, prefix, and tokenized matches.
func (c *InstrumentCatalog) Search(ctx context.Context, query string, limit int) ([]models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: search query is required", models.ErrInvalidInput)
	}
	if len(searchTerms(query)) == 0 {
		return nil, fmt.Errorf("%w: search query must contain a searchable term", models.ErrInvalidInput)
	}
	if limit <= 0 {
		limit = DefaultInstrumentSearchLimit
	}
	return c.repository.Search(ctx, query, limit)
}

// List returns all instruments, optionally restricted to exchanges.
func (c *InstrumentCatalog) List(ctx context.Context, exchanges ...string) ([]models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	return c.repository.List(ctx, sortedUniqueStrings(exchanges))
}

// ListFO returns listed underlyings that currently have futures or options.
func (c *InstrumentCatalog) ListFO(ctx context.Context, exchanges ...string) ([]models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	return c.repository.ListFO(ctx, sortedUniqueStrings(exchanges))
}

// ListUnderlyingIDs returns distinct derivative underlying IDs. Exchange
// filters apply to the derivative exchange, such as NFO, BFO, or MCX.
func (c *InstrumentCatalog) ListUnderlyingIDs(ctx context.Context, exchanges ...string) ([]models.InstrumentID, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	return c.repository.ListUnderlyingIDs(ctx, sortedUniqueStrings(exchanges))
}

// GetFutures returns futures matching filter.
func (c *InstrumentCatalog) GetFutures(ctx context.Context, filter models.FuturesFilter) ([]models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	normalized, err := normalizeFuturesFilter(filter)
	if err != nil {
		return nil, err
	}
	return c.repository.GetFutures(ctx, normalized)
}

// GetOptions returns options matching filter.
func (c *InstrumentCatalog) GetOptions(ctx context.Context, filter models.OptionsFilter) ([]models.Instrument, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	normalized, err := normalizeOptionsFilter(filter)
	if err != nil {
		return nil, err
	}
	return c.repository.GetOptions(ctx, normalized)
}

// TokenIndex returns the in-memory ID/token index. It is built on the first
// RefreshIfStale, Refresh, or TokenIndex call and replaced after each refresh;
// a returned index is an immutable snapshot that never changes.
func (c *InstrumentCatalog) TokenIndex(ctx context.Context) (*InstrumentTokenIndex, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	if index := c.tokens.Load(); index != nil {
		return index, nil
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.loadTokenIndexLocked(ctx)
}

// ready reports whether the catalog can serve a call with ctx.
func (c *InstrumentCatalog) ready(ctx context.Context) error {
	if c == nil {
		return ErrNoInstrumentRepository
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is required", models.ErrInvalidInput)
	}
	return ctx.Err()
}

// InstrumentTokenIndex is an immutable, concurrency-safe snapshot of
// ID/token mappings.
type InstrumentTokenIndex struct {
	tokensByID map[models.InstrumentID]int64
	idsByToken map[int64]models.InstrumentID
}

func newInstrumentTokenIndex(instruments []models.Instrument) *InstrumentTokenIndex {
	index := &InstrumentTokenIndex{
		tokensByID: make(map[models.InstrumentID]int64, len(instruments)),
		idsByToken: make(map[int64]models.InstrumentID, len(instruments)),
	}
	for _, instrument := range instruments {
		index.tokensByID[instrument.ID] = instrument.InstrumentToken
		index.idsByToken[instrument.InstrumentToken] = instrument.ID
	}
	return index
}

// Token returns the token mapped to id.
func (i *InstrumentTokenIndex) Token(id models.InstrumentID) (int64, bool) {
	if i == nil {
		return 0, false
	}
	normalizedID, err := models.ParseInstrumentID(string(id))
	if err != nil {
		return 0, false
	}
	token, ok := i.tokensByID[normalizedID]
	return token, ok
}

// ID returns the instrument ID mapped to token.
func (i *InstrumentTokenIndex) ID(token int64) (models.InstrumentID, bool) {
	if i == nil {
		return "", false
	}
	id, ok := i.idsByToken[token]
	return id, ok
}

// Len returns the number of mappings in the snapshot.
func (i *InstrumentTokenIndex) Len() int {
	if i == nil {
		return 0
	}
	return len(i.tokensByID)
}

func normalizeFuturesFilter(filter models.FuturesFilter) (models.FuturesFilter, error) {
	underlyingID, err := models.ParseInstrumentID(string(filter.UnderlyingID))
	if err != nil {
		return models.FuturesFilter{}, fmt.Errorf("%w: underlying ID: %v", models.ErrInvalidInput, err)
	}
	numbers, err := normalizeExpiryNumbers(filter.ExpiryNumbers)
	if err != nil {
		return models.FuturesFilter{}, err
	}
	return models.FuturesFilter{
		UnderlyingID:  underlyingID,
		ExpiryNumbers: numbers,
		ExpiryDates:   normalizeExpiryDates(filter.ExpiryDates),
	}, nil
}

func normalizeOptionsFilter(filter models.OptionsFilter) (models.OptionsFilter, error) {
	underlyingID, err := models.ParseInstrumentID(string(filter.UnderlyingID))
	if err != nil {
		return models.OptionsFilter{}, fmt.Errorf("%w: underlying ID: %v", models.ErrInvalidInput, err)
	}
	numbers, err := normalizeExpiryNumbers(filter.ExpiryNumbers)
	if err != nil {
		return models.OptionsFilter{}, err
	}

	var strikes *models.StrikeRange
	if filter.Strikes != nil {
		strikes = &models.StrikeRange{Min: filter.Strikes.Min, Max: filter.Strikes.Max}
		if strikes.Min != nil && (math.IsNaN(*strikes.Min) || math.IsInf(*strikes.Min, 0) || *strikes.Min < 0) {
			return models.OptionsFilter{}, fmt.Errorf("%w: minimum strike must be finite and non-negative", models.ErrInvalidInput)
		}
		if strikes.Max != nil && (math.IsNaN(*strikes.Max) || math.IsInf(*strikes.Max, 0) || *strikes.Max < 0) {
			return models.OptionsFilter{}, fmt.Errorf("%w: maximum strike must be finite and non-negative", models.ErrInvalidInput)
		}
		if strikes.Min != nil && strikes.Max != nil && *strikes.Min > *strikes.Max {
			return models.OptionsFilter{}, fmt.Errorf("%w: minimum strike exceeds maximum strike", models.ErrInvalidInput)
		}
	}

	typeSet := make(map[models.OptionType]struct{}, len(filter.Types))
	for _, optionType := range filter.Types {
		optionType = models.OptionType(strings.ToUpper(strings.TrimSpace(string(optionType))))
		if optionType != models.OptionTypeCall && optionType != models.OptionTypePut {
			return models.OptionsFilter{}, fmt.Errorf("%w: unsupported option type %q", models.ErrInvalidInput, optionType)
		}
		typeSet[optionType] = struct{}{}
	}
	types := make([]models.OptionType, 0, len(typeSet))
	for _, optionType := range []models.OptionType{models.OptionTypeCall, models.OptionTypePut} {
		if _, ok := typeSet[optionType]; ok {
			types = append(types, optionType)
		}
	}

	return models.OptionsFilter{
		UnderlyingID:  underlyingID,
		ExpiryNumbers: numbers,
		ExpiryDates:   normalizeExpiryDates(filter.ExpiryDates),
		Strikes:       strikes,
		Types:         types,
	}, nil
}

func normalizeExpiryNumbers(numbers []int) ([]int, error) {
	seen := make(map[int]struct{}, len(numbers))
	result := make([]int, 0, len(numbers))
	for _, number := range numbers {
		if number < 0 {
			return nil, fmt.Errorf("%w: expiry numbers cannot be negative", models.ErrInvalidInput)
		}
		if _, exists := seen[number]; exists {
			continue
		}
		seen[number] = struct{}{}
		result = append(result, number)
	}
	sort.Ints(result)
	return result, nil
}

func normalizeExpiryDates(dates []time.Time) []time.Time {
	seen := make(map[string]struct{}, len(dates))
	result := make([]time.Time, 0, len(dates))
	for _, date := range dates {
		normalized := normalizeDate(date)
		key := dateKey(normalized)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Before(result[j]) })
	return result
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func searchTerms(query string) []string {
	normalized := strings.NewReplacer(":", " ", "-", " ", "_", " ", "/", " ").Replace(strings.ToLower(query))
	seen := make(map[string]struct{})
	terms := make([]string, 0)
	for _, term := range strings.Fields(normalized) {
		if _, ok := seen[term]; !ok {
			seen[term] = struct{}{}
			terms = append(terms, term)
		}
		switch term {
		case "call":
			if _, ok := seen["ce"]; !ok {
				seen["ce"] = struct{}{}
				terms = append(terms, "ce")
			}
		case "put":
			if _, ok := seen["pe"]; !ok {
				seen["pe"] = struct{}{}
				terms = append(terms, "pe")
			}
		}
	}
	return terms
}

type contractCounts struct {
	options int
	futures int
}

// Normalize converts the raw instrument master into a validated catalog with
// derived underlyings, expiry numbers, contract counts, display names, and
// search strings. It rejects empty snapshots and duplicate IDs or tokens.
func (source Instruments) Normalize() ([]models.Instrument, error) {
	if len(source) == 0 {
		return nil, fmt.Errorf("%w: instrument snapshot is empty", models.ErrInvalidInput)
	}

	instruments := make([]models.Instrument, 0, len(source))
	ids := make(map[models.InstrumentID]struct{}, len(source))
	tokens := make(map[int64]struct{}, len(source))

	for rowNumber, sourceInstrument := range source {
		instrument, err := normalizeSourceInstrument(sourceInstrument)
		if err != nil {
			return nil, fmt.Errorf("instrument row %d: %w", rowNumber+1, err)
		}
		if _, exists := ids[instrument.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate instrument ID %s", models.ErrInvalidInput, instrument.ID)
		}
		if _, exists := tokens[instrument.InstrumentToken]; exists {
			return nil, fmt.Errorf("%w: duplicate instrument token %d", models.ErrInvalidInput, instrument.InstrumentToken)
		}
		ids[instrument.ID] = struct{}{}
		tokens[instrument.InstrumentToken] = struct{}{}
		instruments = append(instruments, instrument)
	}

	expiries := make(map[models.InstrumentID]map[string]time.Time)
	counts := make(map[models.InstrumentID]contractCounts)
	for i := range instruments {
		instrument := &instruments[i]
		if instrument.UnderlyingID == nil {
			continue
		}
		instrument.UnderlyingIsListed = containsID(ids, *instrument.UnderlyingID)
		count := counts[*instrument.UnderlyingID]
		switch instrument.InstrumentType {
		case string(models.OptionTypeCall), string(models.OptionTypePut):
			count.options++
		case "FUT":
			count.futures++
		}
		counts[*instrument.UnderlyingID] = count

		if instrument.Expiry != nil {
			if expiries[*instrument.UnderlyingID] == nil {
				expiries[*instrument.UnderlyingID] = make(map[string]time.Time)
			}
			expiries[*instrument.UnderlyingID][dateKey(*instrument.Expiry)] = *instrument.Expiry
		}
	}

	expiryNumbers := make(map[models.InstrumentID]map[string]int, len(expiries))
	for underlyingID, datesByKey := range expiries {
		dates := make([]time.Time, 0, len(datesByKey))
		for _, date := range datesByKey {
			dates = append(dates, date)
		}
		sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
		expiryNumbers[underlyingID] = make(map[string]int, len(dates))
		for number, date := range dates {
			expiryNumbers[underlyingID][dateKey(date)] = number
		}
	}

	for i := range instruments {
		instrument := &instruments[i]
		countKey := instrument.ID
		if instrument.UnderlyingID != nil {
			countKey = *instrument.UnderlyingID
		}
		instrument.OptionsCount = counts[countKey].options
		instrument.FuturesCount = counts[countKey].futures
		if instrument.UnderlyingID != nil && instrument.Expiry != nil {
			number := expiryNumbers[*instrument.UnderlyingID][dateKey(*instrument.Expiry)]
			instrument.ExpiryNumber = intPointer(number)
		}
		instrument.DisplayName = instrumentDisplayName(*instrument)
		instrument.SearchString = instrumentSearchString(*instrument)
	}

	return instruments, nil
}

func normalizeSourceInstrument(source Instrument) (models.Instrument, error) {
	exchange := strings.ToUpper(strings.TrimSpace(source.Exchange))
	tradingSymbol := strings.ToUpper(strings.TrimSpace(source.Tradingsymbol))
	id, err := models.ParseInstrumentID(exchange + ":" + tradingSymbol)
	if err != nil {
		return models.Instrument{}, err
	}
	if source.InstrumentToken <= 0 {
		return models.Instrument{}, fmt.Errorf("%w: instrument token must be positive", models.ErrInvalidInput)
	}
	if source.ExchangeToken < 0 {
		return models.Instrument{}, fmt.Errorf("%w: exchange token cannot be negative", models.ErrInvalidInput)
	}
	if source.StrikePrice < 0 || source.TickSize < 0 {
		return models.Instrument{}, fmt.Errorf("%w: strike and tick size cannot be negative", models.ErrInvalidInput)
	}
	if source.LotSize < 0 || source.LotSize != math.Trunc(source.LotSize) || source.LotSize > math.MaxInt {
		return models.Instrument{}, fmt.Errorf("%w: lot size %v is not a non-negative integer", models.ErrInvalidInput, source.LotSize)
	}

	segment := strings.ToUpper(strings.TrimSpace(source.Segment))
	instrumentType := strings.ToUpper(strings.TrimSpace(source.InstrumentType))
	if segment == "INDICES" {
		instrumentType = "INDICES"
	}
	if instrumentType == "" {
		return models.Instrument{}, fmt.Errorf("%w: instrument type is required", models.ErrInvalidInput)
	}

	name := stringPointer(strings.TrimSpace(source.Name))
	isFO := instrumentType == string(models.OptionTypeCall) || instrumentType == string(models.OptionTypePut) || instrumentType == "FUT"
	var expiry *time.Time
	if !source.Expiry.IsZero() {
		expiry = timePointer(normalizeDate(source.Expiry.Time))
	}
	if isFO && expiry == nil {
		return models.Instrument{}, fmt.Errorf("%w: derivative %s has no expiry", models.ErrInvalidInput, id)
	}

	var underlyingID *models.InstrumentID
	if isFO {
		if name == nil {
			return models.Instrument{}, fmt.Errorf("%w: derivative %s has no underlying name", models.ErrInvalidInput, id)
		}
		underlying, err := deriveUnderlyingID(exchange, *name)
		if err != nil {
			return models.Instrument{}, err
		}
		underlyingID = &underlying
	}

	return models.Instrument{
		ID:              id,
		Exchange:        exchange,
		TradingSymbol:   tradingSymbol,
		InstrumentToken: int64(source.InstrumentToken),
		ExchangeToken:   strconv.Itoa(source.ExchangeToken),
		Name:            name,
		Expiry:          expiry,
		Strike:          source.StrikePrice,
		TickSize:        source.TickSize,
		LotSize:         int(source.LotSize),
		InstrumentType:  instrumentType,
		Segment:         segment,
		IsFO:            isFO,
		UnderlyingID:    underlyingID,
	}, nil
}

func deriveUnderlyingID(derivativesExchange, name string) (models.InstrumentID, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return "", fmt.Errorf("%w: underlying name is required", models.ErrInvalidInput)
	}

	var value string
	switch name {
	case "NIFTY":
		value = "NSE:NIFTY 50"
	case "BANKNIFTY":
		value = "NSE:NIFTY BANK"
	case "MIDCPNIFTY":
		value = "NSE:NIFTY MID SELECT"
	case "FINNIFTY":
		value = "NSE:NIFTY FIN SERVICE"
	default:
		switch derivativesExchange {
		case "NFO":
			value = "NSE:" + name
		case "BFO":
			value = "BSE:" + name
		default:
			value = derivativesExchange + ":" + name
		}
	}
	return models.ParseInstrumentID(value)
}

func instrumentDisplayName(instrument models.Instrument) string {
	base := instrument.TradingSymbol
	if instrument.Name != nil {
		base = *instrument.Name
	}
	if instrument.Expiry == nil {
		return instrument.TradingSymbol
	}

	expiry := instrument.Expiry.In(indiaLocation).Format("02 Jan 2006")
	switch instrument.InstrumentType {
	case "FUT":
		return strings.Join([]string{base, expiry, "Futures"}, " ")
	case string(models.OptionTypeCall):
		return strings.Join([]string{base, expiry, formatStrike(instrument.Strike), "Call"}, " ")
	case string(models.OptionTypePut):
		return strings.Join([]string{base, expiry, formatStrike(instrument.Strike), "Put"}, " ")
	default:
		return instrument.TradingSymbol
	}
}

func instrumentSearchString(instrument models.Instrument) string {
	parts := []string{
		instrument.DisplayName,
		instrument.Exchange,
		strconv.FormatInt(instrument.InstrumentToken, 10),
		string(instrument.ID),
		instrument.TradingSymbol,
		instrument.InstrumentType,
		instrument.Segment,
	}
	if instrument.Name != nil {
		parts = append(parts, *instrument.Name)
	}
	if instrument.UnderlyingID != nil {
		parts = append(parts, string(*instrument.UnderlyingID))
	}
	switch instrument.InstrumentType {
	case string(models.OptionTypeCall):
		parts = append(parts, "CE", "CALL")
	case string(models.OptionTypePut):
		parts = append(parts, "PE", "PUT")
	case "FUT":
		parts = append(parts, "FUT", "FUTURES")
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func normalizeDate(value time.Time) time.Time {
	inIndia := value.In(indiaLocation)
	return time.Date(inIndia.Year(), inIndia.Month(), inIndia.Day(), 0, 0, 0, 0, indiaLocation)
}

func dateKey(value time.Time) string {
	return value.In(indiaLocation).Format(time.DateOnly)
}

func formatStrike(strike float64) string {
	return strconv.FormatFloat(strike, 'f', -1, 64)
}

func containsID(ids map[models.InstrumentID]struct{}, id models.InstrumentID) bool {
	_, ok := ids[id]
	return ok
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func timePointer(value time.Time) *time.Time { return &value }

func intPointer(value int) *int { return &value }
