package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Price modes: prices from polled quotes, or streamed by the ticker.
const (
	PriceModeQuote  = "quote"
	PriceModeTicker = "ticker"
)

// SettingSpec describes a known setting: its key, its label and help, its
// default, whether the settings dialog shows it, and the values it may take
// (any, when Options is empty).
type SettingSpec struct {
	Key         string
	DisplayName string
	Help        string
	Default     string
	Hidden      bool
	Options     []string
}

// SettingSpecs are the known settings, in the order the file and the
// settings dialog list them.
var SettingSpecs = []SettingSpec{
	{Key: "TRADEBOT_LOCAL_STORAGE_ROOT", DisplayName: "Base Path", Default: "~/tradebot",
		Help: "Where local files are stored: the credentials, instrument and candle databases."},
	{Key: "TRADEBOT_CREDENTIALS_SQLITE_PATH", DisplayName: "Credentials Database", Default: "credentials.sqlite3", Hidden: true,
		Help: "The credentials database, relative to the base path."},
	{Key: "TRADEBOT_INSTRUMENTS_DUCKDB_PATH", DisplayName: "Instruments Database", Default: "instruments.duckdb", Hidden: true,
		Help: "The instruments database, relative to the base path."},
	{Key: "TRADEBOT_LISTS_SQLITE_PATH", DisplayName: "Lists Database", Default: "lists.sqlite3", Hidden: true,
		Help: "The instrument lists database, relative to the base path."},
	{Key: "TRADEBOT_CANDLES_DUCKDB_PATH", DisplayName: "Candles Database", Default: "market-data/historical_data.duckdb", Hidden: true,
		Help: "The candles database, relative to the base path."},
	{Key: "TRADEBOT_DEFAULT_USER_ID", DisplayName: "Default User", Default: "CC0006",
		Help: "The account whose portfolio kite-cli opens at start."},
	{Key: "TRADEBOT_PRICE_MODE", DisplayName: "Price Mode", Default: PriceModeQuote, Options: []string{PriceModeQuote, PriceModeTicker},
		Help: "quote polls prices; ticker streams them."},
	{Key: "TRADEBOT_DATA_USER_ID", DisplayName: "Data User",
		Help: "The API user whose session fetches market quotes. Empty picks one with a data subscription."},
	{Key: "TRADEBOT_TICKER_USER_ID", DisplayName: "Ticker User",
		Help: "The account ticker-server streams for. ticker-server needs it."},
	{Key: "TRADEBOT_NATS_URL", DisplayName: "NATS URL",
		Help: "One or more comma-separated NATS server URLs. ticker-server needs it."},
	{Key: "TRADEBOT_NATS_WS_URL", DisplayName: "NATS WebSocket URL", Default: "ws://localhost:9222",
		Help: "The NATS WebSocket kite-cli reads ticks from in price mode ticker."},
	{Key: "TRADEBOT_HEARTBEAT_TOPIC", DisplayName: "Heartbeat Topic", Default: "ticks.subscriptions",
		Help: "The NATS subject ticker-server takes subscription heartbeats on."},
	{Key: "TRADEBOT_SUBSCRIPTION_TTL_SECONDS", DisplayName: "Subscription TTL", Default: "60",
		Help: "Seconds a subscription lives without a heartbeat."},
	{Key: "TRADEBOT_TICKER_TOKEN_POLL_INTERVAL_SECONDS", DisplayName: "Token Poll Interval", Default: "300",
		Help: "Seconds between ticker-server's checks for a new access token."},
	{Key: "TRADEBOT_TICKER_MAPPING_REFRESH_INTERVAL_SECONDS", DisplayName: "Mapping Refresh", Default: "900",
		Help: "Seconds between ticker-server's instrument mapping refreshes."},
	{Key: "TRADEBOT_TICKER_SERVICE_PORT", DisplayName: "Ticker Port", Default: "8082",
		Help: "The port ticker-server's health endpoint listens on."},
}

// Spec is key's SettingSpec, if it is a known setting.
func Spec(key string) (SettingSpec, bool) {
	for _, spec := range SettingSpecs {
		if spec.Key == key {
			return spec, true
		}
	}
	return SettingSpec{}, false
}

