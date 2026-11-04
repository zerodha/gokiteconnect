package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// Two of the portfolio's panels have tabs: the middle one the user's
// holdings and mutual funds, the lowest the day's orders, the user's
// baskets and their price alerts. [ and ] switch the focused panel's tabs,
// or the lowest panel's from the positions.

type orderTab int

const (
	tabOrders orderTab = iota
	tabBaskets
	tabAlerts
)

var orderTabNames = []string{"Orders", "Baskets", "Alerts"}

type holdingsTab int

const (
	tabHoldings holdingsTab = iota
	tabFunds
)

var holdingsTabNames = []string{"Holdings", "Mutual funds"}

// holdingsPanel is the middle panel's index in grids.
const holdingsPanel = 1

type (
	portfolioBasketsMsg struct {
		user    string
		baskets []models.Basket
		err     error
	}
	portfolioAlertsMsg struct {
		user   string
		alerts []models.PriceTrigger
		err    error
	}
	alertDeletedMsg struct {
		user    string
		trigger models.PriceTrigger
		err     error
	}
)

// basketOrderRow is one of a basket's orders, as the Baskets tab lists it
// under the basket's row.
type basketOrderRow struct {
	basket models.Basket
	item   models.BasketItem
}

// alertRow is an alert as the Alerts tab lists it, with the last quote
// alerts were checked against.
type alertRow struct {
	trigger models.PriceTrigger
	tick    models.Tick
}

// now is the alert's field's value in the row's quote, if it has one.
func (r alertRow) now() (float64, bool) {
	return r.tick.Value(r.trigger.Field)
}

// tabGrid is the table the lowest panel shows.
func (p *portfolioScreen) tabGrid() *grid {
	switch p.tab {
	case tabBaskets:
		return &p.basketGrid
	case tabAlerts:
		return &p.alertGrid
	}
	return &p.orders
}

// holdingsGrid is the table the middle panel shows.
func (p *portfolioScreen) holdingsGrid() *grid {
	if p.holdingsTab == tabFunds {
		return &p.funds
	}
	return &p.holdings
}

// switchHoldingsTab moves the middle panel step tabs on, focusing it. Funds
// load with the portfolio.
func (p *portfolioScreen) switchHoldingsTab(step int) {
	p.holdingsTab = holdingsTab((int(p.holdingsTab) + step + len(holdingsTabNames)) % len(holdingsTabNames))
	p.focus = holdingsPanel
	p.raw, p.rawOffset = false, 0
}

// switchOrderTab moves the lowest panel step tabs on, focusing it, and
// loads what the tab lists.
func (a *app) switchOrderTab(step int) tea.Cmd {
	p := a.portfolio
	p.tab = orderTab((int(p.tab) + step + len(orderTabNames)) % len(orderTabNames))
	p.focus = len(p.grids()) - 1
	p.raw, p.rawOffset = false, 0
	return a.loadOrderTab()
}

// loadOrderTab reads the shown tab's baskets or alerts afresh. Orders load
// with the portfolio.
func (a *app) loadOrderTab() tea.Cmd {
	p := a.portfolio
	user := p.user.UserID
	if user == "" {
		return nil
	}
	switch p.tab {
	case tabBaskets:
		store := a.deps.baskets
		if store == nil {
			p.basketsErr = errors.New("baskets are unavailable")
			a.renderPortfolioBaskets()
			return nil
		}
		p.basketsLoading = true
		a.renderPortfolioBaskets()
		return a.task(func(ctx context.Context) tea.Msg {
			baskets, err := store.GetBaskets(ctx, user)
			return portfolioBasketsMsg{user: user, baskets: baskets, err: err}
		})
	case tabAlerts:
		store := a.deps.triggers
		if store == nil {
			p.renderAlerts(a.alerts.ticks, false)
			return nil
		}
		p.alertsLoading = true
		p.renderAlerts(a.alerts.ticks, true)
		return a.task(func(ctx context.Context) tea.Msg {
			alerts, err := store.PriceTriggers(ctx, user, false)
			return portfolioAlertsMsg{user: user, alerts: alerts, err: err}
		})
	}
	return nil
}

// alertsChanged reloads the Alerts tab when it lists user's alerts.
func (a *app) alertsChanged(user string) tea.Cmd {
	if p := a.portfolio; p.tab != tabAlerts || p.user.UserID != user {
		return nil
	}
	return a.loadOrderTab()
}

