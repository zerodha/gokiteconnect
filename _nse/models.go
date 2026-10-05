package nse

import (
	"encoding/json"
	"fmt"
)

// SymbolNameResponse is returned by functionName=getSymbolName.
type SymbolNameResponse struct {
	Symbol      string `json:"symbol"`
	CompanyName string `json:"companyName"`
}

// MetaDataResponse is returned by functionName=getMetaData.
type MetaDataResponse struct {
	Symbol              string   `json:"symbol"`
	ActiveSeries        []string `json:"activeSeries"`
	CompanyName         string   `json:"companyName"`
	DebtSeries          []string `json:"debtSeries"`
	IsFNOSec            string   `json:"isFNOSec"`
	IsCASec             string   `json:"isCASec"`
	IsSLBSec            string   `json:"isSLBSec"`
	IsDebtSec           string   `json:"isDebtSec"`
	TempSuspendedSeries []string `json:"tempSuspendedSeries"`
	IsSuspended         string   `json:"isSuspended"`
	IsETFSec            string   `json:"isETFSec"`
	IsDelisted          string   `json:"isDelisted"`
	ISIN                string   `json:"isin"`
	IsMunicipalBond     string   `json:"isMunicipalBond"`
	IsHybridSymbol      string   `json:"isHybridSymbol"`
	MarketType          string   `json:"marketType"`
	ParentSymbol        string   `json:"parentSymbol"`
}

// SymbolDataResponse is returned by functionName=getSymbolData.
type SymbolDataResponse struct {
	EquityResponse []EquityResponse `json:"equityResponse"`
}

// EquityResponse contains the combined quote sections for one equity series.
type EquityResponse struct {
	OrderBook      OrderBook      `json:"orderBook"`
	MetaData       SymbolMetaData `json:"metaData"`
	TradeInfo      TradeInfo      `json:"tradeInfo"`
	PriceInfo      PriceInfo      `json:"priceInfo"`
	SecInfo        SecInfo        `json:"secInfo"`
	LastUpdateTime string         `json:"lastUpdateTime"`
}

// OrderBook contains five-level market depth and last traded price data.
type OrderBook struct {
	BuyPrice1         float64 `json:"buyPrice1"`
	BuyQuantity1      float64 `json:"buyQuantity1"`
	BuyPrice2         float64 `json:"buyPrice2"`
	BuyQuantity2      float64 `json:"buyQuantity2"`
	BuyPrice3         float64 `json:"buyPrice3"`
	BuyQuantity3      float64 `json:"buyQuantity3"`
	BuyPrice4         float64 `json:"buyPrice4"`
	BuyQuantity4      float64 `json:"buyQuantity4"`
	BuyPrice5         float64 `json:"buyPrice5"`
	BuyQuantity5      float64 `json:"buyQuantity5"`
	SellPrice1        float64 `json:"sellPrice1"`
	SellQuantity1     float64 `json:"sellQuantity1"`
	SellPrice2        float64 `json:"sellPrice2"`
	SellQuantity2     float64 `json:"sellQuantity2"`
	SellPrice3        float64 `json:"sellPrice3"`
	SellQuantity3     float64 `json:"sellQuantity3"`
	SellPrice4        float64 `json:"sellPrice4"`
	SellQuantity4     float64 `json:"sellQuantity4"`
	SellPrice5        float64 `json:"sellPrice5"`
	SellQuantity5     float64 `json:"sellQuantity5"`
	LastPrice         float64 `json:"lastPrice"`
	TotalBuyQuantity  float64 `json:"totalBuyQuantity"`
	TotalSellQuantity float64 `json:"totalSellQuantity"`
	PerBuyQty         float64 `json:"perBuyQty"`
	PerSellQty        float64 `json:"perSellQty"`
}

