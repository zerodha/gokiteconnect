package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const orderPage = "order"

// orderTag marks orders placed from kite-cli in the order book.
const orderTag = "kite-cli"

// Order form labels.
const (
	orderFieldAccount  = "Account"
	orderFieldSide     = "Side"
	orderFieldQuantity = "Quantity"
	orderFieldType     = "Order type"
	orderFieldPrice    = "Price"
	orderFieldTrigger  = "Trigger price"
	orderFieldProduct  = "Product"
)

var (
	orderSides = []string{kite.TransactionTypeBuy, kite.TransactionTypeSell}
	orderTypes = []string{kite.OrderTypeMarket, kite.OrderTypeLimit, kite.OrderTypeSL, kite.OrderTypeSLM}
)

// orderTicket is what the order modal opens with: the instrument and the
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

// orderModal is the buy and sell form, shown over every screen. One is open
// at a time.
type orderModal struct {
	application *tview.Application
	pages       *tview.Pages
	ctx         context.Context
	place       orderPlacer

	// accounts lists the users an order can be placed for.
	accounts func() []credentials.Credentials
	// closed runs when the modal closes, to give the screen back its focus.
	closed func()
	// placed runs after Kite accepts an order.
	placed func(userID, orderID string, params kite.OrderParams)

	visible bool
	placing bool
	ticket  orderTicket
	users   []string
	// disabled tracks which input fields refresh has disabled, so they are
	// only toggled when the order type changes.
	disabled map[string]bool
	form    *tview.Form
	header  *tview.TextView
	summary *tview.TextView
}

func newOrderModal(ctx context.Context, application *tview.Application, pages *tview.Pages, place orderPlacer) *orderModal {
	return &orderModal{application: application, pages: pages, ctx: ctx, place: place}
}

// show opens the modal for ticket, placing for userID by default.
func (modal *orderModal) show(ticket orderTicket, userID string) error {
	if modal.visible {
		return errors.New("an order is already open")
	}
	var accounts []credentials.Credentials
	if modal.accounts != nil {
		accounts = modal.accounts()
	}
	if len(accounts) == 0 {
		return errors.New("add a user before placing orders")
	}
	if ticket.side == "" {
		ticket.side = kite.TransactionTypeBuy
	}
	if ticket.orderType == "" {
		ticket.orderType = kite.OrderTypeMarket
	}
	products := orderProducts(ticket.instrument)
	if ticket.product == "" {
		ticket.product = products[0]
	} else if !slices.Contains(products, ticket.product) {
		// Keep a position's own product, such as MTF, to close it.
		products = append(products, ticket.product)
	}
	modal.ticket = ticket
	modal.disabled = map[string]bool{}
	modal.users = modal.users[:0]
	labels := make([]string, len(accounts))
	account := 0
	for index, user := range accounts {
		modal.users = append(modal.users, user.UserID)
		labels[index] = user.UserID
		if user.UserName != "" {
			labels[index] += " · " + user.UserName
		}
		if user.UserID == userID {
			account = index
		}
	}

	modal.header = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	modal.summary = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter).
		SetFieldBackgroundColor(tcell.ColorDarkSlateGray).
		SetButtonBackgroundColor(tcell.ColorDarkSlateGray)
	modal.form = form
	form.AddDropDown(orderFieldAccount, labels, account, func(string, int) { modal.refresh() })
	form.AddDropDown(orderFieldSide, orderSides, indexOf(orderSides, ticket.side), func(string, int) { modal.refresh() })
	form.AddInputField(orderFieldQuantity, strconv.Itoa(ticket.quantity), 12, tview.InputFieldInteger, func(string) { modal.refresh() })
	form.AddDropDown(orderFieldType, orderTypes, indexOf(orderTypes, ticket.orderType), func(string, int) { modal.refresh() })
	price := ""
	if ticket.ltp > 0 {
		price = strconv.FormatFloat(ticket.ltp, 'f', -1, 64)
	}
	form.AddInputField(orderFieldPrice, price, 12, tview.InputFieldFloat, func(string) { modal.refresh() })
	form.AddInputField(orderFieldTrigger, "", 12, tview.InputFieldFloat, func(string) { modal.refresh() })
	form.AddDropDown(orderFieldProduct, products, indexOf(products, ticket.product), func(string, int) { modal.refresh() })
	form.AddButton("Place", modal.submit)
	form.AddButton("Cancel", modal.close)
	form.SetCancelFunc(modal.close)
	// Start on the quantity, the field most orders change.
	form.SetFocus(form.GetFormItemIndex(orderFieldQuantity))

	title := " Place order "
	if ticket.closing {
		title = " Close position "
	}
	content := modalFrame(tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(modal.header, 2, 0, false).
		AddItem(form, 0, 1, true).
		AddItem(modal.summary, 2, 0, false))
	content.SetTitle(title)

	modal.visible, modal.placing = true, false
	modal.refresh()
	modal.pages.AddPage(orderPage, centered(64, 18, content), true, true)
	modal.application.SetFocus(form)
	return nil
}

