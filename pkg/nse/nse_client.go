package nse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL   = "https://www.nseindia.com"
	defaultTimeout   = 25 * time.Second
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36"

	getSymbolName                 = "getSymbolName"
	getPreOpenMarketStatus        = "getPreOpenMarketStatus"
	getQuoteIndexData             = "getQuoteIndexData"
	getTopTenStock                = "getTopTenStock"
	getBlockDealSession           = "getBlockDealSession"
	getMetaData                   = "getMetaData"
	getSymbolData                 = "getSymbolData"
	getRegDetails                 = "getRegDetails"
	getSymbolChartData            = "getSymbolChartData"
	getYearwiseData               = "getYearwiseData"
	getIndexList                  = "getIndexList"
	getFinancialStatus            = "getFinancialStatus"
	getCorporateAnnouncement      = "getCorporateAnnouncement"
	getCorpAction                 = "getCorpAction"
	getCorpAnnualReport           = "getCorpAnnualReport"
	getCorpBrsr                   = "getCorpBrsr"
	getCorpEventCalender          = "getCorpEventCalender"
	getShareholdingPattern        = "getShareholdingPattern"
	getCorpBoardMeeting           = "getCorpBoardMeeting"
	getPeerComparisonData         = "getPeerComparisonData"
	getPeerComparisonQuaters      = "getPeerComparisonQuaters"
	getPeerComparisonAboutCompany = "getPeerComparisonAboutCompany"
	getIndexData                  = "getIndexData"
	getGiftNifty                  = "getGiftNifty"
	getNotificationList           = "getNotificationList"
	getNavbarData                 = "getNavbarData"
	getNotesData                  = "getNotesData"
)

// Client calls NSE India data endpoints used by equity quote pages.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	userAgent  string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client used by the scraper.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithBaseURL replaces the NSE base URL. It is mainly useful for tests.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if parsed, err := url.Parse(strings.TrimRight(baseURL, "/")); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			if strings.EqualFold(parsed.Host, "nse.com") || strings.EqualFold(parsed.Host, "www.nse.com") {
				parsed.Host = "www.nseindia.com"
			}
			c.baseURL = parsed
		}
	}
}

// WithUserAgent replaces the browser User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(c *Client) {
		if strings.TrimSpace(userAgent) != "" {
			c.userAgent = strings.TrimSpace(userAgent)
		}
	}
}

// NewClient creates an NSE India data client.
func NewClient(opts ...Option) *Client {
	jar, _ := cookiejar.New(nil)
	baseURL, _ := url.Parse(defaultBaseURL)

	client := &Client{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
			Jar:     jar,
		},
		baseURL:   baseURL,
		userAgent: defaultUserAgent,
	}

	for _, opt := range opts {
		opt(client)
	}
	if client.httpClient.Jar == nil {
		client.httpClient.Jar = jar
	}

	return client
}

// DataResponse is a typed JSON response from one NSE data function.
type DataResponse[T any] struct {
	FunctionName string
	URL          string
	StatusCode   int
	Raw          json.RawMessage
	Data         T
}

// Decode decodes the raw NSE JSON payload into target.
func (r *DataResponse[T]) Decode(target any) error {
	return json.Unmarshal(r.Raw, target)
}

// StockPageData contains the stock-specific data functions observed on the NSE
// equity quote page.
type StockPageData struct {
	Symbol                     string
	Identifier                 string
	SymbolName                 *DataResponse[SymbolNameResponse]
	MetaData                   *DataResponse[MetaDataResponse]
	SymbolData                 *DataResponse[SymbolDataResponse]
	RegDetails                 *DataResponse[RegDetailsResponse]
	SymbolChartData            *DataResponse[SymbolChartDataResponse]
	YearwiseData               *DataResponse[YearwiseDataResponse]
	IndexList                  *DataResponse[IndexListResponse]
	FinancialStatus            *DataResponse[FinancialStatusResponse]
	CorporateAnnouncement      *DataResponse[CorporateAnnouncementResponse]
	CorpAction                 *DataResponse[CorpActionResponse]
	CorpAnnualReport           *DataResponse[CorpAnnualReportResponse]
	CorpBrsr                   *DataResponse[CorpBrsrResponse]
	CorpEventCalender          *DataResponse[CorpEventCalenderResponse]
	ShareholdingPattern        *DataResponse[ShareholdingPatternResponse]
	CorpBoardMeeting           *DataResponse[CorpBoardMeetingResponse]
	PeerComparisonData         *DataResponse[PeerComparisonDataResponse]
	PeerComparisonQuaters      *DataResponse[PeerComparisonQuatersResponse]
	PeerComparisonAboutCompany *DataResponse[PeerComparisonAboutCompanyResponse]
	PreOpenMarketStatus        *DataResponse[PreOpenMarketStatusResponse]
	QuoteIndexData             *DataResponse[QuoteIndexDataResponse]
	TopTenStock                *DataResponse[TopTenStockResponse]
	BlockDealSession           *DataResponse[BlockDealSessionResponse]
}

