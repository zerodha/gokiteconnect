package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devshoe/gokiteconnect/models"
)

// Each trigger is one JSON document in triggers.data; user_id and status
// are copied out of it to query by.

// migrateTriggers moves the price_triggers of a database made before
// triggers were JSON, a column per field, into triggers, then drops it.
// Their triggers fired once, so each gets a firing budget of one.
func migrateTriggers(ctx context.Context, db *sql.DB, user string) error {
	var has int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'price_triggers'",
	).Scan(&has); err != nil {
		return fmt.Errorf("read trigger tables: %w", err)
	}
	if has == 0 {
		return nil
	}
	if err := addTriggerUsers(ctx, db, user); err != nil {
		return err
	}
	if err := addTriggerOrders(ctx, db); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin moving triggers: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT trigger_id, user_id, instrument_id, direction, price, reference, note, status, created_at,
		fired_at, fired_price, orders, order_mode, order_results FROM price_triggers ORDER BY trigger_id`)
	if err != nil {
		return fmt.Errorf("read old triggers: %w", err)
	}
	var triggers []models.PriceTrigger
	for rows.Next() {
		var trigger models.PriceTrigger
		var instrumentID, direction, orders, results string
		var firedAt sql.NullTime
		if err := rows.Scan(&trigger.ID, &trigger.UserID, &instrumentID, &direction, &trigger.Value, &trigger.Reference,
			&trigger.Note, &trigger.Status, &trigger.CreatedAt, &firedAt, &trigger.LastFiredValue, &orders, &trigger.OrderMode, &results); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan old trigger: %w", err)
		}
		trigger.InstrumentID, trigger.LastFired = models.InstrumentID(instrumentID), firedAt.Time
		// They were on the last price, above as at or above and below as at
		// or below.
		trigger.Field, trigger.Condition = models.FieldLastPrice, models.TriggerGTE
		if direction == "below" {
			trigger.Condition = models.TriggerLTE
		}
		once := 1
		trigger.FiringBudget = &once
		if trigger.Status == models.TriggerFired {
			trigger.TimesFired, trigger.Tripped = 1, true
		}
		if trigger.Orders, err = decodeJSON[models.BasketItem](orders); err == nil {
			trigger.OrderResults, err = decodeJSON[models.TriggerOrderResult](results)
		}
		trigger.Action = models.TriggerActionMessage
		if len(trigger.Orders) > 0 {
			trigger.Action = models.TriggerActionBoth
		}
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("old trigger %d: %w", trigger.ID, err)
		}
		triggers = append(triggers, trigger)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read old triggers: %w", err)
	}
	for _, trigger := range triggers {
		data, err := json.Marshal(trigger)
		if err != nil {
			return fmt.Errorf("encode old trigger %d: %w", trigger.ID, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO triggers (trigger_id, user_id, status, data) VALUES (?, ?, ?, ?)",
			trigger.ID, trigger.UserID, trigger.Status, string(data)); err != nil {
			return fmt.Errorf("move trigger %d: %w", trigger.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE price_triggers"); err != nil {
		return fmt.Errorf("drop old triggers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit moving triggers: %w", err)
	}
	return nil
}

// addTriggerUsers adds user_id to the price_triggers of a database made
// before triggers named their user, filling it with the database's user.
func addTriggerUsers(ctx context.Context, db *sql.DB, user string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin adding trigger users: %w", err)
	}
	defer tx.Rollback()
	var has int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM pragma_table_info('price_triggers') WHERE name = 'user_id'",
	).Scan(&has); err != nil {
		return fmt.Errorf("read trigger columns: %w", err)
	}
	if has > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE price_triggers ADD COLUMN user_id TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add trigger users: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE price_triggers SET user_id = ?", user); err != nil {
		return fmt.Errorf("fill trigger users: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trigger users: %w", err)
	}
	return nil
}

// addTriggerOrders adds the order columns to the price_triggers of a
// database made before triggers placed orders.
func addTriggerOrders(ctx context.Context, db *sql.DB) error {
	for _, column := range []string{"orders", "order_mode", "order_results"} {
		var has int
		if err := db.QueryRowContext(ctx,
			"SELECT count(*) FROM pragma_table_info('price_triggers') WHERE name = ?", column,
		).Scan(&has); err != nil {
			return fmt.Errorf("read trigger columns: %w", err)
		}
		if has > 0 {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE price_triggers ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add trigger %s: %w", column, err)
		}
	}
	return nil
}

// decodeJSON reads a list the old price_triggers kept as JSON, "" when
// empty.
func decodeJSON[T any](text string) ([]T, error) {
	if text == "" {
		return nil, nil
	}
	var values []T
	err := json.Unmarshal([]byte(text), &values)
	return values, err
}

// triggerQuerier is a database or a transaction to read triggers from.
type triggerQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readTriggers reads the triggers a query of trigger_id and data returns.
func (r *PortfolioSQLiteRepository) readTriggers(ctx context.Context, db triggerQuerier, query string, args ...any) ([]models.PriceTrigger, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("portfolio %s: read triggers: %w", r.userID, err)
	}
	defer rows.Close()
	var triggers []models.PriceTrigger
	for rows.Next() {
		var id int64
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("portfolio %s: scan trigger: %w", r.userID, err)
		}
		var trigger models.PriceTrigger
		if err := json.Unmarshal([]byte(data), &trigger); err != nil {
			return nil, fmt.Errorf("portfolio %s: trigger %d: %w", r.userID, id, err)
		}
		trigger.ID = id
		triggers = append(triggers, trigger)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("portfolio %s: read triggers: %w", r.userID, err)
	}
	return triggers, nil
}

// writeTrigger stores trigger over the row with its ID.
func (r *PortfolioSQLiteRepository) writeTrigger(ctx context.Context, tx *sql.Tx, trigger models.PriceTrigger) error {
	data, err := json.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("portfolio %s: encode trigger %d: %w", r.userID, trigger.ID, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE triggers SET user_id = ?, status = ?, data = ? WHERE trigger_id = ?",
		trigger.UserID, trigger.Status, string(data), trigger.ID); err != nil {
		return fmt.Errorf("portfolio %s: store trigger %d: %w", r.userID, trigger.ID, err)
	}
	return nil
}

// AddPriceTrigger stores trigger as an active price alert of the
// repository's user and returns it with its user, ID and creation time. A
// trigger naming another user is refused. Its counts are kept, so one can
// be copied with them; its firing budget nil is no limit.
func (r *PortfolioSQLiteRepository) AddPriceTrigger(ctx context.Context, trigger models.PriceTrigger) (models.PriceTrigger, error) {
	if err := trigger.Validate(); err != nil {
		return models.PriceTrigger{}, err
	}
	if user := strings.TrimSpace(trigger.UserID); user != "" && !strings.EqualFold(user, r.userID) {
		return models.PriceTrigger{}, fmt.Errorf("%w: trigger for %s stored in %s's portfolio", models.ErrInvalidInput, user, r.userID)
	}
	trigger.UserID = r.userID
	trigger.Note = strings.TrimSpace(trigger.Note)
	trigger.CreatedAt = r.now().UTC()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return models.PriceTrigger{}, fmt.Errorf("portfolio %s: begin storing trigger: %w", r.userID, err)
	}
	defer tx.Rollback()
	// The document names its ID, which the row gets first.
	if err := tx.QueryRowContext(ctx, "INSERT INTO triggers (user_id, status, data) VALUES (?, ?, '{}') RETURNING trigger_id",
		trigger.UserID, trigger.Status,
	).Scan(&trigger.ID); err != nil {
		return models.PriceTrigger{}, fmt.Errorf("portfolio %s: store trigger on %s: %w", r.userID, trigger.InstrumentID, err)
	}
	if err := r.writeTrigger(ctx, tx, trigger); err != nil {
		return models.PriceTrigger{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.PriceTrigger{}, fmt.Errorf("portfolio %s: commit trigger: %w", r.userID, err)
	}
	return trigger, nil
}

// PriceTriggers returns the price alerts, oldest first: only the active
// ones when activeOnly is set.
func (r *PortfolioSQLiteRepository) PriceTriggers(ctx context.Context, activeOnly bool) ([]models.PriceTrigger, error) {
	query := "SELECT trigger_id, data FROM triggers"
	var args []any
	if activeOnly {
		query += " WHERE status = ?"
		args = append(args, models.TriggerActive)
	}
	return r.readTriggers(ctx, r.db, query+" ORDER BY trigger_id", args...)
}

// UpdatePriceTrigger applies edit to trigger id as stored, checks the
// result and stores it, keeping the trigger's ID, user and creation time.
func (r *PortfolioSQLiteRepository) UpdatePriceTrigger(ctx context.Context, id int64, edit func(*models.PriceTrigger) error) (models.PriceTrigger, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return models.PriceTrigger{}, fmt.Errorf("portfolio %s: begin updating trigger %d: %w", r.userID, id, err)
	}
	defer tx.Rollback()
	found, err := r.readTriggers(ctx, tx, "SELECT trigger_id, data FROM triggers WHERE trigger_id = ?", id)
	if err != nil {
		return models.PriceTrigger{}, err
	}
	if len(found) == 0 {
		return models.PriceTrigger{}, fmt.Errorf("%w: trigger %d", models.ErrNotFound, id)
	}
	trigger := found[0]
	if err := edit(&trigger); err != nil {
		return models.PriceTrigger{}, err
	}
	trigger.ID, trigger.UserID, trigger.CreatedAt = found[0].ID, found[0].UserID, found[0].CreatedAt
	trigger.Note = strings.TrimSpace(trigger.Note)
	if err := trigger.Validate(); err != nil {
		return models.PriceTrigger{}, err
	}
	if err := r.writeTrigger(ctx, tx, trigger); err != nil {
		return models.PriceTrigger{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.PriceTrigger{}, fmt.Errorf("portfolio %s: commit trigger %d: %w", r.userID, id, err)
	}
	return trigger, nil
}

// EvaluatePriceTriggers checks each active trigger whose instrument ticks
// has a tick with the trigger's field for, counting the check and firing
// it when its condition comes to hold, and returns them as they now are.
// Triggers waiting on their entry order are not checked, and one firing
// deletes the others in its OCO group. Checks run in one transaction, so
// when two processes check the same trigger, only one of them fires it.
func (r *PortfolioSQLiteRepository) EvaluatePriceTriggers(ctx context.Context, ticks map[models.InstrumentID]models.Tick) ([]models.TriggerEvaluation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("portfolio %s: begin checking triggers: %w", r.userID, err)
	}
	defer tx.Rollback()
	active, err := r.readTriggers(ctx, tx, "SELECT trigger_id, data FROM triggers WHERE status = ? ORDER BY trigger_id", models.TriggerActive)
	if err != nil {
		return nil, err
	}
	now := r.now()
	var evaluated []models.TriggerEvaluation
	// cancelled are the triggers a firing in their OCO group deleted, which
	// are not checked after.
	cancelled := map[int64]bool{}
	for _, trigger := range active {
		tick, ok := ticks[trigger.InstrumentID]
		if _, valued := tick.Value(trigger.Field); !ok || !valued || trigger.Entry != "" || cancelled[trigger.ID] {
			continue
		}
		fired := trigger.Evaluate(tick, now)
		if err := r.writeTrigger(ctx, tx, trigger); err != nil {
			return nil, err
		}
		evaluation := models.TriggerEvaluation{Trigger: trigger, Fired: fired}
		if fired && trigger.OCO != "" {
			for _, other := range active {
				if other.ID == trigger.ID || other.OCO != trigger.OCO || cancelled[other.ID] {
					continue
				}
				if _, err := tx.ExecContext(ctx, "DELETE FROM triggers WHERE trigger_id = ?", other.ID); err != nil {
					return nil, fmt.Errorf("portfolio %s: cancel trigger %d: %w", r.userID, other.ID, err)
				}
				cancelled[other.ID] = true
				evaluation.Cancelled = append(evaluation.Cancelled, other.ID)
			}
		}
		evaluated = append(evaluated, evaluation)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("portfolio %s: commit trigger checks: %w", r.userID, err)
	}
	return evaluated, nil
}

// RecordTriggerOrders stores how placing trigger id's orders went the
// last time it fired.
func (r *PortfolioSQLiteRepository) RecordTriggerOrders(ctx context.Context, id int64, results []models.TriggerOrderResult) error {
	_, err := r.UpdatePriceTrigger(ctx, id, func(trigger *models.PriceTrigger) error {
		trigger.OrderResults = results
		return nil
	})
	return err
}

// DeletePriceTrigger deletes trigger id. Deleting one already gone does
// nothing.
func (r *PortfolioSQLiteRepository) DeletePriceTrigger(ctx context.Context, id int64) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM triggers WHERE trigger_id = ?", id); err != nil {
		return fmt.Errorf("portfolio %s: delete trigger %d: %w", r.userID, id, err)
	}
	return nil
}
