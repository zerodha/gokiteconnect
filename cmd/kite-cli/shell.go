package main

import (
	"context"
	"strings"

	"github.com/devshoe/gokiteconnect/credentials"
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

// kiteApp is kite-cli: the users screen, a user's portfolio and the
// instruments explorer in one application. Q goes back a screen, I opens the
// instruments, and Ctrl+C quits from anywhere.
type kiteApp struct {
	application *tview.Application
	pages       *tview.Pages
	users       *userApp
	portfolio   *portfolioView
	instruments *instrumentApp

	screen   screen
	previous screen
}

// newKiteApp builds the app on the instruments explorer's application. The
// explorer opens its catalog the first time it is shown.
func newKiteApp(ctx context.Context, manager *credentials.UserManager, credentialsDB string, instruments *instrumentApp, load portfolioLoader) *kiteApp {
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
	instruments.onBack = app.leaveInstruments
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
	switch target {
	case screenUsers:
		app.application.SetFocus(app.users.table)
	case screenPortfolio:
		app.application.SetFocus(app.portfolio.focused())
	case screenInstruments:
		app.application.SetFocus(app.instruments.activeTable())
	}
}
