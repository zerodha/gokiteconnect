package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	marketdata "github.com/devshoe/gokiteconnect/market-data-service"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/nse"
)

// adminTab is one of the Admin screen's sub tabs.
type adminTab int

const (
	adminHistory adminTab = iota
	adminInstruments
	adminLists
)

var adminTabs = []struct {
	icon  string
	label string
	tab   adminTab
}{{"◷", "Historical Data", adminHistory}, {"▦", "Instruments", adminInstruments}, {"☰", "Admin Lists", adminLists}}

// adminScreen holds the Admin screen's Instruments and Admin Lists tabs;
// Historical Data is the history screen.
type adminScreen struct {
	tab   adminTab
	spots []adminSpot

	// Instruments summarises the catalog.
	summaryStarted bool
	summary        catalogSummary
	summaryGrid    grid
	summaryStatus  string
	// fundamentals summarises the stored screener fundamentals.
	fundamentals    fundamentalsSummary
	fundamentalsErr string

	// Admin Lists shows the lists ADMIN owns beside the NSE indices that
	// can be downloaded as lists.
	listsStarted bool
	lists        []models.List
	indices      []nse.NSEIndexMetadata
	listsGrid    grid
	itemsGrid    grid
	listsStatus  string
}

type adminSpot struct {
	start, end int
	tab        adminTab
}

func newAdminScreen() *adminScreen {
	right := func(title string, width int) column { return column{title: title, width: width, align: alignRight} }
	return &adminScreen{
		summaryStatus: statusInfo("Reading the catalog…"),
		listsStatus:   statusInfo("Loading ADMIN's lists…"),
		summaryGrid: grid{columns: []column{
			{title: "EXCHANGE", width: 10}, right("EQ", 9), right("INDEX", 7), right("FUT", 9),
			right("CE", 9), right("PE", 9), right("OTHER", 8), right("TOTAL", 10),
		}, lines: true, empty: styleMuted.Render("Reading the catalog…")},
		listsGrid: grid{columns: []column{
			{title: "LIST", width: 18}, {title: "SOURCE", width: 24, flex: 1}, right("ITEMS", 6), {title: "UPDATED", width: 15},
		}, lines: true, empty: styleMuted.Render("Loading ADMIN's lists…")},
		itemsGrid: grid{columns: []column{{title: "INSTRUMENT", width: 20, flex: 1}}, empty: styleMuted.Render("Not downloaded")},
	}
}

type (
	// catalogSummary counts the catalog's instruments by exchange and kind.
	catalogSummary struct {
		lastRefreshed time.Time
		underlyings   int
		counts        map[string]map[string]int
		total         int
	}
	catalogSummaryMsg struct {
		summary catalogSummary
		err     error
		// refreshed is set after a refresh from Kite.
		refreshed bool
	}
	adminListsMsg struct {
		lists      []models.List
		err        error
		indices    []nse.NSEIndexMetadata
		indicesErr error
		// opErr is a failed download or deletion; selectName is the list to
		// select afterwards and done the status to show.
		opErr      error
		selectName string
		done       string
	}
)

// summaryKinds are the summary's columns, after the exchange.
var summaryKinds = []string{"EQ", "INDEX", "FUT", "CE", "PE", "OTHER"}

func instrumentKind(instrument models.Instrument) string {
	if instrument.Segment == "INDICES" {
		return "INDEX"
	}
	switch instrument.InstrumentType {
	case "EQ", "FUT", "CE", "PE":
		return instrument.InstrumentType
	}
	return "OTHER"
}

func summarise(instruments []models.Instrument) catalogSummary {
	summary := catalogSummary{counts: make(map[string]map[string]int), total: len(instruments)}
	for _, instrument := range instruments {
		byKind := summary.counts[instrument.Exchange]
		if byKind == nil {
			byKind = make(map[string]int)
			summary.counts[instrument.Exchange] = byKind
		}
		byKind[instrumentKind(instrument)]++
	}
	return summary
}

