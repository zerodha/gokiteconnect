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
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/broker"
)

// basketStore keeps the order baskets: named sets of order parameters,
// each user's own, placed together into their account.
type basketStore interface {
	GetBaskets(context.Context, string) ([]models.Basket, error)
	CreateBasket(context.Context, models.Basket) (models.Basket, error)
	RenameBasket(context.Context, string, string) error
	DeleteBasket(context.Context, string) error
	AddBasketItems(context.Context, string, []models.BasketItem) error
	UpdateBasketItem(context.Context, string, models.BasketItem) error
	RemoveBasketItems(context.Context, string, []string) error
}

// basketMarginer works out what a basket needs as margin, hedges netted,
// without the account's own positions.
type basketMarginer func(ctx context.Context, legs []kite.OrderMarginParam) (kite.BasketMargins, error)

// dataBasketMargins asks the data user's margin calculator, on its stored
// session.
func dataBasketMargins(credentialsPath, dataUserID string) basketMarginer {
	return func(ctx context.Context, legs []kite.OrderMarginParam) (kite.BasketMargins, error) {
		client, _, err := dataClient(ctx, credentialsPath, dataUserID)
		if err != nil {
			return kite.BasketMargins{}, err
		}
		return client.GetBasketMargins(kite.GetBasketParams{OrderParams: legs})
	}
}

// draftPrefix marks the IDs of draft baskets: new baskets kept in memory,
// not stored until S saves them.
const draftPrefix = "draft:"

func isDraft(id string) bool { return strings.HasPrefix(id, draftPrefix) }

// basketItemOf is an order's parameters as a basket keeps them.
func basketItemOf(p kite.OrderParams) models.BasketItem {
	return models.BasketItem{Exchange: p.Exchange, TradingSymbol: p.Tradingsymbol, TransactionType: p.TransactionType,
		OrderType: p.OrderType, Product: p.Product, Quantity: p.Quantity, Price: p.Price, TriggerPrice: p.TriggerPrice,
		Validity: p.Validity, Tag: p.Tag}
}

// itemParams is the order a basket item places, as the order form places
// it: a day order tagged kite-cli, market orders with market protection.
func itemParams(item models.BasketItem) kite.OrderParams {
	p := kite.OrderParams{Exchange: item.Exchange, Tradingsymbol: item.TradingSymbol, TransactionType: item.TransactionType,
		OrderType: item.OrderType, Product: item.Product, Quantity: item.Quantity, Price: item.Price, TriggerPrice: item.TriggerPrice,
		Validity: cmp.Or(item.Validity, kite.ValidityDay), Tag: cmp.Or(item.Tag, orderTag)}
	if p.OrderType == kite.OrderTypeMarket || p.OrderType == kite.OrderTypeSLM {
		p.MarketProtection = -1
	}
	return p
}

// itemText describes an item in a sentence.
func itemText(item models.BasketItem) string {
	return fmt.Sprintf("%s %d %s", item.TransactionType, item.Quantity, item.TradingSymbol)
}

// basketItemRef is the basket item the order form changes.
type basketItemRef struct {
	basketID string
	owner    string
	item     models.BasketItem
}

// basketsScreen is the Baskets tab: the active user's baskets beside the
// selected one's orders, priced, with what they come to.
type basketsScreen struct {
	owner  string
	stored []models.Basket
	// drafts are every user's unsaved baskets; nextDraft numbers them.
	drafts    []models.Basket
	nextDraft int
	loading   bool
	baskets   grid
	items     grid
	focus     int
	status    string
	back      screenID
	source    quoteSource
	quotes    map[string]kite.QuoteData
	fetching  bool
	priceErr  error
	// margin is the selected basket's margin, asked for the basket as
	// marginFor describes it, so a stale answer is dropped.
	margin    string
	marginFor string
	// results are the last placement's outcome, by item ID.
	results map[string]string
}

func newBasketsScreen() *basketsScreen {
	return &basketsScreen{
		baskets: grid{columns: []column{
			{title: "NAME", width: 14, flex: 1},
			{title: "ORDERS", width: 6, align: alignRight},
			{title: "UPDATED", width: 12},
		}},
		items: grid{columns: []column{
			{title: "SIDE", width: 4},
			{title: "QTY", width: 6, align: alignRight},
			{title: "INSTRUMENT", width: 14, flex: 2},
			{title: "ORDER", width: 16},
			{title: "PRODUCT", width: 7},
			{title: "LTP", width: 9, align: alignRight},
			{title: "VALUE", width: 11, align: alignRight},
			{title: "LAST RUN", width: 12, flex: 1},
		}},
		quotes:  map[string]kite.QuoteData{},
		results: map[string]string{},
	}
}

