package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/nse"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	listsPage      = "lists"
	listFormPage   = "list-form"
	listPickerPage = "list-picker"
	masterPage     = "master"
)

const listsHelp = "  [yellow]N[-] new · [yellow]E[-] rename · [yellow]A[-] add instrument · [yellow]D[-] delete · [yellow]R[-] refresh NSE list · [yellow]M[-] NSE master list · [yellow]Tab[-] items · [yellow]Q[-] back"

func (ui *instrumentApp) buildListsView() tview.Primitive {
	ui.listsTable = newInstrumentTable(" Lists ")
	ui.listsTable.SetSelectionChangedFunc(func(int, int) { ui.renderListItems() })
	ui.listsTable.SetSelectedFunc(func(int, int) { ui.focusListItems(true) })

	ui.itemsTable = newInstrumentTable(" Items ")

	return tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(ui.listsTable, 0, 3, true).
		AddItem(ui.itemsTable, 0, 2, false)
}

// captureListKey handles keys that only apply in the lists view.
func (ui *instrumentApp) captureListKey(event *tcell.EventKey) bool {
	switch event.Key() {
	case tcell.KeyTab, tcell.KeyBacktab:
		ui.focusListItems(!ui.listItemsFocused)
		return true
	case tcell.KeyDelete:
		ui.deleteSelected()
		return true
	case tcell.KeyRune:
	default:
		return false
	}
	switch strings.ToLower(string(event.Rune())) {
	case "n":
		ui.openListForm("New list", "", func(name string) {
			ui.listOperation("Creating list…", func(ctx context.Context, store listStore) (string, error) {
				list, err := store.CreateList(ctx, models.List{CreatedBy: models.AdminUser, Name: name})
				return list.ID, err
			}, ui.renderLists)
		})
	case "e":
		ui.renameSelectedList()
	case "a":
		ui.addInstrumentByID()
	case "d":
		ui.deleteSelected()
	case "r":
		ui.refreshSelectedList()
	case "m":
		ui.openMasterList()
	default:
		return false
	}
	return true
}

// openLists loads every list and switches to the lists view.
func (ui *instrumentApp) openLists() {
	if ui.catalog == nil || ui.busy {
		return
	}
	if ui.lists == nil {
		ui.setStatus("  Lists are unavailable for this catalog.")
		return
	}
	if ui.mode == viewDashboard {
		ui.snapshot = ui.dashboardSnapshot()
	}
	ui.searcher.cancelPending()
	ui.listOperation("Loading lists…", nil, ui.renderLists)
}

// listOperation runs operation in the background, reloads every list, and
// then calls done on the UI goroutine with the list ID the operation returned.
// The lists are reloaded even when the operation fails, because a batch
// download can fail part-way through.
func (ui *instrumentApp) listOperation(message string, operation func(context.Context, listStore) (string, error), done func(selectID string)) {
	if ui.lists == nil || ui.busy || !ui.startOperation() {
		return
	}
	ui.showBusy(message)
	store := ui.lists
	go func() {
		defer ui.wg.Done()
		var selectID string
		var operationErr error
		if operation != nil {
			selectID, operationErr = operation(ui.ctx, store)
		}
		lists, loadErr := store.GetLists(ui.ctx, "")
		if ui.ctx.Err() != nil {
			return
		}
		ui.application.QueueUpdateDraw(func() {
			if loadErr != nil {
				ui.showError(errors.Join(operationErr, loadErr))
				return
			}
			ui.listData = lists
			ui.closeOverlay()
			if done != nil {
				done(selectID)
			}
			if operationErr != nil {
				ui.showError(operationErr)
			}
		})
	}()
}

