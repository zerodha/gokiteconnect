package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPortfolioResetsThePaperAccount(t *testing.T) {
	store, _ := niftyMarket()
	deps := explorerDeps(t, store, nil, nil, &fakePlacer{})
	var mu sync.Mutex
	var reset []string
	deps.resetPaper = func(_ context.Context, userID string) error {
		mu.Lock()
		defer mu.Unlock()
		reset = append(reset, userID)
		return nil
	}
	resets := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), reset...) }
	h := newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.press("enter")
	h.waitFor("the portfolio", func() bool { return h.a.screen == screenPortfolio && !h.a.portfolio.loading })
	// The portfolio's help line is long: it fits a wide terminal.
	h.dispatch(tea.WindowSizeMsg{Width: 260, Height: 40})
	if !strings.Contains(h.screen(), "reset paper") {
		t.Errorf("the help lacks the reset:\n%s", h.screen())
	}

	// ⇧Z asks first; Cancel keeps the account.
	h.press("Z")
	if !strings.Contains(h.screen(), "Reset AB1234's paper account?") {
		t.Fatalf("⇧Z did not ask:\n%s", h.screen())
	}
	h.press("enter")
	if !h.idle() || len(resets()) != 0 {
		t.Fatalf("Cancel reset the account: %v", resets())
	}
	h.press("Z", "left", "enter")
	h.waitFor("the reset", func() bool { return len(resets()) == 1 && h.idle() && !h.a.portfolio.loading })
	if got := resets(); got[0] != "AB1234" || !strings.Contains(h.screen(), "Reset AB1234's paper account") {
		t.Fatalf("reset %v:\n%s", got, h.screen())
	}

	// A live account is never reset.
	h.press("P", "Z")
	if !h.idle() || len(resets()) != 1 || !strings.Contains(h.screen(), "Only paper accounts reset") || strings.Contains(h.screen(), "reset paper") {
		t.Fatalf("⇧Z in live mode:\n%s", h.screen())
	}
}
