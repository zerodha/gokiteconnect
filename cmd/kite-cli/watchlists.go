package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	marketdata "github.com/devshoe/gokiteconnect/market-data-service"
	"github.com/devshoe/gokiteconnect/models"
)

// Watchlists are the active user's lists. The Watchlists tab shows one at a
// time in the instrument table; [ and ] switch between them. A new one is
// empty, or made from an ADMIN list: smart, following that list, or a
// clone of its items as they are now. A user with none gets a smart
// DEFAULT SYNC.

type (
	// watchlistsMsg ends loading the active user's watchlists. lists is
	// every list, so smart watchlists can name their parents.
	watchlistsMsg struct {
		user     string
		lists    []models.List
		created  bool
		selectID string
		err      error
	}
	// watchlistShownMsg carries a watchlist's instruments from the catalog.
	watchlistShownMsg struct {
		generation  int
		listID      string
		instruments []models.Instrument
		selectID    models.InstrumentID
		err         error
	}
)

// watchlistMode is how a new watchlist is made from an ADMIN list.
type watchlistMode int

const (
	watchlistSmart watchlistMode = iota
	watchlistClone
)

// loadWatchlists reads the active user's watchlists, making the default one
// when they have none, and shows selectID's, or the current one.
func (a *app) loadWatchlists(selectID string) tea.Cmd {
	e := a.explorer
	if e.lists == nil || e.catalog == nil {
		return nil
	}
	user := a.activeUser()
	if user == "" {
		e.watchUser, e.watchlists = "", nil
		return nil
	}
	store, catalog := e.lists, e.catalog
	return a.task(func(ctx context.Context) tea.Msg {
		msg := watchlistsMsg{user: user, selectID: selectID}
		if msg.lists, msg.err = store.GetLists(ctx, ""); msg.err != nil {
			return msg
		}
		if len(ownLists(msg.lists, user)) > 0 {
			return msg
		}
		parent, err := storedDefaultSyncList(ctx, store)
		if errors.Is(err, models.ErrNotFound) {
			parent, err = marketdata.RebuildDefaultSyncList(ctx, catalog, store)
		}
		if err != nil {
			msg.err = fmt.Errorf("make the default watchlist: %w", err)
			return msg
		}
		created, err := store.CreateList(ctx, models.List{CreatedBy: user, Name: parent.Name, ParentID: parent.ID})
		if err != nil {
			msg.err = fmt.Errorf("make the default watchlist: %w", err)
			return msg
		}
		msg.created, msg.selectID = true, created.ID
		msg.lists, msg.err = store.GetLists(ctx, "")
		return msg
	})
}

// ownLists are the lists user created, in list-number order.
func ownLists(lists []models.List, user string) []models.List {
	var own []models.List
	for _, list := range lists {
		if list.CreatedBy == user {
			own = append(own, list)
		}
	}
	return own
}

func (a *app) handleWatchlists(msg tea.Msg) (tea.Cmd, bool) {
	e := a.explorer
	switch msg := msg.(type) {
	case watchlistsMsg:
		if msg.err != nil {
			e.status = statusErr("Watchlists unavailable:", msg.err.Error())
			return nil, true
		}
		current := e.currentWatchlistID()
		first := e.watchUser != msg.user
		e.listData = msg.lists
		e.watchUser = msg.user
		e.setWatchlists(ownLists(msg.lists, msg.user), fallback(msg.selectID, current))
		if msg.created {
			e.watchNote = statusOK(fmt.Sprintf("Made your first watchlist: %s, following %s's.", e.watchlists[e.watchIndex].Name, models.AdminUser))
		}
		// The watchlist replaces the dashboard when it shows one, or on the
		// first load, unless a search is under way.
		searching := e.searchFocused || e.search.Value() != ""
		if e.mode == viewDashboard && !searching && (e.watching || first) {
			var selectID models.InstrumentID
			if instrument, ok := e.results.selectedRef().(models.Instrument); ok && !first {
				selectID = instrument.ID
			}
			return a.showWatchlist(e.watchIndex, selectID), true
		}
		return nil, true
	case watchlistShownMsg:
		if msg.generation != e.watchGen {
			return nil, true
		}
		if msg.err != nil {
			a.showError(msg.err)
			return nil, true
		}
		list, ok := e.currentWatchlist()
		if !ok || list.ID != msg.listID {
			return nil, true
		}
		e.chain = nil
		e.mode = viewDashboard
		cmd := a.setResults(msg.instruments, false)
		e.watching = true
		e.renderResults()
		if msg.selectID != "" {
			e.selectResult(msg.selectID)
		}
		if !e.searchFocused && e.search.Value() != "" {
			e.search.SetValue("")
		}
		e.status = statusInfo(fmt.Sprintf("%s · %s", list.Name, countOf("instrument", len(msg.instruments))))
		if e.watchNote != "" {
			e.status, e.watchNote = e.watchNote, ""
		}
		return cmd, true
	case watchlistChangedMsg:
		return a.watchlistChanged(msg), true
	}
	return nil, false
}

