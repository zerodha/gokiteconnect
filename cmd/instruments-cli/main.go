// Command instruments-cli provides an interactive terminal explorer for the
// local Zerodha instrument catalog.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/devshoe/gokiteconnect/config"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/pkg/nse"
	instrumentstore "github.com/devshoe/gokiteconnect/repository"
)

const kiteRequestTimeout = 45 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "instruments: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	databaseFlag := flag.String("db", settings.InstrumentsPath, "DuckDB database used for the instrument catalog")
	listsFlag := flag.String("lists-db", settings.ListsPath, "SQLite database used for instrument lists")
	dataUserFlag := flag.String("data-user", settings.DataUserID, "API user whose session fetches prices; empty picks the first API user with a data subscription")
	quotesEvery := flag.Duration("quotes-every", 5*time.Second, "how often visible prices refresh; 0 refreshes only on R")
	noQuotes := flag.Bool("no-quotes", false, "start without prices")
	flag.Parse()

	databasePath := strings.TrimSpace(*databaseFlag)
	if databasePath == "" {
		return errors.New("database path is required")
	}
	if databasePath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	factory := func(ctx context.Context) (catalog, listStore, error) {
		return openCatalog(ctx, "instruments-cli", databasePath, listsPath)
	}

	ui := newInstrumentApp(ctx, databasePath, factory, nse.NewIndicesClient())
	if !*noQuotes {
		ui.loadQuotes = credentialsQuoteLoader(settings.CredentialsPath, strings.TrimSpace(*dataUserFlag))
		ui.quoteEvery = *quotesEvery
	}
	return ui.run()
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
