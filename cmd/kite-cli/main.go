// Command kite-cli is the terminal console for local Kite accounts: it opens
// on the stored users, shows a user's positions, holdings and mutual funds,
// explores the instrument catalog with live prices, and places orders.
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

	tea "charm.land/bubbletea/v2"
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
		repository, err := instrumentstore.NewInstrumentsDuckDBRepository(ctx, instrumentsPath)
		if err != nil {
			return nil, nil, err
		}
		client := kite.New("")
		client.SetAppName("kite-cli")
		client.SetTimeout(kiteRequestTimeout)
		client.SetInstrumentRepository(repository)
		instruments := client.Instruments()
		if _, err := instruments.RefreshIfStale(ctx); err != nil {
			_ = instruments.Close()
			return nil, nil, err
		}
		return instruments, repository, nil
	}

	// The explorer, the strike picker and the positions' underlyings share
	// one catalog, opened by whichever needs it first.
	shared := &sharedCatalog{open: factory}
	deps := appDeps{
		manager:       manager,
		credentialsDB: credentialsPath,
		catalogPath:   instrumentsPath,
		openCatalog:   shared.get,
		indices:       nse.NewIndicesClient(),
		loadPortfolio: kitePortfolioLoader(manager, catalogInstruments(shared.get)),
		place:         kiteOrderPlacer(manager),
		modify:        kiteOrderModifier(manager),
		cancel:        kiteOrderCanceller(manager),
		defaultUser:   strings.TrimSpace(*userFlag),
		every:         *quotesEvery,
	}
	if !*noQuotes {
		deps.loadQuotes = credentialsQuoteLoader(credentialsPath, strings.TrimSpace(*dataUserFlag))
	}
	model := newApp(ctx, deps)
	program := tea.NewProgram(model, tea.WithContext(ctx))
	model.send = program.Send
	_, runErr := program.Run()
	if ctx.Err() != nil {
		// SIGINT or SIGTERM stopped the program.
		runErr = nil
	}
	return errors.Join(runErr, model.shutdown(), shared.close())
}

// sharedCatalog opens the catalog once for every caller and closes it once.
type sharedCatalog struct {
	open    catalogFactory
	mu      sync.Mutex
	catalog catalog
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
		shared.catalog, shared.lists = opened, lists
	}
	return shared.catalog, shared.lists, nil
}

func (shared *sharedCatalog) close() error {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.catalog == nil {
		return nil
	}
	err := shared.catalog.Close()
	shared.catalog = nil
	return err
}
