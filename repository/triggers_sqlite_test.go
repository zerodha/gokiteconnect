package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

func TestPriceTriggersFireOnce(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	start := time.Date(2026, 10, 27, 9, 15, 0, 0, time.UTC)
	tick(repo, start)

	once := 1
	above, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: " NSE:INFY ", Condition: " GTE ", Value: 1550, Reference: 1500, Note: " breakout ", FiringBudget: &once})
	if err != nil {
		t.Fatal(err)
	}
	if above.ID == 0 || above.UserID != "CC0006" || above.InstrumentID != "NSE:INFY" || above.Condition != models.TriggerGTE || above.Field != models.FieldLastPrice || above.Action != models.TriggerActionMessage || above.Status != models.TriggerActive || above.Note != "breakout" {
		t.Fatalf("added %+v", above)
	}
	below, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:INFY", Condition: models.TriggerLTE, Value: 1450, Reference: 1500})
	if err != nil {
		t.Fatal(err)
	}

	checks, err := repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NSE:INFY": 1551.5}))
	if err != nil || len(checks) != 2 || !checks[0].Fired || checks[1].Fired {
		t.Fatalf("EvaluatePriceTriggers() = %+v, %v; want the above one fired", checks, err)
	}
	// A second watcher finds it spent: only the below one is checked.
	checks, err = repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NSE:INFY": 1552, "NSE:TCS": 3000}))
	if err != nil || len(checks) != 1 || checks[0].Trigger.ID != below.ID || checks[0].Fired {
		t.Fatalf("second EvaluatePriceTriggers() = %+v, %v; want only the below one checked", checks, err)
	}

	active, err := repo.PriceTriggers(ctx, true)
	if err != nil || len(active) != 1 || active[0].ID != below.ID {
		t.Fatalf("active triggers = %+v, %v; want the below one", active, err)
	}
	all, err := repo.PriceTriggers(ctx, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("triggers = %+v, %v", all, err)
	}
	if got := all[0]; got.UserID != "CC0006" || got.Status != models.TriggerFired || got.LastFiredValue != 1551.5 || got.LastFired.IsZero() ||
		got.TimesFired != 1 || got.TimesEvaluated != 1 || !got.CreatedAt.After(start) {
		t.Fatalf("fired trigger = %+v", got)
	}
	if got := all[1]; !got.LastFired.IsZero() || got.TimesEvaluated != 2 || got.TimesFired != 0 || got.FiringBudget != nil {
		t.Fatalf("active trigger = %+v; want checked twice, never fired, no budget", got)
	}

	if err := repo.DeletePriceTrigger(ctx, below.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeletePriceTrigger(ctx, below.ID); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if active, err := repo.PriceTriggers(ctx, true); err != nil || len(active) != 0 {
		t.Fatalf("after delete active = %+v, %v", active, err)
	}
}

func TestPriceTriggersAreValidated(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	for name, trigger := range map[string]models.PriceTrigger{
		"no instrument":         {Condition: models.TriggerGTE, Value: 10},
		"no condition":          {InstrumentID: "NSE:INFY", Value: 10},
		"sideways":              {InstrumentID: "NSE:INFY", Condition: "across", Value: 10},
		"unknown field":         {InstrumentID: "NSE:INFY", Field: "bid", Condition: models.TriggerGTE, Value: 10},
		"not a number":          {InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: math.NaN()},
		"unknown action":        {InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 10, Action: "call"},
		"basket without orders": {InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 10, Action: models.TriggerActionBasket},
		"message with orders": {InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 10, Action: models.TriggerActionMessage, OrderMode: "paper",
			Orders: []models.BasketItem{{Exchange: "NSE", TradingSymbol: "INFY", TransactionType: "BUY", OrderType: "MARKET", Product: "CNC", Quantity: 1}}},
		"another user":    {UserID: "AB1234", InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 10},
		"negative count":  {InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 10, TimesFired: -1},
		"negative budget": {InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 10, FiringBudget: new(-1)},
	} {
		if _, err := repo.AddPriceTrigger(ctx, trigger); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("%s: AddPriceTrigger() error = %v, want invalid input", name, err)
		}
	}
}

func TestPriceTriggersLiveInTheUsersDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "CC0006.db")
	repo := openPortfolio(t, ctx, path, "CC0006")
	if _, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 1550}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openPortfolio(t, ctx, path, "CC0006")
	if active, err := reopened.PriceTriggers(ctx, true); err != nil || len(active) != 1 || active[0].Value != 1550 {
		t.Fatalf("reopened triggers = %+v, %v", active, err)
	}
}

func TestPriceTriggerConditions(t *testing.T) {
	on := func(condition string) models.PriceTrigger {
		return models.PriceTrigger{Condition: condition, Value: 100}
	}
	for _, test := range []struct {
		condition string
		previous  *float64
		value     float64
		want      bool
	}{
		{models.TriggerGTE, nil, 99.95, false}, {models.TriggerGTE, nil, 100, true}, {models.TriggerGTE, nil, 101, true},
		{models.TriggerGT, nil, 100, false}, {models.TriggerGT, nil, 100.05, true},
		{models.TriggerLT, nil, 100, false}, {models.TriggerLT, nil, 99.95, true},
		{models.TriggerLTE, nil, 100.05, false}, {models.TriggerLTE, nil, 100, true}, {models.TriggerLTE, nil, -5, true},
		{models.TriggerEQ, nil, 100, true}, {models.TriggerEQ, nil, 100.05, false},
		// A crossing needs the value before on the other side.
		{models.TriggerCrossAbove, nil, 101, false}, {models.TriggerCrossAbove, new(99.0), 100, true},
		{models.TriggerCrossAbove, new(100.0), 101, false}, {models.TriggerCrossAbove, new(99.0), 99.5, false},
		{models.TriggerCrossBelow, nil, 99, false}, {models.TriggerCrossBelow, new(101.0), 100, true},
		{models.TriggerCrossBelow, new(100.0), 99, false},
	} {
		if got := on(test.condition).Holds(test.value, test.previous); got != test.want {
			t.Errorf("%s 100 at %v (before %v): Holds = %v, want %v", test.condition, test.value, test.previous, got, test.want)
		}
	}
}

func TestTickFieldsAreWorkedOutFromTheTick(t *testing.T) {
	tick := models.Tick{LastPrice: 105, LastTradedQuantity: 25, AverageTradePrice: 103.5, VolumeTraded: 1200, TotalBuyQuantity: 300,
		TotalSellQuantity: 400, OI: 900, OIDayHigh: 950, OIDayLow: 850, OHLC: models.OHLC{Open: 102, High: 106, Low: 101, Close: 100}}
	want := map[models.TickField]float64{
		models.FieldLastPrice: 105, models.FieldHigh: 106, models.FieldLow: 101, models.FieldOpen: 102, models.FieldClose: 100,
		models.FieldDayChange: 5, models.FieldDayChangePercent: 5, models.FieldIntradayChange: 3, models.FieldIntradayChangePercent: 3.0 / 102 * 100,
		models.FieldLastTradedQty: 25, models.FieldAvgTradedPrice: 103.5, models.FieldVolume: 1200, models.FieldTotalBuyQty: 300,
		models.FieldTotalSellQty: 400, models.FieldOI: 900, models.FieldOIDayHigh: 950, models.FieldOIDayLow: 850,
	}
	if got := tick.Values(); len(got) != len(models.TickFields) || !maps.EqualFunc(got, want, func(x, y float64) bool { return math.Abs(x-y) < 1e-9 }) {
		t.Fatalf("Values() = %v, want %v", got, want)
	}
	// Without a close or open, the changes are unknown; without a price,
	// everything is.
	tick.OHLC = models.OHLC{}
	if _, ok := tick.Value(models.FieldDayChange); ok {
		t.Error("day change without a close")
	}
	if _, ok := tick.Value(models.FieldIntradayChangePercent); ok {
		t.Error("intraday change without an open")
	}
	if values := (models.Tick{VolumeTraded: 10}).Values(); len(values) != 0 {
		t.Errorf("a tick without a price has %v", values)
	}
}