func (a *app) handlePortfolioTabs(msg tea.Msg) (tea.Cmd, bool) {
	p := a.portfolio
	switch msg := msg.(type) {
	case portfolioBasketsMsg:
		if msg.user != p.user.UserID {
			return nil, true
		}
		p.basketsLoading = false
		p.basketList, p.basketsErr = msg.baskets, msg.err
		a.renderPortfolioBaskets()
		return nil, true
	case portfolioAlertsMsg:
		if msg.user != p.user.UserID {
			return nil, true
		}
		p.alertsLoading = false
		p.alertList, p.alertsErr = msg.alerts, msg.err
		p.renderAlerts(a.alerts.ticks, a.deps.triggers != nil)
		return nil, true
	case alertDeletedMsg:
		if msg.err != nil {
			a.notify(statusErr("Cannot delete the alert:", msg.err.Error()))
			return nil, true
		}
		deleted := userTrigger{user: msg.user, trigger: msg.trigger}
		a.alerts.active = slices.DeleteFunc(a.alerts.active, deleted.same)
		a.notify(statusOK("Deleted alert: " + alertText(msg.trigger)))
		return a.alertsChanged(msg.user), true
	}
	return nil, false
}

// deleteAlert deletes an alert of the portfolio's user, active or fired.
func (a *app) deleteAlert(row alertRow) tea.Cmd {
	store, user := a.deps.triggers, a.portfolio.user.UserID
	if store == nil {
		return nil
	}
	trigger := row.trigger
	return a.task(func(ctx context.Context) tea.Msg {
		return alertDeletedMsg{user: user, trigger: trigger, err: store.DeletePriceTrigger(ctx, user, trigger.ID)}
	})
}

// openPortfolioBasket shows basket on Admin's Baskets tab, with the order
// itemID selected when it is set.
func (a *app) openPortfolioBasket(basket models.Basket, itemID string) tea.Cmd {
	cmd := a.openBaskets()
	s := a.baskets
	// A user's first visit has nothing loaded to select from yet.
	if s.stored == nil {
		s.stored = slices.Clone(a.portfolio.basketList)
	}
	s.render(basket.ID)
	if itemID != "" && s.items.selectWhere(func(r row) bool { return r.ref == itemID }) {
		s.focus = 1
	}
	return cmd
}

// toggleBasket collapses or expands a basket's orders on the Baskets tab,
// keeping its row selected.
func (a *app) toggleBasket(id string) {
	p := a.portfolio
	if p.basketsCollapsed == nil {
		p.basketsCollapsed = make(map[string]bool)
	}
	p.basketsCollapsed[id] = !p.basketsCollapsed[id]
	a.renderPortfolioBaskets()
	p.basketGrid.selectWhere(func(r row) bool {
		basket, ok := r.ref.(models.Basket)
		return ok && basket.ID == id
	})
}

// renderPortfolioBaskets lists the portfolio user's baskets, stored ones
// then unsaved drafts, each a row with its orders under it, as positions
// are under their underlying.
func (a *app) renderPortfolioBaskets() {
	p := a.portfolio
	g := &p.basketGrid
	switch {
	case p.basketsErr != nil:
		g.empty = styleRed.Render(quoteErrorText(p.basketsErr))
	case p.basketsLoading && p.basketList == nil:
		g.empty = styleMuted.Render("Loading baskets…")
	default:
		g.empty = styleMuted.Render("No baskets · B opens Baskets, where N starts one")
	}
	baskets := slices.Clone(p.basketList)
	for _, draft := range a.baskets.drafts {
		if draft.CreatedBy == p.user.UserID {
			baskets = append(baskets, draft)
		}
	}
	var rows []row
	for _, basket := range baskets {
		marker := "▾"
		if p.basketsCollapsed[basket.ID] {
			marker = "▸"
		}
		name := basket.Name
		updated := textCell(basket.UpdatedAt.Local().Format("02 Jan 15:04"), styleMuted)
		if isDraft(basket.ID) {
			name += " *"
			updated = textCell("unsaved", styleYellow)
		}
		rows = append(rows, row{ref: basket, cells: []cell{
			textCell(fmt.Sprintf("%s %s (%d)", marker, name, len(basket.Items)), styleYellow.Bold(true)), {}, {}, {}, {}, {}, {}, updated,
		}})
		if p.basketsCollapsed[basket.ID] {
			continue
		}
		for _, item := range basket.Items {
			rows = append(rows, row{ref: basketOrderRow{basket: basket, item: item}, cells: []cell{
				textCell("  "+item.TradingSymbol, styleCyan),
				textCell(item.Exchange, styleBlue),
				textCell(item.TransactionType, sideStyle(item.TransactionType)),
				textCell(strconv.Itoa(item.Quantity), styleText),
				textCell(orderAt(itemParams(item)), styleText),
				textCell(item.Product, styleMuted),
				textCell(itemParams(item).Validity, styleMuted),
				{},
			}})
		}
	}
	g.setRows(rows)
}

