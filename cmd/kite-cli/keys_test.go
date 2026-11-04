package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestKeysModalOpensOnHoldOrTap(t *testing.T) {
	store, quotes := niftyMarket()
	h := newHarness(t, explorerDeps(t, store, quotes, nil, &fakePlacer{}))
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })

	// The help bar is gone: the status line, with its ? hint, is last.
	lines := strings.Split(h.screen(), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "USERS") || !strings.HasSuffix(strings.TrimSpace(last), "? keys") {
		t.Fatalf("last line %q, want the status bar ending in the ? hint", last)
	}
	if strings.Contains(h.screen(), "refresh session") {
		t.Fatalf("the screen still shows the keys:\n%s", h.screen())
	}

	// A tap opens the modal, and the quick release leaves it open.
	h.press("?")
	if _, ok := h.a.overlay.(*keysOverlay); !ok || !strings.Contains(h.screen(), "refresh session") || !strings.Contains(h.screen(), "Everywhere") {
		t.Fatalf("? did not show the users screen's keys:\n%s", h.screen())
	}
	h.dispatch(tea.KeyReleaseMsg{Code: '/', Mod: tea.ModShift})
	if h.idle() {
		t.Fatal("the release of a tapped ? closed the modal")
	}
	// The next ? closes it.
	h.press("?")
	if !h.idle() {
		t.Fatal("a second ? left the modal open")
	}

	// Held, its repeats keep the modal open and its release closes it, with
	// or without Shift.
	for _, release := range []tea.KeyReleaseMsg{{Code: '/', Mod: tea.ModShift}, {Code: '/'}, {Code: '?', Text: "?"}} {
		h.press("?")
		h.dispatch(tea.KeyPressMsg{Code: '?', Text: "?", IsRepeat: true})
		if h.idle() {
			t.Fatal("a repeat of a held ? closed the modal")
		}
		h.a.overlay.(*keysOverlay).opened = time.Now().Add(-time.Second)
		h.dispatch(release)
		if !h.idle() {
			t.Fatalf("releasing a held ? (%v) left the modal open", release)
		}
	}

	// Esc closes it too.
	h.press("?", "esc")
	if !h.idle() {
		t.Fatal("Esc left the modal open")
	}
}

func TestKeysModalFollowsTheScreenAndLeavesTyping(t *testing.T) {
	store, quotes := niftyMarket()
	deps := explorerDeps(t, store, quotes, nil, &fakePlacer{})
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("enter")
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })

	// The portfolio's keys follow its tab.
	h.press("]", "]", "?")
	if screen := h.screen(); !strings.Contains(screen, "Portfolio · Alerts") || !strings.Contains(screen, "delete alert") {
		t.Fatalf("the keys lack the Alerts tab's:\n%s", screen)
	}
	h.press("?")

	// In the search, ? is typed.
	openExplorer(h)
	h.press("/", "?")
	if !h.idle() || h.a.explorer.search.Value() != "?" {
		t.Fatalf("? in the search opened the modal or was lost: %q", h.a.explorer.search.Value())
	}
}
