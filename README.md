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

The catalog file is opened read-only, so kite-cli, ticker-server,
instruments-cli and market-data-service can all have it open at once. A refresh
never writes to it: `Replace` builds the new catalog in a sibling file
(`instruments.duckdb.next-<pid>`), renames it over the old one and reopens.
Processes already reading keep their snapshot until they reopen the catalog.
Within one process DuckDB shares a single instance per file, so the process
that refreshes needs to hold only one handle on it; `Replace` reports an error
if another handle in the same process still has the old catalog open.

Instrument lists live in their own SQLite database,
`repository.NewListsSQLiteRepository(ctx, "lists.sqlite3", "instruments.duckdb")`,
which several processes can write (WAL mode with a busy timeout). The first open copies across
the lists an older instruments database held, once. If that database was refreshed before
then, its first `Replace` hard-linked the old file to
`instruments.duckdb.lists-legacy`, and the copy reads from there. Once the lists
show up in `lists.sqlite3` that file can be deleted.

### Terminal console (kite-cli)

`kite-cli` combines `users-cli` and `instruments-cli` in one application,
built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), Bubbles
and Lip Gloss:

```sh
go run ./cmd/kite-cli
```

The tab bar along the top switches between Users, Portfolio, Instruments and History,
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
reloads afterwards. The Orders title also totals the estimated charges of the
day's executed orders, and an executed order's details pane breaks its charges
down into brokerage, STT, exchange charges, GST, stamp duty and SEBI fees.

`X` (or `O`) on a group header exits every position in the group at market,
after a confirmation that lists the legs and their estimated charges and
defaults to keeping them. Buys are placed first, so short legs are covered
before their hedges are sold.

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
SL-M orders, and shows Kite's reason when it is rejected. Below the summary it
shows the order's estimated charges at its price (the last price for a market
order). Nothing is sent until **Place** is pressed; `Escape` cancels.

Charges are estimated locally by `kite.EstimateOrderCharges` (and
`kite.CalculateCharges` for one order), from Zerodha's published rates for
equity delivery, intraday, futures and options on NSE and BSE. They can drift
from what the contract note says when rates change.

`s` opens the settings dialog from any screen (on the users screen, `Shift+S`
toggles sudo). It edits `~/tradebot/settings.json`, the only place the tools
read configuration from (environment variables and `.env` are no longer
read). The file is an object keyed by each setting's `TRADEBOT_*` name:

```json
{
  "TRADEBOT_LOCAL_STORAGE_ROOT": {
    "display_name": "Base Path",
    "value": "",
    "default": "~/tradebot",
    "help": "Where local files are stored: the credentials, instrument and candle databases.",
    "visible": true
  }
}
```

The dialog labels each setting with its `display_name`; when that is empty,
the label comes from the key, so `TRADEBOT_ABC_DEF` reads "Abc Def". It
lists only `visible` settings and shows the focused one's `help` and
default below the fields. An empty `value` means the `default`:

