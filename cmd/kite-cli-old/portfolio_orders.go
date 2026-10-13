package main

import (
	"errors"
	"fmt"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// tradeTarget is what the order menu opens for: an instrument at its last
// price and, when the user holds it, the kite.Position or kite.Holding that
// Close position exits.
type tradeTarget struct {
	instrument models.Instrument
	ltp        float64
	held       any
}

// selectedTarget is the focused table's selected position or holding. Group
// and fund rows are not tradable.
func (view *portfolioView) selectedTarget() (tradeTarget, bool) {
	table := view.focused()
	row, _ := table.GetSelection()
	if row <= 0 || row >= table.GetRowCount() {
		return tradeTarget{}, false
	}
	switch reference := table.GetCell(row, 0).GetReference().(type) {
	case kite.Position:
		id := positionID(reference)
		instrument, ok := view.data.instruments[id]
		if !ok {
			instrument = models.Instrument{ID: id, Exchange: reference.Exchange, TradingSymbol: reference.Tradingsymbol, LotSize: 1}
		}
		return tradeTarget{instrument: instrument, ltp: reference.LastPrice, held: reference}, true
	case kite.Holding:
		return tradeTarget{instrument: holdingInstrument(reference), ltp: reference.LastPrice, held: reference}, true
	}
	return tradeTarget{}, false
}

func holdingInstrument(holding kite.Holding) models.Instrument {
	id := models.InstrumentID(holding.Exchange + ":" + holding.Tradingsymbol)
	return models.Instrument{ID: id, Exchange: holding.Exchange, TradingSymbol: holding.Tradingsymbol, LotSize: 1, InstrumentType: "EQ"}
}

// held finds the open position, else the holding, in instrument id.
func (data portfolio) held(id models.InstrumentID) any {
	for _, position := range data.positions {
		if positionID(position) == id && position.Quantity != 0 {
			return position
		}
	}
	for _, holding := range data.holdings {
		if models.InstrumentID(holding.Exchange+":"+holding.Tradingsymbol) == id && holding.Quantity+holding.T1Quantity > 0 {
			return holding
		}
	}
	return nil
}

// openOrderMenu opens the order menu for the selected position or holding.
func (view *portfolioView) openOrderMenu() {
	target, ok := view.selectedTarget()
	if !ok {
		view.status.SetText("  [yellow]Select a position or holding to order[-] · funds cannot be ordered here")
		return
	}
	if view.orderMenu != nil {
		view.orderMenu(target)
	}
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
