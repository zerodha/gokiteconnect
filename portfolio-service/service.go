// Package portfolio keeps each user's local portfolio record: snapshots of
// the account read from Kite (profile, margins, holdings, positions,
// orders, trades and MF holdings) and every state an order is seen in. Each
// user's record is its own SQLite database, <base path>/portfolio/<USER>.db.
// It has no server; a command-line tool or the ticker drives it.
package portfolio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

// Portfolio is one user's stored record.
// *repository.PortfolioSQLiteRepository implements it.
type Portfolio interface {
	UserID() string
	RecordOrderUpdates(ctx context.Context, orders ...kite.Order) (int, error)
	OrderUpdates(ctx context.Context, filter models.OrderUpdateFilter) ([]repository.PortfolioOrderUpdate, error)
	OrderHistory(ctx context.Context, orderID string) ([]repository.PortfolioOrderUpdate, error)
	LatestOrderUpdate(ctx context.Context, orderID string) (repository.PortfolioOrderUpdate, error)
	OpenOrders(ctx context.Context) ([]repository.PortfolioOrderUpdate, error)
	SaveSnapshot(ctx context.Context, snapshotType models.SnapshotType, value any) (models.PortfolioSnapshot, error)
	LatestSnapshot(ctx context.Context, snapshotType models.SnapshotType) (models.PortfolioSnapshot, error)
	Snapshots(ctx context.Context, snapshotType models.SnapshotType, from, to time.Time) ([]models.PortfolioSnapshot, error)
	LastSnapshotTimes(ctx context.Context) (map[models.SnapshotType]time.Time, error)
	DeleteSnapshotsBefore(ctx context.Context, before time.Time) (int64, error)
}

// Store opens a user's record.
type Store func(ctx context.Context, userID string) (Portfolio, error)

// DirectoryStore opens records from a directory of per-user databases.
func DirectoryStore(directory *repository.PortfolioSQLiteDirectory) Store {
	return func(ctx context.Context, userID string) (Portfolio, error) {
		return directory.User(ctx, userID)
	}
}

// Source reads one user's account from Kite. *kite.Client implements it.
type Source interface {
	GetUserProfile() (kite.UserProfile, error)
	GetUserMargins() (kite.AllMargins, error)
	GetHoldings() (kite.Holdings, error)
	GetPositions() (kite.Positions, error)
	GetOrders() (kite.Orders, error)
	GetTrades() (kite.Trades, error)
	GetMFHoldings() (kite.MFHoldings, error)
	GetOrderHistory(orderID string) ([]kite.Order, error)
}

// Sources opens a Source with a user's session.
type Sources func(ctx context.Context, userID string) (Source, error)

// ErrInvalid matches every refused request with errors.Is, so a caller can
// tell a usage mistake from a failure.
var ErrInvalid = errors.New("invalid request")

type inputError struct{ message string }

func (e inputError) Error() string        { return e.message }
func (e inputError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error {
	return inputError{message: fmt.Sprintf(format, args...)}
}

// Service records users' portfolios. Sources may be nil when only stored
// records are read and order updates recorded.
type Service struct {
	store   Store
	sources Sources
	log     *slog.Logger
}

// New is a Service over store. A nil log discards logging.
func New(store Store, sources Sources, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, sources: sources, log: log}
}

// SyncResult is what a sync stored. Errors holds the snapshot types that
// could not be read or stored, with why.
type SyncResult struct {
	UserID       string
	Snapshots    []models.PortfolioSnapshot
	OrderUpdates int
	Errors       map[models.SnapshotType]error
}

