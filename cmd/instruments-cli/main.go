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

	kiteconnect "github.com/devshoe/gokiteconnect"
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
	settings, err := kiteconnect.LoadConfig()
	if err != nil {
		return err
	}
	databaseFlag := flag.String("db", settings.InstrumentsPath, "DuckDB database used for the instrument catalog")
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	factory := func(ctx context.Context) (catalog, error) {
		repository, err := instrumentstore.NewInstrumentsDuckDBRepository(ctx, databasePath)
		if err != nil {
			return nil, err
		}
		kite := kiteconnect.New("")
		kite.SetAppName("instruments-cli")
		kite.SetTimeout(kiteRequestTimeout)
		kite.SetInstrumentRepository(repository)
		instruments := kite.Instruments()
		if _, err := instruments.RefreshIfStale(ctx); err != nil {
			_ = instruments.Close()
			return nil, err
		}
		return instruments, nil
	}

	ui := newInstrumentApp(ctx, databasePath, factory)
	return ui.run()
}
