package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker/consumer"
	"github.com/devshoe/gokiteconnect/cmd/ticker/producer"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	tickerCLIRefreshInterval = time.Second
	defaultTickerLogLimit    = 500
	tickerDashboardPage      = "dashboard"
	tickerLogsPage           = "logs"
)

// tickerLogBuffer is the interactive logger's bounded, race-safe destination.
// Keeping slog away from stdout prevents writes from corrupting tcell's screen.
type tickerLogBuffer struct {
	mu      sync.RWMutex
	limit   int
	lines   []string
	partial string
}

func newTickerLogBuffer(limit int) *tickerLogBuffer {
	if limit <= 0 {
		limit = defaultTickerLogLimit
	}
	return &tickerLogBuffer{limit: limit}
}

func (buffer *tickerLogBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	text := buffer.partial + string(value)
	parts := strings.Split(text, "\n")
	buffer.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			buffer.lines = append(buffer.lines, line)
		}
	}
	if overflow := len(buffer.lines) - buffer.limit; overflow > 0 {
		buffer.lines = append([]string(nil), buffer.lines[overflow:]...)
	}
	return len(value), nil
}

func (buffer *tickerLogBuffer) Text() string {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	lines := append([]string(nil), buffer.lines...)
	if buffer.partial != "" {
		lines = append(lines, buffer.partial)
	}
	return strings.Join(lines, "\n")
}

type tickerCLISnapshot struct {
	producer      producer.Status
	manager       bool
	heartbeat     bool
	subscriptions []consumer.Subscription
	refreshedAt   time.Time
}

// tickerApp is a read-only terminal dashboard over the running ticker
// pipeline. The manager remains the source of truth; the UI only takes
// race-free snapshots from the status-provider interfaces.
type tickerApp struct {
	application *tview.Application
	pages       *tview.Pages
	producer    producerStatusProvider
	manager     subscriptionStatusProvider
	heartbeat   heartbeatStatusProvider
	logs        *tickerLogBuffer

	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time

	userID         string
	address        string
	heartbeatTopic string

	health  *tview.TextView
	table   *tview.Table
	detail  *tview.TextView
	status  *tview.TextView
	logView *tview.TextView

	subscriptions []consumer.Subscription
	logsVisible   bool
	stopOnce      sync.Once
}

func newTickerApp(
	parent context.Context,
	config tickerConfig,
	tickProducer producerStatusProvider,
	manager subscriptionStatusProvider,
	heartbeat heartbeatStatusProvider,
	logs *tickerLogBuffer,
) *tickerApp {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if logs == nil {
		logs = newTickerLogBuffer(defaultTickerLogLimit)
	}
	ui := &tickerApp{
		application:    tview.NewApplication().EnableMouse(true),
		pages:          tview.NewPages(),
		producer:       tickProducer,
		manager:        manager,
		heartbeat:      heartbeat,
		logs:           logs,
		ctx:            ctx,
		cancel:         cancel,
		now:            time.Now,
		userID:         config.UserID,
		address:        ":" + config.Port,
		heartbeatTopic: config.HeartbeatTopic,
	}
	ui.build()
	return ui
}

func (ui *tickerApp) run() error {
	ui.applySnapshot(ui.snapshot())
	go ui.refreshLoop()
	err := ui.application.Run()
	ui.stop()
	return err
}

func (ui *tickerApp) stop() {
	ui.stopOnce.Do(func() {
		ui.cancel()
		ui.application.Stop()
	})
}

func (ui *tickerApp) refreshLoop() {
	ticker := time.NewTicker(tickerCLIRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ui.ctx.Done():
			return
		case <-ticker.C:
			snapshot := ui.snapshot()
			ui.application.QueueUpdateDraw(func() {
				ui.applySnapshot(snapshot)
			})
		}
	}
}