// Sync reads types from Kite, all of them when none are given, and stores
// a snapshot of each. The orders read are also recorded as order updates.
// A type that fails does not stop the others; the error joins every
// failure. A profile of another account than userID stops the sync before
// anything is stored.
func (s *Service) Sync(ctx context.Context, userID string, types ...models.SnapshotType) (SyncResult, error) {
	user, err := normalizeUser(userID)
	if err != nil {
		return SyncResult{}, err
	}
	types, err = syncTypes(types)
	if err != nil {
		return SyncResult{}, err
	}
	if s.sources == nil {
		return SyncResult{}, invalid("portfolio %s: no Kite source is configured", user)
	}
	source, err := s.sources(ctx, user)
	if err != nil {
		return SyncResult{}, fmt.Errorf("portfolio %s: open Kite session: %w", user, err)
	}
	record, err := s.store(ctx, user)
	if err != nil {
		return SyncResult{}, err
	}

	result := SyncResult{UserID: user, Errors: map[models.SnapshotType]error{}}
	for _, snapshotType := range types {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		value, err := fetch(source, snapshotType)
		if err == nil && snapshotType == models.SnapshotProfile {
			if account := value.(kite.UserProfile).UserID; account != "" && !strings.EqualFold(account, user) {
				return result, fmt.Errorf("portfolio %s: the session is %s's", user, account)
			}
		}
		if err != nil {
			result.Errors[snapshotType] = fmt.Errorf("read %s: %w", snapshotType, err)
			continue
		}
		snapshot, err := record.SaveSnapshot(ctx, snapshotType, value)
		if err != nil {
			result.Errors[snapshotType] = err
			continue
		}
		result.Snapshots = append(result.Snapshots, snapshot)
		if orders, ok := value.(kite.Orders); ok {
			stored, err := record.RecordOrderUpdates(ctx, orders...)
			if err != nil {
				result.Errors[snapshotType] = fmt.Errorf("record order updates: %w", err)
				continue
			}
			result.OrderUpdates += stored
		}
	}

	var errs []error
	for _, snapshotType := range types {
		if err := result.Errors[snapshotType]; err != nil {
			errs = append(errs, err)
		}
	}
	s.log.Info("portfolio synced", "user", user, "snapshots", len(result.Snapshots),
		"order_updates", result.OrderUpdates, "failed", len(errs))
	if len(errs) > 0 {
		return result, fmt.Errorf("portfolio %s: %w", user, errors.Join(errs...))
	}
	return result, nil
}

// RecordOrderUpdates stores order updates for userID, such as the ticker's
// order updates, and returns how many were new.
func (s *Service) RecordOrderUpdates(ctx context.Context, userID string, orders ...kite.Order) (int, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return 0, err
	}
	return record.RecordOrderUpdates(ctx, orders...)
}

// LatestOrder is an order's current recorded state, wrapping
// models.ErrNotFound when none is recorded.
func (s *Service) LatestOrder(ctx context.Context, userID, orderID string) (kite.Order, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return kite.Order{}, err
	}
	update, err := record.LatestOrderUpdate(ctx, orderID)
	return update.Order, err
}

// RefreshOrder reads an order's history from Kite, records it, and
// returns the order's current state.
func (s *Service) RefreshOrder(ctx context.Context, userID, orderID string) (kite.Order, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return kite.Order{}, invalid("an order ID is required")
	}
	record, err := s.open(ctx, userID)
	if err != nil {
		return kite.Order{}, err
	}
	if s.sources == nil {
		return kite.Order{}, invalid("portfolio %s: no Kite source is configured", record.UserID())
	}
	source, err := s.sources(ctx, record.UserID())
	if err != nil {
		return kite.Order{}, fmt.Errorf("portfolio %s: open Kite session: %w", record.UserID(), err)
	}
	history, err := source.GetOrderHistory(orderID)
	if err != nil {
		return kite.Order{}, fmt.Errorf("portfolio %s: read order %s history: %w", record.UserID(), orderID, err)
	}
	if _, err := record.RecordOrderUpdates(ctx, history...); err != nil {
		return kite.Order{}, err
	}
	update, err := record.LatestOrderUpdate(ctx, orderID)
	return update.Order, err
}

// OrderHistory is every recorded state of an order, oldest first.
func (s *Service) OrderHistory(ctx context.Context, userID, orderID string) ([]repository.PortfolioOrderUpdate, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return nil, err
	}
	return record.OrderHistory(ctx, orderID)
}

// OrderUpdates are the recorded updates filter selects, oldest first.
func (s *Service) OrderUpdates(ctx context.Context, userID string, filter models.OrderUpdateFilter) ([]repository.PortfolioOrderUpdate, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return nil, err
	}
	return record.OrderUpdates(ctx, filter)
}

