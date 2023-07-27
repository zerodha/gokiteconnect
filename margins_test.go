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

	compactOrderResp, err := ts.KiteConnect.GetOrderMargins(GetMarginParams{
		OrderParams: []OrderMarginParam{params},
		Compact:     true,
	})
	if err != nil {
		t.Errorf("Error while getting compact order margins: %v", err)
	}

	if len(compactOrderResp) != 1 {
		t.Errorf("Incorrect response length, expected len(compactOrderResp) to be 1, got: %v", len(compactOrderResp))
	}

	if compactOrderResp[0].TradingSymbol != "INFY" {
		t.Errorf("Incorrect tradingsymbol, expected INFY, got: %v", compactOrderResp[0].TradingSymbol)
	}

	if compactOrderResp[0].Total == 0 {
		t.Errorf("Incorrect compact total margins, got: %v", compactOrderResp[0].Total)
	}

	// Detailed order margin tests include charges, leverage
	detailOrderResp, err := ts.KiteConnect.GetOrderMargins(GetMarginParams{
		OrderParams: []OrderMarginParam{params},
		Compact:     false,
	})

	if err != nil {
		t.Errorf("Error while getting detailed order margins: %v", err)
	}

	if detailOrderResp[0].Leverage != 1 {
		t.Errorf("Incorrect leverage multiplier, expected 1x, got: %v", detailOrderResp[0].TradingSymbol)
	}

	if len(detailOrderResp) != 1 {
		t.Errorf("Incorrect response, expected len(detailOrderResp) to be 1, got: %v", len(detailOrderResp))
	}

	if detailOrderResp[0].Charges.TransactionTax == 0 {
		t.Errorf("Incorrect TransactionTax in detailed order margins, got: %v", detailOrderResp[0].Charges.TransactionTax)
	}

	if detailOrderResp[0].Charges.StampDuty == 0 {
		t.Errorf("Incorrect StampDuty in detailed order margins, got: %v", detailOrderResp[0].Charges.StampDuty)
	}

	if detailOrderResp[0].Charges.GST.Total == 0 {
		t.Errorf("Incorrect GST in detailed order margins, got: %v", detailOrderResp[0].Charges.GST.Total)
	}

	if detailOrderResp[0].Charges.Total == 0 {
		t.Errorf("Incorrect charges total in detailed order margins, got: %v", detailOrderResp[0].Charges.Total)
	}

	if detailOrderResp[0].Total == 0 {
		t.Errorf("Incorrect total margin in detailed order margins, got: %v", detailOrderResp[0].Total)
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
		t.Errorf("Error while getting compact basket order margins: %v", err)
	}

	if len(orderResponseBasket.Orders) != 2 {
		t.Errorf("Incorrect response, expected len(orderResponseBasket.Orders) to be 2, got: %v", len(orderResponseBasket.Orders))
	}
}

func (ts *TestSuite) TestGetOrderCharges(t *testing.T) {
	t.Parallel()

	params :=
		[]OrderChargesParam{
			{
				Exchange:        "SBIN",
				Tradingsymbol:   "INFY",
				TransactionType: "BUY",
				Variety:         "regular",
				Product:         "CNC",
				OrderType:       "MARKET",
				Quantity:        1,
				AveragePrice:    560,
				OrderID:         "11111",
			},
			{
				Exchange:        "MCX",
				Tradingsymbol:   "GOLDPETAL23JULFUT",
				TransactionType: "SELL",
				Variety:         "regular",
				Product:         "NRML",
				OrderType:       "LIMIT",
				Quantity:        1,
				AveragePrice:    5862,
				OrderID:         "11111",
			},
			{
				Exchange:        "NFO",
				Tradingsymbol:   "NIFTY2371317900PE",
				TransactionType: "BUY",
				Variety:         "regular",
				Product:         "NRML",
				OrderType:       "LIMIT",
				Quantity:        100,
				AveragePrice:    1.5,
				OrderID:         "11111",
			},
		}

	orderResponseCharges, err := ts.KiteConnect.GetOrderCharges(GetChargesParams{
		OrderParams: params,
	})
	if err != nil {
		t.Errorf("Error while getting order charges: %v", err)
	}

	if len(orderResponseCharges) != 3 {
		t.Errorf("Incorrect response, expected len(orderResponseCharges) to be 3, got: %v", len(orderResponseCharges))
	}
}
