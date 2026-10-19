// Package marketdata keeps the local candles database: it syncs candles
// from Kite, reconciles the stored instruments with an instrument list, and
// reads, writes and deletes candles. It has no server; a command-line tool
// drives it.
package marketdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
)

// DefaultRecentLimit is how many candles RecentCandles reads when asked
// for none.
const DefaultRecentLimit = 15

// Store is the candles database. *repository.CandlesDuckDBRepository
// implements it.
type Store interface {
	GetCandles(ctx context.Context, params models.GetCandlesParams) ([]candles.Candle, error)
	GetRecentCandles(ctx context.Context, instrumentID models.InstrumentID, interval models.CandleInterval, limit int) ([]candles.Candle, error)
	GetRecentCandlesBatch(ctx context.Context, instrumentIDs []models.InstrumentID, interval models.CandleInterval, limit int) ([]candles.Candle, error)
	InsertHistoricalCandles(ctx context.Context, rows []candles.Candle) error
	UpsertHistoricalCandles(ctx context.Context, rows []candles.Candle) error
	DeleteCandlesByInstrumentAndInterval(ctx context.Context, instrumentID models.InstrumentID, interval models.CandleInterval) error
	DropInstrumentsByID(ctx context.Context, ids []models.InstrumentID) error
	FlushLiveCandles(ctx context.Context) error
	GetAvailableInstruments(ctx context.Context) (map[int64]models.InstrumentID, error)
	GetDataAvailability(ctx context.Context, instrumentIDs []models.InstrumentID) ([]models.CandleAvailability, error)
}

// Source fetches historical candles, handing them to consume a batch at a
// time so a long backfill is stored as it arrives. Every candle names its
// instrument, token and interval.
type Source interface {
	StreamCandles(ctx context.Context, instrumentID models.InstrumentID, interval models.CandleInterval, from, to time.Time, consume func([]candles.Candle) error) error
}

// Lists reads an instrument list from the instruments database.
type Lists func(ctx context.Context, listID string) (models.List, error)

// ErrInvalid matches every refused request with errors.Is, so a caller can
// tell a usage mistake from a failure.
var ErrInvalid = errors.New("invalid request")

type inputError struct{ message string }

func (e inputError) Error() string        { return e.message }
func (e inputError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error {
	return inputError{message: fmt.Sprintf(format, args...)}
}

// Service syncs and keeps candles. Source and Lists may be nil when only
// reads and writes are needed.
type Service struct {
	store  Store
	source Source
	lists  Lists
	now    func() time.Time
	log    *slog.Logger
}

// New is a Service over store. A nil log discards logging.
func New(store Store, source Source, lists Lists, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, source: source, lists: lists, now: time.Now, log: log}
}

// SyncRequest is a range of one instrument's candles to fetch. A zero From
// resumes after the last stored candle, or starts at the interval's
// earliest date when none is stored; a zero To is the last settled close.
type SyncRequest struct {
	InstrumentID models.InstrumentID
	Interval     models.CandleInterval
	From         time.Time
	To           time.Time
	// Progress, when set, is called after each stored batch.
	Progress func(SyncProgress)
}

// SyncProgress is how far a sync has got: Candles stored so far, through
// the last stored candle's time, of the range From to To.
type SyncProgress struct {
	InstrumentID models.InstrumentID
	Interval     models.CandleInterval
	From, To     time.Time
	Through      time.Time
	Candles      int
}

// Fraction is the share of the range synced, from 0 to 1.
func (p SyncProgress) Fraction() float64 {
	total := p.To.Sub(p.From)
	if total <= 0 || p.Through.IsZero() {
		return 0
	}
	return min(max(float64(p.Through.Sub(p.From))/float64(total), 0), 1)
}

// SyncResult is what a sync fetched.
type SyncResult struct {
	InstrumentID models.InstrumentID   `json:"instrument_id"`
	Interval     models.CandleInterval `json:"interval"`
	From         time.Time             `json:"from"`
	To           time.Time             `json:"to"`
	Candles      int                   `json:"candles"`
	Skipped      bool                  `json:"skipped,omitempty"`
}

