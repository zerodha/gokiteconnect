package kite

import (
	"fmt"
	"math"
	"strings"
)

// Charge schedules for equity and equity-derivative orders, from Zerodha's
// published rates: brokerage, the transaction tax (STT), exchange and SEBI
// turnover charges, GST on those, and stamp duty.
const (
	ChargesDelivery = "EQUITY_DELIVERY"
	ChargesIntraday = "EQUITY_INTRADAY"
	ChargesFutures  = "EQUITY_FUTURES"
	ChargesOptions  = "EQUITY_OPTIONS"
)

const (
	brokerageRate      = 0.0003
	brokerageCap       = 20.0
	gstRate            = 0.18
	sebiChargePerRupee = 10.0 / 10_000_000.0
	deliverySTTRate    = 0.001
	intradaySTTRate    = 0.00025
	futuresSTTRate     = 0.0005
	optionsSTTRate     = 0.0015
	deliveryStampRate  = 0.00015
	intradayStampRate  = 0.00003
	futuresStampRate   = 0.00002
	optionsStampRate   = 0.00003
	nseEquityTxnRate   = 0.0000307
	bseEquityTxnRate   = 0.0000375
	nseFuturesTxnRate  = 0.0000183
	nseOptionsTxnRate  = 0.0003553
	bseOptionsTxnRate  = 0.000325
)

// ChargesOrder is one executed order to work out the charges of. Turnover
// is its executed value, quantity times price. ExercisedIntrinsicValue is
// the intrinsic value of bought options that were exercised.
type ChargesOrder struct {
	// Schedule is ChargesDelivery, ChargesIntraday, ChargesFutures or
	// ChargesOptions.
	Schedule string
	// Exchange is NSE or BSE; a derivative's NFO and BFO count as these.
	Exchange                string
	TransactionType         string
	Turnover                float64
	ExercisedIntrinsicValue float64
}

// CalculateCharges works out an executed order's charges locally, without
// Kite's charges calculator. The amounts are unrounded rupees.
func CalculateCharges(order ChargesOrder) (Charges, error) {
	order.Schedule = strings.ToUpper(strings.TrimSpace(order.Schedule))
	order.Exchange = chargesExchange(order.Exchange)
	order.TransactionType = strings.ToUpper(strings.TrimSpace(order.TransactionType))
	if err := validateChargesOrder(order); err != nil {
		return Charges{}, err
	}

	buy, sell := order.TransactionType == TransactionTypeBuy, order.TransactionType == TransactionTypeSell
	turnover := order.Turnover
	charges := Charges{TransactionTaxType: "stt", SEBITurnoverCharge: turnover * sebiChargePerRupee}
	switch order.Schedule {
	case ChargesDelivery:
		charges.TransactionTax = turnover * deliverySTTRate
		charges.ExchangeTurnoverCharge = turnover * equityTransactionRate(order.Exchange)
		if buy {
			charges.StampDuty = turnover * deliveryStampRate
		}
	case ChargesIntraday:
		charges.Brokerage = math.Min(turnover*brokerageRate, brokerageCap)
		if sell {
			charges.TransactionTax = turnover * intradaySTTRate
		}
		charges.ExchangeTurnoverCharge = turnover * equityTransactionRate(order.Exchange)
		if buy {
			charges.StampDuty = turnover * intradayStampRate
		}
	case ChargesFutures:
		charges.Brokerage = math.Min(turnover*brokerageRate, brokerageCap)
		if sell {
			charges.TransactionTax = turnover * futuresSTTRate
		}
		if order.Exchange == "NSE" {
			charges.ExchangeTurnoverCharge = turnover * nseFuturesTxnRate
		}
		if buy {
			charges.StampDuty = turnover * futuresStampRate
		}
	case ChargesOptions:
		charges.Brokerage = brokerageCap
		if sell {
			charges.TransactionTax = turnover * optionsSTTRate
		} else if order.ExercisedIntrinsicValue > 0 {
			charges.TransactionTax = order.ExercisedIntrinsicValue * optionsSTTRate
		}
		charges.ExchangeTurnoverCharge = turnover * optionsTransactionRate(order.Exchange)
		if buy {
			charges.StampDuty = turnover * optionsStampRate
		}
	}

	charges.GST.IGST = (charges.Brokerage + charges.SEBITurnoverCharge + charges.ExchangeTurnoverCharge) * gstRate
	charges.GST.Total = charges.GST.IGST
	charges.Total = charges.Brokerage + charges.TransactionTax + charges.ExchangeTurnoverCharge +
		charges.SEBITurnoverCharge + charges.GST.Total + charges.StampDuty
	return charges, nil
}