// setWatchlists replaces the watchlists, selecting selectID's, or the first.
func (e *explorerScreen) setWatchlists(lists []models.List, selectID string) {
	e.watchlists = lists
	e.watchIndex = 0
	for index, list := range lists {
		if list.ID == selectID {
			e.watchIndex = index
		}
	}
}

func (e *explorerScreen) currentWatchlist() (models.List, bool) {
	if e.watchIndex < 0 || e.watchIndex >= len(e.watchlists) {
		return models.List{}, false
	}
	return e.watchlists[e.watchIndex], true
}

func (e *explorerScreen) currentWatchlistID() string {
	list, _ := e.currentWatchlist()
	return list.ID
}

// listByID finds a list among every list loaded.
func (e *explorerScreen) listByID(id string) (models.List, bool) {
	for _, list := range e.listData {
		if list.ID == id {
			return list, true
		}
	}
	return models.List{}, false
}

// showWatchlist shows watchlist index in the instrument table, selecting
// selectID when it is there.
func (a *app) showWatchlist(index int, selectID models.InstrumentID) tea.Cmd {
	e := a.explorer
	if index < 0 || index >= len(e.watchlists) || e.catalog == nil {
		return nil
	}
	e.watchIndex = index
	e.watchGen++
	e.searchGen++
	list, generation, catalog := e.watchlists[index], e.watchGen, e.catalog
	return a.task(func(ctx context.Context) tea.Msg {
		msg := watchlistShownMsg{generation: generation, listID: list.ID, selectID: selectID}
		msg.instruments, msg.err = watchlistInstruments(ctx, catalog, list)
		return msg
	})
}

// watchlistInstruments looks up a list's instruments, in list order. One
// the catalog no longer has shows by its ID alone.
func watchlistInstruments(ctx context.Context, catalog catalog, list models.List) ([]models.Instrument, error) {
	instruments := make([]models.Instrument, 0, len(list.Items))
	for _, id := range list.InstrumentIDs() {
		instrument, err := catalog.Get(ctx, id)
		if errors.Is(err, models.ErrNotFound) {
			exchange, symbol, _ := strings.Cut(string(id), ":")
			instruments = append(instruments, models.Instrument{ID: id, Exchange: exchange, TradingSymbol: symbol, DisplayName: symbol + " · not in the catalog"})
			continue
		}
		if err != nil {
			return nil, err
		}
		instruments = append(instruments, instrument)
	}
	return instruments, nil
}

// cycleWatchlist shows the watchlist step away from the current one, or the
// current one when the table shows something else.
func (a *app) cycleWatchlist(step int) tea.Cmd {
	e := a.explorer
	if len(e.watchlists) == 0 {
		e.status = statusWarn("No watchlists yet · press ") + styleKey.Render("N") + statusWarn(" to make one")
		return nil
	}
	index := e.watchIndex
	if e.watching {
		index = (index + step + len(e.watchlists)) % len(e.watchlists)
	}
	e.blurSearch()
	return a.showWatchlist(index, "")
}

// watchlistInfo describes the shown watchlist for its panel.
func (e *explorerScreen) watchlistInfo() string {
	list, ok := e.currentWatchlist()
	if !ok {
		return ""
	}
	info := fmt.Sprintf("‹ %d/%d › · %s", e.watchIndex+1, len(e.watchlists), countOf("row", len(e.resultItems)))
	if list.IsSmart() {
		parent := list.ParentID
		if found, ok := e.listByID(list.ParentID); ok {
			parent = found.Name
		}
		info = "smart · follows " + parent + " · " + info
	}
	return info
}