// modalFrame borders an overlay's content with a column of padding either
// side. tview does not clear a Flex's own padding, so the padding is drawn
// by boxes, which hide the screen behind.
func modalFrame(content tview.Primitive) *tview.Flex {
	frame := tview.NewFlex().
		AddItem(tview.NewBox(), 1, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(tview.NewBox(), 1, 0, false)
	frame.SetBorder(true).SetTitleColor(tcell.ColorLightYellow)
	return frame
}

func indexOf(options []string, value string) int {
	for index, option := range options {
		if option == value {
			return index
		}
	}
	return 0
}

// capture handles keys while the modal is open: Escape cancels, Up and Down
// move between fields, Left and Right change a drop-down's option in place,
// and every other key goes to the form.
func (modal *orderModal) capture(event *tcell.EventKey) *tcell.EventKey {
	if modal.placing {
		return nil
	}
	focus := modal.application.GetFocus()
	if dropDown, ok := focus.(*tview.DropDown); ok && dropDown.IsOpen() {
		// The open list takes its keys, Escape closing it.
		return event
	}
	switch event.Key() {
	case tcell.KeyEscape:
		modal.close()
		return nil
	case tcell.KeyUp:
		return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
	case tcell.KeyDown:
		return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
	case tcell.KeyLeft, tcell.KeyRight:
		if dropDown, ok := focus.(*tview.DropDown); ok {
			step := 1
			if event.Key() == tcell.KeyLeft {
				step = -1
			}
			count := dropDown.GetOptionCount()
			current, _ := dropDown.GetCurrentOption()
			dropDown.SetCurrentOption((current + step + count) % count)
			return nil
		}
	}
	return event
}

func (modal *orderModal) close() {
	if !modal.visible || modal.placing {
		return
	}
	modal.visible = false
	modal.pages.RemovePage(orderPage)
	if modal.closed != nil {
		modal.closed()
	}
}

func (modal *orderModal) dropDown(label string) string {
	_, option := modal.form.GetFormItemByLabel(label).(*tview.DropDown).GetCurrentOption()
	return option
}

func (modal *orderModal) input(label string) *tview.InputField {
	return modal.form.GetFormItemByLabel(label).(*tview.InputField)
}

// order reads the form into the account and the order to place.
func (modal *orderModal) order() (string, kite.OrderParams, error) {
	index, _ := modal.form.GetFormItemByLabel(orderFieldAccount).(*tview.DropDown).GetCurrentOption()
	if index < 0 || index >= len(modal.users) {
		return "", kite.OrderParams{}, errors.New("choose an account")
	}
	instrument := modal.ticket.instrument
	params := kite.OrderParams{
		Exchange:        instrument.Exchange,
		Tradingsymbol:   instrument.TradingSymbol,
		TransactionType: modal.dropDown(orderFieldSide),
		OrderType:       modal.dropDown(orderFieldType),
		Product:         modal.dropDown(orderFieldProduct),
		Validity:        kite.ValidityDay,
		Tag:             orderTag,
	}
	quantity, err := strconv.Atoi(strings.TrimSpace(modal.input(orderFieldQuantity).GetText()))
	if err != nil || quantity <= 0 {
		return "", params, errors.New("quantity must be a whole number above 0")
	}
	if lot := instrument.LotSize; lot > 1 && quantity%lot != 0 {
		return "", params, fmt.Errorf("quantity must be a multiple of the lot size, %d", lot)
	}
	params.Quantity = quantity
	switch params.OrderType {
	case kite.OrderTypeLimit, kite.OrderTypeSL:
		if params.Price, err = positiveField(modal.input(orderFieldPrice), "price"); err != nil {
			return "", params, err
		}
	}
	switch params.OrderType {
	case kite.OrderTypeSL, kite.OrderTypeSLM:
		if params.TriggerPrice, err = positiveField(modal.input(orderFieldTrigger), "trigger price"); err != nil {
			return "", params, err
		}
	}
	switch params.OrderType {
	case kite.OrderTypeMarket, kite.OrderTypeSLM:
		// Kite's automatic market protection caps how far the order can
		// fill from the last price.
		params.MarketProtection = -1
	}
	return modal.users[index], params, nil
}

func positiveField(field *tview.InputField, name string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(field.GetText()), 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("enter a %s above 0", name)
	}
	return value, nil
}

