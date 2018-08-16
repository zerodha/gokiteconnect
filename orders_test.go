package kiteconnect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func (ts *TestSuite) TestGetOrders(t *testing.T) {
	orders, err := ts.KiteConnect.GetOrders()
	assert.Nil(t, err, "Error while fetching orders")
	for _, order := range orders {
		assert.NotEqual(t, 0, order.OrderID, "Error while fetching order id in orders.")
	}
}

func (ts *TestSuite) TestGetTrades(t *testing.T) {
	trades, err := ts.KiteConnect.GetTrades()
	assert.Nil(t, err, "Error while fetching trades.")
	for _, trade := range trades {
		assert.NotEqual(t, "", trade.TradeID, "Error while fetching trade id in trades.")
	}
}

func (ts *TestSuite) TestGetOrderHistory(t *testing.T) {
	orderHistory, err := ts.KiteConnect.GetOrderHistory("test")
	assert.Nil(t, err, "Error while fetching trades.")
	for _, order := range orderHistory {
		assert.NotEqual(t, "", order.OrderID, "Error while fetching trade id in trades.")
	}
}

func (ts *TestSuite) TestGetOrderTrades(t *testing.T) {
	tradeHistory, err := ts.KiteConnect.GetOrderTrades("test")
	assert.Nil(t, err, "Error while fetching trades.")
	for _, trade := range tradeHistory {
		assert.NotEqual(t, "", trade.TradeID, "Error while fetching trade id in trades.")
	}
}

func (ts *TestSuite) TestPlaceOrder(t *testing.T) {
	params := OrderParams{
		Exchange:          "test",
		Tradingsymbol:     "test",
		Validity:          "test",
		Product:           "test",
		OrderType:         "test",
		TransactionType:   "test",
		Quantity:          100,
		DisclosedQuantity: 100,
		Price:             100,
		TriggerPrice:      100,
		Squareoff:         100,
		Stoploss:          100,
		TrailingStoploss:  100,
		Tag:               "test",
	}
	orderResponse, err := ts.KiteConnect.PlaceOrder("test", params)
	assert.Nil(t, err, "Error while placing order.")
	assert.NotEqual(t, "", orderResponse.OrderID, "Error while fetching trade id in trades.")
}

func (ts *TestSuite) TestModifyOrder(t *testing.T) {
	params := OrderParams{
		Exchange:          "test",
		Tradingsymbol:     "test",
		Validity:          "test",
		Product:           "test",
		OrderType:         "test",
		TransactionType:   "test",
		Quantity:          100,
		DisclosedQuantity: 100,
		Price:             100,
		TriggerPrice:      100,
		Squareoff:         100,
		Stoploss:          100,
		TrailingStoploss:  100,
		Tag:               "test",
	}
	orderResponse, err := ts.KiteConnect.ModifyOrder("test", "test", params)
	assert.Nil(t, err, "Error while placing order.")
	assert.NotEqual(t, "", orderResponse.OrderID, "Error while fetching trade id in trades.")
}

func (ts *TestSuite) TestCancelOrder(t *testing.T) {
	parentOrderID := "test"

	orderResponse, err := ts.KiteConnect.CancelOrder("test", "test", &parentOrderID)
	assert.Nil(t, err, "Error while cancelling order.")
	assert.NotEqual(t, "", orderResponse.OrderID, "No order id returned.")
}

func (ts *TestSuite) TestExitOrder(t *testing.T) {
	parentOrderID := "test"

	orderResponse, err := ts.KiteConnect.ExitOrder("test", "test", &parentOrderID)
	assert.Nil(t, err, "Error while exit order.")
	assert.NotEqual(t, "", orderResponse.OrderID, "No order id returned.")
}
