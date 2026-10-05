package nse

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
	"tradebot-go/domain"

	"github.com/PuerkitoBio/goquery"
)

const (
	niftyIndicesBaseURL   = "https://www.niftyindices.com"
	niftyIndexMappingURL  = "https://iislliveblob.niftyindices.com/assets/json/IndexMapping.json"
	defaultIndicesTimeout = 25 * time.Second
)

var niftyIndexCategoryPaths = []string{
	"/indices/equity/broad-based-indices",
	"/indices/equity/sectoral-indices",
	"/indices/equity/thematic-indices",
	"/indices/equity/strategy-indices",
}

var niftyLongNameReplacements = map[string]string{
	"nifty midcap150":                             "nifty midcap 150",
	"nifty financial services ex bank":            "nifty financial services ex-bank",
	"nifty oil and gas":                           "nifty oil & gas",
	"nifty alpha low volatility 30":               "nifty alpha low-volatility 30",
	"nifty alpha quality low volatility 30":       "nifty alpha quality low-volatility 30",
	"nifty alpha quality value low volatility 30": "nifty alpha quality value low-volatility 30",
	"nifty quality low volatility 30":             "nifty quality low-volatility 30",
}

// IndicesClient discovers Nifty index pages and constituent CSVs.
type IndicesClient struct {
	httpClient *http.Client
	baseURL    *url.URL
	mappingURL string
	userAgent  string
}

// IndicesOption configures an IndicesClient.
type IndicesOption func(*IndicesClient)

// WithIndicesHTTPClient replaces the HTTP client used by the indices client.
func WithIndicesHTTPClient(httpClient *http.Client) IndicesOption {
	return func(c *IndicesClient) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithIndicesBaseURL replaces the Nifty Indices base URL. It is mainly useful for tests.
func WithIndicesBaseURL(baseURL string) IndicesOption {
	return func(c *IndicesClient) {
		if parsed, err := url.Parse(strings.TrimRight(baseURL, "/")); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			c.baseURL = parsed
		}
	}
}

// WithIndexMappingURL replaces the index mapping URL. It is mainly useful for tests.
func WithIndexMappingURL(mappingURL string) IndicesOption {
	return func(c *IndicesClient) {
		if strings.TrimSpace(mappingURL) != "" {
			c.mappingURL = strings.TrimSpace(mappingURL)
		}
	}
}

// WithIndicesUserAgent replaces the browser User-Agent header.
func WithIndicesUserAgent(userAgent string) IndicesOption {
	return func(c *IndicesClient) {
		if strings.TrimSpace(userAgent) != "" {
			c.userAgent = strings.TrimSpace(userAgent)
		}
	}
}

// NewIndicesClient creates a client that crawls Nifty index constituent pages.
func NewIndicesClient(opts ...IndicesOption) *IndicesClient {
	jar, _ := cookiejar.New(nil)
	baseURL, _ := url.Parse(niftyIndicesBaseURL)

	client := &IndicesClient{
		httpClient: &http.Client{
			Timeout: defaultIndicesTimeout,
			Jar:     jar,
		},
		baseURL:    baseURL,
		mappingURL: niftyIndexMappingURL,
		userAgent:  defaultUserAgent,
	}

	for _, opt := range opts {
		opt(client)
	}
	if client.httpClient.Jar == nil {
		client.httpClient.Jar = jar
	}

	return client
}

// NSEIndexMetadata describes the source pages and CSV asset for a Nifty index.
type NSEIndexMetadata struct {
	TradingSymbol     string `json:"trading_symbol"`
	IndexName         string `json:"index_name"`
	CollectionName    string `json:"collection_name"`
	CategoryPageURL   string `json:"category_page_url"`
	IndexPageURL      string `json:"index_page_url"`
	ConstituentCSVURL string `json:"constituent_csv_url"`
}

// GetIndicesMetadata returns discovered Nifty index pages and constituent CSV URLs.
func (c *IndicesClient) GetIndicesMetadata(ctx context.Context) ([]NSEIndexMetadata, error) {
	longNameToSymbol, err := c.fetchLongNameToTradingSymbolMapping(ctx)
	if err != nil {
		return nil, err
	}

	metadataBySymbol, err := c.fetchIndexPages(ctx, longNameToSymbol)
	if err != nil {
		return nil, err
	}

	tradingSymbols := sortedIndexTradingSymbols(metadataBySymbol)
	metadata := make([]NSEIndexMetadata, 0, len(tradingSymbols))
	errs := make([]error, 0)
	for _, tradingSymbol := range tradingSymbols {
		item := metadataBySymbol[tradingSymbol]
		csvURL, err := c.FetchConstituentCSVURL(ctx, item.IndexPageURL)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", tradingSymbol, err))
			continue
		}
		item.ConstituentCSVURL = csvURL
		metadata = append(metadata, item)
	}

	if len(errs) > 0 {
		return metadata, fmt.Errorf("failed to fetch metadata for %d indices: %w", len(errs), errors.Join(errs...))
	}
	return metadata, nil
}

