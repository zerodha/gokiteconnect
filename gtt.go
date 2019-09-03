package kiteconnect

type TriggerType string

const (
	Single TriggerType = "single"
	TwoLeg TriggerType = "two-leg"
)

type Triggers []Trigger

type GTTMeta struct {
	RejectionReason string `json:"rejection_reason"`
}

type GTTCondition struct {
	Exchange      string    `json:"exchange"`
	LastPrice     float64   `json:"last_price"`
	Tradingsymbol string    `json:"tradingsymbol"`
	TriggerValues []float64 `json:"trigger_values"`
}

type Trigger struct {
	ID            int          `json:"id"`
	UserID        string       `json:"user_id"`
	ParentTrigger interface{}  `json:"parent_trigger"`
	Type          string       `json:"type"`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
	ExpiresAt     string       `json:"expires_at"`
	Status        string       `json:"status"`
	Condition     GTTCondition `json:"condition"`
	Orders        []Order      `json:"orders"`
	Meta          GTTMeta      `json:"meta"`
}

type GTTOrderParams struct {
	Tradingsymbol string
	Exchange      string
	LastPrice     float64
	Type          TriggerType
	TriggerValues []float64
	Orders        []Order
}

func AddTrigger(o GTTOrderParams) error {
	return nil
}

func UpdateTrigger(triggerID int) error {
	return nil
}

func GetTriggers() (Triggers, error) {
	return nil, nil
}

func GetTrigger(triggerID int) (Trigger, error) {
	return Trigger{}, nil
}

func RemoveTrigger(triggerID int) error {
	return nil
}
