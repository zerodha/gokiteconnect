package main

import (
	"fmt"
	"strings"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const orderMenuPage = "order-menu"

// orderAction is one entry of the order menu, run by its key.
type orderAction struct {
	key   rune
	label string
	color string
	// detail describes what the action orders, in gray after the label.
	detail string
	run    func()
}

// orderActions lists what can be ordered for target: a raw buy or sell of
// the instrument itself (not of an index), closing the position or holding
// when one is held, and buying or selling its underlying's options.
func (app *kiteApp) orderActions(target tradeTarget) []orderAction {
	instrument := target.instrument
	var actions []orderAction
	if instrument.Segment != "INDICES" {
		lot := "1 share"
		if instrument.LotSize > 1 {
			lot = fmt.Sprintf("1 lot of %d", instrument.LotSize)
		}
		raw := func(side string) func() {
			return func() {
				ticket := newOrderTicket(instrument, side, target.ltp)
				if position, ok := target.held.(kite.Position); ok {
					ticket.product = position.Product
				}
				app.openOrder(ticket)
			}
		}
		actions = append(actions,
			orderAction{key: 'B', label: "Raw buy", color: "lightgreen", detail: lot, run: raw(kite.TransactionTypeBuy)},
			orderAction{key: 'S', label: "Raw sell", color: "lightcoral", detail: lot, run: raw(kite.TransactionTypeSell)},
		)
	}
	if ticket, err := closingTicket(target); err == nil {
		actions = append(actions, orderAction{key: 'X', label: "Close position", color: "yellow",
			detail: fmt.Sprintf("%s %d at market", ticket.side, ticket.quantity),
			run:    func() { app.openOrder(ticket) }})
	}
	if underlying, ok := optionUnderlying(instrument); ok {
		option := func(side string, optionType models.OptionType) func() {
			return func() { app.openStrikePicker(underlying, side, optionType) }
		}
		name := string(underlying)
		if _, symbol, found := strings.Cut(name, ":"); found {
			name = symbol
		}
		actions = append(actions,
			orderAction{key: 'c', label: "Buy call", color: "lightgreen", detail: name, run: option(kite.TransactionTypeBuy, models.OptionTypeCall)},
			orderAction{key: 'C', label: "Sell call", color: "lightcoral", detail: name, run: option(kite.TransactionTypeSell, models.OptionTypeCall)},
			orderAction{key: 'p', label: "Buy put", color: "lightgreen", detail: name, run: option(kite.TransactionTypeBuy, models.OptionTypePut)},
			orderAction{key: 'P', label: "Sell put", color: "lightcoral", detail: name, run: option(kite.TransactionTypeSell, models.OptionTypePut)},
		)
	}
	return actions
}

// optionUnderlying is the underlying whose options the menu offers: a
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

// openOrderMenu lists the orders for target; each runs by its key or by
// Enter.
func (app *kiteApp) openOrderMenu(target tradeTarget) {
	actions := app.orderActions(target)
	if len(actions) == 0 {
		app.notify("  [yellow]Nothing to order for " + tview.Escape(string(target.instrument.ID)) + "[-]")
		return
	}
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true).
		SetSelectedBackgroundColor(tcell.ColorDarkSlateGray)
	for _, action := range actions {
		label := fmt.Sprintf(" [%s::b]%c[-:-:-]  %-15s [darkgray]%s[-]", action.color, action.key, action.label, tview.Escape(action.detail))
		run := action.run
		list.AddItem(label, "", 0, func() {
			app.closeOrderMenu(false)
			run()
		})
	}
	name := fallback(strings.TrimSpace(target.instrument.DisplayName), target.instrument.TradingSymbol)
	title := " Order · " + tview.Escape(name) + " "
	if target.ltp > 0 {
		title += "· " + formatAmount(target.ltp) + " "
	}
	list.SetBorder(true).SetTitle(title).SetTitleColor(tcell.ColorLightYellow).SetBorderPadding(0, 0, 1, 1)
	app.menuActions = actions
	app.pages.AddPage(orderMenuPage, centered(max(46, len([]rune(title))+4), len(actions)+2, list), true, true)
	app.application.SetFocus(list)
}

// closeOrderMenu removes the menu, giving the screen back its focus unless
// an action is about to open another overlay.
func (app *kiteApp) closeOrderMenu(restore bool) {
	app.menuActions = nil
	app.pages.RemovePage(orderMenuPage)
	if restore {
		app.restoreFocus()
	}
}

// captureOrderMenu runs the action whose key is pressed. B, S and X match
// either case; c, C, p and P pick calls and puts by case.
func (app *kiteApp) captureOrderMenu(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyEscape {
		app.closeOrderMenu(true)
		return nil
	}
	if event.Key() != tcell.KeyRune {
		return event
	}
	pressed := event.Rune()
	for _, action := range app.menuActions {
		matches := pressed == action.key
		if strings.ContainsRune("BSX", action.key) {
			matches = strings.EqualFold(string(pressed), string(action.key))
		}
		if matches {
			app.closeOrderMenu(false)
			action.run()
			return nil
		}
	}
	if pressed == 'q' || pressed == 'Q' {
		app.closeOrderMenu(true)
	}
	return nil
}