func (ui *tickerApp) snapshot() tickerCLISnapshot {
	return tickerCLISnapshot{
		producer:      ui.producer.Status(),
		manager:       ui.manager.Running(),
		heartbeat:     ui.heartbeat.Running(),
		subscriptions: ui.manager.Subscriptions(),
		refreshedAt:   ui.now().UTC(),
	}
}

func (ui *tickerApp) build() {
	configureTickerTheme()

	header := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[aqua::b]◆[-] [white::b]TICKER[-] [lightblue::b]SUBSCRIPTIONS[-]  [lightcyan]Live Zerodha market-data bridge[-]").
		SetTextAlign(tview.AlignCenter)
	header.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)
	identity := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false).
		SetWordWrap(false).
		SetTextAlign(tview.AlignRight).
		SetText(fmt.Sprintf("[yellow::b]USER[-] %s  [yellow::b]HTTP[-] %s", tview.Escape(ui.userID), tview.Escape(ui.address)))
	identity.SetBorder(true).SetBorderColor(tcell.ColorLightYellow).SetTitle(" Service ").SetTitleColor(tcell.ColorLightYellow)
	headerRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(header, 0, 3, false).
		AddItem(identity, 0, 2, false)

	ui.health = tview.NewTextView().SetDynamicColors(true).SetWrap(false).SetWordWrap(false)
	ui.health.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(" Pipeline ").SetTitleColor(tcell.ColorLightYellow)

	ui.table = tview.NewTable().
		SetBorders(true).
		SetBordersColor(tcell.ColorDarkCyan).
		SetSelectable(true, false).
		SetFixed(1, 0).
		SetEvaluateAllRows(true).
		SetSelectedStyle(tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite).Bold(true))
	ui.table.SetTitle(" Subscriptions ").SetTitleColor(tcell.ColorLightCyan)
	ui.table.SetSelectionChangedFunc(func(row, _ int) {
		ui.updateDetail(row)
	})

	ui.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	ui.detail.SetBorder(true).SetBorderColor(tcell.ColorLightPink).SetTitle(" Selected subscription ").SetTitleColor(tcell.ColorLightPink)
	ui.detail.SetText(ui.emptyDetail())

	body := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(ui.table, 0, 4, true).
		AddItem(ui.detail, 43, 1, false)

	actions := tview.NewFlex().SetDirection(tview.FlexColumn)
	actions.SetBorder(true).SetBorderColor(tcell.ColorDarkCyan).SetTitle(" Actions ").SetTitleColor(tcell.ColorLightYellow)
	actions.AddItem(tickerActionButton("↻ Refresh [R]", tcell.ColorLightSkyBlue, func() {
		ui.applySnapshot(ui.snapshot())
	}), 0, 1, false)
	actions.AddItem(tickerActionButton("☷ Logs [L]", tcell.ColorLightYellow, ui.toggleLogs), 0, 1, false)
	actions.AddItem(tickerActionButton("⏻ Quit [Q]", tcell.ColorLightCoral, ui.stop), 0, 1, false)

	ui.status = tview.NewTextView().SetDynamicColors(true).SetTextColor(tcell.ColorLightCyan)
	ui.status.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(headerRow, 3, 0, false).
		AddItem(ui.health, 3, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(actions, 3, 0, false).
		AddItem(ui.status, 2, 0, false)
	ui.pages.AddPage(tickerDashboardPage, root, true, true)

	ui.logView = tview.NewTextView().SetScrollable(true).SetWrap(false).SetWordWrap(false)
	ui.logView.SetBorder(true).SetBorderColor(tcell.ColorLightYellow).SetTitle(" Ticker logs ").SetTitleColor(tcell.ColorLightYellow)
	logActions := tview.NewFlex().SetDirection(tview.FlexColumn)
	logActions.SetBorder(true).SetBorderColor(tcell.ColorDarkCyan).SetTitle(" Actions ").SetTitleColor(tcell.ColorLightYellow)
	logActions.AddItem(tickerActionButton("← Subscriptions [L / Esc]", tcell.ColorLightCyan, ui.closeLogs), 0, 1, false)
	logActions.AddItem(tickerActionButton("⏻ Quit [Q]", tcell.ColorLightCoral, ui.stop), 0, 1, false)
	logHelp := tview.NewTextView().
		SetDynamicColors(true).
		SetText("  [green]↑↓ PgUp PgDn Home End[-] scroll  [lightcyan]L / Esc[-] subscriptions  [coral]Q[-] quit  ·  newest entries are at the bottom")
	logHelp.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)
	logRoot := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.logView, 0, 1, true).
		AddItem(logActions, 3, 0, false).
		AddItem(logHelp, 2, 0, false)
	ui.pages.AddPage(tickerLogsPage, logRoot, true, false)
	ui.application.SetRoot(ui.pages, true).SetFocus(ui.table).SetInputCapture(ui.capture)
}