// SymbolMetaData contains current quote metadata from getSymbolData.
type SymbolMetaData struct {
	Identifier      string  `json:"identifier"`
	CompanyName     string  `json:"companyName"`
	ISINCode        string  `json:"isinCode"`
	Symbol          string  `json:"symbol"`
	Series          string  `json:"series"`
	MarketType      string  `json:"marketType"`
	Open            float64 `json:"open"`
	DayHigh         float64 `json:"dayHigh"`
	DayLow          float64 `json:"dayLow"`
	PreviousClose   float64 `json:"previousClose"`
	AveragePrice    float64 `json:"averagePrice"`
	Change          float64 `json:"change"`
	BasePrice       float64 `json:"basePrice"`
	ClosePrice      float64 `json:"closePrice"`
	IndicativeClose float64 `json:"indicativeClose"`
	ICChange        float64 `json:"ic_change"`
	ICPChange       float64 `json:"ic_pchange"`
	SPOChange       float64 `json:"spoChange"`
	SPOPChange      float64 `json:"spoPchange"`
	SymbolStatus    string  `json:"symbolStatus"`
	AdjPrice        float64 `json:"adjPrice"`
	IEP             float64 `json:"iep"`
	IEQ             float64 `json:"ieq"`
	PChange         float64 `json:"pChange"`
}

// TradeInfo contains traded value, volume, margin, and delivery data.
type TradeInfo struct {
	TotalTradedVolume        float64         `json:"totalTradedVolume"`
	TotalTradedValue         float64         `json:"totalTradedValue"`
	Series                   string          `json:"series"`
	LastPrice                float64         `json:"lastPrice"`
	IssuedSize               float64         `json:"issuedSize"`
	BasePrice                float64         `json:"basePrice"`
	FreeFloatMarketCap       float64         `json:"ffmc"`
	FaceValue                float64         `json:"faceValue"`
	ImpactCost               float64         `json:"impactCost"`
	DeliveryToTradedQuantity float64         `json:"deliveryToTradedQuantity"`
	ApplicableMargin         float64         `json:"applicableMargin"`
	MarketLot                json.RawMessage `json:"marketLot"`
	QuantityTraded           float64         `json:"quantitytraded"`
	DeliveryQuantity         float64         `json:"deliveryquantity"`
	TotalMarketCap           float64         `json:"totalMarketCap"`
	SecWiseDelPosDate        string          `json:"secwisedelposdate"`
}

// PriceInfo contains yearly range, volatility, tick, and band data.
type PriceInfo struct {
	YearHighDate       string  `json:"yearHightDt"`
	YearLowDate        string  `json:"yearLowDt"`
	YearHigh           float64 `json:"yearHigh"`
	YearLow            float64 `json:"yearLow"`
	CMDailyVolatility  string  `json:"cmDailyVolatility"`
	CMAnnualVolatility string  `json:"cmAnnualVolatility"`
	TickSize           float64 `json:"tickSize"`
	INAV               float64 `json:"inav"`
	IsINAV             string  `json:"isINav"`
	PriceBand          string  `json:"priceBand"`
	PPriceBand         string  `json:"ppriceBand"`
}