// shown are the owner's baskets: stored ones oldest first, then drafts.
func (s *basketsScreen) shown() []models.Basket {
	shown := slices.Clone(s.stored)
	for _, draft := range s.drafts {
		if draft.CreatedBy == s.owner {
			shown = append(shown, draft)
		}
	}
	return shown
}

func (s *basketsScreen) selected() (models.Basket, bool) {
	id, _ := s.baskets.selectedRef().(string)
	for _, basket := range s.shown() {
		if basket.ID == id {
			return basket, true
		}
	}
	return models.Basket{}, false
}

func (s *basketsScreen) selectedItem() (models.BasketItem, bool) {
	basket, ok := s.selected()
	id, _ := s.items.selectedRef().(string)
	if !ok {
		return models.BasketItem{}, false
	}
	for _, item := range basket.Items {
		if item.ID == id {
			return item, true
		}
	}
	return models.BasketItem{}, false
}

func (s *basketsScreen) focused() *grid {
	if s.focus == 1 {
		return &s.items
	}
	return &s.baskets
}

// draft is the draft basket id, to change in place.
func (s *basketsScreen) draft(id string) *models.Basket {
	for index := range s.drafts {
		if s.drafts[index].ID == id {
			return &s.drafts[index]
		}
	}
	return nil
}

// names are owner's basket names but except's, which a new name must not
// repeat; stored are the stored baskets known for owner.
func (s *basketsScreen) names(owner, except string, stored []models.Basket) []string {
	var names []string
	for _, basket := range stored {
		if basket.ID != except {
			names = append(names, basket.Name)
		}
	}
	for _, draft := range s.drafts {
		if draft.CreatedBy == owner && draft.ID != except {
			names = append(names, draft.Name)
		}
	}
	return names
}

// unsaved counts the draft baskets, of every user.
func (s *basketsScreen) unsaved() int { return len(s.drafts) }

// newDraft starts an unsaved basket of owner's with items.
func (s *basketsScreen) newDraft(owner, name string, items ...models.BasketItem) models.Basket {
	s.nextDraft++
	now := time.Now()
	draft := models.Basket{ID: draftPrefix + strconv.Itoa(s.nextDraft), CreatedBy: owner, Name: name, CreatedAt: now, UpdatedAt: now}
	s.drafts = append(s.drafts, draft)
	s.addToDraft(draft.ID, items...)
	return *s.draft(draft.ID)
}

// addToDraft appends items to a draft, giving each an ID.
func (s *basketsScreen) addToDraft(id string, items ...models.BasketItem) {
	draft := s.draft(id)
	for _, item := range items {
		s.nextDraft++
		item.ID = draftPrefix + "item:" + strconv.Itoa(s.nextDraft)
		item.CreatedAt, item.UpdatedAt = time.Now(), time.Now()
		draft.Items = append(draft.Items, item)
	}
	draft.UpdatedAt = time.Now()
}

// render fills both tables, selecting selectID when it is shown.
func (s *basketsScreen) render(selectID string) {
	if selectID == "" {
		if basket, ok := s.selected(); ok {
			selectID = basket.ID
		}
	}
	switch {
	case s.owner == "":
		s.baskets.empty = styleMuted.Render("Pick a user first")
	case s.loading:
		s.baskets.empty = styleMuted.Render("Loading baskets…")
	default:
		s.baskets.empty = styleMuted.Render("No baskets yet · N starts one, or Add to basket on an order")
	}
	previous, _ := s.selected()
	var rows []row
	for _, basket := range s.shown() {
		updated := textCell(basket.UpdatedAt.Local().Format("02 Jan 15:04"), styleMuted)
		name := textCell(basket.Name, styleCyan)
		if isDraft(basket.ID) {
			updated = textCell("unsaved", styleYellow)
			name = textCell(basket.Name+" *", styleYellow.Bold(true))
		}
		rows = append(rows, row{ref: basket.ID, cells: []cell{name, textCell(strconv.Itoa(len(basket.Items)), styleGreen), updated}})
	}
	s.baskets.setRows(rows)
	s.baskets.selectWhere(func(r row) bool { return r.ref == selectID })
	current, _ := s.selected()
	if current.ID != previous.ID {
		s.items.cursor, s.items.offset = 0, 0
	}
	s.renderItems()
	if len(s.items.rows) == 0 {
		s.focus = 0
	}
}

