// Package kiteweb opens Kite web in Chrome, signed in with a stored web
// session, so that no login is needed.
//
// Kite web keeps its session in the cookies enctoken, kf_session,
// public_token and user_id of kite.zerodha.com. Open starts Chrome with a
// profile of its own, sets those cookies over the DevTools protocol and opens
// the dashboard. Chrome stays open after the program exits, and a later Open
// with the same profile adds a tab to the running browser.
package kiteweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

const (
	// Domain is the host the session cookies belong to.
	Domain = "kite.zerodha.com"
	// DashboardURL is the page Open shows.
	DashboardURL = "https://" + Domain + "/dashboard"
)

// ErrSessionRefused indicates that Kite web's pages no longer accept the
// session, though its enctoken may still work with the /oms APIs.
var ErrSessionRefused = errors.New("kiteweb: Kite web refuses the session")

// ErrIncompleteSession indicates a session without one of its four values.
var ErrIncompleteSession = errors.New("kiteweb: session needs a user ID, enctoken, kf_session and public_token")

// Session is a Kite web session.
type Session struct {
	UserID      string
	EncToken    string
	KFSession   string
	PublicToken string
}

func (s Session) complete() bool {
	for _, value := range []string{s.UserID, s.EncToken, s.KFSession, s.PublicToken} {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

// Cookies are the session's cookies on Domain.
func (s Session) Cookies() []*network.CookieParam {
	cookie := func(name, value string) *network.CookieParam {
		return &network.CookieParam{
			Name: name, Value: value, Domain: Domain, Path: "/",
			Secure: true, SameSite: network.CookieSameSiteLax,
		}
	}
	return []*network.CookieParam{
		cookie("enctoken", s.EncToken),
		cookie("kf_session", s.KFSession),
		cookie("public_token", s.PublicToken),
		cookie("user_id", s.UserID),
	}
}

// Options configure Open.
type Options struct {
	// ProfileDir is the Chrome profile, kept between runs. Give each user
	// a profile of their own.
	ProfileDir string
	// URL is the page to open, DashboardURL when empty.
	URL string
	// ExecPath is the browser to start; chromedp finds Chrome when empty.
	ExecPath string
	// Headless hides the window; for tests.
	Headless bool
}

// Open shows Kite web signed in with session, in the browser of
// opts.ProfileDir. It starts that browser when it is not running.
func Open(ctx context.Context, session Session, opts Options) error {
	if !session.complete() {
		return ErrIncompleteSession
	}
	if strings.TrimSpace(opts.ProfileDir) == "" {
		return errors.New("kiteweb: no profile directory")
	}
	if opts.URL == "" {
		opts.URL = DashboardURL
	}
	if err := os.MkdirAll(opts.ProfileDir, 0o700); err != nil {
		return fmt.Errorf("kiteweb: create profile: %w", err)
	}

	// A browser of this profile that runs already gets a new tab.
	if wsURL, ok := runningBrowser(opts.ProfileDir); ok {
		actx, cancel := remote.NewAllocator(ctx, wsURL)
		defer cancel()
		err := show(actx, session, opts.URL, chromedp.WithDetachOnCancel())
		if err == nil {
			return nil
		}
		// The address was stale: the browser has gone. Start a new one.
		_ = os.Remove(filepath.Join(opts.ProfileDir, "DevToolsActivePort"))
	}

	options := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(opts.ProfileDir),
		chromedp.KeepOpen,
		remote.WebSocket,
	}
	if opts.Headless {
		options = append(options, chromedp.Headless)
	}
	if opts.ExecPath != "" {
		options = append(options, chromedp.ExecPath(opts.ExecPath))
	}
	actx, cancel := chromedp.NewExecAllocator(ctx, options...)
	defer cancel()
	return show(actx, session, opts.URL)
}

// show sets the session's cookies and opens url in a tab of the browser of
// the allocator context actx.
func show(actx context.Context, session Session, url string, opts ...chromedp.ContextOption) error {
	tctx, cancel := chromedp.NewContext(actx, opts...)
	defer cancel()
	err := chromedp.Do(tctx,
		func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
			_, err := cdp.Call(ctx, t, network.SetCookies, network.SetCookiesParams{Cookies: session.Cookies()})
			return chromedp.Void{}, err
		},
		chromedp.Navigate(url),
	)
	if err != nil {
		return fmt.Errorf("kiteweb: open %s: %w", url, err)
	}
	return nil
}

// webBaseURI is Kite web, which tests replace.
var webBaseURI = "https://" + Domain

// Check asks Kite web for the user's profile as its pages do, with the
// cookies and the public token, and reports ErrSessionRefused when Kite
// refuses them. Kite web sends a refused session to its login page.
func Check(ctx context.Context, client *http.Client, session Session) error {
	if !session.complete() {
		return ErrIncompleteSession
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webBaseURI+"/api/user/profile", nil)
	if err != nil {
		return fmt.Errorf("kiteweb: check session: %w", err)
	}
	req.Header.Set("X-Kite-Version", "3.0.0")
	req.Header.Set("X-CSRFTOKEN", session.PublicToken)
	for _, cookie := range session.Cookies() {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("kiteweb: check session: %w", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Status    string `json:"status"`
		ErrorType string `json:"error_type"`
		Data      struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope)
	switch {
	case resp.StatusCode == http.StatusForbidden || envelope.ErrorType == "TokenException":
		return ErrSessionRefused
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("kiteweb: check session: HTTP %d", resp.StatusCode)
	case decodeErr != nil || envelope.Status != "success":
		return fmt.Errorf("kiteweb: check session: unexpected response (status %q)", envelope.Status)
	case envelope.Data.UserID != "" && !strings.EqualFold(envelope.Data.UserID, session.UserID):
		return fmt.Errorf("kiteweb: session belongs to %s, not %s", envelope.Data.UserID, session.UserID)
	}
	return nil
}

// runningBrowser is the DevTools address that a browser of profileDir wrote,
// when it wrote one. The browser may have exited since.
func runningBrowser(profileDir string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort"))
	if err != nil {
		return "", false
	}
	lines := strings.Fields(string(raw))
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "/devtools/browser/") {
		return "", false
	}
	return "ws://127.0.0.1:" + lines[0] + lines[1], true
}
