package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds local settings shared by the command-line tools.
//
// Values come only from the settings file, ~/tradebot/settings.json; a
// setting the file leaves empty takes its default.
type Config struct {
	// StorageRoot is TRADEBOT_LOCAL_STORAGE_ROOT, default ~/tradebot.
	StorageRoot string
	// CredentialsPath is TRADEBOT_CREDENTIALS_SQLITE_PATH under StorageRoot,
	// default credentials.sqlite3.
	CredentialsPath string
	// InstrumentsPath is TRADEBOT_INSTRUMENTS_DUCKDB_PATH under StorageRoot,
	// default instruments.duckdb.
	InstrumentsPath string
	// ListsPath is TRADEBOT_LISTS_SQLITE_PATH under StorageRoot, default
	// lists.sqlite3.
	ListsPath string
	// CandlesPath is TRADEBOT_CANDLES_DUCKDB_PATH under StorageRoot, default
	// market-data/historical_data.duckdb.
	CandlesPath string
	// TickerUserID is TRADEBOT_TICKER_USER_ID, the Zerodha account the ticker streams for.
	TickerUserID string
	// DataUserID is TRADEBOT_DATA_USER_ID, the Zerodha API user whose session
	// the command-line tools use for market quotes. When empty, tools pick an
	// API user with a data subscription.
	DataUserID string
	// DefaultUserID is TRADEBOT_DEFAULT_USER_ID, default CC0006: the
	// account kite-cli opens at start.
	DefaultUserID string
	// PriceMode is TRADEBOT_PRICE_MODE: PriceModeQuote (the default) or
	// PriceModeTicker.
	PriceMode string
	// SettingsPath is the settings file.
	SettingsPath string
	// Settings are the settings file's entries, for settings the fields
	// above don't cover.
	Settings Settings
}

// Load reads Config from the settings file.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	settingsPath, err := SettingsPath()
	if err != nil {
		return Config{}, err
	}
	settings, err := ReadSettings(settingsPath)
	if err != nil {
		return Config{}, fmt.Errorf("read settings: %w", err)
	}

	root := filepath.Clean(expandHome(settings.Get("TRADEBOT_LOCAL_STORAGE_ROOT"), home))
	credentials, err := underRoot(root, settings, "TRADEBOT_CREDENTIALS_SQLITE_PATH")
	if err != nil {
		return Config{}, err
	}
	instruments, err := underRoot(root, settings, "TRADEBOT_INSTRUMENTS_DUCKDB_PATH")
	if err != nil {
		return Config{}, err
	}
	lists, err := underRoot(root, settings, "TRADEBOT_LISTS_SQLITE_PATH")
	if err != nil {
		return Config{}, err
	}
	candles, err := underRoot(root, settings, "TRADEBOT_CANDLES_DUCKDB_PATH")
	if err != nil {
		return Config{}, err
	}
	priceMode := strings.ToLower(settings.Get("TRADEBOT_PRICE_MODE"))
	if priceMode != PriceModeQuote && priceMode != PriceModeTicker {
		return Config{}, fmt.Errorf("price mode %q: want %s or %s", priceMode, PriceModeQuote, PriceModeTicker)
	}

	return Config{
		StorageRoot:     root,
		CredentialsPath: credentials,
		InstrumentsPath: instruments,
		ListsPath:       lists,
		CandlesPath:     candles,
		TickerUserID:    settings.Get("TRADEBOT_TICKER_USER_ID"),
		DataUserID:      settings.Get("TRADEBOT_DATA_USER_ID"),
		DefaultUserID:   settings.Get("TRADEBOT_DEFAULT_USER_ID"),
		PriceMode:       priceMode,
		SettingsPath:    settingsPath,
		Settings:        settings,
	}, nil
}

// underRoot is key's path, which is relative to the storage root.
func underRoot(root string, settings Settings, key string) (string, error) {
	offset := settings.Get(key)
	if filepath.IsAbs(offset) || !filepath.IsLocal(offset) {
		return "", fmt.Errorf("%s %q: want a path inside the base path", key, offset)
	}
	return filepath.Join(root, offset), nil
}

// expandHome replaces a leading ~ or $HOME with home.
func expandHome(value, home string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"~", "$HOME"} {
		if value == prefix {
			return home
		}
		if rest, ok := strings.CutPrefix(value, prefix+"/"); ok {
			return filepath.Join(home, rest)
		}
	}
	return value
}