// lastPrices are ticks with only last prices.
func lastPrices(prices map[models.InstrumentID]float64) map[models.InstrumentID]models.Tick {
	ticks := make(map[models.InstrumentID]models.Tick, len(prices))
	for id, price := range prices {
		ticks[id] = models.Tick{LastPrice: price}
	}
	return ticks
}

func TestPriceTriggersGainTheirUser(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "CC0006.db")
	// A database from before triggers named their user.
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE price_triggers (
		trigger_id    INTEGER PRIMARY KEY AUTOINCREMENT,
		instrument_id TEXT NOT NULL,
		direction     TEXT NOT NULL,
		price         REAL NOT NULL,
		reference     REAL NOT NULL,
		note          TEXT NOT NULL,
		status        TEXT NOT NULL,
		created_at    DATETIME NOT NULL,
		fired_at      DATETIME,
		fired_price   REAL NOT NULL DEFAULT 0
	);
	INSERT INTO price_triggers (instrument_id, direction, price, reference, note, status, created_at)
		VALUES ('NSE:INFY', 'above', 1550, 1500, '', 'active', '2026-10-27 09:15:00');`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	repo := openPortfolio(t, ctx, path, "cc0006")
	if _, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{UserID: "cc0006", InstrumentID: "NSE:TCS", Condition: models.TriggerLTE, Value: 3000}); err != nil {
		t.Fatal(err)
	}
	triggers, err := repo.PriceTriggers(ctx, true)
	if err != nil || len(triggers) != 2 || triggers[0].UserID != "CC0006" || triggers[1].UserID != "CC0006" {
		t.Fatalf("triggers = %+v, %v; want both CC0006's", triggers, err)
	}
	// Opening it again leaves the column as it is.
	_ = repo.Close()
	if again := openPortfolio(t, ctx, path, "CC0006"); again == nil {
		t.Fatal("reopen failed")
	}
}

func TestPriceTriggersKeepTheirOrders(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	orders := []models.BasketItem{
		{Exchange: "NFO", TradingSymbol: "NIFTY26NOV25000CE", TransactionType: "SELL", OrderType: "MARKET", Product: "NRML", Quantity: 75},
		{Exchange: "NFO", TradingSymbol: "NIFTY26NOV25500CE", TransactionType: "BUY", OrderType: "LIMIT", Product: "NRML", Quantity: 75, Price: 42.5},
	}
	trigger, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:NIFTY 50", Condition: models.TriggerGTE, Value: 25100,
		Orders: orders, OrderMode: " Live "})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.OrderMode != "live" {
		t.Fatalf("mode %q", trigger.OrderMode)
	}
	results := []models.TriggerOrderResult{{OrderID: "2610290001"}, {Error: "insufficient margin"}}
	if checks, err := repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NSE:NIFTY 50": 25110})); err != nil || len(checks) != 1 || !checks[0].Fired {
		t.Fatalf("EvaluatePriceTriggers() = %+v, %v", checks, err)
	}
	if err := repo.RecordTriggerOrders(ctx, trigger.ID, results); err != nil {
		t.Fatal(err)
	}
	all, err := repo.PriceTriggers(ctx, false)
	if err != nil || len(all) != 1 {
		t.Fatalf("triggers = %+v, %v", all, err)
	}
	got := all[0]
	if len(got.Orders) != 2 || got.Orders[1] != orders[1] || got.OrderMode != "live" || len(got.OrderResults) != 2 || got.OrderResults[1] != results[1] {
		t.Fatalf("stored %+v", got)
	}

	// An order needs its parameters and a mode.
	for _, bad := range []models.PriceTrigger{
		{InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 1, Orders: []models.BasketItem{{Exchange: "NSE"}}, OrderMode: "paper"},
		{InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 1, Orders: orders[:1]},
	} {
		if _, err := repo.AddPriceTrigger(ctx, bad); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("AddPriceTrigger(%+v) = %v; want invalid input", bad, err)
		}
	}
	// One without orders keeps none.
	plain, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:INFY", Condition: models.TriggerLTE, Value: 1400, OrderMode: "live"})
	if err != nil || plain.OrderMode != "" || len(plain.Orders) != 0 {
		t.Fatalf("plain trigger %+v, %v", plain, err)
	}
}

func TestOldPriceTriggersMoveToJSON(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "CC0006.db")
	// A database from before triggers were JSON: one fired with an order,
	// one active.
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE price_triggers (
		trigger_id    INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id       TEXT NOT NULL,
		instrument_id TEXT NOT NULL,
		direction     TEXT NOT NULL,
		price         REAL NOT NULL,
		reference     REAL NOT NULL,
		note          TEXT NOT NULL,
		status        TEXT NOT NULL,
		created_at    DATETIME NOT NULL,
		fired_at      DATETIME,
		fired_price   REAL NOT NULL DEFAULT 0,
		orders        TEXT NOT NULL DEFAULT '',
		order_mode    TEXT NOT NULL DEFAULT '',
		order_results TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO price_triggers (trigger_id, user_id, instrument_id, direction, price, reference, note, status, created_at, fired_at, fired_price, orders, order_mode, order_results)
		VALUES (3, 'CC0006', 'NSE:INFY', 'above', 1550, 1500, 'breakout', 'fired', '2026-10-27 09:15:00', '2026-10-28 10:00:00', 1551,
			'[{"Exchange":"NSE","TradingSymbol":"INFY","TransactionType":"BUY","OrderType":"MARKET","Product":"CNC","Quantity":1}]', 'paper', '[{"order_id":"P1"}]'),
		(7, 'CC0006', 'NSE:NIFTY 50', 'below', 23000, 23300, '', 'active', '2026-11-02 09:15:00', NULL, 0, '', '', '');`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	repo := openPortfolio(t, ctx, path, "CC0006")
	all, err := repo.PriceTriggers(ctx, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("triggers = %+v, %v", all, err)
	}
	fired, active := all[0], all[1]
	if fired.ID != 3 || fired.Status != models.TriggerFired || fired.TimesFired != 1 || fired.FiringBudget == nil || *fired.FiringBudget != 1 ||
		fired.LastFiredValue != 1551 || fired.LastFired.IsZero() || fired.Note != "breakout" || len(fired.Orders) != 1 || fired.Orders[0].Product != "CNC" ||
		fired.OrderMode != "paper" || len(fired.OrderResults) != 1 || fired.OrderResults[0].OrderID != "P1" {
		t.Fatalf("moved fired trigger = %+v", fired)
	}
	if active.ID != 7 || active.Status != models.TriggerActive || active.TimesFired != 0 || active.FiringBudget == nil || *active.FiringBudget != 1 || !active.LastFired.IsZero() {
		t.Fatalf("moved active trigger = %+v", active)
	}
	// The old table is gone, and new triggers number on from the moved.
	var tables int
	if err := repo.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'price_triggers'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("price_triggers tables = %d, %v", tables, err)
	}
	added, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:TCS", Condition: models.TriggerLTE, Value: 3000})
	if err != nil || added.ID != 8 {
		t.Fatalf("added %+v, %v; want ID 8", added, err)
	}
	_ = repo.Close()
	if again := openPortfolio(t, ctx, path, "CC0006"); again == nil {
		t.Fatal("reopen failed")
	}
}