| Setting              | Key                                                | Default               | Visible |
| -------------------- | -------------------------------------------------- | --------------------- | ------- |
| Base Path            | `TRADEBOT_LOCAL_STORAGE_ROOT`                      | `~/tradebot`          | yes     |
| Credentials Database | `TRADEBOT_CREDENTIALS_SQLITE_PATH`                 | `credentials.sqlite3` | no      |
| Instruments Database | `TRADEBOT_INSTRUMENTS_DUCKDB_PATH`                 | `instruments.duckdb`  | no      |
| Lists Database       | `TRADEBOT_LISTS_SQLITE_PATH`                       | `lists.sqlite3`       | no      |
| Candles Database     | `TRADEBOT_CANDLES_DUCKDB_PATH`                     | `market-data/historical_data.duckdb` | no      |
| Default User         | `TRADEBOT_DEFAULT_USER_ID`                         | `CC0006`              | yes     |
| Price Mode           | `TRADEBOT_PRICE_MODE`                              | `quote`               | yes     |
| Data User            | `TRADEBOT_DATA_USER_ID`                            |                       | yes     |
| Ticker User          | `TRADEBOT_TICKER_USER_ID`                          |                       | yes     |
| NATS URL             | `TRADEBOT_NATS_URL`                                |                       | yes     |
| NATS WebSocket URL   | `TRADEBOT_NATS_WS_URL`                             | `ws://localhost:9222` | yes     |
| Heartbeat Topic      | `TRADEBOT_HEARTBEAT_TOPIC`                         | `ticks.subscriptions` | yes     |
| Subscription TTL     | `TRADEBOT_SUBSCRIPTION_TTL_SECONDS`                | `60`                  | yes     |
| Token Poll Interval  | `TRADEBOT_TICKER_TOKEN_POLL_INTERVAL_SECONDS`      | `300`                 | yes     |
| Mapping Refresh      | `TRADEBOT_TICKER_MAPPING_REFRESH_INTERVAL_SECONDS` | `900`                 | yes     |
| Ticker Port          | `TRADEBOT_TICKER_SERVICE_PORT`                     | `8082`                | yes     |

The database paths are relative to Base Path and must stay inside it.
Price Mode is `quote` or `ticker`. `quote` polls Kite every `-quotes-every`.
`ticker` has kite-cli read the ticks ticker-server publishes, over the local
NATS WebSocket at NATS WebSocket URL; it never opens a Kite ticker itself. It
sends the IDs on screen (explorer and portfolio) as the `{"ids": [...]}`
heartbeat on Heartbeat Topic, at once when new ones appear and every 15s,
dropping IDs not shown for 30s, and redraws on ticks at most four times a
second. Instruments without a tick from the last 30s, such as outside market
hours, are priced from quotes. When ticker-server's health endpoint (on
Ticker Port) or the WebSocket doesn't answer, kite-cli shows the error, marks
the explorer's prices `ticker down` and polls quotes; `R` tries the ticker
again. Saved settings apply the next time a tool starts.
The file always lives in `~/tradebot`, since Base Path is read from it.

`I` opens the instruments explorer
described below from either screen, `Q` goes back one screen, and `Ctrl+C`
quits. The catalog is `-instruments-db` and the lists `-lists-db`; the price flags match
`instruments-cli`.

`H` opens the History screen, which shows what Candles Database
(`-candles-db`) holds. It lists the default sync list, `DEFAULT SYNC`: the
main NSE indices, `BSE:SENSEX` and `BSE:BANKEX`, then every NSE stock and
index with futures or options. The list is built from the catalog the first
time and saved as an auto-generated list owned by `ADMIN`; `R` rebuilds it, for
example after the F&O list changes. Each row shows the stored range of 1m and
1d candles. A pane on the right shows the selected instrument's latest candles.

`s` on the History screen syncs the selected instrument. It runs the standard
sync: 1m candles from 2015 and 1d candles from the earliest Kite has, both up
to yesterday's close. Run it again to fetch only what's new. Syncs run one at a
time in the background, using the Data User's stored session. Instruments
pressed while a sync runs are queued. `A` syncs every instrument on the screen
after asking, and `X` drops the queue and stops the running sync, keeping what
it stored. While a sync runs, the status line shows live progress: the
interval, the percentage done, the time of the last stored candle and how many
candles it has stored. On the right, a counter shows which sync of the batch is
running and how many have failed. The SYNC column marks each row as queued,
syncing (with its percentage), done, failed or stopped. The candles database is opened only for each read or sync, so other
tools can use it in between. Since `s` syncs here, open settings from another
screen.

In the explorer, `H` opens History on the selected instrument (a search
result, chain contract, future or list item). If the instrument isn't in the
list, it's added to the top of the screen for this session only.

### Instruments terminal explorer

Run the full-screen catalog browser on its own with:

