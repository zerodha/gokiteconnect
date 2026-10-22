package kiteweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

var testSession = Session{UserID: "CC0006", EncToken: "enc", KFSession: "session", PublicToken: "public"}

func TestOpenRejectsIncompleteSession(t *testing.T) {
	session := testSession
	session.KFSession = " "
	if err := Open(context.Background(), session, Options{ProfileDir: t.TempDir()}); !errors.Is(err, ErrIncompleteSession) {
		t.Fatalf("Open() error = %v, want ErrIncompleteSession", err)
	}
}

func TestSessionCookies(t *testing.T) {
	values := map[string]string{}
	for _, cookie := range testSession.Cookies() {
		if cookie.Domain != Domain || !cookie.Secure || cookie.Path != "/" {
			t.Errorf("cookie %s = %+v", cookie.Name, cookie)
		}
		values[cookie.Name] = cookie.Value
	}
	want := map[string]string{"enctoken": "enc", "kf_session": "session", "public_token": "public", "user_id": "CC0006"}
	for name, value := range want {
		if values[name] != value {
			t.Errorf("cookie %s = %q, want %q", name, values[name], value)
		}
	}
}

func TestCheck(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		body   string
		want   error
		fails  bool
	}{
		"accepted":     {http.StatusOK, `{"status":"success","data":{"user_id":"CC0006"}}`, nil, false},
		"refused":      {http.StatusForbidden, `{"status":"error","error_type":"TokenException"}`, ErrSessionRefused, true},
		"another user": {http.StatusOK, `{"status":"success","data":{"user_id":"AB1234"}}`, nil, true},
		"server error": {http.StatusBadGateway, `<html>`, nil, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/user/profile" || r.Header.Get("X-CSRFTOKEN") != "public" {
				t.Errorf("%s: request %s with CSRF %q", name, r.URL.Path, r.Header.Get("X-CSRFTOKEN"))
			}
			for _, cookie := range []string{"enctoken", "kf_session", "public_token", "user_id"} {
				if _, err := r.Cookie(cookie); err != nil {
					t.Errorf("%s: no %s cookie", name, cookie)
				}
			}
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(test.body))
		}))
		webBaseURI = server.URL
		err := Check(context.Background(), server.Client(), testSession)
		server.Close()
		if (err != nil) != test.fails || (test.want != nil && !errors.Is(err, test.want)) {
			t.Errorf("%s: Check() error = %v, want %v (fails %v)", name, err, test.want, test.fails)
		}
	}
	webBaseURI = "https://" + Domain
}

// closeBrowser closes the browser at wsURL over a connection of its own:
// chromedp refuses Browser.close on the browsers it attaches to.
func closeBrowser(t *testing.T, wsURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		t.Logf("close browser: %v", err)
		return
	}
	defer conn.Close()
	if err := conn.Write(ctx, &cdproto.Message{ID: 1, Method: browser.CommandClose}); err != nil {
		t.Logf("close browser: %v", err)
		return
	}
	var reply cdproto.Message
	_ = conn.Read(ctx, &reply)
}

// chromePath finds Chrome for the browser test, or skips it.
func chromePath(t *testing.T) string {
	t.Helper()
	candidates := []string{"google-chrome", "chromium", "chromium-browser"}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}, candidates...)
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("Chrome not found")
	return ""
}

// TestOpenKeepsBrowserOpen opens a page twice in one profile and checks,
// from a separate connection, that the tabs stay open with the cookies set.
func TestOpenKeepsBrowserOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	exe := chromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<title>" + strings.TrimPrefix(r.URL.Path, "/") + "</title>"))
	}))
	defer server.Close()
	profile := filepath.Join(t.TempDir(), "CC0006")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	opts := Options{ProfileDir: profile, URL: server.URL + "/first", ExecPath: exe, Headless: true}
	if err := Open(ctx, testSession, opts); err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	wsURL, ok := runningBrowser(profile)
	if !ok {
		t.Fatal("no DevToolsActivePort after Open")
	}
	actx, cancelAlloc := remote.NewAllocator(ctx, wsURL)
	defer cancelAlloc()
	bctx, cancelBrowser := chromedp.NewContext(actx, chromedp.WithDetachOnCancel())
	defer cancelBrowser()
	if err := chromedp.Do(bctx); err != nil {
		t.Fatalf("attach to kept browser: %v", err)
	}
	b := chromedp.FromContext(bctx).Browser
	t.Cleanup(func() { closeBrowser(t, wsURL) })

	opts.URL = server.URL + "/second"
	if err := Open(ctx, testSession, opts); err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	if again, _ := runningBrowser(profile); again != wsURL {
		t.Fatalf("second Open() started another browser: %s, want %s", again, wsURL)
	}

	targets, err := cdp.Call(ctx, b, target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		t.Fatalf("GetTargets() error = %v", err)
	}
	open := map[string]bool{}
	for _, info := range targets.TargetInfos {
		open[strings.TrimPrefix(info.URL, server.URL)] = true
	}
	if !open["/first"] || !open["/second"] {
		t.Fatalf("open tabs = %v, want /first and /second", open)
	}

	cookies, err := cdp.Call(ctx, b, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		t.Fatalf("GetCookies() error = %v", err)
	}
	found := map[string]string{}
	for _, cookie := range cookies.Cookies {
		if cookie.Domain == Domain {
			found[cookie.Name] = cookie.Value
		}
	}
	if found["enctoken"] != "enc" || found["kf_session"] != "session" || found["public_token"] != "public" || found["user_id"] != "CC0006" {
		t.Fatalf("cookies on %s = %v", Domain, found)
	}
}