// SecInfo contains listing, risk, industry, and classification details.
type SecInfo struct {
	SecStatus                string          `json:"secStatus"`
	ListingDate              string          `json:"listingDate"`
	PDSectorInd              string          `json:"pdSectorInd"`
	PDSectorPE               string          `json:"pdSectorPe"`
	PDSymbolPE               string          `json:"pdSymbolPe"`
	IsSuspended              string          `json:"isSuspended"`
	BasicIndustry            string          `json:"basicIndustry"`
	Index                    string          `json:"index"`
	DeliveryQuantity         string          `json:"deliveryQuantity"`
	DeliveryToTradedQuantity string          `json:"deliveryTotradedQuantity"`
	SecurityVAR              string          `json:"securityvar"`
	IndexVAR                 string          `json:"indexvar"`
	ExtremeLossMargin        string          `json:"extremelossMargin"`
	VARMargin                string          `json:"varMargin"`
	AdhocMargin              string          `json:"adhocMargin"`
	ApplicableMargin         string          `json:"applicableMargin"`
	BondType                 json.RawMessage `json:"bondType"`
	IssueDesc                string          `json:"issueDesc"`
	IssueDate                json.RawMessage `json:"issueDate"`
	MaturityDate             json.RawMessage `json:"maturityDate"`
	CouponRate               json.RawMessage `json:"couponRate"`
	NextIPDate               json.RawMessage `json:"nxtIpDate"`
	CreditRating             json.RawMessage `json:"creditRating"`
	Macro                    string          `json:"macro"`
	Sector                   string          `json:"sector"`
	IndustryInfo             string          `json:"industryInfo"`
	IndexList                []string        `json:"indexList"`
	BoardStatus              string          `json:"boardStatus"`
	TradingSegment           string          `json:"tradingSegment"`
	SessionNo                json.RawMessage `json:"sessionNo"`
	ClassShare               string          `json:"classShare"`
	NameOfComplianceOfficer  json.RawMessage `json:"nameOfComplianceOfficer"`
	SDDCompliance            json.RawMessage `json:"sddcompliance"`
}

// RegDetailsResponse is returned by functionName=getRegDetails.
type RegDetailsResponse []RegDetailsItem

// RegDetailsItem contains regulatory status for a symbol.
type RegDetailsItem struct {
	RegAction    json.RawMessage `json:"regAction"`
	ScripCode    string          `json:"scripCode"`
	Symbol       string          `json:"symbol"`
	NSEExclusive string          `json:"nseExclusive"`
	Status       string          `json:"status"`
	Series       json.RawMessage `json:"series"`
	RegNote      json.RawMessage `json:"regNote"`
}

// SymbolChartDataResponse is returned by functionName=getSymbolChartData.
type SymbolChartDataResponse struct {
	Identifier string       `json:"identifier"`
	Name       string       `json:"name"`
	GrapthData []ChartPoint `json:"grapthData"`
	ClosePrice float64      `json:"closePrice"`
}

// ChartPoint is one tuple in NSE's grapthData array.
type ChartPoint struct {
	Timestamp  int64
	Price      float64
	MarketType string
	Change     string
	PChange    string
}

// UnmarshalJSON decodes [timestamp, price, marketType, change, pChange].
func (p *ChartPoint) UnmarshalJSON(data []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	if len(items) != 5 {
		return fmt.Errorf("nse chart point has %d fields, want 5", len(items))
	}
	if err := json.Unmarshal(items[0], &p.Timestamp); err != nil {
		return err
	}
	if err := json.Unmarshal(items[1], &p.Price); err != nil {
		return err
	}
	if err := json.Unmarshal(items[2], &p.MarketType); err != nil {
		return err
	}
	if err := json.Unmarshal(items[3], &p.Change); err != nil {
		return err
	}
	return json.Unmarshal(items[4], &p.PChange)
}

// YearwiseDataResponse is returned by functionName=getYearwiseData.
type YearwiseDataResponse []YearwisePerformance

// YearwisePerformance compares stock and index return windows.
type YearwisePerformance struct {
	YesterdayChangePercent       float64 `json:"yesterday_chng_per"`
	OneWeekChangePercent         float64 `json:"one_week_chng_per"`
	OneMonthChangePercent        float64 `json:"one_month_chng_per"`
	ThreeMonthChangePercent      float64 `json:"three_month_chng_per"`
	SixMonthChangePercent        float64 `json:"six_month_chng_per"`
	OneYearChangePercent         float64 `json:"one_year_chng_per"`
	TwoYearChangePercent         float64 `json:"two_year_chng_per"`
	ThreeYearChangePercent       float64 `json:"three_year_chng_per"`
	FiveYearChangePercent        float64 `json:"five_year_chng_per"`
	OneWeekDate                  string  `json:"one_week_date"`
	IndexYesterdayChangePercent  float64 `json:"index_yesterday_chng_per"`
	IndexOneWeekChangePercent    float64 `json:"index_one_week_chng_per"`
	IndexOneMonthChangePercent   float64 `json:"index_one_month_chng_per"`
	IndexThreeMonthChangePercent float64 `json:"index_three_month_chng_per"`
	IndexSixMonthChangePercent   float64 `json:"index_six_month_chng_per"`
	IndexOneYearChangePercent    float64 `json:"index_one_year_chng_per"`
	IndexTwoYearChangePercent    float64 `json:"index_two_year_chng_per"`
	IndexThreeYearChangePercent  float64 `json:"index_three_year_chng_per"`
	IndexFiveYearChangePercent   float64 `json:"index_five_year_chng_per"`
	IndexOneWeekDate             string  `json:"index_one_week_date"`
	IndexName                    string  `json:"index_name"`
}