// Sync fetches a range of candles from Kite and stores them, replacing any
// already stored, so syncing a range again is harmless.
func (s *Service) Sync(ctx context.Context, request SyncRequest) (SyncResult, error) {
	id, err := parseID(request.InstrumentID)
	if err != nil {
		return SyncResult{}, err
	}
	if !request.Interval.IsValid() {
		return SyncResult{}, invalid("unsupported interval %q", request.Interval)
	}
	from, to := request.From, request.To
	if from.IsZero() {
		resume, err := s.resumeFrom(ctx, id, request.Interval)
		if err != nil {
			return SyncResult{}, err
		}
		from = resume
	}
	if to.IsZero() {
		to = settledMarketClose(s.now())
	}
	from, to = from.In(models.ExchangeLocation), to.In(models.ExchangeLocation)
	result := SyncResult{InstrumentID: id, Interval: request.Interval, From: from, To: to}
	if !to.After(from) {
		result.Skipped = true
		return result, nil
	}
	if s.source == nil {
		return result, errors.New("no candle source")
	}

	s.log.Info("syncing candles", "instrument", id, "interval", request.Interval, "from", from, "to", to)
	err = s.source.StreamCandles(ctx, id, request.Interval, from, to, func(batch []candles.Candle) error {
		if err := s.store.UpsertHistoricalCandles(ctx, normalizeCandles(batch)); err != nil {
			return err
		}
		result.Candles += len(batch)
		if request.Progress != nil && len(batch) > 0 {
			request.Progress(SyncProgress{
				InstrumentID: id, Interval: request.Interval, From: from, To: to,
				Through: batch[len(batch)-1].Timestamp.In(models.ExchangeLocation), Candles: result.Candles,
			})
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("sync %s %s: %w", id, request.Interval, err)
	}
	s.log.Info("synced candles", "instrument", id, "interval", request.Interval, "candles", result.Candles)
	return result, nil
}

// resumeFrom is where a sync of an instrument's interval starts: a step
// after its last stored candle, or the interval's earliest date.
func (s *Service) resumeFrom(ctx context.Context, id models.InstrumentID, interval models.CandleInterval) (time.Time, error) {
	availability, err := s.store.GetDataAvailability(ctx, []models.InstrumentID{id})
	if err != nil {
		return time.Time{}, err
	}
	for _, row := range availability {
		if row.InstrumentID == id && row.Interval == interval && !row.HistEnd.IsZero() {
			step, _ := interval.Duration()
			return models.ExchangeClockTime(row.HistEnd).Add(step), nil
		}
	}
	return DefaultStartDate(interval), nil
}

// StandardIntervals are the intervals SyncStandard keeps: 1m candles, from
// which every intraday interval is aggregated, and 1d candles.
var StandardIntervals = []models.CandleInterval{models.CandleInterval1m, models.CandleInterval1d}

// SyncStandard caches an instrument's standard candles: 1m from 2015 and 1d
// from the earliest Kite has, each resuming after the last stored candle.
// It stops at the first interval that fails, returning what was synced.
func (s *Service) SyncStandard(ctx context.Context, id models.InstrumentID) ([]SyncResult, error) {
	return s.SyncStandardProgress(ctx, id, nil)
}

// SyncStandardProgress is SyncStandard, calling progress, when set, after
// each stored batch.
func (s *Service) SyncStandardProgress(ctx context.Context, id models.InstrumentID, progress func(SyncProgress)) ([]SyncResult, error) {
	results := make([]SyncResult, 0, len(StandardIntervals))
	for _, interval := range StandardIntervals {
		result, err := s.Sync(ctx, SyncRequest{InstrumentID: id, Interval: interval, Progress: progress})
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// SyncAllResult is what syncing every stored series did.
type SyncAllResult struct {
	Synced []SyncResult `json:"synced"`
	Failed []SyncFailed `json:"failed,omitempty"`
}

// SyncFailed is a series SyncAll couldn't sync.
type SyncFailed struct {
	InstrumentID models.InstrumentID   `json:"instrument_id"`
	Interval     models.CandleInterval `json:"interval"`
	Error        string                `json:"error"`
}

// SyncAll brings every stored instrument and interval up to the last
// settled close; an empty interval means every interval. A series that
// fails doesn't stop the others.
func (s *Service) SyncAll(ctx context.Context, interval models.CandleInterval) (SyncAllResult, error) {
	if interval != "" && !interval.IsValid() {
		return SyncAllResult{}, invalid("unsupported interval %q", interval)
	}
	availability, err := s.store.GetDataAvailability(ctx, nil)
	if err != nil {
		return SyncAllResult{}, err
	}
	var result SyncAllResult
	for _, row := range availability {
		if interval != "" && row.Interval != interval {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		synced, err := s.Sync(ctx, SyncRequest{InstrumentID: row.InstrumentID, Interval: row.Interval})
		if err != nil {
			s.log.Error("sync failed", "instrument", row.InstrumentID, "interval", row.Interval, "error", err)
			result.Failed = append(result.Failed, SyncFailed{InstrumentID: row.InstrumentID, Interval: row.Interval, Error: err.Error()})
			continue
		}
		result.Synced = append(result.Synced, synced)
	}
	return result, nil
}

// ReconcileResult compares a list with the stored instruments.
type ReconcileResult struct {
	ListID           string                `json:"list_id"`
	ListName         string                `json:"list_name"`
	ListInstruments  int                   `json:"list_instruments"`
	LocalInstruments int                   `json:"local_instruments"`
	Added            []models.InstrumentID `json:"added"`
	Removed          []models.InstrumentID `json:"removed"`
	Synced           []SyncResult          `json:"synced,omitempty"`
}

// PreviewReconcile says which instruments reconciling with a list would
// add and remove, without changing anything.
func (s *Service) PreviewReconcile(ctx context.Context, listID string) (ReconcileResult, error) {
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return ReconcileResult{}, invalid("a list is required")
	}
	if s.lists == nil {
		return ReconcileResult{}, errors.New("no instrument lists")
	}
	list, err := s.lists(ctx, listID)
	if err != nil {
		return ReconcileResult{}, err
	}
	stored, err := s.store.GetAvailableInstruments(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	target := idSet(list.InstrumentIDs())
	local := make(map[models.InstrumentID]struct{}, len(stored))
	for _, id := range stored {
		if id = normalizeID(id); id != "" {
			local[id] = struct{}{}
		}
	}
	return ReconcileResult{
		ListID:           list.ID,
		ListName:         list.Name,
		ListInstruments:  len(target),
		LocalInstruments: len(local),
		Added:            difference(target, local),
		Removed:          difference(local, target),
	}, nil
}

// Reconcile makes the stored instruments the list's: it drops every candle
// of instruments not in the list and backfills the list's new instruments
// at interval.
func (s *Service) Reconcile(ctx context.Context, listID string, interval models.CandleInterval) (ReconcileResult, error) {
	if !interval.IsValid() {
		return ReconcileResult{}, invalid("unsupported interval %q", interval)
	}
	result, err := s.PreviewReconcile(ctx, listID)
	if err != nil {
		return result, err
	}
	s.log.Info("reconciling candles with list", "list", result.ListName, "add", len(result.Added), "remove", len(result.Removed))
	if len(result.Removed) > 0 {
		if err := s.store.DropInstrumentsByID(ctx, result.Removed); err != nil {
			return result, err
		}
	}
	for _, id := range result.Added {
		synced, err := s.Sync(ctx, SyncRequest{InstrumentID: id, Interval: interval, From: DefaultStartDate(interval)})
		if err != nil {
			return result, err
		}
		result.Synced = append(result.Synced, synced)
	}
	return result, nil
}

// Availability is the range of stored candles of each instrument and
// interval; no instruments means all of them.
func (s *Service) Availability(ctx context.Context, ids []models.InstrumentID) ([]models.CandleAvailability, error) {
	ids, err := parseIDs(ids)
	if err != nil {
		return nil, err
	}
	return s.store.GetDataAvailability(ctx, ids)
}

// CandleRange is a range of candles to read.
type CandleRange struct {
	InstrumentIDs []models.InstrumentID
	Interval      models.CandleInterval
	From, To      time.Time
	// Warmup is how many candles before From to include per instrument.
	Warmup int
}

// Candles reads candles in [From, To) of the instruments, each preceded by
// up to Warmup candles before From.
func (s *Service) Candles(ctx context.Context, query CandleRange) ([]candles.Candle, error) {
	ids, err := parseIDs(query.InstrumentIDs)
	if err != nil {
		return nil, err
	}
	switch {
	case len(ids) == 0:
		return nil, invalid("at least one instrument is required")
	case !query.Interval.IsValid():
		return nil, invalid("unsupported interval %q", query.Interval)
	case query.From.IsZero() || query.To.IsZero() || !query.To.After(query.From):
		return nil, invalid("to must be after from")
	case query.Warmup < 0:
		return nil, invalid("warmup can't be negative")
	}
	return s.store.GetCandles(ctx, models.GetCandlesParams{
		InstrumentIDs: ids,
		Intervals:     []models.CandleInterval{query.Interval},
		Start:         query.From,
		End:           query.To,
		Warmup:        query.Warmup,
	})
}

// RecentCandles reads the last limit candles of each instrument, oldest
// first; a zero limit is DefaultRecentLimit.
func (s *Service) RecentCandles(ctx context.Context, ids []models.InstrumentID, interval models.CandleInterval, limit int) ([]candles.Candle, error) {
	ids, err := parseIDs(ids)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = DefaultRecentLimit
	}
	switch {
	case len(ids) == 0:
		return nil, invalid("at least one instrument is required")
	case !interval.IsValid():
		return nil, invalid("unsupported interval %q", interval)
	case limit < 0:
		return nil, invalid("limit can't be negative")
	}
	if len(ids) == 1 {
		return s.store.GetRecentCandles(ctx, ids[0], interval, limit)
	}
	return s.store.GetRecentCandlesBatch(ctx, ids, interval, limit)
}

// PutCandles stores candles given directly. With replace, a candle already
// stored for the same instrument, interval and time is replaced; without
// it, that is an error and nothing is stored.
func (s *Service) PutCandles(ctx context.Context, rows []candles.Candle, replace bool) (int, error) {
	if len(rows) == 0 {
		return 0, invalid("no candles")
	}
	rows = slices.Clone(rows)
	for index, candle := range rows {
		id, err := parseID(candle.InstrumentID)
		if err != nil {
			return 0, fmt.Errorf("candle %d: %w", index, err)
		}
		rows[index].InstrumentID = id
		switch {
		case candle.InstrumentToken <= 0:
			return 0, invalid("candle %d: instrument token is required", index)
		case !candle.Interval.IsValid():
			return 0, invalid("candle %d: unsupported interval %q", index, candle.Interval)
		case candle.Timestamp.IsZero():
			return 0, invalid("candle %d: date is required", index)
		case candle.High < candle.Low:
			return 0, invalid("candle %d: high is below low", index)
		}
	}
	rows = normalizeCandles(rows)
	write := s.store.InsertHistoricalCandles
	if replace {
		write = s.store.UpsertHistoricalCandles
	}
	if err := write(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// DeleteCandles deletes the instruments' candles of interval, or every
// candle of theirs when interval is empty.
func (s *Service) DeleteCandles(ctx context.Context, ids []models.InstrumentID, interval models.CandleInterval) error {
	ids, err := parseIDs(ids)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return invalid("at least one instrument is required")
	}
	if interval == "" {
		return s.store.DropInstrumentsByID(ctx, ids)
	}
	if !interval.IsValid() {
		return invalid("unsupported interval %q", interval)
	}
	for _, id := range ids {
		if err := s.store.DeleteCandlesByInstrumentAndInterval(ctx, id, interval); err != nil {
			return err
		}
	}
	return nil
}

// FlushLiveCandles deletes every live candle.
func (s *Service) FlushLiveCandles(ctx context.Context) error {
	return s.store.FlushLiveCandles(ctx)
}

// DefaultStartDate is the earliest date a sync goes back to: 2015 for
// intraday intervals, and otherwise 1990, before Kite's earliest daily
// candles, so a daily sync gets all Kite has. Kite answers the windows
// before an instrument's first candle with none.
func DefaultStartDate(interval models.CandleInterval) time.Time {
	if interval.IsIntraday() {
		return time.Date(2015, time.January, 1, 0, 0, 0, 0, models.ExchangeLocation)
	}
	return time.Date(1990, time.January, 1, 0, 0, 0, 0, models.ExchangeLocation)
}

// settledMarketClose is the latest close Kite's historical candles are
// settled for: yesterday's. Same-day pulls after the close can include
// transient after-market candles, and live candles cover the current day.
func settledMarketClose(now time.Time) time.Time {
	local := now.In(models.ExchangeLocation)
	return time.Date(local.Year(), local.Month(), local.Day()-1, 15, 30, 0, 0, models.ExchangeLocation)
}

// normalizeCandles puts timestamps in exchange time, which the store keeps
// the clock of.
func normalizeCandles(rows []candles.Candle) []candles.Candle {
	for index := range rows {
		rows[index].Timestamp = rows[index].Timestamp.In(models.ExchangeLocation)
	}
	return rows
}

func normalizeID(id models.InstrumentID) models.InstrumentID {
	return models.InstrumentID(strings.ToUpper(strings.TrimSpace(string(id))))
}

// parseID is id in its canonical EXCHANGE:SYMBOL form.
func parseID(id models.InstrumentID) (models.InstrumentID, error) {
	if strings.TrimSpace(string(id)) == "" {
		return "", invalid("an instrument is required")
	}
	parsed, err := models.ParseInstrumentID(string(id))
	if err != nil {
		return "", invalid("instrument %q must be EXCHANGE:SYMBOL", id)
	}
	return parsed, nil
}

// parseIDs canonicalizes ids, dropping repeated ones.
func parseIDs(ids []models.InstrumentID) ([]models.InstrumentID, error) {
	out := make([]models.InstrumentID, 0, len(ids))
	seen := make(map[models.InstrumentID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := parseID(id)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[parsed]; ok {
			continue
		}
		seen[parsed] = struct{}{}
		out = append(out, parsed)
	}
	return out, nil
}

func idSet(ids []models.InstrumentID) map[models.InstrumentID]struct{} {
	out := make(map[models.InstrumentID]struct{}, len(ids))
	for _, id := range ids {
		if id = normalizeID(id); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

// difference is the IDs in left and not in right, sorted.
func difference(left, right map[models.InstrumentID]struct{}) []models.InstrumentID {
	out := make([]models.InstrumentID, 0)
	for id := range left {
		if _, ok := right[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
