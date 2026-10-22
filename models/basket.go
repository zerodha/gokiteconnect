package models

import (
	"fmt"
	"strings"
	"time"
)

// Basket is a named, user-owned set of orders, placed together into the
// owner's account. Its items are kept in the order they were added.
type Basket struct {
	ID        string
	CreatedBy string
	Name      string
	Items     []BasketItem
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BasketItem is one order of a basket: the parameters it is placed with.
type BasketItem struct {
	ID              string
	Exchange        string
	TradingSymbol   string
	TransactionType string
	OrderType       string
	Product         string
	Quantity        int
	Price           float64
	TriggerPrice    float64
	Validity        string
	Tag             string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// InstrumentID is the instrument the item orders.
func (i BasketItem) InstrumentID() InstrumentID {
	return InstrumentID(i.Exchange + ":" + i.TradingSymbol)
}

// Validate reports an item missing what an order needs.
func (i BasketItem) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"exchange", i.Exchange}, {"trading symbol", i.TradingSymbol}, {"transaction type", i.TransactionType},
		{"order type", i.OrderType}, {"product", i.Product},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: basket item %s is required", ErrInvalidInput, field.name)
		}
	}
	if i.Quantity <= 0 {
		return fmt.Errorf("%w: basket item quantity must be above 0", ErrInvalidInput)
	}
	if i.Price < 0 || i.TriggerPrice < 0 {
		return fmt.Errorf("%w: basket item prices cannot be negative", ErrInvalidInput)
	}
	return nil
}
