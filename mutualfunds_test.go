package kiteconnect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func (ts *TestSuite) TestGetMFOrders(t *testing.T) {
	mfOrders, err := ts.KiteConnect.GetMFOrders()
	assert.Nil(t, err, "Error while fetching")
	for _, mfOrder := range mfOrders {
		assert.NotEqual(t, "", mfOrder.OrderID, "Error while fetching order id in MF orders.")
	}
}

func (ts *TestSuite) TestGetMFOrderInfo(t *testing.T) {
	orderInfo, err := ts.KiteConnect.GetMFOrderInfo("test")
	assert.Nil(t, err, "Error while fetching")
	assert.NotEqual(t, "", orderInfo.OrderID, "Error while fetching order id in MF order info.")
}

func (ts *TestSuite) TestPlaceMFOrder(t *testing.T) {
	params := MFOrderParams{
		Tradingsymbol:   "test",
		TransactionType: "test",
		Quantity:        100,
		Amount:          100,
		Tag:             "test",
	}
	orderResponse, err := ts.KiteConnect.PlaceMFOrder(params)
	assert.Nil(t, err, "Error while placing")
	assert.NotEqual(t, "", orderResponse.OrderID, "No order id returned while placing MF order.")
}

func (ts *TestSuite) TestGetMFSIPs(t *testing.T) {
	sips, err := ts.KiteConnect.GetMFSIPs()
	assert.Nil(t, err, "Error while fetching MF SIP")
	for _, sip := range sips {
		assert.NotEqual(t, "", sip.ID, "Error while fetching MF SIPs.")
	}
}

func (ts *TestSuite) TestGetMFSIPInfo(t *testing.T) {
	sip, err := ts.KiteConnect.GetMFSIPInfo("test")
	assert.Nil(t, err, "Error while fetching MF SIP Info.")
	assert.NotEqual(t, "", sip.ID, "Error while fetching MF SIP Info.")
}

func (ts *TestSuite) TestPlaceMFSIP(t *testing.T) {
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
	assert.Nil(t, err, "Error while placing MF SIP order.")
	assert.NotEqual(t, "", sipResponse.SIPID, "No SIP id returned while placing MF SIP Order.")
}

func (ts *TestSuite) TestModifyMFSIP(t *testing.T) {
	params := MFSIPModifyParams{
		Amount:        100,
		Frequency:     "test",
		InstalmentDay: 100,
		Instalments:   100,
		Status:        "test",
	}
	sipResponse, err := ts.KiteConnect.ModifyMFSIP("test", params)
	assert.Nil(t, err, "Error while modifying MF SIP order.")
	assert.NotEqual(t, "", sipResponse.SIPID, "No SIP id returned while modifying MF SIP Order.")
}

func (ts *TestSuite) TestCancelMFSIP(t *testing.T) {
	sipResponse, err := ts.KiteConnect.CancelMFSIP("test")
	assert.Nil(t, err, "Error while cancelling MF SIP order.")
	assert.NotEqual(t, "", sipResponse.SIPID, "No SIP id returned while cancelling MF SIP Order.")
}

func (ts *TestSuite) TestGetMFHoldings(t *testing.T) {
	holdings, err := ts.KiteConnect.GetMFHoldings()
	assert.Nil(t, err, "Error while fetching MF orders.")

	for _, holding := range holdings {
		assert.NotEqual(t, "", holding.Tradingsymbol, "Error while fetching Tradingsymbol in MF holdings.")
	}
}
