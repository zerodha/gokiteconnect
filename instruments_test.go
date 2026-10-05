package kiteconnect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

const catalogTestCSV = `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange
256265,1001,NIFTY 50,NIFTY 50,0,,0,0.05,1,EQ,INDICES,NSE
408065,1002,INFY,INFOSYS,0,,0,0.05,1,EQ,NSE,NSE
2001,2001,NIFTY26JUNFUT,NIFTY,0,2026-06-25,0,0.05,75,FUT,NFO-FUT,NFO
`

// fakeInstrumentRepository implements the write side of InstrumentRepository;
// read methods panic through the nil embedded interface if called.
type fakeInstrumentRepository struct {
	InstrumentRepository
	mu            sync.Mutex
	instruments   []models.Instrument
	lastRefreshed time.Time
	replaces      int
	lists         int
	closes        int
}

func (f *fakeInstrumentRepository) Replace(_ context.Context, instruments []models.Instrument, refreshedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instruments = instruments
	f.lastRefreshed = refreshedAt
	f.replaces++
	return nil
}

func (f *fakeInstrumentRepository) LastRefreshedAt(context.Context) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastRefreshed, nil
}

func (f *fakeInstrumentRepository) List(context.Context, []string) ([]models.Instrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return f.instruments, nil
}

func (f *fakeInstrumentRepository) Close() error {
	f.closes++
	return nil
}

type csvTransport struct{ body string }

func (t csvTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.body)),
	}, nil
}

func newCatalogTestClient(body string, repository InstrumentRepository) *Client {
	client := New("key")
	client.SetHTTPClient(&http.Client{Transport: csvTransport{body: body}})
	client.SetInstrumentRepository(repository)
	return client
}

func TestInstrumentCatalogRefreshWritesNormalizedSnapshot(t *testing.T) {
	t.Parallel()
	repository := &fakeInstrumentRepository{}
	client := newCatalogTestClient(catalogTestCSV, repository)

	if err := client.Instruments().Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if repository.replaces != 1 || len(repository.instruments) != 3 || repository.lastRefreshed.IsZero() {
		t.Fatalf("repository after refresh = %d replaces, %d instruments, refreshed %v", repository.replaces, len(repository.instruments), repository.lastRefreshed)
	}
	for _, instrument := range repository.instruments {
		if instrument.ID == "NSE:NIFTY 50" && instrument.FuturesCount != 1 {
			t.Fatalf("normalized index = %#v, want 1 future", instrument)
		}
	}
}

func TestInstrumentCatalogRefreshIfStale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := &fakeInstrumentRepository{lastRefreshed: time.Now()}
	catalog := newCatalogTestClient(catalogTestCSV, repository).Instruments()

	refreshed, err := catalog.RefreshIfStale(ctx)
	if err != nil || refreshed || repository.replaces != 0 {
		t.Fatalf("same-day RefreshIfStale() = %v, %v with %d replaces", refreshed, err, repository.replaces)
	}

	repository.lastRefreshed = time.Now().Add(-24 * time.Hour)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := catalog.RefreshIfStale(ctx); err != nil {
				t.Errorf("concurrent RefreshIfStale() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if repository.replaces != 1 {
		t.Fatalf("concurrent stale refreshes = %d replaces, want 1", repository.replaces)
	}
}

func TestInstrumentCatalogErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	unset := newCatalogTestClient(catalogTestCSV, nil).Instruments()
	if err := unset.Refresh(ctx); !errors.Is(err, ErrNoInstrumentRepository) {
		t.Fatalf("Refresh(no repository) error = %v, want ErrNoInstrumentRepository", err)
	}
	if _, err := unset.Get(ctx, "NSE:INFY"); !errors.Is(err, ErrNoInstrumentRepository) {
		t.Fatalf("Get(no repository) error = %v, want ErrNoInstrumentRepository", err)
	}
	if err := unset.Close(); err != nil {
		t.Fatalf("Close(no repository) error = %v", err)
	}

	repository := &fakeInstrumentRepository{}
	catalog := newCatalogTestClient(catalogTestCSV, repository).Instruments()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := catalog.Refresh(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh(cancelled) error = %v", err)
	}
	if _, err := catalog.GetByToken(ctx, 0); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("GetByToken(0) error = %v, want ErrInvalidInput", err)
	}
	if _, err := catalog.Search(ctx, "::--", 0); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("Search(punctuation) error = %v, want ErrInvalidInput", err)
	}

	header := strings.SplitN(catalogTestCSV, "\n", 2)[0] + "\n"
	empty := newCatalogTestClient(header, repository).Instruments()
	if err := empty.Refresh(ctx); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("Refresh(empty) error = %v, want ErrInvalidInput", err)
	}
	if repository.replaces != 0 {
		t.Fatalf("failed refreshes wrote %d snapshots, want 0", repository.replaces)
	}

	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := catalog.Close(); err != nil || repository.closes != 1 {
		t.Fatalf("second Close() = %v with %d repository closes, want 1", err, repository.closes)
	}
}

func TestNormalizeFilters(t *testing.T) {
	minStrike, maxStrike := 100.0, 200.0
	got, err := normalizeOptionsFilter(models.OptionsFilter{
		UnderlyingID:  "nse:infy",
		ExpiryNumbers: []int{1, 0, 1},
		ExpiryDates:   []time.Time{instrumentDate(2026, time.July, 30), instrumentDate(2026, time.June, 25)},
		Strikes:       &models.StrikeRange{Min: &minStrike, Max: &maxStrike},
		Types:         []models.OptionType{"pe", "CE", "CE"},
	})
	if err != nil {
		t.Fatalf("normalizeOptionsFilter() error = %v", err)
	}
	if got.UnderlyingID != "NSE:INFY" || !reflect.DeepEqual(got.ExpiryNumbers, []int{0, 1}) || !reflect.DeepEqual(got.Types, []models.OptionType{models.OptionTypeCall, models.OptionTypePut}) {
		t.Fatalf("normalized filter = %#v", got)
	}

	negative := -1.0
	for name, filter := range map[string]models.OptionsFilter{
		"missing underlying": {},
		"negative expiry":    {UnderlyingID: "NSE:INFY", ExpiryNumbers: []int{-1}},
		"unknown type":       {UnderlyingID: "NSE:INFY", Types: []models.OptionType{"XX"}},
		"negative strike":    {UnderlyingID: "NSE:INFY", Strikes: &models.StrikeRange{Min: &negative}},
		"reversed strikes":   {UnderlyingID: "NSE:INFY", Strikes: &models.StrikeRange{Min: &maxStrike, Max: &minStrike}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeOptionsFilter(filter); !errors.Is(err, models.ErrInvalidInput) {
				t.Fatalf("normalizeOptionsFilter() error = %v, want models.ErrInvalidInput", err)
			}
		})
	}
}