func (s *basketsScreen) renderItems() {
	basket, ok := s.selected()
	s.items.empty = styleMuted.Render("No orders · Add to basket on an order form adds one")
	if !ok {
		s.items.empty = ""
	}
	var rows []row
	for _, item := range basket.Items {
		style := styleGreen
		if item.TransactionType == kite.TransactionTypeSell {
			style = styleRed
		}
		ltp, value := textCell("—", styleFaint), textCell("—", styleFaint)
		last := s.quotes[string(item.InstrumentID())].LastPrice
		if last > 0 {
			ltp = textCell(formatPrice(last), styleText)
		}
		if price := cmp.Or(item.Price, last); price > 0 {
			value = textCell(formatAmount(price*float64(item.Quantity)), styleText)
		}
		result := textCell(s.results[item.ID], styleMuted)
		if strings.HasPrefix(s.results[item.ID], "✖") {
			result.style = styleRed
		} else if result.text != "" {
			result.style = styleGreen
		}
		rows = append(rows, row{ref: item.ID, cells: []cell{
			textCell(item.TransactionType, style.Bold(true)),
			textCell(strconv.Itoa(item.Quantity), styleText),
			textCell(item.TradingSymbol, styleBold),
			textCell(orderAt(itemParams(item)), styleText),
			textCell(item.Product, styleMuted),
			ltp, value, result,
		}})
	}
	s.items.setRows(rows)
}

// orderAt describes an order's type and prices.
func orderAt(p kite.OrderParams) string {
	switch p.OrderType {
	case kite.OrderTypeLimit:
		return "LIMIT " + formatAmount(p.Price)
	case kite.OrderTypeSL:
		return fmt.Sprintf("SL %s / %s", formatAmount(p.Price), formatAmount(p.TriggerPrice))
	case kite.OrderTypeSLM:
		return "SL-M " + formatAmount(p.TriggerPrice)
	}
	return "MARKET"
}

type (
	// basketsLoadedMsg ends a basket operation: owner's baskets reloaded
	// after it, and done, run with the basket ID the operation returned.
	basketsLoadedMsg struct {
		owner    string
		baskets  []models.Basket
		opErr    error
		loadErr  error
		selectID string
		done     func(a *app, selectID string, baskets []models.Basket) tea.Cmd
	}
	basketQuotesMsg struct {
		source quoteSource
		quotes kite.Quote
		err    error
	}
	basketMarginMsg struct {
		key     string
		margins kite.BasketMargins
		err     error
	}
	basketItemMsg struct {
		ref        basketItemRef
		instrument models.Instrument
		ltp        float64
		err        error
	}
	basketPlacedMsg struct {
		basket   models.Basket
		mode     broker.Mode
		results  map[string]string
		placed   int
		failures []string
	}
)

// basketOperation runs operation in the background, behind a busy dialog
// when message is set, then reloads owner's baskets and runs done. The
// baskets reload even when the operation fails.
func (a *app) basketOperation(owner, message string, operation func(context.Context, basketStore) (string, error), done func(*app, string, []models.Basket) tea.Cmd) tea.Cmd {
	store := a.deps.baskets
	if store == nil {
		a.notify(statusWarn("Baskets are unavailable"))
		return nil
	}
	var busy tea.Cmd
	if message != "" {
		busy = a.busy(message)
	}
	return tea.Batch(busy, a.task(func(ctx context.Context) tea.Msg {
		msg := basketsLoadedMsg{owner: owner, done: done}
		if operation != nil {
			msg.selectID, msg.opErr = operation(ctx, store)
		}
		msg.baskets, msg.loadErr = store.GetBaskets(ctx, owner)
		return msg
	}))
}

// openBaskets shows the Baskets tab for the active user.
func (a *app) openBaskets() tea.Cmd {
	s := a.baskets
	if a.screen != screenBaskets {
		s.back = a.screen
	}
	a.show(screenBaskets)
	owner := a.activeUser()
	if owner != s.owner {
		s.owner, s.stored, s.focus = owner, nil, 0
		s.baskets.cursor, s.baskets.offset = 0, 0
	}
	if owner == "" {
		s.render("")
		s.status = statusWarn("Pick a user first · baskets are each user's own")
		return nil
	}
	s.loading = true
	s.render("")
	return a.basketOperation(owner, "", nil, nil)
}

// basketsChanged re-renders the tab after its baskets changed, selecting
// selectID, and prices the selected basket again.
func (a *app) basketsChanged(selectID string) tea.Cmd {
	a.baskets.render(selectID)
	return tea.Batch(a.basketMargin(), a.fetchBasketQuotes())
}

