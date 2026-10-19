package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	marketdata "github.com/devshoe/gokiteconnect/market-data-service"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/candles"
	"github.com/devshoe/gokiteconnect/repository"
)

// candleDB reads and syncs the local candles database.
type candleDB interface {
	Availability(ctx context.Context, ids []models.InstrumentID) ([]models.CandleAvailability, error)
	Recent(ctx context.Context, id models.InstrumentID, interval models.CandleInterval, limit int) ([]candles.Candle, error)
	// SyncStandard caches an instrument's 1m candles since 2015 and all
	// its 1d candles, calling progress after each stored batch.
	SyncStandard(ctx context.Context, id models.InstrumentID, progress func(marketdata.SyncProgress)) ([]marketdata.SyncResult, error)
}

// localCandles opens the candles database only for each operation, so
// other tools can use it in between. Syncs use the data user's stored
// session and find tokens in the shared catalog.
type localCandles struct {
	path, credentialsPath, dataUserID string
	open                              catalogFactory
}

func (l localCandles) service(ctx context.Context, source marketdata.Source, run func(*marketdata.Service) error) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("create candles directory: %w", err)
	}
	store, err := repository.NewCandlesDuckDBRepository(ctx, l.path, "")
	if err != nil {
		return fmt.Errorf("open candles database: %w", err)
	}
	defer store.Close()
	return run(marketdata.New(store, source, nil, nil))
}

func (l localCandles) Availability(ctx context.Context, ids []models.InstrumentID) (rows []models.CandleAvailability, err error) {
	err = l.service(ctx, nil, func(s *marketdata.Service) error {
		rows, err = s.Availability(ctx, ids)
		return err
	})
	return rows, err
}

func (l localCandles) Recent(ctx context.Context, id models.InstrumentID, interval models.CandleInterval, limit int) (rows []candles.Candle, err error) {
	err = l.service(ctx, nil, func(s *marketdata.Service) error {
		rows, err = s.RecentCandles(ctx, []models.InstrumentID{id}, interval, limit)
		return err
	})
	return rows, err
}

func (l localCandles) SyncStandard(ctx context.Context, id models.InstrumentID, progress func(marketdata.SyncProgress)) (results []marketdata.SyncResult, err error) {
	catalog, _, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	tokens := func(ctx context.Context, id models.InstrumentID) (int64, error) {
		instrument, err := catalog.Get(ctx, id)
		if err != nil {
			return 0, err
		}
		return instrument.InstrumentToken, nil
	}
	err = l.service(ctx, marketdata.NewKiteSource(l.credentialsPath, l.dataUserID, tokens), func(s *marketdata.Service) error {
		results, err = s.SyncStandardProgress(ctx, id, progress)
		return err
	})
	return results, err
}

const (
	historyDetailDelay = 150 * time.Millisecond
	historyDailyRows   = 30
	historyMinuteRows  = 15
)

// historyScreen shows the default sync list with what the candles database
// holds of each instrument, and syncs them one at a time: syncs share the
// data user's rate limit and the candles database.
type historyScreen struct {
	started bool
	status  string
	list    models.List
	// extra are instruments opened from the explorer that aren't in the
	// list.
	extra        []models.InstrumentID
	availability map[models.InstrumentID]map[models.CandleInterval]models.CandleAvailability
	grid         grid

	// syncing is the instrument being synced, progress how far it has got,
	// queue the ones waiting and results the last sync of each.
	syncing    models.InstrumentID
	progress   *marketdata.SyncProgress
	queue      []models.InstrumentID
	results    map[models.InstrumentID]string
	cancelSync context.CancelFunc
	stopping   bool
	// note follows the live progress in the status line, for messages
	// while a sync runs.
	note string
	// A batch counts the syncs started since the queue was last empty.
	batchTotal, batchDone, batchFailed int

	// The detail pane shows the selected instrument's latest candles.
	detailGen int
	detailID  models.InstrumentID
	daily     []candles.Candle
	minute    []candles.Candle
	detailErr error
}

func newHistoryScreen() *historyScreen {
	return &historyScreen{
		status:       statusInfo("Opening the default sync list…"),
		availability: make(map[models.InstrumentID]map[models.CandleInterval]models.CandleAvailability),
		results:      make(map[models.InstrumentID]string),
		grid: grid{columns: []column{
			{title: "INSTRUMENT", width: 24}, {title: "1m FROM", width: 15}, {title: "1m TO", width: 15},
			{title: "1d FROM", width: 9}, {title: "1d TO", width: 9}, {title: "SYNC", width: 12, flex: 1},
		}, empty: styleMuted.Render("Loading the default sync list…")},
	}
}

