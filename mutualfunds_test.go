package kiteconnect

import (
	"testing"
)

func (ts *TestSuite) TestGetMFOrders(t *testing.T) {
	t.Parallel()
	mfOrders, err := ts.KiteConnect.GetMFOrders()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}
	for _, mfOrder := range mfOrders {
		if mfOrder.OrderID == "" {
			t.Errorf("Error while fetching order id in MF orders. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetMFOrderInfo(t *testing.T) {
	t.Parallel()
	orderInfo, err := ts.KiteConnect.GetMFOrderInfo("test")
	if err != nil {
		t.Errorf("Error while fetching trades. %v", err)
	}
	if orderInfo.OrderID == "" {
		t.Errorf("Error while fetching order id in MF order info. %v", err)
	}
}

func (ts *TestSuite) TestPlaceMFOrder(t *testing.T) {
	t.Parallel()
	params := MFOrderParams{
		Tradingsymbol:   "test",
		TransactionType: "test",
		Quantity:        100,
		Amount:          100,
		Tag:             "test",
	}
	orderResponse, err := ts.KiteConnect.PlaceMFOrder(params)
	if err != nil {
		t.Errorf("Error while placing MF order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned while placing MF order. Error %v", err)
	}
}

func (ts *TestSuite) TestGetMFSIPs(t *testing.T) {
	t.Parallel()
	sips, err := ts.KiteConnect.GetMFSIPs()
	if err != nil {
		t.Errorf("Error while fetching MF SIPs. %v", err)
	}
	for _, sip := range sips {
		if sip.ID == "" {
			t.Errorf("Error while fetching id in MF SIP. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetMFSIPInfo(t *testing.T) {
	t.Parallel()
	sip, err := ts.KiteConnect.GetMFSIPInfo("test")
	if err != nil || sip.ID == "" {
		t.Errorf("Error while fetching MF SIP Info. %v", err)
	}
}

func (ts *TestSuite) TestPlaceMFSIP(t *testing.T) {
	t.Parallel()
	params := MFSIPParams{
		Tradingsymbol: "test",
		Amount:        100,
		Instalments:   100,
		Frequency:     "4",
		InstalmentDay: 2,
		InitialAmount: 2000,
		Tag:           "test",
	}
	sipResponse, err := ts.KiteConnect.PlaceMFSIP(params)
	if err != nil {
		t.Errorf("Error while placing MF SIP order. %v", err)
	}
	if sipResponse.SIPID == "" {
		t.Errorf("No SIP id returned while placing MF SIP Order. Error %v", err)
	}
}

func (ts *TestSuite) TestModifyMFSIP(t *testing.T) {
	t.Parallel()
	params := MFSIPModifyParams{
		Amount:        100,
		Frequency:     "test",
		InstalmentDay: 100,
		Instalments:   100,
		Status:        "test",
	}
	sipResponse, err := ts.KiteConnect.ModifyMFSIP("test", params)
	if err != nil {
		t.Errorf("Error while modifying MF SIP order. %v", err)
	}
	if sipResponse.SIPID == "" {
		t.Errorf("No SIP id returned while modifying MF SIP Order. Error %v", err)
	}
}

func (ts *TestSuite) TestCancelMFSIP(t *testing.T) {
	t.Parallel()
	sipResponse, err := ts.KiteConnect.CancelMFSIP("test")
	if err != nil {
		t.Errorf("Error while canceling MF SIP order. %v", err)
	}
	if sipResponse.SIPID == "" {
		t.Errorf("No SIP id returned while canceling MF SIP Order. Error %v", err)
	}
}

func (ts *TestSuite) TestGetMFHoldings(t *testing.T) {
	t.Parallel()
	holdings, err := ts.KiteConnect.GetMFHoldings()
	if err != nil {
		t.Errorf("Error while fetching MF orders. %v", err)
	}
	for _, holding := range holdings {
		if holding.Tradingsymbol == "" {
			t.Errorf("Error while fetching Tradingsymbol in MF holdings. %v", err)
		}
	}
}
