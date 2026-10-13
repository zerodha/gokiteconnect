package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	_ "github.com/mattn/go-sqlite3"
)

// quoteSource fetches full market quotes. *kite.Client implements it,
// batching large requests and pacing them to Kite's 1 request per second.
type quoteSource interface {
	GetQuote(instruments ...string) (kite.Quote, error)
}

// quoteLoader opens the data user's quote source and names the user. It runs
// when the explorer opens and again on every manual price refresh, so a
// session refreshed on the users screen is picked up without restarting.
type quoteLoader func(context.Context) (quoteSource, string, error)

const quoteRequestTimeout = 10 * time.Second

// credentialsQuoteLoader reads the data user's stored access token from the
// credentials database. It never logs in: a fresh login could invalidate a
// session another tool is using, so an expired session is reported instead.
// An empty userID picks the first API user with a data subscription.
func credentialsQuoteLoader(path, userID string) quoteLoader {
	return func(ctx context.Context) (quoteSource, string, error) {
		if _, err := os.Stat(path); err != nil {
			return nil, "", fmt.Errorf("credentials database %s: %w", path, err)
		}
		database, err := sql.Open("sqlite3", path)
		if err != nil {
			return nil, "", err
		}
		defer database.Close()
		store, err := credentialstore.NewUserSQLiteRepository(database)
		if err != nil {
			return nil, "", err
		}
		user, err := dataUser(ctx, store, userID)
		if err != nil {
			return nil, "", err
		}
		client := kite.New(user.APIKey)
		client.SetAppName("kite-cli")
		client.SetTimeout(quoteRequestTimeout)
		client.SetAccessToken(user.AccessToken)
		return client, user.UserID, nil
	}
}

func dataUser(ctx context.Context, store credentials.CredentialsRepository, userID string) (*credentials.Credentials, error) {
	var user *credentials.Credentials
	if userID != "" {
		found, err := store.GetUserByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("data user %s: %w", userID, err)
		}
		user = found
	} else {
		users, err := store.ListAvailableUsers(ctx)
		if err != nil {
			return nil, err
		}
		for i := range users {
			if users[i].IsAPIUser && users[i].DataSubscription {
				user = &users[i]
				break
			}
		}
		if user == nil {
			return nil, errors.New("no API user with a data subscription; set TRADEBOT_DATA_USER_ID")
		}
	}
	if !user.IsAPIUser {
		return nil, fmt.Errorf("data user %s is a web user; full quotes need an API user", user.UserID)
	}
	if user.AccessToken == "" {
		return nil, fmt.Errorf("data user %s has no session; refresh it on the users screen", user.UserID)
	}
	return user, nil
}

func quoteErrorText(err error) string {
	var kiteErr kite.Error
	if errors.As(err, &kiteErr) && kiteErr.ErrorType == kite.TokenError {
		return err.Error() + " · refresh the session on the users screen (F), then press R"
	}
	return err.Error()
}

// marginSource prices orders with Kite's margin calculator. The data user's
// *kite.Client implements it; a quote source without it shows no margins.
type marginSource interface {
	GetOrderMargins(kite.GetMarginParams) ([]kite.OrderMargins, error)
}

// marginTTL is how long a contract's margin is reused before it is fetched
// again. Margins follow the price, so they go stale with it.
const marginTTL = time.Minute

// marginDelay is how long a contract stays selected before its margin is
// fetched, so scrolling through the chain does not request every row.
const marginDelay = 300 * time.Millisecond

// lotMargin is the margin Kite blocks for one lot of a contract.
type lotMargin struct {
	total     float64
	err       error
	fetchedAt time.Time
}

// marginParams asks for the margin of one lot: sold for an option, the
// writer's margin, and bought for anything else.
func marginParams(instrument models.Instrument) kite.GetMarginParams {
	side := kite.TransactionTypeBuy
	if isOption(instrument) {
		side = kite.TransactionTypeSell
	}
	return kite.GetMarginParams{OrderParams: []kite.OrderMarginParam{{
		Exchange:        instrument.Exchange,
		Tradingsymbol:   instrument.TradingSymbol,
		TransactionType: side,
		Variety:         kite.VarietyRegular,
		Product:         kite.ProductNRML,
		OrderType:       kite.OrderTypeMarket,
		Quantity:        float64(max(instrument.LotSize, 1)),
	}}}
}

