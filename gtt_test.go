package kiteconnect

import (
	"testing"
)

func (ts *TestSuite) TestGetGTTOrders(t *testing.T) {
	t.Parallel()
	gttOrders, err := ts.KiteConnect.GetGTTOrders()
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	for _, gttOrder := range gttOrders {
		if gttOrder.ID == 0 {
			t.Errorf("Error while parsing order id in GTT orders. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetGTTOrder(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.GetGTTOrder(123)
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.ID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestModifyGTTOrder(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.ModifyGTTOrder(123, GTTOrderParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: TransactionTypeBuy,
		Type:            GTTOrderTypeSingle,
		TriggerValues:   []float64{2},
		Quantities:      []float64{2},
		LimitPrices:     []float64{2},
	})
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestPlaceGTTOrder(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.PlaceGTTOrder(GTTOrderParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: TransactionTypeBuy,
		Type:            GTTOrderTypeSingle,
		TriggerValues:   []float64{1},
		Quantities:      []float64{1},
		LimitPrices:     []float64{1},
	})
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestDeleteGTTOrder(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.DeleteGTTOrder(123)
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}
