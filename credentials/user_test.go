package credentials

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
)

func validCredentials() Credentials {
	return Credentials{
		UserID:     "user",
		Password:   "password",
		TOTPSecret: "secret",
	}
}

func TestNewUserCopiesCredentials(t *testing.T) {
	createdAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	credentials := validCredentials()
	credentials.EncToken = "enc-token"
	credentials.KFSessionToken = "kf-session"
	credentials.PublicToken = "public-token"
	credentials.CreatedAt = createdAt

	user, err := NewUser(credentials)
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	if user.UserID != credentials.UserID || user.EncToken != credentials.EncToken {
		t.Fatal("credentials were not embedded in the user")
	}
	if user.KFSessionToken != credentials.KFSessionToken || user.PublicToken != credentials.PublicToken {
		t.Fatal("web-session tokens were not copied")
	}
	if !user.CreatedAt.Equal(createdAt) {
		t.Fatal("credential metadata was not copied")
	}

	credentials.EncToken = "changed"
	if user.EncToken == credentials.EncToken {
		t.Fatal("the login client retained the caller's credential storage")
	}
}

func TestNewUserValidatesCredentials(t *testing.T) {
	tests := []struct {
		name        string
		credentials Credentials
	}{
		{name: "user ID", credentials: Credentials{Password: "password", TOTPSecret: "secret"}},
		{name: "password", credentials: Credentials{UserID: "user", TOTPSecret: "secret"}},
		{name: "TOTP secret", credentials: Credentials{UserID: "user", Password: "password"}},
		{
			name: "API key",
			credentials: Credentials{
				UserID: "user", Password: "password", TOTPSecret: "secret",
				IsAPIUser: true, APISecret: "api-secret",
			},
		},
		{
			name: "API secret",
			credentials: Credentials{
				UserID: "user", Password: "password", TOTPSecret: "secret",
				IsAPIUser: true, APIKey: "api-key",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewUser(test.credentials); err == nil {
				t.Fatalf("expected missing %s to return an error", test.name)
			}
		})
	}
}

func TestIsAPIUserUsesExplicitFlag(t *testing.T) {
	webCredentials := validCredentials()
	webCredentials.APIKey = "api-key"
	webCredentials.APISecret = "api-secret"
	webUser, err := NewUser(webCredentials)
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	if webUser.isAPIUser() {
		t.Fatal("API credentials must not override IsAPIUser=false")
	}

	apiCredentials := webCredentials
	apiCredentials.IsAPIUser = true
	apiUser, err := NewUser(apiCredentials)
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	if !apiUser.isAPIUser() {
		t.Fatal("IsAPIUser=true should enable API-user mode")
	}
}

func TestGenerateWebSessionTokensCollectsResponseAndJarCookies(t *testing.T) {
	user, err := NewUser(validCredentials())
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	user.loggedIn = true
	user.baseLoginCookies = []*http.Cookie{
		{Name: "enctoken", Value: "enc-token"},
		{Name: "kf_session", Value: "kf-session"},
	}
	kiteURL, _ := url.Parse("https://kite.zerodha.com/")
	user.httpClient.Jar.SetCookies(kiteURL, []*http.Cookie{
		{Name: "public_token", Value: "public-token"},
	})

	if err := user.generateWebSessionTokens(); err != nil {
		t.Fatalf("generateWebSessionTokens returned an error: %v", err)
	}
	if user.EncToken != "enc-token" {
		t.Fatalf("unexpected enctoken: %q", user.EncToken)
	}
	if user.KFSessionToken != "kf-session" {
		t.Fatalf("unexpected KF session token: %q", user.KFSessionToken)
	}
	if user.PublicToken != "public-token" {
		t.Fatalf("unexpected public token: %q", user.PublicToken)
	}
}

func TestGenerateWebSessionTokensDoesNotRequireOptionalTokens(t *testing.T) {
	credentials := validCredentials()
	credentials.KFSessionToken = "stale-kf-session"
	credentials.PublicToken = "stale-public-token"
	user, err := NewUser(credentials)
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	user.loggedIn = true
	user.baseLoginCookies = []*http.Cookie{{Name: "enctoken", Value: "enc-token"}}

	if err := user.generateWebSessionTokens(); err != nil {
		t.Fatalf("optional web-session tokens caused an error: %v", err)
	}
	if user.KFSessionToken != "" || user.PublicToken != "" {
		t.Fatal("stale optional web-session tokens were not cleared")
	}
}

func TestGenerateWebSessionTokensRequiresEncToken(t *testing.T) {
	user, err := NewUser(validCredentials())
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	user.loggedIn = true
	if err := user.generateWebSessionTokens(); err == nil {
		t.Fatal("expected a missing enctoken error")
	}
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (body *trackingBody) Close() error {
	body.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestCloseResponseDrainsAndClosesBody(t *testing.T) {
	reader := strings.NewReader("response")
	body := &trackingBody{Reader: reader}
	closeResponse(&http.Response{Body: body})

	if reader.Len() != 0 {
		t.Fatal("response body was not drained")
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestUserReusesHTTPConnectionPool(t *testing.T) {
	user, err := NewUser(validCredentials())
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	sharedClient := user.kiteHTTPClient
	sharedTransport := user.httpClient.Transport

	user.KiteClient()
	user.KiteClient()

	if user.kiteHTTPClient != sharedClient {
		t.Fatal("KiteClient replaced the shared HTTP client")
	}
	if user.kiteHTTPClient.Transport != sharedTransport {
		t.Fatal("login and Kite clients do not share a connection pool")
	}
}

func TestLoginIfRequiredIgnoresMissingOptionalWebSessionTokens(t *testing.T) {
	credentials := validCredentials()
	credentials.EncToken = "valid-enc-token"
	user, err := NewUser(credentials)
	if err != nil {
		t.Fatalf("NewUser returned an error: %v", err)
	}
	user.kiteHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &trackingBody{Reader: strings.NewReader(
				`{"status":"success","data":{"user_id":"user"}}`,
			)},
			Request: req,
		}, nil
	})

	profile, didLogin, err := user.LoginIfRequired()
	if err != nil {
		t.Fatalf("LoginIfRequired returned an error: %v", err)
	}
	if didLogin {
		t.Fatal("missing optional web-session tokens triggered a login")
	}
	if profile == nil || profile.UserID != "user" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestShouldLoginAfterProfileErrorOnlyForTokenErrors(t *testing.T) {
	if !shouldLoginAfterProfileError(kiteconnect.NewError(kiteconnect.TokenError, "expired", nil)) {
		t.Fatal("token error should trigger login")
	}
	if shouldLoginAfterProfileError(kiteconnect.NewError(kiteconnect.NetworkError, "network", nil)) {
		t.Fatal("network error should not trigger login")
	}
	if shouldLoginAfterProfileError(errors.New("unexpected")) {
		t.Fatal("unknown error should not trigger login")
	}
}