// renderLists shows ui.listData and selects selectID, or keeps the current
// selection when selectID is empty.
func (ui *instrumentApp) renderLists(selectID string) {
	if selectID == "" {
		if list, ok := ui.selectedList(); ok {
			selectID = list.ID
		}
	}
	ui.mode = viewLists
	ui.chainData = nil
	ui.bodyPages.SwitchToPage(listsPage)

	ui.listsTable.Clear()
	ui.listsTable.SetTitle(fmt.Sprintf(" Lists · %d ", len(ui.listData)))
	setHeaders(ui.listsTable, []string{"#", "NAME", "ITEMS", "OWNER", "SOURCE", "UPDATED"})
	selectedRow := 1
	for index, list := range ui.listData {
		row := index + 1
		if list.ID == selectID {
			selectedRow = row
		}
		cells := []struct {
			text  string
			color tcell.Color
		}{
			{strconv.Itoa(list.Number), tcell.ColorLightGray},
			{list.Name, tcell.ColorLightCyan},
			{strconv.Itoa(len(list.Items)), tcell.ColorLightGreen},
			{list.CreatedBy, tcell.ColorLightPink},
			{listSource(list), tcell.ColorLightYellow},
			{list.UpdatedAt.Local().Format("02 Jan 2006 15:04"), tcell.ColorLightGray},
		}
		for column, cell := range cells {
			ui.listsTable.SetCell(row, column, tview.NewTableCell(tview.Escape(cell.text)).
				SetTextColor(cell.color).
				SetReference(list))
		}
	}
	if len(ui.listData) > 0 {
		ui.listsTable.Select(selectedRow, 0)
	}
	ui.renderListItems()
	ui.updateModeControls()
	ui.updateHeaderWithCount(len(ui.listData))
	ui.setStatus(listsHelp)
	ui.focusListItems(ui.listItemsFocused)
}

func (ui *instrumentApp) renderListItems() {
	ui.itemsTable.Clear()
	list, ok := ui.selectedList()
	if !ok {
		ui.itemsTable.SetTitle(" Items ")
		ui.itemsTable.SetCell(1, 0, tview.NewTableCell("No lists yet. Press N to create one or M to load NSE indices.").
			SetTextColor(tcell.ColorLightGray).
			SetSelectable(false))
		return
	}
	ui.itemsTable.SetTitle(fmt.Sprintf(" %s · %d items ", tview.Escape(list.Name), len(list.Items)))
	setHeaders(ui.itemsTable, []string{"INSTRUMENT", "LTP", "CHG%", "ADDED"})
	for index, item := range list.Items {
		ui.itemsTable.SetCell(index+1, 0, tview.NewTableCell(tview.Escape(string(item.InstrumentID))).
			SetTextColor(tcell.ColorLightCyan).
			SetReference(item.InstrumentID))
		ui.setPriceCells(ui.itemsTable, index+1, itemsLTPColumn, models.Instrument{ID: item.InstrumentID})
		ui.itemsTable.SetCell(index+1, itemsLTPColumn+2, tview.NewTableCell(item.CreatedAt.Local().Format("02 Jan 2006 15:04")).
			SetTextColor(tcell.ColorLightGray).
			SetReference(item.InstrumentID))
	}
	if len(list.Items) > 0 {
		ui.itemsTable.Select(1, 0)
	}
	ui.itemsTable.ScrollToBeginning()
	ui.refreshQuotes()
}

func (ui *instrumentApp) focusListItems(items bool) {
	if items {
		if _, ok := ui.selectedListItem(); !ok {
			items = false
		}
	}
	ui.listItemsFocused = items
	ui.listsTable.SetBorderColor(tcell.ColorLightCyan)
	ui.itemsTable.SetBorderColor(tcell.ColorLightCyan)
	if items {
		ui.itemsTable.SetBorderColor(tcell.ColorLightYellow)
	} else {
		ui.listsTable.SetBorderColor(tcell.ColorLightYellow)
	}
	ui.application.SetFocus(ui.activeTable())
}

func (ui *instrumentApp) renameSelectedList() {
	list, ok := ui.selectedList()
	if !ok {
		return
	}
	ui.openListForm("Rename list", list.Name, func(name string) {
		ui.listOperation("Renaming list…", func(ctx context.Context, store listStore) (string, error) {
			return list.ID, store.RenameList(ctx, list.ID, name)
		}, ui.renderLists)
	})
}

