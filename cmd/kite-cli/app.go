package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

type screenID int

const (
	screenUsers screenID = iota
	screenPortfolio
	screenInstruments
	screenHistory
	screenSettings
	screenBaskets
)

// appDeps is everything kite-cli talks to, so tests can replace it.
type appDeps struct {
	manager       *credentials.UserManager
	credentialsDB string
	catalogPath   string
	listsPath     string
	// openCatalog opens the shared instrument catalog on first use.
	openCatalog   catalogFactory
	indices       indexSource
	loadPortfolio portfolioLoader
	// loadQuotes connects the data user; nil turns prices off.
	loadQuotes quoteLoader
	// candles is the local candles database the history screen shows and
	// syncs; nil turns syncing off.
	candles     candleDB
	candlesPath string
	// settingsPath is the settings file S edits.
	settingsPath string
	// mode is the account orders and portfolios start in; empty is paper.
	mode   broker.Mode
	place  orderPlacer
	modify orderModifier
	cancel orderCanceller
	// baskets keeps the order baskets; nil turns them off.
	baskets basketStore
	// resetPaper deletes a user's paper account and opens it again; nil
	// turns resets off.
	resetPaper func(ctx context.Context, userID string) error
	// basketMargins works out a basket's margin; nil leaves it out.
	basketMargins basketMarginer
	// fundamentals keeps the screener's fundamentals; nil turns them off.
	fundamentals fundamentalsStore
	// openWeb opens Kite web signed in as a user; nil turns B off.
	openWeb webOpener
	// defaultUser's portfolio opens at start, when stored.
	defaultUser string
	// every is how often visible prices refresh; 0 refreshes only on R.
	every time.Duration
	// strikeEvery is how often the strike picker reprices.
	strikeEvery time.Duration
}

// overlay is a dialog over the screens: it takes every key while open.
type overlay interface {
	update(a *app, msg tea.KeyPressMsg) tea.Cmd
	view(a *app, w, h int) string
}

// tracker counts running background work so shutdown can wait for it, and
// refuses new work once shutdown starts.
type tracker struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (t *tracker) start() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.wg.Add(1)
	return true
}

func (t *tracker) done() { t.wg.Done() }

func (t *tracker) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.wg.Wait()
}

// app is kite-cli: the users screen, a user's portfolio, the instruments
// explorer and the candle history, with dialogs over them. Q goes back a
// screen, I opens the instruments, H the history, U picks a user, O opens
// the order menu and Ctrl+C quits.
type app struct {
	ctx    context.Context
	cancel context.CancelFunc
	deps   appDeps
	// send delivers progress from background work; the program sets it.
	send  func(tea.Msg)
	tasks tracker

	width, height int
	screen        screenID
	previous      screenID
	users         *usersScreen
	portfolio     *portfolioScreen
	explorer      *explorerScreen
	history       *historyScreen
	admin         *adminScreen
	// historyBack is the screen the Admin screen goes back to.
	historyBack screenID
	// settings is the Settings tab's form, nil until it is opened;
	// settingsBack is the screen it goes back to.
	settings       *settingsForm
	settingsBack   screenID
	settingsStatus string
	overlay        overlay
	spinner        spinner.Model
	// tabs are the tab bar's screen positions, for clicks.
	tabs []tabSpot
	// openedDefault records that the default user was looked for.
	openedDefault bool
	// mode is the account new orders go to and the portfolio shows; P
	// toggles it.
	mode broker.Mode
	// baskets is the Baskets tab, which also keeps the unsaved baskets.
	baskets *basketsScreen
}

type tabSpot struct {
	start, end int
	screen     screenID
}

func newApp(parent context.Context, deps appDeps) *app {
	ctx, cancel := context.WithCancel(parent)
	if deps.strikeEvery == 0 {
		deps.strikeEvery = 5 * time.Second
	}
	a := &app{
		ctx:     ctx,
		cancel:  cancel,
		deps:    deps,
		mode:    cmp.Or(deps.mode, broker.Paper),
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleAccent)),
	}
	a.users = newUsersScreen()
	a.portfolio = newPortfolioScreen()
	a.explorer = newExplorerScreen()
	a.history = newHistoryScreen()
	a.admin = newAdminScreen()
	a.baskets = newBasketsScreen()
	return a
}

// shutdown stops background work and waits for it to finish.
func (a *app) shutdown() error {
	a.cancel()
	a.tasks.close()
	return nil
}

