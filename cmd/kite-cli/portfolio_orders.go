package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// detailMinWidth is the narrowest screen that shows the portfolio's details
// pane; narrower, an order's payload opens in a dialog.
const detailMinWidth = 110

// orderID is the instrument an order trades.
func orderID(order kite.Order) models.InstrumentID {
	return models.InstrumentID(order.Exchange + ":" + order.TradingSymbol)
}

// orderStatusStyle colours a status: filled green, rejected red, working
// yellow and cancelled faint.
func orderStatusStyle(status string) lipgloss.Style {
	switch status {
	case "COMPLETE":
		return styleGreen.Bold(true)
	case "REJECTED":
		return styleRed.Bold(true)
	case "CANCELLED":
		return styleFaint
	case "OPEN", "TRIGGER PENDING", "AMO REQ RECEIVED", "MODIFIED":
		return styleYellow
	}
	return styleCyan
}

func sideStyle(side string) lipgloss.Style {
	if side == kite.TransactionTypeSell {
		return styleRed.Bold(true)
	}
	return styleGreen.Bold(true)
}

// ordersInfo counts the day's orders by outcome for the panel title.
func ordersInfo(data portfolio) string {
	orders := data.orders
	var complete, working, failed int
	for _, order := range orders {
		switch order.Status {
		case "COMPLETE":
			complete++
		case "REJECTED", "CANCELLED":
			failed++
		default:
			working++
		}
	}
	info := fmt.Sprintf("%d today · %d complete · %d open · %d closed", len(orders), complete, working, failed)
	switch {
	case data.chargesErr != nil:
		info += " · charges unavailable"
	case len(data.orderCharges) > 0:
		total := 0.0
		for _, charges := range data.orderCharges {
			total += charges.Total
		}
		info += " · charges ₹" + formatAmount(total)
	}
	return info
}

// renderOrders lists the day's orders, newest first.
func (p *portfolioScreen) renderOrders() {
	p.sectionEmpty(&p.orders, p.data.ordersErr, "No orders today")
	var rows []row
	if p.data.ordersErr == nil {
		orders := append(kite.Orders(nil), p.data.orders...)
		sort.SliceStable(orders, func(i, j int) bool { return orders[i].OrderTimestamp.After(orders[j].OrderTimestamp.Time) })
		for _, order := range orders {
			at := ""
			if !order.OrderTimestamp.IsZero() {
				at = order.OrderTimestamp.Format("15:04:05")
			}
			filled := fmt.Sprintf("%g/%g", order.FilledQuantity, order.Quantity)
			filledStyle := styleMuted
			switch {
			case order.Quantity > 0 && order.FilledQuantity >= order.Quantity:
				filledStyle = styleGreen
			case order.FilledQuantity > 0:
				filledStyle = styleYellow
			}
			price := styleFaint.Render("market")
			if order.Price > 0 {
				price = formatPrice(order.Price)
			}
			average := "—"
			if order.AveragePrice > 0 {
				average = formatPrice(order.AveragePrice)
			}
			rows = append(rows, row{ref: order, cells: []cell{
				textCell(at, styleMuted),
				textCell(displayName(p.data.instruments, orderID(order), order.TradingSymbol), styleCyan),
				textCell(order.TransactionType, sideStyle(order.TransactionType)),
				textCell(order.OrderType, styleText),
				textCell(order.Product, styleMuted),
				textCell(filled, filledStyle),
				textCell(price, styleText),
				textCell(average, styleText),
				textCell(order.Status, orderStatusStyle(order.Status)),
			}})
		}
	}
	p.orders.setRows(rows)
}

// orderTarget orders the instrument of a listed order again.
func (p *portfolioScreen) orderTarget(order kite.Order) tradeTarget {
	id := orderID(order)
	instrument, ok := p.data.instruments[id]
	if !ok {
		instrument = models.Instrument{ID: id, Exchange: order.Exchange, TradingSymbol: order.TradingSymbol, LotSize: 1}
	}
	return tradeTarget{instrument: instrument, ltp: order.AveragePrice, held: p.data.held(id)}
}