// addInstrumentByID adds an instrument typed by ID to the selected list after
// checking that the catalog knows it.
func (ui *instrumentApp) addInstrumentByID() {
	list, ok := ui.selectedList()
	if !ok {
		return
	}
	catalog := ui.catalog
	ui.openInputForm(" Add to "+tview.Escape(list.Name)+" ", "Instrument ID", "", "Add", func(text string) {
		id := models.InstrumentID(strings.ToUpper(text))
		ui.listOperation(fmt.Sprintf("Adding %s…", id), func(ctx context.Context, store listStore) (string, error) {
			if _, err := catalog.Get(ctx, id); err != nil {
				return list.ID, fmt.Errorf("%s: %w", id, err)
			}
			return list.ID, store.AddListItems(ctx, list.ID, []models.InstrumentID{id})
		}, ui.renderLists)
	})
}

// deleteSelected removes the selected item when the items table has focus,
// otherwise it deletes the selected list after confirmation.
func (ui *instrumentApp) deleteSelected() {
	list, ok := ui.selectedList()
	if !ok {
		return
	}
	if ui.listItemsFocused {
		id, ok := ui.selectedListItem()
		if !ok {
			return
		}
		ui.listOperation(fmt.Sprintf("Removing %s…", id), func(ctx context.Context, store listStore) (string, error) {
			return list.ID, store.RemoveListItems(ctx, list.ID, []models.InstrumentID{id})
		}, ui.renderLists)
		return
	}
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Delete list [yellow::b]%s[-] with %d items?", tview.Escape(list.Name), len(list.Items))).
		AddButtons([]string{"Delete", "Cancel"}).
		SetDoneFunc(func(index int, _ string) {
			ui.closeOverlay()
			if index != 0 {
				return
			}
			ui.listOperation("Deleting list…", func(ctx context.Context, store listStore) (string, error) {
				return "", store.DeleteList(ctx, list.ID)
			}, ui.renderLists)
		})
	ui.showOverlay(modalPage, modal, modal)
}

// refreshSelectedList downloads the selected NSE index list again.
func (ui *instrumentApp) refreshSelectedList() {
	list, ok := ui.selectedList()
	if !ok {
		return
	}
	if list.AutoGenerated && list.CreatedBy == models.AdminUser && list.Name == foListMetadata.ListName {
		ui.downloadIndices([]nse.NSEIndexMetadata{foListMetadata}, nil)
		return
	}
	if !list.IsIndex || !list.AutoGenerated || list.URL == "" || ui.indices == nil {
		ui.setStatus("  Only NSE index lists can be refreshed. Press [yellow]M[-] to load them.")
		return
	}
	ui.downloadIndices([]nse.NSEIndexMetadata{{ListName: list.Name, IndexName: list.Name, IndexPageURL: list.URL}}, nil)
}

// addSelectedToList adds the instrument selected in the dashboard or chain to
// a list chosen from a picker.
func (ui *instrumentApp) addSelectedToList() {
	if ui.lists == nil || ui.busy || ui.mode == viewLists {
		return
	}
	instrument, ok := ui.selectedInstrument()
	if !ok {
		ui.setStatus("  Select an instrument to add it to a list.")
		return
	}
	ui.listOperation("Loading lists…", nil, func(string) {
		ui.openListPicker(instrument)
	})
}

func (ui *instrumentApp) openListPicker(instrument models.Instrument) {
	picker := tview.NewList().ShowSecondaryText(false)
	picker.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).
		SetTitle(" Add " + tview.Escape(string(instrument.ID)) + " to ").SetTitleColor(tcell.ColorLightYellow)

	add := func(list models.List) {
		ui.closeOverlay()
		ui.listOperation(fmt.Sprintf("Adding to %s…", list.Name), func(ctx context.Context, store listStore) (string, error) {
			return list.ID, store.AddListItems(ctx, list.ID, []models.InstrumentID{instrument.ID})
		}, func(string) {
			ui.setStatus(fmt.Sprintf("  [green::b]✔[-] Added %s to %s.", tview.Escape(string(instrument.ID)), tview.Escape(list.Name)))
		})
	}
	picker.AddItem("+ New list…", "", 0, func() {
		ui.openListForm("New list", "", func(name string) {
			ui.listOperation("Creating list…", func(ctx context.Context, store listStore) (string, error) {
				list, err := store.CreateList(ctx, models.List{
					CreatedBy: models.AdminUser,
					Name:      name,
					Items:     []models.ListItem{{InstrumentID: instrument.ID}},
				})
				return list.ID, err
			}, func(string) {
				ui.setStatus(fmt.Sprintf("  [green::b]✔[-] Created %s with %s.", tview.Escape(name), tview.Escape(string(instrument.ID))))
			})
		})
	})
	shown := 1
	for _, list := range ui.listData {
		if list.AutoGenerated {
			continue
		}
		list := list
		picker.AddItem(fmt.Sprintf("%s  (%d)", tview.Escape(list.Name), len(list.Items)), "", 0, func() { add(list) })
		shown++
	}
	picker.SetDoneFunc(ui.closeOverlay)
	ui.showOverlay(listPickerPage, centered(48, minInt(shown+2, 18), picker), picker)
}

