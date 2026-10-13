# The Kite Connect API Go client

The official Go client for communicating with the Kite Connect API.

Kite Connect is a set of REST-like APIs that expose many capabilities required
to build a complete investment and trading platform. Execute orders in real
time, manage user portfolio, stream live market data (WebSockets), and more,
with the simple HTTP API collection.

Zerodha Technology (c) 2021. Licensed under the MIT License.

## Documentation

- [Client API documentation - GoDoc](https://godoc.org/github.com/zerodha/gokiteconnect)
- [Kite Connect HTTP API documentation](https://kite.trade/docs/connect/v3)

## Installation

```
go get github.com/zerodha/gokiteconnect/v4
```

## API usage

```go
package main

import (
	"fmt"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

const (
	apiKey    string = "my_api_key"
	apiSecret string = "my_api_secret"
)

func main() {
	// Create a new Kite connect instance
	kc := kiteconnect.New(apiKey)

	// Login URL from which request token can be obtained
	fmt.Println(kc.GetLoginURL())

	// Obtained request token after Kite Connect login flow
	requestToken := "request_token_obtained"

	// Get user details and access token
	data, err := kc.GenerateSession(requestToken, apiSecret)
	if err != nil {
		fmt.Printf("Error: %v", err)
		return
	}

	// Set access token
	kc.SetAccessToken(data.AccessToken)

	// Get margins
	margins, err := kc.GetUserMargins()
	if err != nil {
		fmt.Printf("Error getting margins: %v", err)
	}
	fmt.Println("margins: ", margins)
}
```

## Local instrument catalog

`Client.Instruments()` exposes a normalized, searchable catalog built from the
Zerodha instrument master. Attach a repository to enable it; the DuckDB
implementation lives in the `repository` package.

```go
package main

import (
	"context"
	"log"

	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/repository"
)

func main() {
	ctx := context.Background()
	client := kite.New("my_api_key")
	client.SetAccessToken("my_access_token")

	instruments, err := repository.NewInstrumentsDuckDBRepository(ctx, "instruments.duckdb")
	if err != nil {
		log.Fatal(err)
	}
	client.SetInstrumentRepository(instruments)
	defer client.Instruments().Close()

	// Refresh when the catalog has not been updated today in Asia/Kolkata.
	if _, err := client.Instruments().RefreshIfStale(ctx); err != nil {
		log.Fatal(err)
	}

	options, err := client.Instruments().GetOptions(ctx, models.OptionsFilter{
		UnderlyingID: "NSE:NIFTY 50",
		Types:        []models.OptionType{models.OptionTypeCall},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("found %d call options", len(options))
}
```

Use a new database path, or reopen a database previously created by the DuckDB
repository. Legacy tradebot databases are rejected without modification. DuckDB
uses CGO, so builds must have a working C toolchain and `CGO_ENABLED=1`.

### Terminal console (kite-cli)

`kite-cli` combines `users-cli` and `instruments-cli` in one application,
built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), Bubbles
and Lip Gloss:

```sh
go run ./cmd/kite-cli
```

The tab bar along the top switches between Users, Portfolio and Instruments,
and the mouse works too: click a tab or a row to select it, click a selected
row again to open it, and scroll tables with the wheel. The previous
tview-based console is kept as `cmd/kite-cli-old` with the same flags and
keys.

It opens the portfolio of the default user (`TRADEBOT_DEFAULT_USER_ID`, or
`-user`; `CC0006` unless set), or the users stored in the credentials database
(`TRADEBOT_CREDENTIALS_SQLITE_PATH`, or `-credentials-db`) when that user is
not stored. `Enter` on a user, or `U` from a portfolio, shows a user's net
positions, holdings and mutual fund holdings. The stored
session is checked first, and the user is logged in again only when there is
no session or Kite reports it expired; the new session is saved. Positions are
grouped by underlying (looked up in the instrument catalog, which is opened on
first use) with a subtotal P&L per group; `Enter` on a group collapses it, and
an underlying with one position is not grouped. A details pane describes the
selected row. Kite's positions and holdings responses carry delayed prices, so
the portfolio is repriced from OHLC quotes with the user's session, and again
every `-quotes-every` while it is shown. Next to the P&L cards, the Margin card
shows the margin used out of the total (equity and commodity together, such as
`₹1.4L/6.4L`) and how much is free. Only the focused table highlights its
selected row.

The Orders table, at the bottom, lists the day's orders, newest first. Each row shows the
time, side, type, product, filled over ordered quantity, price, average fill
price and a coloured status, and the panel title counts orders that are
complete, open and rejected or cancelled. `Tab` moves between the tables,
as do `Down` past a table's last row and `Up` past its first. On
an order, the details pane shows its status message, quantities, prices and
timestamps. `Enter` swaps that pane for the raw Kite payload: every key in
Kite's order, values coloured by type, scrolled with `J` and `K`. `Esc` goes
back to the summary. On screens narrower than 110 columns there is no details
pane, so `Enter` opens the payload in a dialog. `O` on an order opens the
order menu for its instrument. On an order Kite has not yet completed,
cancelled or rejected, `M` modifies it: the order form opens with its
quantity, type, price and trigger, and its account, side and product are
fixed. `X` cancels it, after you confirm with `Y`. Either way the portfolio
reloads afterwards.

`O` opens the order menu for the selected position or holding, or the
selected instrument in the explorer (a search result, chain contract, future
or list item):

- `B` / `S` raw buy or sell of the instrument itself (not of an index).
- `X` close position: a market order on the opposite side for the whole
  quantity, in its product. Offered when the open portfolio holds it.
- `c` / `C` buy or sell a call, `p` / `P` buy or sell a put, of the
  instrument's underlying.

Option orders first open a strike picker: one expiry at a time (`←` `→` or
`[` `]` change it), the strikes priced live with the at-the-money strike
selected, in-the-money strikes tinted and the spot price marked between
strikes. Each strike shows its premium, change, moneyness, breakeven, cost per
lot and open interest; `Enter` orders the selected strike.

The order form picks the account (the open portfolio's user by default), side,
quantity (one lot by default, in multiples of the lot size), order type
(market, limit, SL or SL-M, with price and trigger as the type needs) and
product (NRML or MIS for derivatives, CNC or MIS otherwise). `Up` and `Down`
(or `Tab`) move between fields, `Left` and `Right` change a drop-down's option,
and fields the order type doesn't use are skipped. `+` and `-` change the
quantity by one lot from any field. In the price and trigger fields, `Left` and
`Right` move the price by the instrument's tick size, starting from the last
price when the field is empty. It places a regular day
order tagged `kite-cli`, with Kite's automatic market protection on market and
SL-M orders, and shows Kite's reason when it is rejected. Nothing is sent until
**Place** is pressed; `Escape` cancels.

`I` opens the instruments explorer
described below from either screen, `Q` goes back one screen, and `Ctrl+C`
quits. The catalog is `-instruments-db`; the price flags match
`instruments-cli`.

### Instruments terminal explorer

Run the full-screen catalog browser on its own with:

```sh
go run ./cmd/instruments-cli
```

The explorer uses `$TRADEBOT_LOCAL_STORAGE_ROOT/instruments.duckdb` by default.
Set `TRADEBOT_INSTRUMENTS_DUCKDB_PATH` or pass `-db /path/to/catalog.duckdb`
to use another catalog. The public Kite instrument master does not require a
stored user session or API key. A missing or stale catalog is refreshed when the
application opens.

The main view shows listed F&O underlyings when the search field is empty and
ranked instrument matches while typing. Select an underlying or derivative to
open its futures and option chain. The chain supports expiry selection, strike
bounds, and call/put filtering. Each leg shows its breakeven at expiry (strike
plus premium for a call, strike less premium for a put), as the move from spot
by default, and its open interest over a bar scaled to the chain's largest OI,
growing out from the strike column. The status bar shows
the selected contract's premium per lot and the margin Kite blocks for one lot
(sold, for an option; bought, for a future), from the data user's margin
calculator.

Prices come from full quotes fetched with the data user's session:
`TRADEBOT_DATA_USER_ID` or `-data-user`, otherwise the first API user with a
data subscription. Visible prices refresh every `-quotes-every` (5s by
default); `-no-quotes` turns them off.

- `/` focuses search and `U` returns to the F&O-underlying browser.
- `Enter` explores the selected instrument; `Q` or `Esc` goes back, and `Ctrl+C` quits.
- `E` selects an expiry, `[` and `]` cycle expiries, and `F` edits filters.
- `%` switches chain breakevens between the move from spot (the default) and prices.
- `R` refreshes prices, `C` forces a complete catalog refresh, and `L` opens lists.

The explorer does not place trades. Mouse navigation is supported.

## Kite ticker usage

```go
package main

import (
	"fmt"
	"time"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
	kitemodels "github.com/devshoe/gokiteconnect/models"
	kiteticker "github.com/zerodha/gokiteconnect/v4/ticker"
)

var (
	ticker *kiteticker.Ticker
)

var (
	instToken = []uint32{408065, 112129}
)

// Triggered when any error is raised
func onError(err error) {
	fmt.Println("Error: ", err)
}

// Triggered when websocket connection is closed
func onClose(code int, reason string) {
	fmt.Println("Close: ", code, reason)
}

// Triggered when connection is established and ready to send and accept data
func onConnect() {
	fmt.Println("Connected")
	err := ticker.Subscribe(instToken)
	if err != nil {
		fmt.Println("err: ", err)
	}
	// Set subscription mode for the subscribed token
	// Default mode is Quote
	err = ticker.SetMode(kiteticker.ModeFull, instToken)
	if err != nil {
		fmt.Println("err: ", err)
	}

}

// Triggered when tick is recevived
func onTick(tick kitemodels.Tick) {
	fmt.Println("Tick: ", tick)
}

// Triggered when reconnection is attempted which is enabled by default
func onReconnect(attempt int, delay time.Duration) {
	fmt.Printf("Reconnect attempt %d in %fs\n", attempt, delay.Seconds())
}

// Triggered when maximum number of reconnect attempt is made and the program is terminated
func onNoReconnect(attempt int) {
	fmt.Printf("Maximum no of reconnect attempt reached: %d", attempt)
}

// Triggered when order update is received
func onOrderUpdate(order kiteconnect.Order) {
	fmt.Printf("Order: ", order.OrderID)
}

func main() {
	apiKey := "my_api_key"
	accessToken := "my_access_token"

	// Create new Kite ticker instance
	ticker = kiteticker.New(apiKey, accessToken)

	// Assign callbacks
	ticker.OnError(onError)
	ticker.OnClose(onClose)
	ticker.OnConnect(onConnect)
	ticker.OnReconnect(onReconnect)
	ticker.OnNoReconnect(onNoReconnect)
	ticker.OnTick(onTick)
	ticker.OnOrderUpdate(onOrderUpdate)

	// Start the connection
	ticker.Serve()
}
```

## Development

Test fixtures live in `kite/testdata/` and `kite/ticker/testdata/`; no setup is needed.

#### Run unit tests

```
go test ./...
```
