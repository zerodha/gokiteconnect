package main

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// holdTime is how long ? must be held for its release to close the keys
// modal; a quicker tap leaves it open.
const holdTime = 300 * time.Millisecond

// generalKeys work on every screen.
var generalKeys = bindings("⇧←→", "tabs", "`", "users", "^P", "paper / live", "?", "hold or tap for keys", "^C", "quit")

// keysOverlay lists the keys of the screen under it. Holding ? shows it
// until ? is let go, in terminals that report key releases; tapping ?
// leaves it open until ?, Esc or Q.
type keysOverlay struct {
	opened time.Time
}

// openKeys shows the keys modal.
func (a *app) openKeys() tea.Cmd {
	a.overlay = &keysOverlay{opened: time.Now()}
	return nil
}

// typing reports whether a text field on the screen has the keys, so ?
// types a ? instead of opening the keys modal.
func (a *app) typing() bool {
	switch a.screen {
	case screenInstruments:
		return a.explorer.searchFocused
	case screenSettings:
		if a.settings == nil {
			return false
		}
		current := a.settings.form.current()
		return current != nil && current.kind == fieldInput
	}
	return false
}

// isHelpKey reports whether a key is ?, matching its release too, which
// some terminals report as / with or without Shift.
func isHelpKey(key tea.Key) bool {
	return key.Text == "?" || key.Code == '?' || key.Code == '/' && key.Mod&tea.ModShift != 0 || key.ShiftedCode == '?'
}

func (k *keysOverlay) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case isHelpKey(msg.Key()):
		// A held ? repeats; only a fresh press closes the modal.
		if !msg.IsRepeat {
			a.closeOverlay()
		}
	case msg.String() == "esc", msg.String() == "q", msg.String() == "Q":
		a.closeOverlay()
	}
	return nil
}

// keyReleased closes the keys modal when the ? that opened it is let go
// after a hold.
func (a *app) keyReleased(msg tea.KeyReleaseMsg) tea.Cmd {
	keys, ok := a.overlay.(*keysOverlay)
	if !ok {
		return nil
	}
	key := msg.Key()
	// The release of a shifted key can come without Shift, when Shift is
	// let go first.
	if isHelpKey(key) || key.Code == '/' {
		if time.Since(keys.opened) >= holdTime {
			a.closeOverlay()
		}
	}
	return nil
}

func (k *keysOverlay) view(a *app, w, h int) string {
	screen := a.screenKeys()
	general := map[string]bool{}
	for index := 0; index+1 < len(generalKeys); index += 2 {
		general[generalKeys[index]] = true
	}
	var own []string
	for index := 0; index+1 < len(screen); index += 2 {
		if !general[screen[index]] {
			own = append(own, screen[index], screen[index+1])
		}
	}
	keyW := 0
	for _, pairs := range [][]string{own, generalKeys} {
		for index := 0; index < len(pairs); index += 2 {
			keyW = max(keyW, ansi.StringWidth(pairs[index]))
		}
	}
	rows := func(pairs []string) []string {
		var lines []string
		for index := 0; index+1 < len(pairs); index += 2 {
			lines = append(lines, styleKey.Render(padRight(pairs[index], keyW))+"  "+styleMuted.Render(pairs[index+1]))
		}
		return lines
	}
	lines := rows(own)
	lines = append(lines, "", styleHeader.Render("Everywhere"))
	lines = append(lines, rows(generalKeys)...)

	// Too many keys for the screen's height go into a second column.
	columns := [][]string{lines}
	if room := h - 6; len(lines) > room && room > 0 {
		half := (len(lines) + 1) / 2
		columns = [][]string{lines[:half], lines[half:]}
	}
	colW := 0
	for _, line := range lines {
		colW = max(colW, ansi.StringWidth(line))
	}
	var body []string
	for row := range len(columns[0]) {
		var cells []string
		for _, column := range columns {
			cell := ""
			if row < len(column) {
				cell = column[row]
			}
			cells = append(cells, padRight(cell, colW))
		}
		body = append(body, strings.TrimRight(strings.Join(cells, "    "), " "))
	}
	width := min(len(columns)*colW+(len(columns)-1)*4+4, w-2)
	return modal("Keys", styleFaint.Render(a.screenName()), strings.Join(body, "\n"), max(width, 30))
}

// screenName names the screen, and its tab, for the keys modal.
func (a *app) screenName() string {
	switch a.screen {
	case screenPortfolio:
		p := a.portfolio
		if p.focus == holdingsPanel {
			return "Portfolio · " + holdingsTabNames[p.holdingsTab]
		}
		return "Portfolio · " + orderTabNames[p.tab]
	case screenInstruments:
		return "Watchlists"
	case screenHistory:
		return "Admin · " + adminTabs[a.admin.tab].label
	case screenSettings:
		return "Settings"
	}
	return "Users"
}

// screenKeys are the keys of the screen, and the tab, that is showing.
func (a *app) screenKeys() []string {
	switch a.screen {
	case screenPortfolio:
		keys := bindings("+", "order", "T", "alert", "O", "chain", "B", "baskets", "[ ]", "panel tabs: holdings / funds, orders / baskets / alerts")
		switch a.portfolio.tab {
		case tabBaskets:
			keys = append(keys, "Enter", "collapse basket / open order", "B", "open in Admin › Baskets")
		case tabAlerts:
			keys = append(keys, "Enter", "edit alert", "D", "delete alert")
		}
		keys = append(keys, "M/X", "modify / cancel order · X on a group exits it", "Tab", "next table", "Enter", "collapse / raw order", "J/K", "scroll raw", "R", "reload", "U", "switch user", "P", "paper / live")
		if a.mode == broker.Paper {
			keys = append(keys, "⇧Z", "reset paper")
		}
		return append(keys, "W", "watchlists", "H", "admin", "Esc", "users")
	case screenInstruments:
		return a.explorerHelp()
	case screenHistory:
		return a.adminHelp()
	case screenSettings:
		return a.settingsHelp()
	}
	return bindings("Enter", "portfolio", "W", "watchlists", "H", "admin", "A", "add", "E", "edit", "D", "delete", "F", "refresh session", "B", "open Kite web", "R", "reload", "P", "paper / live", "⇧S", "sudo")
}
