package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	kiteticker "github.com/devshoe/gokiteconnect/ticker"
	"github.com/pquerna/otp/totp"
)

// User contains the credentials and state required to log in to Zerodha.
type User struct {
	Credentials

	httpClient       http.Client
	kiteHTTPClient   *http.Client
	loginResponse    loginResponse
	twofaResponse    twofaResponse
	baseLoginCookies []*http.Cookie
	loggedIn         bool
}

// NewUser creates a login client from a copy of credentials.
func NewUser(credentials Credentials) (*User, error) {
	if err := credentials.validate(); err != nil {
		return nil, err
	}

	cookies, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("credentials: create cookie jar: %w", err)
	}
	transport := &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	return &User{
		Credentials: credentials,
		httpClient: http.Client{
			Jar:       cookies,
			Timeout:   15 * time.Second,
			Transport: transport,
		},
		kiteHTTPClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
	}, nil
}

// LoginIfRequired checks whether the current token is valid and logs in when
// Zerodha reports that it has expired. It returns whether a login occurred.
func (user *User) LoginIfRequired() (*kiteconnect.UserProfile, bool, error) {
	// A newly created user has no session token yet. Do not make an
	// unauthenticated profile request first: web users intentionally have no
	// API key, and Kite reports that preflight request as "invalid api_key or
	// token" instead of a token-expired error.
	if !user.hasSessionToken() {
		if err := user.Login(); err != nil {
			return nil, false, err
		}
		profile, err := user.KiteClient().GetUserProfile()
		if err != nil {
			return nil, false, err
		}
		return &profile, true, nil
	}
	if profile, err := user.KiteClient().GetUserProfile(); err == nil {
		return &profile, false, nil
	} else if !shouldLoginAfterProfileError(err) {
		return nil, false, err
	} else if err = user.Login(); err != nil {
		return nil, false, err
	} else if profile, err = user.KiteClient().GetUserProfile(); err != nil {
		return nil, false, err
	} else {
		return &profile, true, nil
	}
}

func (user *User) hasSessionToken() bool {
	if user.IsAPIUser {
		return user.AccessToken != ""
	}
	return user.EncToken != ""
}

func shouldLoginAfterProfileError(err error) bool {
	var kiteErr kiteconnect.Error
	return errors.As(err, &kiteErr) && kiteErr.ErrorType == kiteconnect.TokenError
}