// task runs fn in the background, tracked for shutdown. Its message is
// dropped once the app is stopping.
func (a *app) task(fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if !a.tasks.start() {
			return nil
		}
		defer a.tasks.done()
		if a.ctx.Err() != nil {
			return nil
		}
		msg := fn(a.ctx)
		if a.ctx.Err() != nil {
			return nil
		}
		return msg
	}
}

// progress updates the busy dialog from background work.
func (a *app) progress(text string) {
	if a.send != nil {
		a.send(busyMsg{text: text})
	}
}

type (
	tickMsg struct{}
	busyMsg struct{ text string }
)

func (a *app) Init() tea.Cmd {
	return tea.Batch(a.loadUsers("", ""), a.tick())
}

func (a *app) tick() tea.Cmd {
	if a.deps.every <= 0 {
		return nil
	}
	return tea.Tick(a.deps.every, func(time.Time) tea.Msg { return tickMsg{} })
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return a, a.handle(msg)
}

func (a *app) handle(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return nil
	case tea.KeyPressMsg:
		return a.key(msg)
	case tea.MouseWheelMsg:
		if a.overlay == nil {
			if g := a.focusedGrid(); g != nil {
				g.wheel(msg.Button)
				return a.selectionChanged()
			}
		}
		return nil
	case tea.MouseClickMsg:
		if a.overlay != nil || msg.Button != tea.MouseLeft {
			return nil
		}
		return a.click(msg.X, msg.Y)
	case spinner.TickMsg:
		if _, busy := a.overlay.(*busyOverlay); busy {
			var cmd tea.Cmd
			a.spinner, cmd = a.spinner.Update(msg)
			return cmd
		}
		return nil
	case busyMsg:
		if busy, ok := a.overlay.(*busyOverlay); ok {
			busy.message = msg.text
		}
		return nil
	case tickMsg:
		return tea.Batch(a.tick(), a.periodic())
	}
	if cmd, handled := a.handleUsers(msg); handled {
		return cmd
	}
	if _, ok := msg.(tickerTicksMsg); ok {
		return a.periodic()
	}
	if cmd, handled := a.handlePortfolio(msg); handled {
		return cmd
	}
	if cmd, handled := a.handleExplorer(msg); handled {
		return cmd
	}
	if cmd, handled := a.handleOrders(msg); handled {
		return cmd
	}
	if cmd, handled := a.handleBaskets(msg); handled {
		return cmd
	}
	if cmd, handled := a.handleHistory(msg); handled {
		return cmd
	}
	if cmd, handled := a.handleAdmin(msg); handled {
		return cmd
	}
	return nil
}

// periodic refreshes the visible prices.
func (a *app) periodic() tea.Cmd {
	if a.overlay != nil {
		return nil
	}
	switch a.screen {
	case screenPortfolio:
		return a.refreshPortfolioPrices()
	case screenInstruments:
		return a.refreshQuotes()
	case screenBaskets:
		return a.fetchBasketQuotes()
	}
	return nil
}

func (a *app) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		a.cancel()
		return tea.Quit
	}
	if a.overlay != nil {
		return a.overlay.update(a, msg)
	}
	// Ctrl+P toggles paper and live trading anywhere; P does on the users,
	// portfolio and baskets screens.
	if key == "ctrl+p" || ((key == "p" || key == "P") && (a.screen == screenUsers || a.screen == screenPortfolio || a.screen == screenBaskets)) {
		return a.toggleMode()
	}
	// Shift+←/→ cycle the tabs.
	if key == "shift+left" || key == "shift+right" {
		step := 1
		if key == "shift+left" {
			step = -1
		}
		return a.cycleTab(step)
	}
	// Settings takes every other key: its fields take text.
	if a.screen == screenSettings {
		return a.settingsKey(msg)
	}
	// W opens the Watchlists tab; I, its key when it was Instruments, too.
	if a.screen != screenInstruments && strings.ContainsAny(key, "wWiI") && len(key) == 1 {
		return a.openInstruments()
	}
	// H opens the Admin screen, except from Watchlists.
	if (key == "h" || key == "H") && a.screen != screenHistory && a.screen != screenInstruments {
		return a.openAdmin()
	}
	switch a.screen {
	case screenPortfolio:
		return a.portfolioKey(msg)
	case screenInstruments:
		return a.explorerKey(msg)
	case screenHistory:
		return a.adminKey(msg)
	case screenBaskets:
		return a.basketsKey(msg)
	}
	return a.usersKey(msg)
}