// PrimeStockPage opens the visible stock page so NSE can set edge cookies.
func (c *Client) PrimeStockPage(ctx context.Context, symbol string) error {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	return c.openPage(ctx, c.stockPageURL(symbol))
}

// ScrapeStockPage fetches the stock data functions used by the NSE equity page.
func (c *Client) ScrapeStockPage(ctx context.Context, symbol string) (*StockPageData, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	identifier := defaultIdentifier(symbol)

	if err := c.PrimeStockPage(ctx, symbol); err != nil {
		return nil, err
	}

	data := &StockPageData{
		Symbol:     symbol,
		Identifier: identifier,
	}

	if data.SymbolName, err = c.GetSymbolName(ctx, symbol); err != nil {
		return nil, err
	}
	if data.MetaData, err = c.GetMetaData(ctx, symbol); err != nil {
		return nil, err
	}
	if data.SymbolData, err = c.GetSymbolData(ctx, symbol, "N", "EQ"); err != nil {
		return nil, err
	}
	if data.RegDetails, err = c.GetRegDetails(ctx, symbol); err != nil {
		return nil, err
	}
	if data.SymbolChartData, err = c.GetSymbolChartData(ctx, identifier, "1D"); err != nil {
		return nil, err
	}
	if data.YearwiseData, err = c.GetYearwiseData(ctx, identifier); err != nil {
		return nil, err
	}
	if data.IndexList, err = c.GetIndexList(ctx, symbol); err != nil {
		return nil, err
	}
	if data.FinancialStatus, err = c.GetFinancialStatus(ctx, symbol); err != nil {
		return nil, err
	}
	if data.CorporateAnnouncement, err = c.GetCorporateAnnouncement(ctx, symbol, "equities", 3); err != nil {
		return nil, err
	}
	if data.CorpAction, err = c.GetCorpAction(ctx, symbol, "equities", 3); err != nil {
		return nil, err
	}
	if data.CorpAnnualReport, err = c.GetCorpAnnualReport(ctx, symbol, "equities", 6); err != nil {
		return nil, err
	}
	if data.CorpBrsr, err = c.GetCorpBrsr(ctx, symbol); err != nil {
		return nil, err
	}
	if data.CorpEventCalender, err = c.GetCorpEventCalender(ctx, symbol, "equities", 3); err != nil {
		return nil, err
	}
	if data.ShareholdingPattern, err = c.GetShareholdingPattern(ctx, symbol, 5); err != nil {
		return nil, err
	}
	if data.CorpBoardMeeting, err = c.GetCorpBoardMeeting(ctx, symbol, "equities", "W", 4); err != nil {
		return nil, err
	}
	if data.PeerComparisonData, err = c.GetPeerComparisonData(ctx, symbol, "S", "", "industry", ""); err != nil {
		return nil, err
	}
	if data.PeerComparisonQuaters, err = c.GetPeerComparisonQuaters(ctx, symbol); err != nil {
		return nil, err
	}
	if data.PeerComparisonAboutCompany, err = c.GetPeerComparisonAboutCompany(ctx, symbol); err != nil {
		return nil, err
	}
	if data.PreOpenMarketStatus, err = c.GetPreOpenMarketStatus(ctx); err != nil {
		return nil, err
	}
	if data.QuoteIndexData, err = c.GetQuoteIndexData(ctx); err != nil {
		return nil, err
	}
	if data.TopTenStock, err = c.GetTopTenStock(ctx); err != nil {
		return nil, err
	}
	if data.BlockDealSession, err = c.GetBlockDealSession(ctx); err != nil {
		return nil, err
	}

	return data, nil
}