// newWatchlist offers an empty watchlist or one made from an ADMIN list.
func (a *app) newWatchlist() tea.Cmd {
	e := a.explorer
	if e.lists == nil {
		e.status = statusWarn("Lists are unavailable for this catalog.")
		return nil
	}
	user := a.activeUser()
	if user == "" {
		e.status = statusWarn("Add a user on the Users tab to keep watchlists.")
		return nil
	}
	picker := &menu{title: "New watchlist for " + user, width: 56, info: styleMuted.Render("start empty, or from an ADMIN list")}
	picker.items = append(picker.items, menuItem{label: styleAccent.Render("+ Empty watchlist…"), run: func(a *app) tea.Cmd {
		a.nameWatchlist(user, "", models.List{}, watchlistClone)
		return nil
	}})
	for _, list := range e.listData {
		if list.CreatedBy != models.AdminUser || list.IsSmart() {
			continue
		}
		picker.items = append(picker.items, menuItem{
			label: styleCyan.Render(list.Name) + styleFaint.Render(fmt.Sprintf("  (%d) · %s", len(list.Items), listSource(list))),
			run: func(a *app) tea.Cmd {
				a.chooseWatchlistMode(user, list)
				return nil
			},
		})
	}
	a.overlay = picker
	return nil
}

// chooseWatchlistMode asks whether a watchlist from parent follows it or
// copies it.
func (a *app) chooseWatchlistMode(user string, parent models.List) {
	a.overlay = &menu{
		title: "From " + parent.Name, width: 64, keyFold: "sc",
		items: []menuItem{
			{key: "s", label: styleKey.Render("S") + "  Smart · follows " + parent.Name + ", changing when it is refreshed", run: func(a *app) tea.Cmd {
				a.nameWatchlist(user, parent.Name, parent, watchlistSmart)
				return nil
			}},
			{key: "c", label: styleKey.Render("C") + "  Clone · copies its " + countOf("instrument", len(parent.Items)) + " now, to edit freely", run: func(a *app) tea.Cmd {
				a.nameWatchlist(user, parent.Name, parent, watchlistClone)
				return nil
			}},
		},
	}
}

// nameWatchlist asks for the new watchlist's name, then makes it from
// parent, or empty when parent has no ID.
func (a *app) nameWatchlist(user, name string, parent models.List, mode watchlistMode) {
	a.overlay = newInputOverlay("New watchlist", "Name", name, "Save", func(a *app, name string) tea.Cmd {
		return a.watchlistOperation("Making the watchlist…", func(ctx context.Context, store listStore) (string, error) {
			list := models.List{CreatedBy: user, Name: name}
			switch {
			case parent.ID == "":
			case mode == watchlistSmart:
				list.ParentID = parent.ID
			default:
				// Copy the parent as stored now, not as last loaded.
				lists, err := store.GetLists(ctx, models.AdminUser)
				if err != nil {
					return "", err
				}
				index := slices.IndexFunc(lists, func(l models.List) bool { return l.ID == parent.ID })
				if index < 0 {
					return "", fmt.Errorf("%w: list %s", models.ErrNotFound, parent.Name)
				}
				list.Items = lists[index].Items
			}
			created, err := store.CreateList(ctx, list)
			return created.ID, err
		})
	})
}

// watchlistOperation runs operation, then reloads the watchlists and shows
// the one it returns.
func (a *app) watchlistOperation(message string, operation func(context.Context, listStore) (string, error)) tea.Cmd {
	store := a.explorer.lists
	if store == nil {
		return nil
	}
	return tea.Batch(a.busy(message), a.task(func(ctx context.Context) tea.Msg {
		id, err := operation(ctx, store)
		return watchlistChangedMsg{selectID: id, err: err}
	}))
}

type watchlistChangedMsg struct {
	selectID string
	err      error
}

func (a *app) watchlistChanged(msg watchlistChangedMsg) tea.Cmd {
	a.closeOverlay()
	if msg.err != nil {
		a.showError(msg.err)
	}
	a.explorer.watching = true
	return a.loadWatchlists(msg.selectID)
}

// removeFromWatchlist removes the selected instrument from the shown
// watchlist.
func (a *app) removeFromWatchlist() tea.Cmd {
	e := a.explorer
	list, ok := e.currentWatchlist()
	instrument, selected := e.selectedInstrument()
	if !e.watching || !ok || !selected {
		return nil
	}
	if list.IsSmart() {
		e.status = statusWarn(list.Name+" is smart: it follows its parent. Make a clone with ") + styleKey.Render("N") + statusWarn(" to edit it.")
		return nil
	}
	return a.watchlistOperation(fmt.Sprintf("Removing %s…", instrument.ID), func(ctx context.Context, store listStore) (string, error) {
		return list.ID, store.RemoveListItems(ctx, list.ID, []models.InstrumentID{instrument.ID})
	})
}

// countOf is count and noun, plural unless count is 1.
func countOf(noun string, count int) string {
	return fmt.Sprintf("%d %s", count, pluralize(noun, count))
}
