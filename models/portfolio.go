package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// SnapshotType names what a portfolio snapshot holds.
type SnapshotType string

// The snapshot types, each the JSON of one Kite read.
const (
	SnapshotProfile    SnapshotType = "profile"     // kite.UserProfile
	SnapshotMargins    SnapshotType = "margins"     // kite.AllMargins
	SnapshotHoldings   SnapshotType = "holdings"    // kite.Holdings
	SnapshotPositions  SnapshotType = "positions"   // kite.Positions
	SnapshotOrders     SnapshotType = "orders"      // kite.Orders
	SnapshotTrades     SnapshotType = "trades"      // kite.Trades
	SnapshotMFHoldings SnapshotType = "mf_holdings" // kite.MFHoldings
)

// SnapshotTypes are all the snapshot types, in the order a sync takes them.
var SnapshotTypes = []SnapshotType{
	SnapshotProfile, SnapshotMargins, SnapshotHoldings, SnapshotPositions,
	SnapshotOrders, SnapshotTrades, SnapshotMFHoldings,
}

// IsValid reports whether t is a known snapshot type.
func (t SnapshotType) IsValid() bool {
	return slices.Contains(SnapshotTypes, t)
}

// PortfolioSnapshot is one stored read of a user's portfolio: Data is the
// JSON Kite's value marshalled to.
type PortfolioSnapshot struct {
	ID      int64
	Type    SnapshotType
	TakenAt time.Time
	Data    json.RawMessage
}

// Decode unmarshals the snapshot's data into v, such as a *kite.Holdings
// for a holdings snapshot.
func (s PortfolioSnapshot) Decode(v any) error {
	if err := json.Unmarshal(s.Data, v); err != nil {
		return fmt.Errorf("decode %s snapshot %d: %w", s.Type, s.ID, err)
	}
	return nil
}

// Terminal order statuses: an order in one of them changes no further.
const (
	OrderStatusComplete  = "COMPLETE"
	OrderStatusCancelled = "CANCELLED"
	OrderStatusRejected  = "REJECTED"
)

// IsTerminalOrderStatus reports whether status is COMPLETE, CANCELLED or
// REJECTED.
func IsTerminalOrderStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case OrderStatusComplete, OrderStatusCancelled, OrderStatusRejected:
		return true
	}
	return false
}

// OrderUpdateFilter selects stored order updates. Empty fields match
// everything; values within a field are alternatives.
type OrderUpdateFilter struct {
	OrderIDs []string
	Tags     []string
	Statuses []string
	// LatestOnly keeps only each order's current update: its terminal one
	// if it has one, otherwise the last one recorded.
	LatestOnly bool
}

var userIDPattern = regexp.MustCompile(`^[A-Z0-9_-]{1,32}$`)

// NormalizeUserID trims and upper-cases a broker user ID such as CC0006,
// refusing one that is not 1-32 letters, digits, - or _, so the ID is safe
// as a file name.
func NormalizeUserID(userID string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(userID))
	if !userIDPattern.MatchString(normalized) {
		return "", fmt.Errorf("%w: user ID %q: want 1-32 letters, digits, - or _", ErrInvalidInput, userID)
	}
	return normalized, nil
}
