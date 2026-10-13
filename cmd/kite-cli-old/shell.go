package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type screen int

const (
	screenUsers screen = iota
	screenPortfolio
	screenInstruments
)

var screenPages = map[screen]string{
	screenUsers:       "screen-users",
	screenPortfolio:   "screen-portfolio",
	screenInstruments: "screen-instruments",
}

const userPickerPage = "user-picker"

// kiteApp is kite-cli: the users screen, a user's portfolio and the
// instruments explorer in one application. Q goes back a screen, I opens the
// instruments, U picks a user, O opens the order menu, and Ctrl+C quits from
// anywhere.
type kiteApp struct {
	application *tview.Application
	pages       *tview.Pages
	users       *userApp
	portfolio   *portfolioView
	instruments *instrumentApp
	order       *orderModal
	strikes     *strikePicker

	screen   screen
	previous screen
	// defaultUser is the user whose portfolio opens at start, when stored.
	defaultUser string
	// picking marks the user picker as open.
	picking bool
	// menuActions are the open order menu's actions; nil when it is closed.
	menuActions []orderAction
}

// newKiteApp builds the app on the instruments explorer's application. The
// explorer opens its catalog the first time it is shown.
func newKiteApp(ctx context.Context, manager *credentials.UserManager, credentialsDB string, instruments *instrumentApp, load portfolioLoader, place orderPlacer) *kiteApp {
	app := &kiteApp{
		application: instruments.application,
		pages:       tview.NewPages(),
		instruments: instruments,
	}
	app.users = newUserApp(ctx, app.application, manager, credentialsDB)
	app.users.open = app.openPortfolio
	app.users.quit = instruments.stop
	// The portfolio's background work stops with the explorer's context,
	// which Ctrl+C cancels.
	app.portfolio = newPortfolioView(instruments.ctx, app.application, load)
	app.portfolio.back = func() { app.show(screenUsers) }
	app.portfolio.quit = instruments.stop
	app.portfolio.loggedIn = app.reloadUsers
	app.portfolio.orderMenu = app.openOrderMenu
	app.portfolio.pickUser = app.openUserPicker
	instruments.onBack = app.leaveInstruments
	instruments.onOrder = app.instrumentOrder
	app.order = newOrderModal(instruments.ctx, app.application, app.pages, place)
	app.order.accounts = func() []credentials.Credentials { return app.users.users }
	app.order.closed = app.restoreFocus
	app.order.placed = app.orderPlaced
	app.strikes = newStrikePicker(instruments.ctx, app.application, app.pages, instruments.factory)
	app.strikes.closed = app.restoreFocus
	app.strikes.choose = func(option models.Instrument, side string, ltp float64) {
		app.openOrder(newOrderTicket(option, side, ltp))
	}
	instruments.hidden = true
	instruments.updateModeControls()

	app.pages.AddPage(screenPages[screenUsers], app.users.pages, true, true)
	app.pages.AddPage(screenPages[screenPortfolio], app.portfolio.root, true, false)
	app.pages.AddPage(screenPages[screenInstruments], instruments.pages, true, false)
	app.application.SetRoot(app.pages, true).SetInputCapture(app.capture)
	app.application.SetFocus(app.users.table)
	return app
}

func (app *kiteApp) run() error {
	if err := app.users.reload(); err != nil {
		return err
	}
	app.openDefaultUser()
	app.portfolio.startPricing()
	go func() {
		<-app.instruments.ctx.Done()
		app.instruments.stop()
	}()
	return app.instruments.shutdown(app.application.Run())
}

func (app *kiteApp) capture(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyCtrlC {
		app.instruments.stop()
		return nil
	}
	if app.order.visible {
		return app.order.capture(event)
	}
	if app.strikes.visible {
		return app.strikes.capture(event)
	}
	if app.menuActions != nil {
		return app.captureOrderMenu(event)
	}
	if app.picking {
		if event.Key() == tcell.KeyEscape || (event.Key() == tcell.KeyRune && strings.EqualFold(string(event.Rune()), "q")) {
			app.closeUserPicker()
			return nil
		}
		return event
	}
	if app.screen == screenInstruments {
		return app.instruments.capture(event)
	}
	if event.Key() == tcell.KeyRune && strings.EqualFold(string(event.Rune()), "i") && app.canLeave() {
		app.openInstruments()
		return nil
	}
	if app.screen == screenPortfolio {
		return app.portfolio.capture(event)
	}
	return app.users.capture(event)
}

// canLeave reports whether the current screen is idle, with no form, dialog
// or text field taking keys.
func (app *kiteApp) canLeave() bool {
	if _, typing := app.application.GetFocus().(*tview.InputField); typing {
		return false
	}
	if app.screen == screenUsers {
		return app.users.overlay == "" && !app.users.busy
	}
	return true
}

// reloadUsers redraws the users table, keeping the selected user, after a
// portfolio load stored a new session.
func (app *kiteApp) reloadUsers() {
	row, _ := app.users.table.GetSelection()
	if err := app.users.reload(); err != nil {
		app.users.setStatus("  [red::b]Reload failed:[-] " + tview.Escape(err.Error()))
		return
	}
	if row > 0 && row < app.users.table.GetRowCount() {
		app.users.table.Select(row, 0)
	}
}