func (ui *tickerApp) capture(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyCtrlC {
		ui.stop()
		return nil
	}
	if event.Key() == tcell.KeyEscape && ui.logsVisible {
		ui.closeLogs()
		return nil
	}
	if event.Key() != tcell.KeyRune {
		return event
	}
	switch strings.ToLower(string(event.Rune())) {
	case "q":
		ui.stop()
		return nil
	case "r":
		ui.applySnapshot(ui.snapshot())
		return nil
	case "l":
		ui.toggleLogs()
		return nil
	}
	return event
}

func (ui *tickerApp) toggleLogs() {
	if ui.logsVisible {
		ui.closeLogs()
		return
	}
	ui.logsVisible = true
	ui.updateLogs(true)
	ui.pages.SwitchToPage(tickerLogsPage)
	ui.application.SetFocus(ui.logView)
}

func (ui *tickerApp) closeLogs() {
	ui.logsVisible = false
	ui.pages.SwitchToPage(tickerDashboardPage)
	ui.application.SetFocus(ui.table)
}

func (ui *tickerApp) updateLogs(scrollToEnd bool) {
	text := ui.logs.Text()
	if text == "" {
		text = "No log entries yet."
	}
	ui.logView.SetText(text)
	if scrollToEnd {
		ui.logView.ScrollToEnd()
	}
}

func (ui *tickerApp) applySnapshot(snapshot tickerCLISnapshot) {
	selectedID := ""
	selectedRow, _ := ui.table.GetSelection()
	if selectedRow > 0 && selectedRow <= len(ui.subscriptions) {
		selectedID = ui.subscriptions[selectedRow-1].ID
	}

	ui.subscriptions = append(ui.subscriptions[:0], snapshot.subscriptions...)
	ui.updateHealth(snapshot)
	ui.table.Clear()
	ui.table.SetTitle(fmt.Sprintf(" Subscriptions (%d) ", len(ui.subscriptions)))
	setTickerHeaders(ui.table, []string{"ID", "TOKEN", "MODE", "MAPPING", "HEARTBEAT", "EXPIRES", "LAST TICK", "PUBLISHED"})

	restoreRow := 1
	for row, subscription := range ui.subscriptions {
		mapping, mappingColor := subscriptionMapping(subscription)
		cells := []struct {
			text  string
			color tcell.Color
		}{
			{subscription.ID, tcell.ColorLightCyan},
			{formatTickerToken(subscription.InstrumentToken), tcell.ColorLightPink},
			{strings.ToUpper(subscription.Mode), tcell.ColorLightBlue},
			{mapping, mappingColor},
			{formatTickerAge(subscription.LastHeartbeatAt, snapshot.refreshedAt), tcell.ColorWhite},
			{formatTickerExpiry(subscription.ExpiresAt, snapshot.refreshedAt), expiryColor(subscription.ExpiresAt, snapshot.refreshedAt)},
			{formatTickerAge(subscription.LastRealTickAt, snapshot.refreshedAt), activityColor(subscription.LastRealTickAt)},
			{formatTickerAge(subscription.LastPublishedAt, snapshot.refreshedAt), activityColor(subscription.LastPublishedAt)},
		}
		for column, cell := range cells {
			ui.table.SetCell(row+1, column, tview.NewTableCell(cell.text).
				SetTextColor(cell.color).
				SetExpansion(1))
		}
		if subscription.ID == selectedID {
			restoreRow = row + 1
		}
	}

	if len(ui.subscriptions) == 0 {
		ui.detail.SetText(ui.emptyDetail())
		ui.table.Select(0, 0)
	} else {
		if restoreRow > len(ui.subscriptions) {
			restoreRow = len(ui.subscriptions)
		}
		ui.table.Select(restoreRow, 0)
		ui.updateDetail(restoreRow)
	}
	ui.status.SetText(fmt.Sprintf(
		"  [green]↑↓[-] navigate  [lightskyblue]R[-] refresh  [yellow]L[-] logs  [coral]Q[-] quit  ·  auto-refresh %s  ·  updated %s",
		tickerCLIRefreshInterval,
		snapshot.refreshedAt.Local().Format("15:04:05"),
	))
	if ui.logsVisible {
		ui.updateLogs(false)
	}
}