// openAdmin shows the Admin screen, loading its open tab the first time.
func (a *app) openAdmin() tea.Cmd {
	if a.screen != screenHistory {
		a.historyBack = a.screen
	}
	a.show(screenHistory)
	return a.startAdminTab()
}

func (a *app) startAdminTab() tea.Cmd {
	ad := a.admin
	switch ad.tab {
	case adminInstruments:
		if !ad.summaryStarted {
			ad.summaryStarted = true
			return tea.Batch(a.loadCatalogSummary(false), a.loadFundamentals(false))
		}
	case adminLists:
		if !ad.listsStarted {
			ad.listsStarted = true
			return a.loadAdminLists(true)
		}
	default:
		if !a.history.started {
			a.history.started = true
			return a.loadHistory(false)
		}
	}
	return nil
}

// cycleAdminTab moves step sub tabs along, wrapping around.
func (a *app) cycleAdminTab(step int) tea.Cmd {
	ad := a.admin
	ad.tab = adminTab((int(ad.tab) + step + len(adminTabs)) % len(adminTabs))
	return a.startAdminTab()
}

// loadCatalogSummary counts the catalog, refreshing it from Kite first
// when refresh is set.
func (a *app) loadCatalogSummary(refresh bool) tea.Cmd {
	open := a.deps.openCatalog
	message := "Reading the catalog…"
	if refresh {
		message = "Refreshing the complete Kite instrument catalog…"
	}
	return tea.Batch(a.busy(message), a.task(func(ctx context.Context) tea.Msg {
		msg := catalogSummaryMsg{refreshed: refresh}
		catalog, _, err := open(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		if refresh {
			if msg.err = catalog.Refresh(ctx); msg.err != nil {
				return msg
			}
		}
		instruments, err := catalog.List(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.summary = summarise(instruments)
		if msg.summary.lastRefreshed, msg.err = catalog.LastRefreshedAt(ctx); msg.err != nil {
			return msg
		}
		underlyings, err := catalog.ListFO(ctx)
		msg.summary.underlyings, msg.err = len(underlyings), err
		return msg
	}))
}

// loadAdminLists reads ADMIN's lists, and the NSE index list when indices
// is set and an index source is configured.
func (a *app) loadAdminLists(indices bool) tea.Cmd {
	return a.adminListOperation("Loading ADMIN's lists…", indices, nil)
}

// adminListOperation runs operation, then reloads ADMIN's lists, whether or
// not it failed: a batch download can fail part-way.
func (a *app) adminListOperation(message string, indices bool, operation func(context.Context, listStore) adminListsMsg) tea.Cmd {
	open, source := a.deps.openCatalog, a.deps.indices
	return tea.Batch(a.busy(message), a.task(func(ctx context.Context) tea.Msg {
		var msg adminListsMsg
		_, store, err := open(ctx)
		if err == nil && store == nil {
			err = errors.New("lists are unavailable for this catalog")
		}
		if err != nil {
			msg.err = err
			return msg
		}
		if operation != nil {
			msg = operation(ctx, store)
		}
		if indices && source != nil {
			msg.indices, msg.indicesErr = source.ListIndices(ctx)
		}
		msg.lists, msg.err = store.GetLists(ctx, models.AdminUser)
		return msg
	}))
}

func (a *app) handleAdmin(msg tea.Msg) (tea.Cmd, bool) {
	ad := a.admin
	switch msg := msg.(type) {
	case fundamentalsMsg:
		a.handleFundamentals(msg)
		return nil, true
	case catalogSummaryMsg:
		a.closeOverlay()
		if msg.err != nil {
			ad.summaryStatus = statusErr("Could not read the catalog:", msg.err.Error())
			if msg.refreshed {
				ad.summaryStatus = statusErr("Catalog refresh failed:", msg.err.Error())
			}
			ad.summaryGrid.empty = styleRed.Render("No summary · press R to refresh the catalog from Kite")
			return nil, true
		}
		ad.summary = msg.summary
		a.renderSummary()
		ad.summaryStatus = statusInfo(fmt.Sprintf("%s instruments", formatCount(float64(msg.summary.total))))
		if msg.refreshed {
			ad.summaryStatus = statusOK("Catalog refreshed from Kite · " + formatCount(float64(msg.summary.total)) + " instruments")
			// The explorer reads the same catalog; its header shows when.
			a.explorer.lastRefreshed = msg.summary.lastRefreshed
			a.explorer.underlyingCount = msg.summary.underlyings
		}
		return nil, true
	case adminListsMsg:
		a.closeOverlay()
		if msg.indices != nil {
			ad.indices = msg.indices
		}
		if msg.err != nil {
			ad.listsStatus = statusErr("Could not load ADMIN's lists:", msg.err.Error())
			return nil, true
		}
		ad.lists = msg.lists
		a.renderAdminLists(msg.selectName)
		ad.listsStatus = statusInfo(fmt.Sprintf("%d stored %s · %d NSE indices", len(ad.lists), pluralize("list", len(ad.lists)), len(ad.indices)))
		switch {
		case msg.opErr != nil:
			a.showError(msg.opErr)
		case msg.done != "":
			ad.listsStatus = statusOK(msg.done)
		case msg.indicesErr != nil:
			ad.listsStatus = statusErr("Could not fetch the NSE index list:", msg.indicesErr.Error())
		}
		return nil, true
	}
	return nil, false
}

func (a *app) renderSummary() {
	ad := a.admin
	exchanges := make([]string, 0, len(ad.summary.counts))
	for exchange := range ad.summary.counts {
		exchanges = append(exchanges, exchange)
	}
	slices.Sort(exchanges)
	totals := make(map[string]int)
	countCell := func(count int, style lipgloss.Style) cell {
		if count == 0 {
			return textCell("—", styleFaint)
		}
		return textCell(formatThousands(count), style)
	}
	rows := make([]row, 0, len(exchanges)+1)
	for _, exchange := range exchanges {
		cells := []cell{textCell(exchange, styleCyan)}
		sum := 0
		for _, kind := range summaryKinds {
			count := ad.summary.counts[exchange][kind]
			totals[kind] += count
			sum += count
			cells = append(cells, countCell(count, styleText))
		}
		rows = append(rows, row{ref: exchange, cells: append(cells, countCell(sum, styleBold))})
	}
	if len(exchanges) > 1 {
		cells := []cell{textCell("ALL", styleBold)}
		for _, kind := range summaryKinds {
			cells = append(cells, countCell(totals[kind], styleBold))
		}
		rows = append(rows, row{ref: "", cells: append(cells, countCell(ad.summary.total, styleYellow.Bold(true)))})
	}
	ad.summaryGrid.empty = styleMuted.Render("The catalog is empty · press R to refresh it from Kite")
	ad.summaryGrid.setRows(rows)
}

// formatThousands is 12,345.
func formatThousands(count int) string {
	text := strconv.Itoa(count)
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + "," + text[index:]
	}
	return text
}

// adminListRow is a row of Admin Lists: a stored list, an NSE index that
// may not be downloaded yet, or both.
type adminListRow struct {
	name     string
	metadata *nse.NSEIndexMetadata
	list     *models.List
}

// adminListRows are the F&O list, then every NSE index, then ADMIN's other
// lists, such as DEFAULT SYNC.
func (ad *adminScreen) adminListRows() []adminListRow {
	byName := make(map[string]*models.List, len(ad.lists))
	for index := range ad.lists {
		list := &ad.lists[index]
		if list.CreatedBy == models.AdminUser {
			byName[list.Name] = list
		}
	}
	var rows []adminListRow
	seen := make(map[string]bool)
	for _, metadata := range append([]nse.NSEIndexMetadata{foListMetadata}, ad.indices...) {
		if seen[metadata.ListName] {
			continue
		}
		seen[metadata.ListName] = true
		rows = append(rows, adminListRow{name: metadata.ListName, metadata: &metadata, list: byName[metadata.ListName]})
	}
	var others []adminListRow
	for name, list := range byName {
		if !seen[name] {
			others = append(others, adminListRow{name: name, list: list})
		}
	}
	slices.SortFunc(others, func(x, y adminListRow) int { return cmp.Compare(x.name, y.name) })
	return append(rows, others...)
}

func (a *app) renderAdminLists(selectName string) {
	ad := a.admin
	if selectName == "" {
		if current, ok := ad.listsGrid.selectedRef().(adminListRow); ok {
			selectName = current.name
		}
	}
	listRows := ad.adminListRows()
	rows := make([]row, 0, len(listRows))
	for _, listRow := range listRows {
		source := textCell("", styleFaint)
		switch {
		case listRow.metadata != nil:
			source = textCell(listRow.metadata.IndexName, styleText)
		case listRow.list != nil:
			source = textCell(listSource(*listRow.list), styleMuted)
		}
		items, updated := textCell("—", styleFaint), textCell("not downloaded", styleFaint)
		if list := listRow.list; list != nil {
			items = textCell(strconv.Itoa(len(list.Items)), styleGreen)
			updated = textCell(list.UpdatedAt.Local().Format("02 Jan 06 15:04"), styleMuted)
		}
		rows = append(rows, row{ref: listRow, cells: []cell{textCell(listRow.name, styleCyan), source, items, updated}})
	}
	ad.listsGrid.empty = styleMuted.Render("No lists")
	ad.listsGrid.setRows(rows)
	if selectName != "" {
		ad.listsGrid.selectWhere(func(r row) bool { return r.ref.(adminListRow).name == selectName })
	}
	a.renderAdminItems()
}

func (a *app) renderAdminItems() {
	ad := a.admin
	ad.itemsGrid.cursor, ad.itemsGrid.offset = 0, 0
	selected, ok := ad.listsGrid.selectedRef().(adminListRow)
	if !ok || selected.list == nil {
		ad.itemsGrid.empty = styleMuted.Render("Not downloaded · Enter downloads it")
		ad.itemsGrid.setRows(nil)
		return
	}
	ad.itemsGrid.empty = styleMuted.Render("No items")
	rows := make([]row, 0, len(selected.list.Items))
	for _, item := range selected.list.Items {
		rows = append(rows, row{ref: item.InstrumentID, cells: []cell{textCell(string(item.InstrumentID), styleCyan)}})
	}
	ad.itemsGrid.setRows(rows)
}

func (a *app) adminKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.show(a.historyBack)
		return nil
	case "[", "]":
		step := 1
		if key == "[" {
			step = -1
		}
		return a.cycleAdminTab(step)
	}
	switch a.admin.tab {
	case adminInstruments:
		if a.admin.summaryGrid.update(key) {
			return nil
		}
		switch strings.ToLower(key) {
		case "r":
			return a.loadCatalogSummary(true)
		case "f":
			return a.loadFundamentals(true)
		}
		return nil
	case adminLists:
		return a.adminListsKey(key)
	}
	return a.historyKey(msg)
}

