package kiteconnect

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"

	"github.com/zerodhatech/go-kite-connect/models"
)

// Client represents interface for Kiteconnect client.
type Client struct {
	apiKey      string
	accessToken string
	debug       bool
	baseURI     string
	httpClient  HTTPClient
}

// UserSession represents the response after a successful authentication.
type UserSession struct {
	UserProfile
	UserSessionTokens

	APIKey      string `json:"api_key"`
	PublicToken string `json:"public_token"`
}

// UserSessionTokens represents response after renew access token.
type UserSessionTokens struct {
	UserID       string `json:"user_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Bank represents the details of a single bank account entry on a user's file.
type Bank struct {
	Name    string `json:"name"`
	Branch  string `json:"branch"`
	Account string `json:"account"`
}

// UserProfile represents a user's personal and financial profile.
type UserProfile struct {
	UserName string `json:"user_name" msgp:"user_name"`

	// This does not come from the OMS.
	UserShortName string `json:"user_shortname"`

	AvatarURL string `json:"avatar_url"`

	UserType   string   `json:"user_type" msgp:"user_type"`
	Email      string   `json:"email" msgp:"email"`
	Phone      string   `json:"phone" msgp:"phone"`
	Broker     string   `json:"broker" msgp:"broker"`
	Banks      []Bank   `json:"bank_accounts" msgp:"bank_accounts"`
	DPIDs      []string `json:"dp_ids" msgp:"dp_ids"`
	Products   []string `json:"products" msgp:"products"`
	OrderTypes []string `json:"order_types" msgp:"order_types"`
	Exchanges  []string `json:"exchanges" msgp:"exchanges"`
	Pan        string   `json:"pan" msgp:"pan"`
}

// Margins represents the margins response from Nestcore for a single segment.
type Margins struct {
	Category  string           `json:"-"`
	Enabled   bool             `json:"enabled"`
	Net       float64          `json:"net"`
	Available AvailableMargins `json:"available"`
	Used      UsedMargins      `json:"utilised"`
}

// AvailableMargins represents the available margins from the margins response for a single segment.
// TODO: Add holding_sales and turnover once in the nestcore API.
type AvailableMargins struct {
	AdHocMargin   float64 `json:"adhoc_margin"`
	Cash          float64 `json:"cash"`
	Collateral    float64 `json:"collateral"`
	IntradayPayin float64 `json:"intraday_payin"`
}

// UsedMargins represents the used margins from the margins response for a single segment.
type UsedMargins struct {
	Debits        float64 `json:"debits"`
	Exposure      float64 `json:"exposure"`
	M2MRealised   float64 `json:"m2m_realised"`
	M2MUnrealised float64 `json:"m2m_unrealised"`
	OptionPremium float64 `json:"option_premium"`
	Payout        float64 `json:"payout"`
	Span          float64 `json:"span"`
	HoldingSales  float64 `json:"holding_sales"`
	Turnover      float64 `json:"turnover"`
}

// AllMargins contains both equity and commodity margins.
type AllMargins struct {
	Equity    Margins `json:"equity"`
	Commodity Margins `json:"commodity"`
}

// GenerateSession gets a user session details in exchange or request token.
// Access token is automatically set if the session is retrieved successfully.
// Do the token exchange with the `requestToken` obtained after the login flow,
// and retrieve the `accessToken` required for all subsequent requests. The
// response contains not just the `accessToken`, but metadata for the user who has authenticated.
func (c *Client) GenerateSession(requestToken string, apiSecret string) (UserSession, error) {
	// Get SHA256 checksum
	h := sha256.New()
	h.Write([]byte(c.apiKey + requestToken + apiSecret))

	// construct url values
	params := url.Values{}
	params.Add("api_key", c.apiKey)
	params.Add("request_token", requestToken)
	params.Set("checksum", fmt.Sprintf("%x", h.Sum(nil)))

	var session UserSession
	err := c.doEnvelope(http.MethodPost, URIUserSession, params, nil, &session)

	// Set accessToken on successful session retrieve
	if err != nil && session.AccessToken != "" {
		c.SetAccessToken(session.AccessToken)
	}

	return session, err
}

func (c *Client) invalidateToken(tokenKey string, tokenValue string) (bool, error) {
	var (
		b   bool
		out models.PlainResponse
	)

	// construct url values
	params := url.Values{}
	params.Add("api_key", c.apiKey)
	params.Add(tokenKey, tokenValue)

	err := c.doEnvelope(http.MethodDelete, URIUserSessionInvalidate, params, nil, &out)
	if err == nil {
		b = true
	}

	return b, err
}

// InvalidateAccessToken invalidates the current set access token.
func (c *Client) InvalidateAccessToken() (bool, error) {
	return c.invalidateToken("access_token", c.accessToken)
}

// RenewAccessToken renews expired access token using valid refresh token.
func (c *Client) RenewAccessToken(refreshToken string, apiSecret string) (UserSessionTokens, error) {
	// Get SHA256 checksum
	h := sha256.New()
	h.Write([]byte(c.apiKey + refreshToken + apiSecret))

	// construct url values
	params := url.Values{}
	params.Add("api_key", c.apiKey)
	params.Add("refresh_token", refreshToken)
	params.Set("checksum", fmt.Sprintf("%x", h.Sum(nil)))

	var session UserSessionTokens
	err := c.doEnvelope(http.MethodPost, URIUserSessionRenew, params, nil, &session)

	// Set accessToken on successful session retrieve
	if err != nil && session.AccessToken != "" {
		c.SetAccessToken(session.AccessToken)
	}

	return session, err
}

// InvalidateRefreshToken invalidates the given refresh token.
func (c *Client) InvalidateRefreshToken(refreshToken string) (bool, error) {
	return c.invalidateToken("refresh_token", refreshToken)
}

// GetUserProfile gets user profile.
func (c *Client) GetUserProfile() (UserProfile, error) {
	var userProfile UserProfile
	err := c.doEnvelope(http.MethodGet, URIUserProfile, nil, nil, &userProfile)
	return userProfile, err
}

// GetUserMargins gets all user margins.
func (c *Client) GetUserMargins() (AllMargins, error) {
	var allUserMargins AllMargins
	err := c.doEnvelope(http.MethodGet, URIUserMargins, nil, nil, &allUserMargins)
	return allUserMargins, err
}

// GetUserSegmentMargins gets segmentwise user margins.
func (c *Client) GetUserSegmentMargins(segment string) (Margins, error) {
	var margins Margins
	err := c.doEnvelope(http.MethodGet, fmt.Sprintf(URIUserMarginsSegment, segment), nil, nil, &margins)
	return margins, err
}
