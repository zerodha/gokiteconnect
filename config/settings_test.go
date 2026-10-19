package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingLabel(t *testing.T) {
	for key, want := range map[string]string{"TRADEBOT_BASE_PATH": "Base Path", "DEFAULT_USER": "Default User", "TRADEBOT_PRICE_MODE": "Price Mode", "ABC": "Abc", "A__B": "A B"} {
		if got := SettingLabel(key); got != want {
			t.Errorf("SettingLabel(%q) = %q, want %q", key, got, want)
		}
	}
	if got := (Setting{Key: "TRADEBOT_LOCAL_STORAGE_ROOT", DisplayName: "Base Path"}).Label(); got != "Base Path" {
		t.Errorf("display name label = %q", got)
	}
	if got := (Setting{Key: "TRADEBOT_DATA_USER_ID"}).Label(); got != "Data User Id" {
		t.Errorf("derived label = %q", got)
	}
}

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tradebot", "settings.json")
	settings, err := ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != len(SettingSpecs) || settings[0].Label() != "Base Path" || settings[0].Help == "" || settings[0].Value != "" || settings[0].Default != "~/tradebot" || !settings[0].Visible {
		t.Fatalf("missing file read as %+v", settings)
	}
	if got := settings.Get("TRADEBOT_LOCAL_STORAGE_ROOT") + "|" + settings.Get("TRADEBOT_DEFAULT_USER_ID") + "|" + settings.Get("TRADEBOT_PRICE_MODE"); got != "~/tradebot|CC0006|quote" {
		t.Fatalf("defaults = %s", got)
	}

	settings.Set("TRADEBOT_DEFAULT_USER_ID", "AB1234")
	settings.Set("TRADEBOT_DATA_USER_ID", `has "quotes"`)
	if err := WriteSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("settings file mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("written file is not the documented JSON: %v\n%s", err, data)
	}
	if entry := raw["TRADEBOT_DEFAULT_USER_ID"]; entry["value"] != "AB1234" || entry["display_name"] != "Default User" || entry["help"] == "" ||
		entry["default"] != "CC0006" || entry["visible"] != true {
		t.Errorf("default user entry = %v", entry)
	}
	if entry := raw["TRADEBOT_CREDENTIALS_SQLITE_PATH"]; entry["default"] != "credentials.sqlite3" || entry["visible"] != false {
		t.Errorf("credentials entry = %v", entry)
	}
	if entry := raw["TRADEBOT_DATA_USER_ID"]; entry["visible"] != true {
		t.Errorf("an added entry is hidden: %v", entry)
	}

	again, err := ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(settings) {
		t.Fatalf("read back %v, want %v", again, settings)
	}
	for index := range settings {
		if again[index] != settings[index] {
			t.Errorf("entry %d = %+v, want %+v", index, again[index], settings[index])
		}
	}
}

func TestReadSettingsKeepsFileNamesAndOrdersOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	text := `{
  "TRADEBOT_ZETA": {"value": "", "default": "zz", "visible": false},
  "TRADEBOT_PRICE_MODE": {"display_name": "Prices", "value": "ticker", "help": "mine"},
  "TRADEBOT_DEFAULT_USER_ID": {"value": "", "default": "XY0001", "visible": false},
  "TRADEBOT_ALPHA": {"display_name": "", "value": "a", "help": ""}
}`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, setting := range settings {
		keys = append(keys, setting.Key)
	}
	var want []string
	for _, spec := range SettingSpecs {
		want = append(want, spec.Key)
	}
	want = append(want, "TRADEBOT_ALPHA", "TRADEBOT_ZETA")
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for index := range want {
		if keys[index] != want[index] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
	if user := settingByKey(t, settings, "TRADEBOT_DEFAULT_USER_ID"); user.Default != "XY0001" || user.Visible || settings.Get(user.Key) != "XY0001" {
		t.Errorf("the file's default and visibility were not kept: %+v", user)
	}
	if price := settingByKey(t, settings, "TRADEBOT_PRICE_MODE"); price.Label() != "Prices" || price.Help != "mine" || price.Value != "ticker" {
		t.Errorf("the file's own name and help were not kept: %+v", price)
	}
	alpha, zeta := settings[len(settings)-2], settings[len(settings)-1]
	if alpha.Label() != "Alpha" || !alpha.Visible || alpha.Default != "" {
		t.Errorf("unknown key = %+v", alpha)
	}
	if zeta.Visible || settings.Get("TRADEBOT_ZETA") != "zz" {
		t.Errorf("unknown key's own default and visibility = %+v", zeta)
	}

	if err := os.WriteFile(path, []byte(`{"TRADEBOT_X": "not an object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSettings(path); err == nil {
		t.Error("a malformed file was accepted")
	}
}

// settingByKey is settings' entry for key.
func settingByKey(t *testing.T, settings Settings, key string) Setting {
	t.Helper()
	for _, setting := range settings {
		if setting.Key == key {
			return setting
		}
	}
	t.Fatalf("no setting %s", key)
	return Setting{}
}