func (a *app) adminListsKey(key string) tea.Cmd {
	ad := a.admin
	if ad.listsGrid.update(key) {
		a.renderAdminItems()
		return nil
	}
	switch key {
	case "enter":
		return a.refreshAdminList()
	case "delete":
		return a.deleteAdminList()
	}
	switch strings.ToLower(key) {
	case "a":
		return a.downloadAllIndices()
	case "r":
		if a.deps.indices == nil {
			ad.listsStatus = statusWarn("The NSE index source is unavailable.")
			return nil
		}
		return a.adminListOperation("Fetching the NSE index list from niftyindices.com…", true, nil)
	case "d":
		return a.deleteAdminList()
	}
	return nil
}

// refreshAdminList downloads the selected index again, or rebuilds the
// selected generated list.
func (a *app) refreshAdminList() tea.Cmd {
	ad := a.admin
	selected, ok := ad.listsGrid.selectedRef().(adminListRow)
	if !ok {
		return nil
	}
	metadata := selected.metadata
	if metadata == nil && selected.list != nil && selected.list.IsIndex && selected.list.URL != "" {
		metadata = &nse.NSEIndexMetadata{ListName: selected.list.Name, IndexName: selected.list.Name, IndexPageURL: selected.list.URL}
	}
	if metadata != nil {
		return a.downloadIndices([]nse.NSEIndexMetadata{*metadata})
	}
	if selected.name == marketdata.DefaultSyncListName {
		return a.adminListOperation("Rebuilding "+selected.name+" from the catalog…", false, func(ctx context.Context, store listStore) adminListsMsg {
			catalog, _, err := a.deps.openCatalog(ctx)
			if err == nil {
				_, err = marketdata.RebuildDefaultSyncList(ctx, catalog, store)
			}
			return adminListsMsg{opErr: err, selectName: selected.name, done: "Rebuilt " + selected.name}
		})
	}
	ad.listsStatus = statusWarn(selected.name + " isn't downloaded or generated, so it can't be refreshed.")
	return nil
}