// focusedGrid is the table that keys and the wheel move.
func (a *app) focusedGrid() *grid {
	switch a.screen {
	case screenPortfolio:
		return a.portfolio.focused()
	case screenInstruments:
		return a.explorer.focused()
	case screenHistory:
		return a.adminGrids()[0]
	case screenBaskets:
		return a.baskets.focused()
	case screenSettings:
		return nil
	}
	return &a.users.grid
}

// selectionChanged runs after a table's selection moved.
func (a *app) selectionChanged() tea.Cmd {
	switch a.screen {
	case screenInstruments:
		return a.explorerSelectionChanged()
	case screenHistory:
		return a.adminSelectionChanged()
	case screenBaskets:
		return a.basketsChanged("")
	}
	return nil
}

// click selects the clicked row, activating it when it was already
// selected, or switches screens from the tab bar.
func (a *app) click(x, y int) tea.Cmd {
	if y == 0 {
		for _, tab := range a.tabs {
			if x >= tab.start && x < tab.end {
				return a.switchTo(tab.screen)
			}
		}
		return nil
	}
	if y == 1 && a.screen == screenHistory {
		return a.adminClick(x)
	}
	for index, g := range a.screenGrids() {
		hit, again := g.click(x, y)
		if !hit {
			continue
		}
		a.focusGrid(index)
		cmd := a.selectionChanged()
		if again {
			return tea.Batch(cmd, a.key(tea.KeyPressMsg{Code: tea.KeyEnter}))
		}
		return cmd
	}
	return nil
}

// screenGrids lists the current screen's tables, in focus order.
func (a *app) screenGrids() []*grid {
	switch a.screen {
	case screenPortfolio:
		return a.portfolio.grids()
	case screenInstruments:
		return a.explorer.grids()
	case screenHistory:
		return a.adminGrids()
	case screenBaskets:
		return []*grid{&a.baskets.baskets, &a.baskets.items}
	case screenSettings:
		return nil
	}
	return []*grid{&a.users.grid}
}

func (a *app) focusGrid(index int) {
	switch a.screen {
	case screenPortfolio:
		a.portfolio.focus = index
	case screenInstruments:
		a.explorer.focusGrid(index)
	case screenBaskets:
		a.baskets.focus = index
	}
}

// tabOrder is the tab bar's screens, left to right.
var tabOrder = []screenID{screenUsers, screenPortfolio, screenInstruments, screenBaskets, screenHistory, screenSettings}

// cycleTab switches to the tab step places along, wrapping around and
// skipping Portfolio until a user is picked.
func (a *app) cycleTab(step int) tea.Cmd {
	current := slices.Index(tabOrder, a.screen)
	for range tabOrder {
		current = (current + step + len(tabOrder)) % len(tabOrder)
		if tabOrder[current] != screenPortfolio || a.portfolio.user.UserID != "" {
			break
		}
	}
	return a.switchTo(tabOrder[current])
}

// switchTo shows a screen from the tab bar.
func (a *app) switchTo(target screenID) tea.Cmd {
	switch target {
	case screenInstruments:
		if a.screen != screenInstruments {
			return a.openInstruments()
		}
	case screenBaskets:
		return a.openBaskets()
	case screenHistory:
		return a.openAdmin()
	case screenSettings:
		return a.openSettings()
	case screenPortfolio:
		if a.portfolio.user.UserID == "" {
			a.notify(statusWarn("Pick a user first"))
			return nil
		}
		a.show(screenPortfolio)
	default:
		a.show(screenUsers)
	}
	return nil
}

func (a *app) show(target screenID) {
	a.screen = target
}

func (a *app) closeOverlay() {
	a.overlay = nil
}

// busy shows a spinner over the screen until the work finishes.
func (a *app) busy(message string) tea.Cmd {
	a.overlay = &busyOverlay{message: message}
	return a.spinner.Tick
}

func (a *app) isBusy() bool {
	_, busy := a.overlay.(*busyOverlay)
	return busy
}

// showError reports err in a dialog.
func (a *app) showError(err error) {
	a.overlay = &dialog{
		title:   "Operation failed",
		text:    styleRed.Render(err.Error()),
		buttons: []dialogButton{{label: "OK"}},
		danger:  true,
	}
}

// notify shows message in the current screen's status bar.
func (a *app) notify(message string) {
	switch a.screen {
	case screenPortfolio:
		a.portfolio.status = message
	case screenInstruments:
		a.explorer.status = message
	case screenHistory:
		a.history.status = message
	case screenSettings:
		a.settingsStatus = message
	case screenBaskets:
		a.baskets.status = message
	default:
		a.users.status = message
	}
}