// GetIndices returns auto-generated index collections from Nifty indices.
func (c *IndicesClient) GetIndices(ctx context.Context) ([]domain.InstrumentCollection, error) {
	metadata, err := c.GetIndicesMetadata(ctx)
	if err != nil && len(metadata) == 0 {
		return nil, err
	}

	collections := make([]domain.InstrumentCollection, 0, len(metadata))
	errs := make([]error, 0)
	for _, item := range metadata {
		collection, collectionErr := c.GetIndex(ctx, item)
		if collectionErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", item.TradingSymbol, collectionErr))
			continue
		}
		collections = append(collections, collection)
	}

	if len(errs) > 0 {
		return collections, fmt.Errorf("failed to fetch constituents for %d indices: %w", len(errs), errors.Join(errs...))
	}
	return collections, err
}

// GetIndex returns one auto-generated collection for the given metadata.
func (c *IndicesClient) GetIndex(ctx context.Context, metadata NSEIndexMetadata) (domain.InstrumentCollection, error) {
	instrumentIDs, err := c.GetConstituentIDs(ctx, metadata)
	if err != nil {
		return domain.InstrumentCollection{}, err
	}

	items := make([]domain.InstrumentCollectionItem, 0, len(instrumentIDs))
	for _, instrumentID := range instrumentIDs {
		items = append(items, domain.InstrumentCollectionItem{InstrumentID: instrumentID})
	}

	collectionName := metadata.CollectionName
	if collectionName == "" {
		collectionName = "NSE:" + strings.ToUpper(metadata.TradingSymbol)
	}

	return domain.InstrumentCollection{
		Name:          collectionName,
		IsIndex:       true,
		AutoGenerated: true,
		Instruments:   items,
	}, nil
}

// GetConstituentIDs returns NSE instrument IDs like NSE:RELIANCE for an index.
func (c *IndicesClient) GetConstituentIDs(ctx context.Context, metadata NSEIndexMetadata) ([]string, error) {
	if metadata.ConstituentCSVURL != "" {
		return c.DownloadConstituentIDs(ctx, metadata.ConstituentCSVURL)
	}
	if metadata.IndexPageURL != "" {
		csvURL, err := c.FetchConstituentCSVURL(ctx, metadata.IndexPageURL)
		if err != nil {
			return nil, err
		}
		return c.DownloadConstituentIDs(ctx, csvURL)
	}
	return nil, fmt.Errorf("index metadata has no constituent csv url or index page url")
}

// FetchConstituentCSVURL finds the constituent CSV link on an index page.
func (c *IndicesClient) FetchConstituentCSVURL(ctx context.Context, indexPageURL string) (string, error) {
	doc, err := c.getHTML(ctx, indexPageURL, niftyIndicesBaseURL)
	if err != nil {
		return "", err
	}

	csvURL := ""
	doc.Find(`a[href*="../IndexConstituent"]`).EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		href := strings.TrimSpace(selection.AttrOr("href", ""))
		if href == "" {
			return true
		}
		csvURL = resolveURL(c.baseURL.String(), href)
		return csvURL == ""
	})

	if csvURL == "" {
		return "", fmt.Errorf("no constituent csv link found")
	}
	return csvURL, nil
}

// DownloadConstituentIDs downloads a constituent CSV and returns NSE instrument IDs.
func (c *IndicesClient) DownloadConstituentIDs(ctx context.Context, csvURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, csvURL, nil)
	if err != nil {
		return nil, err
	}
	c.setBrowserHeaders(req.Header, niftyIndicesBaseURL, false)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("csv download failed with status code %d", resp.StatusCode)
	}

	return parseConstituentIDs(resp.Body)
}

type indexMappingItem struct {
	TradingIndexName string `json:"Trading_Index_Name"`
	IndexLongName    string `json:"Index_long_name"`
}

func (c *IndicesClient) fetchLongNameToTradingSymbolMapping(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mappingURL, nil)
	if err != nil {
		return nil, err
	}
	c.setBrowserHeaders(req.Header, niftyIndicesBaseURL, true)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("index mapping request failed with status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	body = stripUTF8BOM(body)

	var items []indexMappingItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}

	longNameToSymbol := make(map[string]string, len(items))
	for _, item := range items {
		tradingSymbol := normalizeIndexText(item.TradingIndexName)
		longName := normalizeIndexText(item.IndexLongName)
		if tradingSymbol != "" && longName != "" {
			longNameToSymbol[longName] = tradingSymbol
		}
	}

	if len(longNameToSymbol) == 0 {
		return nil, fmt.Errorf("index mapping returned no valid items")
	}
	return longNameToSymbol, nil
}

