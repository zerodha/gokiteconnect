package kiteconnect

import (
	"testing"
	"time"
)

func (ts *TestSuite) TestGetQuote(t *testing.T) {
	marketQuote, err := ts.KiteConnect.GetQuote()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	if q, ok := marketQuote["NSE:INFY"]; ok {
		if q.InstrumentToken != 408065 {
			t.Errorf("Incorrect values set. %v", err)
		}
	} else {
		t.Errorf("Key wanted but not found. %v", err)
	}
}

func (ts *TestSuite) TestGetLTP(t *testing.T) {
	marketLTP, err := ts.KiteConnect.GetLTP()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	if ltp, ok := marketLTP["NSE:INFY"]; ok {
		if ltp.InstrumentToken != 408065 {
			t.Errorf("Incorrect values set. %v", err)
		}
	} else {
		t.Errorf("Key wanted but not found. %v", err)
	}
}

func (ts *TestSuite) TestGetHistoricalData(t *testing.T) {
	marketHistorical, err := ts.KiteConnect.GetHistoricalData(123, "interval", time.Now(), time.Now(), true)
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	for i := 0; i < len(marketHistorical)-1; i++ {
		if marketHistorical[i].Date.Unix() > marketHistorical[i-1].Date.Unix() {
			t.Errorf("Unsorted candles returned. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetOHLC(t *testing.T) {
	marketOHLC, err := ts.KiteConnect.GetOHLC()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	if ohlc, ok := marketOHLC["NSE:INFY"]; ok {
		if ohlc.InstrumentToken != 408065 {
			t.Errorf("Incorrect values set. %v", err)
		}
	} else {
		t.Errorf("Key wanted but not found. %v", err)
	}
}

func (ts *TestSuite) TestGetInstruments(t *testing.T) {
	marketInstruments, err := ts.KiteConnect.GetInstruments()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	for _, mInstr := range marketInstruments {
		if mInstr.InstrumentToken == 0 {
			t.Errorf("Incorrect data loaded. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetInstrumentsByExchange(t *testing.T) {
	marketInstruments, err := ts.KiteConnect.GetInstrumentsByExchange("nse")
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	for _, mInstr := range marketInstruments {
		if mInstr.Exchange != "NSE" {
			t.Errorf("Incorrect data loaded. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetMFInstruments(t *testing.T) {
	marketInstruments, err := ts.KiteConnect.GetMFInstruments()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}

	for _, mInstr := range marketInstruments {
		if mInstr.Tradingsymbol == "" {
			t.Errorf("Incorrect data loaded. %v", err)
		}
	}
}