func (a *app) downloadAllIndices() tea.Cmd {
	ad := a.admin
	indices := append([]nse.NSEIndexMetadata{foListMetadata}, ad.indices...)
	a.confirm("Download all",
		fmt.Sprintf("Download %s as ADMIN lists? Each replaces the stored copy.",
			styleYellow.Bold(true).Render(fmt.Sprintf("%d lists", len(indices)))),
		"Download", func(a *app) tea.Cmd { return a.downloadIndices(indices) })
	return nil
}

func (a *app) deleteAdminList() tea.Cmd {
	selected, ok := a.admin.listsGrid.selectedRef().(adminListRow)
	if !ok || selected.list == nil {
		return nil
	}
	list := *selected.list
	a.confirm("Delete list",
		fmt.Sprintf("Delete ADMIN list %s with %d items? Smart watchlists that follow it keep its last items.",
			styleYellow.Bold(true).Render(list.Name), len(list.Items)),
		"Delete", func(a *app) tea.Cmd {
			return a.adminListOperation("Deleting list…", false, func(ctx context.Context, store listStore) adminListsMsg {
				return adminListsMsg{opErr: store.DeleteList(ctx, list.ID), selectName: list.Name, done: "Deleted " + list.Name}
			})
		})
	return nil
}

// foListMetadata is the master-list entry for the NSE and BSE underlyings
// that have futures or options. It is built from the catalog rather than
// niftyindices.
var foListMetadata = nse.NSEIndexMetadata{IndexName: "F&O stocks and indices (from catalog)", ListName: "FO_LIST"}

