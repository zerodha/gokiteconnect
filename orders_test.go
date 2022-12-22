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
		require.Equal(t, "connect test order1", orders[1].Tag)
		require.Equal(t, []string{"connect test order2", "XXXXX"}, orders[2].Tags)
	})
	t.Run("test ice-berg and TTL orders", func(t *testing.T) {
		require.Equal(t, "iceberg", orders[3].Variety)
		require.Equal(t, "TTL", orders[3].Validity)
		require.Equal(t, 200.0, orders[3].Meta["iceberg"].(map[string]interface{})["leg_quantity"])
		require.Equal(t, 1000.0, orders[3].Meta["iceberg"].(map[string]interface{})["total_quantity"])
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

func (ts *TestSuite) TestPlaceIceBergOrder(t *testing.T) {
	t.Parallel()
	params := OrderParams{
		Exchange:        "test_iceberg",
		Tradingsymbol:   "test_iceberg",
		Validity:        "TTL",
		Product:         "test_iceberg",
		OrderType:       "test_iceberg",
		TransactionType: "test_iceberg",
		Quantity:        1000,
		Price:           100,
		IcebergLegs:     2,
		IcebergQty:      500,
		Tag:             "test_iceberg",
	}
	orderResponse, err := ts.KiteConnect.PlaceOrder("iceberg", params)
	if err != nil {
		t.Errorf("Error while placing iceberg order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
}

func (ts *TestSuite) TestPlaceCoOrder(t *testing.T) {
	t.Parallel()
	params := OrderParams{
		Exchange:        "test_co",
		Tradingsymbol:   "test_co",
		Validity:        "test_co",
		Product:         "test_co",
		OrderType:       "test_co",
		TransactionType: "test_co",
		Quantity:        100,
		Price:           101,
		TriggerPrice:    100,
		Tag:             "test_co",
	}
	orderResponse, err := ts.KiteConnect.PlaceOrder("co", params)
	if err != nil {
		t.Errorf("Error while placing co order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned. Error %v", err)
	}
}

func (ts *TestSuite) TestPlaceAuctionOrder(t *testing.T) {
	t.Parallel()
	params := OrderParams{
		Exchange:        "test_auction",
		Tradingsymbol:   "test_auction",
		Validity:        "test_auction",
		Product:         "test_auction",
		OrderType:       "test_auction",
		TransactionType: "test_auction",
		Quantity:        100,
		Price:           100,
		AuctionNumber:   7359,
		Tag:             "test_auction",
	}
	orderResponse, err := ts.KiteConnect.PlaceOrder("auction", params)
	if err != nil {
		t.Errorf("Error while placing auction order. %v", err)
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
