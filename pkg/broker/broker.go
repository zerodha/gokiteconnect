// Package broker is the account a tool trades through, split from the Kite
// client so a paper account can stand in for a live one. *kite.Client
// implements every interface here; package paper implements them over a
// local order book.
package broker

import (
	"context"
	"fmt"

	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/kite"
)

// Mode is which of a user's accounts a request goes to.
type Mode string

const (
	// Live is the user's Kite account: orders are real.
	Live Mode = "live"
	// Paper is the user's paper account: orders fill against live prices
	// in a local book, and nothing reaches Kite's order endpoints.
	Paper Mode = "paper"
)

// ParseMode reads a mode setting; empty is paper.
func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case "", Paper:
		return Paper, nil
	case Live:
		return Live, nil
	}
	return "", fmt.Errorf("account mode %q: want %s or %s", value, Paper, Live)
}

// Orders places, changes and lists an account's orders.
type Orders interface {
	PlaceOrder(variety string, params kite.OrderParams) (kite.OrderResponse, error)
	ModifyOrder(variety, orderID string, params kite.OrderParams) (kite.OrderResponse, error)
	CancelOrder(variety, orderID string, parentOrderID *string) (kite.OrderResponse, error)
	GetOrders() (kite.Orders, error)
	GetOrderHistory(orderID string) ([]kite.Order, error)
	GetTrades() (kite.Trades, error)
}

// Portfolio reads what an account holds and its funds.
type Portfolio interface {
	GetUserProfile() (kite.UserProfile, error)
	GetPositions() (kite.Positions, error)
	GetHoldings() (kite.Holdings, error)
	GetMFHoldings() (kite.MFHoldings, error)
	GetUserMargins() (kite.AllMargins, error)
}

// Market reads prices.
type Market interface {
	GetQuote(instruments ...string) (kite.Quote, error)
	GetLTP(instruments ...string) (kite.QuoteLTP, error)
	GetOHLC(instruments ...string) (kite.QuoteOHLC, error)
}

// Account is everything a tool does with one account.
type Account interface {
	Orders
	Portfolio
	Market
}

// Matcher is an account that fills its own open orders, as a paper account
// does. Match fills what the market now allows and returns the orders it
// changed; a live account has nothing to match, since Kite fills its
// orders.
type Matcher interface {
	Match() ([]kite.Order, error)
}

var _ Account = (*kite.Client)(nil)

// Opener opens one user's account.
type Opener func(ctx context.Context, userID string) (Account, error)

// Accounts opens users' live and paper accounts. A nil opener turns its
// mode off.
type Accounts struct {
	Live  Opener
	Paper Opener
}

// Open opens userID's account in mode.
func (a Accounts) Open(ctx context.Context, userID string, mode Mode) (Account, error) {
	var open Opener
	switch mode {
	case Live:
		open = a.Live
	case Paper:
		open = a.Paper
	default:
		return nil, fmt.Errorf("account mode %q: want %s or %s", mode, Paper, Live)
	}
	if open == nil {
		return nil, fmt.Errorf("%s trading is unavailable", mode)
	}
	return open(ctx, userID)
}

// KiteAccount is a live account: the user's Kite client. LoggedIn reports
// that opening it logged the user in again.
type KiteAccount struct {
	*kite.Client
	LoggedIn bool
}

// KiteSessions opens live accounts with each user's stored session,
// logging in first only when it has expired, and names requests appName.
func KiteSessions(manager *credentials.UserManager, appName string) Opener {
	return func(ctx context.Context, userID string) (Account, error) {
		user, loggedIn, err := manager.RefreshUserSessionIfRequired(ctx, userID)
		if err != nil {
			return nil, err
		}
		client := user.KiteClient()
		client.SetAppName(appName)
		return KiteAccount{Client: client, LoggedIn: loggedIn}, nil
	}
}