func TestInstrumentsNormalizeDerivesCatalogMetadata(t *testing.T) {
	nearExpiry := instrumentDate(2026, time.June, 25)
	farExpiry := instrumentDate(2026, time.July, 30)
	source := Instruments{
		rawInstrument(256265, "NSE", "NIFTY 50", "NIFTY 50", "INDICES", "EQ", time.Time{}, 0),
		rawInstrument(408065, "NSE", "INFY", "INFOSYS", "NSE", "EQ", time.Time{}, 0),
		rawInstrument(1004, "BSE", "SENSEX", "SENSEX", "INDICES", "EQ", time.Time{}, 0),
		rawInstrument(403209, "BSE", "BSE SENSEX SIXTY 65:35", "BSE SENSEX SIXTY 65:35", "INDICES", "EQ", time.Time{}, 0),
		rawInstrument(422409, "BSE", "BSE LGMD60:40ST DIV50", "BSE INDEX BSE LGMD60:40ST DIV50", "INDICES", "EQ", time.Time{}, 0),
		rawInstrument(2002, "NFO", "NIFTY26JULFUT", "NIFTY", "NFO-FUT", "FUT", farExpiry, 0),
		rawInstrument(2001, "NFO", "NIFTY26JUNFUT", "NIFTY", "NFO-FUT", "FUT", nearExpiry, 0),
		rawInstrument(3001, "NFO", "NIFTY26JUN20000CE", "NIFTY", "NFO-OPT", "CE", nearExpiry, 20000),
		rawInstrument(3002, "NFO", "NIFTY26JUN20000PE", "NIFTY", "NFO-OPT", "PE", nearExpiry, 20000),
		rawInstrument(4001, "BFO", "SENSEX26JUN80000CE", "SENSEX", "BFO-OPT", "CE", nearExpiry, 80000),
		rawInstrument(5001, "MCX", "CRUDEOIL26JUNFUT", "CRUDEOIL", "MCX-FUT", "FUT", nearExpiry, 0),
	}

	got, err := source.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(got) != len(source) {
		t.Fatalf("Normalize() returned %d rows, want %d", len(got), len(source))
	}

	byID := make(map[models.InstrumentID]models.Instrument, len(got))
	for _, instrument := range got {
		byID[instrument.ID] = instrument
	}

	index := byID["NSE:NIFTY 50"]
	if index.InstrumentType != "INDICES" || index.OptionsCount != 2 || index.FuturesCount != 2 {
		t.Fatalf("normalized index = %#v, want INDICES with 2 options and 2 futures", index)
	}

	nearFuture := byID["NFO:NIFTY26JUNFUT"]
	farFuture := byID["NFO:NIFTY26JULFUT"]
	if nearFuture.UnderlyingID == nil || *nearFuture.UnderlyingID != "NSE:NIFTY 50" || !nearFuture.UnderlyingIsListed {
		t.Fatalf("near future underlying = %#v, want listed NSE:NIFTY 50", nearFuture)
	}
	if nearFuture.ExpiryNumber == nil || *nearFuture.ExpiryNumber != 0 {
		t.Fatalf("near future expiry number = %v, want 0", nearFuture.ExpiryNumber)
	}
	if farFuture.ExpiryNumber == nil || *farFuture.ExpiryNumber != 1 {
		t.Fatalf("far future expiry number = %v, want 1", farFuture.ExpiryNumber)
	}

	call := byID["NFO:NIFTY26JUN20000CE"]
	if call.DisplayName != "NIFTY 25 Jun 2026 20000 Call" {
		t.Fatalf("call display name = %q", call.DisplayName)
	}
	for _, term := range []string{"CALL", "CE", "NSE:NIFTY 50", "3001"} {
		if !strings.Contains(call.SearchString, term) {
			t.Errorf("call search string %q does not contain %q", call.SearchString, term)
		}
	}

	bfo := byID["BFO:SENSEX26JUN80000CE"]
	if bfo.UnderlyingID == nil || *bfo.UnderlyingID != "BSE:SENSEX" || !bfo.UnderlyingIsListed {
		t.Fatalf("BFO underlying = %#v, want listed BSE:SENSEX", bfo.UnderlyingID)
	}
	mcx := byID["MCX:CRUDEOIL26JUNFUT"]
	if mcx.UnderlyingID == nil || *mcx.UnderlyingID != "MCX:CRUDEOIL" || mcx.UnderlyingIsListed {
		t.Fatalf("MCX underlying = %#v listed=%v", mcx.UnderlyingID, mcx.UnderlyingIsListed)
	}
	if byID["NSE:INFY"].Expiry != nil || byID["NSE:INFY"].Name == nil {
		t.Fatalf("cash instrument nullable fields = %#v", byID["NSE:INFY"])
	}
	if colonIndex := byID["BSE:BSE SENSEX SIXTY 65:35"]; colonIndex.InstrumentToken != 403209 || colonIndex.InstrumentType != "INDICES" {
		t.Fatalf("colon-bearing index = %#v", colonIndex)
	}
}