// basketMargin asks for the selected basket's margin, once per state of
// the basket.
func (a *app) basketMargin() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	if !ok || len(basket.Items) == 0 || a.deps.basketMargins == nil {
		s.margin, s.marginFor = "", ""
		return nil
	}
	key := fmt.Sprintf("%s|%d|%d", basket.ID, basket.UpdatedAt.UnixNano(), len(basket.Items))
	if key == s.marginFor {
		return nil
	}
	s.marginFor, s.margin = key, styleFaint.Render("Margin …")
	legs := make([]kite.OrderMarginParam, len(basket.Items))
	for index, item := range basket.Items {
		legs[index] = marginLeg(itemParams(item))
	}
	margins := a.deps.basketMargins
	return a.task(func(ctx context.Context) tea.Msg {
		result, err := margins(ctx, legs)
		return basketMarginMsg{key: key, margins: result, err: err}
	})
}

// fetchBasketQuotes prices the selected basket's instruments, connecting
// the data user the first time when the explorer has not.
func (a *app) fetchBasketQuotes() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	if !ok || len(basket.Items) == 0 || s.fetching {
		return nil
	}
	source := s.source
	if source == nil {
		source = a.explorer.quoteSource
	}
	load := a.deps.loadQuotes
	if source == nil && load == nil {
		return nil
	}
	var ids []string
	for _, item := range basket.Items {
		ids = append(ids, string(item.InstrumentID()))
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	s.fetching = true
	return a.task(func(ctx context.Context) tea.Msg {
		msg := basketQuotesMsg{source: source}
		if msg.source == nil {
			if msg.source, _, msg.err = load(ctx); msg.err != nil {
				return msg
			}
		}
		msg.quotes, msg.err = msg.source.GetQuote(ids...)
		return msg
	})
}

func (a *app) handleBaskets(msg tea.Msg) (tea.Cmd, bool) {
	s := a.baskets
	switch msg := msg.(type) {
	case basketsLoadedMsg:
		if a.isBusy() {
			a.closeOverlay()
		}
		if msg.loadErr != nil {
			s.loading = false
			a.showError(errors.Join(msg.opErr, msg.loadErr))
			return nil, true
		}
		var cmd tea.Cmd
		if msg.owner == s.owner {
			s.stored, s.loading = msg.baskets, false
			cmd = a.basketsChanged(msg.selectID)
		}
		if msg.opErr != nil {
			a.showError(msg.opErr)
			return cmd, true
		}
		if msg.done != nil {
			cmd = tea.Batch(cmd, msg.done(a, msg.selectID, msg.baskets))
		}
		return cmd, true
	case basketQuotesMsg:
		s.fetching, s.priceErr = false, msg.err
		if msg.source != nil {
			s.source = msg.source
		}
		for id, quote := range msg.quotes {
			s.quotes[id] = quote
		}
		s.renderItems()
		return nil, true
	case basketMarginMsg:
		if msg.key != s.marginFor {
			return nil, true
		}
		if msg.err != nil {
			s.margin = styleFaint.Render("Margin unavailable: " + quoteErrorText(msg.err))
			return nil, true
		}
		final := msg.margins.Final.Total
		s.margin = styleMuted.Render("Margin ") + styleYellow.Render("≈ ₹"+formatAmount(final))
		if saved := msg.margins.Initial.Total - final; saved >= 1 {
			s.margin += styleMuted.Render(" · hedges save ") + styleGreen.Render("₹"+formatAmount(saved))
		}
		return nil, true
	case basketItemMsg:
		if a.isBusy() {
			a.closeOverlay()
		}
		if msg.err != nil {
			a.showError(fmt.Errorf("edit %s: %w", msg.ref.item.TradingSymbol, msg.err))
			return nil, true
		}
		item := msg.ref.item
		ref := msg.ref
		return a.openOrder(orderTicket{instrument: msg.instrument, side: item.TransactionType, quantity: item.Quantity,
			product: item.Product, orderType: item.OrderType, ltp: msg.ltp, userID: ref.owner,
			price: item.Price, trigger: item.TriggerPrice, item: &ref}), true
	case basketPlacedMsg:
		if a.isBusy() {
			a.closeOverlay()
		}
		for id, result := range msg.results {
			s.results[id] = result
		}
		s.renderItems()
		where := " · check their status in Kite"
		if msg.mode == broker.Paper {
			where = " in the paper account"
		}
		message := statusOK(fmt.Sprintf("Basket %s · %d %s placed for %s%s", msg.basket.Name, msg.placed, pluralize("order", msg.placed), msg.basket.CreatedBy, where))
		if len(msg.failures) > 0 {
			message = statusWarn(fmt.Sprintf("Basket %s · %d placed, %d failed", msg.basket.Name, msg.placed, len(msg.failures)))
			a.showError(fmt.Errorf("basket %s: %d of %d orders failed\n%s", msg.basket.Name, len(msg.failures),
				len(msg.failures)+msg.placed, strings.Join(msg.failures, "\n")))
		}
		a.notify(message)
		if msg.placed > 0 && a.portfolio.user.UserID == msg.basket.CreatedBy {
			a.portfolio.notice = message
			return a.reloadPortfolio(), true
		}
		return nil, true
	}
	return nil, false
}

