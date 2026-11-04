package models

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Trigger conditions: how a trigger's field must stand against its value.
// The levels hold while the field is past the value; a trigger on one
// fires when it comes to hold, and again only once it has stopped holding.
// The crossings hold at a check where the field went from one side of the
// value, at the check before, to the other, so they never fire on the
// first check.
const (
	TriggerGTE         = "gte"
	TriggerGT          = "gt"
	TriggerLT          = "lt"
	TriggerLTE         = "lte"
	TriggerEQ          = "eq"
	TriggerCrossAbove  = "ca"
	TriggerCrossBelow  = "cb"
	legacyTriggerAbove = "above"
	legacyTriggerBelow = "below"
)

// TriggerConditions are the conditions, in the order they are offered.
var TriggerConditions = []string{TriggerGTE, TriggerGT, TriggerLT, TriggerLTE, TriggerEQ, TriggerCrossAbove, TriggerCrossBelow}

// Trigger actions: what a trigger does when it fires. Each firing is also
// shown wherever it is checked.
const (
	// TriggerActionMessage sends a message, on Telegram when it is set up.
	TriggerActionMessage = "message"
	// TriggerActionBasket places the trigger's orders.
	TriggerActionBasket = "basket"
	// TriggerActionBoth does both.
	TriggerActionBoth = "both"
)

// The exits a trigger can be to the order it guards.
const (
	TriggerStoploss = "stoploss"
	TriggerTarget   = "target"
)

// TriggerActions are the actions, in the order they are offered.
var TriggerActions = []string{TriggerActionMessage, TriggerActionBasket, TriggerActionBoth}

// Price trigger states. An active trigger can fire; a fired one has fired
// as many times as its budget allows, and stays fired until the budget is
// raised.
const (
	TriggerActive = "active"
	TriggerFired  = "fired"
)

