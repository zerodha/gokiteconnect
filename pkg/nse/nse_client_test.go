package nse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestScrapeStockPageCallsObservedDataFunctions(t *testing.T) {
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		if r.UserAgent() == "" {
			t.Fatalf("expected user agent")
		}

		switch r.URL.Path {
		case "/get-quotes/equity":
			if r.URL.Query().Get("symbol") != "TCS" {
				t.Fatalf("unexpected page symbol: %s", r.URL.RawQuery)
			}
			http.SetCookie(w, &http.Cookie{Name: "nse_session", Value: "ok", Path: "/"})
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html>TCS</html>"))
		case "/api/NextApi/apiClient/GetQuoteApi":
			assertCookieAndReferer(t, r)
			writeFunctionPayload(w, r.URL.Query().Get("functionName"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	page, err := client.ScrapeStockPage(context.Background(), " tcs ")
	if err != nil {
		t.Fatalf("ScrapeStockPage() error = %v", err)
	}

	if page.Symbol != "TCS" {
		t.Fatalf("symbol = %q, want TCS", page.Symbol)
	}
	if page.Identifier != "TCSEQN" {
		t.Fatalf("identifier = %q, want TCSEQN", page.Identifier)
	}
	if page.SymbolData.FunctionName != getSymbolData {
		t.Fatalf("symbol data function = %q", page.SymbolData.FunctionName)
	}
	if len(page.SymbolData.Data.EquityResponse) != 1 {
		t.Fatalf("symbol data rows = %d, want 1", len(page.SymbolData.Data.EquityResponse))
	}
	if page.SymbolData.Data.EquityResponse[0].MetaData.Symbol != "TCS" {
		t.Fatalf("symbol data model symbol = %q", page.SymbolData.Data.EquityResponse[0].MetaData.Symbol)
	}
	if len(page.FinancialStatus.Data) != 1 {
		t.Fatalf("financial status rows = %d, want 1", len(page.FinancialStatus.Data))
	}
	if page.FinancialStatus.Data[0].TotalIncome != "6156800" {
		t.Fatalf("financial total income = %q", page.FinancialStatus.Data[0].TotalIncome)
	}

	var decoded map[string]string
	if err := page.SymbolName.Decode(&decoded); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded["symbol"] != "TCS" {
		t.Fatalf("decoded symbol = %q", decoded["symbol"])
	}

	wantPaths := []string{
		"/get-quotes/equity?symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getSymbolName&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getMetaData&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getSymbolData&marketType=N&series=EQ&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getRegDetails&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?days=1D&functionName=getSymbolChartData&symbol=TCSEQN",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getYearwiseData&symbol=TCSEQN",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getIndexList&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getFinancialStatus&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getCorporateAnnouncement&marketApiType=equities&noOfRecords=3&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getCorpAction&marketApiType=equities&noOfRecords=3&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getCorpAnnualReport&marketApiType=equities&noOfRecords=6&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getCorpBrsr&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getCorpEventCalender&marketApiType=equities&noOfRecords=3&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getShareholdingPattern&noOfRecords=5&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getCorpBoardMeeting&marketApiType=equities&noOfRecords=4&symbol=TCS&type=W",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getPeerComparisonData&index=&param=industry&quarter=&symbol=TCS&type=S",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getPeerComparisonQuaters&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getPeerComparisonAboutCompany&symbol=TCS",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getPreOpenMarketStatus",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getQuoteIndexData",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getTopTenStock",
		"/api/NextApi/apiClient/GetQuoteApi?functionName=getBlockDealSession",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestMarketAndCMSFunctionsUseObservedEndpoints(t *testing.T) {
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())

		switch r.URL.Path {
		case "/api/NextApi/apiClient", "/api/NextApi/cmsHandler", "/api/NextApi/apiStatic":
			writeFunctionPayload(w, r.URL.Query().Get("functionName"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	ctx := context.Background()

	if _, err := client.GetIndexData(ctx, "All"); err != nil {
		t.Fatalf("GetIndexData() error = %v", err)
	}
	if _, err := client.GetGiftNifty(ctx); err != nil {
		t.Fatalf("GetGiftNifty() error = %v", err)
	}
	if _, err := client.GetNotificationList(ctx); err != nil {
		t.Fatalf("GetNotificationList() error = %v", err)
	}
	if _, err := client.GetNavbarData(ctx, "footer"); err != nil {
		t.Fatalf("GetNavbarData() error = %v", err)
	}
	if _, err := client.GetNotesData(ctx, "/home-footer-quick-link"); err != nil {
		t.Fatalf("GetNotesData() error = %v", err)
	}

	wantPaths := []string{
		"/api/NextApi/apiClient?functionName=getIndexData&type=All",
		"/api/NextApi/apiClient?functionName=getGiftNifty",
		"/api/NextApi/cmsHandler?functionName=getNotificationList",
		"/api/NextApi/apiStatic?functionName=getNavbarData&refkey=footer",
		"/api/NextApi/apiStatic?functionName=getNotesData&url=%2Fhome-footer-quick-link",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestDefaultAndLegacyBaseURLsUseNSEIndiaHost(t *testing.T) {
	client := NewClient()
	if got := client.baseURL.String(); got != "https://www.nseindia.com" {
		t.Fatalf("default base url = %q, want NSE India host", got)
	}

	client = NewClient(WithBaseURL("https://www.nse.com"))
	if got := client.baseURL.String(); got != "https://www.nseindia.com" {
		t.Fatalf("legacy base url = %q, want NSE India host", got)
	}
}

func TestFunctionRetriesAfterForbiddenByPrimingPage(t *testing.T) {
	apiCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get-quotes/equity":
			http.SetCookie(w, &http.Cookie{Name: "nse_session", Value: "ok", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/api/NextApi/apiClient/GetQuoteApi":
			apiCalls++
			if apiCalls == 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if _, err := r.Cookie("nse_session"); err != nil {
				t.Fatalf("expected cookie on retry: %v", err)
			}
			writeFunctionPayload(w, r.URL.Query().Get("functionName"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	resp, err := client.GetMetaData(context.Background(), "infy")
	if err != nil {
		t.Fatalf("GetMetaData() error = %v", err)
	}
	if resp.FunctionName != getMetaData {
		t.Fatalf("function = %q, want %q", resp.FunctionName, getMetaData)
	}
	if apiCalls != 2 {
		t.Fatalf("api calls = %d, want 2", apiCalls)
	}
}

func TestFunctionRetriesAfterHTMLByPrimingPage(t *testing.T) {
	apiCalls := 0
	pageCalls := 0
	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get-quotes/equity":
			pageCalls++
			http.SetCookie(w, &http.Cookie{Name: "nse_session", Value: "ok", Path: "/"})
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html>TCS</html>"))
		case "/api/NextApi/apiClient/GetQuoteApi":
			apiCalls++
			if r.Header.Get("Origin") != server.URL {
				t.Fatalf("origin = %q, want %q", r.Header.Get("Origin"), server.URL)
			}
			if r.Header.Get("Sec-Fetch-Mode") != "cors" {
				t.Fatalf("sec-fetch-mode = %q, want cors", r.Header.Get("Sec-Fetch-Mode"))
			}
			if apiCalls == 1 {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("<!doctype html><html><title>Not Found</title></html>"))
				return
			}
			assertCookieAndReferer(t, r)
			writeFunctionPayload(w, r.URL.Query().Get("functionName"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	resp, err := client.GetSymbolData(context.Background(), "tcs", "N", "EQ")
	if err != nil {
		t.Fatalf("GetSymbolData() error = %v", err)
	}
	if len(resp.Data.EquityResponse) != 1 {
		t.Fatalf("symbol data rows = %d, want 1", len(resp.Data.EquityResponse))
	}
	if pageCalls != 1 {
		t.Fatalf("page calls = %d, want 1", pageCalls)
	}
	if apiCalls != 2 {
		t.Fatalf("api calls = %d, want 2", apiCalls)
	}
}

func TestRejectsBlankSymbol(t *testing.T) {
	client := NewClient()
	if _, err := client.GetMetaData(context.Background(), " "); err == nil {
		t.Fatal("expected blank symbol error")
	}
}

func assertCookieAndReferer(t *testing.T, r *http.Request) {
	t.Helper()
	if _, err := r.Cookie("nse_session"); err != nil {
		t.Fatalf("expected primed NSE cookie: %v", err)
	}
	if r.Header.Get("Referer") == "" {
		t.Fatalf("expected referer")
	}
}

func writeFunctionPayload(w http.ResponseWriter, functionName string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(functionPayload(functionName)))
}

func functionPayload(functionName string) string {
	switch functionName {
	case getSymbolName:
		return `{"symbol":"TCS","companyName":"Tata Consultancy Services Limited"}`
	case getMetaData:
		return `{"symbol":"TCS","activeSeries":["EQ","T0"],"companyName":"Tata Consultancy Services Limited","debtSeries":[],"isFNOSec":"true","isCASec":"false","isSLBSec":"true","isDebtSec":"false","tempSuspendedSeries":[],"isSuspended":"false","isETFSec":"false","isDelisted":"false","isin":"INE467B01029","isMunicipalBond":"false","isHybridSymbol":"false","marketType":"N","parentSymbol":"TCS"}`
	case getSymbolData:
		return `{"equityResponse":[{"orderBook":{"lastPrice":2451.1,"totalBuyQuantity":0,"totalSellQuantity":0},"metaData":{"identifier":"TCSEQN","companyName":"Tata Consultancy Services Limited","isinCode":"INE467B01029","symbol":"TCS","series":"EQ","marketType":"N","open":2320,"dayHigh":2457.4,"dayLow":2316.2,"previousClose":2297.4,"averagePrice":2419.08,"change":153.7,"basePrice":2297.4,"closePrice":2446.9,"symbolStatus":"NM","pChange":6.69},"tradeInfo":{"totalTradedVolume":11306127,"totalTradedValue":27350425703.16,"series":"EQ","lastPrice":2451.1,"issuedSize":3618087518,"basePrice":2297.4,"ffmc":2495999171649.46,"faceValue":1,"impactCost":0.02,"deliveryToTradedQuantity":52.02,"applicableMargin":13.04,"marketLot":null,"quantitytraded":11306127,"deliveryquantity":5881072,"totalMarketCap":8868294315369.8,"secwisedelposdate":"02-Jun-2026 00:00:00"},"priceInfo":{"yearHightDt":"18-Jun-2025 00:00:00","yearLowDt":"14-May-2026 00:00:00","yearHigh":3538,"yearLow":2206.4,"cmDailyVolatility":"1.52","cmAnnualVolatility":"29.04","tickSize":0.1,"inav":0,"isINav":"False","priceBand":"2202.30-2691.50","ppriceBand":"No Band"},"secInfo":{"secStatus":"Listed","listingDate":"25-Aug-2004 00:00:00","pdSectorInd":"NIFTY IT","pdSectorPe":"15.72","pdSymbolPe":"16.81","isSuspended":"Active","basicIndustry":"Computers - Software & Consulting","index":"Nifty 50","deliveryQuantity":"5881072","deliveryTotradedQuantity":"52.02","securityvar":"9.54","indexvar":"0","extremelossMargin":"3.5","varMargin":"9.54","adhocMargin":"0","applicableMargin":"13.04","bondType":null,"issueDesc":"Tata Consultancy Serv Ltd","issueDate":null,"maturityDate":null,"couponRate":null,"nxtIpDate":null,"creditRating":null,"macro":"Information Technology","sector":"Information Technology","industryInfo":"IT - Software","indexList":["NIFTY 50"],"boardStatus":"Main","tradingSegment":"Normal Market","sessionNo":null,"classShare":"Equity","nameOfComplianceOfficer":null,"sddcompliance":null},"lastUpdateTime":"02-Jun-2026 16:00:00"}]}`
	case getRegDetails:
		return `[{"regAction":null,"scripCode":"NA","symbol":"TCS","nseExclusive":"N","status":"A","series":null,"regNote":null}]`
	case getSymbolChartData:
		return `{"identifier":"TCSEQN","name":"TCS","grapthData":[[1780390859000,2314,"PO","16.6","0.72"]],"closePrice":2297.4}`
	case getYearwiseData:
		return `[{"yesterday_chng_per":-24.05,"one_week_chng_per":7.31,"one_month_chng_per":-0.92,"three_month_chng_per":-6.21,"six_month_chng_per":-22.92,"one_year_chng_per":-28.02,"two_year_chng_per":-33.81,"three_year_chng_per":-25.85,"five_year_chng_per":-21.97,"one_week_date":"27-MAY-26","index_yesterday_chng_per":-10.18,"index_one_week_chng_per":-1.8,"index_one_month_chng_per":-2.14,"index_three_month_chng_per":-5.56,"index_six_month_chng_per":-9.79,"index_one_year_chng_per":-4.99,"index_two_year_chng_per":4.23,"index_three_year_chng_per":26.7,"index_five_year_chng_per":50.77,"index_one_week_date":"26-MAY-26","index_name":"NIFTY 50"}]`
	case getIndexList:
		return `["NIFTY 50","NIFTY IT"]`
	case getFinancialStatus:
		return `[{"from_date":null,"to_date":"31 Mar 2026","to_date_MonYr":"Mar-2026","series":null,"expenditure":null,"totalIncome":"6156800","audited":"Audited","cumulative":null,"consolidated":null,"eps":"40.15","reProLossBefTax":"1846400","netProLossAftTax":"1452600","re_broadcast_timestamp":"09-Apr-2026 23:41"}]`
	case getCorporateAnnouncement:
		return `[{"symbol":"TCS","desc":"Updates","dt":"29052026201923","attchmntFile":"https://nsearchives.nse.com/corporate/TCS.pdf","sm_name":"Tata Consultancy Services Limited","sm_isin":"INE467B01029","an_dt":"29-May-2026 20:19:23","sort_date":"2026-05-29 20:19:23","seq_id":null,"smIndustry":"Computers - Software","orgid":null,"attchmntText":"Tata Consultancy Services Limited has informed the Exchange regarding Credit Rating.","bflag":null,"old_new":null,"csvName":null,"exchdisstime":"29-May-2026 20:19:24","difference":"00:00:01","fileSize":"1.40 MB","attFileSize":"1.40 MB","hasXbrl":true}]`
	case getCorpAction:
		return `[{"subject":"Dividend - Rs 31 Per Share","date":null,"recordDate":null,"symbol":"TCS","series":"EQ","ind":"-","faceVal":"1","exDate":"25-May-2026","recDate":"25-May-2026","bcStartDate":"-","bcEndDate":"-","ndStartDate":"-","comp":"Tata Consultancy Services Limited","isin":"INE467B01029","ndEndDate":"-","caBroadcastDate":null}]`
	case getCorpAnnualReport:
		return `[{"companyName":"Tata Consultancy Services Limited","fromYr":"2025","toYr":"2026","submission_type":"New","broadcast_dttm":"15-MAY-2026 23:48:30","disseminationDateTime":"15-MAY-2026 23:48:31","timeTaken":"00:00:01","fileName":"https://nsearchives.nse.com/annual_reports/TCS.pdf","attFileSize":"16.62 MB"}]`
	case getCorpBrsr:
		return `[{"symbol":"TCS","companyName":"Tata Consultancy Services Limited","fyFrom":2025,"fyTo":2026,"attachmentFile":"https://nsearchives.nse.com/corporate/BRSR.pdf","xbrlFile":"https://nsearchives.nse.com/corporate/xbrl/BRSR.xml","submissionDate":"02-JUN-26 16:39:37","revisionDate":null,"xbrlFilSize":"898 KB","attachedFileSize":"1.66 MB"}]`
	case getCorpEventCalender:
		return `[{"bm_symbol":"TCS","bm_date":"09-Apr-2026","bm_purpose":"Approval of financial results","bm_desc":"Financial Results/Dividend","sm_indusrty":"-","bm_timestamp":"23-Mar-2026","sm_name":"Tata Consultancy Services Limited","sm_isin":"INE467B01029","bm_dt":"2026-04-09 00:00:00","bm_timestamp_full":"2026-03-23 18:21:06","bm_an_seq_id":"106564250","bm_attachment":"TCS.pdf"}]`
	case getShareholdingPattern:
		return `{"31-Mar-2026":{"ndsid":"210064","series":"equity","public":{"name":"Public","value":"28.23"},"Total":"100.00","promoter_group":{"name":"Promoter & Promoter Group","value":"71.77"}}}`
	case getCorpBoardMeeting:
		return `[{"bm_symbol":"TCS","bm_date":"09-Apr-2026","bm_purpose":"Board Meeting Intimation","bm_desc":"TATA CONSULTANCY SERVICES LIMITED has informed the Exchange about Board Meeting.","sm_indusrty":"Computers - Software","bm_timestamp":"23-Mar-2026 18:27:06","sm_name":"Tata Consultancy Services Limited","sm_isin":"INE467B01029","attachment":"https://nsearchives.nse.com/corporate/xbrl/PIBM.xml","ixbrl":"https://nsearchives.nse.com/corporate/ixbrl/PRIOR_INTIMATION.html","diff":"00:00:03","sysTime":"23-Mar-2026 18:27:09","attFileSize":"14.94 KB","ixbrlFileSize":"3.38 KB"}]`
	case getPeerComparisonData:
		return `[{"symbol":"TCS","series":"EQ","marketType":"N","marketCap":8868294315369.8,"value":27350425703.16,"volume":11306127,"eps":40.15,"ltp":2451.1,"pat":1452600,"pe":15.72,"debtEqRatio":null,"promoterHolding":71.77,"totalIncome":6156800,"PChange":6.69,"pChange":6.69}]`
	case getPeerComparisonQuaters:
		return `[{"label":"MAR 2026","value":"2026-03"}]`
	case getPeerComparisonAboutCompany:
		return `{"data":null}`
	case getPreOpenMarketStatus:
		return `{"marketStatus":"PC"}`
	case getQuoteIndexData:
		return `["BANKNIFTY","MIDCPNIFTY","FINNIFTY","NIFTY","NIFTYNXT50"]`
	case getTopTenStock:
		return `{"topGainers":[{"symbol":"THACKER","series":"EQ","openPrice":1380,"highPrice":1435.2,"lowPrice":1350,"lastPrice":1435.2,"previousClose":1196,"change":239.2,"totalTradedVolume":2521,"mktType":"N","caExDt":"-","caPurpose":"-","basePrice":1196,"averagePrice":1430.37,"totalTradedValue":3605962.77,"pchange":20,"ppriceBand":null}],"topLoosers":[],"mostActiveValue":[],"mostActiveVolume":[],"volumeSpurtsValue":[],"etfWatchValue":[],"fiftyTwoWeekHigh":[],"fiftyTwoWeekLow":[],"timestamp":"02-Jun-2026 16:00"}`
	case getBlockDealSession:
		return `{"data":{"session1":[{"identifier":"ALKEMBLO","symbol":"ALKEM","series":"BL","marketType":"O","change":0,"lastPrice":5200,"totalTradedVolume":1788220,"status":null,"open":5200,"dayHigh":5200,"dayLow":5200,"previousClose":0,"averagePrice":5200,"totalBuyQuantity":0,"totalSellQuantity":0,"onlineIndex":0,"lastUpdateTime":"02-Jun-2026 08:51:12","totalTradedValue":9298744000,"exDate":null,"purpose":null,"PChange":0,"pChange":0}],"session2":[]}}`
	case getIndexData:
		return `{"data":[{"indexName":"NIFTY 50","open":23229.15,"high":23556.95,"low":23229.15,"last":23483.55,"previousClose":23382.6,"percChange":0.43,"yearHigh":26373.2,"yearLow":22182.55,"timeVal":"02-Jun-2026 15:30","constituents":null,"indicativeClose":0,"icChange":0,"icPerChange":0,"isConstituents":"Y"}]}`
	case getGiftNifty:
		return `{"data":{"marketCapitalization":{"tlMKtCapTri":4.862155052770581,"tlMKtCapLacCr":462.7322688031868,"timestamp":"02-Jun-2026"},"usdInr":{"symbol":"USDINR","updated_time":"02-Jun-2026 17:00","ltp":"95.15","instrument_type":"FUTCUR","expiry_dt":"05-Jun-2026"},"giftNifty":{"instrumenttype":null,"symbol":null,"expirydate":null,"optiontype":null,"strikeprice":null,"lastprice":null,"daychange":null,"perchange":null,"contractstraded":null,"timestmp":null,"id":null}}}`
	case getNotificationList:
		return `{"data":{"success":true,"message":"Data fetched successfully","data":[{"ID":43,"EVENT_DATE":"2026-05-21T00:00:00.000Z","TITLE":"Visit of the President of the Republic of Cyprus ","CATEGORY_NAME":"Others","SLUG_URL":"/visit-of-the-president-of-the-republic-of-cyprus","EVENT_START_TIMESTAMP":"2026-05-21T05:50:00.000Z","EVENT_END_TIMESTAMP":"2026-05-21T05:50:00.000Z","EVENT_DATE_LABEL":"PAST","THUMBNAIL_URL":"https://nsearchives.nse.com//web/event/2026-05/300x200_20260525112026.jpg"}],"responseCode":200}}`
	case getNavbarData:
		return `{"data":{"success":true,"message":"Data fetched successfully","data":{"ID":1484,"TITLE":"Footer","REFERENCEKEY":"footer","NAV_DATA":"[{\"name\":\"Footer\",\"url\":\"\",\"key\":\"footer\",\"term_id\":\"250\",\"items\":[]}]","STATUS":10,"CHECKER_COMMENT":"ok","CHECKED_BY":8,"CREATED_BY":2,"UPDATED_BY":8,"CREATED_AT":"2025-07-14T10:17:29.792Z","UPDATED_AT":"2026-03-24T09:39:42.515Z"},"responseCode":200}}`
	case getNotesData:
		return `{"DESCRIPTION":"<ul class=\"quick_list\"></ul>","TITLE":"Home Footer Quick Link"}`
	default:
		return `{}`
	}
}