// IndexListResponse is returned by functionName=getIndexList.
type IndexListResponse []string

// FinancialStatusResponse is returned by functionName=getFinancialStatus.
type FinancialStatusResponse []FinancialStatusItem

// FinancialStatusItem contains one financial-result period row.
type FinancialStatusItem struct {
	FromDate              json.RawMessage `json:"from_date"`
	ToDate                string          `json:"to_date"`
	ToDateMonthYear       string          `json:"to_date_MonYr"`
	Series                json.RawMessage `json:"series"`
	Expenditure           json.RawMessage `json:"expenditure"`
	TotalIncome           string          `json:"totalIncome"`
	Audited               string          `json:"audited"`
	Cumulative            json.RawMessage `json:"cumulative"`
	Consolidated          json.RawMessage `json:"consolidated"`
	EPS                   string          `json:"eps"`
	ProfitLossBeforeTax   string          `json:"reProLossBefTax"`
	NetProfitLossAfterTax string          `json:"netProLossAftTax"`
	ReBroadcastTimestamp  string          `json:"re_broadcast_timestamp"`
}

// CorporateAnnouncementResponse is returned by functionName=getCorporateAnnouncement.
type CorporateAnnouncementResponse []CorporateAnnouncement

// CorporateAnnouncement contains one corporate announcement row.
type CorporateAnnouncement struct {
	Symbol                    string          `json:"symbol"`
	Description               string          `json:"desc"`
	DateToken                 string          `json:"dt"`
	AttachmentFile            string          `json:"attchmntFile"`
	CompanyName               string          `json:"sm_name"`
	ISIN                      string          `json:"sm_isin"`
	AnnouncementDate          string          `json:"an_dt"`
	SortDate                  string          `json:"sort_date"`
	SequenceID                json.RawMessage `json:"seq_id"`
	Industry                  string          `json:"smIndustry"`
	OrgID                     json.RawMessage `json:"orgid"`
	AttachmentText            string          `json:"attchmntText"`
	BFlag                     json.RawMessage `json:"bflag"`
	OldNew                    json.RawMessage `json:"old_new"`
	CSVName                   json.RawMessage `json:"csvName"`
	ExchangeDisseminationTime string          `json:"exchdisstime"`
	Difference                string          `json:"difference"`
	FileSize                  string          `json:"fileSize"`
	AttachmentFileSize        string          `json:"attFileSize"`
	HasXBRL                   bool            `json:"hasXbrl"`
}

// CorpActionResponse is returned by functionName=getCorpAction.
type CorpActionResponse []CorpAction

// CorpAction contains one corporate action row.
type CorpAction struct {
	Subject         string          `json:"subject"`
	Date            json.RawMessage `json:"date"`
	RecordDate      json.RawMessage `json:"recordDate"`
	Symbol          string          `json:"symbol"`
	Series          string          `json:"series"`
	Indicator       string          `json:"ind"`
	FaceValue       string          `json:"faceVal"`
	ExDate          string          `json:"exDate"`
	RecDate         string          `json:"recDate"`
	BCStartDate     string          `json:"bcStartDate"`
	BCEndDate       string          `json:"bcEndDate"`
	NDStartDate     string          `json:"ndStartDate"`
	Company         string          `json:"comp"`
	ISIN            string          `json:"isin"`
	NDEndDate       string          `json:"ndEndDate"`
	CABroadcastDate json.RawMessage `json:"caBroadcastDate"`
}

