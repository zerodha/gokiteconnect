package main

import (
	"fmt"
	"strings"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/rivo/tview"
)

// updateDetail describes the focused table's selected row in the right-hand
// pane: a position, a group of positions, a holding or a fund.
func (view *portfolioView) updateDetail() {
	table := view.focused()
	row, _ := table.GetSelection()
	if row <= 0 || row >= table.GetRowCount() {
		view.detail.SetText("[darkgray]Select a row to see its details.[-]")
		return
	}
	switch reference := table.GetCell(row, 0).GetReference().(type) {
	case kite.Position:
		view.detail.SetText(view.positionDetail(reference))
	case models.InstrumentID:
		view.detail.SetText(view.groupDetail(reference))
	case kite.Holding:
		view.detail.SetText(holdingDetail(reference))
	case kite.MFHolding:
		view.detail.SetText(fundDetail(reference))
	default:
		view.detail.SetText("[darkgray]Select a row to see its details.[-]")
	}
}

// detailText builds the pane: a title, a subtitle and labelled sections.
type detailText struct {
	strings.Builder
}

func newDetailText(title, subtitle string) *detailText {
	text := &detailText{}
	fmt.Fprintf(text, "[aqua::b]%s[-:-:-]\n[white]%s[-]", tview.Escape(title), tview.Escape(subtitle))
	return text
}

func (text *detailText) section(name string) {
	fmt.Fprintf(text, "\n\n[lightskyblue]%s[-]", name)
}

// field adds a label and an already formatted value, which may carry colour
// tags.
func (text *detailText) field(label, value string) {
	fmt.Fprintf(text, "\n[white]%-14s[-] %s", label, value)
}

func (text *detailText) plain(label, value string) {
	text.field(label, tview.Escape(fallback(value, "—")))
}

func (view *portfolioView) positionDetail(position kite.Position) string {
	id := positionID(position)
	instrument, known := view.data.instruments[id]
	text := newDetailText(displayName(view.data.instruments, id, position.Tradingsymbol), string(id))

	text.section("Instrument")
	text.plain("ID", string(id))
	text.field("Token", fmt.Sprintf("%d", position.InstrumentToken))
	if known {
		text.plain("Type", instrument.InstrumentType)
		text.plain("Segment", instrument.Segment)
		if instrument.Expiry != nil {
			text.plain("Expiry", formatExpiry(instrument.Expiry))
		}
		if instrument.Strike > 0 {
			text.field("Strike", formatStrike(instrument.Strike))
		}
		if instrument.LotSize > 0 {
			text.field("Lot size", fmt.Sprintf("%d", instrument.LotSize))
			if lots := float64(position.Quantity) / float64(instrument.LotSize); lots != 0 {
				text.field("Lots", fmt.Sprintf("%g", lots))
			}
		}
		if instrument.UnderlyingID != nil {
			underlying := *instrument.UnderlyingID
			text.plain("Underlying", displayName(view.data.instruments, underlying, string(underlying)))
		}
	} else {
		text.field("Catalog", "[darkgray]not found[-]")
	}

	text.section("Position")
	text.plain("Product", position.Product)
	text.field("Net qty", fmt.Sprintf("[%s]%d[-]", quantityName(position.Quantity), position.Quantity))
	text.field("Overnight qty", fmt.Sprintf("%d", position.OvernightQuantity))
	if position.Multiplier != 0 && position.Multiplier != 1 {
		text.field("Multiplier", fmt.Sprintf("%g", position.Multiplier))
	}
	text.field("Avg price", formatAmount(position.AveragePrice))
	text.field("LTP", formatAmount(position.LastPrice))
	text.field("Prev close", formatAmount(position.ClosePrice))
	text.field("Value", formatAmount(position.Value))
	text.field("P&L", signedMarkup(position.PnL))
	text.field("M2M", signedMarkup(position.M2M))
	text.field("Realised", signedMarkup(position.Realised))
	text.field("Unrealised", signedMarkup(position.Unrealised))

	text.section("Activity")
	text.field("Bought", trades(position.BuyQuantity, position.BuyPrice, position.BuyValue))
	text.field("Sold", trades(position.SellQuantity, position.SellPrice, position.SellValue))
	text.field("Bought today", trades(position.DayBuyQuantity, position.DayBuyPrice, position.DayBuyValue))
	text.field("Sold today", trades(position.DaySellQuantity, position.DaySellPrice, position.DaySellValue))
	return text.String()
}