// orderDetail summarises an order for the details pane.
func orderDetail(order kite.Order, charges map[string]kite.Charges) string {
	text := newDetailText(order.TradingSymbol, "Order "+order.OrderID)
	text.section("Status")
	text.field("Status", orderStatusStyle(order.Status).Render(fallback(order.Status, "—")))
	if order.StatusMessage != "" {
		text.field("Message", styleRed.Render(order.StatusMessage))
	}
	text.field("Side", sideStyle(order.TransactionType).Render(order.TransactionType))
	text.plain("Type", order.OrderType)
	text.plain("Variety", order.Variety)
	text.plain("Product", order.Product)
	text.plain("Validity", order.Validity)
	if order.Tag != "" {
		text.plain("Tag", order.Tag)
	}

	text.section("Quantity")
	text.plain("Ordered", fmt.Sprintf("%g", order.Quantity))
	text.plain("Filled", fmt.Sprintf("%g", order.FilledQuantity))
	text.plain("Pending", fmt.Sprintf("%g", order.PendingQuantity))
	text.plain("Cancelled", fmt.Sprintf("%g", order.CancelledQuantity))
	if order.DisclosedQuantity > 0 {
		text.plain("Disclosed", fmt.Sprintf("%g", order.DisclosedQuantity))
	}

	text.section("Price")
	text.plain("Price", priceOrMarket(order.Price))
	if order.TriggerPrice > 0 {
		text.plain("Trigger", formatPrice(order.TriggerPrice))
	}
	if order.AveragePrice > 0 {
		text.plain("Average", formatPrice(order.AveragePrice))
		text.plain("Value", formatAmount(order.AveragePrice*order.FilledQuantity))
	}
	if charged, ok := charges[order.OrderID]; ok {
		text.section("Charges")
		text.field("Total", styleYellow.Render("₹"+formatAmount(charged.Total)))
		for _, part := range strings.Split(chargesBreakdown(charged), " · ") {
			if name, value, ok := strings.Cut(part, " "); ok {
				text.plain(strings.ToUpper(name[:1])+name[1:], value)
			}
		}
	}

	text.section("Timeline")
	for _, stamp := range []struct {
		label string
		at    models.Time
	}{{"Placed", order.OrderTimestamp}, {"Exchange", order.ExchangeTimestamp}, {"Updated", order.ExchangeUpdateTimestamp}} {
		if !stamp.at.IsZero() {
			text.plain(stamp.label, stamp.at.Format("02 Jan 15:04:05"))
		}
	}
	text.plain("Exchange ID", order.ExchangeOrderID)
	text.plain("Placed by", order.PlacedBy)
	text.WriteString("\n\n" + styleFaint.Render("Enter shows the raw payload."))
	return text.String()
}

func priceOrMarket(price float64) string {
	if price > 0 {
		return formatPrice(price)
	}
	return "market"
}

// rawDetail is the details pane's title, info and text for an order's
// payload, scrolled to fit height lines of width cells.
func (p *portfolioScreen) rawDetail(order kite.Order, width, height int) (string, string, string) {
	lines := payloadLines(order, width)
	p.rawOffset = min(p.rawOffset, max(len(lines)-height, 0))
	end := min(p.rawOffset+height, len(lines))
	info := styleFaint.Render(fmt.Sprintf("%d–%d of %d · J/K", p.rawOffset+1, end, len(lines)))
	return "Raw order", info, strings.Join(lines[p.rawOffset:end], "\n")
}

// jsonField is a key of a JSON object in the order Kite sends it. Objects
// nest as []jsonField and arrays as []any.
type jsonField struct {
	key   string
	value any
}

