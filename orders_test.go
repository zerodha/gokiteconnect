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
		t.Fatalf("Error while fetching orders. %v", err)
	}
	if len(orders) == 0 {
		t.Fatal("No orders returned")
	}
	t.Run("test empty/unparsed orders", func(t *testing.T) {
		for _, order := range orders {
			require.NotEqual(t, "", order.OrderID)
		}
	})
	t.Run("test tag parsing", func(t *testing.T) {
		require.Equal(t, "", orders[0].Tag)
		require.Equal(t, "connect test order1", orders[4].Tag)
		require.Equal(t, []string{"connect test order2", "XXXXX"}, orders[5].Tags)
	})
	t.Run("test ice-berg and TTL orders", func(t *testing.T) {
		require.Equal(t, "iceberg", orders[3].Variety)
		require.Equal(t, false, orders[3].Modified)
		require.Equal(t, "TTL", orders[3].Validity)
		require.Equal(t, 200.0, orders[3].Meta["iceberg"].(map[string]interface{})["leg_quantity"])
		require.Equal(t, 1000.0, orders[3].Meta["iceberg"].(map[string]interface{})["total_quantity"])
	})
	t.Run("test auction order", func(t *testing.T) {
		require.Equal(t, "auction", orders[6].Variety)
		require.Equal(t, "22", orders[6].AuctionNumber)
		require.Equal(t, false, orders[6].Modified)
	})
	t.Run("test mtf order", func(t *testing.T) {
		require.Equal(t, "MTF", orders[7].Product)
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
		if order.Modified {
			t.Errorf("Error for not modified order. %v", err)
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
		AuctionNumber:   "7359",
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

func (ts *TestSuite) TestPlaceMTFOrder(t *testing.T) {
	t.Parallel()
	mtfParams := OrderParams{
		Exchange:        "test_mtf",
		Tradingsymbol:   "test_mtf",
		Validity:        "test_mtf",
		Product:         ProductMTF,
		OrderType:       "test_mtf",
		TransactionType: "test_mtf",
		Quantity:        100,
		Price:           100,
		Tag:             "test_mtf",
	}
	orderResponse, err := ts.KiteConnect.PlaceOrder("test", mtfParams)
	if err != nil {
		t.Errorf("Error while placing mtf order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No order id returned for placing mtf order. Error %v", err)
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

func TestAutosliceOrderResponse(t *testing.T) {
	t.Parallel()

	t.Run("success response", func(t *testing.T) {
		data := []byte(`{
			"order_id": "260318190751749",
			"children": [
				{"order_id": "260318190751750"},
				{"order_id": "260318190751751"}
			]
		}`)
		var resp OrderResponse
		err := json.Unmarshal(data, &resp)
		require.NoError(t, err)
		require.Equal(t, "260318190751749", resp.OrderID)
		require.Len(t, resp.Children, 2)
		require.Equal(t, "260318190751750", resp.Children[0].OrderID)
		require.Equal(t, "260318190751751", resp.Children[1].OrderID)
		require.Nil(t, resp.Children[0].Error)
	})

	t.Run("partial failure response", func(t *testing.T) {
		data := []byte(`{
			"order_id": "2034173850391977984",
			"children": [
				{"order_id": "2034173850391977985"},
				{
					"error": {
						"code": 400,
						"error_type": "MarginException",
						"message": "Insufficient funds. Required margin is 13751.67 but available margin is 13746.26.",
						"data": null
					}
				}
			]
		}`)
		var resp OrderResponse
		err := json.Unmarshal(data, &resp)
		require.NoError(t, err)
		require.Equal(t, "2034173850391977984", resp.OrderID)
		require.Len(t, resp.Children, 2)
		require.Equal(t, "2034173850391977985", resp.Children[0].OrderID)
		require.Nil(t, resp.Children[0].Error)
		require.NotNil(t, resp.Children[1].Error)
		require.Equal(t, 400, resp.Children[1].Error.Code)
		require.Equal(t, "MarginException", resp.Children[1].Error.ErrorType)
		require.Contains(t, resp.Children[1].Error.Message, "Insufficient funds")
	})

	t.Run("regular order backward compat", func(t *testing.T) {
		data := []byte(`{"order_id": "151220000000000"}`)
		var resp OrderResponse
		err := json.Unmarshal(data, &resp)
		require.NoError(t, err)
		require.Equal(t, "151220000000000", resp.OrderID)
		require.Empty(t, resp.Children)
	})
}

// TestPlaceAutosliceOrder tests autoslice order placement and response parsing.
// Note: In production, autoslice orders use variety "regular" with Autoslice: true.
// The test uses variety "autoslice" only for mock routing purposes.
func (ts *TestSuite) TestPlaceAutosliceOrder(t *testing.T) {
	t.Parallel()
	params := OrderParams{
		Exchange:        "NFO",
		Tradingsymbol:   "NIFTY26APRFUT",
		Validity:        "DAY",
		Product:         "NRML",
		OrderType:       "LIMIT",
		TransactionType: "BUY",
		Quantity:        1755,
		Price:           22693,
		Autoslice:       true,
	}
	orderResponse, err := ts.KiteConnect.PlaceOrder("autoslice", params)
	if err != nil {
		t.Errorf("Error while placing autoslice order. %v", err)
	}
	if orderResponse.OrderID == "" {
		t.Errorf("No parent order id returned. Error %v", err)
	}
	if len(orderResponse.Children) == 0 {
		t.Errorf("No children returned for autoslice order")
	}
	// Check that at least one child has an order_id
	hasOrderID := false
	for _, child := range orderResponse.Children {
		if child.OrderID != "" {
			hasOrderID = true
		}
	}
	if !hasOrderID {
		t.Errorf("No child order IDs returned")
	}
	// Check that partial failure child has error
	hasError := false
	for _, child := range orderResponse.Children {
		if child.Error != nil {
			hasError = true
			require.Equal(t, 400, child.Error.Code)
			require.Equal(t, "MarginException", child.Error.ErrorType)
		}
	}
	if !hasError {
		t.Errorf("Expected at least one child error in mock response")
	}
}

func (ts *TestSuite) TestIssue64(t *testing.T) {
	t.Parallel()
	orders, err := ts.KiteConnect.GetOrders()
	if err != nil {
		t.Errorf("Error while fetching orders. %v", err)
	}

	// Check if marshal followed by unmarshall correctly parses timestamps
	if len(orders) == 0 {
		t.Errorf("No orders returned, cannot test timestamp parsing")
		return
	}
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