// Login force logs in to Zerodha and updates the resulting session tokens.
func (user *User) Login() error {
	const (
		baseLoginURL = "https://kite.zerodha.com/api/login"
		baseTwofaURL = "https://kite.zerodha.com/api/twofa"
	)

	// UserID + Password Page
	loginFormValues := make(url.Values)
	loginFormValues.Set("user_id", user.UserID)
	loginFormValues.Set("password", user.Password)

	resp, err := user.httpClient.PostForm(baseLoginURL, loginFormValues)
	if err != nil {
		return fmt.Errorf("credentials.Login: post login request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		closeResponse(resp)
		return fmt.Errorf("credentials.Login: login failed with HTTP status %d", resp.StatusCode)
	}
	user.baseLoginCookies = append(user.baseLoginCookies[:0], resp.Cookies()...)
	err = json.NewDecoder(resp.Body).Decode(&user.loginResponse)
	closeResponse(resp)
	if err != nil {
		return fmt.Errorf("credentials.Login: decode login response: %w", err)
	}
	if user.loginResponse.Status != "success" {
		return fmt.Errorf("credentials.Login: login returned status %q", user.loginResponse.Status)
	}

	//
	twofaFormValues := make(url.Values)
	twofaFormValues.Set("user_id", user.UserID)
	twofaFormValues.Set("twofa_value", user.getTOTP())
	twofaFormValues.Set("request_id", user.loginResponse.Data.RequestID)

	resp, err = user.httpClient.PostForm(baseTwofaURL, twofaFormValues)
	if err != nil {
		return fmt.Errorf("credentials.Login: post two-factor authentication request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		closeResponse(resp)
		return fmt.Errorf("credentials.Login: two-factor authentication failed with HTTP status %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&user.twofaResponse)
	user.baseLoginCookies = append(user.baseLoginCookies, resp.Cookies()...)
	closeResponse(resp)
	if err != nil {
		return fmt.Errorf("credentials.Login: decode two-factor authentication response: %w", err)
	}
	if user.twofaResponse.Status != "success" {
		return fmt.Errorf("credentials.Login: two-factor authentication returned status %q", user.twofaResponse.Status)
	}

	user.loggedIn = true
	if err = user.generateWebSessionTokens(); err != nil {
		return fmt.Errorf("credentials.Login: generate web session tokens: %w", err)
	}

	if user.isAPIUser() {
		if err = user.generateAccessToken(); err != nil {
			return fmt.Errorf("credentials.Login: generate access token: %w", err)
		}
	}

	return nil
}

// KiteClient returns a new Kite client using the current credentials.
func (user *User) KiteClient() *kiteconnect.Client {
	c := kiteconnect.New(user.APIKey)
	c.SetHTTPClient(user.kiteHTTPClient)
	if user.isAPIUser() {
		c.SetAccessToken(user.AccessToken)
	} else {
		c.SetEncToken(user.EncToken)
	}
	return c
}

// KiteTicker returns a new Kite ticker using the current credentials.
func (user *User) KiteTicker() *kiteticker.Ticker {
	tkr := kiteticker.New(user.APIKey, user.AccessToken)
	if !user.isAPIUser() {
		tkr.SetEncToken(user.UserID, user.EncToken)
	}
	return tkr
}

// generateWebSessionTokens extracts the current web-session tokens from the cookies returned by a successful login
// Zerodha places `enctoken`, `kf_session`, `public_token` in the cookies
func (user *User) generateWebSessionTokens() error {
	if !user.loggedIn {
		return errors.New("credentials.generateWebSessionTokens: user is not logged in; call Login first")
	}

	kiteURL, err := url.Parse("https://kite.zerodha.com/")
	if err != nil {
		return fmt.Errorf("credentials.generateWebSessionTokens: parse Kite URL: %w", err)
	}

	user.EncToken = ""
	user.KFSessionToken = ""
	user.PublicToken = ""
	cookies := append([]*http.Cookie{}, user.baseLoginCookies...)
	cookies = append(cookies, user.httpClient.Jar.Cookies(kiteURL)...)
	for _, cookie := range cookies {
		switch cookie.Name {
		case "enctoken":
			user.EncToken = cookie.Value
		case "kf_session":
			user.KFSessionToken = cookie.Value
		case "public_token":
			user.PublicToken = cookie.Value
		}
	}

	if user.EncToken == "" {
		return errors.New("credentials.generateWebSessionTokens: enctoken not found")
	}
	return nil
}

// generateAccessToken generates an access token by building a Kite API session.
func (user *User) generateAccessToken() error {
	var (
		session             kiteconnect.UserSession
		baseRequestTokenURL = fmt.Sprintf("https://kite.trade/connect/login?api_key=%s", user.APIKey)
		firstTimeAuthURL    = fmt.Sprintf("https://kite.zerodha.com/connect/login?v=3&api_key=%s", user.APIKey)
	)

	if !user.loggedIn {
		return errors.New("credentials.generateAccessToken: user is not logged in; call Login first")
	}
	resp, err := user.httpClient.Get(baseRequestTokenURL)
	if err != nil {
		return fmt.Errorf("credentials.generateAccessToken: generate request token: %w", err)
	}
	defer closeResponse(resp)

	if strings.Contains(resp.Request.URL.String(), "authorize") {
		return fmt.Errorf("credentials.generateAccessToken: first-time authorization required; visit %s", firstTimeAuthURL)
	}
	requestToken := resp.Request.URL.Query().Get("request_token")
	if requestToken == "" {
		return errors.New("credentials.generateAccessToken: request token not found; check subscription or first-time authorization")
	}

	sessionClient := kiteconnect.New(user.APIKey)
	sessionClient.SetHTTPClient(user.kiteHTTPClient)
	if session, err = sessionClient.GenerateSession(requestToken, user.APISecret); err != nil {
		return fmt.Errorf("credentials.generateAccessToken: generate Kite session: %w", err)
	}

	user.RequestToken = requestToken
	user.AccessToken = session.AccessToken
	return nil
}

func closeResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(ioutil.Discard, resp.Body)
	_ = resp.Body.Close()
}

func (user *User) isAPIUser() bool {
	return user.IsAPIUser
}

func (user *User) getTOTP() string {
	code, err := totp.GenerateCode(user.TOTPSecret, time.Now())
	if err != nil {
		panic(err)
	}
	return code
}