func TestPriceOnlyTriggerDocumentsReadAsConditions(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, filepath.Join(t.TempDir(), "CC0006.db"), "CC0006")
	// Documents from before fields, conditions and actions: a below with
	// an order, and an above without.
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO triggers (trigger_id, user_id, status, data) VALUES
		(1, 'CC0006', 'active', '{"user_id":"CC0006","instrument_id":"NSE:INFY","direction":"below","price":1400,"reference":1500,"status":"active","last_fired_price":1399,"orders":[{"Exchange":"NSE","TradingSymbol":"INFY","TransactionType":"BUY","OrderType":"MARKET","Product":"CNC","Quantity":1}]}'),
		(2, 'CC0006', 'active', '{"user_id":"CC0006","instrument_id":"NSE:TCS","direction":"above","price":3100,"reference":3000,"status":"active"}')`); err != nil {
		t.Fatal(err)
	}
	all, err := repo.PriceTriggers(ctx, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("triggers = %+v, %v", all, err)
	}
	below, above := all[0], all[1]
	if below.Field != models.FieldLastPrice || below.Condition != models.TriggerLTE || below.Value != 1400 || below.LastFiredValue != 1399 ||
		below.Action != models.TriggerActionBoth || !below.PlacesOrders() || below.Extra != nil {
		t.Fatalf("below = %+v", below)
	}
	if above.Field != models.FieldLastPrice || above.Condition != models.TriggerGTE || above.Value != 3100 ||
		above.Action != models.TriggerActionMessage || above.PlacesOrders() || !above.SendsMessage() {
		t.Fatalf("above = %+v", above)
	}
}

func TestPriceTriggersFireOncePerCrossingWithinTheirBudget(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	tick(repo, time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC))
	twice := 2
	limited, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 1550, FiringBudget: &twice})
	if err != nil {
		t.Fatal(err)
	}
	unlimited, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 1550})
	if err != nil {
		t.Fatal(err)
	}
	// Up through 1550, staying above, back below, up again, below, up.
	var firings []string
	for _, ltp := range []float64{1540, 1555, 1560, 1545, 1551, 1549, 1552} {
		checks, err := repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NSE:INFY": ltp}))
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range checks {
			if check.Fired {
				firings = append(firings, fmt.Sprintf("%d@%v", check.Trigger.ID, ltp))
			}
		}
	}
	want := fmt.Sprintf("[%[1]d@1555 %[2]d@1555 %[1]d@1551 %[2]d@1551 %[2]d@1552]", limited.ID, unlimited.ID)
	if got := fmt.Sprint(firings); got != want {
		t.Fatalf("firings %s, want %s", got, want)
	}
	all, err := repo.PriceTriggers(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	// The limited one stopped being checked once spent.
	if got := all[0]; got.Status != models.TriggerFired || got.TimesFired != 2 || got.TimesEvaluated != 5 || got.LastFiredValue != 1551 {
		t.Fatalf("limited = %+v", got)
	}
	if got := all[1]; got.Status != models.TriggerActive || got.TimesFired != 3 || got.TimesEvaluated != 7 || got.LastFiredValue != 1552 || !got.Tripped {
		t.Fatalf("unlimited = %+v", got)
	}

	// Raising the budget makes it active again, but it waits for the next
	// crossing.
	raised, err := repo.UpdatePriceTrigger(ctx, limited.ID, func(trigger *models.PriceTrigger) error {
		trigger.FiringBudget = new(3)
		return nil
	})
	if err != nil || raised.Status != models.TriggerActive {
		t.Fatalf("raised %+v, %v", raised, err)
	}
	checks, err := repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NSE:INFY": 1553}))
	if err != nil || len(checks) != 2 || checks[0].Fired {
		t.Fatalf("checks after raising = %+v, %v; want it waiting", checks, err)
	}
}

func TestPriceTriggerUpdatesKeepWhatTheyDoNotEdit(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	trigger, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NSE:INFY", Condition: models.TriggerGTE, Value: 1550,
		Extra: map[string]json.RawMessage{"cooldown": json.RawMessage(`"5m"`)}})
	if err != nil {
		t.Fatal(err)
	}
	// A key this version does not know is kept in the document.
	var data string
	if err := repo.db.QueryRowContext(ctx, "SELECT data FROM triggers WHERE trigger_id = ?", trigger.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(data), &document); err != nil || document["cooldown"] != "5m" || document["firing_budget"] != nil || document["times_evaluated"] != 0.0 {
		t.Fatalf("document %s, %v", data, err)
	}
	if _, ok := document["firing_budget"]; !ok {
		t.Fatalf("document %s has no firing_budget", data)
	}

	updated, err := repo.UpdatePriceTrigger(ctx, trigger.ID, func(trigger *models.PriceTrigger) error {
		trigger.ID, trigger.UserID = 99, "AB1234"
		trigger.TimesEvaluated, trigger.TimesFired, trigger.Note = 40, 2, " edited "
		trigger.LastFired = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
		return nil
	})
	if err != nil || updated.ID != trigger.ID || updated.UserID != "CC0006" || updated.Note != "edited" || updated.TimesEvaluated != 40 || updated.TimesFired != 2 {
		t.Fatalf("updated %+v, %v", updated, err)
	}
	all, err := repo.PriceTriggers(ctx, false)
	if err != nil || len(all) != 1 || all[0].TimesFired != 2 || !all[0].LastFired.Equal(updated.LastFired) || string(all[0].Extra["cooldown"]) != `"5m"` {
		t.Fatalf("stored %+v, %v", all, err)
	}
	if _, err := repo.UpdatePriceTrigger(ctx, trigger.ID, func(trigger *models.PriceTrigger) error {
		trigger.TimesFired = -1
		return nil
	}); !errors.Is(err, models.ErrInvalidInput) {
		t.Fatalf("negative count update = %v", err)
	}
	if _, err := repo.UpdatePriceTrigger(ctx, 404, func(*models.PriceTrigger) error { return nil }); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("missing trigger update = %v", err)
	}
}

func TestPriceTriggerExitsWaitForTheirEntryAndCancelEachOther(t *testing.T) {
	ctx := context.Background()
	repo := openPortfolio(t, ctx, ":memory:", "CC0006")
	once := 1
	exit := func(kind, condition string, value float64) models.PriceTrigger {
		trigger, err := repo.AddPriceTrigger(ctx, models.PriceTrigger{InstrumentID: "NFO:NIFTYW125000CE", Condition: condition, Value: value, Reference: 110,
			FiringBudget: &once, OCO: "order:2510120001", Entry: "2510120001", Exit: kind})
		if err != nil {
			t.Fatal(err)
		}
		return trigger
	}
	stop, target := exit(models.TriggerStoploss, models.TriggerLTE, 100), exit(models.TriggerTarget, models.TriggerGTE, 130)

	// Waiting for the entry to fill, neither is checked.
	checks, err := repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NFO:NIFTYW125000CE": 95}))
	if err != nil || len(checks) != 0 {
		t.Fatalf("EvaluatePriceTriggers() = %+v, %v; want nothing checked", checks, err)
	}
	for _, trigger := range []models.PriceTrigger{stop, target} {
		if _, err := repo.UpdatePriceTrigger(ctx, trigger.ID, func(trigger *models.PriceTrigger) error { trigger.Entry = ""; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// Armed, the stop fires and deletes the target in the same check.
	checks, err = repo.EvaluatePriceTriggers(ctx, lastPrices(map[models.InstrumentID]float64{"NFO:NIFTYW125000CE": 99}))
	if err != nil || len(checks) != 1 || checks[0].Trigger.ID != stop.ID || !checks[0].Fired || len(checks[0].Cancelled) != 1 || checks[0].Cancelled[0] != target.ID {
		t.Fatalf("EvaluatePriceTriggers() = %+v, %v; want the stop fired and the target cancelled", checks, err)
	}
	all, err := repo.PriceTriggers(ctx, false)
	if err != nil || len(all) != 1 || all[0].ID != stop.ID || all[0].Status != models.TriggerFired || all[0].Exit != models.TriggerStoploss || all[0].OCO != "order:2510120001" {
		t.Fatalf("triggers = %+v, %v; want the fired stop", all, err)
	}
}