func isOption(instrument models.Instrument) bool {
	return instrument.InstrumentType == string(models.OptionTypeCall) || instrument.InstrumentType == string(models.OptionTypePut)
}

// breakeven is the underlying price at expiry at which buying option for
// premium neither gains nor loses: strike plus premium for a call, strike
// less premium for a put.
func breakeven(option models.Instrument, premium float64) float64 {
	if option.InstrumentType == string(models.OptionTypePut) {
		return option.Strike - premium
	}
	return option.Strike + premium
}

// formatBreakeven shows a breakeven as a price or, in percent, as the move
// from spot it needs. A percentage needs the underlying's price.
func formatBreakeven(value, spot float64, percent bool) string {
	if !percent {
		return formatPrice(value)
	}
	if spot <= 0 {
		return "—"
	}
	move := (value - spot) / spot * 100
	if move > 0 {
		return fmt.Sprintf("+%.2f%%", move)
	}
	return fmt.Sprintf("%.2f%%", move)
}

// formatChange returns the change from the previous close as a percentage,
// with its sign.
func formatChange(quote kite.QuoteData) (string, int) {
	previous := quote.OHLC.Close
	if previous <= 0 || quote.LastPrice <= 0 {
		return "", 0
	}
	change := (quote.LastPrice - previous) / previous * 100
	switch {
	case change > 0:
		return fmt.Sprintf("+%.2f%%", change), 1
	case change < 0:
		return fmt.Sprintf("%.2f%%", change), -1
	}
	return "0.00%", 0
}

func formatPrice(price float64) string {
	return fmt.Sprintf("%.2f", price)
}

// formatCount shortens large counts: 1234567 becomes 12.35L, Indian style.
func formatCount(value float64) string {
	switch {
	case value >= 1e7:
		return fmt.Sprintf("%.2fCr", value/1e7)
	case value >= 1e5:
		return fmt.Sprintf("%.2fL", value/1e5)
	}
	return fmt.Sprintf("%.0f", value)
}

// formatAmount groups digits the Indian way: 1234567.5 is 12,34,567.50.
func formatAmount(value float64) string {
	text := fmt.Sprintf("%.2f", math.Abs(value))
	whole, fraction, _ := strings.Cut(text, ".")
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		var groups []string
		for len(head) > 2 {
			groups = append([]string{head[len(head)-2:]}, groups...)
			head = head[:len(head)-2]
		}
		groups = append([]string{head}, groups...)
		whole = strings.Join(groups, ",") + "," + tail
	}
	if value < 0 && text != "0.00" {
		whole = "-" + whole
	}
	return whole + "." + fraction
}

func formatSigned(value float64) string {
	if formatted := formatAmount(value); value > 0 && formatted != "0.00" {
		return "+" + formatted
	} else {
		return formatted
	}
}

func formatPercent(value, base float64) string {
	if base == 0 {
		return "—"
	}
	percent := value / base * 100
	if percent > 0 {
		return fmt.Sprintf("+%.2f%%", percent)
	}
	return fmt.Sprintf("%.2f%%", percent)
}

// sign is 1, -1 or 0 for a value above, below or within half a paisa of 0.
func sign(value float64) int {
	switch {
	case value > 0.005:
		return 1
	case value < -0.005:
		return -1
	}
	return 0
}

func fallback(value, replacement string) string {
	if value == "" {
		return replacement
	}
	return value
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width < 2 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func pluralize(noun string, count int) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}

func symbolOf(id models.InstrumentID) string {
	if _, symbol, found := strings.Cut(string(id), ":"); found {
		return symbol
	}
	return string(id)
}

func expiryDay(expiry time.Time) time.Time {
	return time.Date(expiry.Year(), expiry.Month(), expiry.Day(), 0, 0, 0, 0, time.UTC)
}

func daysLeft(expiry, today time.Time) string {
	days := int(expiry.Sub(today).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "1d"
	}
	return fmt.Sprintf("%dd", days)
}