func (ui *tickerApp) updateHealth(snapshot tickerCLISnapshot) {
	ready := snapshot.producer.Ready() && snapshot.manager && snapshot.heartbeat
	overall := "[yellow::b]STARTING[-]"
	if ready {
		overall = "[green::b]ONLINE[-]"
	}
	ui.health.SetText(fmt.Sprintf(
		" %s  %s  %s  %s  %s  %s  %s  %s",
		overall,
		readinessBadge("Session", snapshot.producer.SessionLoaded),
		readinessBadge("Kite socket", snapshot.producer.SocketConnected),
		readinessBadge("NATS", snapshot.producer.NATSConnected),
		readinessBadge("Streams", snapshot.producer.StreamsReady),
		readinessBadge("Orders", snapshot.producer.OrderDeliveryReady),
		readinessBadge("Manager", snapshot.manager),
		readinessBadge("Heartbeats", snapshot.heartbeat),
	))
}

func (ui *tickerApp) updateDetail(row int) {
	if row <= 0 || row > len(ui.subscriptions) {
		ui.detail.SetText(ui.emptyDetail())
		return
	}
	subscription := ui.subscriptions[row-1]
	mapping, mappingColor := subscriptionMapping(subscription)
	mappingTag := tickerColorTag(mappingColor)
	errorText := subscription.MappingError
	if errorText == "" {
		errorText = "—"
	}
	ui.detail.SetText(fmt.Sprintf(
		"[aqua::b]%s[-]\n\n"+
			"[yellow]Token[-]  %s\n"+
			"[yellow]Mode[-]  %s\n"+
			"[yellow]Mapping[-]  [%s]%s[-]\n\n"+
			"[yellow]Last heartbeat[-]\n%s\n\n"+
			"[yellow]Expires[-]\n%s\n\n"+
			"[yellow]Mapped[-]\n%s\n\n"+
			"[yellow]Last real tick[-]\n%s\n\n"+
			"[yellow]Last published[-]\n%s\n\n"+
			"[yellow]Mapping error[-]\n%s",
		tview.Escape(subscription.ID),
		formatTickerToken(subscription.InstrumentToken),
		tview.Escape(strings.ToUpper(subscription.Mode)),
		mappingTag,
		tview.Escape(mapping),
		formatTickerTime(subscription.LastHeartbeatAt),
		formatTickerTime(subscription.ExpiresAt),
		formatTickerTime(subscription.MappedAt),
		formatTickerTime(subscription.LastRealTickAt),
		formatTickerTime(subscription.LastPublishedAt),
		tview.Escape(errorText),
	))
}