// renderAlerts lists the user's alerts, active ones first, each newest
// first, against ticks. on is whether alerts are on at all.
func (p *portfolioScreen) renderAlerts(ticks map[models.InstrumentID]models.Tick, on bool) {
	g := &p.alertGrid
	switch {
	case !on:
		g.empty = styleMuted.Render("Alerts are off")
	case p.alertsErr != nil:
		g.empty = styleRed.Render(quoteErrorText(p.alertsErr))
	case p.alertsLoading && p.alertList == nil:
		g.empty = styleMuted.Render("Loading alerts…")
	default:
		g.empty = styleMuted.Render("No alerts · T on a position, holding or order sets one")
	}
	alerts := slices.Clone(p.alertList)
	slices.SortStableFunc(alerts, func(x, y models.PriceTrigger) int {
		return cmp.Or(cmp.Compare(boolRank(y.Status == models.TriggerActive), boolRank(x.Status == models.TriggerActive)), cmp.Compare(y.ID, x.ID))
	})
	var rows []row
	for _, trigger := range alerts {
		alert := alertRow{trigger: trigger, tick: ticks[trigger.InstrumentID]}
		active := trigger.Status == models.TriggerActive
		when := textCell(fieldName(trigger.Field)+" "+conditionSymbol(trigger.Condition), styleText)
		switch {
		case upward(trigger.Condition):
			when.style = styleGreen
		case downward(trigger.Condition):
			when.style = styleRed
		}
		now, away := textCell("—", styleFaint), textCell("", styleMuted)
		if value, ok := alert.now(); ok {
			now = textCell(fieldCell(trigger.Field, value), styleText)
			if active {
				away = textCell(awayText(trigger.Field, trigger.Value, value), styleMuted)
			}
		}
		status := textCell("● active", styleGreen)
		switch {
		case !active:
			status = textCell("fired "+trigger.LastFired.Local().Format("02 Jan 15:04"), styleYellow)
		case trigger.Entry != "":
			// A stoploss or target arms once the order it guards fills.
			status = textCell("◌ waits for fill", styleMuted)
		case trigger.Tripped:
			// Fired, and fires again once its condition stops holding.
			status = textCell("◐ fired · re-arms", styleYellow)
		}
		rows = append(rows, row{ref: alert, cells: []cell{
			textCell(trigger.CreatedAt.Local().Format("02 Jan 15:04"), styleMuted),
			textCell(displayName(p.data.instruments, trigger.InstrumentID, symbolOf(trigger.InstrumentID)), styleCyan),
			when,
			textCell(fieldCell(trigger.Field, trigger.Value), styleText),
			now, away, status,
			textCell(firingText(trigger), styleText), textCell(strconv.Itoa(trigger.TimesEvaluated), styleMuted),
			alertThenCell(trigger),
			textCell(trigger.Note, styleMuted),
		}})
	}
	g.setRows(rows)
}

// fieldCell is a value of field as a table shows it: a price or a change
// to two places, a percentage with its sign, a count shortened.
func fieldCell(field models.TickField, value float64) string {
	switch fieldInfo(field).Kind {
	case models.KindChange:
		return formatSigned(value)
	case models.KindPercent:
		return fmt.Sprintf("%+.2f%%", value)
	case models.KindQuantity:
		return formatCount(value)
	}
	return formatPrice(value)
}

// alertsChecked puts the alerts a check counted, as they now are, in the
// Alerts tab's list when they are its user's.
func (p *portfolioScreen) alertsChecked(checked []userTrigger, ticks map[models.InstrumentID]models.Tick) {
	changed := false
	for _, alert := range checked {
		if alert.user != p.user.UserID {
			continue
		}
		index := slices.IndexFunc(p.alertList, func(trigger models.PriceTrigger) bool { return trigger.ID == alert.trigger.ID })
		if index >= 0 {
			p.alertList[index], changed = alert.trigger, true
		}
	}
	if changed && p.tab == tabAlerts {
		p.renderAlerts(ticks, true)
	}
}

