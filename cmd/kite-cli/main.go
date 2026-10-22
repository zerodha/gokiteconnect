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
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/paper"
	"github.com/devshoe/gokiteconnect/pkg/broker"
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
	listsFlag := flag.String("lists-db", settings.ListsPath, "SQLite database used for instrument lists and order baskets")
	fundamentalsFlag := flag.String("fundamentals-db", settings.FundamentalsPath, "SQLite database the screener's fundamentals are stored in")
	candlesFlag := flag.String("candles-db", settings.CandlesPath, "DuckDB candles database the Data screen shows and syncs")
	credentialsFlag := flag.String("credentials-db", settings.CredentialsPath, "SQLite database used for the credentials store")
	dataUserFlag := flag.String("data-user", settings.DataUserID, "API user whose session fetches instrument prices; empty picks the first API user with a data subscription")
	quotesEvery := flag.Duration("quotes-every", 5*time.Second, "how often visible prices (instruments and portfolio) refresh; 0 refreshes only on R")
	userFlag := flag.String("user", settings.DefaultUserID, "user whose portfolio opens at start; empty starts on the users screen")
	noQuotes := flag.Bool("no-quotes", false, "start the instruments explorer without prices")
	modeFlag := flag.String("mode", settings.AccountMode, "account orders and portfolios start in: paper or live; P toggles it")
	flag.Parse()
	mode, err := broker.ParseMode(strings.ToLower(strings.TrimSpace(*modeFlag)))
	if err != nil {
		return err
	}

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

	// The lists database also keeps the baskets, on one connection. Lists
	// still in an older instruments database move here first.
	lists, err := instrumentstore.NewListsSQLiteRepository(ctx, listsPath, instrumentsPath)
	if err != nil {
		return err
	}
	defer lists.Close()
	baskets, err := lists.Baskets(ctx)
	if err != nil {
		return err
	}
	factory := func(ctx context.Context) (catalog, listStore, error) {
		return openCatalog(ctx, "kite-cli", instrumentsPath, lists)
	}

	// The explorer, the strike picker and the positions' underlyings share
	// one catalog, opened by whichever needs it first.
	shared := &sharedCatalog{open: factory}
	// Paper accounts trade on the data user's prices, candles and margin
	// calculator, read on its stored session: kite-cli never logs it in.
	dataUserID := strings.TrimSpace(*dataUserFlag)
	book, err := paper.NewBook(paper.Config{
		Dir:         settings.PortfolioDir,
		Capital:     settings.PaperCapital,
		SlippageBPS: settings.PaperSlippageBPS,
		Data: func(ctx context.Context) (paper.Data, error) {
			client, _, err := dataClient(ctx, credentialsPath, dataUserID)
			if err != nil {
				return nil, fmt.Errorf("paper market data: %w", err)
			}
			return client, nil
		},
		Instruments: func(ctx context.Context, id models.InstrumentID) (models.Instrument, error) {
			catalog, _, err := shared.get(ctx)
			if err != nil {
				return models.Instrument{}, err
			}
			return catalog.Get(ctx, id)
		},
	})
	if err != nil {
		return err
	}
	defer book.Close()
	accounts := broker.Accounts{Live: broker.KiteSessions(manager, "kite-cli"), Paper: book.Open}
	deps := appDeps{
		manager:       manager,
		credentialsDB: credentialsPath,
		catalogPath:   instrumentsPath,
		listsPath:     listsPath,
		openCatalog:   shared.get,
		indices:       nse.NewIndicesClient(),
		loadPortfolio: accountPortfolioLoader(accounts, catalogInstruments(shared.get)),
		settingsPath:  settings.SettingsPath,
		mode:          mode,
		place:         accountOrderPlacer(accounts),
		modify:        accountOrderModifier(accounts),
		cancel:        accountOrderCanceller(accounts),
		baskets:       baskets,
		resetPaper:    book.Reset,
		basketMargins: dataBasketMargins(credentialsPath, dataUserID),
		openWeb:       kiteWebOpener(settings.BrowserDir),
		defaultUser:   strings.TrimSpace(*userFlag),
		every:         *quotesEvery,
	}
	if fundamentalsPath := strings.TrimSpace(*fundamentalsFlag); fundamentalsPath != "" {
		if fundamentalsPath != ":memory:" {
			if err := os.MkdirAll(filepath.Dir(fundamentalsPath), 0o700); err != nil {
				return fmt.Errorf("create fundamentals directory: %w", err)
			}
		}
		deps.fundamentals = localFundamentals{path: fundamentalsPath, credentialsPath: credentialsPath, dataUserID: strings.TrimSpace(*dataUserFlag)}
	}
	if !*noQuotes {
		deps.loadQuotes = credentialsQuoteLoader(credentialsPath, strings.TrimSpace(*dataUserFlag))
	}
	// Price mode ticker reads ticks ticker-server publishes, through the
	// local NATS WebSocket, falling back to quotes when it isn't running.
	var ticker *tickerConnector
	if settings.PriceMode == config.PriceModeTicker {
		healthURL := "http://localhost:" + settings.Settings.Get("TRADEBOT_TICKER_SERVICE_PORT") + "/health"
		ticker = &tickerConnector{connect: localTicker(settings.Settings.Get("TRADEBOT_NATS_WS_URL"), healthURL, settings.Settings.Get("TRADEBOT_HEARTBEAT_TOPIC"))}
		defer ticker.close()
		deps.loadPortfolio = tickerPortfolioLoader(deps.loadPortfolio, ticker, *quotesEvery)
		if deps.loadQuotes != nil {
			deps.loadQuotes = tickerQuoteLoader(deps.loadQuotes, ticker, *quotesEvery)
		}
	}
	if candlesPath := strings.TrimSpace(*candlesFlag); candlesPath != "" {
		deps.candlesPath = candlesPath
		deps.candles = localCandles{path: candlesPath, credentialsPath: credentialsPath, dataUserID: strings.TrimSpace(*dataUserFlag), open: shared.get}
	}
	model := newApp(ctx, deps)
	program := tea.NewProgram(model, tea.WithContext(ctx))
	model.send = program.Send
	if ticker != nil {
		ticker.setNotify(func() { program.Send(tickerTicksMsg{}) })
	}
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

// openCatalog opens the catalog beside the open lists, refreshing it when
// stale. The catalog is read-only between refreshes, so other tools can
// read it meanwhile.
func openCatalog(ctx context.Context, appName, instrumentsPath string, lists *instrumentstore.ListsSQLiteRepository) (catalog, listStore, error) {
	repository, err := instrumentstore.NewInstrumentsDuckDBRepository(ctx, instrumentsPath)
	if err != nil {
		return nil, nil, err
	}
	client := kite.New("")
	client.SetAppName(appName)
	client.SetTimeout(kiteRequestTimeout)
	client.SetInstrumentRepository(repository)
	instruments := client.Instruments()
	if _, err := instruments.RefreshIfStale(ctx); err != nil {
		_ = instruments.Close()
		return nil, nil, err
	}
	return instruments, lists, nil
}