func (ui *tickerApp) emptyDetail() string {
	return fmt.Sprintf(
		"[aqua::b]Waiting for subscriptions[-]\n\nThe ticker adds instruments when heartbeat messages arrive on:\n\n[yellow]%s[-]\n\nExpired subscriptions disappear automatically.",
		tview.Escape(ui.heartbeatTopic),
	)
}

func tickerActionButton(label string, color tcell.Color, selected func()) *tview.Button {
	return tview.NewButton(tview.Escape(label)).
		SetLabelColor(color).
		SetLabelColorActivated(tcell.ColorBlack).
		SetActivatedStyle(tcell.StyleDefault.Background(color).Foreground(tcell.ColorBlack).Bold(true)).
		SetSelectedFunc(selected)
}

func configureTickerTheme() {
	tview.Styles = tview.Theme{
		PrimitiveBackgroundColor:    tcell.ColorBlack,
		ContrastBackgroundColor:     tcell.ColorDarkBlue,
		MoreContrastBackgroundColor: tcell.ColorDarkCyan,
		BorderColor:                 tcell.ColorLightCyan,
		TitleColor:                  tcell.ColorLightYellow,
		GraphicsColor:               tcell.ColorLightCyan,
		PrimaryTextColor:            tcell.ColorWhite,
		SecondaryTextColor:          tcell.ColorLightYellow,
		TertiaryTextColor:           tcell.ColorLightSkyBlue,
		InverseTextColor:            tcell.ColorBlack,
		ContrastSecondaryTextColor:  tcell.ColorWhite,
	}
}

func setTickerHeaders(table *tview.Table, headers []string) {
	for column, header := range headers {
		table.SetCell(0, column, tview.NewTableCell(header).
			SetTextColor(tcell.ColorYellow).
			SetAttributes(tcell.AttrBold).
			SetSelectable(false).
			SetExpansion(1))
	}
}

func readinessBadge(label string, ready bool) string {
	if ready {
		return fmt.Sprintf("[green]●[-] %s", label)
	}
	return fmt.Sprintf("[red]○[-] %s", label)
}

func subscriptionMapping(subscription consumer.Subscription) (string, tcell.Color) {
	if subscription.MappingError != "" {
		return "ERROR", tcell.ColorLightCoral
	}
	if subscription.InstrumentToken == 0 {
		return "PENDING", tcell.ColorYellow
	}
	return "MAPPED", tcell.ColorLightGreen
}

func formatTickerToken(token uint32) string {
	if token == 0 {
		return "—"
	}
	return strconv.FormatUint(uint64(token), 10)
}

func formatTickerTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("02 Jan 2006 15:04:05 MST")
}

func formatTickerAge(value, now time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return humanTickerDuration(now.Sub(value)) + " ago"
}

func formatTickerExpiry(value, now time.Time) string {
	if value.IsZero() {
		return "—"
	}
	remaining := value.Sub(now)
	if remaining <= 0 {
		return "expired"
	}
	return "in " + humanTickerDuration(remaining)
}

func humanTickerDuration(value time.Duration) string {
	if value < 0 {
		value = -value
	}
	switch {
	case value < time.Second:
		return "<1s"
	case value < time.Minute:
		return fmt.Sprintf("%ds", int(value/time.Second))
	case value < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(value/time.Minute), int(value/time.Second)%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(value/time.Hour), int(value/time.Minute)%60)
	}
}

func expiryColor(value, now time.Time) tcell.Color {
	if value.IsZero() || !value.After(now) {
		return tcell.ColorLightCoral
	}
	if value.Sub(now) < 10*time.Second {
		return tcell.ColorYellow
	}
	return tcell.ColorLightGreen
}

func activityColor(value time.Time) tcell.Color {
	if value.IsZero() {
		return tcell.ColorDarkGray
	}
	return tcell.ColorLightGreen
}

func tickerColorTag(color tcell.Color) string {
	switch color {
	case tcell.ColorLightGreen:
		return "lightgreen"
	case tcell.ColorLightCoral:
		return "coral"
	default:
		return "yellow"
	}
}