// orderFields is the order's payload, keys in Kite's order.
func orderFields(order kite.Order) []jsonField {
	data, err := json.Marshal(order)
	if err != nil {
		return []jsonField{{key: "error", value: err.Error()}}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeOrdered(decoder)
	if err != nil {
		return []jsonField{{key: "error", value: err.Error()}}
	}
	fields, _ := value.([]jsonField)
	return fields
}

// decodeOrdered decodes the next JSON value, keeping objects' key order.
func decodeOrdered(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		var fields []jsonField
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeOrdered(decoder)
			if err != nil {
				return nil, err
			}
			fields = append(fields, jsonField{key: fmt.Sprint(key), value: value})
		}
		_, err = decoder.Token()
		return fields, err
	case json.Delim('['):
		values := []any{}
		for decoder.More() {
			value, err := decodeOrdered(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err = decoder.Token()
		return values, err
	}
	return token, nil
}

// payloadLines formats an order's payload in width cells: keys aligned in
// a column, values coloured by type and wrapped beside them.
func payloadLines(order kite.Order, width int) []string {
	return fieldLines(orderFields(order), width, 0)
}

func fieldLines(fields []jsonField, width, indent int) []string {
	keyWidth := 0
	for _, field := range fields {
		keyWidth = max(keyWidth, len(field.key))
	}
	keyWidth = min(keyWidth, max(width/2-indent, 8))
	pad := strings.Repeat(" ", indent)
	var lines []string
	for _, field := range fields {
		if nested, ok := field.value.([]jsonField); ok && len(nested) > 0 {
			lines = append(lines, pad+styleBlue.Bold(true).Render(field.key))
			lines = append(lines, fieldLines(nested, width, indent+2)...)
			continue
		}
		valueWidth := max(width-indent-keyWidth-1, 8)
		value := strings.Split(ansi.Hardwrap(ansi.Wordwrap(jsonText(field.value), valueWidth, ""), valueWidth, true), "\n")
		key := styleBlue.Render(field.key)
		if len(field.key) > keyWidth {
			// A long key takes its own line, its value below.
			lines = append(lines, pad+key)
			key = strings.Repeat(" ", keyWidth)
		} else {
			key = padRight(key, keyWidth)
		}
		for index, part := range value {
			if index > 0 {
				key = strings.Repeat(" ", keyWidth)
			}
			lines = append(lines, pad+key+" "+part)
		}
	}
	return lines
}

// jsonText styles a JSON value: strings plain, numbers yellow, booleans
// cyan and empty values faint. Zero timestamps read as null.
func jsonText(value any) string {
	switch value := value.(type) {
	case nil:
		return styleFaint.Render("null")
	case string:
		if value == "" {
			return styleFaint.Render(`""`)
		}
		if strings.HasPrefix(value, "0001-01-01T") {
			return styleFaint.Render("null")
		}
		if at, err := time.Parse(time.RFC3339, value); err == nil {
			// Spaced, so a narrow pane wraps it between date and time.
			return styleText.Render(at.Format("2006-01-02 15:04:05 -07:00"))
		}
		return styleText.Render(value)
	case json.Number:
		return styleYellow.Render(value.String())
	case bool:
		return styleCyan.Render(fmt.Sprint(value))
	case []jsonField:
		return styleFaint.Render("{}")
	case []any:
		if len(value) == 0 {
			return styleFaint.Render("[]")
		}
		parts := make([]string, len(value))
		for index, item := range value {
			parts[index] = jsonText(item)
		}
		return strings.Join(parts, styleFaint.Render(", "))
	}
	return fmt.Sprint(value)
}

// payloadOverlay shows an order's payload in a scrolling dialog, for
// screens too narrow for the details pane.
type payloadOverlay struct {
	order  kite.Order
	offset int
}

func newPayloadOverlay(order kite.Order) *payloadOverlay {
	return &payloadOverlay{order: order}
}

func (o *payloadOverlay) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter":
		a.closeOverlay()
	case "up", "k", "K":
		o.offset = max(o.offset-1, 0)
	case "down", "j", "J":
		o.offset++
	case "pgup":
		o.offset = max(o.offset-10, 0)
	case "pgdown":
		o.offset += 10
	case "home", "g":
		o.offset = 0
	}
	return nil
}

func (o *payloadOverlay) view(_ *app, w, h int) string {
	width := min(max(w-2, 30), 80)
	height := max(h-8, 3)
	lines := payloadLines(o.order, width-4)
	o.offset = min(o.offset, max(len(lines)-height, 0))
	end := min(o.offset+height, len(lines))
	info := styleFaint.Render(fmt.Sprintf("%d–%d of %d · ↑↓ · Esc", o.offset+1, end, len(lines)))
	return modal("Order "+o.order.OrderID, info, strings.Join(lines[o.offset:end], "\n"), width)
}
