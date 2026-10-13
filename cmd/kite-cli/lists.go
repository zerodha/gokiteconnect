package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/nse"
)

// listsLoadedMsg ends a list operation: the lists reloaded after it, and
// done, run with the list ID the operation returned.
type listsLoadedMsg struct {
	lists    []models.List
	opErr    error
	loadErr  error
	selectID string
	done     func(a *app, selectID string) tea.Cmd
}

type masterIndicesMsg struct {
	indices []nse.NSEIndexMetadata
	err     error
}

func (a *app) handleLists(msg tea.Msg) (tea.Cmd, bool) {
	e := a.explorer
	switch msg := msg.(type) {
	case listsLoadedMsg:
		if msg.loadErr != nil {
			a.showError(errors.Join(msg.opErr, msg.loadErr))
			return nil, true
		}
		e.listData = msg.lists
		a.closeOverlay()
		var cmd tea.Cmd
		if msg.done != nil {
			cmd = msg.done(a, msg.selectID)
		}
		if msg.opErr != nil {
			a.showError(msg.opErr)
		}
		return cmd, true
	case masterIndicesMsg:
		if msg.err != nil {
			a.showError(fmt.Errorf("fetch NSE indices: %w", msg.err))
			return nil, true
		}
		e.masterIndices = msg.indices
		a.closeOverlay()
		a.showMasterList(0)
		return nil, true
	}
	return a.handleOrders(msg)
}

// listOperation runs operation in the background, then reloads every list
// and runs done with the list ID the operation returned. The lists reload
// even when the operation fails, because a batch download can fail part-way.
func (a *app) listOperation(message string, operation func(context.Context, listStore) (string, error), done func(*app, string) tea.Cmd) tea.Cmd {
	store := a.explorer.lists
	if store == nil {
		return nil
	}
	return tea.Batch(a.busy(message), a.task(func(ctx context.Context) tea.Msg {
		msg := listsLoadedMsg{done: done}
		if operation != nil {
			msg.selectID, msg.opErr = operation(ctx, store)
		}
		msg.lists, msg.loadErr = store.GetLists(ctx, "")
		return msg
	}))
}

// openLists loads every list and switches to the lists view.
func (a *app) openLists() tea.Cmd {
	e := a.explorer
	if e.lists == nil {
		e.status = statusWarn("Lists are unavailable for this catalog.")
		return nil
	}
	if e.mode == viewDashboard {
		e.takeSnapshot()
	}
	e.searchGen++
	return a.listOperation("Loading lists…", nil, (*app).renderLists)
}

// renderLists shows the lists and selects selectID, or keeps the current
// selection when it is empty.
func (a *app) renderLists(selectID string) tea.Cmd {
	e := a.explorer
	if selectID == "" {
		if list, ok := e.selectedList(); ok {
			selectID = list.ID
		}
	}
	reset := e.mode != viewLists
	e.mode = viewLists
	e.chain = nil
	e.blurSearch()
	rows := make([]row, 0, len(e.listData))
	cursor := 0
	for index, list := range e.listData {
		if list.ID == selectID {
			cursor = index
		}
		rows = append(rows, row{ref: list, cells: []cell{
			textCell(strconv.Itoa(list.Number), styleMuted),
			textCell(list.Name, styleCyan),
			textCell(strconv.Itoa(len(list.Items)), styleGreen),
			textCell(list.CreatedBy, styleKey.UnsetBold()),
			textCell(listSource(list), styleYellow),
			textCell(list.UpdatedAt.Local().Format("02 Jan 2006 15:04"), styleMuted),
		}})
	}
	previous, _ := e.selectedList()
	e.listsGrid.setRows(rows)
	e.listsGrid.cursor = cursor
	current, _ := e.selectedList()
	e.renderListItems(reset || previous.ID != current.ID)
	if reset || e.focus == 1 && len(e.itemsGrid.rows) == 0 {
		e.focus = 0
	}
	e.status = statusInfo(fmt.Sprintf("%d lists", len(e.listData)))
	return a.refreshQuotes()
}

// renderListItems shows the selected list's items, from the top when reset
// is set.
func (e *explorerScreen) renderListItems(reset bool) {
	list, ok := e.selectedList()
	if !ok {
		e.itemsGrid.empty = styleMuted.Render("No lists yet. Press N to create one or M to load NSE indices.")
		e.itemsGrid.setRows(nil)
		return
	}
	e.itemsGrid.empty = styleMuted.Render("No items · press A to add an instrument")
	rows := make([]row, 0, len(list.Items))
	for _, item := range list.Items {
		ltp, change := e.priceCells(item.InstrumentID)
		rows = append(rows, row{ref: item.InstrumentID, cells: []cell{
			textCell(string(item.InstrumentID), styleCyan),
			ltp, change,
			textCell(item.CreatedAt.Local().Format("02 Jan 2006 15:04"), styleMuted),
		}})
	}
	if reset {
		e.itemsGrid.cursor, e.itemsGrid.offset = 0, 0
	}
	e.itemsGrid.setRows(rows)
}