// foListMetadata is the master-list entry for the NSE and BSE underlyings that
// have futures or options. It is built from the catalog rather than niftyindices.
var foListMetadata = nse.NSEIndexMetadata{IndexName: "F&O stocks and indices (from catalog)", ListName: "FO_LIST"}

// buildFOList lists the NSE and BSE stocks and indices with active futures or
// options, which on BSE are SENSEX and BANKEX.
func buildFOList(ctx context.Context, catalog catalog) (models.List, error) {
	if catalog == nil {
		return models.List{}, errors.New("the instrument catalog is not loaded")
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
func (ui *instrumentApp) openMasterList() {
	if ui.indices == nil {
		ui.setStatus("  The NSE index source is unavailable.")
		return
	}
	if ui.masterIndices != nil {
		ui.showMasterList(0)
		return
	}
	ui.fetchMasterIndices()
}

func (ui *instrumentApp) fetchMasterIndices() {
	if ui.busy || !ui.startOperation() {
		return
	}
	ui.showBusy("Fetching the NSE index list from niftyindices.com…")
	indices := ui.indices
	go func() {
		defer ui.wg.Done()
		metadata, err := indices.ListIndices(ui.ctx)
		if ui.ctx.Err() != nil {
			return
		}
		ui.application.QueueUpdateDraw(func() {
			if err != nil {
				ui.showError(fmt.Errorf("fetch NSE indices: %w", err))
				return
			}
			ui.masterIndices = metadata
			ui.closeOverlay()
			ui.showMasterList(0)
		})
	}()
}

func (ui *instrumentApp) showMasterList(selectedRow int) {
	downloaded := make(map[string]models.List)
	for _, list := range ui.listData {
		if list.AutoGenerated && list.CreatedBy == models.AdminUser {
			downloaded[list.Name] = list
		}
	}

	rows := append([]nse.NSEIndexMetadata{foListMetadata}, ui.masterIndices...)
	table := newInstrumentTable(fmt.Sprintf(" NSE master list · %d indices ", len(ui.masterIndices)))
	table.SetBorderColor(tcell.ColorLightYellow)
	setHeaders(table, []string{"INDEX", "LIST", "STATUS"})
	for index, metadata := range rows {
		status, color := "not downloaded", tcell.ColorDarkGray
		if list, ok := downloaded[metadata.ListName]; ok {
			status = fmt.Sprintf("✔ %d items · %s", len(list.Items), list.UpdatedAt.Local().Format("02 Jan 15:04"))
			color = tcell.ColorLightGreen
		}
		values := []string{metadata.IndexName, metadata.ListName, status}
		colors := []tcell.Color{tcell.ColorWhite, tcell.ColorLightCyan, color}
		for column, value := range values {
			table.SetCell(index+1, column, tview.NewTableCell(tview.Escape(value)).
				SetTextColor(colors[column]).
				SetReference(metadata))
		}
	}
	table.Select(max(1, min(selectedRow, len(rows))), 0)
	table.SetSelectedFunc(func(row, _ int) {
		if metadata, ok := table.GetCell(row, 0).GetReference().(nse.NSEIndexMetadata); ok {
			ui.downloadIndices([]nse.NSEIndexMetadata{metadata}, func() { ui.showMasterList(row) })
		}
	})
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ui.closeOverlay()
			return nil
		}
		if event.Key() != tcell.KeyRune {
			return event
		}
		row, _ := table.GetSelection()
		switch strings.ToLower(string(event.Rune())) {
		case "a":
			ui.downloadIndices(rows, func() { ui.showMasterList(row) })
		case "r":
			ui.closeOverlay()
			ui.fetchMasterIndices()
		case "q":
			ui.closeOverlay()
		default:
			return event
		}
		return nil
	})

	help := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).
		SetText("[yellow]Enter[-] download/refresh · [yellow]A[-] download all · [yellow]R[-] reload index list · [yellow]Q[-] close")
	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(table, 0, 1, true).
		AddItem(help, 1, 0, false)
	ui.showOverlay(masterPage, centered(96, minInt(len(rows)+5, 30), view), table)
	ui.setStatus("  Downloaded indices are saved as lists owned by " + models.AdminUser + ".")
}