// buildFOList lists the NSE and BSE stocks and indices with active futures
// or options, which on BSE are SENSEX and BANKEX.
func buildFOList(ctx context.Context, catalog catalog) (models.List, error) {
	if catalog == nil {
		return models.List{}, errNoCatalog
	}
	underlyings, err := catalog.ListFO(ctx, "NSE", "BSE")
	if err != nil {
		return models.List{}, err
	}
	list := models.List{Name: foListMetadata.ListName, AutoGenerated: true}
	for _, instrument := range underlyings {
		list.Items = append(list.Items, models.ListItem{InstrumentID: instrument.ID})
	}
	if len(list.Items) == 0 {
		return models.List{}, errors.New("no F&O underlyings in the catalog; refresh it first")
	}
	return list, nil
}

// downloadIndices downloads each index and saves it as an ADMIN list,
// continuing past failures.
func (a *app) downloadIndices(indices []nse.NSEIndexMetadata) tea.Cmd {
	if len(indices) == 0 {
		return nil
	}
	source, open := a.deps.indices, a.deps.openCatalog
	message := fmt.Sprintf("Downloading %s…", indices[0].IndexName)
	if len(indices) > 1 {
		message = fmt.Sprintf("Downloading %d NSE indices…", len(indices))
	}
	return a.adminListOperation(message, false, func(ctx context.Context, store listStore) adminListsMsg {
		msg := adminListsMsg{done: fmt.Sprintf("Saved %d NSE index list(s)", len(indices))}
		catalog, _, catalogErr := open(ctx)
		var errs []error
		for i, metadata := range indices {
			if len(indices) > 1 {
				a.progress(fmt.Sprintf("Downloading %s (%d/%d)…", metadata.IndexName, i+1, len(indices)))
			}
			var list models.List
			var err error
			switch {
			case metadata == foListMetadata && catalogErr != nil:
				err = catalogErr
			case metadata == foListMetadata:
				list, err = buildFOList(ctx, catalog)
			case source == nil:
				err = errors.New("the NSE index source is unavailable")
			default:
				list, err = source.GetIndex(ctx, metadata)
			}
			if err == nil {
				list.CreatedBy = models.AdminUser
				list, err = store.ReplaceAutoGeneratedList(ctx, list)
			}
			if ctx.Err() != nil {
				msg.opErr = ctx.Err()
				return msg
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", metadata.IndexName, err))
				continue
			}
			msg.selectName = list.Name
		}
		if len(errs) > 0 {
			msg.opErr = fmt.Errorf("%d of %d downloads failed: %w", len(errs), len(indices), errors.Join(errs...))
		}
		return msg
	})
}