// refresh redraws the header, enables the price fields the order type uses
// and shows the order as it will be placed, or why it cannot be.
func (modal *orderModal) refresh() {
	if modal.form == nil || modal.form.GetFormItemCount() < 7 || modal.form.GetButtonCount() == 0 {
		return
	}
	instrument := modal.ticket.instrument
	side := modal.dropDown(orderFieldSide)
	color := "green"
	if side == kite.TransactionTypeSell {
		color = "red"
	}
	name := fallback(strings.TrimSpace(instrument.DisplayName), instrument.TradingSymbol)
	ltp := "—"
	if modal.ticket.ltp > 0 {
		ltp = formatAmount(modal.ticket.ltp)
	}
	modal.header.SetText(fmt.Sprintf("[black:%s:b] %s [-:-:-] [white::b]%s[-:-:-]\n[darkgray]%s · lot %d · LTP %s[-]",
		color, side, tview.Escape(name), tview.Escape(string(instrument.ID)), max(instrument.LotSize, 1), ltp))

	orderType := modal.dropDown(orderFieldType)
	modal.setDisabled(orderFieldPrice, orderType != kite.OrderTypeLimit && orderType != kite.OrderTypeSL)
	modal.setDisabled(orderFieldTrigger, orderType != kite.OrderTypeSL && orderType != kite.OrderTypeSLM)
	modal.form.GetButton(0).SetLabel("Place " + side)

	userID, params, err := modal.order()
	if err != nil {
		modal.summary.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
		return
	}
	at := "at market"
	switch params.OrderType {
	case kite.OrderTypeLimit:
		at = "limit " + formatAmount(params.Price)
	case kite.OrderTypeSL:
		at = fmt.Sprintf("limit %s, trigger %s", formatAmount(params.Price), formatAmount(params.TriggerPrice))
	case kite.OrderTypeSLM:
		at = "market, trigger " + formatAmount(params.TriggerPrice)
	}
	lots := ""
	if lot := instrument.LotSize; lot > 1 {
		lots = fmt.Sprintf(" (%d %s)", params.Quantity/lot, pluralize("lot", params.Quantity/lot))
	}
	value := ""
	if price := modal.ticket.ltp; params.Price > 0 {
		value = " · ≈ ₹" + formatAmount(params.Price*float64(params.Quantity))
	} else if price > 0 {
		value = " · ≈ ₹" + formatAmount(price*float64(params.Quantity))
	}
	modal.summary.SetText(fmt.Sprintf("[%s::b]%s %d[-:-:-]%s %s · %s · [yellow]%s[-]%s",
		color, params.TransactionType, params.Quantity, lots, at, params.Product, tview.Escape(userID), value))
}

// setDisabled enables or disables an input field when that changes. tview's
// SetDisabled reports the field as finished, which the form takes as a Tab
// and moves focus on, so the field's finished handler is cleared around it
// and the form refocused to set it again.
func (modal *orderModal) setDisabled(label string, disabled bool) {
	if modal.disabled[label] == disabled {
		return
	}
	modal.disabled[label] = disabled
	field := modal.input(label)
	field.SetFinishedFunc(nil)
	field.SetDisabled(disabled)
	if modal.form.HasFocus() {
		modal.application.SetFocus(modal.form)
	}
}

// submit places the order. The modal stays open with the error if Kite
// rejects it, and closes once Kite accepts it.
func (modal *orderModal) submit() {
	if modal.placing {
		return
	}
	userID, params, err := modal.order()
	if err != nil {
		modal.summary.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
		return
	}
	modal.placing = true
	modal.summary.SetText(fmt.Sprintf("[yellow]Placing %s %d %s for %s…[-]", params.TransactionType, params.Quantity, tview.Escape(params.Tradingsymbol), tview.Escape(userID)))
	go func() {
		orderID, err := modal.place(modal.ctx, userID, params)
		if modal.ctx.Err() != nil {
			return
		}
		modal.application.QueueUpdateDraw(func() {
			modal.placing = false
			if err != nil {
				modal.summary.SetText("[red::b]Order rejected:[-:-:-] [red]" + tview.Escape(quoteErrorText(err)) + "[-]")
				return
			}
			modal.close()
			if modal.placed != nil {
				modal.placed(userID, orderID, params)
			}
		})
	}()
}
