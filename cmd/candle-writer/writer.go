package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/devshoe/gokiteconnect/cmd/ticker-server/producer"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

// finalFlushTimeout bounds the last write after the writer is stopped.
const finalFlushTimeout = 10 * time.Second

type candleStore interface {
	UpsertLiveCandles(context.Context, []repository.LiveCandle) error
}

type candleKey struct {
	id     models.InstrumentID
	minute time.Time
}

// pendingCandles are minute candles folded from ticks and not yet stored,
// in the order they were opened.
type pendingCandles struct {
	order   []candleKey
	candles map[candleKey]*repository.LiveCandle
}

func newPendingCandles() *pendingCandles {
	return &pendingCandles{candles: make(map[candleKey]*repository.LiveCandle)}
}

// add folds a tick into its minute's candle.
func (p *pendingCandles) add(tick producer.Tick) {
	minute := tick.Timestamp.Time.Truncate(time.Minute)
	key := candleKey{id: models.InstrumentID(tick.ID), minute: minute.UTC()}
	volume := int64(tick.VolumeTraded)
	if candle, ok := p.candles[key]; ok {
		candle.High = max(candle.High, tick.LastPrice)
		candle.Low = min(candle.Low, tick.LastPrice)
		candle.Close = tick.LastPrice
		candle.LastVolume = volume
		return
	}
	p.order = append(p.order, key)
	p.candles[key] = &repository.LiveCandle{
		Token: tick.InstrumentToken, InstrumentID: key.id, Minute: minute,
		Open: tick.LastPrice, High: tick.LastPrice, Low: tick.LastPrice, Close: tick.LastPrice,
		FirstVolume: volume, LastVolume: volume,
	}
}

func (p *pendingCandles) len() int { return len(p.order) }

func (p *pendingCandles) list() []repository.LiveCandle {
	candles := make([]repository.LiveCandle, 0, len(p.order))
	for _, key := range p.order {
		candles = append(candles, *p.candles[key])
	}
	return candles
}

func (p *pendingCandles) clear() {
	p.order = p.order[:0]
	clear(p.candles)
}

// runWriter folds ticks into minute candles and stores them every
// flushInterval, or sooner once batchSize ticks have been folded. A failed
// write keeps the candles to try again at the next flush, so a database
// outage loses nothing while the writer runs. It returns when ctx is done,
// after a last write.
func runWriter(ctx context.Context, store candleStore, ticks <-chan producer.Tick, batchSize int, flushInterval time.Duration, logger *slog.Logger) error {
	pending := newPendingCandles()
	folded := 0
	flush := func(ctx context.Context) error {
		if pending.len() == 0 {
			return nil
		}
		candles := pending.list()
		if err := store.UpsertLiveCandles(ctx, candles); err != nil {
			return err
		}
		logger.Info("stored live candles", "candles", len(candles), "ticks", folded)
		pending.clear()
		folded = 0
		return nil
	}
	// A failed write waits for the timer or another batch of ticks, rather
	// than trying again on every tick.
	retry := func(err error) {
		logger.Error("store live candles; retrying at the next flush", "error", err, "candles", pending.len())
		folded = 0
	}

	timer := time.NewTicker(flushInterval)
	defer timer.Stop()
	for {
		select {
		case tick := <-ticks:
			if !tick.InCandle() {
				continue
			}
			pending.add(tick)
			if folded++; folded >= batchSize {
				if err := flush(ctx); err != nil && ctx.Err() == nil {
					retry(err)
				}
			}
		case <-timer.C:
			if err := flush(ctx); err != nil && ctx.Err() == nil {
				retry(err)
			}
		case <-ctx.Done():
			// Ticks already received go into the last write.
			for drained := false; !drained; {
				select {
				case tick := <-ticks:
					if tick.InCandle() {
						pending.add(tick)
						folded++
					}
				default:
					drained = true
				}
			}
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlushTimeout)
			defer cancel()
			return flush(last)
		}
	}
}