// Setting is one entry of the settings file. An empty Value means the
// Default. Visible is whether the settings dialog shows it.
type Setting struct {
	Key         string `json:"-"`
	DisplayName string `json:"display_name"`
	Value       string `json:"value"`
	Default     string `json:"default"`
	Help        string `json:"help"`
	Visible     bool   `json:"visible"`
}

// settingEntry is a Setting as the file has it: a missing visible or
// default takes the spec's.
type settingEntry struct {
	DisplayName string  `json:"display_name"`
	Value       string  `json:"value"`
	Default     *string `json:"default"`
	Help        string  `json:"help"`
	Visible     *bool   `json:"visible"`
}

// Label is how the setting reads in the UI: its display name, or its key
// without the TRADEBOT_ prefix in title case, so TRADEBOT_BASE_PATH reads
// "Base Path".
func (s Setting) Label() string {
	if name := strings.TrimSpace(s.DisplayName); name != "" {
		return name
	}
	return SettingLabel(s.Key)
}

// Settings are the settings file's entries: the known settings in order,
// then any others by key.
type Settings []Setting

// Get is key's value, falling back to its default when empty or unset.
func (s Settings) Get(key string) string {
	for _, setting := range s {
		if setting.Key == key {
			if value := strings.TrimSpace(setting.Value); value != "" {
				return value
			}
			return strings.TrimSpace(setting.Default)
		}
	}
	spec, _ := Spec(key)
	return spec.Default
}

// Set changes key's value, adding it when missing.
func (s *Settings) Set(key, value string) {
	for index := range *s {
		if (*s)[index].Key == key {
			(*s)[index].Value = value
			return
		}
	}
	spec, known := Spec(key)
	if !known {
		spec.Key = key
	}
	added := settingEntry{Value: value}.setting(spec)
	added.DisplayName, added.Help = spec.DisplayName, spec.Help
	*s = append(*s, added)
}

// SettingLabel turns a key into a label: TRADEBOT_ABC_DEF is "Abc Def".
func SettingLabel(key string) string {
	key = strings.TrimPrefix(key, "TRADEBOT_")
	words := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool { return r == '_' })
	for index, word := range words {
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

// SettingsPath is the settings file, settings.json in ~/tradebot. It stays
// there whatever the storage root is, since the root is read from it.
func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "tradebot", "settings.json"), nil
}

// ReadSettings reads the settings file at path, an object of settings by
// key. Every known setting is listed, with its display name, help, default
// and visibility filled in when the file leaves them out. A missing file
// gives the known settings, unset.
func ReadSettings(path string) (Settings, error) {
	entries := map[string]settingEntry{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case len(bytes.TrimSpace(data)) > 0:
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	settings := make(Settings, 0, len(SettingSpecs)+len(entries))
	for _, spec := range SettingSpecs {
		setting := entries[spec.Key].setting(spec)
		delete(entries, spec.Key)
		if setting.DisplayName == "" {
			setting.DisplayName = spec.DisplayName
		}
		if setting.Help == "" {
			setting.Help = spec.Help
		}
		settings = append(settings, setting)
	}
	var others []string
	for key := range entries {
		others = append(others, key)
	}
	sort.Strings(others)
	for _, key := range others {
		settings = append(settings, entries[key].setting(SettingSpec{Key: key}))
	}
	return settings, nil
}

// setting is the entry as a Setting, taking spec's default and visibility
// where the entry has none.
func (e settingEntry) setting(spec SettingSpec) Setting {
	setting := Setting{Key: spec.Key, DisplayName: e.DisplayName, Value: e.Value, Default: spec.Default, Help: e.Help, Visible: !spec.Hidden}
	if e.Default != nil {
		setting.Default = *e.Default
	}
	if e.Visible != nil {
		setting.Visible = *e.Visible
	}
	return setting
}

// WriteSettings replaces the settings file at path with settings, as an
// indented object in their order.
func WriteSettings(path string, settings Settings) error {
	var text bytes.Buffer
	text.WriteString("{")
	for index, setting := range settings {
		key, err := json.Marshal(setting.Key)
		if err != nil {
			return err
		}
		entry, err := json.MarshalIndent(setting, "  ", "  ")
		if err != nil {
			return err
		}
		if index > 0 {
			text.WriteString(",")
		}
		fmt.Fprintf(&text, "\n  %s: %s", key, entry)
	}
	text.WriteString("\n}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Write beside the file and rename, so a failed write keeps the old one.
	temp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(text.Bytes()); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