// activeUser is the account orders default to: the open portfolio's user,
// else the user selected on the users screen.
func (a *app) activeUser() string {
	if a.portfolio.user.UserID != "" {
		return a.portfolio.user.UserID
	}
	if user, ok := a.users.selected(); ok {
		return user.UserID
	}
	return ""
}

// busyOverlay blocks input while work runs.
type busyOverlay struct {
	message string
}

func (b *busyOverlay) update(*app, tea.KeyPressMsg) tea.Cmd { return nil }

func (b *busyOverlay) view(a *app, w, _ int) string {
	width := min(max(ansi.StringWidth(b.message)+10, 44), w-2)
	body := "\n" + alignText(a.spinner.View()+"  "+styleText.Render(b.message), width-4, alignCenter) +
		"\n\n" + alignText(styleFaint.Render("Please wait…"), width-4, alignCenter) + "\n"
	return modal("Working", "", body, width)
}

type dialogButton struct {
	label string
	run   func(*app) tea.Cmd
}

// dialog is a message with buttons. Escape and Q choose the cancel button,
// the last one, unless the dialog has no way back.
type dialog struct {
	title    string
	text     string
	buttons  []dialogButton
	focus    int
	danger   bool
	noCancel bool
}

func (d *dialog) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "left", "shift+tab", "h":
		d.focus = max(d.focus-1, 0)
	case "right", "tab", "l":
		d.focus = min(d.focus+1, len(d.buttons)-1)
	case "enter", "space":
		return d.press(a, d.focus)
	case "esc":
		if !d.noCancel {
			return d.press(a, len(d.buttons)-1)
		}
	}
	return nil
}

func (d *dialog) press(a *app, index int) tea.Cmd {
	a.closeOverlay()
	if run := d.buttons[index].run; run != nil {
		return run(a)
	}
	return nil
}

func (d *dialog) view(_ *app, w, _ int) string {
	width := min(64, w-2)
	var buttons []string
	for index, button := range d.buttons {
		style := lipgloss.NewStyle().Padding(0, 2).Background(lipgloss.Color("#2A2D37")).Foreground(colorText)
		if index == d.focus {
			background := colorAccent
			if d.danger && index == 0 && len(d.buttons) > 1 {
				background = colorRed
			}
			style = style.Background(background).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
		}
		buttons = append(buttons, style.Render(button.label))
	}
	body := ansi.Wordwrap(d.text, width-4, "") + "\n\n" + alignText(strings.Join(buttons, "  "), width-4, alignCenter)
	return modal(d.title, "", body, width)
}

// confirm asks before running yes.
func (a *app) confirm(title, text, action string, yes func(*app) tea.Cmd) {
	a.overlay = &dialog{
		title:   title,
		text:    text,
		buttons: []dialogButton{{label: action, run: yes}, {label: "Cancel"}},
		danger:  true,
		focus:   1,
	}
}

// inputOverlay asks for one value, such as a list's name.
type inputOverlay struct {
	title  string
	form   *form
	submit func(*app, string) tea.Cmd
	// back is the dialog Esc and Cancel return to; nil closes the input.
	back overlay
}

func newInputOverlay(title, label, value, button string, submit func(*app, string) tea.Cmd) *inputOverlay {
	f := newForm(14, 30, button, "Cancel")
	f.addInput(label, value, anyText, false)
	f.focusFirst()
	return &inputOverlay{title: title, form: f, submit: submit}
}

func (o *inputOverlay) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		a.overlay = o.back
		return nil
	}
	pressed, _ := o.form.update(msg)
	if msg.String() == "enter" && o.form.focus == len(o.form.fields) {
		// Enter in the field saves.
		pressed = 0
	}
	switch pressed {
	case 0:
		label := o.form.fields[0].label
		text := o.form.fields[0].value()
		if text == "" {
			o.form.message = styleRed.Render(label + " is required")
			o.form.focusFirst()
			return nil
		}
		a.closeOverlay()
		return o.submit(a, text)
	case 1:
		a.overlay = o.back
	}
	return nil
}

func (o *inputOverlay) view(_ *app, w, _ int) string {
	width := min(56, w-2)
	return modal(o.title, "", o.form.view(width-4), width)
}