// downloadIndices downloads each index and saves it as an ADMIN list,
// continuing past failures. done runs after the lists are reloaded.
func (ui *instrumentApp) downloadIndices(indices []nse.NSEIndexMetadata, done func()) {
	source, catalog := ui.indices, ui.catalog
	if len(indices) == 0 {
		return
	}
	message := fmt.Sprintf("Downloading %s…", indices[0].IndexName)
	if len(indices) > 1 {
		message = fmt.Sprintf("Downloading %d NSE indices…", len(indices))
	}
	ui.listOperation(message, func(ctx context.Context, store listStore) (string, error) {
		var selectID string
		var errs []error
		for i, metadata := range indices {
			if len(indices) > 1 {
				ui.updateBusy(fmt.Sprintf("Downloading %s (%d/%d)…", metadata.IndexName, i+1, len(indices)))
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
	}, func(selectID string) {
		ui.renderLists(selectID)
		ui.setStatus(fmt.Sprintf("  [green::b]✔[-] Saved %d NSE index list(s).", len(indices)))
		if done != nil {
			done()
		}
	})
}

// openListForm asks for a list name.
func (ui *instrumentApp) openListForm(title string, name string, submit func(string)) {
	ui.openInputForm(" "+title+" ", "Name", name, "Save", submit)
}

func (ui *instrumentApp) openInputForm(title string, label string, value string, button string, submit func(string)) {
	form := tview.NewForm().SetItemPadding(1).SetButtonsAlign(tview.AlignCenter)
	form.SetBorder(true).SetBorderColor(tcell.ColorLightCyan).SetTitle(title).SetTitleColor(tcell.ColorLightYellow)
	form.AddInputField(label, value, 32, nil, nil)
	save := func() {
		text := strings.TrimSpace(form.GetFormItemByLabel(label).(*tview.InputField).GetText())
		if text == "" {
			form.SetTitle(" " + label + " is required ").SetTitleColor(tcell.ColorRed).SetBorderColor(tcell.ColorRed)
			return
		}
		ui.closeOverlay()
		submit(text)
	}
	form.AddButton(button, save)
	form.AddButton("Cancel", ui.closeOverlay)
	form.SetCancelFunc(ui.closeOverlay)
	form.GetFormItemByLabel(label).(*tview.InputField).SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			save()
		}
	})
	ui.showOverlay(listFormPage, centered(52, 9, form), form)
}

func (ui *instrumentApp) selectedList() (models.List, bool) {
	row, _ := ui.listsTable.GetSelection()
	if row <= 0 || row >= ui.listsTable.GetRowCount() {
		return models.List{}, false
	}
	list, ok := ui.listsTable.GetCell(row, 0).GetReference().(models.List)
	return list, ok
}

func (ui *instrumentApp) selectedListItem() (models.InstrumentID, bool) {
	row, _ := ui.itemsTable.GetSelection()
	if row <= 0 || row >= ui.itemsTable.GetRowCount() {
		return "", false
	}
	id, ok := ui.itemsTable.GetCell(row, 0).GetReference().(models.InstrumentID)
	return id, ok
}

func listSource(list models.List) string {
	switch {
	case list.IsIndex && list.AutoGenerated:
		return "NSE index"
	case list.AutoGenerated:
		return "generated"
	default:
		return "manual"
	}
}
