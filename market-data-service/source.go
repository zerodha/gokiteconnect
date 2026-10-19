package marketdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
	instrumentstore "github.com/devshoe/gokiteconnect/repository"
	_ "github.com/mattn/go-sqlite3"
)

const (
	kiteRequestTimeout = 30 * time.Second
	// historicalRequestGap spaces historical-data requests to stay within
	// Kite's rate limit.
	historicalRequestGap = 100 * time.Millisecond
	// tokenIndexTTL is how long CatalogTokens reuses the token index.
	tokenIndexTTL = time.Hour
)

// TokenLookup finds an instrument's token, returning an error wrapping
// models.ErrNotFound for an unknown instrument.
type TokenLookup func(ctx context.Context, id models.InstrumentID) (int64, error)

// KiteSource fetches historical candles with the data user's session. It
// reads the stored access token on every sync and never logs in: a fresh
// login could invalidate a session another tool is using.
type KiteSource struct {
	// client opens a Kite client with the data user's session.
	client func(ctx context.Context) (*kite.Client, error)
	token  TokenLookup
	sleep  func(ctx context.Context, d time.Duration) error
}

// NewKiteSource fetches with dataUserID's stored session, or the first API
// user with a data subscription when it is empty, finding tokens with
// tokens.
func NewKiteSource(credentialsPath, dataUserID string, tokens TokenLookup) *KiteSource {
	return &KiteSource{
		client: func(ctx context.Context) (*kite.Client, error) {
			return dataUserClient(ctx, credentialsPath, dataUserID)
		},
		token: tokens,
		sleep: sleepContext,
	}
}

// StreamCandles fetches the instrument's candles in [from, to) one Kite
// batch at a time, handing each batch to consume.
func (k *KiteSource) StreamCandles(ctx context.Context, id models.InstrumentID, interval models.CandleInterval, from, to time.Time, consume func([]candles.Candle) error) error {
	kiteInterval, ok := kite.HistoricalInterval(interval)
	if !ok {
		return fmt.Errorf("unsupported interval %q", interval)
	}
	window, ok := kite.HistoricalBatchDuration(kiteInterval)
	if !ok {
		return fmt.Errorf("unsupported interval %q", interval)
	}
	token, err := k.token(ctx, id)
	if err != nil {
		return err
	}
	client, err := k.client(ctx)
	if err != nil {
		return err
	}
	for start := from; start.Before(to); {
		end := start.Add(window)
		if end.After(to) {
			end = to
		}
		data, err := client.GetHistoricalData(int(token), kiteInterval, start, end, false, true)
		if err != nil {
			return fmt.Errorf("%s to %s: %w", start.Format(time.DateOnly), end.Format(time.DateOnly), err)
		}
		batch := make([]candles.Candle, 0, len(data))
		for _, d := range data {
			batch = append(batch, candles.Candle{
				InstrumentID:    id,
				InstrumentToken: token,
				Interval:        interval,
				Timestamp:       d.Date.Time,
				Open:            d.Open,
				High:            d.High,
				Low:             d.Low,
				Close:           d.Close,
				Volume:          d.Volume,
				OI:              d.OI,
			})
		}
		if err := consume(batch); err != nil {
			return err
		}
		start = end
		if start.Before(to) {
			if err := k.sleep(ctx, historicalRequestGap); err != nil {
				return err
			}
		}
	}
	return nil
}

// CatalogTokens finds tokens in the instruments database at path, reading
// its token index at most once an hour and refreshing the catalog first
// when it is stale. The database is opened only for the read, so other
// tools can use it; a process that keeps the catalog open should look
// tokens up there instead.
func CatalogTokens(path string) TokenLookup {
	var (
		mu       sync.Mutex
		index    *kite.InstrumentTokenIndex
		loadedAt time.Time
	)
	return func(ctx context.Context, id models.InstrumentID) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		if index == nil || time.Since(loadedAt) > tokenIndexTTL {
			loaded, err := catalogTokenIndex(ctx, path)
			if err != nil {
				return 0, fmt.Errorf("instrument tokens: %w", err)
			}
			index, loadedAt = loaded, time.Now()
		}
		token, ok := index.Token(id)
		if !ok {
			return 0, fmt.Errorf("instrument %s: %w", id, models.ErrNotFound)
		}
		return token, nil
	}
}

func catalogTokenIndex(ctx context.Context, path string) (*kite.InstrumentTokenIndex, error) {
	repository, err := instrumentstore.NewInstrumentsDuckDBRepository(ctx, path)
	if err != nil {
		return nil, err
	}
	client := kite.New("")
	client.SetTimeout(kiteRequestTimeout)
	client.SetInstrumentRepository(repository)
	catalog := client.Instruments()
	defer catalog.Close()
	if _, err := catalog.RefreshIfStale(ctx); err != nil {
		return nil, err
	}
	return catalog.TokenIndex(ctx)
}

// dataUserClient is a Kite client with the data user's stored session. An
// empty userID picks the first API user with a data subscription.
func dataUserClient(ctx context.Context, path, userID string) (*kite.Client, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("credentials database %s: %w", path, err)
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	store, err := credentialstore.NewUserSQLiteRepository(database)
	if err != nil {
		return nil, err
	}
	user, err := dataUser(ctx, store, userID)
	if err != nil {
		return nil, err
	}
	client := kite.New(user.APIKey)
	client.SetAppName("market-data-service")
	client.SetTimeout(kiteRequestTimeout)
	client.SetAccessToken(user.AccessToken)
	return client, nil
}

func dataUser(ctx context.Context, store credentials.CredentialsRepository, userID string) (*credentials.Credentials, error) {
	var user *credentials.Credentials
	if userID != "" {
		found, err := store.GetUserByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("data user %s: %w", userID, err)
		}
		user = found
	} else {
		users, err := store.ListAvailableUsers(ctx)
		if err != nil {
			return nil, err
		}
		for i := range users {
			if users[i].IsAPIUser && users[i].DataSubscription {
				user = &users[i]
				break
			}
		}
		if user == nil {
			return nil, errors.New("no API user with a data subscription; set TRADEBOT_DATA_USER_ID in settings")
		}
	}
	if !user.IsAPIUser {
		return nil, fmt.Errorf("data user %s is a web user; historical data needs an API user", user.UserID)
	}
	if user.AccessToken == "" {
		return nil, fmt.Errorf("data user %s has no session; refresh it in kite-cli", user.UserID)
	}
	return user, nil
}

// ListsReader reads instrument lists from the lists database, opening it
// only for the read. The first open migrates the lists an instruments
// database at instrumentsPath held before lists moved out of it.
func ListsReader(listsPath, instrumentsPath string) Lists {
	return func(ctx context.Context, listID string) (models.List, error) {
		lists, err := instrumentstore.NewListsSQLiteRepository(ctx, listsPath, instrumentsPath)
		if err != nil {
			return models.List{}, err
		}
		defer lists.Close()
		return lists.GetList(ctx, listID)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
