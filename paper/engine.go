package paper

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

// Order states besides Kite's final ones.
const (
	statusOpen           = "OPEN"
	statusTriggerPending = "TRIGGER PENDING"
)

// Kite's own wording, for the errors traders know.
const (
	msgMarketClosed = "Markets are closed right now. Use GTT for placing long standing orders instead."
	msgNoInstrument = "The instrument you are placing an order for has either expired or does not exist."
)

// systemTag marks the orders a paper account places itself: square-offs and
// expiries.
const systemTag = "paper"

// terms are an order's contract terms, kept in its meta so matching and
// settlement need not look the instrument up again.
type terms struct {
	LotSize         int                 `json:"lot_size,omitempty"`
	TickSize        float64             `json:"tick_size,omitempty"`
	InstrumentType  string              `json:"instrument_type,omitempty"`
	Expiry          time.Time           `json:"expiry,omitzero"`
	Strike          float64             `json:"strike,omitempty"`
	Underlying      models.InstrumentID `json:"underlying,omitempty"`
	UnderlyingToken uint32              `json:"underlying_token,omitempty"`
}

const metaKey = "paper"

// termsOf reads an order's terms back from its meta, which holds them as
// placed or, once stored, as decoded JSON.
func termsOf(o kite.Order) terms {
	var t terms
	if raw, ok := o.Meta[metaKey]; ok {
		if data, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(data, &t)
		}
	}
	return t
}

func termsOfFill(f models.PaperFill) terms {
	return terms{InstrumentType: f.InstrumentType, Expiry: f.Expiry, Strike: f.Strike,
		Underlying: f.Underlying, UnderlyingToken: f.UnderlyingToken}
}

func isOpen(o kite.Order) bool { return o.Status == statusOpen || o.Status == statusTriggerPending }

func isStop(orderType string) bool {
	return orderType == kite.OrderTypeSL || orderType == kite.OrderTypeSLM
}

func orderID(o kite.Order) string { return o.Exchange + ":" + o.TradingSymbol }

func invalid(format string, args ...any) error {
	return kite.NewError(kite.InputError, fmt.Sprintf(format, args...), nil)
}

// session is one locked step of a user's paper account: it reads the
// stored orders and fills, changes them in memory, and records what
// changed in one write.
type session struct {
	ctx  context.Context
	book *Book
	user *userBook
	now  time.Time

	data    Data
	dataErr error
	quotes  kite.Quote
	asked   map[string]bool
	// quoteErr is why the last quote failed.
	quoteErr error

	orders  kite.Orders
	fills   []models.PaperFill
	settled time.Time
	// checked is how far candles were matched for each order this step.
	checked map[string]time.Time

	change  repository.PaperChange
	changed []string
}

func (b *Book) begin(ctx context.Context, user *userBook) (*session, error) {
	orders, err := user.store.Orders(ctx)
	if err != nil {
		return nil, err
	}
	fills, err := user.store.Fills(ctx)
	if err != nil {
		return nil, err
	}
	return &session{ctx: ctx, book: b, user: user, now: b.config.Now(), quotes: kite.Quote{}, asked: map[string]bool{},
		orders: orders, fills: fills, settled: dayOf(user.account.SettledThrough), checked: map[string]time.Time{}}, nil
}

// commit records what the step changed.
func (s *session) commit() error {
	change := s.change
	if s.settled.After(s.user.account.SettledThrough) {
		change.SettledThrough = s.settled
	}
	if len(change.Orders) == 0 && len(change.Fills) == 0 && change.SettledThrough.IsZero() {
		return nil
	}
	if err := s.user.store.Record(s.ctx, change); err != nil {
		return err
	}
	if !change.SettledThrough.IsZero() {
		s.user.account.SettledThrough = change.SettledThrough
	}
	for id, at := range s.checked {
		s.user.checked[id] = at
	}
	s.user.view = nil
	s.change = repository.PaperChange{}
	return nil
}

