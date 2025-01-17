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
