package paper

import (
	"fmt"
	"slices"
	"time"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
)

// advance brings the account up to the clock: it runs each closed day not
// yet settled, then today as far as it has gone. Settling a closed day must
// succeed, or the account would show what never happened; today's candles
// may lag, so a run of today keeps going on the live quote without them.
func (s *session) advance() error {
	today := dayOf(s.now)
	for day := nextDay(s.settled); !day.After(today); day = nextDay(day) {
		end := day.Add(sessionEnd)
		if s.now.Before(end) {
			break
		}
		if isWeekday(day) {
			if err := s.runDay(day, end, false); err != nil {
				return fmt.Errorf("paper %s: settle %s: %w", s.user.userID, day.Format(time.DateOnly), err)
			}
		}
		s.settled = day
	}
	if !s.settled.Before(today) || !isWeekday(today) || s.now.Before(today.Add(sessionOpen)) {
		return nil
	}
	s.prefetch()
	return s.runDay(today, s.now, true)
}

// prefetch quotes, in one call, everything this step can need: the market
// index, the open orders and whatever the account holds.
func (s *session) prefetch(extra ...string) {
	ids := append([]string{marketIndex}, extra...)
	for _, o := range s.orders {
		if isOpen(o) {
			ids = append(ids, orderID(o))
		}
	}
	today := dayOf(s.now)
	for key := range openAt(s.fills, s.now) {
		ids = append(ids, string(key.id()))
	}
	for _, f := range s.fills {
		if !f.FilledAt.Before(today) {
			ids = append(ids, string(f.ID()))
		}
	}
	slices.Sort(ids)
	_, _ = s.quote(slices.Compact(ids)...)
}

// runDay runs day's matching and day-end work up to through: candles to
// the square-off, the square-off, candles to the close, and at the close
// the day's end. A live run, of today, also matches on the live quote and
// leaves for a later step what the market data cannot give it yet.
func (s *session) runDay(day, through time.Time, live bool) error {
	squareOff, end := day.Add(squareOffAt), day.Add(sessionEnd)
	replay := func(until time.Time) error {
		err := s.replay(earlier(floorMinute(through), until))
		if live {
			return nil
		}
		return err
	}
	if err := replay(squareOff); err != nil {
		return err
	}
	if !through.Before(squareOff) {
		if err := s.squareOff(day); err != nil && !live {
			return err
		}
	}
	if err := replay(end); err != nil {
		return err
	}
	if live {
		s.matchOpen()
	}
	if through.Before(end) {
		return nil
	}
	for i := range s.orders {
		if o := &s.orders[i]; isOpen(*o) {
			s.cancel(o, end, "Cancelled at the close.")
		}
	}
	return s.expire(day)
}

// replay matches the open orders against the minute candles from each
// order's last check up to until, a minute boundary: one candle request
// per instrument.
func (s *session) replay(until time.Time) error {
	type waiting struct {
		index int
		from  time.Time
	}
	byToken := map[uint32][]waiting{}
	var tokens []uint32
	for i, o := range s.orders {
		if !isOpen(o) {
			continue
		}
		from := s.checkedFrom(o)
		if !from.Before(until) {
			continue
		}
		if _, ok := byToken[o.InstrumentToken]; !ok {
			tokens = append(tokens, o.InstrumentToken)
		}
		byToken[o.InstrumentToken] = append(byToken[o.InstrumentToken], waiting{i, from})
	}
	if len(tokens) == 0 {
		return nil
	}
	data, err := s.market()
	if err != nil {
		return err
	}
	slices.Sort(tokens)
	for _, token := range tokens {
		group := byToken[token]
		from := group[0].from
		for _, w := range group {
			from = earlier(from, w.from)
		}
		candles, err := data.GetHistoricalData(int(token), "minute", from.In(ist), until.Add(-time.Minute).In(ist), false, false)
		if err != nil {
			return fmt.Errorf("minute candles of %s: %w", s.orders[group[0].index].TradingSymbol, err)
		}
		for _, w := range group {
			s.replayOrder(&s.orders[w.index], w.from, until, candles)
			s.checked[s.orders[w.index].OrderID] = until
		}
	}
	return nil
}

// replayOrder matches o against the candles from from up to until. A limit
// fills when a candle reaches it, at the limit or the candle's open if that
// was better; a stop sets off when a candle reaches its trigger, and an
// SL-M then fills at the trigger or the open if that was worse.
func (s *session) replayOrder(o *kite.Order, from, until time.Time, candles []kite.HistoricalData) {
	before := *o
	buy := o.TransactionType == kite.TransactionTypeBuy
	t := termsOf(*o)
	for _, c := range candles {
		at := c.Date.Time
		if at.Before(from) || !at.Before(until) {
			continue
		}
		if !isOpen(*o) {
			break
		}
		quote := kite.QuoteData{LastPrice: c.Open}
		if o.Status == statusTriggerPending {
			if (buy && c.High < o.TriggerPrice) || (!buy && c.Low > o.TriggerPrice) {
				continue
			}
			if o.OrderType == kite.OrderTypeSLM {
				price := min(o.TriggerPrice, c.Open)
				if buy {
					price = max(o.TriggerPrice, c.Open)
				}
				s.fill(o, int(o.PendingQuantity), s.slipped(o.TransactionType, price, t, quote), at, models.PaperFillTrade)
				continue
			}
			// The limit is matched from the next candle: which came first
			// in this one, the trigger or the limit, is not known.
			o.Status = statusOpen
			o.ExchangeUpdateTimestamp = models.Time{Time: at.In(ist)}
			continue
		}
		switch {
		case o.OrderType == kite.OrderTypeMarket || o.OrderType == kite.OrderTypeSLM:
			s.fill(o, int(o.PendingQuantity), s.slipped(o.TransactionType, c.Open, t, quote), at, models.PaperFillTrade)
		case buy && c.Low <= o.Price:
			s.fill(o, int(o.PendingQuantity), min(o.Price, c.Open), at, models.PaperFillTrade)
		case !buy && c.High >= o.Price:
			s.fill(o, int(o.PendingQuantity), max(o.Price, c.Open), at, models.PaperFillTrade)
		}
	}
	if o.Status != before.Status || o.FilledQuantity != before.FilledQuantity {
		s.update(o)
	}
}

