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

`kite-cli` combines `users-cli` and `instruments-cli` in one application:

```sh
go run ./cmd/kite-cli
```

It opens on the users stored in the credentials database
(`TRADEBOT_CREDENTIALS_SQLITE_PATH`, or `-credentials-db`). `Enter` on a user
shows their net positions, holdings and mutual fund holdings. The stored
session is checked first, and the user is logged in again only when there is
no session or Kite reports it expired; the new session is saved. Positions are
grouped by underlying (looked up in the instrument catalog, which is opened on
first use) with a subtotal P&L per group; `Enter` on a group collapses it, and
an underlying with one position is not grouped. A details pane describes the
selected row. Kite's positions and holdings responses carry delayed prices, so
the portfolio is repriced from OHLC quotes with the user's session, and again
every `-quotes-every` while it is shown. `I` opens the instruments explorer
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