```sh
go run ./cmd/instruments-cli
```

The explorer uses `instruments.duckdb` under Base Path by default. Set
Instruments Database in settings or pass `-db /path/to/catalog.duckdb` to use
another catalog, and `-lists-db` for the lists database (Lists Database,
`lists.sqlite3`). The public Kite instrument master does not require a
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
Data User (`TRADEBOT_DATA_USER_ID`) or `-data-user`, otherwise the first API user with a
data subscription. Visible prices refresh every `-quotes-every` (5s by
default); `-no-quotes` turns them off.

- `/` focuses search and `U` returns to the F&O-underlying browser.
- `Enter` explores the selected instrument; `Q` or `Esc` goes back, and `Ctrl+C` quits.
- `E` selects an expiry, `[` and `]` cycle expiries, and `F` edits filters.
- `%` switches chain breakevens between the move from spot (the default) and prices.
- `R` refreshes prices, `C` forces a complete catalog refresh, and `L` opens lists.

The explorer does not place trades. Mouse navigation is supported.

## Local candles (market-data-service)

`market-data-service` (package `marketdata`) keeps the candles database at
Candles Database. It's a library, without a server, for a command-line tool to drive:

```go
store, err := repository.NewCandlesDuckDBRepository(ctx, cfg.CandlesPath, "")
if err != nil {
	log.Fatal(err)
}
defer store.Close()

svc := marketdata.New(store,
	marketdata.NewKiteSource(cfg.CredentialsPath, cfg.DataUserID, marketdata.CatalogTokens(cfg.InstrumentsPath)),
	marketdata.ListsReader(cfg.ListsPath, cfg.InstrumentsPath), nil)

// Cache NSE:INFY's 1m candles from 2015 and 1d candles from the earliest Kite
// has, resuming after the last stored candles, up to yesterday's close.
results, err := svc.SyncStandard(ctx, "NSE:INFY")
```

- `SyncStandard` runs `Sync` for the standard intervals, 1m and 1d. Every intraday interval is aggregated from 1m.
- `SyncRequest.Progress` and `SyncStandardProgress` report a sync's progress after each stored batch, as a `SyncProgress`. `Fraction` gives the share of the range done.
- `DefaultSyncList` builds the default sync list from an instrument catalog. The list holds `DefaultSyncIndices` (tradebot's NSE index set, `BSE:SENSEX` and `BSE:BANKEX`), then every NSE F&O underlying. `RebuildDefaultSyncList` saves it as the auto-generated `DEFAULT SYNC` list.
- `Sync` fetches one instrument's interval and stores it, replacing candles already stored. A zero `From` resumes after the last stored candle, or starts in 2015 (intraday) or 1990, before Kite's earliest daily candles. A zero `To` is the last settled close: yesterday at 15:30 IST.
- `SyncAll` brings every stored series up to date. A series that fails is reported, and the rest carry on.
- `PreviewReconcile` and `Reconcile` compare the stored instruments with an instrument list. `Reconcile` drops instruments not in the list and backfills new ones.
- `Candles`, `RecentCandles` and `Availability` read. Intraday intervals are aggregated from 1m candles. 1d is the stored day candles, whose close is the closing auction price that 1m candles miss; days after the last stored one are aggregated from 1m until synced. 1W is aggregated from the days, in weeks from Monday.
- `PutCandles` writes (with `replace` to overwrite). `DeleteCandles` deletes one interval, or every interval when the interval is empty. `FlushLiveCandles` clears live candles.

Refused requests match `errors.Is(err, marketdata.ErrInvalid)`.

The Kite source uses the Data User's stored session and never logs in. It finds
instrument tokens with a `TokenLookup`. `CatalogTokens` reads them from the
instruments database at most once an hour, opening it only for the read. A process
that keeps the catalog open, like kite-cli, looks tokens up there instead. The
source requests one Kite batch window at a time, 100ms apart, and stores each
batch as it arrives.

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