func (a *app) adminHelp() string {
	switch a.admin.tab {
	case adminInstruments:
		return help("[ ]", "sub tab", "R", "refresh catalog from Kite", "F", "refresh fundamentals", "W", "watchlists", "Esc", "back", "^C", "quit")
	case adminLists:
		return help("[ ]", "sub tab", "↑↓", "select", "Enter", "download / refresh", "A", "download all", "R", "reload NSE index list", "D", "delete", "W", "watchlists", "Esc", "back")
	}
	return help("[ ]", "sub tab", "↑↓", "select", "s", "sync 1m + 1d", "A", "sync all", "X", "stop", "R", "rebuild list", "W", "watchlists", "Esc", "back", "^C", "quit")
}

func (a *app) adminModeName() string {
	switch a.admin.tab {
	case adminInstruments:
		return "ADMIN · INSTRUMENTS"
	case adminLists:
		return "ADMIN · LISTS"
	}
	if a.history.syncing != "" {
		return "ADMIN · SYNCING"
	}
	return "ADMIN · DATA"
}

func (a *app) adminStatus() string {
	switch a.admin.tab {
	case adminInstruments:
		return a.admin.summaryStatus
	case adminLists:
		return a.admin.listsStatus
	}
	return a.historyStatus()
}

// adminPills keep a running sync's count in view from every sub tab.
func (a *app) adminPills() []string {
	return a.historyPills()
}

// adminGrids are the open sub tab's tables.
func (a *app) adminGrids() []*grid {
	switch a.admin.tab {
	case adminInstruments:
		return []*grid{&a.admin.summaryGrid}
	case adminLists:
		return []*grid{&a.admin.listsGrid}
	}
	return []*grid{&a.history.grid}
}

func (a *app) adminSelectionChanged() tea.Cmd {
	switch a.admin.tab {
	case adminLists:
		a.renderAdminItems()
		return nil
	case adminInstruments:
		return nil
	}
	return a.historySelectionChanged()
}