// GetSymbolName calls functionName=getSymbolName.
func (c *Client) GetSymbolName(ctx context.Context, symbol string) (*DataResponse[SymbolNameResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[SymbolNameResponse](c, ctx, getSymbolName, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetPreOpenMarketStatus calls functionName=getPreOpenMarketStatus.
func (c *Client) GetPreOpenMarketStatus(ctx context.Context) (*DataResponse[PreOpenMarketStatusResponse], error) {
	return callGetQuoteAPI[PreOpenMarketStatusResponse](c, ctx, getPreOpenMarketStatus, nil, c.homeURL())
}

// GetQuoteIndexData calls functionName=getQuoteIndexData.
func (c *Client) GetQuoteIndexData(ctx context.Context) (*DataResponse[QuoteIndexDataResponse], error) {
	return callGetQuoteAPI[QuoteIndexDataResponse](c, ctx, getQuoteIndexData, nil, c.homeURL())
}

// GetTopTenStock calls functionName=getTopTenStock.
func (c *Client) GetTopTenStock(ctx context.Context) (*DataResponse[TopTenStockResponse], error) {
	return callGetQuoteAPI[TopTenStockResponse](c, ctx, getTopTenStock, nil, c.homeURL())
}

// GetBlockDealSession calls functionName=getBlockDealSession.
func (c *Client) GetBlockDealSession(ctx context.Context) (*DataResponse[BlockDealSessionResponse], error) {
	return callGetQuoteAPI[BlockDealSessionResponse](c, ctx, getBlockDealSession, nil, c.homeURL())
}

// GetMetaData calls functionName=getMetaData.
func (c *Client) GetMetaData(ctx context.Context, symbol string) (*DataResponse[MetaDataResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[MetaDataResponse](c, ctx, getMetaData, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetSymbolData calls functionName=getSymbolData.
func (c *Client) GetSymbolData(ctx context.Context, symbol string, marketType string, series string) (*DataResponse[SymbolDataResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	marketType = defaultIfBlank(strings.ToUpper(strings.TrimSpace(marketType)), "N")
	series = defaultIfBlank(strings.ToUpper(strings.TrimSpace(series)), "EQ")

	return callGetQuoteAPI[SymbolDataResponse](c, ctx, getSymbolData, map[string]string{
		"marketType": marketType,
		"series":     series,
		"symbol":     symbol,
	}, c.stockPageURL(symbol))
}

// GetRegDetails calls functionName=getRegDetails.
func (c *Client) GetRegDetails(ctx context.Context, symbol string) (*DataResponse[RegDetailsResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[RegDetailsResponse](c, ctx, getRegDetails, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetSymbolChartData calls functionName=getSymbolChartData.
func (c *Client) GetSymbolChartData(ctx context.Context, identifier string, days string) (*DataResponse[SymbolChartDataResponse], error) {
	identifier, err := normalizeIdentifier(identifier)
	if err != nil {
		return nil, err
	}
	days = defaultIfBlank(strings.ToUpper(strings.TrimSpace(days)), "1D")

	return callGetQuoteAPI[SymbolChartDataResponse](c, ctx, getSymbolChartData, map[string]string{
		"symbol": identifier,
		"days":   days,
	}, c.homeURL())
}

// GetYearwiseData calls functionName=getYearwiseData.
func (c *Client) GetYearwiseData(ctx context.Context, identifier string) (*DataResponse[YearwiseDataResponse], error) {
	identifier, err := normalizeIdentifier(identifier)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[YearwiseDataResponse](c, ctx, getYearwiseData, map[string]string{
		"symbol": identifier,
	}, c.homeURL())
}

// GetIndexList calls functionName=getIndexList.
func (c *Client) GetIndexList(ctx context.Context, symbol string) (*DataResponse[IndexListResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[IndexListResponse](c, ctx, getIndexList, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetFinancialStatus calls functionName=getFinancialStatus.
func (c *Client) GetFinancialStatus(ctx context.Context, symbol string) (*DataResponse[FinancialStatusResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[FinancialStatusResponse](c, ctx, getFinancialStatus, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetCorporateAnnouncement calls functionName=getCorporateAnnouncement.
func (c *Client) GetCorporateAnnouncement(ctx context.Context, symbol string, marketAPIType string, noOfRecords int) (*DataResponse[CorporateAnnouncementResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[CorporateAnnouncementResponse](c, ctx, getCorporateAnnouncement, map[string]string{
		"symbol":        symbol,
		"marketApiType": defaultIfBlank(strings.TrimSpace(marketAPIType), "equities"),
		"noOfRecords":   defaultRecords(noOfRecords, 3),
	}, c.stockPageURL(symbol))
}

// GetCorpAction calls functionName=getCorpAction.
func (c *Client) GetCorpAction(ctx context.Context, symbol string, marketAPIType string, noOfRecords int) (*DataResponse[CorpActionResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[CorpActionResponse](c, ctx, getCorpAction, map[string]string{
		"symbol":        symbol,
		"marketApiType": defaultIfBlank(strings.TrimSpace(marketAPIType), "equities"),
		"noOfRecords":   defaultRecords(noOfRecords, 3),
	}, c.stockPageURL(symbol))
}

// GetCorpAnnualReport calls functionName=getCorpAnnualReport.
func (c *Client) GetCorpAnnualReport(ctx context.Context, symbol string, marketAPIType string, noOfRecords int) (*DataResponse[CorpAnnualReportResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[CorpAnnualReportResponse](c, ctx, getCorpAnnualReport, map[string]string{
		"symbol":        symbol,
		"marketApiType": defaultIfBlank(strings.TrimSpace(marketAPIType), "equities"),
		"noOfRecords":   defaultRecords(noOfRecords, 6),
	}, c.stockPageURL(symbol))
}

// GetCorpBrsr calls functionName=getCorpBrsr.
func (c *Client) GetCorpBrsr(ctx context.Context, symbol string) (*DataResponse[CorpBrsrResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[CorpBrsrResponse](c, ctx, getCorpBrsr, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetCorpEventCalender calls functionName=getCorpEventCalender.
func (c *Client) GetCorpEventCalender(ctx context.Context, symbol string, marketAPIType string, noOfRecords int) (*DataResponse[CorpEventCalenderResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[CorpEventCalenderResponse](c, ctx, getCorpEventCalender, map[string]string{
		"symbol":        symbol,
		"marketApiType": defaultIfBlank(strings.TrimSpace(marketAPIType), "equities"),
		"noOfRecords":   defaultRecords(noOfRecords, 3),
	}, c.stockPageURL(symbol))
}

// GetShareholdingPattern calls functionName=getShareholdingPattern.
func (c *Client) GetShareholdingPattern(ctx context.Context, symbol string, noOfRecords int) (*DataResponse[ShareholdingPatternResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[ShareholdingPatternResponse](c, ctx, getShareholdingPattern, map[string]string{
		"symbol":      symbol,
		"noOfRecords": defaultRecords(noOfRecords, 5),
	}, c.stockPageURL(symbol))
}

// GetCorpBoardMeeting calls functionName=getCorpBoardMeeting.
func (c *Client) GetCorpBoardMeeting(ctx context.Context, symbol string, marketAPIType string, meetingType string, noOfRecords int) (*DataResponse[CorpBoardMeetingResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[CorpBoardMeetingResponse](c, ctx, getCorpBoardMeeting, map[string]string{
		"symbol":        symbol,
		"marketApiType": defaultIfBlank(strings.TrimSpace(marketAPIType), "equities"),
		"type":          defaultIfBlank(strings.TrimSpace(meetingType), "W"),
		"noOfRecords":   defaultRecords(noOfRecords, 4),
	}, c.stockPageURL(symbol))
}

// GetPeerComparisonData calls functionName=getPeerComparisonData.
func (c *Client) GetPeerComparisonData(ctx context.Context, symbol string, comparisonType string, quarter string, param string, index string) (*DataResponse[PeerComparisonDataResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[PeerComparisonDataResponse](c, ctx, getPeerComparisonData, map[string]string{
		"symbol":  symbol,
		"type":    defaultIfBlank(strings.TrimSpace(comparisonType), "S"),
		"quarter": strings.TrimSpace(quarter),
		"param":   defaultIfBlank(strings.TrimSpace(param), "industry"),
		"index":   strings.TrimSpace(index),
	}, c.stockPageURL(symbol))
}

// GetPeerComparisonQuaters calls functionName=getPeerComparisonQuaters.
func (c *Client) GetPeerComparisonQuaters(ctx context.Context, symbol string) (*DataResponse[PeerComparisonQuatersResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[PeerComparisonQuatersResponse](c, ctx, getPeerComparisonQuaters, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetPeerComparisonAboutCompany calls functionName=getPeerComparisonAboutCompany.
func (c *Client) GetPeerComparisonAboutCompany(ctx context.Context, symbol string) (*DataResponse[PeerComparisonAboutCompanyResponse], error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return callGetQuoteAPI[PeerComparisonAboutCompanyResponse](c, ctx, getPeerComparisonAboutCompany, map[string]string{
		"symbol": symbol,
	}, c.stockPageURL(symbol))
}

// GetIndexData calls functionName=getIndexData.
func (c *Client) GetIndexData(ctx context.Context, indexType string) (*DataResponse[IndexDataResponse], error) {
	indexType = defaultIfBlank(strings.TrimSpace(indexType), "All")
	return callAPIClient[IndexDataResponse](c, ctx, getIndexData, map[string]string{
		"type": indexType,
	}, c.homeURL())
}

// GetGiftNifty calls functionName=getGiftNifty.
func (c *Client) GetGiftNifty(ctx context.Context) (*DataResponse[GiftNiftyResponse], error) {
	return callAPIClient[GiftNiftyResponse](c, ctx, getGiftNifty, nil, c.homeURL())
}

// GetNotificationList calls functionName=getNotificationList.
func (c *Client) GetNotificationList(ctx context.Context) (*DataResponse[NotificationListResponse], error) {
	return callCMSHandler[NotificationListResponse](c, ctx, getNotificationList, nil, c.homeURL())
}

// GetNavbarData calls functionName=getNavbarData.
func (c *Client) GetNavbarData(ctx context.Context, refkey string) (*DataResponse[NavbarDataResponse], error) {
	refkey = defaultIfBlank(strings.TrimSpace(refkey), "mainNavigation")
	return callAPIStatic[NavbarDataResponse](c, ctx, getNavbarData, map[string]string{
		"refkey": refkey,
	}, c.homeURL())
}

// GetNotesData calls functionName=getNotesData.
func (c *Client) GetNotesData(ctx context.Context, pageURL string) (*DataResponse[NotesDataResponse], error) {
	pageURL = defaultIfBlank(strings.TrimSpace(pageURL), "/home-footer-quick-link")
	return callAPIStatic[NotesDataResponse](c, ctx, getNotesData, map[string]string{
		"url": pageURL,
	}, c.homeURL())
}

func callGetQuoteAPI[T any](c *Client, ctx context.Context, functionName string, params map[string]string, referer string) (*DataResponse[T], error) {
	return callNextAPI[T](c, ctx, "/api/NextApi/apiClient/GetQuoteApi", functionName, params, referer)
}

func callAPIClient[T any](c *Client, ctx context.Context, functionName string, params map[string]string, referer string) (*DataResponse[T], error) {
	return callNextAPI[T](c, ctx, "/api/NextApi/apiClient", functionName, params, referer)
}

func callAPIStatic[T any](c *Client, ctx context.Context, functionName string, params map[string]string, referer string) (*DataResponse[T], error) {
	return callNextAPI[T](c, ctx, "/api/NextApi/apiStatic", functionName, params, referer)
}

func callCMSHandler[T any](c *Client, ctx context.Context, functionName string, params map[string]string, referer string) (*DataResponse[T], error) {
	return callNextAPI[T](c, ctx, "/api/NextApi/cmsHandler", functionName, params, referer)
}

func callNextAPI[T any](c *Client, ctx context.Context, path string, functionName string, params map[string]string, referer string) (*DataResponse[T], error) {
	requestURL := c.nextAPIURL(path, functionName, params)
	resp, err := c.do(ctx, requestURL, referer, true)
	if err != nil {
		return nil, err
	}

	raw, statusCode, err := readAndCloseResponse(resp)
	if err != nil && shouldRetryAfterPriming(statusCode, err, referer) {
		if err := c.openPage(ctx, referer); err != nil {
			return nil, err
		}

		resp, err = c.do(ctx, requestURL, referer, true)
		if err != nil {
			return nil, err
		}
		raw, statusCode, err = readAndCloseResponse(resp)
	}
	if err != nil {
		return nil, err
	}

	var data T
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}

	return &DataResponse[T]{
		FunctionName: functionName,
		URL:          requestURL,
		StatusCode:   statusCode,
		Raw:          raw,
		Data:         data,
	}, nil
}

type invalidJSONResponseError struct {
	statusCode  int
	contentType string
	preview     string
}

func (e *invalidJSONResponseError) Error() string {
	if e.contentType == "" {
		return fmt.Sprintf("nse response is not valid json (status %d; preview: %q)", e.statusCode, e.preview)
	}
	return fmt.Sprintf("nse response is not valid json (status %d, content-type %q; preview: %q)", e.statusCode, e.contentType, e.preview)
}

func readAndCloseResponse(resp *http.Response) (json.RawMessage, int, error) {
	defer resp.Body.Close()
	raw, err := readResponse(resp)
	return raw, resp.StatusCode, err
}

func readResponse(resp *http.Response) (json.RawMessage, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nse request failed with status %d: %s", resp.StatusCode, responsePreview(raw))
	}
	if !json.Valid(raw) {
		return nil, &invalidJSONResponseError{
			statusCode:  resp.StatusCode,
			contentType: resp.Header.Get("Content-Type"),
			preview:     responsePreview(raw),
		}
	}
	return json.RawMessage(raw), nil
}

func shouldRetryAfterPriming(statusCode int, err error, referer string) bool {
	if referer == "" {
		return false
	}
	if statusCode == http.StatusForbidden || statusCode == http.StatusUnauthorized {
		return true
	}

	var invalidJSON *invalidJSONResponseError
	return errors.As(err, &invalidJSON)
}

func responsePreview(raw []byte) string {
	preview := strings.TrimSpace(string(raw))
	const maxPreviewLength = 500
	if len(preview) > maxPreviewLength {
		preview = preview[:maxPreviewLength] + "..."
	}
	return preview
}

func (c *Client) openPage(ctx context.Context, pageURL string) error {
	resp, err := c.do(ctx, pageURL, "", false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("nse page request failed with status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) do(ctx context.Context, requestURL string, referer string, jsonRequest bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,hi;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if jsonRequest {
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Origin", c.origin())
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
	}

	return c.httpClient.Do(req)
}

func (c *Client) origin() string {
	u := *c.baseURL
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func (c *Client) stockPageURL(symbol string) string {
	u := c.resolve("/get-quotes/equity")
	q := u.Query()
	q.Set("symbol", symbol)
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) homeURL() string {
	u := c.resolve("/")
	return u.String()
}

func (c *Client) nextAPIURL(path string, functionName string, params map[string]string) string {
	u := c.resolve(path)
	q := u.Query()
	q.Set("functionName", functionName)
	for key, value := range params {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) resolve(path string) *url.URL {
	u := *c.baseURL
	u.Path = path
	u.RawQuery = ""
	return &u
}

func defaultIdentifier(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol)) + "EQN"
}

func defaultIfBlank(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func defaultRecords(value int, fallback int) string {
	if value <= 0 {
		value = fallback
	}
	return strconv.Itoa(value)
}

func normalizeIdentifier(identifier string) (string, error) {
	identifier = strings.ToUpper(strings.TrimSpace(identifier))
	if identifier == "" {
		return "", fmt.Errorf("nse identifier is required")
	}
	return identifier, nil
}

func normalizeSymbol(symbol string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", fmt.Errorf("nse symbol is required")
	}
	return symbol, nil
}