// basketsKey handles the Baskets tab's keys.
func (a *app) basketsKey(msg tea.KeyPressMsg) tea.Cmd {
	s := a.baskets
	key := msg.String()
	before, _ := s.selected()
	if s.focused().update(key) {
		if after, _ := s.selected(); after.ID != before.ID {
			return a.basketsChanged("")
		}
		return nil
	}
	switch key {
	case "esc":
		if s.focus == 1 {
			s.focus = 0
			return nil
		}
		back := s.back
		if back == screenBaskets {
			back = screenUsers
		}
		a.show(back)
		return nil
	case "tab", "shift+tab":
		if s.focus == 0 && len(s.items.rows) > 0 {
			s.focus = 1
		} else {
			s.focus = 0
		}
		return nil
	case "enter":
		if s.focus == 0 {
			if len(s.items.rows) > 0 {
				s.focus = 1
			}
			return nil
		}
		return a.editBasketItem()
	case "delete", "backspace":
		return a.deleteBasketSelection()
	}
	switch strings.ToLower(key) {
	case "n":
		return a.newBasket()
	case "s":
		return a.saveDraft()
	case "e":
		if s.focus == 1 {
			return a.editBasketItem()
		}
		return a.renameBasket()
	case "d":
		return a.deleteBasketSelection()
	case "o":
		return a.confirmPlaceBasket()
	case "r":
		if s.owner == "" {
			return a.openBaskets()
		}
		s.marginFor = ""
		return tea.Batch(a.basketOperation(s.owner, "", nil, nil), a.fetchBasketQuotes())
	}
	return nil
}

// askBasketName asks for a basket name not among taken; Esc goes back to
// back.
func (a *app) askBasketName(title, value string, taken []string, back overlay, submit func(*app, string) tea.Cmd) {
	var input *inputOverlay
	input = newInputOverlay(title, "Name", value, "Save", func(a *app, name string) tea.Cmd {
		name = strings.TrimSpace(name)
		if slices.Contains(taken, name) {
			input.form.message = styleRed.Render("There is a basket named " + name + " already")
			a.overlay = input
			return nil
		}
		return submit(a, name)
	})
	input.back = back
	a.overlay = input
}

// nextBasketName suggests a name not among taken.
func nextBasketName(taken []string) string {
	for n := len(taken) + 1; ; n++ {
		if name := fmt.Sprintf("Basket %d", n); !slices.Contains(taken, name) {
			return name
		}
	}
}

// newBasket starts an empty draft for the tab's user; nothing is stored
// until S saves it.
func (a *app) newBasket() tea.Cmd {
	s := a.baskets
	if s.owner == "" {
		s.status = statusWarn("Pick a user first")
		return nil
	}
	taken := s.names(s.owner, "", s.stored)
	a.askBasketName("New basket", nextBasketName(taken), taken, nil, func(a *app, name string) tea.Cmd {
		draft := s.newDraft(s.owner, name)
		s.status = statusInfo(fmt.Sprintf("Started %s · unsaved until S saves it · Add to basket on an order fills it", name))
		return a.basketsChanged(draft.ID)
	})
	return nil
}

// saveDraft stores the selected draft, which then leaves the drafts.
func (a *app) saveDraft() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	if !ok {
		return nil
	}
	if !isDraft(basket.ID) {
		s.status = statusInfo(basket.Name + " is saved · changes to it are stored as they are made")
		return nil
	}
	draft := basket
	return a.basketOperation(basket.CreatedBy, "Saving "+basket.Name+"…", func(ctx context.Context, store basketStore) (string, error) {
		saved, err := store.CreateBasket(ctx, draft)
		return saved.ID, err
	}, func(a *app, savedID string, _ []models.Basket) tea.Cmd {
		s.drafts = slices.DeleteFunc(s.drafts, func(b models.Basket) bool { return b.ID == draft.ID })
		s.status = statusOK(fmt.Sprintf("Saved %s with %d %s", draft.Name, len(draft.Items), pluralize("order", len(draft.Items))))
		return a.basketsChanged(savedID)
	})
}