// OpenOrders are the current states of userID's orders not yet complete,
// cancelled or rejected.
func (s *Service) OpenOrders(ctx context.Context, userID string) ([]kite.Order, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return nil, err
	}
	updates, err := record.OpenOrders(ctx)
	if err != nil {
		return nil, err
	}
	orders := make([]kite.Order, 0, len(updates))
	for _, update := range updates {
		orders = append(orders, update.Order)
	}
	return orders, nil
}

// LatestSnapshot is userID's last snapshot of a type, wrapping
// models.ErrNotFound when none was taken.
func (s *Service) LatestSnapshot(ctx context.Context, userID string, snapshotType models.SnapshotType) (models.PortfolioSnapshot, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return models.PortfolioSnapshot{}, err
	}
	return record.LatestSnapshot(ctx, snapshotType)
}

// Snapshots are userID's snapshots of a type taken from from up to but not
// including to, oldest first. A zero bound is open.
func (s *Service) Snapshots(ctx context.Context, userID string, snapshotType models.SnapshotType, from, to time.Time) ([]models.PortfolioSnapshot, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return nil, err
	}
	return record.Snapshots(ctx, snapshotType, from, to)
}

// LastSynced is when each snapshot type was last taken for userID.
func (s *Service) LastSynced(ctx context.Context, userID string) (map[models.SnapshotType]time.Time, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return nil, err
	}
	return record.LastSnapshotTimes(ctx)
}

// Prune deletes userID's snapshots taken before before, keeping the last
// of each type, and returns how many it deleted.
func (s *Service) Prune(ctx context.Context, userID string, before time.Time) (int64, error) {
	record, err := s.open(ctx, userID)
	if err != nil {
		return 0, err
	}
	return record.DeleteSnapshotsBefore(ctx, before)
}

// Latest decodes userID's last snapshot of a type into T, such as
// kite.Holdings for SnapshotHoldings, and returns when it was taken.
func Latest[T any](ctx context.Context, s *Service, userID string, snapshotType models.SnapshotType) (T, time.Time, error) {
	var value T
	snapshot, err := s.LatestSnapshot(ctx, userID, snapshotType)
	if err != nil {
		return value, time.Time{}, err
	}
	if err := snapshot.Decode(&value); err != nil {
		return value, time.Time{}, err
	}
	return value, snapshot.TakenAt, nil
}

func (s *Service) open(ctx context.Context, userID string) (Portfolio, error) {
	user, err := normalizeUser(userID)
	if err != nil {
		return nil, err
	}
	return s.store(ctx, user)
}

func normalizeUser(userID string) (string, error) {
	user, err := models.NormalizeUserID(userID)
	if err != nil {
		return "", invalid("%v", err)
	}
	return user, nil
}

// syncTypes is types without repeats, or every type when empty, with the
// profile first so a wrong session is caught before anything is stored.
func syncTypes(types []models.SnapshotType) ([]models.SnapshotType, error) {
	if len(types) == 0 {
		return models.SnapshotTypes, nil
	}
	seen := map[models.SnapshotType]bool{}
	var ordered []models.SnapshotType
	for _, snapshotType := range types {
		if !snapshotType.IsValid() {
			return nil, invalid("unknown snapshot type %q", snapshotType)
		}
		seen[snapshotType] = true
	}
	for _, snapshotType := range models.SnapshotTypes {
		if seen[snapshotType] {
			ordered = append(ordered, snapshotType)
		}
	}
	return ordered, nil
}

func fetch(source Source, snapshotType models.SnapshotType) (any, error) {
	switch snapshotType {
	case models.SnapshotProfile:
		return value(source.GetUserProfile())
	case models.SnapshotMargins:
		return value(source.GetUserMargins())
	case models.SnapshotHoldings:
		return value(source.GetHoldings())
	case models.SnapshotPositions:
		return value(source.GetPositions())
	case models.SnapshotOrders:
		return value(source.GetOrders())
	case models.SnapshotTrades:
		return value(source.GetTrades())
	case models.SnapshotMFHoldings:
		return value(source.GetMFHoldings())
	}
	return nil, invalid("unknown snapshot type %q", snapshotType)
}

func value[T any](v T, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return v, nil
}
