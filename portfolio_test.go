package kiteconnect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func (ts *TestSuite) TestGetPositions(t *testing.T) {
	positions, err := ts.KiteConnect.GetPositions()
	assert.Nil(t, err, "Error while fetching positions")
	assert.NotNil(t, positions.Day, "Error while fetching day positions")
	assert.NotNil(t, positions.Net, "Error while fetching net positions")

	for _, position := range positions.Day {
		assert.NotEqual(t, "", position.Tradingsymbol, "Error while fetching trading symbol in day position.")
	}
	for _, position := range positions.Net {
		assert.NotEqual(t, "", position.Tradingsymbol, "Error while fetching tradingsymbol in net position.")
	}
}

func (ts *TestSuite) TestGetHoldings(t *testing.T) {
	holdings, err := ts.KiteConnect.GetHoldings()
	assert.Nil(t, err, "Error while fetching holdings")
	for _, holding := range holdings {
		assert.NotEqual(t, "", holding.Tradingsymbol, "Error while fetching tradingsymbol in net position.")
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
	assert.Nil(t, err, "Error while converting position")
	assert.True(t, response, "Error while converting position")
}
