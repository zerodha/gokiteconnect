package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/models"
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

func (a *app) handleLists(msg tea.Msg) (tea.Cmd, bool) {
	e := a.explorer
	switch msg := msg.(type) {
	case listsLoadedMsg:
		if msg.loadErr != nil {
			a.showError(errors.Join(msg.opErr, msg.loadErr))
			return nil, true
		}
		e.listData = msg.lists
		if e.watchUser != "" {
			e.setWatchlists(ownLists(msg.lists, e.watchUser), e.currentWatchlistID())
		}
		a.closeOverlay()
		var cmd tea.Cmd
		if msg.done != nil {
			cmd = msg.done(a, msg.selectID)
		}
		if msg.opErr != nil {
			a.showError(msg.opErr)
		}
		return cmd, true
	}
	if cmd, handled := a.handleWatchlists(msg); handled {
		return cmd, true
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
	owned := e.ownedLists()
	rows := make([]row, 0, len(owned))
	cursor := 0
	for index, list := range owned {
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
	e.status = statusInfo(fmt.Sprintf("%d %s of %s", len(owned), pluralize("list", len(owned)), e.watchUser))
	return a.refreshQuotes()
}

// ownedLists are the watch user's lists, the only ones the lists view
// shows; ADMIN's lists are managed on Admin.
func (e *explorerScreen) ownedLists() []models.List {
	if e.watchUser == "" {
		return nil
	}
	return ownLists(e.listData, e.watchUser)
}

// renderListItems shows the selected list's items, from the top when reset
// is set.
func (e *explorerScreen) renderListItems(reset bool) {
	list, ok := e.selectedList()
	if !ok {
		e.itemsGrid.empty = styleMuted.Render("No lists yet · press N to make one, empty or from an ADMIN list")
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
		return a.newWatchlist(), true
	case "e":
		a.renameSelectedList()
	case "a":
		a.addInstrumentByID()
	case "d":
		return a.deleteSelected(), true
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

// openListPicker offers the active user's watchlists that can be edited:
// not generated and not smart.
func (a *app) openListPicker(instrument models.Instrument) {
	owner := fallback(a.activeUser(), models.AdminUser)
	picker := &menu{title: "Add " + string(instrument.ID) + " to", width: 48}
	picker.items = append(picker.items, menuItem{label: styleAccent.Render("+ New watchlist…"), run: func(a *app) tea.Cmd {
		a.overlay = newInputOverlay("New watchlist", "Name", "", "Save", func(a *app, name string) tea.Cmd {
			return a.listOperation("Creating list…", func(ctx context.Context, store listStore) (string, error) {
				list, err := store.CreateList(ctx, models.List{
					CreatedBy: owner,
					Name:      name,
					Items:     []models.ListItem{{InstrumentID: instrument.ID}},
				})
				return list.ID, err
			}, func(a *app, _ string) tea.Cmd {
				a.explorer.status = statusOK(fmt.Sprintf("Created %s with %s.", name, instrument.ID))
				return a.loadWatchlists("")
			})
		})
		return nil
	}})
	editable := slices.DeleteFunc(ownLists(a.explorer.listData, owner), func(list models.List) bool {
		return list.AutoGenerated || list.IsSmart()
	})
	for _, list := range editable {
		picker.items = append(picker.items, menuItem{
			label: styleCyan.Render(list.Name) + styleFaint.Render(fmt.Sprintf("  (%d)", len(list.Items))),
			run: func(a *app) tea.Cmd {
				return a.listOperation(fmt.Sprintf("Adding to %s…", list.Name), func(ctx context.Context, store listStore) (string, error) {
					return list.ID, store.AddListItems(ctx, list.ID, []models.InstrumentID{instrument.ID})
				}, func(a *app, _ string) tea.Cmd {
					a.explorer.status = statusOK(fmt.Sprintf("Added %s to %s.", instrument.ID, list.Name))
					return a.loadWatchlists("")
				})
			},
		})
	}
	a.overlay = picker
}

func listSource(list models.List) string {
	switch {
	case list.IsSmart():
		return "smart"
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