func TestInstrumentsNormalizeRejectsInvalidSnapshots(t *testing.T) {
	if _, err := Instruments(nil).Normalize(); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("empty snapshot error = %v, want models.ErrInvalidInput", err)
	}

	valid := rawInstrument(1, "NSE", "INFY", "INFOSYS", "NSE", "EQ", time.Time{}, 0)
	tests := map[string]Instruments{
		"duplicate ID":    {valid, rawInstrument(2, "NSE", "INFY", "INFOSYS", "NSE", "EQ", time.Time{}, 0)},
		"duplicate token": {valid, rawInstrument(1, "NSE", "TCS", "TCS", "NSE", "EQ", time.Time{}, 0)},
		"fractional lot":  {instrumentWithLotSize(valid, 1.5)},
		"missing expiry":  {rawInstrument(3, "NFO", "INFY26JUNFUT", "INFY", "NFO-FUT", "FUT", time.Time{}, 0)},
		"missing name":    {rawInstrument(4, "NFO", "INFY26JUNFUT", "", "NFO-FUT", "FUT", instrumentDate(2026, time.June, 25), 0)},
	}
	for name, snapshot := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := snapshot.Normalize(); !errors.Is(err, models.ErrInvalidInput) {
				t.Fatalf("Normalize() error = %v, want models.ErrInvalidInput", err)
			}
		})
	}
}
func rawInstrument(token int, exchange, symbol, name, segment, instrumentType string, expiry time.Time, strike float64) Instrument {
	return Instrument{
		InstrumentToken: token,
		ExchangeToken:   token + 10,
		Tradingsymbol:   symbol,
		Name:            name,
		Expiry:          models.Time{Time: expiry},
		StrikePrice:     strike,
		TickSize:        0.05,
		LotSize:         50,
		InstrumentType:  instrumentType,
		Segment:         segment,
		Exchange:        exchange,
	}
}

func instrumentWithLotSize(instrument Instrument, lotSize float64) Instrument {
	instrument.LotSize = lotSize
	return instrument
}

func instrumentDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, indiaLocation)
}

func TestInstrumentCatalogTokenIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("refresh builds index without reading repository", func(t *testing.T) {
		repository := &fakeInstrumentRepository{}
		catalog := newCatalogTestClient(catalogTestCSV, repository).Instruments()
		if err := catalog.Refresh(ctx); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
		index, err := catalog.TokenIndex(ctx)
		if err != nil {
			t.Fatalf("TokenIndex() error = %v", err)
		}
		if token, ok := index.Token("nse:infy"); !ok || token != 408065 {
			t.Fatalf("Token(nse:infy) = %d, %v", token, ok)
		}
		if id, ok := index.ID(256265); !ok || id != "NSE:NIFTY 50" {
			t.Fatalf("ID(256265) = %q, %v", id, ok)
		}
		if index.Len() != 3 || repository.lists != 0 {
			t.Fatalf("index has %d entries after %d repository lists, want 3 after 0", index.Len(), repository.lists)
		}
	})

	t.Run("fresh catalog loads index once", func(t *testing.T) {
		repository := &fakeInstrumentRepository{
			lastRefreshed: time.Now(),
			instruments:   []models.Instrument{{ID: "NSE:INFY", InstrumentToken: 408065}},
		}
		catalog := newCatalogTestClient(catalogTestCSV, repository).Instruments()
		if refreshed, err := catalog.RefreshIfStale(ctx); err != nil || refreshed {
			t.Fatalf("RefreshIfStale() = %v, %v; want loaded without refresh", refreshed, err)
		}
		first, _ := catalog.TokenIndex(ctx)
		second, _ := catalog.TokenIndex(ctx)
		if first != second || repository.lists != 1 {
			t.Fatalf("index reused = %v after %d repository lists, want reused after 1", first == second, repository.lists)
		}
		if token, ok := first.Token("NSE:INFY"); !ok || token != 408065 {
			t.Fatalf("Token(NSE:INFY) = %d, %v", token, ok)
		}

		if err := catalog.Refresh(ctx); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
		refreshed, _ := catalog.TokenIndex(ctx)
		if refreshed == first || refreshed.Len() != 3 || first.Len() != 1 {
			t.Fatalf("after refresh index len = %d (old snapshot %d), want new index of 3 and old unchanged", refreshed.Len(), first.Len())
		}
	})
}