func (e *explorerScreen) selectedList() (models.List, bool) {
	list, ok := e.listsGrid.selectedRef().(models.List)
	return list, ok
}

func (e *explorerScreen) selectedListItem() (models.InstrumentID, bool) {
	id, ok := e.itemsGrid.selectedRef().(models.InstrumentID)
	return id, ok
}

// listKey handles the keys that only apply in the lists view.
func (a *app) listKey(key string) (tea.Cmd, bool) {
	e := a.explorer
	switch key {
	case "tab", "shift+tab":
		e.focus = 1 - e.focus
		if e.focus == 1 && len(e.itemsGrid.rows) == 0 {
			e.focus = 0
		}
		return nil, true
	case "enter":
		if e.focus == 0 {
			if len(e.itemsGrid.rows) > 0 {
				e.focus = 1
			}
			return nil, true
		}
		return a.orderSelected(), true
	case "delete":
		return a.deleteSelected(), true
	}
	switch strings.ToLower(key) {
	case "n":
		a.overlay = newInputOverlay("New list", "Name", "", "Save", func(a *app, name string) tea.Cmd {
			return a.listOperation("Creating list…", func(ctx context.Context, store listStore) (string, error) {
				list, err := store.CreateList(ctx, models.List{CreatedBy: models.AdminUser, Name: name})
				return list.ID, err
			}, (*app).renderLists)
		})
	case "e":
		a.renameSelectedList()
	case "a":
		a.addInstrumentByID()
	case "d":
		return a.deleteSelected(), true
	case "r":
		return a.refreshSelectedList(), true
	case "m":
		return a.openMasterList(), true
	default:
		return nil, false
	}
	return nil, true
}

func (a *app) renameSelectedList() {
	list, ok := a.explorer.selectedList()
	if !ok {
		return
	}
	a.overlay = newInputOverlay("Rename list", "Name", list.Name, "Save", func(a *app, name string) tea.Cmd {
		return a.listOperation("Renaming list…", func(ctx context.Context, store listStore) (string, error) {
			return list.ID, store.RenameList(ctx, list.ID, name)
		}, (*app).renderLists)
	})
}

// addInstrumentByID adds an instrument typed by ID to the selected list
// after checking that the catalog knows it.
func (a *app) addInstrumentByID() {
	list, ok := a.explorer.selectedList()
	if !ok {
		return
	}
	catalog := a.explorer.catalog
	a.overlay = newInputOverlay("Add to "+list.Name, "Instrument ID", "", "Add", func(a *app, text string) tea.Cmd {
		id := models.InstrumentID(strings.ToUpper(text))
		return a.listOperation(fmt.Sprintf("Adding %s…", id), func(ctx context.Context, store listStore) (string, error) {
			if _, err := catalog.Get(ctx, id); err != nil {
				return list.ID, fmt.Errorf("%s: %w", id, err)
			}
			return list.ID, store.AddListItems(ctx, list.ID, []models.InstrumentID{id})
		}, (*app).renderLists)
	})
}

// deleteSelected removes the selected item when the items have focus, and
// otherwise deletes the selected list after confirmation.
func (a *app) deleteSelected() tea.Cmd {
	e := a.explorer
	list, ok := e.selectedList()
	if !ok {
		return nil
	}
	if e.focus == 1 {
		id, ok := e.selectedListItem()
		if !ok {
			return nil
		}
		return a.listOperation(fmt.Sprintf("Removing %s…", id), func(ctx context.Context, store listStore) (string, error) {
			return list.ID, store.RemoveListItems(ctx, list.ID, []models.InstrumentID{id})
		}, (*app).renderLists)
	}
	a.confirm("Delete list",
		fmt.Sprintf("Delete list %s with %d items?", styleYellow.Bold(true).Render(list.Name), len(list.Items)),
		"Delete", func(a *app) tea.Cmd {
			return a.listOperation("Deleting list…", func(ctx context.Context, store listStore) (string, error) {
				return "", store.DeleteList(ctx, list.ID)
			}, (*app).renderLists)
		})
	return nil
}