// View draws the tab bar, the screen, the status and help lines, and any
// dialog over them.
func (a *app) View() tea.View {
	view := tea.NewView(a.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "kite"
	return view
}

func (a *app) render() string {
	w, h := a.width, a.height
	if w <= 0 || h <= 0 {
		w, h = 120, 40
	}
	if w < 40 || h < 12 {
		return "Terminal too small for kite-cli."
	}
	bodyHeight := h - 3
	var body string
	switch a.screen {
	case screenPortfolio:
		body = a.portfolioView(w, bodyHeight)
	case screenInstruments:
		body = a.explorerView(w, bodyHeight)
	case screenHistory:
		body = a.adminView(w, bodyHeight)
	case screenSettings:
		body = a.settingsView(w, bodyHeight)
	case screenBaskets:
		body = a.basketsView(w, bodyHeight)
	default:
		body = a.usersView(w, bodyHeight)
	}
	screen := strings.Join([]string{a.tabBar(w), strings.Join(fitLines(body, w, bodyHeight), "\n"), a.helpBar(w), a.statusBar(w)}, "\n")
	if a.overlay != nil {
		return composite(screen, a.overlay.view(a, w, h), w, h)
	}
	return screen
}

func (a *app) tabBar(w int) string {
	logo := pill("◆ kite", colorAccent, lipgloss.Color("#FFFFFF"))
	line := logo + " "
	a.tabs = a.tabs[:0]
	portfolio := "Portfolio"
	if a.portfolio.user.UserID != "" {
		portfolio += " · " + a.portfolio.user.UserID
	}
	for _, tab := range []struct {
		label  string
		screen screenID
	}{{"Users", screenUsers}, {portfolio, screenPortfolio}, {"Watchlists", screenInstruments}, {"Baskets", screenBaskets}, {"Admin", screenHistory}, {"Settings", screenSettings}} {
		style := lipgloss.NewStyle().Padding(0, 1).Foreground(colorMuted)
		if tab.screen == a.screen {
			style = style.Background(colorSelection).Foreground(colorText).Bold(true)
		}
		start := ansi.StringWidth(line)
		line += style.Render(tab.label) + " "
		a.tabs = append(a.tabs, tabSpot{start: start, end: ansi.StringWidth(line) - 1, screen: tab.screen})
	}
	right := styleFaint.Render(a.databaseLabel())
	gap := w - ansi.StringWidth(line) - ansi.StringWidth(right) - 1
	if gap < 1 {
		return ansi.Truncate(line, w, "")
	}
	return line + strings.Repeat(" ", gap) + right + " "
}

func (a *app) databaseLabel() string {
	switch a.screen {
	case screenInstruments:
		return "DB " + a.deps.catalogPath
	case screenHistory:
		if a.admin.tab != adminHistory {
			return "DB " + a.deps.catalogPath
		}
		return "DB " + a.deps.candlesPath
	case screenSettings:
		return homeRelative(a.deps.settingsPath)
	case screenBaskets:
		return "DB " + a.deps.listsPath
	}
	return "DB " + a.deps.credentialsDB
}

func (a *app) statusBar(w int) string {
	var mode, message string
	var pills []string
	switch a.screen {
	case screenPortfolio:
		mode, message = "PORTFOLIO", a.portfolio.status
		pills = a.portfolioPills()
	case screenInstruments:
		mode, message = a.explorer.modeName(), a.explorer.status
		pills = []string{a.quoteHeader()}
	case screenHistory:
		mode, message = a.adminModeName(), a.adminStatus()
		pills = a.adminPills()
	case screenSettings:
		mode, message = "SETTINGS", a.settingsStatus
	case screenBaskets:
		mode, message = "BASKETS", a.baskets.status
	default:
		mode, message = "USERS", a.users.status
		if a.users.sudo {
			pills = append(pills, pill("SUDO", colorRed, colorInk))
		}
	}
	if n := a.baskets.unsaved(); n > 0 {
		pills = append(pills, pill(fmt.Sprintf("UNSAVED %d", n), colorYellow, colorInk))
	}
	pills = append(pills, modePill(a.mode))
	// A statusline: the mode, the message in its own segment, then the
	// pills on the right, all on one bar.
	left := pill(mode, colorAccent, lipgloss.Color("#FFFFFF"))
	if message != "" {
		left += withBackground(" "+message+" ", colorStatusMsg, 0)
	}
	right := strings.Join(pills, " ")
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return withBackground(ansi.Truncate(left, w, "…"), colorStatusBar, w)
	}
	return withBackground(left+strings.Repeat(" ", gap)+right, colorStatusBar, w)
}