func (view *portfolioView) groupDetail(underlying models.InstrumentID) string {
	var group positionGroup
	for _, candidate := range groupPositions(view.data.positions, view.data.instruments) {
		if candidate.underlying == underlying {
			group = candidate
		}
	}
	text := newDetailText(displayName(view.data.instruments, underlying, string(underlying)), string(underlying))
	var m2m, realised, unrealised float64
	for _, position := range group.positions {
		m2m += position.M2M
		realised += position.Realised
		unrealised += position.Unrealised
	}
	text.section("Underlying")
	text.plain("ID", string(underlying))
	text.field("Positions", fmt.Sprintf("%d", len(group.positions)))
	text.field("P&L", signedMarkup(group.pnl))
	text.field("M2M", signedMarkup(m2m))
	text.field("Realised", signedMarkup(realised))
	text.field("Unrealised", signedMarkup(unrealised))

	text.section("Legs")
	for _, position := range group.positions {
		fmt.Fprintf(text, "\n[%s]%+d[-] %s  %s", quantityName(position.Quantity), position.Quantity,
			tview.Escape(displayName(view.data.instruments, positionID(position), position.Tradingsymbol)), signedMarkup(position.PnL))
	}
	text.WriteString("\n\n[darkgray]Enter collapses or expands the group.[-]")
	return text.String()
}

func holdingDetail(holding kite.Holding) string {
	id := holding.Exchange + ":" + holding.Tradingsymbol
	text := newDetailText(holding.Tradingsymbol, id)
	quantity := float64(holding.Quantity + holding.T1Quantity)
	invested, current := holding.AveragePrice*quantity, holding.LastPrice*quantity

	text.section("Instrument")
	text.plain("ID", id)
	text.field("Token", fmt.Sprintf("%d", holding.InstrumentToken))
	text.plain("ISIN", holding.ISIN)
	text.plain("Product", holding.Product)

	text.section("Holding")
	text.field("Quantity", fmt.Sprintf("%d", holding.Quantity))
	text.field("T1 quantity", fmt.Sprintf("%d", holding.T1Quantity))
	if holding.CollateralQuantity > 0 {
		text.field("Collateral", fmt.Sprintf("%d %s", holding.CollateralQuantity, tview.Escape(holding.CollateralType)))
	}
	text.field("Avg price", formatAmount(holding.AveragePrice))
	text.field("LTP", formatAmount(holding.LastPrice))
	text.field("Prev close", formatAmount(holding.ClosePrice))
	text.field("Invested", formatAmount(invested))
	text.field("Current", formatAmount(current))
	text.field("P&L", fmt.Sprintf("%s (%s)", signedMarkup(current-invested), formatPercent(current-invested, invested)))
	text.field("Day change", fmt.Sprintf("%s (%s)", signedMarkup(holding.DayChange*quantity), formatPercent(holding.DayChangePercentage, 100)))
	return text.String()
}

func fundDetail(fund kite.MFHolding) string {
	text := newDetailText(fallback(fund.Fund, fund.Tradingsymbol), fund.Tradingsymbol)
	invested, current := fund.AveragePrice*fund.Quantity, fund.LastPrice*fund.Quantity
	text.section("Fund")
	text.plain("Symbol", fund.Tradingsymbol)
	text.plain("Folio", fund.Folio)
	text.field("Units", fmt.Sprintf("%.3f", fund.Quantity))
	text.field("Avg NAV", formatAmount(fund.AveragePrice))
	text.field("NAV", formatAmount(fund.LastPrice))
	text.plain("NAV date", fund.LastPriceDate)
	text.field("Invested", formatAmount(invested))
	text.field("Current", formatAmount(current))
	text.field("P&L", fmt.Sprintf("%s (%s)", signedMarkup(current-invested), formatPercent(current-invested, invested)))
	return text.String()
}

// trades formats a quantity bought or sold at an average price, with the
// value shortened to fit the pane.
func trades(quantity int, price, value float64) string {
	if quantity == 0 {
		return "[darkgray]—[-]"
	}
	return fmt.Sprintf("%d @ %s (%s)", quantity, formatAmount(price), formatCount(value))
}

func quantityName(quantity int) string {
	switch {
	case quantity > 0:
		return "lightgreen"
	case quantity < 0:
		return "lightcoral"
	}
	return "darkgray"
}