func (a *app) renameBasket() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	if !ok {
		return nil
	}
	a.askBasketName("Rename basket", basket.Name, s.names(basket.CreatedBy, basket.ID, s.stored), nil, func(a *app, name string) tea.Cmd {
		if draft := s.draft(basket.ID); draft != nil {
			draft.Name, draft.UpdatedAt = name, time.Now()
			return a.basketsChanged(basket.ID)
		}
		return a.basketOperation(basket.CreatedBy, "Renaming basket…", func(ctx context.Context, store basketStore) (string, error) {
			return basket.ID, store.RenameBasket(ctx, basket.ID, name)
		}, nil)
	})
	return nil
}

// deleteBasketSelection removes the selected order when the orders have
// focus, and otherwise deletes (or discards) the selected basket after
// asking.
func (a *app) deleteBasketSelection() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	if !ok {
		return nil
	}
	if s.focus == 1 {
		item, ok := s.selectedItem()
		if !ok {
			return nil
		}
		if draft := s.draft(basket.ID); draft != nil {
			draft.Items = slices.DeleteFunc(draft.Items, func(i models.BasketItem) bool { return i.ID == item.ID })
			draft.UpdatedAt = time.Now()
			s.status = statusInfo("Removed " + itemText(item))
			return a.basketsChanged(basket.ID)
		}
		return a.basketOperation(basket.CreatedBy, "Removing "+itemText(item)+"…", func(ctx context.Context, store basketStore) (string, error) {
			return basket.ID, store.RemoveBasketItems(ctx, basket.ID, []string{item.ID})
		}, func(a *app, _ string, _ []models.Basket) tea.Cmd {
			s.status = statusInfo("Removed " + itemText(item))
			return nil
		})
	}
	what := fmt.Sprintf("%s with %d %s", styleYellow.Bold(true).Render(basket.Name), len(basket.Items), pluralize("order", len(basket.Items)))
	if isDraft(basket.ID) {
		a.confirm("Discard basket", "Discard the unsaved basket "+what+"?", "Discard", func(a *app) tea.Cmd {
			s.drafts = slices.DeleteFunc(s.drafts, func(b models.Basket) bool { return b.ID == basket.ID })
			s.status = statusInfo("Discarded " + basket.Name)
			return a.basketsChanged("")
		})
		return nil
	}
	a.confirm("Delete basket", "Delete basket "+what+"?", "Delete", func(a *app) tea.Cmd {
		return a.basketOperation(basket.CreatedBy, "Deleting basket…", func(ctx context.Context, store basketStore) (string, error) {
			return "", store.DeleteBasket(ctx, basket.ID)
		}, func(a *app, _ string, _ []models.Basket) tea.Cmd {
			s.status = statusInfo("Deleted " + basket.Name)
			return nil
		})
	})
	return nil
}

// editBasketItem opens the selected order in the order form, looking its
// instrument up for the lot and tick sizes.
func (a *app) editBasketItem() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	item, found := s.selectedItem()
	if !ok || !found {
		return nil
	}
	ref := basketItemRef{basketID: basket.ID, owner: basket.CreatedBy, item: item}
	ltp := s.quotes[string(item.InstrumentID())].LastPrice
	catalog, open := a.explorer.catalog, a.deps.openCatalog
	return tea.Batch(a.busy("Opening "+itemText(item)+"…"), a.task(func(ctx context.Context) tea.Msg {
		msg := basketItemMsg{ref: ref, ltp: ltp}
		if catalog == nil {
			if open == nil {
				msg.err = errors.New("the instrument catalog is unavailable")
				return msg
			}
			if catalog, _, msg.err = open(ctx); msg.err != nil {
				return msg
			}
		}
		msg.instrument, msg.err = catalog.Get(ctx, item.InstrumentID())
		return msg
	}))
}