// CorpAnnualReportResponse is returned by functionName=getCorpAnnualReport.
type CorpAnnualReportResponse []CorpAnnualReport

// CorpAnnualReport contains one annual report file row.
type CorpAnnualReport struct {
	CompanyName           string          `json:"companyName"`
	FromYear              string          `json:"fromYr"`
	ToYear                string          `json:"toYr"`
	SubmissionType        string          `json:"submission_type"`
	BroadcastDateTime     string          `json:"broadcast_dttm"`
	DisseminationDateTime string          `json:"disseminationDateTime"`
	TimeTaken             string          `json:"timeTaken"`
	FileName              string          `json:"fileName"`
	AttachmentFileSize    json.RawMessage `json:"attFileSize"`
}

// CorpBrsrResponse is returned by functionName=getCorpBrsr.
type CorpBrsrResponse []CorpBrsr

// CorpBrsr contains one Business Responsibility and Sustainability Report row.
type CorpBrsr struct {
	Symbol           string          `json:"symbol"`
	CompanyName      string          `json:"companyName"`
	FYFrom           int             `json:"fyFrom"`
	FYTo             int             `json:"fyTo"`
	AttachmentFile   string          `json:"attachmentFile"`
	XBRLFile         string          `json:"xbrlFile"`
	SubmissionDate   string          `json:"submissionDate"`
	RevisionDate     json.RawMessage `json:"revisionDate"`
	XBRLFileSize     json.RawMessage `json:"xbrlFilSize"`
	AttachedFileSize json.RawMessage `json:"attachedFileSize"`
}

// CorpEventCalenderResponse is returned by functionName=getCorpEventCalender.
type CorpEventCalenderResponse []CorpEventCalenderItem

// CorpEventCalenderItem contains one corporate event calendar row.
type CorpEventCalenderItem struct {
	Symbol            string `json:"bm_symbol"`
	Date              string `json:"bm_date"`
	Purpose           string `json:"bm_purpose"`
	Description       string `json:"bm_desc"`
	Industry          string `json:"sm_indusrty"`
	Timestamp         string `json:"bm_timestamp"`
	CompanyName       string `json:"sm_name"`
	ISIN              string `json:"sm_isin"`
	DateTime          string `json:"bm_dt"`
	TimestampFull     string `json:"bm_timestamp_full"`
	AnnouncementSeqID string `json:"bm_an_seq_id"`
	Attachment        string `json:"bm_attachment"`
}

// ShareholdingPatternResponse is returned by functionName=getShareholdingPattern.
type ShareholdingPatternResponse map[string]ShareholdingPatternItem

// ShareholdingPatternItem contains one date-keyed shareholding snapshot.
type ShareholdingPatternItem struct {
	NDSID         string             `json:"ndsid"`
	Series        string             `json:"series"`
	Public        ShareholdingBucket `json:"public"`
	Total         string             `json:"Total"`
	PromoterGroup ShareholdingBucket `json:"promoter_group"`
}

// ShareholdingBucket contains a labelled shareholding percentage.
type ShareholdingBucket struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CorpBoardMeetingResponse is returned by functionName=getCorpBoardMeeting.
type CorpBoardMeetingResponse []CorpBoardMeeting