// EstimateOrderCharges works out the charges of orders locally, answering
// like GetOrderCharges: one item per order, in order. Each order's schedule
// comes from its exchange, product and trading symbol.
func EstimateOrderCharges(orders []OrderChargesParam) ([]OrderCharges, error) {
	out := make([]OrderCharges, 0, len(orders))
	for _, order := range orders {
		schedule, err := ChargesSchedule(order.Exchange, order.Tradingsymbol, order.Product)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", order.Tradingsymbol, err)
		}
		charges, err := CalculateCharges(ChargesOrder{
			Schedule:        schedule,
			Exchange:        order.Exchange,
			TransactionType: order.TransactionType,
			Turnover:        order.Quantity * order.AveragePrice,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", order.Tradingsymbol, err)
		}
		out = append(out, OrderCharges{
			Exchange:        order.Exchange,
			Tradingsymbol:   order.Tradingsymbol,
			TransactionType: order.TransactionType,
			Variety:         order.Variety,
			Product:         order.Product,
			OrderType:       order.OrderType,
			Quantity:        order.Quantity,
			Price:           order.AveragePrice,
			Charges:         charges,
		})
	}
	return out, nil
}

// ChargesSchedule is the charge schedule of an order: options and futures
// on NFO and BFO by their trading symbol's CE, PE or FUT suffix, and shares
// on NSE and BSE as intraday for MIS and delivery otherwise.
func ChargesSchedule(exchange, tradingsymbol, product string) (string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(tradingsymbol))
	switch strings.ToUpper(strings.TrimSpace(exchange)) {
	case "NFO", "BFO":
		switch {
		case strings.HasSuffix(symbol, "FUT"):
			return ChargesFutures, nil
		case strings.HasSuffix(symbol, "CE"), strings.HasSuffix(symbol, "PE"):
			return ChargesOptions, nil
		}
		return "", fmt.Errorf("unknown derivative %q", tradingsymbol)
	case "NSE", "BSE":
		if strings.EqualFold(product, ProductMIS) {
			return ChargesIntraday, nil
		}
		return ChargesDelivery, nil
	}
	return "", fmt.Errorf("no charge schedule for exchange %q", exchange)
}

// chargesExchange is the exchange whose rates apply: NFO is NSE's and BFO
// is BSE's.
func chargesExchange(exchange string) string {
	switch exchange = strings.ToUpper(strings.TrimSpace(exchange)); exchange {
	case "NFO":
		return "NSE"
	case "BFO":
		return "BSE"
	}
	return exchange
}

func validateChargesOrder(order ChargesOrder) error {
	switch order.Schedule {
	case ChargesDelivery, ChargesIntraday, ChargesFutures, ChargesOptions:
	default:
		return fmt.Errorf("unsupported charge schedule %q", order.Schedule)
	}
	if order.Exchange != "NSE" && order.Exchange != "BSE" {
		return fmt.Errorf("unsupported exchange %q", order.Exchange)
	}
	if order.TransactionType != TransactionTypeBuy && order.TransactionType != TransactionTypeSell {
		return fmt.Errorf("unsupported transaction type %q", order.TransactionType)
	}
	if order.Turnover <= 0 || math.IsNaN(order.Turnover) || math.IsInf(order.Turnover, 0) {
		return fmt.Errorf("turnover must be positive and finite")
	}
	if order.ExercisedIntrinsicValue < 0 || math.IsNaN(order.ExercisedIntrinsicValue) || math.IsInf(order.ExercisedIntrinsicValue, 0) {
		return fmt.Errorf("exercised intrinsic value must be non-negative and finite")
	}
	if order.Schedule != ChargesOptions && order.ExercisedIntrinsicValue != 0 {
		return fmt.Errorf("exercised intrinsic value is only valid for options")
	}
	if order.TransactionType == TransactionTypeSell && order.ExercisedIntrinsicValue != 0 {
		return fmt.Errorf("exercised intrinsic value is only valid for bought options")
	}
	return nil
}

func equityTransactionRate(exchange string) float64 {
	if exchange == "BSE" {
		return bseEquityTxnRate
	}
	return nseEquityTxnRate
}

func optionsTransactionRate(exchange string) float64 {
	if exchange == "BSE" {
		return bseOptionsTxnRate
	}
	return nseOptionsTxnRate
}