// saveBasketItem writes the form back into the basket order it edits.
func (a *app) saveBasketItem(o *orderForm) tea.Cmd {
	_, params, err := o.order()
	if err != nil {
		o.summary = styleRed.Render(err.Error())
		return nil
	}
	ref := o.ticket.item
	item := basketItemOf(params)
	item.ID, item.CreatedAt = ref.item.ID, ref.item.CreatedAt
	s := a.baskets
	a.closeOverlay()
	if draft := s.draft(ref.basketID); draft != nil {
		for index := range draft.Items {
			if draft.Items[index].ID == item.ID {
				draft.Items[index] = item
			}
		}
		draft.UpdatedAt = time.Now()
		s.status = statusOK("Changed " + itemText(item))
		return a.basketsChanged(ref.basketID)
	}
	return a.basketOperation(ref.owner, "Saving "+itemText(item)+"…", func(ctx context.Context, store basketStore) (string, error) {
		return ref.basketID, store.UpdateBasketItem(ctx, ref.basketID, item)
	}, func(a *app, _ string, _ []models.Basket) tea.Cmd {
		s.status = statusOK("Changed " + itemText(item))
		return nil
	})
}

// pickBasket offers to add the form's order to a new basket or one of its
// account's baskets.
func (a *app) pickBasket(o *orderForm) tea.Cmd {
	owner, params, err := o.order()
	if err != nil {
		o.summary = styleRed.Render(err.Error())
		return nil
	}
	if a.deps.baskets == nil {
		o.summary = styleRed.Render("Baskets are unavailable")
		return nil
	}
	o.summary = styleYellow.Render("Loading " + owner + "'s baskets…")
	item := basketItemOf(params)
	return a.basketOperation(owner, "", nil, func(a *app, _ string, stored []models.Basket) tea.Cmd {
		if a.overlay != o {
			return nil
		}
		o.refresh()
		a.openBasketPicker(o, owner, item, stored)
		return nil
	})
}

// openBasketPicker lists a new basket, owner's drafts and owner's stored
// baskets to add item to. Esc goes back to the order form.
func (a *app) openBasketPicker(o *orderForm, owner string, item models.BasketItem, stored []models.Basket) {
	s := a.baskets
	picker := &menu{title: "Add " + itemText(item) + " to", info: styleMuted.Render(owner), width: 52, back: o,
		footer: "↑↓ Enter · Esc back to the order"}
	added := func(a *app, name string, saved bool) tea.Cmd {
		note := " · unsaved, S on Baskets saves it"
		if saved {
			note = ""
		}
		a.notify(statusOK(fmt.Sprintf("Added %s to %s%s", itemText(item), name, note)))
		if a.screen == screenBaskets && s.owner == owner {
			return a.basketsChanged("")
		}
		return nil
	}
	taken := s.names(owner, "", stored)
	picker.items = append(picker.items, menuItem{label: styleAccent.Render("+ New basket…"), run: func(a *app) tea.Cmd {
		a.askBasketName("New basket", nextBasketName(taken), taken, picker, func(a *app, name string) tea.Cmd {
			s.newDraft(owner, name, item)
			return added(a, name, false)
		})
		return nil
	}})
	for _, basket := range stored {
		picker.items = append(picker.items, menuItem{
			label: styleCyan.Render(basket.Name) + styleFaint.Render(fmt.Sprintf("  (%d)", len(basket.Items))),
			run: func(a *app) tea.Cmd {
				return a.basketOperation(owner, "Adding to "+basket.Name+"…", func(ctx context.Context, store basketStore) (string, error) {
					return basket.ID, store.AddBasketItems(ctx, basket.ID, []models.BasketItem{item})
				}, func(a *app, _ string, _ []models.Basket) tea.Cmd { return added(a, basket.Name, true) })
			},
		})
	}
	for _, draft := range s.drafts {
		if draft.CreatedBy != owner {
			continue
		}
		picker.items = append(picker.items, menuItem{
			label: styleYellow.Render(draft.Name+" *") + styleFaint.Render(fmt.Sprintf("  (%d) · unsaved", len(draft.Items))),
			run: func(a *app) tea.Cmd {
				s.addToDraft(draft.ID, item)
				return added(a, draft.Name, false)
			},
		})
	}
	a.overlay = picker
}

// confirmPlaceBasket asks, then places the selected basket's orders into
// its owner's account in the current mode.
func (a *app) confirmPlaceBasket() tea.Cmd {
	s := a.baskets
	basket, ok := s.selected()
	switch {
	case !ok:
		return nil
	case len(basket.Items) == 0:
		s.status = statusWarn(basket.Name + " has no orders")
		return nil
	case a.deps.place == nil:
		s.status = statusWarn("Ordering is unavailable")
		return nil
	}
	mode := a.mode
	account := "paper account"
	if mode == broker.Live {
		account = "LIVE Kite account"
	}
	text := fmt.Sprintf("Place all %d orders of %s in %s's %s?\n\nBuys go first, so hedges are in before the shorts they cover.",
		len(basket.Items), styleYellow.Bold(true).Render(basket.Name), basket.CreatedBy, account)
	a.confirm("Place basket", text, "Place all", func(a *app) tea.Cmd { return a.placeBasket(basket, mode) })
	return nil
}