// CorpBoardMeeting contains one board meeting disclosure row.
type CorpBoardMeeting struct {
	Symbol             string          `json:"bm_symbol"`
	Date               string          `json:"bm_date"`
	Purpose            string          `json:"bm_purpose"`
	Description        string          `json:"bm_desc"`
	Industry           string          `json:"sm_indusrty"`
	Timestamp          string          `json:"bm_timestamp"`
	CompanyName        string          `json:"sm_name"`
	ISIN               string          `json:"sm_isin"`
	Attachment         string          `json:"attachment"`
	IXBRL              json.RawMessage `json:"ixbrl"`
	Difference         string          `json:"diff"`
	SystemTime         string          `json:"sysTime"`
	AttachmentFileSize string          `json:"attFileSize"`
	IXBRLFileSize      json.RawMessage `json:"ixbrlFileSize"`
}

// PeerComparisonDataResponse is returned by functionName=getPeerComparisonData.
type PeerComparisonDataResponse []PeerComparisonItem

// PeerComparisonItem contains one peer-comparison row.
type PeerComparisonItem struct {
	Symbol          string          `json:"symbol"`
	Series          string          `json:"series"`
	MarketType      string          `json:"marketType"`
	MarketCap       float64         `json:"marketCap"`
	Value           float64         `json:"value"`
	Volume          float64         `json:"volume"`
	EPS             float64         `json:"eps"`
	LastTradedPrice float64         `json:"ltp"`
	PAT             float64         `json:"pat"`
	PE              float64         `json:"pe"`
	DebtEquityRatio json.RawMessage `json:"debtEqRatio"`
	PromoterHolding float64         `json:"promoterHolding"`
	TotalIncome     float64         `json:"totalIncome"`
	UpperPChange    float64         `json:"PChange"`
	PChange         float64         `json:"pChange"`
}

// PeerComparisonQuatersResponse is returned by functionName=getPeerComparisonQuaters.
type PeerComparisonQuatersResponse []PeerComparisonQuarter

// PeerComparisonQuarter contains one selectable financial quarter.
type PeerComparisonQuarter struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// PeerComparisonAboutCompanyResponse is returned by functionName=getPeerComparisonAboutCompany.
type PeerComparisonAboutCompanyResponse struct {
	Data json.RawMessage `json:"data"`
}

// PreOpenMarketStatusResponse is returned by functionName=getPreOpenMarketStatus.
type PreOpenMarketStatusResponse struct {
	MarketStatus string `json:"marketStatus"`
}

// QuoteIndexDataResponse is returned by functionName=getQuoteIndexData.
type QuoteIndexDataResponse []string