// openDefaultUser opens the default user's portfolio at start, selecting
// them on the users screen to come back to. Without one stored, the app
// stays on the users screen.
func (app *kiteApp) openDefaultUser() {
	if app.defaultUser == "" {
		return
	}
	for index, user := range app.users.users {
		if strings.EqualFold(user.UserID, app.defaultUser) {
			app.users.table.Select(index+1, 0)
			app.openPortfolio(user)
			return
		}
	}
	app.users.setStatus(fmt.Sprintf("  [yellow]Default user %s is not stored[-] · pick a user", tview.Escape(app.defaultUser)))
}

// activeUser is the account orders default to: the open portfolio's user,
// else the user selected on the users screen.
func (app *kiteApp) activeUser() string {
	if app.portfolio.user.UserID != "" {
		return app.portfolio.user.UserID
	}
	if user, ok := app.users.selectedUser(); ok {
		return user.UserID
	}
	return ""
}

// openOrder opens the order modal over the current screen.
func (app *kiteApp) openOrder(ticket orderTicket) {
	if err := app.order.show(ticket, app.activeUser()); err != nil {
		app.notify("  [red::b]Cannot place an order:[-] " + tview.Escape(err.Error()))
		app.restoreFocus()
	}
}

// orderPlaced reports an accepted order and reloads the portfolio it
// changes.
func (app *kiteApp) orderPlaced(userID, orderID string, params kite.OrderParams) {
	message := fmt.Sprintf("  [green::b]✔ Order %s placed[-] · %s %d %s for [yellow]%s[-] · check its status in Kite",
		tview.Escape(orderID), params.TransactionType, params.Quantity, tview.Escape(params.Tradingsymbol), tview.Escape(userID))
	app.notify(message)
	if app.portfolio.user.UserID == userID {
		app.portfolio.notice = message
		app.portfolio.reload()
	}
}

// notify shows message in the current screen's status bar.
func (app *kiteApp) notify(message string) {
	switch app.screen {
	case screenPortfolio:
		app.portfolio.status.SetText(message)
	case screenInstruments:
		app.instruments.setStatus(message)
	default:
		app.users.setStatus(message)
	}
}

// restoreFocus gives the current screen back its focus after an overlay.
func (app *kiteApp) restoreFocus() {
	switch app.screen {
	case screenUsers:
		app.application.SetFocus(app.users.table)
	case screenPortfolio:
		app.application.SetFocus(app.portfolio.focused())
	case screenInstruments:
		app.application.SetFocus(app.instruments.activeTable())
	}
}

// openUserPicker lists the stored users to switch to; Enter opens the
// chosen user's portfolio.
func (app *kiteApp) openUserPicker() {
	users := app.users.users
	if len(users) == 0 {
		app.notify("  [yellow]No users stored[-] · press Q to add one")
		return
	}
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true).
		SetSelectedBackgroundColor(tcell.ColorBlue)
	current := 0
	for index, user := range users {
		mode := "WEB"
		if user.IsAPIUser {
			mode = "API"
		}
		label := fmt.Sprintf("[aqua]%-10s[-] %-20s [darkgray]%s[-]", tview.Escape(user.UserID), tview.Escape(truncate(fallback(user.UserName, "—"), 20)), mode)
		chosen := user
		list.AddItem(label, "", 0, func() {
			app.closeUserPicker()
			app.users.table.Select(indexOfUser(app.users.users, chosen.UserID)+1, 0)
			app.openPortfolio(chosen)
		})
		if user.UserID == app.activeUser() {
			current = index
		}
	}
	list.SetCurrentItem(current)
	list.SetBorder(true).SetTitle(" Switch user · Enter opens · Esc cancels ").SetTitleColor(tcell.ColorLightYellow)
	app.picking = true
	app.pages.AddPage(userPickerPage, centered(52, min(len(users), 12)+2, list), true, true)
	app.application.SetFocus(list)
}

func (app *kiteApp) closeUserPicker() {
	app.picking = false
	app.pages.RemovePage(userPickerPage)
	app.restoreFocus()
}

func indexOfUser(users []credentials.Credentials, userID string) int {
	for index, user := range users {
		if user.UserID == userID {
			return index
		}
	}
	return 0
}

// instrumentOrder opens the order menu for the explorer's selected
// instrument, offering to close it when the open portfolio holds it.
func (app *kiteApp) instrumentOrder(instrument models.Instrument, ltp float64) {
	app.openOrderMenu(tradeTarget{instrument: instrument, ltp: ltp, held: app.portfolio.data.held(instrument.ID)})
}

// openStrikePicker picks a strike of underlying's calls or puts to buy or
// sell, priced with the explorer's data user.
func (app *kiteApp) openStrikePicker(underlying models.InstrumentID, side string, optionType models.OptionType) {
	app.strikes.show(underlying, side, optionType, app.instruments.quoteSource, app.instruments.loadQuotes)
}

func (app *kiteApp) openPortfolio(user credentials.Credentials) {
	app.show(screenPortfolio)
	app.portfolio.open(user)
}

func (app *kiteApp) openInstruments() {
	app.previous = app.screen
	app.show(screenInstruments)
	if !app.instruments.started {
		app.instruments.start()
		return
	}
	app.instruments.refreshQuotes()
}

func (app *kiteApp) leaveInstruments() {
	app.show(app.previous)
}

func (app *kiteApp) show(target screen) {
	app.screen = target
	app.instruments.hidden = target != screenInstruments
	app.portfolio.visible = target == screenPortfolio
	app.pages.SwitchToPage(screenPages[target])
	app.restoreFocus()
}
