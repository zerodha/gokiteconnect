package broker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for value, want := range map[string]Mode{"": Paper, "paper": Paper, "live": Live} {
		if got, err := ParseMode(value); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	if _, err := ParseMode("LIVE"); err == nil {
		t.Error("ParseMode(LIVE) succeeded; modes are exact")
	}
}

func TestAccountsOpenTheModeAsked(t *testing.T) {
	errOpened := errors.New("opened")
	opened := ""
	opener := func(name string) Opener {
		return func(_ context.Context, userID string) (Account, error) {
			opened = name + ":" + userID
			return nil, errOpened
		}
	}
	accounts := Accounts{Paper: opener("paper")}
	if _, err := accounts.Open(context.Background(), "CC0006", Paper); !errors.Is(err, errOpened) || opened != "paper:CC0006" {
		t.Fatalf("Open(paper) = %v, opened %q", err, opened)
	}
	if _, err := accounts.Open(context.Background(), "CC0006", Live); err == nil || !strings.Contains(err.Error(), "live trading is unavailable") {
		t.Fatalf("Open(live) without an opener error = %v", err)
	}
	if _, err := accounts.Open(context.Background(), "CC0006", "demo"); err == nil {
		t.Fatal("Open(demo) succeeded")
	}
}
