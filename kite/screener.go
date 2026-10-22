package kite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

const (
	// screenerBaseURI is where Kite web's own APIs, the screener among
	// them, live.
	screenerBaseURI = kiteBaseURI + "/oms"
	// URIScreenerInstruments is Kite web's screener endpoint.
	URIScreenerInstruments = "/screener/instruments"
	// ScreenerMaxPerPage is the most results a screener page holds: Kite
	// returns at most 100, whatever page size is asked for.
	ScreenerMaxPerPage = 100
	// screenerMaxBody bounds how much of a response is read.
	screenerMaxBody = 64 << 20
)

// ErrScreenerSession indicates a screener request without a complete web
// session: a user ID, enctoken, kf_session and public_token.
var ErrScreenerSession = errors.New("kite: screener needs a complete web session")

// ScreenerClient requests Kite web's instrument screener, which filters,
// sorts and pages instruments by market and fundamental data. It signs
// requests as the Kite web app does, with the user's web session, not an
// API access token.
type ScreenerClient struct {
	userID      string
	enctoken    string
	kfSession   string
	publicToken string
	baseURI     string
	userAgent   string
	httpClient  *http.Client
}

// NewScreenerClient is a screener client for userID's web session.
func NewScreenerClient(userID, enctoken, kfSession, publicToken string) *ScreenerClient {
	return &ScreenerClient{
		userID:      userID,
		enctoken:    enctoken,
		kfSession:   kfSession,
		publicToken: publicToken,
		baseURI:     screenerBaseURI,
		userAgent:   name + "/" + version,
		httpClient:  &http.Client{Timeout: requestTimeout},
	}
}

// ScreenerClient is a screener client with the web session set by
// SetScreenerSession and the client's HTTP client and user agent.
func (c *Client) ScreenerClient() *ScreenerClient {
	screener := NewScreenerClient(c.screenerUserID, c.screenerEncToken, c.screenerKFSession, c.screenerPublicToken)
	screener.userAgent = c.userAgent()
	if c.httpClient != nil && c.httpClient.GetClient() != nil {
		screener.SetHTTPClient(c.httpClient.GetClient().client)
	}
	return screener
}

// SetScreenerSession sets the web session ScreenerClient uses, leaving the
// client's own API authentication as it is.
func (c *Client) SetScreenerSession(userID, enctoken, kfSession, publicToken string) {
	c.screenerUserID = userID
	c.screenerEncToken = enctoken
	c.screenerKFSession = kfSession
	c.screenerPublicToken = publicToken
}

// SetHTTPClient overrides the HTTP client screener requests use.
func (s *ScreenerClient) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.httpClient = client
	}
}

// SetBaseURI overrides the screener's API root, https://kite.zerodha.com/oms.
func (s *ScreenerClient) SetBaseURI(baseURI string) {
	s.baseURI = strings.TrimSuffix(baseURI, "/")
}

// ScreenerRequest is a screener query: Query filters with conditions such
// as "market_cap>=1000 and pe<20", OrderBy names a result field and Order
// is "asc" or "desc". Tags keep instruments with any of the tags. Page
// counts from 1; PerPage is at most ScreenerMaxPerPage.
type ScreenerRequest struct {
	PerPage int      `json:"per_page"`
	Page    int      `json:"page"`
	Query   string   `json:"query"`
	OrderBy string   `json:"order_by"`
	Order   string   `json:"order"`
	Tags    []string `json:"tags"`
}

// ScreenerResponse is the screener's response envelope.
type ScreenerResponse struct {
	Status    string       `json:"status"`
	Data      ScreenerData `json:"data"`
	ErrorType string       `json:"error_type,omitempty"`
	Message   string       `json:"message,omitempty"`
}

// ScreenerData is one page of screener results.
type ScreenerData struct {
	Page       int                  `json:"page"`
	PerPage    int                  `json:"per_page"`
	Total      int                  `json:"total"`
	TotalPages int                  `json:"total_pages"`
	Results    []ScreenerInstrument `json:"results"`
}

// ScreenerInstrument is an instrument's market and fundamental data.
type ScreenerInstrument struct {
	Segment              string   `json:"segment"`
	InstrumentToken      string   `json:"instrument_token"`
	TradingSymbol        string   `json:"tradingsymbol"`
	ExchangeToken        string   `json:"exchange_token"`
	ISIN                 string   `json:"isin"`
	Name                 string   `json:"name"`
	Timestamp            string   `json:"timestamp"`
	LastTradeTime        string   `json:"last_trade_time"`
	LastPrice            float64  `json:"last_price"`
	BuyQuantity          int64    `json:"buy_quantity"`
	SellQuantity         int64    `json:"sell_quantity"`
	Volume               int64    `json:"volume"`
	Change               float64  `json:"change"`
	ChangePercent        float64  `json:"change_percent"`
	Open                 float64  `json:"open"`
	High                 float64  `json:"high"`
	Low                  float64  `json:"low"`
	Close                float64  `json:"close"`
	LowerCircuitLimit    float64  `json:"lower_circuit_limit"`
	UpperCircuitLimit    float64  `json:"upper_circuit_limit"`
	AveragePrice         float64  `json:"average_price"`
	TradeValue           float64  `json:"trade_value"`
	Sector               string   `json:"sector"`
	MarketCap            float64  `json:"market_cap"`
	PE                   float64  `json:"pe"`
	DividendYield        float64  `json:"dividend_yield"`
	ROE                  float64  `json:"roe"`
	DebtToEquity         float64  `json:"debt_to_equity"`
	Week52High           float64  `json:"week_52_high"`
	Week52Low            float64  `json:"week_52_low"`
	RevenueGrowthYOY     float64  `json:"revenue_growth_yoy"`
	PATGrowthYOY         float64  `json:"pat_growth_yoy"`
	FreeCashFlow         float64  `json:"free_cash_flow"`
	FundamentalUpdatedAt string   `json:"fundamental_updated_at"`
	Tags                 []string `json:"tags"`
}

