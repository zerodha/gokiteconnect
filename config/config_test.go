package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHomeSettings makes a home whose settings file holds values.
func writeHomeSettings(t *testing.T, values map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	var settings Settings
	for key, value := range values {
		settings.Set(key, value)
	}
	if err := WriteSettings(filepath.Join(home, "tradebot", "settings.json"), settings); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestLoadUsesTradebotHomeDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The environment no longer counts.
	t.Setenv("TRADEBOT_LOCAL_STORAGE_ROOT", "/elsewhere")
	t.Setenv("TRADEBOT_TICKER_USER_ID", "ENV001")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	wantRoot := filepath.Join(home, "tradebot")
	if got.StorageRoot != wantRoot {
		t.Fatalf("unexpected storage root: got %q want %q", got.StorageRoot, wantRoot)
	}
	if got.CredentialsPath != filepath.Join(wantRoot, "credentials.sqlite3") {
		t.Fatalf("unexpected database path: got %q", got.CredentialsPath)
	}
	if got.InstrumentsPath != filepath.Join(wantRoot, "instruments.duckdb") {
		t.Fatalf("unexpected instruments database path: got %q", got.InstrumentsPath)
	}
	if got.CandlesPath != filepath.Join(wantRoot, "market-data", "historical_data.duckdb") {
		t.Fatalf("unexpected candles database path: got %q", got.CandlesPath)
	}
	if got.PortfolioDir != filepath.Join(wantRoot, "portfolio") {
		t.Fatalf("unexpected portfolio directory: got %q", got.PortfolioDir)
	}
	if got.FundamentalsPath != filepath.Join(wantRoot, "fundamentals.sqlite3") {
		t.Fatalf("unexpected fundamentals path: got %q", got.FundamentalsPath)
	}
	if got.BrowserDir != filepath.Join(wantRoot, "browser") {
		t.Fatalf("unexpected browser directory: got %q", got.BrowserDir)
	}
	if got.TickerUserID != "" || got.DataUserID != "" {
		t.Fatalf("unexpected user IDs: ticker %q, data %q", got.TickerUserID, got.DataUserID)
	}
	if got.DefaultUserID != "CC0006" || got.PriceMode != PriceModeQuote {
		t.Fatalf("default user %q, price mode %q", got.DefaultUserID, got.PriceMode)
	}
	if got.AccountMode != AccountModePaper || got.PaperCapital != 1_000_000 || got.PaperSlippageBPS != 0 {
		t.Fatalf("account mode %q, paper capital %v, slippage %v", got.AccountMode, got.PaperCapital, got.PaperSlippageBPS)
	}
	if got.SettingsPath != filepath.Join(home, "tradebot", "settings.json") {
		t.Fatalf("settings path = %q", got.SettingsPath)
	}
}

func TestLoadReadsSettingsAndJoinsOffsets(t *testing.T) {
	home := writeHomeSettings(t, map[string]string{
		"TRADEBOT_LOCAL_STORAGE_ROOT":      "$HOME/custom",
		"TRADEBOT_CREDENTIALS_SQLITE_PATH": "state/users.sqlite3",
		"TRADEBOT_INSTRUMENTS_DUCKDB_PATH": "state/instruments.duckdb",
		"TRADEBOT_CANDLES_DUCKDB_PATH":     "state/candles.duckdb",
		"TRADEBOT_TICKER_USER_ID":          "  CC0006  ",
		"TRADEBOT_DATA_USER_ID":            " DA0001 ",
		"TRADEBOT_DEFAULT_USER_ID":         "AB1234",
		"TRADEBOT_PRICE_MODE":              "Ticker",
		"TRADEBOT_NATS_URL":                "nats://127.0.0.1:4222",
		"TRADEBOT_ACCOUNT_MODE":            "LIVE",
		"TRADEBOT_PAPER_CAPITAL":           "250000",
		"TRADEBOT_PAPER_SLIPPAGE_BPS":      "5",
	})

	got, err := Load()
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	root := filepath.Join(home, "custom")
	if got.StorageRoot != root {
		t.Fatalf("unexpected root: %q", got.StorageRoot)
	}
	if got.CredentialsPath != filepath.Join(root, "state", "users.sqlite3") {
		t.Fatalf("unexpected database path: %q", got.CredentialsPath)
	}
	if got.InstrumentsPath != filepath.Join(root, "state", "instruments.duckdb") {
		t.Fatalf("unexpected instruments database path: %q", got.InstrumentsPath)
	}
	if got.CandlesPath != filepath.Join(root, "state", "candles.duckdb") {
		t.Fatalf("unexpected candles database path: %q", got.CandlesPath)
	}
	if got.TickerUserID != "CC0006" || got.DataUserID != "DA0001" || got.DefaultUserID != "AB1234" || got.PriceMode != PriceModeTicker {
		t.Fatalf("ticker %q, data %q, default %q, price mode %q", got.TickerUserID, got.DataUserID, got.DefaultUserID, got.PriceMode)
	}
	if got.AccountMode != AccountModeLive || got.PaperCapital != 250000 || got.PaperSlippageBPS != 5 {
		t.Fatalf("account mode %q, paper capital %v, slippage %v", got.AccountMode, got.PaperCapital, got.PaperSlippageBPS)
	}
	if url := got.Settings.Get("TRADEBOT_NATS_URL"); url != "nats://127.0.0.1:4222" {
		t.Fatalf("NATS URL = %q", url)
	}
	if _, err := os.Stat(got.CredentialsPath); !os.IsNotExist(err) {
		t.Fatalf("config unexpectedly created the database: %v", err)
	}
}

func TestLoadRejectsBadSettings(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"absolute credentials path": {"TRADEBOT_CREDENTIALS_SQLITE_PATH": "/var/credentials.sqlite3"},
		"instruments off the root":  {"TRADEBOT_INSTRUMENTS_DUCKDB_PATH": "../instruments.duckdb"},
		"unknown price mode":        {"TRADEBOT_PRICE_MODE": "smoke-signals"},
		"unknown account mode":      {"TRADEBOT_ACCOUNT_MODE": "demo"},
		"no paper capital":          {"TRADEBOT_PAPER_CAPITAL": "0"},
		"negative slippage":         {"TRADEBOT_PAPER_SLIPPAGE_BPS": "-1"},
	} {
		writeHomeSettings(t, values)
		if _, err := Load(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
