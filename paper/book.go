// Package paper is a paper broker: each user's paper account takes orders
// like Kite does and fills them against live prices, keeping only its
// orders and fills, in the user's database, <portfolio dir>/<USER>.db,
// beside their live order record. Positions, holdings and funds are worked
// out from the fills. Nothing reaches Kite's order endpoints: prices,
// candles and margins come from the data user.
//
// Orders fill when the account is read or matched: market orders at once
// against the quote's depth, and limit and stop orders when the last price
// reaches them, or, for time the account was not read, when a minute
// candle did. Day-end work runs on the first read after it is due: open
// day orders are cancelled at the close, intraday positions are squared
// off at 15:20 and contracts settle at expiry, in cash, at the
// underlying's close. Delivery bought on one day is a holding the next.
package paper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
	"github.com/devshoe/gokiteconnect/repository"
)

// Data is the market a paper account trades against. The data user's
// *kite.Client implements it.
type Data interface {
	broker.Market
	GetHistoricalData(instrumentToken int, interval string, from, to time.Time, continuous, oi bool) ([]kite.HistoricalData, error)
	GetBasketMargins(kite.GetBasketParams) (kite.BasketMargins, error)
}

// Config sets up a Book.
type Config struct {
	// Dir is the portfolio directory of the users' databases.
	Dir string
	// Capital is the cash a new paper account opens with.
	Capital float64
	// SlippageBPS moves every market fill against the order by this many
	// basis points, past what the quote's depth already costs.
	SlippageBPS float64
	// Data opens the market data. It is opened again after dataTTL, so a
	// refreshed data session is picked up.
	Data func(context.Context) (Data, error)
	// Instruments looks up an instrument in the catalog.
	Instruments func(context.Context, models.InstrumentID) (models.Instrument, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// DefaultCapital is what a paper account opens with when the settings
// name no amount.
const DefaultCapital = 10_00_000

const (
	// dataTTL is how long an opened data source is reused.
	dataTTL = 5 * time.Minute
	// viewTTL is how long a worked-out account is reused by reads, so one
	// portfolio load reads the market once.
	viewTTL = 2 * time.Second
)

// Book keeps users' paper accounts, opening each database once.
type Book struct {
	config Config

	mu       sync.Mutex
	users    map[string]*userBook
	closed   bool
	data     Data
	openedAt time.Time
}

// NewBook keeps paper accounts as config describes.
func NewBook(config Config) (*Book, error) {
	if strings.TrimSpace(config.Dir) == "" {
		return nil, fmt.Errorf("%w: paper accounts need the portfolio directory", models.ErrInvalidInput)
	}
	if config.Data == nil || config.Instruments == nil {
		return nil, fmt.Errorf("%w: paper accounts need market data and the instrument catalog", models.ErrInvalidInput)
	}
	if config.Capital <= 0 {
		config.Capital = DefaultCapital
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Book{config: config, users: map[string]*userBook{}}, nil
}

// userBook is one user's open paper account.
type userBook struct {
	mu      sync.Mutex
	userID  string
	store   *repository.PaperSQLiteRepository
	account models.PaperAccount
	// checked is when each open order was last matched against candles.
	checked map[string]time.Time
	// orderSeq and tradeSeq number new orders and fills.
	orderSeq, tradeSeq int
	// view is the account last worked out, for viewTTL.
	view *view
}

// Open opens userID's paper account, creating it with the configured
// capital the first time. It satisfies broker.Opener.
func (b *Book) Open(ctx context.Context, userID string) (broker.Account, error) {
	user, err := b.user(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Account{book: b, user: user, ctx: ctx}, nil
}

func (b *Book) user(ctx context.Context, userID string) (*userBook, error) {
	id, err := models.NormalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("paper accounts are closed")
	}
	if user, ok := b.users[id]; ok {
		return user, nil
	}
	store, err := repository.OpenUserPaperSQLiteRepository(ctx, b.config.Dir, id)
	if err != nil {
		return nil, err
	}
	now := b.config.Now()
	account, err := store.OpenAccount(ctx, models.PaperAccount{
		Capital:   b.config.Capital,
		CreatedAt: now,
		// A new account has no earlier day to settle.
		SettledThrough: dayOf(now).AddDate(0, 0, -1),
	})
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	orders, err := store.Orders(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	fills, err := store.Fills(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	user := &userBook{userID: id, store: store, account: account, checked: map[string]time.Time{},
		orderSeq: lastSeq(orders, func(o kite.Order) string { return o.OrderID }),
		tradeSeq: lastSeq(fills, func(f models.PaperFill) string { return f.TradeID })}
	b.users[id] = user
	return user, nil
}

// Reset deletes userID's paper account, every order and fill of it, and
// opens it again with the configured capital, as on first use.
func (b *Book) Reset(ctx context.Context, userID string) error {
	user, err := b.user(ctx, userID)
	if err != nil {
		return err
	}
	user.mu.Lock()
	defer user.mu.Unlock()
	if err := user.store.Reset(ctx); err != nil {
		return err
	}
	now := b.config.Now()
	account, err := user.store.OpenAccount(ctx, models.PaperAccount{
		Capital:        b.config.Capital,
		CreatedAt:      now,
		SettledThrough: dayOf(now).AddDate(0, 0, -1),
	})
	if err != nil {
		return err
	}
	user.account, user.checked, user.orderSeq, user.tradeSeq, user.view = account, map[string]time.Time{}, 0, 0, nil
	return nil
}

// market returns the data source, opening it again once it is dataTTL old.
func (b *Book) market(ctx context.Context) (Data, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.config.Now()
	if b.data != nil && now.Sub(b.openedAt) < dataTTL {
		return b.data, nil
	}
	data, err := b.config.Data(ctx)
	if err != nil {
		return nil, fmt.Errorf("paper market data: %w", err)
	}
	b.data, b.openedAt = data, now
	return data, nil
}

// Close closes every paper database the book opened.
func (b *Book) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	var errs []error
	for id, user := range b.users {
		user.mu.Lock()
		errs = append(errs, user.store.Close())
		user.mu.Unlock()
		delete(b.users, id)
	}
	return errors.Join(errs...)
}