// Exchange is the instrument's exchange, the part of Segment before any
// "-", such as NSE for "NSE" or NFO for "NFO-OPT".
func (i ScreenerInstrument) Exchange() string {
	exchange, _, _ := strings.Cut(i.Segment, "-")
	return exchange
}

// Fundamentals is the instrument's company data, without its intraday
// market data.
func (i ScreenerInstrument) Fundamentals() (models.Fundamentals, error) {
	token, err := strconv.ParseInt(strings.TrimSpace(i.InstrumentToken), 10, 64)
	if err != nil || token <= 0 {
		return models.Fundamentals{}, fmt.Errorf("%w: screener instrument %s has instrument token %q", models.ErrInvalidInput, i.TradingSymbol, i.InstrumentToken)
	}
	var updatedAt time.Time
	if value := strings.TrimSpace(i.FundamentalUpdatedAt); value != "" {
		if updatedAt, err = time.Parse(time.RFC3339, value); err != nil {
			return models.Fundamentals{}, fmt.Errorf("%w: screener instrument %s updated at %q", models.ErrInvalidInput, i.TradingSymbol, value)
		}
	}
	tags := i.Tags
	if tags == nil {
		tags = []string{}
	}
	return models.Fundamentals{
		InstrumentToken: token, Exchange: i.Exchange(), TradingSymbol: i.TradingSymbol,
		ISIN: i.ISIN, Name: i.Name, Sector: i.Sector, Tags: tags, LastPrice: i.LastPrice,
		MarketCap: i.MarketCap, PE: i.PE, DividendYield: i.DividendYield, ROE: i.ROE, DebtToEquity: i.DebtToEquity,
		Week52High: i.Week52High, Week52Low: i.Week52Low,
		RevenueGrowthYOY: i.RevenueGrowthYOY, PATGrowthYOY: i.PATGrowthYOY, FreeCashFlow: i.FreeCashFlow,
		UpdatedAt: updatedAt,
	}, nil
}

// GetInstruments returns one page of the instruments request selects.
func (s *ScreenerClient) GetInstruments(ctx context.Context, request ScreenerRequest) (ScreenerData, error) {
	if strings.TrimSpace(s.userID) == "" || strings.TrimSpace(s.enctoken) == "" ||
		strings.TrimSpace(s.kfSession) == "" || strings.TrimSpace(s.publicToken) == "" {
		return ScreenerData{}, ErrScreenerSession
	}
	if request.Page < 1 {
		request.Page = 1
	}
	if request.PerPage < 1 || request.PerPage > ScreenerMaxPerPage {
		request.PerPage = ScreenerMaxPerPage
	}
	if request.Tags == nil {
		request.Tags = []string{}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return ScreenerData{}, fmt.Errorf("kite: encode screener request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURI+URIScreenerInstruments, bytes.NewReader(body))
	if err != nil {
		return ScreenerData{}, fmt.Errorf("kite: create screener request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", kiteBaseURI)
	req.Header.Set("Referer", kiteBaseURI+"/dashboard/screener")
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("X-CSRFToken", s.publicToken)
	req.Header.Set("X-Kite-UserID", s.userID)
	req.Header.Set("X-Kite-Version", "3.0.0")
	// Kite web's APIs authenticate with the enctoken header; the cookies
	// alone are refused.
	req.Header.Set("Authorization", "enctoken "+s.enctoken)
	for _, cookie := range []*http.Cookie{
		{Name: "kf_session", Value: s.kfSession},
		{Name: "user_id", Value: s.userID},
		{Name: "public_token", Value: s.publicToken},
		{Name: "enctoken", Value: s.enctoken},
	} {
		req.AddCookie(cookie)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ScreenerData{}, fmt.Errorf("kite: screener request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, screenerMaxBody))
	if err != nil {
		return ScreenerData{}, fmt.Errorf("kite: read screener response: %w", err)
	}

	var result ScreenerResponse
	decodeErr := json.Unmarshal(raw, &result)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := result.Message
		if decodeErr != nil || message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		err := fmt.Errorf("kite: screener returned HTTP %d: %s", resp.StatusCode, message)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			err = fmt.Errorf("%w: %w", ErrScreenerSession, err)
		}
		return ScreenerData{}, err
	}
	if decodeErr != nil {
		return ScreenerData{}, fmt.Errorf("kite: decode screener response: %w", decodeErr)
	}
	if result.Status != "success" {
		return ScreenerData{}, fmt.Errorf("kite: screener failed: %s %s", result.ErrorType, result.Message)
	}
	return result.Data, nil
}

// AllInstruments returns every instrument request selects, from its page
// on, reading at most maxResults (all when maxResults is 0).
func (s *ScreenerClient) AllInstruments(ctx context.Context, request ScreenerRequest, maxResults int) ([]ScreenerInstrument, error) {
	if request.Page < 1 {
		request.Page = 1
	}
	var all []ScreenerInstrument
	for {
		page, err := s.GetInstruments(ctx, request)
		if err != nil {
			return all, err
		}
		all = append(all, page.Results...)
		if maxResults > 0 && len(all) >= maxResults {
			return all[:maxResults], nil
		}
		if len(page.Results) == 0 || request.Page >= page.TotalPages {
			return all, nil
		}
		request.Page++
	}
}
