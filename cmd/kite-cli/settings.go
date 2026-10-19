package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/config"
)

// settingsForm edits the settings file's visible settings, one field per
// setting, labelled by its display name (or Abc Def for TRADEBOT_ABC_DEF).
// Settings with options are drop-downs. The focused setting's help shows
// below the fields.
type settingsForm struct {
	form     *form
	path     string
	settings config.Settings
	// labels maps each field's label back to its key.
	labels map[string]string
	// opened holds each field's value when the dialog opened.
	opened map[string]string
	// help describes the focused setting, below a spacer.
	help, spacer *field
	message      string
}

// openSettings reads the settings file into the settings dialog.
func (a *app) openSettings() tea.Cmd {
	path := a.deps.settingsPath
	if path == "" {
		a.notify(statusWarn("Settings are unavailable"))
		return nil
	}
	settings, err := config.ReadSettings(path)
	if err != nil {
		a.showError(fmt.Errorf("read settings: %w", err))
		return nil
	}
	labelWidth := 0
	for _, setting := range settings {
		if setting.Visible {
			labelWidth = max(labelWidth, len(setting.Label()))
		}
	}
	s := &settingsForm{path: path, settings: settings, labels: map[string]string{}, form: newForm(labelWidth+1, 48, "Save", "Cancel")}
	for _, setting := range settings {
		if !setting.Visible {
			continue
		}
		label := setting.Label()
		s.labels[label] = setting.Key
		// An unset setting shows the default it takes.
		value := settings.Get(setting.Key)
		if spec, known := config.Spec(setting.Key); known && len(spec.Options) > 0 {
			s.form.addSelect(label, spec.Options, max(slices.Index(spec.Options, value), 0))
		} else {
			s.form.addInput(label, value, anyText, false)
		}
	}
	s.opened = map[string]string{}
	for label := range s.labels {
		s.opened[label] = s.form.field(label).value()
	}
	s.spacer = s.form.addNote("", "")
	s.help = s.form.addNote("", "")
	s.form.focusFirst()
	s.refresh()
	a.overlay = s
	return nil
}

// refresh shows the focused setting's help, its default, and what the
// chosen price mode means.
func (s *settingsForm) refresh() {
	s.help.note = ""
	if current := s.form.current(); current != nil {
		if key, ok := s.labels[current.label]; ok {
			for _, setting := range s.settings {
				if setting.Key != key {
					continue
				}
				help := setting.Help
				if setting.Default != "" {
					help = strings.TrimSpace(help + " Default " + setting.Default + ".")
				}
				s.help.note = styleFaint.Render(help)
			}
			if key == "TRADEBOT_PRICE_MODE" && current.value() == config.PriceModeTicker {
				s.help.note += "\n" + styleYellow.Render("Needs ticker-server running; prices fall back to quotes without it.")
			}
		}
	}
	s.help.hidden = s.help.note == ""
	s.spacer.hidden = s.help.hidden
}

// values are the dialog's settings, in the file's order. An emptied field
// goes back to its default.
func (s *settingsForm) values() config.Settings {
	settings := slices.Clone(s.settings)
	for label, key := range s.labels {
		value := s.form.field(label).value()
		if key == "TRADEBOT_DEFAULT_USER_ID" {
			value = strings.ToUpper(value)
		}
		settings.Set(key, value)
	}
	return settings
}

// edited reports whether any field differs from when the dialog opened.
func (s *settingsForm) edited() bool {
	for label, value := range s.opened {
		if s.form.field(label).value() != value {
			return true
		}
	}
	return false
}

func (s *settingsForm) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		a.closeOverlay()
		return nil
	}
	// Enter on a field saves once something changed; otherwise it moves on.
	pressed, changed := -1, false
	if msg.String() == "enter" && s.form.button() < 0 && s.edited() {
		pressed = 0
	} else {
		pressed, changed = s.form.update(msg)
	}
	if changed {
		s.message = ""
	}
	s.refresh()
	switch pressed {
	case 0:
		if err := config.WriteSettings(s.path, s.values()); err != nil {
			s.message = styleRed.Render(err.Error())
			return nil
		}
		a.closeOverlay()
		a.notify(statusOK("Settings saved to " + homeRelative(s.path) + " · they apply the next time kite-cli starts"))
	case 1:
		a.closeOverlay()
	}
	return nil
}

func (s *settingsForm) view(_ *app, w, _ int) string {
	width := min(100, w-2)
	body := s.form.view(width - 4)
	if s.message != "" {
		body += "\n\n" + s.message
	}
	body += "\n\n" + styleFaint.Render("↑↓ field · ←→ option · Enter saves changes · Esc cancel")
	return modal("Settings", styleFaint.Render(homeRelative(s.path)), body, width)
}

// homeRelative shortens a path under the home directory to ~/….
func homeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if rest, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rest, "..") {
		return filepath.Join("~", rest)
	}
	return path
}
