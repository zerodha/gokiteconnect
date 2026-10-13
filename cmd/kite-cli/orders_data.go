package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// orderTag marks orders placed from kite-cli in the order book.
const orderTag = "kite-cli"

var (
	orderSides = []string{kite.TransactionTypeBuy, kite.TransactionTypeSell}
	orderTypes = []string{kite.OrderTypeMarket, kite.OrderTypeLimit, kite.OrderTypeSL, kite.OrderTypeSLM}
)

// orderTicket is what the order form opens with: the instrument and the
// defaults for each field.
type orderTicket struct {
	instrument models.Instrument
	side       string
	quantity   int
	product    string
	orderType  string
	// ltp is the last price, shown for reference and used as the default
	// limit price; 0 when unknown.
	ltp float64
	// closing marks an order that exits a position or holding.
	closing bool
	// modify is the open order the form changes, placed by userID; its
	// account, side and product are fixed.
	modify *kite.Order
	userID string
	// price and trigger prefill the price fields.
	price, trigger float64
}

// modifyTicket opens the form on an open order of userID's.
func modifyTicket(instrument models.Instrument, order kite.Order, userID string) orderTicket {
	return orderTicket{
		instrument: instrument,
		side:       order.TransactionType,
		quantity:   int(order.Quantity),
		product:    order.Product,
		orderType:  order.OrderType,
		ltp:        order.AveragePrice,
		modify:     &order,
		userID:     userID,
		price:      order.Price,
		trigger:    order.TriggerPrice,
	}
}

// newOrderTicket opens an order for one lot (or one share) of instrument at
// market, with the derivative or delivery product.
func newOrderTicket(instrument models.Instrument, side string, ltp float64) orderTicket {
	return orderTicket{
		instrument: instrument,
		side:       side,
		quantity:   max(instrument.LotSize, 1),
		product:    orderProducts(instrument)[0],
		orderType:  kite.OrderTypeMarket,
		ltp:        ltp,
	}
}

// orderPlacer places a regular order with userID's session and returns its
// order ID.
type orderPlacer func(ctx context.Context, userID string, params kite.OrderParams) (string, error)

// kiteOrderPlacer places orders with the user's stored session, logging in
// first only when it has expired.
func kiteOrderPlacer(manager *credentials.UserManager) orderPlacer {
	return func(ctx context.Context, userID string, params kite.OrderParams) (string, error) {
		user, _, err := manager.RefreshUserSessionIfRequired(ctx, userID)
		if err != nil {
			return "", err
		}
		client := user.KiteClient()
		client.SetAppName("kite-cli")
		response, err := client.PlaceOrder(kite.VarietyRegular, params)
		return response.OrderID, err
	}
}

// orderModifier changes an open order of userID's to params' quantity,
// type, price and trigger.
type orderModifier func(ctx context.Context, userID string, order kite.Order, params kite.OrderParams) error

// orderCanceller cancels an open order of userID's.
type orderCanceller func(ctx context.Context, userID string, order kite.Order) error

// kiteOrderClient is userID's client, logging in first only when the stored
// session has expired.
func kiteOrderClient(ctx context.Context, manager *credentials.UserManager, userID string) (*kite.Client, error) {
	user, _, err := manager.RefreshUserSessionIfRequired(ctx, userID)
	if err != nil {
		return nil, err
	}
	client := user.KiteClient()
	client.SetAppName("kite-cli")
	return client, nil
}

func kiteOrderModifier(manager *credentials.UserManager) orderModifier {
	return func(ctx context.Context, userID string, order kite.Order, params kite.OrderParams) error {
		client, err := kiteOrderClient(ctx, manager, userID)
		if err != nil {
			return err
		}
		_, err = client.ModifyOrder(orderVariety(order), order.OrderID, params)
		return err
	}
}

func kiteOrderCanceller(manager *credentials.UserManager) orderCanceller {
	return func(ctx context.Context, userID string, order kite.Order) error {
		client, err := kiteOrderClient(ctx, manager, userID)
		if err != nil {
			return err
		}
		var parent *string
		if order.ParentOrderID != "" {
			parent = &order.ParentOrderID
		}
		_, err = client.CancelOrder(orderVariety(order), order.OrderID, parent)
		return err
	}
}

func orderVariety(order kite.Order) string {
	return fallback(order.Variety, kite.VarietyRegular)
}

// orderOpen reports whether an order can still be modified or cancelled:
// anything Kite has not completed, cancelled or rejected.
func orderOpen(order kite.Order) bool {
	switch order.Status {
	case "", "COMPLETE", "CANCELLED", "REJECTED":
		return false
	}
	return true
}

// modifyParams are the fields Kite changes on an open order.
func modifyParams(params kite.OrderParams) kite.OrderParams {
	return kite.OrderParams{
		OrderType:    params.OrderType,
		Quantity:     params.Quantity,
		Price:        params.Price,
		TriggerPrice: params.TriggerPrice,
		Validity:     params.Validity,
	}
}

// stepPrice moves price by steps ticks, snapped to the tick; an unset
// price starts from ltp.
func stepPrice(price, ltp, tick float64, steps int) float64 {
	if tick <= 0 {
		tick = 0.05
	}
	if price <= 0 {
		price = ltp
	}
	ticks := math.Round(price/tick) + float64(steps)
	if price <= 0 {
		ticks = float64(max(steps, 1))
	}
	return math.Max(ticks, 1) * tick
}

