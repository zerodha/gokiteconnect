package kiteconnect

import (
	"testing"
)

func (ts *TestSuite) TestGetOrders(t *testing.T) {
	orders, err := ts.KiteConnect.GetOrders()
	if err != nil {
		t.Errorf("Error while fetching orders. %v", err)
	}
	for _, order := range orders {
		if order.OrderID == "" {
			t.Errorf("Error while fetching order id in orders. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetTrades(t *testing.T) {
	trades, err := ts.KiteConnect.GetTrades()
	if err != nil {
		t.Errorf("Error while fetching trades. %v", err)
	}
	for _, trade := range trades {
		if trade.TradeID == "" {
			t.Errorf("Error while fetching trade id in trades. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetOrderHistory(t *testing.T) {
	orderHistory, err := ts.KiteConnect.GetOrderHistory("test")
	if err != nil {
		t.Errorf("Error while fetching trades. %v", err)
	}
	for _, order := range orderHistory {
		if order.OrderID == "" {
			t.Errorf("Error while fetching order id in order history. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetOrderTrades(t *testing.T) {
	tradeHistory, err := ts.KiteConnect.GetOrderTrades("test")
	if err != nil {
		t.Errorf("Error while fetching trades. %v", err)
	}
	for _, trade := range tradeHistory {
		if trade.TradeID == "" {
			t.Errorf("Error while fetching trade id in trade history. %v", err)
		}
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
	if err != nil {
		t.Errorf("Error while placing order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
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
	if err != nil {
		t.Errorf("Error while placing order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
}

func (ts *TestSuite) TestCancelOrder(t *testing.T) {
	parentOrderID := "test"

	orderResponse, err := ts.KiteConnect.CancelOrder("test", "test", &parentOrderID)
	if err != nil {
		t.Errorf("Error while placing order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
}

func (ts *TestSuite) TestExitOrder(t *testing.T) {
	parentOrderID := "test"

	orderResponse, err := ts.KiteConnect.ExitOrder("test", "test", &parentOrderID)
	if err != nil {
		t.Errorf("Error while placing order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
}