// adminTabBar is the sub tab bar on the screen's first line, line y: a
// strip w wide with the open sub tab as an accent pill.
func (a *app) adminTabBar(w int) string {
	ad := a.admin
	ad.spots = ad.spots[:0]
	strip := lipgloss.NewStyle().Background(colorStatusBar)
	line := strip.Render(" ")
	for _, tab := range adminTabs {
		label := strip.Foreground(colorMuted)
		icon := strip.Foreground(colorFaint)
		if tab.tab == ad.tab {
			label = lipgloss.NewStyle().Background(colorAccent).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
			icon = label
		}
		start := ansi.StringWidth(line)
		line += label.Render("  ") + icon.Render(tab.icon) + label.Render(" "+tab.label+"  ")
		ad.spots = append(ad.spots, adminSpot{start: start, end: ansi.StringWidth(line), tab: tab.tab})
		line += strip.Render(" ")
	}
	hint := strip.Foreground(colorKey).Bold(true).Render("[ ]") + strip.Foreground(colorFaint).Render(" switch ")
	gap := w - ansi.StringWidth(line) - ansi.StringWidth(hint)
	if gap < 1 {
		return ansi.Truncate(line+strip.Render(strings.Repeat(" ", max(w-ansi.StringWidth(line), 0))), w, "")
	}
	return line + strip.Render(strings.Repeat(" ", gap)) + hint
}

// adminClick switches sub tabs from the sub tab bar.
func (a *app) adminClick(x int) tea.Cmd {
	for _, spot := range a.admin.spots {
		if x >= spot.start && x < spot.end {
			a.admin.tab = spot.tab
			return a.startAdminTab()
		}
	}
	return nil
}

// adminView draws the sub tab bar over the open sub tab; the panels start
// on screen line 2.
func (a *app) adminView(w, h int) string {
	ad := a.admin
	bar := a.adminTabBar(w)
	h--
	const top = 2
	var body string
	switch ad.tab {
	case adminInstruments:
		ad.summaryGrid.x, ad.summaryGrid.y = 1, top+7
		body = a.instrumentsSummaryView(w, h)
	case adminLists:
		widths := splitWidth(w, 3, 1)
		ad.listsGrid.x, ad.listsGrid.y = 1, top+2
		lists := panel("ADMIN lists", styleMuted.Render(fmt.Sprintf("%d stored", len(ad.lists))), ad.listsGrid.view(widths[0]-2, h-2, true), widths[0], h, true)
		title := "Items"
		if selected, ok := ad.listsGrid.selectedRef().(adminListRow); ok {
			title = selected.name
		}
		items := panel(title, styleMuted.Render(pluralize("item", len(ad.itemsGrid.rows))), ad.itemsGrid.view(widths[1]-2, h-2, false), widths[1], h, false)
		body = joinColumns(lists, items)
	default:
		a.history.grid.x, a.history.grid.y = 1, top+2
		body = a.historyView(w, h)
	}
	return bar + "\n" + body
}

func (a *app) instrumentsSummaryView(w, h int) string {
	ad := a.admin
	refreshed := styleFaint.Render("never")
	if at := ad.summary.lastRefreshed; !at.IsZero() {
		refreshed = styleText.Render(at.In(models.ExchangeLocation).Format("02 Jan 2006 15:04")) +
			styleFaint.Render(" · "+relativeAge(time.Since(at)))
	}
	fundamentals := styleFaint.Render("unavailable")
	if a.deps.fundamentals != nil {
		fundamentals = ad.fundamentalsLine()
	}
	lines := []string{
		" " + styleMuted.Render(padRight("Catalog", 18)) + styleText.Render(a.deps.catalogPath),
		" " + styleMuted.Render(padRight("Last refreshed", 18)) + refreshed,
		" " + styleMuted.Render(padRight("F&O underlyings", 18)) + styleYellow.Render(formatThousands(ad.summary.underlyings)),
		" " + styleMuted.Render(padRight("Fundamentals", 18)) + fundamentals,
		"",
	}
	inner := w - 4
	gridH := max(h-2-len(lines), 1)
	body := strings.Join(lines, "\n") + "\n" + ad.summaryGrid.view(inner, gridH, true)
	return panel("Instrument catalog", styleMuted.Render(formatThousands(ad.summary.total)+" instruments"), body, w, h, true)
}

// relativeAge is "3h ago" or "2d ago".
func relativeAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(age.Hours()/24))
}
