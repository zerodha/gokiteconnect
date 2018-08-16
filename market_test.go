package kiteconnect

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func (ts *TestSuite) TestGetQuote(t *testing.T) {
	marketQuote, err := ts.KiteConnect.GetQuote()
	assert.Nil(t, err, "Error while fetching")
	q, ok := marketQuote["NSE:INFY"]
	assert.True(t, ok, "Does not have required key")
	assert.Equal(t, 408065, q.InstrumentToken, "Incorrect values set.")
}

func (ts *TestSuite) TestGetLTP(t *testing.T) {
	marketLTP, err := ts.KiteConnect.GetLTP()
	assert.Nil(t, err, "Error while fetching")

	ltp, ok := marketLTP["NSE:INFY"]
	assert.True(t, ok, "Does not have required key")
	assert.Equal(t, 408065, ltp.InstrumentToken, "Incorrect values set.")
}

func (ts *TestSuite) TestGetHistoricalData(t *testing.T) {
	marketHistorical, err := ts.KiteConnect.GetHistoricalData(123, "myinterval", time.Unix(0, 0), time.Unix(1, 0), true)
	assert.Nil(t, err, "Error while fetching")

	for i := 0; i < len(marketHistorical)-1; i++ {
		assert.Condition(t, func() bool {
			return marketHistorical[i].Date.Unix() < marketHistorical[i+1].Date.Unix()
		}, "Unsorted candles returned. %v")
	}
}

func (ts *TestSuite) TestGetOHLC(t *testing.T) {
	marketOHLC, err := ts.KiteConnect.GetOHLC()
	assert.Nil(t, err, "Error while fetching")

	ohlc, ok := marketOHLC["NSE:INFY"]
	assert.True(t, ok, "Does not have required key")
	assert.Equal(t, 408065, ohlc.InstrumentToken, "Incorrect values set.")
}

func (ts *TestSuite) TestGetInstruments(t *testing.T) {
	marketInstruments, err := ts.KiteConnect.GetInstruments()
	assert.Nil(t, err, "Error while fetching")

	for _, mInstr := range marketInstruments {
		assert.NotEqual(t, 0, mInstr.InstrumentToken, "Incorrect data loaded. %v")
	}
}

func (ts *TestSuite) TestGetInstrumentsByExchange(t *testing.T) {
	marketInstruments, err := ts.KiteConnect.GetInstrumentsByExchange("nse")
	assert.Nil(t, err, "Error while fetching")

	for _, mInstr := range marketInstruments {
		assert.Equal(t, "NSE", mInstr.Exchange, "Incorrect data loaded. %v")
	}
}

func (ts *TestSuite) TestGetMFInstruments(t *testing.T) {
	marketInstruments, err := ts.KiteConnect.GetMFInstruments()
	assert.Nil(t, err, "Error while fetching")

	for _, mInstr := range marketInstruments {
		assert.NotEqual(t, "", mInstr.Tradingsymbol, "Incorrect data loaded. %v")
	}
}
