package kiteconnect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"github.com/subosito/gotenv"
)

// Config holds local settings shared by the command-line tools.
//
// Values come from TRADEBOT_* environment variables, optionally loaded from a
// .env file. Process environment values always win over the .env file.
type Config struct {
	// StorageRoot is TRADEBOT_LOCAL_STORAGE_ROOT, default ~/tradebot.
	StorageRoot string
	// CredentialsPath is TRADEBOT_CREDENTIALS_SQLITE_PATH, default <StorageRoot>/credentials.sqlite3.
	CredentialsPath string
	// InstrumentsPath is TRADEBOT_INSTRUMENTS_DUCKDB_PATH, default <StorageRoot>/instruments.duckdb.
	InstrumentsPath string
	// TickerUserID is TRADEBOT_TICKER_USER_ID, the Zerodha account the ticker streams for.
	TickerUserID string
}

// LoadConfig reads Config from the environment. It first loads the working
// directory's .env, or the .env beside the nearest parent go.mod.
func LoadConfig() (Config, error) {
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}

	v := viper.New()
	v.SetEnvPrefix("TRADEBOT")
	v.AutomaticEnv()

	v.SetDefault("local_storage_root", "~/tradebot")
	root := expandHome(v.GetString("local_storage_root"), home)

	v.SetDefault("credentials_sqlite_path", filepath.Join(root, "credentials.sqlite3"))
	v.SetDefault("instruments_duckdb_path", filepath.Join(root, "instruments.duckdb"))

	return Config{
		StorageRoot:     filepath.Clean(root),
		CredentialsPath: filepath.Clean(expandHome(v.GetString("credentials_sqlite_path"), home)),
		InstrumentsPath: filepath.Clean(expandHome(v.GetString("instruments_duckdb_path"), home)),
		TickerUserID:    strings.TrimSpace(v.GetString("ticker_user_id")),
	}, nil
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

// loadDotEnv loads the working directory's .env, or the .env beside the
// nearest parent go.mod. Existing process environment values always win.
func loadDotEnv() error {
	path, err := findDotEnv()
	if err != nil || path == "" {
		return err
	}
	if err := gotenv.Load(path); err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}

func findDotEnv() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if found, err := isRegularFile(filepath.Join(directory, ".env")); err != nil || found {
		return filepath.Join(directory, ".env"), err
	}

	for parent := filepath.Dir(directory); parent != directory; parent = filepath.Dir(directory) {
		directory = parent
		isModuleRoot, err := isRegularFile(filepath.Join(directory, "go.mod"))
		if err != nil {
			return "", err
		}
		if !isModuleRoot {
			continue
		}
		path := filepath.Join(directory, ".env")
		if found, err := isRegularFile(path); err != nil || !found {
			return "", err
		}
		return path, nil
	}
	return "", nil
}

func isRegularFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	return true, nil
}
