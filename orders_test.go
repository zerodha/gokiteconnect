package kiteconnect

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func (ts *TestSuite) TestGetOrders(t *testing.T) {
	t.Parallel()
	orders, err := ts.KiteConnect.GetOrders()
	if err != nil {
		t.Errorf("Error while fetching orders. %v", err)
	}
	t.Run("test empty/unparsed orders", func(t *testing.T) {
		for _, order := range orders {
			require.NotEqual(t, "", order.OrderID)
		}
	})
	t.Run("test tag parsing", func(t *testing.T) {
		require.Equal(t, "", orders[0].Tag)
		require.Equal(t, "connect test order1", orders[3].Tag)
		require.Equal(t, []string{"connect test order2", "XXXXX"}, orders[4].Tags)
	})
}

func (ts *TestSuite) TestGetTrades(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	parentOrderID := "test"

	orderResponse, err := ts.KiteConnect.CancelOrder("test", "test", &parentOrderID)
	if err != nil || orderResponse.OrderID == "" {
		t.Errorf("Error while placing cancel order. %v", err)
	}
}

func (ts *TestSuite) TestExitOrder(t *testing.T) {
	t.Parallel()
	parentOrderID := "test"

	orderResponse, err := ts.KiteConnect.ExitOrder("test", "test", &parentOrderID)
	if err != nil {
		t.Errorf("Error while placing order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
}

func (ts *TestSuite) TestIssue64(t *testing.T) {
	t.Parallel()
	orders, err := ts.KiteConnect.GetOrders()
	if err != nil {
		t.Errorf("Error while fetching orders. %v", err)
	}

	// Check if marshal followed by unmarshall correctly parses timestamps
	ord := orders[0]
	js, err := json.Marshal(ord)
	if err != nil {
		t.Errorf("Error while marshalling order. %v", err)
	}

	var outOrd Order
	err = json.Unmarshal(js, &outOrd)
	if err != nil {
		t.Errorf("Error while unmarshalling order. %v", err)
	}

	if !ord.ExchangeTimestamp.Equal(outOrd.ExchangeTimestamp.Time) {
		t.Errorf("Incorrect timestamp parsing.\nwant:\t%v\ngot:\t%v", ord.ExchangeTimestamp, outOrd.ExchangeTimestamp)
	}
}
