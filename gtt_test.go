package kiteconnect

import (
	"testing"
)

func (ts *TestSuite) TestGetGTTs(t *testing.T) {
	t.Parallel()
	gttOrders, err := ts.KiteConnect.GetGTTs()
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	for _, gttOrder := range gttOrders {
		if gttOrder.ID == 0 {
			t.Errorf("Error while parsing order id in GTT orders. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.GetGTT(123)
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.ID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestModifyGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.ModifyGTT(123, GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: TransactionTypeBuy,
		Trigger: &GTTSingleLegTrigger{
			TriggerParams: TriggerParams{
				TriggerValue: 2,
				Quantity:     2,
				LimitPrice:   2,
			},
		},
	})
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestPlaceGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.PlaceGTT(GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: TransactionTypeBuy,
		Trigger: &GTTSingleLegTrigger{
			TriggerParams: TriggerParams{
				TriggerValue: 1,
				Quantity:     1,
				LimitPrice:   1,
			},
		},
	})
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestDeleteGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.DeleteGTT(123)
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}
