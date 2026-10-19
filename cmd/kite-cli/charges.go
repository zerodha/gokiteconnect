package main

import (
	"strings"

	"github.com/devshoe/gokiteconnect/kite"
)

// chargesParam is params as the charges calculator takes it, filled at
// price.
func chargesParam(params kite.OrderParams, price float64) kite.OrderChargesParam {
	return kite.OrderChargesParam{
		Exchange:        params.Exchange,
		Tradingsymbol:   params.Tradingsymbol,
		TransactionType: params.TransactionType,
		Variety:         kite.VarietyRegular,
		Product:         params.Product,
		OrderType:       params.OrderType,
		Quantity:        float64(params.Quantity),
		AveragePrice:    price,
	}
}

// orderChargesParams lists the executed part of each order, for the day's
// charges, and the IDs of the orders listed.
func orderChargesParams(orders kite.Orders) ([]kite.OrderChargesParam, []string) {
	var params []kite.OrderChargesParam
	var ids []string
	for _, order := range orders {
		if order.FilledQuantity <= 0 || order.AveragePrice <= 0 {
			continue
		}
		params = append(params, kite.OrderChargesParam{
			OrderID:         order.OrderID,
			Exchange:        order.Exchange,
			Tradingsymbol:   order.TradingSymbol,
			TransactionType: order.TransactionType,
			Variety:         fallback(order.Variety, kite.VarietyRegular),
			Product:         order.Product,
			OrderType:       order.OrderType,
			Quantity:        order.FilledQuantity,
			AveragePrice:    order.AveragePrice,
		})
		ids = append(ids, order.OrderID)
	}
	return params, ids
}

// chargesTotal adds up the charges of each order.
func chargesTotal(charges []kite.OrderCharges) float64 {
	total := 0.0
	for _, item := range charges {
		total += item.Charges.Total
	}
	return total
}

// chargesBreakdown lists what charges are made of, skipping what is zero.
func chargesBreakdown(charges kite.Charges) string {
	tax := fallback(strings.ToUpper(charges.TransactionTaxType), "STT")
	var parts []string
	for _, part := range []struct {
		name  string
		value float64
	}{
		{"brokerage", charges.Brokerage},
		{tax, charges.TransactionTax},
		{"exchange", charges.ExchangeTurnoverCharge},
		{"GST", charges.GST.Total},
		{"stamp", charges.StampDuty},
		{"SEBI", charges.SEBITurnoverCharge},
	} {
		if part.value >= 0.005 {
			parts = append(parts, part.name+" "+formatAmount(part.value))
		}
	}
	return strings.Join(parts, " · ")
}

// sumCharges adds up charges item by item.
func sumCharges(charges []kite.OrderCharges) kite.Charges {
	var sum kite.Charges
	for _, item := range charges {
		c := item.Charges
		sum.TransactionTaxType = fallback(sum.TransactionTaxType, c.TransactionTaxType)
		sum.TransactionTax += c.TransactionTax
		sum.ExchangeTurnoverCharge += c.ExchangeTurnoverCharge
		sum.SEBITurnoverCharge += c.SEBITurnoverCharge
		sum.Brokerage += c.Brokerage
		sum.StampDuty += c.StampDuty
		sum.GST.Total += c.GST.Total
		sum.Total += c.Total
	}
	return sum
}

// orderCharges describes what the form's order costs at its price, or the
// last price for a market order.
func orderCharges(userParams kite.OrderParams, ltp float64) string {
	price := userParams.Price
	if price <= 0 {
		price = ltp
	}
	if price <= 0 {
		return styleFaint.Render("Charges need a price")
	}
	charges, err := kite.EstimateOrderCharges([]kite.OrderChargesParam{chargesParam(userParams, price)})
	if err != nil {
		return styleFaint.Render("Charges unavailable: " + err.Error())
	}
	sum := sumCharges(charges)
	return styleMuted.Render("Charges ") + styleYellow.Render("≈ ₹"+formatAmount(sum.Total)) + "\n" + styleFaint.Render(chargesBreakdown(sum))
}

// readOrderCharges works out what the day's executed orders cost, by order
// ID.
func readOrderCharges(orders kite.Orders) (map[string]kite.Charges, error) {
	params, ids := orderChargesParams(orders)
	if len(params) == 0 {
		return nil, nil
	}
	charges, err := kite.EstimateOrderCharges(params)
	if err != nil {
		return nil, err
	}
	byOrder := make(map[string]kite.Charges, len(ids))
	for index, id := range ids {
		byOrder[id] = charges[index].Charges
	}
	return byOrder, nil
}