// formatTicked formats price to the tick's decimals.
func formatTicked(price, tick float64) string {
	decimals := 0
	for scaled := tick; decimals < 4 && math.Abs(scaled-math.Round(scaled)) > 1e-9; decimals++ {
		scaled *= 10
	}
	return strconv.FormatFloat(price, 'f', max(decimals, 2), 64)
}

// isDerivative reports whether instrument trades on a derivatives segment,
// where positions are NRML or MIS rather than CNC.
func isDerivative(instrument models.Instrument) bool {
	switch instrument.Exchange {
	case "NFO", "BFO", "MCX", "CDS", "BCD":
		return true
	}
	switch instrument.InstrumentType {
	case "FUT", string(models.OptionTypeCall), string(models.OptionTypePut):
		return true
	}
	return false
}

// orderProducts lists the products instrument can be ordered with, the
// overnight one first.
func orderProducts(instrument models.Instrument) []string {
	if isDerivative(instrument) {
		return []string{kite.ProductNRML, kite.ProductMIS}
	}
	return []string{kite.ProductCNC, kite.ProductMIS}
}

// orderInput is the order form's values as typed.
type orderInput struct {
	side, orderType, product string
	quantity, price, trigger string
}

// orderParams validates input into the order to place for instrument.
func orderParams(instrument models.Instrument, input orderInput) (kite.OrderParams, error) {
	params := kite.OrderParams{
		Exchange:        instrument.Exchange,
		Tradingsymbol:   instrument.TradingSymbol,
		TransactionType: input.side,
		OrderType:       input.orderType,
		Product:         input.product,
		Validity:        kite.ValidityDay,
		Tag:             orderTag,
	}
	quantity, err := strconv.Atoi(strings.TrimSpace(input.quantity))
	if err != nil || quantity <= 0 {
		return params, errors.New("quantity must be a whole number above 0")
	}
	if lot := instrument.LotSize; lot > 1 && quantity%lot != 0 {
		return params, fmt.Errorf("quantity must be a multiple of the lot size, %d", lot)
	}
	params.Quantity = quantity
	if usesPrice(params.OrderType) {
		if params.Price, err = positive(input.price, "price"); err != nil {
			return params, err
		}
	}
	if usesTrigger(params.OrderType) {
		if params.TriggerPrice, err = positive(input.trigger, "trigger price"); err != nil {
			return params, err
		}
	}
	switch params.OrderType {
	case kite.OrderTypeMarket, kite.OrderTypeSLM:
		// Kite's automatic market protection caps how far the order can
		// fill from the last price.
		params.MarketProtection = -1
	}
	return params, nil
}

func usesPrice(orderType string) bool {
	return orderType == kite.OrderTypeLimit || orderType == kite.OrderTypeSL
}

func usesTrigger(orderType string) bool {
	return orderType == kite.OrderTypeSL || orderType == kite.OrderTypeSLM
}

func positive(text, name string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("enter a %s above 0", name)
	}
	return value, nil
}

// tradeTarget is what the order menu opens for: an instrument at its last
// price and, when the user holds it, the kite.Position or kite.Holding that
// Close position exits.
type tradeTarget struct {
	instrument models.Instrument
	ltp        float64
	held       any
}

func holdingInstrument(holding kite.Holding) models.Instrument {
	return models.Instrument{ID: holdingID(holding), Exchange: holding.Exchange, TradingSymbol: holding.Tradingsymbol, LotSize: 1, InstrumentType: "EQ"}
}

// closingTicket is a market order that exits target's position, or sells
// its holding, for the whole quantity in its product.
func closingTicket(target tradeTarget) (orderTicket, error) {
	ticket := newOrderTicket(target.instrument, kite.TransactionTypeSell, target.ltp)
	ticket.closing = true
	switch held := target.held.(type) {
	case kite.Position:
		if held.Quantity == 0 {
			return ticket, fmt.Errorf("%s is already closed", held.Tradingsymbol)
		}
		ticket.quantity = held.Quantity
		if held.Quantity < 0 {
			ticket.side, ticket.quantity = kite.TransactionTypeBuy, -held.Quantity
		}
		ticket.product = held.Product
	case kite.Holding:
		quantity := held.Quantity + held.T1Quantity
		if quantity <= 0 {
			return ticket, fmt.Errorf("no %s shares to sell", held.Tradingsymbol)
		}
		ticket.quantity = quantity
		ticket.product = kite.ProductCNC
	default:
		return ticket, errors.New("no open position")
	}
	return ticket, nil
}

// optionUnderlying is the underlying whose options the order menu offers: a
// derivative's underlying, an F&O underlying itself, or a held share, whose
// options the strike picker looks for.
func optionUnderlying(instrument models.Instrument) (models.InstrumentID, bool) {
	if id, ok := drilldownUnderlying(instrument); ok {
		return id, true
	}
	if instrument.InstrumentType == "EQ" && !isDerivative(instrument) {
		return instrument.ID, true
	}
	return "", false
}

func sideWord(side string) string {
	if side == kite.TransactionTypeSell {
		return "Sell"
	}
	return "Buy"
}

func optionWord(optionType models.OptionType) string {
	if optionType == models.OptionTypePut {
		return "put"
	}
	return "call"
}