// PriceTrigger is an alert on one of an instrument's tick fields: WHEN
// Field stands against Value as Condition says, THEN it does Action. It is
// kept as one JSON document, so fields can be added without changing the
// database, and keys a version does not know are kept in Extra and
// written back as they were.
type PriceTrigger struct {
	ID int64 `json:"id"`
	// UserID is the user who set the trigger, in whose portfolio it is
	// kept.
	UserID       string       `json:"user_id"`
	InstrumentID InstrumentID `json:"instrument_id"`
	// Field is the tick field watched, the last price unless set.
	Field TickField `json:"field"`
	// Condition is one of TriggerConditions.
	Condition string  `json:"condition"`
	Value     float64 `json:"value"`
	// Action is one of TriggerActions: a message unless the trigger has
	// orders, both when it has.
	Action string `json:"action"`
	// Reference is the field's value when the trigger was set, which the
	// trigger's move is measured from.
	Reference float64   `json:"reference,omitempty"`
	Note      string    `json:"note,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// TimesEvaluated counts the prices the trigger was checked against,
	// and TimesFired the times it fired.
	TimesEvaluated int `json:"times_evaluated"`
	TimesFired     int `json:"times_fired"`
	// FiringBudget is how many times the trigger may fire; nil is no
	// limit.
	FiringBudget *int `json:"firing_budget"`
	// LastFired and LastFiredValue are when the trigger last fired and
	// the field's value that fired it, once it has.
	LastFired      time.Time `json:"last_fired,omitzero"`
	LastFiredValue float64   `json:"last_fired_value,omitempty"`
	// LastValue is the field's value at the last check, which a crossing
	// is measured from; nil before the first.
	LastValue *float64 `json:"last_value,omitempty"`
	// Tripped is set when the trigger fires and cleared once its
	// condition no longer holds: it fires once each time the condition
	// comes to hold, not at every check while it does.
	Tripped bool `json:"tripped,omitempty"`
	// Orders are the basket placed into the user's account each time the
	// trigger fires, when its action places one, buys first, in
	// OrderMode: "paper" or "live", the account it was set in.
	Orders    []BasketItem `json:"orders,omitempty"`
	OrderMode string       `json:"order_mode,omitempty"`
	// OrderResults are how placing each of Orders went, in their order,
	// the last time the trigger fired.
	OrderResults []TriggerOrderResult `json:"order_results,omitempty"`
	// OCO groups triggers that are one-cancels-other: when one fires, the
	// others with the same OCO are deleted, as a stoploss and target are.
	OCO string `json:"oco,omitempty"`
	// Entry is the order whose fill arms the trigger, such as the order a
	// stoploss guards. While it is set the trigger is not checked.
	Entry string `json:"entry,omitempty"`
	// Exit is what the trigger is to the order it guards: TriggerStoploss
	// or TriggerTarget, or empty for an alert of its own.
	Exit string `json:"exit,omitempty"`
	// Extra are the document's keys this version does not know.
	Extra map[string]json.RawMessage `json:"-"`
}

// TriggerOrderResult is how placing one of a trigger's orders went: the
// order's ID, or why it failed.
type TriggerOrderResult struct {
	OrderID string `json:"order_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// TriggerEvaluation is a trigger after it was checked against a tick, and
// whether that check fired it.
type TriggerEvaluation struct {
	Trigger PriceTrigger
	Fired   bool
	// Cancelled are the IDs of the triggers in its OCO group that firing
	// deleted.
	Cancelled []int64
}

// priceTriggerFields is PriceTrigger without its JSON methods.
type priceTriggerFields PriceTrigger

// priceTriggerKeys are the JSON keys PriceTrigger's fields take.
var priceTriggerKeys = func() map[string]bool {
	keys := map[string]bool{}
	fields := reflect.TypeFor[priceTriggerFields]()
	for index := range fields.NumField() {
		if name, _, _ := strings.Cut(fields.Field(index).Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}()

// MarshalJSON writes the trigger's fields, then the keys in Extra.
func (t PriceTrigger) MarshalJSON() ([]byte, error) {
	known, err := json.Marshal(priceTriggerFields(t))
	if err != nil || len(t.Extra) == 0 {
		return known, err
	}
	document := map[string]json.RawMessage{}
	if err := json.Unmarshal(known, &document); err != nil {
		return nil, err
	}
	for key, value := range t.Extra {
		if !priceTriggerKeys[key] {
			document[key] = value
		}
	}
	return json.Marshal(document)
}

// legacyTrigger is the keys of the price-only triggers that came before
// fields and conditions: "above" or "below" a price.
type legacyTrigger struct {
	Direction      string   `json:"direction"`
	Price          *float64 `json:"price"`
	LastFiredPrice float64  `json:"last_fired_price"`
}

// UnmarshalJSON reads the trigger's fields, keeping the other keys in
// Extra. A price-only trigger reads as one on the last price: above as at
// or above, below as at or below, which is how they fired.
func (t *PriceTrigger) UnmarshalJSON(data []byte) error {
	var fields priceTriggerFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	for key := range document {
		if priceTriggerKeys[key] {
			delete(document, key)
		}
	}
	*t = PriceTrigger(fields)
	if _, legacy := document["direction"]; legacy && t.Condition == "" {
		var old legacyTrigger
		if err := json.Unmarshal(data, &old); err != nil {
			return err
		}
		t.Field, t.Condition = FieldLastPrice, TriggerGTE
		if strings.EqualFold(old.Direction, legacyTriggerBelow) {
			t.Condition = TriggerLTE
		}
		if old.Price != nil {
			t.Value = *old.Price
		}
		if t.LastFiredValue == 0 {
			t.LastFiredValue = old.LastFiredPrice
		}
		for _, key := range []string{"direction", "price", "last_fired_price"} {
			delete(document, key)
		}
	}
	t.defaults()
	if len(document) == 0 {
		document = nil
	}
	t.Extra = document
	return nil
}

// defaults fills in what a trigger stored before fields and actions
// leaves out: it watched the last price, and sent a message, placing its
// orders too if it had any.
func (t *PriceTrigger) defaults() {
	if t.Field == "" {
		t.Field = FieldLastPrice
	}
	if t.Action == "" {
		t.Action = TriggerActionMessage
		if len(t.Orders) > 0 {
			t.Action = TriggerActionBoth
		}
	}
}

// Validate checks the trigger can be stored, normalising its field,
// condition and action, and setting its status from its budget.
func (t *PriceTrigger) Validate() error {
	t.InstrumentID = InstrumentID(strings.TrimSpace(string(t.InstrumentID)))
	t.Field = TickField(strings.ToLower(strings.TrimSpace(string(t.Field))))
	t.Condition = strings.ToLower(strings.TrimSpace(t.Condition))
	t.Action = strings.ToLower(strings.TrimSpace(t.Action))
	t.defaults()
	_, known := t.Field.Info()
	switch {
	case t.InstrumentID == "":
		return fmt.Errorf("%w: a trigger needs an instrument", ErrInvalidInput)
	case !known:
		return fmt.Errorf("%w: trigger field %q", ErrInvalidInput, t.Field)
	case !slices.Contains(TriggerConditions, t.Condition):
		return fmt.Errorf("%w: trigger condition %q: want one of %s", ErrInvalidInput, t.Condition, strings.Join(TriggerConditions, ", "))
	case math.IsNaN(t.Value) || math.IsInf(t.Value, 0):
		return fmt.Errorf("%w: trigger value %v", ErrInvalidInput, t.Value)
	case !slices.Contains(TriggerActions, t.Action):
		return fmt.Errorf("%w: trigger action %q: want one of %s", ErrInvalidInput, t.Action, strings.Join(TriggerActions, ", "))
	case t.PlacesOrders() && len(t.Orders) == 0:
		return fmt.Errorf("%w: a trigger that places a basket needs its orders", ErrInvalidInput)
	case !t.PlacesOrders() && len(t.Orders) > 0:
		return fmt.Errorf("%w: a trigger that only sends a message places no orders", ErrInvalidInput)
	case math.IsNaN(t.Reference) || math.IsInf(t.Reference, 0):
		return fmt.Errorf("%w: trigger reference %v", ErrInvalidInput, t.Reference)
	case t.TimesEvaluated < 0 || t.TimesFired < 0:
		return fmt.Errorf("%w: a trigger's counts cannot be negative", ErrInvalidInput)
	case t.FiringBudget != nil && *t.FiringBudget < 0:
		return fmt.Errorf("%w: firing budget %d: want zero or more, or none for no limit", ErrInvalidInput, *t.FiringBudget)
	}
	t.Status = TriggerActive
	if t.Spent() {
		t.Status = TriggerFired
	}
	if len(t.Orders) == 0 {
		t.OrderMode = ""
		return nil
	}
	for index, order := range t.Orders {
		if err := order.Validate(); err != nil {
			return fmt.Errorf("trigger order %d: %w", index+1, err)
		}
	}
	if t.OrderMode = strings.ToLower(strings.TrimSpace(t.OrderMode)); t.OrderMode != "paper" && t.OrderMode != "live" {
		return fmt.Errorf("%w: trigger order mode %q: want paper or live", ErrInvalidInput, t.OrderMode)
	}
	return nil
}

// Spent reports whether the trigger has fired as many times as its budget
// allows.
func (t PriceTrigger) Spent() bool {
	return t.FiringBudget != nil && t.TimesFired >= *t.FiringBudget
}

// PlacesOrders reports whether the trigger's action places its basket.
func (t PriceTrigger) PlacesOrders() bool {
	return t.Action == TriggerActionBasket || t.Action == TriggerActionBoth
}

// SendsMessage reports whether the trigger's action sends a message.
func (t PriceTrigger) SendsMessage() bool {
	return t.Action != TriggerActionBasket
}

// Crossing reports whether the condition is a crossing, which needs the
// value at the check before.
func (t PriceTrigger) Crossing() bool {
	return t.Condition == TriggerCrossAbove || t.Condition == TriggerCrossBelow
}

// Holds reports whether value meets the condition, previous being the
// value at the check before, nil before the first: a crossing needs it on
// the other side of the trigger's value.
func (t PriceTrigger) Holds(value float64, previous *float64) bool {
	switch t.Condition {
	case TriggerGTE:
		return value >= t.Value
	case TriggerGT:
		return value > t.Value
	case TriggerLT:
		return value < t.Value
	case TriggerLTE:
		return value <= t.Value
	case TriggerEQ:
		return math.Abs(value-t.Value) <= 1e-9*math.Max(1, math.Abs(t.Value))
	case TriggerCrossAbove:
		return previous != nil && *previous < t.Value && value >= t.Value
	case TriggerCrossBelow:
		return previous != nil && *previous > t.Value && value <= t.Value
	}
	return false
}

// Evaluate checks the active trigger against tick at time at, counting
// the check, and reports whether it fired: its condition came to hold, it
// was not still tripped from firing before, and its budget had a firing
// left. A tick without the field's value is no check, nor is a tick for a
// trigger waiting on its entry.
func (t *PriceTrigger) Evaluate(tick Tick, at time.Time) bool {
	value, ok := tick.Value(t.Field)
	if t.Status != TriggerActive || t.Entry != "" || !ok {
		return false
	}
	t.TimesEvaluated++
	previous := t.LastValue
	t.LastValue = &value
	if !t.Holds(value, previous) {
		t.Tripped = false
		return false
	}
	if t.Tripped || t.Spent() {
		return false
	}
	t.TimesFired++
	t.LastFired, t.LastFiredValue, t.Tripped, t.OrderResults = at.UTC(), value, true, nil
	if t.Spent() {
		t.Status = TriggerFired
	}
	return true
}