// lastFills is the latest fill of each position, which carries its
// contract terms.
func lastFills(fills []models.PaperFill) map[positionKey]models.PaperFill {
	last := map[positionKey]models.PaperFill{}
	for _, f := range fills {
		last[fillKey(f)] = f
	}
	return last
}

// squareOff ends day's intraday trading at 15:20: open MIS orders are
// cancelled and MIS positions closed, at the live price while the market is
// open and at the 15:19 candle's close after.
func (s *session) squareOff(day time.Time) error {
	at := day.Add(squareOffAt)
	for i := range s.orders {
		if o := &s.orders[i]; isOpen(*o) && o.Product == kite.ProductMIS {
			s.cancel(o, at, "Cancelled by the intraday square-off.")
		}
	}
	live := s.now.Before(day.Add(sessionEnd))
	// What a live square-off closed is gone by the close.
	lots := openAt(s.fills, earlier(s.now, day.Add(sessionEnd)))
	last := lastFills(s.fills)
	for _, key := range sortedKeys(lots) {
		if key.product != kite.ProductMIS {
			continue
		}
		f := last[key]
		var price float64
		filledAt := at
		if live {
			quotes, err := s.quote(string(key.id()))
			q, ok := quotes[string(key.id())]
			if !ok || q.LastPrice <= 0 {
				return fmt.Errorf("square off %s: no live price: %w", key.symbol, err)
			}
			price, filledAt = q.LastPrice, s.now
		} else {
			closed, err := s.closeBefore(f.InstrumentToken, at)
			if err != nil {
				return fmt.Errorf("square off %s: %w", key.symbol, err)
			}
			price = closed
			if price <= 0 {
				// Nothing traded all day after the position opened.
				price = f.Price
			}
		}
		s.systemOrder(key, f, lots[key], price, filledAt, models.PaperFillTrade, "Squared off at 3:20 PM.")
	}
	return nil
}

// closeBefore is the close of the last minute candle of token's day
// before at, or 0 when it did not trade.
func (s *session) closeBefore(token uint32, at time.Time) (float64, error) {
	data, err := s.market()
	if err != nil {
		return 0, err
	}
	day := dayOf(at)
	candles, err := data.GetHistoricalData(int(token), "minute", day.Add(sessionOpen), at.Add(-time.Minute), false, false)
	if err != nil {
		return 0, fmt.Errorf("minute candles: %w", err)
	}
	for _, c := range slices.Backward(candles) {
		if c.Date.Before(at) {
			return c.Close, nil
		}
	}
	return 0, nil
}

// dayClose is token's close on day.
func (s *session) dayClose(token uint32, day time.Time) (float64, error) {
	data, err := s.market()
	if err != nil {
		return 0, err
	}
	candles, err := data.GetHistoricalData(int(token), "day", day, day.Add(sessionEnd), false, false)
	if err != nil {
		return 0, fmt.Errorf("day candle: %w", err)
	}
	for _, c := range slices.Backward(candles) {
		if dayOf(c.Date.Time).Equal(day) && c.Close > 0 {
			return c.Close, nil
		}
	}
	return 0, fmt.Errorf("no close on %s", day.Format(time.DateOnly))
}

// expire settles the contracts expiring on day, in cash, at the close:
// futures at the underlying's close and options at what they are worth
// against it.
func (s *session) expire(day time.Time) error {
	end := day.Add(sessionEnd)
	lots := openAt(s.fills, end)
	last := lastFills(s.fills)
	for _, key := range sortedKeys(lots) {
		f := last[key]
		if f.Expiry.IsZero() || !dayOf(f.Expiry).Equal(day) {
			continue
		}
		price, err := s.settlementPrice(f, day)
		if err != nil {
			return fmt.Errorf("expire %s: %w", key.symbol, err)
		}
		s.systemOrder(key, f, lots[key], price, end, models.PaperFillExpiry, "Settled at expiry.")
	}
	return nil
}

func (s *session) settlementPrice(f models.PaperFill, day time.Time) (float64, error) {
	if f.UnderlyingToken == 0 {
		// Without the underlying, the contract's own close is the nearest.
		return s.dayClose(f.InstrumentToken, day)
	}
	spot, err := s.dayClose(f.UnderlyingToken, day)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", f.Underlying, err)
	}
	switch f.InstrumentType {
	case string(models.OptionTypeCall):
		return round2(max(spot-f.Strike, 0)), nil
	case string(models.OptionTypePut):
		return round2(max(f.Strike-spot, 0)), nil
	}
	return spot, nil
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