type (
	historyLoadedMsg struct {
		// focus is the instrument to select once the list loads.
		focus        models.InstrumentID
		list         models.List
		rebuilt      bool
		availability []models.CandleAvailability
		availErr     error
		err          error
	}
	historySyncedMsg struct {
		id           models.InstrumentID
		results      []marketdata.SyncResult
		err          error
		availability []models.CandleAvailability
	}
	historyProgressMsg     struct{ progress marketdata.SyncProgress }
	historyAvailabilityMsg struct {
		availability []models.CandleAvailability
		err          error
	}
	historyDetailFireMsg struct{ generation int }
	historyDetailMsg     struct {
		generation    int
		id            models.InstrumentID
		daily, minute []candles.Candle
		err           error
	}
)

// openHistory shows the history screen, loading the default sync list the
// first time. A non-empty focus selects that instrument, adding it to the
// screen when the list doesn't have it.
func (a *app) openHistory(focus models.InstrumentID) tea.Cmd {
	hs := a.history
	if a.screen != screenHistory {
		a.historyBack = a.screen
	}
	a.show(screenHistory)
	if focus != "" && !slices.Contains(hs.ids(), focus) {
		hs.extra = append([]models.InstrumentID{focus}, hs.extra...)
	}
	if !hs.started {
		hs.started = true
		return a.loadHistory(false, focus)
	}
	if focus == "" {
		return nil
	}
	a.renderHistory(focus)
	return tea.Batch(a.historyDetail(), a.refreshAvailability([]models.InstrumentID{focus}))
}

