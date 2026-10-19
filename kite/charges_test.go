package kite

import (
	"math"
	"testing"
)

func TestCalculateCharges(t *testing.T) {
	tests := []struct {
		name  string
		order ChargesOrder
		want  Charges
	}{
		{
			name:  "NSE delivery buy",
			order: ChargesOrder{Schedule: ChargesDelivery, Exchange: "NSE", TransactionType: TransactionTypeBuy, Turnover: 100_000},
			want:  Charges{TransactionTax: 100, ExchangeTurnoverCharge: 3.07, SEBITurnoverCharge: 0.1, GST: GST{Total: 0.5706}, StampDuty: 15, Total: 118.7406},
		},
		{
			name:  "NSE intraday sell brokerage capped",
			order: ChargesOrder{Schedule: ChargesIntraday, Exchange: "NSE", TransactionType: TransactionTypeSell, Turnover: 100_000},
			want:  Charges{Brokerage: 20, TransactionTax: 25, ExchangeTurnoverCharge: 3.07, SEBITurnoverCharge: 0.1, GST: GST{Total: 4.1706}, Total: 52.3406},
		},
		{
			name:  "NFO futures buy",
			order: ChargesOrder{Schedule: ChargesFutures, Exchange: "NFO", TransactionType: TransactionTypeBuy, Turnover: 100_000},
			want:  Charges{Brokerage: 20, ExchangeTurnoverCharge: 1.83, SEBITurnoverCharge: 0.1, GST: GST{Total: 3.9474}, StampDuty: 2, Total: 27.8774},
		},
		{
			name:  "NSE options sell",
			order: ChargesOrder{Schedule: ChargesOptions, Exchange: "NSE", TransactionType: TransactionTypeSell, Turnover: 100_000},
			want:  Charges{Brokerage: 20, TransactionTax: 150, ExchangeTurnoverCharge: 35.53, SEBITurnoverCharge: 0.1, GST: GST{Total: 10.0134}, Total: 215.6434},
		},
		{
			name:  "BSE exercised option buy",
			order: ChargesOrder{Schedule: ChargesOptions, Exchange: "BFO", TransactionType: TransactionTypeBuy, Turnover: 50_000, ExercisedIntrinsicValue: 100_000},
			want:  Charges{Brokerage: 20, TransactionTax: 150, ExchangeTurnoverCharge: 16.25, SEBITurnoverCharge: 0.05, GST: GST{Total: 6.534}, StampDuty: 1.5, Total: 194.334},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CalculateCharges(test.order)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []struct {
				name      string
				got, want float64
			}{
				{"Brokerage", got.Brokerage, test.want.Brokerage},
				{"TransactionTax", got.TransactionTax, test.want.TransactionTax},
				{"ExchangeTurnoverCharge", got.ExchangeTurnoverCharge, test.want.ExchangeTurnoverCharge},
				{"SEBITurnoverCharge", got.SEBITurnoverCharge, test.want.SEBITurnoverCharge},
				{"GST", got.GST.Total, test.want.GST.Total},
				{"StampDuty", got.StampDuty, test.want.StampDuty},
				{"Total", got.Total, test.want.Total},
			} {
				if math.Abs(value.got-value.want) > 1e-6 {
					t.Errorf("%s = %.8f, want %.8f", value.name, value.got, value.want)
				}
			}
		})
	}
}

func TestCalculateChargesLowTurnoverBrokerage(t *testing.T) {
	got, err := CalculateCharges(ChargesOrder{Schedule: ChargesIntraday, Exchange: "NSE", TransactionType: TransactionTypeBuy, Turnover: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.Brokerage-3) > 1e-6 {
		t.Fatalf("brokerage = %.6f, want 3", got.Brokerage)
	}
}

func TestCalculateChargesRejectsInvalidInput(t *testing.T) {
	for _, order := range []ChargesOrder{
		{Schedule: "CRYPTO", Exchange: "NSE", TransactionType: TransactionTypeBuy, Turnover: 1},
		{Schedule: ChargesDelivery, Exchange: "MCX", TransactionType: TransactionTypeBuy, Turnover: 1},
		{Schedule: ChargesDelivery, Exchange: "NSE", TransactionType: "HOLD", Turnover: 1},
		{Schedule: ChargesDelivery, Exchange: "NSE", TransactionType: TransactionTypeBuy, Turnover: 0},
		{Schedule: ChargesFutures, Exchange: "NSE", TransactionType: TransactionTypeBuy, Turnover: 1, ExercisedIntrinsicValue: 1},
	} {
		if _, err := CalculateCharges(order); err == nil {
			t.Errorf("CalculateCharges(%+v) was accepted", order)
		}
	}
}

func TestEstimateOrderCharges(t *testing.T) {
	got, err := EstimateOrderCharges([]OrderChargesParam{
		{Exchange: "NFO", Tradingsymbol: "NIFTY26OCT25000PE", TransactionType: TransactionTypeSell, Product: ProductNRML, Quantity: 100, AveragePrice: 1000},
		{Exchange: "NSE", Tradingsymbol: "INFY", TransactionType: TransactionTypeSell, Product: ProductMIS, Quantity: 100, AveragePrice: 1000},
		{Exchange: "NSE", Tradingsymbol: "INFY", TransactionType: TransactionTypeBuy, Product: ProductCNC, Quantity: 100, AveragePrice: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []float64{215.6434, 52.3406, 118.7406} {
		if math.Abs(got[index].Charges.Total-want) > 1e-6 || got[index].Price != 1000 {
			t.Errorf("order %d = %+v, want total %v", index, got[index], want)
		}
	}
	if _, err := EstimateOrderCharges([]OrderChargesParam{{Exchange: "MCX", Tradingsymbol: "GOLD", TransactionType: TransactionTypeBuy, Quantity: 1, AveragePrice: 1}}); err == nil {
		t.Error("an MCX order was accepted")
	}
	if schedule, err := ChargesSchedule("BFO", "SENSEX26OCTFUT", ProductNRML); err != nil || schedule != ChargesFutures {
		t.Errorf("BFO future = %q, %v", schedule, err)
	}
}
