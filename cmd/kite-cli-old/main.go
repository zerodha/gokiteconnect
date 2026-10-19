// Command kite-cli is the terminal console for local Kite accounts: it opens
// on the stored users, shows a user's positions, holdings and mutual funds,
// and explores the instrument catalog with live prices.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devshoe/gokiteconnect/config"
	"github.com/devshoe/gokiteconnect/credentials"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/pkg/nse"
	instrumentstore "github.com/devshoe/gokiteconnect/repository"
	_ "github.com/mattn/go-sqlite3"
)

const kiteRequestTimeout = 45 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "kite-cli: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	instrumentsFlag := flag.String("instruments-db", settings.InstrumentsPath, "DuckDB database used for the instrument catalog")
	listsFlag := flag.String("lists-db", settings.ListsPath, "SQLite database used for instrument lists")
	credentialsFlag := flag.String("credentials-db", settings.CredentialsPath, "SQLite database used for the credentials store")
	dataUserFlag := flag.String("data-user", settings.DataUserID, "API user whose session fetches instrument prices; empty picks the first API user with a data subscription")
	quotesEvery := flag.Duration("quotes-every", 5*time.Second, "how often visible prices (instruments and portfolio) refresh; 0 refreshes only on R")
	userFlag := flag.String("user", settings.DefaultUserID, "user whose portfolio opens at start; empty starts on the users screen")
	noQuotes := flag.Bool("no-quotes", false, "start the instruments explorer without prices")
	flag.Parse()

	instrumentsPath := strings.TrimSpace(*instrumentsFlag)
	credentialsPath := strings.TrimSpace(*credentialsFlag)
	if instrumentsPath == "" || credentialsPath == "" {
		return errors.New("instruments and credentials database paths are required")
	}
	if instrumentsPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(instrumentsPath), 0o700); err != nil {
			return fmt.Errorf("create instruments directory: %w", err)
		}
	}
	listsPath := strings.TrimSpace(*listsFlag)
	if listsPath == "" {
		return errors.New("lists database path is required")
	}
	if listsPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(listsPath), 0o700); err != nil {
			return fmt.Errorf("create lists directory: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(credentialsPath), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}

	database, err := sql.Open("sqlite3", credentialsPath)
	if err != nil {
		return err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	store, err := credentialstore.NewUserSQLiteRepository(database)
	if err != nil {
		return err
	}
	manager, err := credentials.NewUserManager(store)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	factory := func(ctx context.Context) (catalog, listStore, error) {
		return openCatalog(ctx, "kite-cli", instrumentsPath, listsPath)
	}

	// The explorer and the positions' underlyings share one catalog, opened
	// by whichever needs it first.
	shared := &sharedCatalog{open: factory}
	instruments := newInstrumentApp(ctx, instrumentsPath, shared.get, nse.NewIndicesClient())
	if !*noQuotes {
		instruments.loadQuotes = credentialsQuoteLoader(credentialsPath, strings.TrimSpace(*dataUserFlag))
		instruments.quoteEvery = *quotesEvery
	}
	load := kitePortfolioLoader(manager, catalogInstruments(shared.get))
	app := newKiteApp(ctx, manager, credentialsPath, instruments, load, kiteOrderPlacer(manager))
	app.defaultUser = strings.TrimSpace(*userFlag)
	app.portfolio.priceEvery = *quotesEvery
	runErr := app.run()
	return errors.Join(runErr, shared.close())
}

// sharedCatalog opens the catalog once for every caller. Both the explorer
// and main close it; only the first close reaches the database.
type sharedCatalog struct {
	open    catalogFactory
	mu      sync.Mutex
	catalog *closeOnceCatalog
	lists   listStore
}

func (shared *sharedCatalog) get(ctx context.Context) (catalog, listStore, error) {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.catalog == nil {
		opened, lists, err := shared.open(ctx)
		if err != nil {
			return nil, nil, err
		}
		shared.catalog, shared.lists = &closeOnceCatalog{catalog: opened}, lists
	}
	return shared.catalog, shared.lists, nil
}

func (shared *sharedCatalog) close() error {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.catalog == nil {
		return nil
	}
	return shared.catalog.Close()
}

type closeOnceCatalog struct {
	catalog
	once sync.Once
	err  error
}

func (c *closeOnceCatalog) Close() error {
	c.once.Do(func() { c.err = c.catalog.Close() })
	return c.err
}

// catalogWithLists closes the lists database with the catalog.
type catalogWithLists struct {
	*kite.InstrumentCatalog
	lists *instrumentstore.ListsSQLiteRepository
}

func (c catalogWithLists) Close() error {
	return errors.Join(c.InstrumentCatalog.Close(), c.lists.Close())
}

// openCatalog opens the lists, migrating them out of an older instruments
// database first, then the catalog, refreshing it when stale. The catalog
// is read-only between refreshes, so other tools can read it meanwhile.
func openCatalog(ctx context.Context, appName, instrumentsPath, listsPath string) (catalog, listStore, error) {
	lists, err := instrumentstore.NewListsSQLiteRepository(ctx, listsPath, instrumentsPath)
	if err != nil {
		return nil, nil, err
	}
	repository, err := instrumentstore.NewInstrumentsDuckDBRepository(ctx, instrumentsPath)
	if err != nil {
		_ = lists.Close()
		return nil, nil, err
	}
	client := kite.New("")
	client.SetAppName(appName)
	client.SetTimeout(kiteRequestTimeout)
	client.SetInstrumentRepository(repository)
	instruments := catalogWithLists{InstrumentCatalog: client.Instruments(), lists: lists}
	if _, err := instruments.RefreshIfStale(ctx); err != nil {
		_ = instruments.Close()
		return nil, nil, err
	}
	return instruments, lists, nil
}