// tabTitle is a tabbed panel's title: its tabs, the shown one lit.
func tabTitle(names []string, shown int, focused bool) string {
	var parts []string
	for index, name := range names {
		style := styleFaint
		if index == shown {
			style = styleHeader
			if focused {
				style = styleAccent
			}
		}
		parts = append(parts, style.Render(name))
	}
	return strings.Join(parts, styleFaint.Render(" │ "))
}

// tabInfo is the lowest panel's summary, and whether its tab failed.
func (p *portfolioScreen) tabInfo() (string, bool) {
	switch p.tab {
	case tabBaskets:
		baskets, orders := 0, 0
		for _, r := range p.basketGrid.rows {
			if basket, ok := r.ref.(models.Basket); ok {
				baskets++
				orders += len(basket.Items)
			}
		}
		return fmt.Sprintf("%d %s · %d %s", baskets, pluralize("basket", baskets), orders, pluralize("order", orders)), p.basketsErr != nil
	case tabAlerts:
		active := 0
		for _, trigger := range p.alertList {
			if trigger.Status == models.TriggerActive {
				active++
			}
		}
		return fmt.Sprintf("%d active · %d fired", active, len(p.alertList)-active), p.alertsErr != nil
	}
	return ordersInfo(p.data), p.data.ordersErr != nil
}

// instrumentOf is id's instrument, as far as the portfolio knows it.
func (p *portfolioScreen) instrumentOf(id models.InstrumentID) models.Instrument {
	if instrument, found := p.data.instruments[id]; found {
		return instrument
	}
	exchange, _, _ := strings.Cut(string(id), ":")
	return models.Instrument{ID: id, Exchange: exchange, TradingSymbol: symbolOf(id)}
}

// alertText is an alert in a few words: "INFY Last price ≥ ₹1,550.00".
func alertText(trigger models.PriceTrigger) string {
	return symbolOf(trigger.InstrumentID) + " " + conditionText(trigger)
}

// basketDetail lists a basket's orders for the details pane.
func basketDetail(basket models.Basket) string {
	subtitle := fmt.Sprintf("%d %s", len(basket.Items), pluralize("order", len(basket.Items)))
	if isDraft(basket.ID) {
		subtitle += " · unsaved"
	}
	text := newDetailText(basket.Name, subtitle)
	text.section("Orders")
	for _, item := range basket.Items {
		text.line(sideStyle(item.TransactionType).Render(item.TransactionType) + " " + styleText.Render(strconv.Itoa(item.Quantity)) + " " +
			styleBold.Render(item.TradingSymbol) + styleMuted.Render(" · "+orderAt(itemParams(item))+" · "+item.Product))
	}
	if len(basket.Items) == 0 {
		text.line(styleFaint.Render("No orders yet"))
	}
	text.line("\n" + styleFaint.Render("Enter collapses or expands it · B opens it in Admin › Baskets"))
	return text.String()
}

// basketOrderDetail describes one of a basket's orders for the details
// pane, as it would be placed.
func basketOrderDetail(selected basketOrderRow) string {
	item, params := selected.item, itemParams(selected.item)
	text := newDetailText(item.TradingSymbol, "in "+selected.basket.Name)
	text.section("Order")
	text.field("Side", sideStyle(item.TransactionType).Render(item.TransactionType))
	text.plain("Quantity", strconv.Itoa(item.Quantity))
	text.plain("Instrument", string(item.InstrumentID()))
	text.plain("Type", params.OrderType)
	if item.Price > 0 {
		text.plain("Price", formatAmount(item.Price))
	}
	if item.TriggerPrice > 0 {
		text.plain("Trigger", formatAmount(item.TriggerPrice))
	}
	text.plain("Product", params.Product)
	text.plain("Validity", params.Validity)
	text.plain("Tag", params.Tag)
	if !item.UpdatedAt.IsZero() {
		text.plain("Updated", item.UpdatedAt.Local().Format("02 Jan 15:04"))
	}
	text.line("\n" + styleFaint.Render("Enter opens it in Admin › Baskets"))
	return text.String()
}

// alertThenCell sums up what an alert does: sends a message, places
// orders, or both. Of its orders it says how many it places and where, or
// once fired, how many went.
func alertThenCell(trigger models.PriceTrigger) cell {
	orders := alertOrdersCell(trigger)
	switch {
	case !trigger.PlacesOrders():
		return textCell("message", styleMuted)
	case trigger.SendsMessage():
		orders.text = "msg + " + orders.text
	}
	return orders
}