// ids are the screen's instruments in order: the extra ones, then the
// list's.
func (hs *historyScreen) ids() []models.InstrumentID {
	ids := append([]models.InstrumentID(nil), hs.extra...)
	for _, id := range hs.list.InstrumentIDs() {
		if !slices.Contains(hs.extra, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// loadHistory reads the default sync list, building it when it isn't
// stored or rebuild is set, and what the candles database holds of it.
func (a *app) loadHistory(rebuild bool, focus models.InstrumentID) tea.Cmd {
	open, db := a.deps.openCatalog, a.deps.candles
	extra := append([]models.InstrumentID(nil), a.history.extra...)
	message := "Loading the default sync list…"
	if rebuild {
		message = "Rebuilding the default sync list from the catalog…"
	}
	return tea.Batch(a.busy(message), a.task(func(ctx context.Context) tea.Msg {
		msg := historyLoadedMsg{focus: focus}
		catalog, lists, err := open(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		if lists == nil {
			msg.err = errors.New("lists are unavailable for this catalog")
			return msg
		}
		if !rebuild {
			msg.list, err = storedDefaultSyncList(ctx, lists)
			if errors.Is(err, models.ErrNotFound) {
				rebuild = true
			} else if err != nil {
				msg.err = err
				return msg
			}
		}
		if rebuild {
			msg.rebuilt = true
			if msg.list, err = marketdata.RebuildDefaultSyncList(ctx, catalog, lists); err != nil {
				msg.err = err
				return msg
			}
		}
		if db != nil {
			msg.availability, msg.availErr = db.Availability(ctx, append(extra, msg.list.InstrumentIDs()...))
		}
		return msg
	}))
}

func storedDefaultSyncList(ctx context.Context, lists listStore) (models.List, error) {
	stored, err := lists.GetLists(ctx, models.AdminUser)
	if err != nil {
		return models.List{}, err
	}
	for _, list := range stored {
		if list.CreatedBy == models.AdminUser && list.AutoGenerated && list.Name == marketdata.DefaultSyncListName {
			return list, nil
		}
	}
	return models.List{}, models.ErrNotFound
}

func (a *app) handleHistory(msg tea.Msg) (tea.Cmd, bool) {
	hs := a.history
	switch msg := msg.(type) {
	case historyLoadedMsg:
		a.closeOverlay()
		if msg.err != nil {
			hs.status = statusErr("Could not load the default sync list:", msg.err.Error())
			hs.grid.empty = styleRed.Render("No default sync list · press R to build it")
			return nil, true
		}
		hs.list = msg.list
		hs.setAvailability(msg.availability)
		hs.status = statusInfo(fmt.Sprintf("%s · %d instruments", marketdata.DefaultSyncListName, len(msg.list.Items)))
		if msg.rebuilt {
			hs.status = statusOK(fmt.Sprintf("Built %s · %d instruments", marketdata.DefaultSyncListName, len(msg.list.Items)))
		}
		if msg.availErr != nil {
			hs.status = statusErr("Could not read the candles database:", msg.availErr.Error())
		}
		focus := msg.focus
		if focus == "" {
			focus = hs.detailID
		}
		a.renderHistory(focus)
		return a.historyDetail(), true
	case historyAvailabilityMsg:
		if msg.err != nil {
			hs.status = statusErr("Could not read the candles database:", msg.err.Error())
			return nil, true
		}
		hs.setAvailability(msg.availability)
		a.renderHistory("")
		return nil, true
	case historySyncedMsg:
		return a.historySynced(msg), true
	case historyProgressMsg:
		if msg.progress.InstrumentID == hs.syncing {
			progress := msg.progress
			hs.progress = &progress
			a.renderHistory("")
		}
		return nil, true
	case historyDetailFireMsg:
		if msg.generation != hs.detailGen {
			return nil, true
		}
		return a.loadHistoryDetail(), true
	case historyDetailMsg:
		if msg.generation != hs.detailGen {
			return nil, true
		}
		hs.daily, hs.minute, hs.detailErr = msg.daily, msg.minute, msg.err
		return nil, true
	}
	return nil, false
}

func (hs *historyScreen) setAvailability(rows []models.CandleAvailability) {
	for _, row := range rows {
		byInterval := hs.availability[row.InstrumentID]
		if byInterval == nil {
			byInterval = make(map[models.CandleInterval]models.CandleAvailability)
			hs.availability[row.InstrumentID] = byInterval
		}
		byInterval[row.Interval] = row
	}
}

// renderHistory fills the table, selecting focus, or keeping the current
// selection when it is empty.
func (a *app) renderHistory(focus models.InstrumentID) {
	hs := a.history
	if focus == "" {
		focus, _ = hs.grid.selectedRef().(models.InstrumentID)
	}
	ids := hs.ids()
	rows := make([]row, 0, len(ids))
	for _, id := range ids {
		name := styleCyan.Render(string(id))
		if slices.Contains(hs.extra, id) {
			name += styleFaint.Render(" · not in list")
		}
		minute := hs.availability[id][models.CandleInterval1m]
		daily := hs.availability[id][models.CandleInterval1d]
		rows = append(rows, row{ref: id, cells: []cell{
			{text: name},
			historyDate(minute.HistStart, "02 Jan 06 15:04"), historyDate(minute.HistEnd, "02 Jan 06 15:04"),
			historyDate(daily.HistStart, "02 Jan 06"), historyDate(daily.HistEnd, "02 Jan 06"),
			hs.syncCell(id),
		}})
	}
	hs.grid.empty = styleMuted.Render("The default sync list is empty · press R to rebuild it")
	hs.grid.setRows(rows)
	if focus != "" {
		hs.grid.selectWhere(func(r row) bool { return r.ref == focus })
	}
}

func historyDate(at time.Time, layout string) cell {
	if at.IsZero() {
		return textCell("—", styleFaint)
	}
	return textCell(models.ExchangeClockTime(at).Format(layout), styleText)
}

func (hs *historyScreen) syncCell(id models.InstrumentID) cell {
	switch {
	case id == hs.syncing && hs.progress != nil && hs.progress.InstrumentID == id:
		return textCell(fmt.Sprintf("%s %.0f%%", hs.progress.Interval, hs.progress.Fraction()*100), styleYellow)
	case id == hs.syncing:
		return textCell("syncing…", styleYellow)
	case slices.Contains(hs.queue, id):
		return textCell("queued", styleMuted)
	}
	if result, ok := hs.results[id]; ok {
		return cell{text: result}
	}
	return textCell("", styleFaint)
}

func (a *app) historyKey(msg tea.KeyPressMsg) tea.Cmd {
	hs := a.history
	key := msg.String()
	if key == "esc" || key == "q" || key == "Q" {
		a.show(a.historyBack)
		return nil
	}
	if hs.grid.update(key) {
		return a.historySelectionChanged()
	}
	switch strings.ToLower(key) {
	case "r":
		return a.loadHistory(true, "")
	case "s":
		return a.syncSelectedHistory()
	case "a":
		return a.syncAllHistory()
	case "x":
		return a.stopHistorySync()
	}
	return nil
}

func (a *app) historySelectionChanged() tea.Cmd {
	return a.historyDetail()
}

// historyDetail loads the selected instrument's latest candles once the
// selection rests.
func (a *app) historyDetail() tea.Cmd {
	hs := a.history
	id, ok := hs.grid.selectedRef().(models.InstrumentID)
	if !ok || a.deps.candles == nil {
		return nil
	}
	hs.detailGen++
	if id != hs.detailID {
		hs.detailID, hs.daily, hs.minute, hs.detailErr = id, nil, nil, nil
	}
	generation := hs.detailGen
	return tea.Tick(historyDetailDelay, func(time.Time) tea.Msg { return historyDetailFireMsg{generation: generation} })
}

func (a *app) loadHistoryDetail() tea.Cmd {
	hs := a.history
	id, generation, db := hs.detailID, hs.detailGen, a.deps.candles
	return a.task(func(ctx context.Context) tea.Msg {
		msg := historyDetailMsg{generation: generation, id: id}
		if msg.daily, msg.err = db.Recent(ctx, id, models.CandleInterval1d, historyDailyRows); msg.err != nil {
			return msg
		}
		msg.minute, msg.err = db.Recent(ctx, id, models.CandleInterval1m, historyMinuteRows)
		return msg
	})
}

func (a *app) refreshAvailability(ids []models.InstrumentID) tea.Cmd {
	db := a.deps.candles
	if db == nil {
		return nil
	}
	return a.task(func(ctx context.Context) tea.Msg {
		availability, err := db.Availability(ctx, ids)
		return historyAvailabilityMsg{availability: availability, err: err}
	})
}

// syncSelectedHistory queues the selected instrument's 1m and 1d candles
// for syncing.
func (a *app) syncSelectedHistory() tea.Cmd {
	hs := a.history
	if a.deps.candles == nil {
		hs.status = statusWarn("The candles database is off")
		return nil
	}
	id, ok := hs.grid.selectedRef().(models.InstrumentID)
	if !ok {
		hs.status = statusWarn("Select an instrument to sync")
		return nil
	}
	if id == hs.syncing || slices.Contains(hs.queue, id) {
		hs.note = statusInfo("Already syncing ") + styleYellow.Render(string(id))
		return nil
	}
	if hs.syncing != "" {
		hs.queue = append(hs.queue, id)
		hs.batchTotal++
		hs.note = statusInfo("Queued ") + styleYellow.Render(string(id)) + statusInfo(fmt.Sprintf(" (%d waiting)", len(hs.queue)))
		a.renderHistory("")
		return nil
	}
	hs.batchTotal, hs.batchDone, hs.batchFailed, hs.note = 1, 0, 0, ""
	return a.startHistorySync(id)
}

// syncAllHistory asks, then queues every instrument on the screen that
// isn't syncing or queued already.
func (a *app) syncAllHistory() tea.Cmd {
	hs := a.history
	if a.deps.candles == nil {
		hs.status = statusWarn("The candles database is off")
		return nil
	}
	var pending []models.InstrumentID
	for _, id := range hs.ids() {
		if id != hs.syncing && !slices.Contains(hs.queue, id) {
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 {
		hs.status = statusInfo("Every instrument is syncing or queued already")
		return nil
	}
	a.overlay = &dialog{
		title: "Sync all",
		text: fmt.Sprintf("Sync 1m and 1d candles for %s? Each one resumes after its last stored candle. "+
			"They run one at a time in the background; X stops them.",
			styleYellow.Bold(true).Render(fmt.Sprintf("%d %s", len(pending), pluralize("instrument", len(pending))))),
		buttons: []dialogButton{{label: "Sync all", run: func(a *app) tea.Cmd {
			hs := a.history
			if hs.syncing != "" {
				hs.queue = append(hs.queue, pending...)
				hs.batchTotal += len(pending)
				hs.note = statusInfo(fmt.Sprintf("Queued %d more", len(pending)))
				a.renderHistory("")
				return nil
			}
			hs.queue = append(hs.queue, pending[1:]...)
			hs.batchTotal, hs.batchDone, hs.batchFailed, hs.note = len(pending), 0, 0, ""
			return a.startHistorySync(pending[0])
		}}, {label: "Cancel"}},
	}
	return nil
}

// stopHistorySync drops the queue and stops the running sync. What a sync
// stored before stopping is kept, and the next sync resumes after it.
func (a *app) stopHistorySync() tea.Cmd {
	hs := a.history
	if hs.syncing == "" {
		hs.status = statusInfo("Nothing is syncing")
		return nil
	}
	dropped := len(hs.queue)
	hs.queue = nil
	hs.batchTotal -= dropped
	hs.stopping = true
	hs.cancelSync()
	hs.status = statusWarn(fmt.Sprintf("Stopping %s · dropped %d queued", hs.syncing, dropped))
	a.renderHistory("")
	return nil
}

func (a *app) startHistorySync(id models.InstrumentID) tea.Cmd {
	hs := a.history
	hs.syncing, hs.progress, hs.stopping = id, nil, false
	ctx, cancel := context.WithCancel(a.ctx)
	hs.cancelSync = cancel
	a.renderHistory("")
	db, send := a.deps.candles, a.send
	progress := func(p marketdata.SyncProgress) {
		if send != nil {
			send(historyProgressMsg{progress: p})
		}
	}
	return a.task(func(context.Context) tea.Msg {
		defer cancel()
		msg := historySyncedMsg{id: id}
		msg.results, msg.err = db.SyncStandard(ctx, id, progress)
		// Show what was stored even when the sync failed or stopped
		// part-way.
		msg.availability, _ = db.Availability(a.ctx, []models.InstrumentID{id})
		return msg
	})
}

// historySynced reports a finished sync and starts the next queued one.
func (a *app) historySynced(msg historySyncedMsg) tea.Cmd {
	hs := a.history
	hs.syncing, hs.progress = "", nil
	hs.setAvailability(msg.availability)
	hs.batchDone++
	switch {
	case msg.err != nil && hs.stopping && errors.Is(msg.err, context.Canceled):
		hs.status = statusWarn("Stopped syncing " + string(msg.id) + " · what it stored is kept")
		hs.results[msg.id] = styleYellow.Render("stopped")
	case msg.err != nil:
		hs.batchFailed++
		hs.status = statusErr("Syncing "+string(msg.id)+" failed:", msg.err.Error())
		// Keep the failure on screen while the queue carries on.
		hs.note = hs.status
		hs.results[msg.id] = styleRed.Render("failed")
	default:
		hs.status = statusOK("Synced " + string(msg.id) + " · " + syncResultText(msg.results))
		hs.results[msg.id] = styleGreen.Render(syncedCellText(msg.results))
	}
	var cmds []tea.Cmd
	if msg.id == hs.detailID {
		cmds = append(cmds, a.historyDetail())
	}
	if len(hs.queue) > 0 {
		next := hs.queue[0]
		hs.queue = hs.queue[1:]
		cmds = append(cmds, a.startHistorySync(next))
	} else if hs.batchTotal > 1 && !hs.stopping {
		summary := fmt.Sprintf("Synced %d of %d", hs.batchDone-hs.batchFailed, hs.batchTotal)
		if hs.batchFailed > 0 {
			hs.status = statusWarn(summary + fmt.Sprintf(" · %d failed, marked in the table", hs.batchFailed))
		} else {
			hs.status = statusOK(summary)
		}
	}
	a.renderHistory("")
	return tea.Batch(cmds...)
}

// historyStatus is the status line: the running sync's live progress,
// then any note, or the last message.
func (a *app) historyStatus() string {
	hs := a.history
	if hs.syncing == "" || hs.stopping {
		return hs.status
	}
	text := statusInfo("Syncing ") + styleYellow.Render(string(hs.syncing))
	if p := hs.progress; p == nil || p.InstrumentID != hs.syncing {
		text += statusInfo(" · starting…")
	} else {
		layout := "02 Jan 2006 15:04"
		if !p.Interval.IsIntraday() {
			layout = "02 Jan 2006"
		}
		text += statusInfo(" · ") + styleKey.Render(string(p.Interval)) +
			styleText.Render(fmt.Sprintf(" %.0f%%", p.Fraction()*100)) +
			statusInfo(" · through "+p.Through.Format(layout)+" · +"+formatCount(float64(p.Candles))+" candles")
	}
	if hs.note != "" {
		text += statusInfo(" │ ") + hs.note
	}
	return text
}

// historyPills count a batch of syncs: done of total, and failures.
func (a *app) historyPills() []string {
	hs := a.history
	if hs.syncing == "" || hs.batchTotal <= 1 {
		return nil
	}
	pills := []string{pill(fmt.Sprintf("%d/%d", hs.batchDone+1, hs.batchTotal), colorAccent, colorInk)}
	if hs.batchFailed > 0 {
		pills = append(pills, pill(fmt.Sprintf("%d failed", hs.batchFailed), colorRed, colorInk))
	}
	return pills
}

// syncResultText is "1m +12.3K · 1d up to date".
func syncResultText(results []marketdata.SyncResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		switch {
		case result.Skipped:
			parts = append(parts, string(result.Interval)+" up to date")
		default:
			parts = append(parts, fmt.Sprintf("%s +%s", result.Interval, formatCount(float64(result.Candles))))
		}
	}
	return strings.Join(parts, " · ")
}

// syncedCellText is the table's short form of a sync: the candles added.
func syncedCellText(results []marketdata.SyncResult) string {
	total := 0
	for _, result := range results {
		total += result.Candles
	}
	if total == 0 {
		return "✔ up to date"
	}
	return "✔ +" + formatCount(float64(total))
}

func (a *app) historyHelp() string {
	return help("↑↓", "select", "s", "sync 1m + 1d", "A", "sync all", "X", "stop", "R", "rebuild list", "I", "instruments", "Q", "back", "^C", "quit")
}

func (a *app) historyModeName() string {
	if a.history.syncing != "" {
		return "HISTORY · SYNCING"
	}
	return "HISTORY"
}

func (a *app) historyView(w, h int) string {
	hs := a.history
	detailW := 60
	if w < 120 {
		detailW = 0
	}
	leftW := w - detailW
	title := marketdata.DefaultSyncListName
	info := fmt.Sprintf("%d instruments", len(hs.grid.rows))
	if len(hs.queue) > 0 {
		info += fmt.Sprintf(" · %d queued", len(hs.queue))
	}
	left := panel(title, info, hs.grid.view(leftW-2, h-2, true), leftW, h, true)
	if detailW == 0 {
		return left
	}
	return joinColumns(left, textPanel(string(hs.detailID), "", hs.detailText(detailW-4), detailW, h, false))
}

// detailText is the selected instrument's stored ranges and latest
// candles, newest first.
func (hs *historyScreen) detailText(w int) string {
	if hs.detailID == "" {
		return styleMuted.Render("Select an instrument")
	}
	var lines []string
	for _, interval := range []models.CandleInterval{models.CandleInterval1m, models.CandleInterval1d} {
		row, ok := hs.availability[hs.detailID][interval]
		text := styleFaint.Render("none stored · press s to sync")
		if ok && !row.HistStart.IsZero() {
			layout := "02 Jan 2006 15:04"
			if interval == models.CandleInterval1d {
				layout = "02 Jan 2006"
			}
			text = styleText.Render(models.ExchangeClockTime(row.HistStart).Format(layout) + " → " + models.ExchangeClockTime(row.HistEnd).Format(layout))
		}
		lines = append(lines, styleKey.Render(padRight(string(interval), 3))+" "+text)
	}
	if hs.detailErr != nil {
		return strings.Join(append(lines, "", styleRed.Render(hs.detailErr.Error())), "\n")
	}
	lines = append(lines, "", candleTable("Latest 1m", hs.minute, "02 Jan 15:04"), "", candleTable("Latest 1d", hs.daily, "02 Jan 2006"))
	return strings.Join(lines, "\n")
}

func candleTable(title string, rows []candles.Candle, layout string) string {
	if len(rows) == 0 {
		return styleHeader.Render(title) + "\n" + styleFaint.Render("no candles")
	}
	out := []string{styleHeader.Render(fmt.Sprintf("%-12s %8s %8s %8s %8s %7s", title, "OPEN", "HIGH", "LOW", "CLOSE", "VOL"))}
	for index := len(rows) - 1; index >= 0; index-- {
		c := rows[index]
		closeStyle := styleGreen
		if c.Close < c.Open {
			closeStyle = styleRed
		}
		out = append(out, styleMuted.Render(fmt.Sprintf("%-12s", models.ExchangeClockTime(c.Timestamp).Format(layout)))+
			styleText.Render(fmt.Sprintf(" %8.2f %8.2f %8.2f ", c.Open, c.High, c.Low))+
			closeStyle.Render(fmt.Sprintf("%8.2f", c.Close))+
			styleFaint.Render(fmt.Sprintf(" %7s", formatCount(float64(c.Volume)))))
	}
	return strings.Join(out, "\n")
}

// selectedInstrumentID is the explorer's selected search result, chain
// contract, future or list item.
func (e *explorerScreen) selectedInstrumentID() (models.InstrumentID, bool) {
	if instrument, ok := e.selectedInstrument(); ok {
		return instrument.ID, true
	}
	if e.mode == viewLists && e.focus == 1 {
		id, ok := e.itemsGrid.selectedRef().(models.InstrumentID)
		return id, ok
	}
	return "", false
}