// TopTenStockResponse is returned by functionName=getTopTenStock.
type TopTenStockResponse struct {
	TopGainers        []MarketMover `json:"topGainers"`
	TopLoosers        []MarketMover `json:"topLoosers"`
	MostActiveValue   []MarketMover `json:"mostActiveValue"`
	MostActiveVolume  []MarketMover `json:"mostActiveVolume"`
	VolumeSpurtsValue []MarketMover `json:"volumeSpurtsValue"`
	ETFWatchValue     []MarketMover `json:"etfWatchValue"`
	FiftyTwoWeekHigh  []MarketMover `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow   []MarketMover `json:"fiftyTwoWeekLow"`
	Timestamp         string        `json:"timestamp"`
}

// MarketMover is a stock row used across top-gainer/loser and activity buckets.
type MarketMover struct {
	Symbol            string          `json:"symbol"`
	Series            string          `json:"series"`
	OpenPrice         float64         `json:"openPrice"`
	HighPrice         float64         `json:"highPrice"`
	LowPrice          float64         `json:"lowPrice"`
	LastPrice         float64         `json:"lastPrice"`
	PreviousClose     float64         `json:"previousClose"`
	Change            float64         `json:"change"`
	TotalTradedVolume float64         `json:"totalTradedVolume"`
	MarketType        string          `json:"mktType"`
	CAExDate          string          `json:"caExDt"`
	CAPurpose         string          `json:"caPurpose"`
	BasePrice         float64         `json:"basePrice"`
	AveragePrice      float64         `json:"averagePrice"`
	TotalTradedValue  float64         `json:"totalTradedValue"`
	PChange           float64         `json:"pchange"`
	PPriceBand        json.RawMessage `json:"ppriceBand"`
}

// BlockDealSessionResponse is returned by functionName=getBlockDealSession.
type BlockDealSessionResponse struct {
	Data BlockDealSessions `json:"data"`
}

// BlockDealSessions groups large deals by NSE session.
type BlockDealSessions struct {
	Session1 []BlockDeal `json:"session1"`
	Session2 []BlockDeal `json:"session2"`
}

// BlockDeal describes one large-deal row.
type BlockDeal struct {
	Identifier        string          `json:"identifier"`
	Symbol            string          `json:"symbol"`
	Series            string          `json:"series"`
	MarketType        string          `json:"marketType"`
	Change            float64         `json:"change"`
	LastPrice         float64         `json:"lastPrice"`
	TotalTradedVolume float64         `json:"totalTradedVolume"`
	Status            json.RawMessage `json:"status"`
	Open              float64         `json:"open"`
	DayHigh           float64         `json:"dayHigh"`
	DayLow            float64         `json:"dayLow"`
	PreviousClose     float64         `json:"previousClose"`
	AveragePrice      float64         `json:"averagePrice"`
	TotalBuyQuantity  float64         `json:"totalBuyQuantity"`
	TotalSellQuantity float64         `json:"totalSellQuantity"`
	OnlineIndex       float64         `json:"onlineIndex"`
	LastUpdateTime    string          `json:"lastUpdateTime"`
	TotalTradedValue  float64         `json:"totalTradedValue"`
	ExDate            json.RawMessage `json:"exDate"`
	Purpose           json.RawMessage `json:"purpose"`
	UpperPChange      float64         `json:"PChange"`
	PChange           float64         `json:"pChange"`
}

// IndexDataResponse is returned by functionName=getIndexData.
type IndexDataResponse struct {
	Data []IndexDataItem `json:"data"`
}

// IndexDataItem contains one live index quote row.
type IndexDataItem struct {
	IndexName       string          `json:"indexName"`
	Open            float64         `json:"open"`
	High            float64         `json:"high"`
	Low             float64         `json:"low"`
	Last            float64         `json:"last"`
	PreviousClose   float64         `json:"previousClose"`
	PercentChange   float64         `json:"percChange"`
	YearHigh        float64         `json:"yearHigh"`
	YearLow         float64         `json:"yearLow"`
	TimeValue       string          `json:"timeVal"`
	Constituents    json.RawMessage `json:"constituents"`
	IndicativeClose float64         `json:"indicativeClose"`
	ICChange        float64         `json:"icChange"`
	ICPercentChange float64         `json:"icPerChange"`
	IsConstituents  string          `json:"isConstituents"`
}

// GiftNiftyResponse is returned by functionName=getGiftNifty.
type GiftNiftyResponse struct {
	Data GiftNiftyData `json:"data"`
}

// GiftNiftyData contains market capitalization, USD/INR, and GIFT NIFTY data.
type GiftNiftyData struct {
	MarketCapitalization MarketCapitalization `json:"marketCapitalization"`
	USDINR               CurrencyQuote        `json:"usdInr"`
	GiftNifty            GiftNiftyQuote       `json:"giftNifty"`
}

// MarketCapitalization contains aggregate market-cap values.
type MarketCapitalization struct {
	TotalMarketCapTrillion float64 `json:"tlMKtCapTri"`
	TotalMarketCapLacCr    float64 `json:"tlMKtCapLacCr"`
	Timestamp              string  `json:"timestamp"`
}

// CurrencyQuote contains USD/INR futures data.
type CurrencyQuote struct {
	Symbol          string `json:"symbol"`
	UpdatedTime     string `json:"updated_time"`
	LastTradedPrice string `json:"ltp"`
	InstrumentType  string `json:"instrument_type"`
	ExpiryDate      string `json:"expiry_dt"`
}

// GiftNiftyQuote contains the nested giftNifty quote, often null outside hours.
type GiftNiftyQuote struct {
	InstrumentType  json.RawMessage `json:"instrumenttype"`
	Symbol          json.RawMessage `json:"symbol"`
	ExpiryDate      json.RawMessage `json:"expirydate"`
	OptionType      json.RawMessage `json:"optiontype"`
	StrikePrice     json.RawMessage `json:"strikeprice"`
	LastPrice       json.RawMessage `json:"lastprice"`
	DayChange       json.RawMessage `json:"daychange"`
	PercentChange   json.RawMessage `json:"perchange"`
	ContractsTraded json.RawMessage `json:"contractstraded"`
	Timestamp       json.RawMessage `json:"timestmp"`
	ID              json.RawMessage `json:"id"`
}

// NotificationListResponse is returned by functionName=getNotificationList.
type NotificationListResponse struct {
	Data CMSListEnvelope[NotificationItem] `json:"data"`
}

// NotificationItem describes an NSE homepage event/notification item.
type NotificationItem struct {
	ID                  int    `json:"ID"`
	EventDate           string `json:"EVENT_DATE"`
	Title               string `json:"TITLE"`
	CategoryName        string `json:"CATEGORY_NAME"`
	SlugURL             string `json:"SLUG_URL"`
	EventStartTimestamp string `json:"EVENT_START_TIMESTAMP"`
	EventEndTimestamp   string `json:"EVENT_END_TIMESTAMP"`
	EventDateLabel      string `json:"EVENT_DATE_LABEL"`
	ThumbnailURL        string `json:"THUMBNAIL_URL"`
}

// NavbarDataResponse is returned by functionName=getNavbarData.
type NavbarDataResponse struct {
	Data CMSObjectEnvelope[NavbarPayload] `json:"data"`
}

// NavbarPayload contains the CMS navigation payload.
type NavbarPayload struct {
	ID             int       `json:"ID"`
	Title          string    `json:"TITLE"`
	ReferenceKey   string    `json:"REFERENCEKEY"`
	NavDataRaw     string    `json:"NAV_DATA"`
	Status         int       `json:"STATUS"`
	CheckerComment string    `json:"CHECKER_COMMENT"`
	CheckedBy      int       `json:"CHECKED_BY"`
	CreatedBy      int       `json:"CREATED_BY"`
	UpdatedBy      int       `json:"UPDATED_BY"`
	CreatedAt      string    `json:"CREATED_AT"`
	UpdatedAt      string    `json:"UPDATED_AT"`
	NavData        []NavItem `json:"-"`
}

// UnmarshalJSON decodes NAV_DATA's JSON string into NavData.
func (p *NavbarPayload) UnmarshalJSON(data []byte) error {
	type alias NavbarPayload
	var value alias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.NavDataRaw != "" {
		if err := json.Unmarshal([]byte(value.NavDataRaw), &value.NavData); err != nil {
			return err
		}
	}
	*p = NavbarPayload(value)
	return nil
}

// NavItem is one recursive item in NSE's navigation JSON.
type NavItem struct {
	Name   string          `json:"name"`
	URL    string          `json:"url"`
	Key    string          `json:"key"`
	TermID string          `json:"term_id"`
	Type   string          `json:"type"`
	Banner json.RawMessage `json:"banner"`
	Items  []NavItem       `json:"items"`
}

// NotesDataResponse is returned by functionName=getNotesData.
type NotesDataResponse struct {
	Description string `json:"DESCRIPTION"`
	Title       string `json:"TITLE"`
}

// CMSListEnvelope is the common CMS response envelope for list payloads.
type CMSListEnvelope[T any] struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	Data         []T    `json:"data"`
	ResponseCode int    `json:"responseCode"`
}

// CMSObjectEnvelope is the common CMS response envelope for object payloads.
type CMSObjectEnvelope[T any] struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	Data         T      `json:"data"`
	ResponseCode int    `json:"responseCode"`
}