// refreshSelectedList downloads the selected NSE index list again.
func (a *app) refreshSelectedList() tea.Cmd {
	e := a.explorer
	list, ok := e.selectedList()
	if !ok {
		return nil
	}
	if list.AutoGenerated && list.CreatedBy == models.AdminUser && list.Name == foListMetadata.ListName {
		return a.downloadIndices([]nse.NSEIndexMetadata{foListMetadata}, -1)
	}
	if !list.IsIndex || !list.AutoGenerated || list.URL == "" || a.deps.indices == nil {
		e.status = statusWarn("Only NSE index lists can be refreshed. Press ") + styleKey.Render("M") + statusWarn(" to load them.")
		return nil
	}
	return a.downloadIndices([]nse.NSEIndexMetadata{{ListName: list.Name, IndexName: list.Name, IndexPageURL: list.URL}}, -1)
}

// addSelectedToList adds the instrument selected in the dashboard or chain
// to a list chosen from a picker.
func (a *app) addSelectedToList() tea.Cmd {
	e := a.explorer
	if e.lists == nil {
		e.status = statusWarn("Lists are unavailable for this catalog.")
		return nil
	}
	instrument, ok := e.selectedInstrument()
	if !ok {
		e.status = statusWarn("Select an instrument to add it to a list.")
		return nil
	}
	return a.listOperation("Loading lists…", nil, func(a *app, _ string) tea.Cmd {
		a.openListPicker(instrument)
		return nil
	})
}

func (a *app) openListPicker(instrument models.Instrument) {
	picker := &menu{title: "Add " + string(instrument.ID) + " to", width: 48}
	picker.items = append(picker.items, menuItem{label: styleAccent.Render("+ New list…"), run: func(a *app) tea.Cmd {
		a.overlay = newInputOverlay("New list", "Name", "", "Save", func(a *app, name string) tea.Cmd {
			return a.listOperation("Creating list…", func(ctx context.Context, store listStore) (string, error) {
				list, err := store.CreateList(ctx, models.List{
					CreatedBy: models.AdminUser,
					Name:      name,
					Items:     []models.ListItem{{InstrumentID: instrument.ID}},
				})
				return list.ID, err
			}, func(a *app, _ string) tea.Cmd {
				a.explorer.status = statusOK(fmt.Sprintf("Created %s with %s.", name, instrument.ID))
				return nil
			})
		})
		return nil
	}})
	for _, list := range a.explorer.listData {
		if list.AutoGenerated {
			continue
		}
		picker.items = append(picker.items, menuItem{
			label: styleCyan.Render(list.Name) + styleFaint.Render(fmt.Sprintf("  (%d)", len(list.Items))),
			run: func(a *app) tea.Cmd {
				return a.listOperation(fmt.Sprintf("Adding to %s…", list.Name), func(ctx context.Context, store listStore) (string, error) {
					return list.ID, store.AddListItems(ctx, list.ID, []models.InstrumentID{instrument.ID})
				}, func(a *app, _ string) tea.Cmd {
					a.explorer.status = statusOK(fmt.Sprintf("Added %s to %s.", instrument.ID, list.Name))
					return nil
				})
			},
		})
	}
	a.overlay = picker
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

// openMasterList shows the NSE indices that can be downloaded as lists.
func (a *app) openMasterList() tea.Cmd {
	if a.deps.indices == nil {
		a.explorer.status = statusWarn("The NSE index source is unavailable.")
		return nil
	}
	if a.explorer.masterIndices != nil {
		a.showMasterList(0)
		return nil
	}
	return a.fetchMasterIndices()
}

func (a *app) fetchMasterIndices() tea.Cmd {
	indices := a.deps.indices
	return tea.Batch(a.busy("Fetching the NSE index list from niftyindices.com…"), a.task(func(ctx context.Context) tea.Msg {
		metadata, err := indices.ListIndices(ctx)
		return masterIndicesMsg{indices: metadata, err: err}
	}))
}

// masterOverlay lists the NSE indices with whether each is downloaded.
type masterOverlay struct {
	grid grid
	rows []nse.NSEIndexMetadata
}

func (a *app) showMasterList(cursor int) {
	downloaded := make(map[string]models.List)
	for _, list := range a.explorer.listData {
		if list.AutoGenerated && list.CreatedBy == models.AdminUser {
			downloaded[list.Name] = list
		}
	}
	o := &masterOverlay{
		rows: append([]nse.NSEIndexMetadata{foListMetadata}, a.explorer.masterIndices...),
		grid: grid{columns: []column{
			{title: "INDEX", width: 20, flex: 2}, {title: "LIST", width: 14, flex: 1}, {title: "STATUS", width: 26},
		}},
	}
	rows := make([]row, 0, len(o.rows))
	for _, metadata := range o.rows {
		status := textCell("not downloaded", styleFaint)
		if list, ok := downloaded[metadata.ListName]; ok {
			status = textCell(fmt.Sprintf("✔ %d items · %s", len(list.Items), list.UpdatedAt.Local().Format("02 Jan 15:04")), styleGreen)
		}
		rows = append(rows, row{ref: metadata, cells: []cell{
			textCell(metadata.IndexName, styleText), textCell(metadata.ListName, styleCyan), status,
		}})
	}
	o.grid.setRows(rows)
	o.grid.cursor = min(max(cursor, 0), len(rows)-1)
	o.grid.centerNext = true
	a.overlay = o
	a.explorer.status = statusInfo("Downloaded indices are saved as lists owned by " + models.AdminUser + ".")
}

func (o *masterOverlay) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.closeOverlay()
		return nil
	case "enter":
		if metadata, ok := o.grid.selectedRef().(nse.NSEIndexMetadata); ok {
			return a.downloadIndices([]nse.NSEIndexMetadata{metadata}, o.grid.cursor)
		}
		return nil
	}
	if o.grid.update(key) {
		return nil
	}
	switch strings.ToLower(key) {
	case "a":
		return a.downloadIndices(o.rows, o.grid.cursor)
	case "r":
		a.closeOverlay()
		return a.fetchMasterIndices()
	case "q":
		a.closeOverlay()
	}
	return nil
}