// placeBasket places every order of basket, buys first, and records each
// one's outcome.
func (a *app) placeBasket(basket models.Basket, mode broker.Mode) tea.Cmd {
	items := slices.Clone(basket.Items)
	slices.SortStableFunc(items, func(x, y models.BasketItem) int {
		return cmp.Compare(boolRank(x.TransactionType != kite.TransactionTypeBuy), boolRank(y.TransactionType != kite.TransactionTypeBuy))
	})
	place := a.deps.place
	return tea.Batch(a.busy(fmt.Sprintf("Placing %d orders of %s…", len(items), basket.Name)), a.task(func(ctx context.Context) tea.Msg {
		msg := basketPlacedMsg{basket: basket, mode: mode, results: map[string]string{}}
		for index, item := range items {
			a.progress(fmt.Sprintf("Placing %s (%d of %d)…", itemText(item), index+1, len(items)))
			orderID, err := place(ctx, basket.CreatedBy, mode, itemParams(item))
			if err != nil {
				reason := quoteErrorText(err)
				msg.results[item.ID] = "✖ " + reason
				msg.failures = append(msg.failures, itemText(item)+": "+reason)
				continue
			}
			msg.placed++
			msg.results[item.ID] = "✔ " + orderID
		}
		return msg
	}))
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// totals sums a basket's value and estimated charges.
func (s *basketsScreen) totals(basket models.Basket) string {
	var value float64
	var charged []kite.OrderChargesParam
	for _, item := range basket.Items {
		if price := cmp.Or(item.Price, s.quotes[string(item.InstrumentID())].LastPrice); price > 0 {
			value += price * float64(item.Quantity)
			charged = append(charged, chargesParam(itemParams(item), price))
		}
	}
	text := styleBold.Render(fmt.Sprintf("%d %s", len(basket.Items), pluralize("order", len(basket.Items))))
	if value > 0 {
		text += styleMuted.Render(" · value ") + styleText.Render("≈ ₹"+formatAmount(value))
	}
	if charges := exitCharges(charged, len(basket.Items)); charges != "" {
		text += styleMuted.Render(" · ") + strings.TrimPrefix(charges, "\n")
	}
	return text
}

func (a *app) basketsView(w, h int) string {
	s := a.baskets
	widths := splitWidth(w, 2, 5)
	const top = 1
	s.baskets.x, s.baskets.y = 1, top+2
	s.items.x, s.items.y = widths[0]+1, top+2
	title := "Baskets"
	if s.owner != "" {
		title += " · " + s.owner
	}
	left := panel(title, styleMuted.Render(strconv.Itoa(len(s.shown()))), s.baskets.view(widths[0]-2, h-2, s.focus == 0), widths[0], h, s.focus == 0)

	basket, ok := s.selected()
	var footer []string
	if ok && len(basket.Items) > 0 {
		footer = append(footer, s.totals(basket))
		if s.margin != "" {
			footer = append(footer, s.margin)
		}
	}
	if s.priceErr != nil {
		footer = append(footer, styleRed.Render("Prices failed: "+quoteErrorText(s.priceErr)))
	}
	itemsH := max(h-2-len(footer)-1, 3)
	body := s.items.view(widths[1]-2, itemsH, s.focus == 1)
	if len(footer) > 0 {
		body += "\n\n" + strings.Join(footer, "\n")
	}
	name, info := "Orders", ""
	if ok {
		name = basket.Name
		if isDraft(basket.ID) {
			info = styleYellow.Render("unsaved · S saves")
		} else {
			info = styleMuted.Render("saved")
		}
	}
	right := panel(name, info, body, widths[1], h, s.focus == 1)
	return joinColumns(left, right)
}

func (a *app) basketsHelp() string {
	if a.baskets.focus == 1 {
		return help("Enter/E", "edit order", "D", "remove order", "O", "place all", "Tab/Esc", "baskets", "R", "refresh", "P", "paper / live", "^C", "quit")
	}
	return help("N", "new", "S", "save draft", "E", "rename", "D", "delete", "O", "place all", "Enter/Tab", "orders", "R", "refresh", "P", "paper / live", "W", "watchlists", "Esc", "back", "^C", "quit")
}
