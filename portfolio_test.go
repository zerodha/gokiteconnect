package kiteconnect

import (
	"strings"
	"testing"
)

func (ts *TestSuite) TestGetPositions(t *testing.T) {
	t.Parallel()
	positions, err := ts.KiteConnect.GetPositions()
	if err != nil {
		t.Errorf("Error while fetching positions. %v", err)
	}
	if positions.Day == nil {
		t.Errorf("Error while fetching day positions. %v", err)
	}
	if positions.Net == nil {
		t.Errorf("Error while fetching net positions. %v", err)
	}
	for _, position := range positions.Day {
		if position.Tradingsymbol == "" {
			t.Errorf("Error while fetching trading symbol in day position. %v", err)
		}
	}
	for _, position := range positions.Net {
		if position.Tradingsymbol == "" {
			t.Errorf("Error while fetching tradingsymbol in net position. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetHoldings(t *testing.T) {
	t.Parallel()
	holdings, err := ts.KiteConnect.GetHoldings()
	if err != nil {
		t.Errorf("Error while fetching holdings. %v", err)
	}
	for _, holding := range holdings {
		if holding.Tradingsymbol == "" {
			t.Errorf("Error while fetching tradingsymbol in holdings. %v", err)
		}
	}
	// MTF fields
	if holdings[0].MTF.Quantity != 1000 {
		t.Errorf("Error while fetching quantity in mtf holdings. %v", err)
	}
	if holdings[0].MTF.Value != 100000 {
		t.Errorf("Error while fetching value in mtf holdings. %v", err)
	}
}

func (ts *TestSuite) TestGetAuctionInstruments(t *testing.T) {
	t.Parallel()
	auctionIns, err := ts.KiteConnect.GetAuctionInstruments()
	if err != nil {
		t.Errorf("Error while fetching auction instrument : %v", err)
	}
	for _, ins := range auctionIns {
		if ins.AuctionNumber == "" {
			t.Errorf("Error while retrieving auction number from the auction instruments list : %v", err)
		}
		if ins.Quantity == 0 {
			t.Errorf("Error while retrieving auction qty from the auction instruments list : %v", err)
		}
	}
}

func (ts *TestSuite) TestConvertPosition(t *testing.T) {
	t.Parallel()
	params := ConvertPositionParams{
		Exchange:        "test",
		TradingSymbol:   "test",
		OldProduct:      "test",
		NewProduct:      "test",
		PositionType:    "test",
		TransactionType: "test",
		Quantity:        1,
	}
	response, err := ts.KiteConnect.ConvertPosition(params)
	if err != nil || response != true {
		t.Errorf("Error while converting position. %v", err)
	}
}

func (ts *TestSuite) TestGetHoldingsSummary(t *testing.T) {
	t.Parallel()
	summary, err := ts.KiteConnect.GetHoldingsSummary()
	if err != nil {
		t.Errorf("Error while fetching holdings summary. %v", err)
	}

	// Verify fields are populated correctly based on mock data
	if summary.TotalPnL == 0 {
		t.Errorf("TotalPnL should not be zero in holdings summary")
	}
	if summary.TotalPnLPercent == 0 {
		t.Errorf("TotalPnLPercent should not be zero in holdings summary")
	}
	if summary.InvestedAmount == 0 {
		t.Errorf("InvestedAmount should not be zero in holdings summary")
	}
	if summary.CurrentValue == 0 {
		t.Errorf("CurrentValue should not be zero in holdings summary")
	}

	// Test specific values from mock response
	if summary.TotalPnL != 6798.235950000001 {
		t.Errorf("Expected TotalPnL to be 6798.235950000001, got %v", summary.TotalPnL)
	}
	if summary.TotalPnLPercent != 16.56312500412587 {
		t.Errorf("Expected TotalPnLPercent to be 16.56312500412587, got %v", summary.TotalPnLPercent)
	}
	if summary.TodayPnL != -76.79999999999775 {
		t.Errorf("Expected TodayPnL to be -76.79999999999775, got %v", summary.TodayPnL)
	}
	if summary.TodayPnLPercent != -0.16026898477945015 {
		t.Errorf("Expected TodayPnLPercent to be -0.16026898477945015, got %v", summary.TodayPnLPercent)
	}
	if summary.InvestedAmount != 41044.40405 {
		t.Errorf("Expected InvestedAmount to be 41044.40405, got %v", summary.InvestedAmount)
	}
	if summary.CurrentValue != 47842.64000000001 {
		t.Errorf("Expected CurrentValue to be 47842.64000000001, got %v", summary.CurrentValue)
	}
}

func (ts *TestSuite) TestGetHoldingsCompact(t *testing.T) {
	t.Parallel()
	holdings, err := ts.KiteConnect.GetHoldingsCompact()
	if err != nil {
		t.Errorf("Error while fetching compact holdings. %v", err)
	}

	// Verify we got holdings
	if len(holdings) == 0 {
		t.Errorf("Expected to receive compact holdings, got empty slice")
	}

	// Test specific values from mock response
	if len(holdings) != 21 {
		t.Errorf("Expected 21 compact holdings, got %v", len(holdings))
	}

	// Test first holding
	if holdings[0].Exchange != "NSE" {
		t.Errorf("Expected first holding exchange to be NSE, got %v", holdings[0].Exchange)
	}
	if holdings[0].Tradingsymbol != "63MOONS" {
		t.Errorf("Expected first holding tradingsymbol to be 63MOONS, got %v", holdings[0].Tradingsymbol)
	}
	if holdings[0].InstrumentToken != 3038209 {
		t.Errorf("Expected first holding instrument token to be 3038209, got %v", holdings[0].InstrumentToken)
	}
	if holdings[0].Quantity != 1 {
		t.Errorf("Expected first holding quantity to be 1, got %v", holdings[0].Quantity)
	}

	// Test a BSE holding (index 7)
	if holdings[7].Exchange != "BSE" {
		t.Errorf("Expected holding[7] exchange to be BSE, got %v", holdings[7].Exchange)
	}
	if holdings[7].Tradingsymbol != "FEDERALBNK" {
		t.Errorf("Expected holding[7] tradingsymbol to be FEDERALBNK, got %v", holdings[7].Tradingsymbol)
	}

	// Test last holding
	lastIdx := len(holdings) - 1
	if holdings[lastIdx].Tradingsymbol != "SBIN" {
		t.Errorf("Expected last holding tradingsymbol to be SBIN, got %v", holdings[lastIdx].Tradingsymbol)
	}
	if holdings[lastIdx].Quantity != 32 {
		t.Errorf("Expected last holding quantity to be 32, got %v", holdings[lastIdx].Quantity)
	}

	// Verify all holdings have required fields
	for i, holding := range holdings {
		if holding.Exchange == "" {
			t.Errorf("Holding at index %d has empty exchange", i)
		}
		if holding.Tradingsymbol == "" {
			t.Errorf("Holding at index %d has empty tradingsymbol", i)
		}
		if holding.InstrumentToken == 0 {
			t.Errorf("Holding at index %d has zero instrument token", i)
		}
	}
}

func (ts *TestSuite) TestInitiateHoldingsAuth(t *testing.T) {
	t.Parallel()
	params := HoldingAuthParams{
		Instruments: []HoldingsAuthInstruments{
			{
				ISIN:     "INE002A01018",
				Quantity: 50,
			},
			{
				ISIN:     "INE009A01021",
				Quantity: 50,
			},
		},
	}
	response, err := ts.KiteConnect.InitiateHoldingsAuth(params)
	if err != nil {
		t.Errorf("Error while initiating holdings auth. %v", err)
	}

	if response.RequestID != "na8QgCeQm05UHG6NL9sAGRzdfSF64UdB" {
		t.Errorf("Error while parsing holdings auth response")
	}

	if !strings.Contains(response.RedirectURL, kiteBaseURI) {
		t.Errorf("Incorrect response URL")
	}
}