func (o *masterOverlay) view(_ *app, w, h int) string {
	width := min(96, w-2)
	height := min(len(o.rows)+3, max(h-10, 5))
	body := o.grid.view(width-4, height, true) + "\n\n" +
		help("Enter", "download/refresh", "A", "download all", "R", "reload index list", "Q", "close")
	return modal("NSE master list", styleMuted.Render(fmt.Sprintf("%d indices", len(o.rows)-1)), body, width)
}

// downloadIndices downloads each index and saves it as an ADMIN list,
// continuing past failures. With reopen at 0 or more, the master list shows
// again afterwards with that row selected.
func (a *app) downloadIndices(indices []nse.NSEIndexMetadata, reopen int) tea.Cmd {
	if len(indices) == 0 {
		return nil
	}
	source, catalog := a.deps.indices, a.explorer.catalog
	message := fmt.Sprintf("Downloading %s…", indices[0].IndexName)
	if len(indices) > 1 {
		message = fmt.Sprintf("Downloading %d NSE indices…", len(indices))
	}
	return a.listOperation(message, func(ctx context.Context, store listStore) (string, error) {
		var selectID string
		var errs []error
		for i, metadata := range indices {
			if len(indices) > 1 {
				a.progress(fmt.Sprintf("Downloading %s (%d/%d)…", metadata.IndexName, i+1, len(indices)))
			}
			var list models.List
			var err error
			switch {
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
				return selectID, ctx.Err()
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", metadata.IndexName, err))
				continue
			}
			selectID = list.ID
		}
		if len(errs) > 0 {
			return selectID, fmt.Errorf("%d of %d downloads failed: %w", len(errs), len(indices), errors.Join(errs...))
		}
		return selectID, nil
	}, func(a *app, selectID string) tea.Cmd {
		cmd := a.renderLists(selectID)
		a.explorer.status = statusOK(fmt.Sprintf("Saved %d NSE index list(s).", len(indices)))
		if reopen >= 0 {
			a.showMasterList(reopen)
		}
		return cmd
	})
}

func listSource(list models.List) string {
	switch {
	case list.IsIndex && list.AutoGenerated:
		return "NSE index"
	case list.AutoGenerated:
		return "generated"
	}
	return "manual"
}

// listsView lays the lists beside the selected list's items. top is the
// screen line the panels start on.
func (a *app) listsView(w, h, top int) string {
	e := a.explorer
	widths := splitWidth(w, 3, 2)
	e.listsGrid.x, e.listsGrid.y = 1, top+2
	e.itemsGrid.x, e.itemsGrid.y = widths[0]+1, top+2
	lists := panel("Lists", styleMuted.Render(fmt.Sprintf("%d", len(e.listData))), e.listsGrid.view(widths[0]-2, h-2, e.focus == 0), widths[0], h, e.focus == 0)
	title := "Items"
	if list, ok := e.selectedList(); ok {
		title = list.Name
	}
	items := panel(title, styleMuted.Render(pluralize("item", len(e.itemsGrid.rows))), e.itemsGrid.view(widths[1]-2, h-2, e.focus == 1), widths[1], h, e.focus == 1)
	return joinColumns(lists, items)
}
