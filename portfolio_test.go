package kiteconnect

import (
	"testing"
)

func (ts *TestSuite) TestGetPositions(t *testing.T) {
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
	holdings, err := ts.KiteConnect.GetHoldings()
	if err != nil {
		t.Errorf("Error while fetching holdings. %v", err)
	}
	for _, holding := range holdings {
		if holding.Tradingsymbol == "" {
			t.Errorf("Error while fetching tradingsymbol in holdings. %v", err)
		}
	}
}

func (ts *TestSuite) TestConvertPosition(t *testing.T) {
	params := ConvertPositionParams{
		Exchange:        "test",
		TradingSymbol:   "test",
		OldProduct:      "test",
		NewProduct:      "test",
		PositionType:    "test",
		TransactionType: "test",
		Quantity:        "test",
	}
	response, err := ts.KiteConnect.ConvertPosition(params)
	if err != nil || response != true {
		t.Errorf("Error while converting position. %v", err)
	}
}