func (a *app) helpBar(w int) string {
	var text string
	switch a.screen {
	case screenPortfolio:
		keys := []string{"+", "order", "B", "baskets", "M/X", "modify / cancel order · X on a group exits it", "Tab", "next table", "Enter", "collapse / raw order", "J/K", "scroll raw", "R", "reload", "U", "switch user", "P", "paper / live"}
		if a.mode == broker.Paper {
			keys = append(keys, "⇧Z", "reset paper")
		}
		text = help(append(keys, "W", "watchlists", "H", "admin", "Esc", "users", "^C", "quit")...)
	case screenInstruments:
		text = a.explorerHelp()
	case screenHistory:
		text = a.adminHelp()
	case screenSettings:
		text = a.settingsHelp()
	case screenBaskets:
		text = a.basketsHelp()
	default:
		text = help("Enter", "portfolio", "W", "watchlists", "H", "admin", "A", "add", "E", "edit", "D", "delete", "F", "refresh session", "B", "open Kite web", "R", "reload", "P", "paper / live", "⇧S", "sudo", "^C", "quit")
	}
	return ansi.Truncate(" "+text, w, "…")
}

// openInstruments shows the explorer, opening the catalog the first time.
func (a *app) openInstruments() tea.Cmd {
	if a.screen != screenInstruments {
		a.previous = a.screen
	}
	a.show(screenInstruments)
	if !a.explorer.started {
		return a.startExplorer()
	}
	// Watchlists are read again: the user may have changed, and smart ones
	// follow lists other tabs refresh. After a price error or with the
	// ticker down, prices reconnect, picking up a refreshed session.
	if e := a.explorer; e.quoteErr != nil || e.tickerDown {
		return tea.Batch(a.reloadQuotes(), a.loadWatchlists(""))
	}
	return tea.Batch(a.refreshQuotes(), a.loadWatchlists(""))
}

// leaveInstruments goes back to the screen the explorer was opened from.
func (a *app) leaveInstruments() {
	a.show(a.previous)
}

// orderPlaced reports an accepted order and reloads the portfolio it
// changes.
func (a *app) orderPlaced(userID string, mode broker.Mode, orderID string, params kite.OrderParams) tea.Cmd {
	message := statusOK(fmt.Sprintf("Order %s placed · %s %d %s for %s · check its status in Kite",
		orderID, params.TransactionType, params.Quantity, params.Tradingsymbol, userID))
	if mode == broker.Paper {
		message = statusOK(fmt.Sprintf("Paper order %s placed · %s %d %s for %s",
			orderID, params.TransactionType, params.Quantity, params.Tradingsymbol, userID))
	}
	a.notify(message)
	if a.portfolio.user.UserID == userID {
		a.portfolio.notice = message
		return a.reloadPortfolio()
	}
	return nil
}

// toggleMode switches new orders and the portfolio between the users' paper
// accounts and Kite, reloading the portfolio from the other account.
func (a *app) toggleMode() tea.Cmd {
	message := statusWarn("LIVE trading · orders go to Kite")
	if a.mode == broker.Live {
		a.mode = broker.Paper
		message = statusInfo("Paper trading · orders fill in each user's paper account at live prices")
	} else {
		a.mode = broker.Live
	}
	a.notify(message)
	p := a.portfolio
	if p.user.UserID == "" {
		return nil
	}
	// The other account's portfolio is not shown under this one's badge.
	p.data = portfolio{}
	p.raw, p.rawOffset = false, 0
	p.render()
	p.notice = message
	return a.reloadPortfolio()
}

// modePill is the account mode's badge: yellow for paper, red for live.
func modePill(mode broker.Mode) string {
	if mode == broker.Live {
		return pill("LIVE", colorRed, colorInk)
	}
	return pill("PAPER", colorYellow, colorInk)
}

// orderChanged reports a modified or cancelled order and reloads the
// portfolio it belongs to.
func (a *app) orderChanged(userID, message string) tea.Cmd {
	a.notify(message)
	if a.portfolio.user.UserID == userID {
		a.portfolio.notice = message
		return a.reloadPortfolio()
	}
	return nil
}

// instrumentOrder opens the order menu for the explorer's selected
// instrument, offering to close it when the open portfolio holds it.
func (a *app) instrumentOrder(instrument models.Instrument, ltp float64) tea.Cmd {
	return a.openOrderMenu(tradeTarget{instrument: instrument, ltp: ltp, held: a.portfolio.data.held(instrument.ID)})
}

var errNoUser = errors.New("select a user first")