func (c *IndicesClient) fetchIndexPages(ctx context.Context, longNameToSymbol map[string]string) (map[string]NSEIndexMetadata, error) {
	metadataBySymbol := make(map[string]NSEIndexMetadata)
	errs := make([]error, 0)

	for _, path := range niftyIndexCategoryPaths {
		categoryURL := c.resolve(path)
		doc, err := c.getHTML(ctx, categoryURL, c.baseURL.String())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", categoryURL, err))
			continue
		}
		parseIndexCategoryPage(doc, categoryURL, c.baseURL.String(), longNameToSymbol, metadataBySymbol)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(metadataBySymbol) == 0 {
		return nil, fmt.Errorf("no index pages were parsed")
	}
	return metadataBySymbol, nil
}

func parseIndexCategoryPage(doc *goquery.Document, categoryURL string, baseURL string, longNameToSymbol map[string]string, metadataBySymbol map[string]NSEIndexMetadata) {
	doc.Find("div.indicesTopics a").Each(func(_ int, selection *goquery.Selection) {
		indexName := strings.Join(strings.Fields(strings.TrimSpace(selection.Text())), " ")
		longName := normalizeIndexText(indexName)
		if replacement, ok := niftyLongNameReplacements[longName]; ok {
			longName = replacement
		}

		tradingSymbol, ok := longNameToSymbol[longName]
		if !ok {
			return
		}

		indexPageURL := resolveURL(baseURL, selection.AttrOr("href", ""))
		if indexPageURL == "" {
			return
		}

		metadataBySymbol[tradingSymbol] = NSEIndexMetadata{
			TradingSymbol:   tradingSymbol,
			IndexName:       indexName,
			CollectionName:  "NSE:" + strings.ToUpper(tradingSymbol),
			CategoryPageURL: categoryURL,
			IndexPageURL:    indexPageURL,
		}
	})
}

func (c *IndicesClient) getHTML(ctx context.Context, pageURL string, referer string) (*goquery.Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	c.setBrowserHeaders(req.Header, referer, false)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("page request failed with status code %d", resp.StatusCode)
	}

	return goquery.NewDocumentFromReader(resp.Body)
}

func (c *IndicesClient) setBrowserHeaders(headers http.Header, referer string, jsonRequest bool) {
	headers.Set("User-Agent", c.userAgent)
	headers.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		headers.Set("Referer", referer)
	}
	if jsonRequest {
		headers.Set("Accept", "application/json, text/plain, */*")
		return
	}
	headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
}

func parseConstituentIDs(reader io.Reader) ([]string, error) {
	records, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("csv has no constituent rows")
	}

	symbolColumn, err := findCSVColumn(records[0], "symbol")
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	instrumentIDs := make([]string, 0, len(records)-1)
	for _, record := range records[1:] {
		if symbolColumn >= len(record) {
			continue
		}
		symbol := strings.TrimSpace(record[symbolColumn])
		if symbol == "" {
			continue
		}
		instrumentID := "NSE:" + symbol
		if _, ok := seen[instrumentID]; ok {
			continue
		}
		seen[instrumentID] = struct{}{}
		instrumentIDs = append(instrumentIDs, instrumentID)
	}

	if len(instrumentIDs) == 0 {
		return nil, fmt.Errorf("no instrument ids parsed from csv")
	}
	return instrumentIDs, nil
}

func findCSVColumn(header []string, columnName string) (int, error) {
	for idx, field := range header {
		if normalizeCSVColumnName(field) == columnName {
			return idx, nil
		}
	}
	return -1, fmt.Errorf("%s column not found", columnName)
}

func sortedIndexTradingSymbols(metadataBySymbol map[string]NSEIndexMetadata) []string {
	tradingSymbols := make([]string, 0, len(metadataBySymbol))
	for tradingSymbol := range metadataBySymbol {
		tradingSymbols = append(tradingSymbols, tradingSymbol)
	}
	sort.Strings(tradingSymbols)
	return tradingSymbols
}

func stripUTF8BOM(body []byte) []byte {
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		return body[3:]
	}
	return body
}

func normalizeIndexText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
}

func normalizeCSVColumnName(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")))
}

func resolveURL(baseURL string, ref string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	refURL, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ""
	}
	return base.ResolveReference(refURL).String()
}

func (c *IndicesClient) resolve(path string) string {
	u := *c.baseURL
	u.Path = path
	u.RawQuery = ""
	return u.String()
}
