package kiteconnect

import "testing"

func (ts *TestSuite) TestGetOrderMargins(t *testing.T) {
	t.Parallel()

	params := OrderMarginParam{
		Exchange:        "NSE",
		Tradingsymbol:   "INFY",
		TransactionType: "BUY",
		Variety:         "regular",
		Product:         "CNC",
		OrderType:       "MARKET",
		Quantity:        1,
		Price:           0,
		TriggerPrice:    0,
	}

	orderResponse, err := ts.KiteConnect.GetOrderMargins(GetMarginParams{
		OrderParams: []OrderMarginParam{params},
		Compact:     true,
	})
	if err != nil {
		t.Errorf("Error while getting order margins: %v", err)
	}

	if len(orderResponse) != 1 {
		t.Errorf("Incorrect response, expected len(orderResponse) to be 0, got: %v", len(orderResponse))
	}

	if orderResponse[0].Total != 961.45 {
		t.Errorf("Incorrect total, expected 961.45, got: %v", orderResponse[0].Total)
	}
}

func (ts *TestSuite) TestGetBasketMargins(t *testing.T) {
	t.Parallel()

	params := OrderMarginParam{
		Exchange:        "NSE",
		Tradingsymbol:   "INFY",
		TransactionType: "BUY",
		Variety:         "regular",
		Product:         "CNC",
		OrderType:       "MARKET",
		Quantity:        1,
		Price:           0,
		TriggerPrice:    0,
	}

	orderResponseBasket, err := ts.KiteConnect.GetBasketMargins(GetBasketParams{
		OrderParams:       []OrderMarginParam{params},
		Compact:           true,
		ConsiderPositions: true,
	})
	if err != nil {
		t.Errorf("Error while getting basket order margins: %v", err)
	}

	if len(orderResponseBasket.Orders) != 2 {
		t.Errorf("Incorrect response, expected len(orderResponseBasket.Orders) to be 2, got: %v", len(orderResponseBasket.Orders))
	}
}