// alertOrdersCell sums up an alert's orders: how many it places and where,
// or once fired, how many went.
func alertOrdersCell(trigger models.PriceTrigger) cell {
	switch {
	case len(trigger.Orders) == 0:
		return textCell("—", styleFaint)
	case len(trigger.OrderResults) == 0:
		style := styleYellow
		if broker.Mode(trigger.OrderMode) == broker.Live {
			style = styleRed
		}
		return textCell(fmt.Sprintf("%d %s", len(trigger.Orders), modeName(broker.Mode(trigger.OrderMode))), style)
	}
	failed := 0
	for _, result := range trigger.OrderResults {
		if result.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		return textCell(fmt.Sprintf("✖ %d/%d", failed, len(trigger.OrderResults)), styleRed)
	}
	return textCell(fmt.Sprintf("✔ %d", len(trigger.OrderResults)), styleGreen)
}

// alertDetail describes an alert for the details pane.
func alertDetail(row alertRow) string {
	trigger := row.trigger
	text := newDetailText(symbolOf(trigger.InstrumentID), string(trigger.InstrumentID))
	text.section("Alert")
	text.field("When", styleText.Render(conditionText(trigger)))
	text.field("Then", styleText.Render(actionText(trigger)))
	if trigger.Exit != "" {
		guards := cmp.Or(trigger.Entry, strings.TrimPrefix(trigger.OCO, "order:"))
		text.field("Exit", styleText.Render(trigger.Exit+" of order "+guards)+styleMuted.Render(" · firing cancels its other exit"))
	}
	switch {
	case trigger.Status != models.TriggerActive:
		text.field("Status", styleYellow.Render("fired")+styleMuted.Render(" · its budget is spent"))
	case trigger.Entry != "":
		text.field("Status", styleMuted.Render("waits for order "+trigger.Entry+" to fill, then orders what filled"))
	case trigger.Tripped && trigger.Crossing():
		text.field("Status", styleYellow.Render("fired")+styleMuted.Render(" · fires again at the next crossing"))
	case trigger.Tripped:
		text.field("Status", styleYellow.Render("fired")+styleMuted.Render(" · fires again once "+fieldName(trigger.Field)+" is no longer "+conditionSymbol(trigger.Condition)+" "+fieldValue(trigger.Field, trigger.Value)))
	default:
		text.field("Status", styleGreen.Render("active"))
	}
	if now, ok := row.now(); ok && trigger.Status == models.TriggerActive {
		text.field("Now", styleText.Render(fieldValue(trigger.Field, now))+styleMuted.Render(" · "+awayText(trigger.Field, trigger.Value, now)+" to go"))
	}
	text.field("Fired", styleText.Render(firingText(trigger))+styleMuted.Render(" · "+budgetText(trigger)))
	if !trigger.LastFired.IsZero() {
		text.field("Last fired", styleText.Render(trigger.LastFired.Local().Format("02 Jan 15:04:05")+" at "+fieldValue(trigger.Field, trigger.LastFiredValue)))
	}
	text.field("Checked", styleText.Render(fmt.Sprintf("%d %s", trigger.TimesEvaluated, pluralize("time", trigger.TimesEvaluated))))
	set := trigger.CreatedAt.Local().Format("02 Jan 15:04:05")
	if trigger.Reference != 0 {
		set += " · at " + fieldValue(trigger.Field, trigger.Reference) + " (" + awayText(trigger.Field, trigger.Value, trigger.Reference) + ")"
	}
	text.field("Set", styleText.Render(set))
	text.plain("User", trigger.UserID)
	if trigger.Note != "" {
		text.plain("Note", trigger.Note)
	}
	if len(trigger.Orders) > 0 {
		text.section("Orders · " + modeName(broker.Mode(trigger.OrderMode)))
		for index, order := range trigger.Orders {
			line := styleText.Render(itemText(order) + " " + order.OrderType)
			if order.OrderType == kite.OrderTypeLimit {
				line += styleMuted.Render(" @ " + formatPrice(order.Price))
			}
			if index < len(trigger.OrderResults) {
				if result := trigger.OrderResults[index]; result.Error != "" {
					line += styleRed.Render(" ✖ " + result.Error)
				} else {
					line += styleGreen.Render(" ✔ " + result.OrderID)
				}
			}
			text.line(line)
		}
	}
	text.line("\n" + styleFaint.Render("Enter or T edits · D deletes"))
	return text.String()
}
