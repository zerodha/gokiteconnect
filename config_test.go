package kiteconnect

import (
	"os"
	"path/filepath"
	"testing"
)

var configEnvironmentKeys = []string{
	"TRADEBOT_LOCAL_STORAGE_ROOT",
	"TRADEBOT_CREDENTIALS_SQLITE_PATH",
	"TRADEBOT_INSTRUMENTS_DUCKDB_PATH",
	"TRADEBOT_TICKER_USER_ID",
}

func TestLoadConfigUsesTradebotHomeDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRADEBOT_LOCAL_STORAGE_ROOT", "")
	t.Setenv("TRADEBOT_CREDENTIALS_SQLITE_PATH", "")
	t.Setenv("TRADEBOT_INSTRUMENTS_DUCKDB_PATH", "")
	t.Setenv("TRADEBOT_TICKER_USER_ID", "")

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
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
	if got.TickerUserID != "" {
		t.Fatalf("unexpected ticker user ID: got %q", got.TickerUserID)
	}
}

func TestLoadConfigEnvironmentOverridesExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRADEBOT_LOCAL_STORAGE_ROOT", "$HOME/custom")
	t.Setenv("TRADEBOT_CREDENTIALS_SQLITE_PATH", "~/state/users.sqlite3")
	t.Setenv("TRADEBOT_INSTRUMENTS_DUCKDB_PATH", "~/state/instruments.duckdb")
	t.Setenv("TRADEBOT_TICKER_USER_ID", "  CC0006  ")

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}
	if got.StorageRoot != filepath.Join(home, "custom") {
		t.Fatalf("unexpected overridden root: %q", got.StorageRoot)
	}
	if got.CredentialsPath != filepath.Join(home, "state", "users.sqlite3") {
		t.Fatalf("unexpected overridden database path: %q", got.CredentialsPath)
	}
	if got.InstrumentsPath != filepath.Join(home, "state", "instruments.duckdb") {
		t.Fatalf("unexpected overridden instruments database path: %q", got.InstrumentsPath)
	}
	if got.TickerUserID != "CC0006" {
		t.Fatalf("unexpected ticker user ID: %q", got.TickerUserID)
	}
	if _, err := os.Stat(got.CredentialsPath); !os.IsNotExist(err) {
		t.Fatalf("config unexpectedly created the database: %v", err)
	}
	if _, err := os.Stat(got.InstrumentsPath); !os.IsNotExist(err) {
		t.Fatalf("config unexpectedly created the instruments database: %v", err)
	}
}

func TestLoadConfigRootOverrideDerivesDatabasePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRADEBOT_LOCAL_STORAGE_ROOT", filepath.Join(home, "custom"))
	t.Setenv("TRADEBOT_CREDENTIALS_SQLITE_PATH", "")
	t.Setenv("TRADEBOT_INSTRUMENTS_DUCKDB_PATH", "")
	t.Setenv("TRADEBOT_TICKER_USER_ID", "")

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}
	want := filepath.Join(home, "custom", "credentials.sqlite3")
	if got.CredentialsPath != want {
		t.Fatalf("unexpected derived database path: got %q want %q", got.CredentialsPath, want)
	}
	wantInstruments := filepath.Join(home, "custom", "instruments.duckdb")
	if got.InstrumentsPath != wantInstruments {
		t.Fatalf("unexpected derived instruments database path: got %q want %q", got.InstrumentsPath, wantInstruments)
	}
}

func TestLoadConfigDiscoversDotEnvFromChildDirectory(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	commandDirectory := filepath.Join(root, "cmd", "ticker")
	if err := os.MkdirAll(commandDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dotEnv := []byte("TRADEBOT_LOCAL_STORAGE_ROOT=$HOME/dotenv-state\n" +
		"TRADEBOT_CREDENTIALS_SQLITE_PATH=$HOME/dotenv-state/users.sqlite3\n" +
		"TRADEBOT_INSTRUMENTS_DUCKDB_PATH=$HOME/dotenv-state/instruments.duckdb\n" +
		"TRADEBOT_TICKER_USER_ID=DOTENV01\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), dotEnv, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(commandDirectory)
	t.Setenv("HOME", home)
	for _, key := range configEnvironmentKeys {
		unsetEnvironment(t, key)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig returned an error: %v", err)
	}
	stateDirectory := filepath.Join(home, "dotenv-state")
	if got.StorageRoot != stateDirectory {
		t.Fatalf("storage root=%q", got.StorageRoot)
	}
	if got.CredentialsPath != filepath.Join(stateDirectory, "users.sqlite3") {
		t.Fatalf("credentials path=%q", got.CredentialsPath)
	}
	if got.InstrumentsPath != filepath.Join(stateDirectory, "instruments.duckdb") {
		t.Fatalf("instruments path=%q", got.InstrumentsPath)
	}
	if got.TickerUserID != "DOTENV01" {
		t.Fatalf("ticker user ID=%q", got.TickerUserID)
	}
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		if present {
			err = os.Setenv(key, value)
		} else {
			err = os.Unsetenv(key)
		}
		if err != nil {
			t.Errorf("restore %s: %v", key, err)
		}
	})
}