// changedOrders are the latest states of the orders the step changed.
func (s *session) changedOrders() []kite.Order {
	var out []kite.Order
	for _, id := range s.changed {
		if o := s.order(id); o != nil {
			out = append(out, *o)
		}
	}
	return out
}

func (s *session) market() (Data, error) {
	if s.data == nil && s.dataErr == nil {
		s.data, s.dataErr = s.book.market(s.ctx)
	}
	return s.data, s.dataErr
}

// quote quotes the ids not quoted yet this step, in one call, and returns
// every quote so far with why the last call failed.
func (s *session) quote(ids ...string) (kite.Quote, error) {
	var missing []string
	for _, id := range ids {
		if !s.asked[id] {
			s.asked[id] = true
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		data, err := s.market()
		if err == nil {
			var quotes kite.Quote
			if quotes, err = data.GetQuote(missing...); err == nil {
				for id, q := range quotes {
					s.quotes[id] = q
				}
			}
		}
		if err != nil {
			s.quoteErr = fmt.Errorf("paper quotes: %w", err)
		}
	}
	return s.quotes, s.quoteErr
}

func (s *session) order(id string) *kite.Order {
	for i := range s.orders {
		if s.orders[i].OrderID == id {
			return &s.orders[i]
		}
	}
	return nil
}

// update records o's new state.
func (s *session) update(o *kite.Order) {
	s.change.Orders = append(s.change.Orders, *o)
	if !slices.Contains(s.changed, o.OrderID) {
		s.changed = append(s.changed, o.OrderID)
	}
}

// nextID numbers the user's next order or trade: prefix, the day and a
// sequence that runs across days.
func nextID(prefix string, seq *int, at time.Time) string {
	*seq++
	return fmt.Sprintf("%s%s%06d", prefix, at.In(ist).Format("060102"), *seq)
}

// lastSeq is the highest sequence among ids made by nextID.
func lastSeq[T any](items []T, id func(T) string) int {
	last := 0
	for _, item := range items {
		value := id(item)
		if len(value) <= 7 {
			continue
		}
		if n, err := strconv.Atoi(value[7:]); err == nil {
			last = max(last, n)
		}
	}
	return last
}

// checkedFrom is the first minute candle that can still fill o: the one
// after it was placed or last changed, or after the last check.
func (s *session) checkedFrom(o kite.Order) time.Time {
	changed := o.OrderTimestamp.Time
	if o.ExchangeUpdateTimestamp.After(changed) {
		changed = o.ExchangeUpdateTimestamp.Time
	}
	from := floorMinute(changed).Add(time.Minute)
	for _, checked := range []map[string]time.Time{s.user.checked, s.checked} {
		if at, ok := checked[o.OrderID]; ok && at.After(from) {
			from = at
		}
	}
	return from
}

// fill executes quantity of o at price.
func (s *session) fill(o *kite.Order, quantity int, price float64, at time.Time, kind string) {
	t := termsOf(*o)
	f := models.PaperFill{
		TradeID: nextID("T", &s.user.tradeSeq, at), OrderID: o.OrderID, Kind: kind,
		Exchange: o.Exchange, TradingSymbol: o.TradingSymbol, InstrumentToken: o.InstrumentToken,
		Product: o.Product, TransactionType: o.TransactionType, Quantity: quantity, Price: round2(price), FilledAt: at,
		InstrumentType: t.InstrumentType, Expiry: t.Expiry, Strike: t.Strike, Underlying: t.Underlying, UnderlyingToken: t.UnderlyingToken,
	}
	filled := o.FilledQuantity
	o.AveragePrice = round2((o.AveragePrice*filled + f.Price*float64(quantity)) / (filled + float64(quantity)))
	o.FilledQuantity += float64(quantity)
	o.PendingQuantity = max(o.Quantity-o.FilledQuantity, 0)
	o.ExchangeUpdateTimestamp = models.Time{Time: at.In(ist)}
	if o.PendingQuantity == 0 {
		o.Status = kite.OrderStatusComplete
	}
	if kind == models.PaperFillTrade {
		f.Charges = round2(max(orderCharges(*o)-s.charged(o.OrderID), 0))
	}
	s.fills = append(s.fills, f)
	s.change.Fills = append(s.change.Fills, f)
}

// orderCharges are the charges on what o has filled, by Kite's schedule.
func orderCharges(o kite.Order) float64 {
	charges, err := kite.EstimateOrderCharges([]kite.OrderChargesParam{{
		OrderID: o.OrderID, Exchange: o.Exchange, Tradingsymbol: o.TradingSymbol, TransactionType: o.TransactionType,
		Variety: kite.VarietyRegular, Product: o.Product, OrderType: o.OrderType,
		Quantity: o.FilledQuantity, AveragePrice: o.AveragePrice,
	}})
	if err != nil || len(charges) == 0 {
		return 0
	}
	return charges[0].Charges.Total
}

func (s *session) charged(orderID string) float64 {
	total := 0.0
	for _, f := range s.fills {
		if f.OrderID == orderID {
			total += f.Charges
		}
	}
	return total
}

// cancel ends what o has left.
func (s *session) cancel(o *kite.Order, at time.Time, message string) {
	o.CancelledQuantity = o.PendingQuantity
	o.PendingQuantity = 0
	o.Status = kite.OrderStatusCancelled
	o.StatusMessage = message
	o.ExchangeUpdateTimestamp = models.Time{Time: at.In(ist)}
	s.update(o)
}

// depth is the side of q's book an order of side takes from.
func depth(side string, q kite.QuoteData) []models.DepthItem {
	levels := q.Depth.Sell[:]
	if side == kite.TransactionTypeSell {
		levels = q.Depth.Buy[:]
	}
	var out []models.DepthItem
	for _, level := range levels {
		if level.Price > 0 && level.Quantity > 0 {
			out = append(out, level)
		}
	}
	return out
}

// slipped moves a market fill's price against the order by the configured
// slippage, onto the tick and within the day's circuits.
func (s *session) slipped(side string, price float64, t terms, q kite.QuoteData) float64 {
	tick := cmp.Or(t.TickSize, 0.05)
	bps := s.book.config.SlippageBPS
	if side == kite.TransactionTypeBuy {
		if bps > 0 {
			price = roundTick(price*(1+bps/10_000), tick, true)
		}
		if q.UpperCircuitLimit > 0 {
			price = min(price, q.UpperCircuitLimit)
		}
		return price
	}
	if bps > 0 {
		price = roundTick(price*(1-bps/10_000), tick, false)
	}
	return max(price, q.LowerCircuitLimit)
}

// fillMarket fills what o has left at market: level by level through the
// depth, then the rest at the last level reached, or at the last price when
// the quote shows no depth.
func (s *session) fillMarket(o *kite.Order, q kite.QuoteData, at time.Time) {
	t := termsOf(*o)
	left, last := int(o.PendingQuantity), q.LastPrice
	for _, level := range depth(o.TransactionType, q) {
		if left == 0 {
			break
		}
		quantity := min(left, int(level.Quantity))
		s.fill(o, quantity, s.slipped(o.TransactionType, level.Price, t, q), at, models.PaperFillTrade)
		left, last = left-quantity, level.Price
	}
	if left > 0 && last > 0 {
		s.fill(o, left, s.slipped(o.TransactionType, last, t, q), at, models.PaperFillTrade)
	}
}

// within reports whether price is at o's limit or better for it.
func within(o kite.Order, price float64) bool {
	if o.TransactionType == kite.TransactionTypeBuy {
		return price <= o.Price
	}
	return price >= o.Price
}

// fillLimit fills what o has left at its limit or better. A new order
// takes the depth's levels within the limit, or with no depth to go by the
// last price when it is within. A resting order was in the book first, so
// it fills at its limit once the best offer or the last trade reaches it.
func (s *session) fillLimit(o *kite.Order, q kite.QuoteData, at time.Time, resting bool) {
	levels := depth(o.TransactionType, q)
	if resting {
		if (len(levels) > 0 && within(*o, levels[0].Price)) || (q.LastPrice > 0 && within(*o, q.LastPrice)) {
			s.fill(o, int(o.PendingQuantity), o.Price, at, models.PaperFillTrade)
		}
		return
	}
	left := int(o.PendingQuantity)
	for _, level := range levels {
		if left == 0 || !within(*o, level.Price) {
			break
		}
		quantity := min(left, int(level.Quantity))
		s.fill(o, quantity, level.Price, at, models.PaperFillTrade)
		left -= quantity
	}
	if left > 0 && len(levels) == 0 && q.LastPrice > 0 && within(*o, q.LastPrice) {
		s.fill(o, left, q.LastPrice, at, models.PaperFillTrade)
	}
}

// triggered reports whether price sets off o's stop.
func triggered(o kite.Order, price float64) bool {
	if o.TransactionType == kite.TransactionTypeBuy {
		return price >= o.TriggerPrice
	}
	return price <= o.TriggerPrice
}

// matchLive matches o against the live quote. A resting order has been in
// the book since an earlier quote; a new one only takes what is offered.
func (s *session) matchLive(o *kite.Order, q kite.QuoteData, resting bool) {
	before := *o
	at := s.now
	if o.Status == statusTriggerPending {
		if q.LastPrice <= 0 || !triggered(*o, q.LastPrice) {
			return
		}
		o.Status = statusOpen
		o.ExchangeUpdateTimestamp = models.Time{Time: at.In(ist)}
		resting = false
	}
	switch o.OrderType {
	case kite.OrderTypeMarket, kite.OrderTypeSLM:
		s.fillMarket(o, q, at)
	default:
		s.fillLimit(o, q, at, resting)
	}
	if o.Validity == kite.ValidityIOC && isOpen(*o) {
		s.cancel(o, at, "IOC order's unfilled quantity cancelled.")
		return
	}
	if o.Status != before.Status || o.FilledQuantity != before.FilledQuantity {
		s.update(o)
	}
}

// matchOpen matches every open order against the live quote while the
// market is open.
func (s *session) matchOpen() {
	if !inSession(s.now) {
		return
	}
	var ids []string
	for _, o := range s.orders {
		if isOpen(o) {
			ids = append(ids, orderID(o))
		}
	}
	if len(ids) == 0 {
		return
	}
	quotes, _ := s.quote(ids...)
	for i := range s.orders {
		o := &s.orders[i]
		if q, ok := quotes[orderID(*o)]; ok && isOpen(*o) {
			s.matchLive(o, q, true)
		}
	}
}

// newOrder checks params the way Kite does before an order reaches the
// exchange and builds the order; an error here records nothing.
func (s *session) newOrder(variety string, params kite.OrderParams) (kite.Order, error) {
	if variety != kite.VarietyRegular {
		return kite.Order{}, invalid("Paper accounts take regular orders only.")
	}
	exchange := strings.ToUpper(strings.TrimSpace(params.Exchange))
	symbol := strings.ToUpper(strings.TrimSpace(params.Tradingsymbol))
	if !exchanges[exchange] {
		return kite.Order{}, invalid("Paper accounts trade on NSE, BSE, NFO and BFO only.")
	}
	side := params.TransactionType
	if side != kite.TransactionTypeBuy && side != kite.TransactionTypeSell {
		return kite.Order{}, invalid("Invalid transaction type %q.", side)
	}
	orderType := params.OrderType
	if !slices.Contains([]string{kite.OrderTypeMarket, kite.OrderTypeLimit, kite.OrderTypeSL, kite.OrderTypeSLM}, orderType) {
		return kite.Order{}, invalid("Invalid order type %q.", orderType)
	}
	validity := cmp.Or(params.Validity, kite.ValidityDay)
	if validity != kite.ValidityDay && validity != kite.ValidityIOC {
		return kite.Order{}, invalid("Paper accounts take DAY and IOC orders only.")
	}
	instrument, err := s.book.config.Instruments(s.ctx, models.InstrumentID(exchange+":"+symbol))
	if err != nil {
		return kite.Order{}, invalid(msgNoInstrument)
	}
	var expiry time.Time
	if instrument.Expiry != nil {
		expiry = dayOf(*instrument.Expiry)
		if expiry.Before(dayOf(s.now)) {
			return kite.Order{}, invalid(msgNoInstrument)
		}
	}
	derivative := exchange == kite.ExchangeNFO || exchange == kite.ExchangeBFO
	switch params.Product {
	case kite.ProductMIS:
	case kite.ProductCNC:
		if derivative {
			return kite.Order{}, invalid("Use NRML or MIS for futures and options; CNC is for delivery.")
		}
	case kite.ProductNRML:
		if !derivative {
			return kite.Order{}, invalid("Use CNC or MIS for equity; NRML is for futures and options.")
		}
	default:
		return kite.Order{}, invalid("Paper accounts take CNC, MIS and NRML orders.")
	}
	lot := max(instrument.LotSize, 1)
	if params.Quantity <= 0 || params.Quantity%lot != 0 {
		return kite.Order{}, invalid("Quantity should be a multiple of the lot size %d.", lot)
	}
	tick := cmp.Or(instrument.TickSize, 0.05)
	price, trigger := params.Price, params.TriggerPrice
	if orderType == kite.OrderTypeMarket || orderType == kite.OrderTypeSLM {
		price = 0
	} else if price <= 0 || !onTick(price, tick) {
		return kite.Order{}, invalid("Price should be a positive multiple of the tick size %g.", tick)
	}
	if !isStop(orderType) {
		trigger = 0
	} else if trigger <= 0 || !onTick(trigger, tick) {
		return kite.Order{}, invalid("Trigger price should be a positive multiple of the tick size %g.", tick)
	}
	if orderType == kite.OrderTypeSL {
		if side == kite.TransactionTypeBuy && price < trigger {
			return kite.Order{}, invalid("Trigger price for stoploss buy orders cannot be above the order price.")
		}
		if side == kite.TransactionTypeSell && price > trigger {
			return kite.Order{}, invalid("Trigger price for stoploss sell orders cannot be below the order price.")
		}
	}
	t := terms{LotSize: lot, TickSize: tick, InstrumentType: instrument.InstrumentType, Expiry: expiry, Strike: instrument.Strike}
	if instrument.UnderlyingID != nil {
		t.Underlying = *instrument.UnderlyingID
		if underlying, err := s.book.config.Instruments(s.ctx, *instrument.UnderlyingID); err == nil {
			t.UnderlyingToken = uint32(underlying.InstrumentToken)
		}
	}
	status := statusOpen
	if isStop(orderType) {
		status = statusTriggerPending
	}
	return kite.Order{
		AccountID: s.user.userID, PlacedBy: s.user.userID, Status: status,
		OrderTimestamp: models.Time{Time: s.now.In(ist)}, Variety: kite.VarietyRegular,
		Meta:     map[string]interface{}{metaKey: t},
		Exchange: exchange, TradingSymbol: symbol, InstrumentToken: uint32(instrument.InstrumentToken),
		OrderType: orderType, TransactionType: side, Validity: validity, Product: params.Product,
		Quantity: float64(params.Quantity), Price: price, TriggerPrice: trigger,
		PendingQuantity: float64(params.Quantity), Tag: params.Tag,
	}, nil
}

// orderQuote is the quote o trades on, after checking the market takes
// orders now and o's stop is on the right side of the last price.
func (s *session) orderQuote(o kite.Order) (kite.QuoteData, error) {
	id := orderID(o)
	quotes, err := s.quote(id, marketIndex)
	q, ok := quotes[id]
	if !ok {
		if err != nil {
			return q, err
		}
		return q, invalid("No quote for %s.", id)
	}
	if !s.marketOpen(quotes) {
		return q, invalid(msgMarketClosed)
	}
	if o.Product == kite.ProductMIS && !s.now.Before(dayOf(s.now).Add(squareOffAt)) {
		return q, invalid("MIS orders are blocked after the 3:20 PM square-off. Use NRML or CNC.")
	}
	if o.Status == statusTriggerPending && q.LastPrice > 0 && triggered(o, q.LastPrice) {
		if o.TransactionType == kite.TransactionTypeBuy {
			return q, invalid("Trigger price for stoploss buy orders should be higher than the last traded price.")
		}
		return q, invalid("Trigger price for stoploss sell orders should be lower than the last traded price.")
	}
	return q, nil
}

// marketOpen reports whether the exchanges are taking orders: in session
// on a weekday, with the index ticking.
func (s *session) marketOpen(quotes kite.Quote) bool {
	if !inSession(s.now) {
		return false
	}
	index, ok := quotes[marketIndex]
	if !ok {
		return false
	}
	ticked := index.Timestamp.Time
	if index.LastTradeTime.After(ticked) {
		ticked = index.LastTradeTime.Time
	}
	return s.now.Sub(ticked) <= staleIndex
}

// rejection is why the risk checks turn o away, or "" when they pass.
// replacing is the open order o changes, if any.
func (s *session) rejection(o kite.Order, q kite.QuoteData, replacing string) string {
	for _, price := range []float64{o.Price, o.TriggerPrice} {
		if price <= 0 {
			continue
		}
		if q.LowerCircuitLimit > 0 && price < q.LowerCircuitLimit {
			return fmt.Sprintf("Your order price is lower than the current lower circuit limit of %.2f. Place an order within the daily range.", q.LowerCircuitLimit)
		}
		if q.UpperCircuitLimit > 0 && price > q.UpperCircuitLimit {
			return fmt.Sprintf("Your order price is higher than the current upper circuit limit of %.2f. Place an order within the daily range.", q.UpperCircuitLimit)
		}
	}
	var pending []kite.Order
	for _, open := range s.orders {
		if isOpen(open) && open.OrderID != replacing {
			pending = append(pending, open)
		}
	}
	if o.Product == kite.ProductCNC && o.TransactionType == kite.TransactionTypeSell {
		key := positionKey{o.Exchange, o.TradingSymbol, kite.ProductCNC}
		sellable := openAt(s.fills, s.now)[key].quantity
		for _, open := range pending {
			if open.Product == kite.ProductCNC && open.TransactionType == kite.TransactionTypeSell && orderID(open) == orderID(o) {
				sellable -= int(open.PendingQuantity)
			}
		}
		if int(o.PendingQuantity) > sellable {
			return fmt.Sprintf("Insufficient holdings: %d %s can be sold on %s.", max(sellable, 0), o.TradingSymbol, o.Exchange)
		}
		return ""
	}
	l := s.ledger()
	legs := append(positionLegs(l), s.orderLegs(pending)...)
	before := s.block(legs)
	after := s.block(append(legs, s.orderLegs([]kite.Order{o})...))
	available := availableMargin(l, before)
	if required := after.used() - before.used(); required > available+0.005 {
		return fmt.Sprintf("Insufficient funds. Required margin is %.2f but available margin is %.2f.", required, available)
	}
	return ""
}

// place places an order. A rejected order is recorded and returned with
// the reason as an error, the way Kite's order book shows it.
func (s *session) place(variety string, params kite.OrderParams) (string, error) {
	o, err := s.newOrder(variety, params)
	if err != nil {
		return "", err
	}
	q, err := s.orderQuote(o)
	if err != nil {
		return "", err
	}
	o.OrderID = nextID("P", &s.user.orderSeq, s.now)
	reason := s.rejection(o, q, "")
	s.orders = append(s.orders, o)
	placed := &s.orders[len(s.orders)-1]
	if reason != "" {
		placed.Status, placed.StatusMessage, placed.PendingQuantity = kite.OrderStatusRejected, reason, 0
		s.update(placed)
		return placed.OrderID, kite.NewError(kite.OrderError, reason, nil)
	}
	s.update(placed)
	s.checked[placed.OrderID] = floorMinute(s.now).Add(time.Minute)
	if placed.Status == statusOpen {
		s.matchLive(placed, q, false)
	}
	return placed.OrderID, nil
}

// modify changes an open order's type, quantity, prices and validity.
func (s *session) modify(id string, params kite.OrderParams) error {
	o := s.order(id)
	if o == nil {
		return invalid("Order %s does not exist.", id)
	}
	if !isOpen(*o) {
		return invalid("Order %s is %s and cannot be modified.", id, strings.ToLower(o.Status))
	}
	merged := kite.OrderParams{
		Exchange: o.Exchange, Tradingsymbol: o.TradingSymbol, Product: o.Product, TransactionType: o.TransactionType,
		OrderType: cmp.Or(params.OrderType, o.OrderType), Validity: cmp.Or(params.Validity, o.Validity),
		Quantity: cmp.Or(params.Quantity, int(o.Quantity)), Price: params.Price, TriggerPrice: params.TriggerPrice, Tag: o.Tag,
	}
	next, err := s.newOrder(kite.VarietyRegular, merged)
	if err != nil {
		return err
	}
	if next.Quantity <= o.FilledQuantity {
		return invalid("Quantity should be more than the %d already filled.", int(o.FilledQuantity))
	}
	// A stop already set off stays set off unless it becomes another stop.
	if isStop(next.OrderType) && isStop(o.OrderType) && o.Status == statusOpen {
		next.Status = statusOpen
	}
	next.PendingQuantity = next.Quantity - o.FilledQuantity
	q, err := s.orderQuote(next)
	if err != nil {
		return err
	}
	if reason := s.rejection(next, q, id); reason != "" {
		return kite.NewError(kite.OrderError, reason, nil)
	}
	o.OrderType, o.Validity, o.Quantity, o.Price, o.TriggerPrice = next.OrderType, next.Validity, next.Quantity, next.Price, next.TriggerPrice
	o.PendingQuantity, o.Status, o.Modified = next.PendingQuantity, next.Status, true
	o.ExchangeUpdateTimestamp = models.Time{Time: s.now.In(ist)}
	s.update(o)
	s.checked[id] = floorMinute(s.now).Add(time.Minute)
	if o.Status == statusOpen {
		s.matchLive(o, q, false)
	}
	return nil
}

// cancelOrder cancels an open order.
func (s *session) cancelOrder(id string) error {
	o := s.order(id)
	if o == nil {
		return invalid("Order %s does not exist.", id)
	}
	if !isOpen(*o) {
		return invalid("Order %s is %s and cannot be cancelled.", id, strings.ToLower(o.Status))
	}
	s.cancel(o, s.now, "")
	return nil
}

// systemOrder places and fills an order of the account's own, for a
// square-off or an expiry.
func (s *session) systemOrder(key positionKey, f models.PaperFill, open lot, price float64, at time.Time, kind, message string) {
	side := kite.TransactionTypeSell
	if open.quantity < 0 {
		side = kite.TransactionTypeBuy
	}
	quantity := abs(open.quantity)
	s.orders = append(s.orders, kite.Order{
		AccountID: s.user.userID, PlacedBy: systemTag, OrderID: nextID("P", &s.user.orderSeq, at),
		Status: statusOpen, StatusMessage: message, OrderTimestamp: models.Time{Time: at.In(ist)}, Variety: kite.VarietyRegular,
		Meta:     map[string]interface{}{metaKey: termsOfFill(f)},
		Exchange: key.exchange, TradingSymbol: key.symbol, InstrumentToken: f.InstrumentToken,
		OrderType: kite.OrderTypeMarket, TransactionType: side, Validity: kite.ValidityDay, Product: key.product,
		Quantity: float64(quantity), PendingQuantity: float64(quantity), Tag: systemTag,
	})
	o := &s.orders[len(s.orders)-1]
	s.fill(o, quantity, price, at, kind)
	s.update(o)
}
