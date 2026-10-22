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

## Screener

`ScreenerClient` queries Kite web's instrument screener. It filters, sorts and
pages instruments using market and fundamental data, such as market cap, P/E,
ROE, debt to equity, 52-week range, growth and sector.

The screener signs in with the user's web session, not an API access token:
the user ID, enctoken, `kf_session` and `public_token`. Set these with
`SetScreenerSession`; `credentials.User.KiteClient()` sets them from the stored
login.

```go
screener := user.KiteClient().ScreenerClient()
page, err := screener.GetInstruments(ctx, kite.ScreenerRequest{
	Query:   "market_cap>=10000 and pe<25",
	OrderBy: "market_cap",
	Order:   "desc",
})
all, err := screener.AllInstruments(ctx, kite.ScreenerRequest{Query: "roe>=15"}, 0)
```

- `GetInstruments` reads one page of up to 100 results (Kite caps the page size at 100).
- `AllInstruments` follows the pages, stopping at an optional limit.
- An incomplete or expired session matches `errors.Is(err, kite.ErrScreenerSession)`. An expired session usually shows up as an HTTP 401/403.
- `ScreenerInstrument.Fundamentals()` keeps a result's company data as a `models.Fundamentals`.

### Stored fundamentals

`repository.FundamentalsSQLiteRepository` keeps the latest screener read in
the `instrument_fundamentals` table of the Fundamentals Database, one row per
instrument token. `Replace` swaps every row in one transaction and refuses an
empty read; `Get`, `List` (optionally by index tag, such as `"Nifty 500"`),
`Count` and `LastRefreshedAt` read it back.

The screener covers about 3,300 NSE instruments, ETFs among them (which have
no fundamentals), in roughly 33 pages; the Nifty 500 is 497 of them, as
REITs aren't listed. Join on `instrument_token`, not the trading symbol: a
stock in the BE series appears as, say, `HFCL-BE`. Market cap, PE, dividend
yield and the 52-week range follow the price, so they are only as fresh as
the last refresh; the rest change with quarterly results. A zero ratio means
Kite has none or it is zero (banks have no debt to equity).

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

The tab bar along the top switches between Users, Portfolio, Watchlists, Admin and Settings;
Shift+←/→ cycle through them, and the mouse works too: click a tab or a row to select it, click a selected
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

`B` on a user opens Kite web in Chrome, already signed in with the stored web
session. First it checks the session with Kite, and it never logs in. Each
user gets a Chrome profile of their own under Browser Profiles
(`TRADEBOT_BROWSER_PROFILES_DIR`, `browser` under the base path). That Chrome
stays open after kite-cli exits, and pressing `B` again adds a tab to it.
Logging out in that browser ends the stored session for kite-cli and the
ticker too.

The Orders table, at the bottom, lists the day's orders, newest first. Each row shows the
time, side, type, product, filled over ordered quantity, price, average fill
price and a coloured status, and the panel title counts orders that are
complete, open and rejected or cancelled. `Tab` moves between the tables,
as do `Down` past a table's last row and `Up` past its first. On
an order, the details pane shows its status message, quantities, prices and
timestamps. `Enter` swaps that pane for the raw Kite payload: every key in
Kite's order, values coloured by type, scrolled with `J` and `K`. `Esc` goes
back to the summary. On screens narrower than 110 columns there is no details
pane, so `Enter` opens the payload in a dialog. `+` on an order opens the
order menu for its instrument. On an order Kite has not yet completed,
cancelled or rejected, `M` modifies it: the order form opens with its
quantity, type, price and trigger, and its account, side and product are
fixed. `X` cancels it, after you confirm with `Y`. Either way the portfolio
reloads afterwards. The Orders title also totals the estimated charges of the
day's executed orders, and an executed order's details pane breaks its charges
down into brokerage, STT, exchange charges, GST, stamp duty and SEBI fees.

`X` (or `+`) on a group header exits every position in the group at market,
after a confirmation that lists the legs and their estimated charges and
defaults to keeping them. Buys are placed first, so short legs are covered
before their hedges are sold.

`+` opens the order menu for the selected position or holding, or the
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
quantity by one lot from any field, as `Left` and `Right` do in the quantity
field. In the price and trigger fields, `Left` and
`Right` move the price by the instrument's tick size, starting from the last
price when the field is empty. Beside the price, a limit buy above the last
price, or a limit sell below it, shows `! Immediate Execution !`. For SL and
SL-M orders, a buy's trigger at or below the last price, or a sell's at or
above it, is flagged in red, as is an SL buy's price below its trigger or an
SL sell's above it. Kite rejects those, so the form won't place, save or
add them to a basket, and the summary says why. It places a regular day
order tagged `kite-cli`, with Kite's automatic market protection on market and
SL-M orders, and shows Kite's reason when it is rejected. Below the summary, one
row shows the order's margin, from Kite's margin calculator once the form has
rested on the order for a moment, and its estimated charges at its price (the
last price for a market order). Nothing is sent until **Place** is pressed; `Escape` cancels.

#### Baskets

A basket is a named set of orders, placed together in one go: a spread, a
hedge and its short, a set of exits. Baskets are each user's own and are kept
in the Lists Database beside the watchlists (`lists.sqlite3`, or `-lists-db`).
Each order in a basket is its parameters: exchange, symbol, side, quantity,
order type, prices and product.

**Add to basket** on the order form asks where the order goes, like Add to
list does: `+ New basket…`, or one of the account's baskets. A new basket is
a draft. It is named, but nothing is stored until `S` on the Baskets tab saves
it, and it is lost when kite-cli quits unsaved. The status bar shows
`UNSAVED n` while drafts exist. An order added to a saved basket is stored at
once. `Esc` on the picker goes back to the order form.

The Baskets tab (after Watchlists, or `B` on the Portfolio and Watchlists
screens) lists the active user's baskets, drafts marked `*`, beside the
selected basket's orders. Each order shows its LTP and value, and the basket
shows its total value, the estimated charges and the margin with hedges netted
(from the data user's margin calculator, without the account's positions).
`N` starts an empty draft, `S` saves one, `E` renames a basket, and `D` deletes
a basket (or discards a draft) after asking. `Tab` or `Enter` moves to the
orders. There, `Enter` or `E` edits an order in the order form, with the
account fixed, and `D` removes it. `O` asks, then places every order into the
basket owner's account in the current mode (paper or live), buys first so
hedges go in before the shorts they cover. The basket stays, and each order
shows how its last run went: the order ID, or Kite's reason. `R` reloads
the baskets and prices, and `P` switches paper and live.

#### Paper trading

kite-cli starts in paper mode (Account Mode, `TRADEBOT_ACCOUNT_MODE`, or
`-mode paper|live`). Every user keeps the same ID and gets a paper account,
created on first use with Paper Capital (`TRADEBOT_PAPER_CAPITAL`, ₹10 lakh)
in cash. It is kept in the user's own database in Portfolio Databases,
`<USER>.db`, in the `paper_` tables beside the live order record. A paper
account from before that, in `<USER>_paper.db`, moves in the first time it is
opened, and the old file is renamed `<USER>_paper.db.migrated`. Quit any
older kite-cli first, so it isn't still writing there. `⇧Z` on the Portfolio
screen in paper mode resets the account after asking: every paper order and
fill is deleted, and it opens again with Paper Capital. Live accounts are never
reset.

`P` on the Users, Portfolio and Baskets screens, or `Ctrl+P` anywhere,
switches between paper and live, and the status bar always shows the mode: a
yellow `PAPER` or a red `LIVE` badge.
Switching reloads the open portfolio from the other account. The order form,
the cancel confirmation and the group exit show the badge of the account they
send to; orders and positions are always changed in the account the portfolio
was loaded from.

A paper account is package `paper`'s broker. It never calls Kite's order
endpoints. Prices, minute and day candles and the margin calculator come from
the Data User's stored session, and kite-cli never logs that user in. Only
orders and fills are stored. Positions, holdings and funds are worked out from
the fills each time the account is read.

- Orders are checked like Kite checks them: regular day or IOC orders only,
  lot multiples, tick sizes, the circuit limits, SL trigger prices, CNC only
  on cash and NRML only on derivatives, MIS blocked after 15:20, sellable
  holdings, and margin. An order that fails a check is kept as `REJECTED`,
  with Kite's message.
- Market orders fill at once against the quote's depth, plus Paper Slippage
  (`TRADEBOT_PAPER_SLIPPAGE_BPS`, 0). Limit orders fill when the price
  reaches the limit, and stop orders trigger when it reaches the trigger.
  Charges are estimated like the order form estimates them.
- Margin comes from the data user's basket calculator, which nets hedges.
  When it can't be reached, flat rates are used instead.
- While the portfolio is shown, every `-quotes-every`, an account with open
  orders or positions is matched against the live quote. A fill reloads the
  portfolio and the status bar names the orders that changed.
- Time the account was not read is caught up on its next read, from minute
  candles. A limit fills at the limit, or at a candle's open when that was
  better. An SL-M fills at the trigger, or at the open when that was worse.
- At 15:20, open MIS orders are cancelled and MIS positions squared off. At
  the close, other open orders are cancelled. Contracts settle in cash at
  expiry: futures at the underlying's close, and options at their intrinsic
  value. Delivery bought on one day is a holding the next.

Settling a closed day needs its candles. If they can't be read, the
portfolio shows the error rather than a guess.

Charges are estimated locally by `kite.EstimateOrderCharges` (and
`kite.CalculateCharges` for one order), from Zerodha's published rates for
equity delivery, intraday, futures and options on NSE and BSE. They can drift
from what the contract note says when rates change.

The Settings tab, after Admin, edits `~/tradebot/settings.json`, the only place the tools
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

The tab labels each setting with its `display_name`; when that is empty,
the label comes from the key, so `TRADEBOT_ABC_DEF` reads "Abc Def". It
lists only `visible` settings and shows the focused one's `help` and
default below the fields. `Enter` saves, `Esc` reverts unsaved changes (or
goes back when there are none), and unsaved changes survive switching tabs.
Saved settings apply the next time kite-cli starts. An empty `value` means
the `default`:

| Setting              | Key                                                | Default               | Visible |
| -------------------- | -------------------------------------------------- | --------------------- | ------- |
| Base Path            | `TRADEBOT_LOCAL_STORAGE_ROOT`                      | `~/tradebot`          | yes     |
| Credentials Database | `TRADEBOT_CREDENTIALS_SQLITE_PATH`                 | `credentials.sqlite3` | no      |
| Instruments Database | `TRADEBOT_INSTRUMENTS_DUCKDB_PATH`                 | `instruments.duckdb`  | no      |
| Lists Database       | `TRADEBOT_LISTS_SQLITE_PATH`                       | `lists.sqlite3`       | no      |
| Fundamentals Database | `TRADEBOT_FUNDAMENTALS_SQLITE_PATH`               | `fundamentals.sqlite3` | no      |
| Candles Database     | `TRADEBOT_CANDLES_DUCKDB_PATH`                     | `market-data/historical_data.duckdb` | no      |
| Portfolio Databases  | `TRADEBOT_PORTFOLIO_SQLITE_DIR`                    | `portfolio`           | no      |
| Browser Profiles     | `TRADEBOT_BROWSER_PROFILES_DIR`                    | `browser`             | no      |
| Default User         | `TRADEBOT_DEFAULT_USER_ID`                         | `CC0006`              | yes     |
| Price Mode           | `TRADEBOT_PRICE_MODE`                              | `quote`               | yes     |
| Account Mode         | `TRADEBOT_ACCOUNT_MODE`                            | `paper`               | yes     |
| Paper Capital        | `TRADEBOT_PAPER_CAPITAL`                           | `1000000`             | no      |
| Paper Slippage       | `TRADEBOT_PAPER_SLIPPAGE_BPS`                      | `0`                   | no      |
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
the explorer's prices `ticker down` and polls quotes; reopening Watchlists
tries the ticker again, as it reconnects prices after any price error. Saved settings apply the next time a tool starts.
The file always lives in `~/tradebot`, since Base Path is read from it.

`W` (or `I`) opens the Watchlists tab, the instruments explorer described
below, from any screen; `Esc` goes back one screen everywhere, and `Ctrl+C` quits. The
catalog is `-instruments-db` and the lists `-lists-db`; the price flags match
`instruments-cli`.

With an empty search, the Watchlists tab shows the active user's watchlists
(the open portfolio's user, else the one selected on Users), in the same table
as search results. `[` and `]` switch between them, wrapping around, and the
title shows the list's kind, its position and its row count. A user with no
watchlists gets a smart copy of `ADMIN`'s `DEFAULT SYNC` as their first one,
and again after deleting their last. `N` makes a new watchlist: from scratch,
or from any `ADMIN` list, either as

- **smart** (`S`): a pointer to the parent. It holds no items of its own and
  shows whatever the parent holds, so it follows the parent's refreshes. It
  can't be edited.
- **clone** (`C`): a copy of the parent's items as they are now, which can be
  edited and doesn't change when the parent does.

The name starts as the parent's. `D` removes the selected instrument from a
clone or a scratch watchlist. `L` opens the lists view, which shows only the
user's own lists: `D` deletes the selected list after you confirm, `A` adds an
instrument, `N` makes a watchlist as above and `E` renames one. `A` on a
watchlist offers the same lists, those the user can edit. To watch an NSE
index, make a smart or cloned watchlist from its `ADMIN` list, which Admin
Lists downloads. `O` opens the selected instrument's option chain, and `+` its order menu.
A search replaces the watchlist, and `Esc` brings it back.
`Shift+S` sorts the list by any column; picking the sorted column again
reverses it, and `List order` goes back to the watchlist's or search's own
order. The header marks the sorted column with ▲ or ▼, instruments without
a value (an unpriced LTP, say) go last, and the order stays as you switch
watchlists or search.

The details pane shows only live data: the quote, market depth and, for an
option, the model values. `M` opens the selected instrument's metadata
(identity, contract and underlying) in an overlay; `Esc` or `M` closes it.

`H` opens the Admin screen from any screen but Watchlists. Its sub tabs,
switched with `[` and `]` or a click, are Historical Data,
Instruments and Admin Lists. `Esc` goes back to where Admin was opened from.

**Instruments** summarises the instrument catalog: its file, when it was last
refreshed, how many F&O underlyings it has, and its instruments counted by
exchange and kind (EQ, INDEX, FUT, CE, PE and other). `R` downloads the
complete catalog from Kite again.

Its Fundamentals line shows how many instruments have stored fundamentals,
how many of them are in the Nifty 500, and when they were read; it turns
yellow after a month. `F` reads every instrument from Kite's screener again
(about 33 requests) with the data user's stored web session, and replaces the
stored fundamentals once every page is read. It never logs in: when the
session has expired, refresh it on the Users screen (`F`) first.
`-fundamentals-db` opens another fundamentals database.

**Admin Lists** shows the lists `ADMIN` owns beside every NSE index on
niftyindices.com, downloaded or not: `FO_LIST` (the F&O underlyings, built
from the catalog), then each index, then `ADMIN`'s other lists such as
`DEFAULT SYNC`. A pane on the right shows the selected list's items. `Enter`
downloads the selected index again (or rebuilds `DEFAULT SYNC`), `A`
downloads every index after asking, `R` fetches the index list from
niftyindices.com again, and `D` deletes the selected list after you confirm.
Deleting a list that smart watchlists follow turns them into clones of its
last items.

**Historical Data** shows what Candles Database
(`-candles-db`) holds. It lists the default sync list, `DEFAULT SYNC`: the
main NSE indices, `BSE:SENSEX` and `BSE:BANKEX`, then every NSE stock and
index with futures or options. The list is built from the catalog the first
time and saved as an auto-generated list owned by `ADMIN`; `R` rebuilds it, for
example after the F&O list changes. Each row shows the stored range of 1m and
1d candles. A pane on the right shows the selected instrument's latest candles.

`s` on Historical Data syncs the selected instrument. It runs the standard
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
tools can use it in between.

In kite-cli's option chain, the CHG column beside each leg's LTP shows its
change since the previous close, as `+27.03 +10.07%`, both coloured by
direction. Each leg also shows its POP, the probability of
profit for a buyer: the chance the underlying ends past the breakeven at the
15:30 close on expiry, under Black-Scholes-Merton with the volatility implied
from the leg's last price and a 6.5% risk-free rate (`pkg/bsm`). The details
pane shows the implied volatility with it. The details pane leads with the selected instrument's live
data: its quote, then its five-level market depth (orders, quantity and price
for bids on the left and asks on the right, best first) over a bar of the
total quantity bid against offered, and the model values for an option. Indices
have no depth. `F` filters by side, strike, LTP and
BE %, the breakeven's distance from spot in either direction. The LTP and BE %
filters hide a contract once its price arrives and drop strikes with nothing
left; contracts not yet priced stay. `Reset` clears every filter.

### Instruments terminal explorer

Run the full-screen catalog browser on its own with:

```sh
go run ./cmd/instruments-cli
```

The explorer uses `instruments.duckdb` under Base Path by default. Set
Instruments Database in settings or pass `-db /path/to/catalog.duckdb` to use
another catalog, and `-lists-db` for the lists database (Lists Database,
`lists.sqlite3`, which also keeps the order baskets). The public Kite instrument master does not require a
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

## Local portfolios (portfolio-service)

`portfolio-service` (package `portfolio`) keeps a local record of each user's
portfolio. Each user has their own SQLite database in the Portfolio Databases
directory, for example `~/tradebot/portfolio/CC0006.db`, which also holds
their paper account (the `paper_` tables). Like
market-data-service, it's a library without a server:

```go
directory, err := repository.NewPortfolioSQLiteDirectory(cfg.PortfolioDir)
if err != nil {
	log.Fatal(err)
}
defer directory.Close()

database, err := sql.Open("sqlite3", cfg.CredentialsPath)
if err != nil {
	log.Fatal(err)
}
users, err := credentialstore.NewUserSQLiteRepository(database)
if err != nil {
	log.Fatal(err)
}
svc := portfolio.New(portfolio.DirectoryStore(directory), portfolio.StoredSessions(users), nil)

// Snapshot CC0006's profile, margins, holdings, positions, orders, trades and
// MF holdings, and record the orders' states.
result, err := svc.Sync(ctx, "CC0006")
holdings, takenAt, err := portfolio.Latest[kite.Holdings](ctx, svc, "CC0006", models.SnapshotHoldings)
```

- **`Sync`** stores one snapshot (the Kite response as JSON) per type, for the types given, or for all of them when none are given.
  - The orders it reads are also recorded as order updates.
  - A type that fails is reported in `SyncResult.Errors`, and the rest are still stored.
  - The profile is read first. If it belongs to another account, the sync stops before anything is stored.
- **`RecordOrderUpdates`** stores order states, such as the ticker's order updates. It skips states it already has, so recording the same update twice is harmless.
- **`LatestOrder`** returns an order's current state. That's its COMPLETE, CANCELLED or REJECTED update if it has one, even when a late OPEN arrived after it; otherwise it's the last update recorded.
- **`RefreshOrder`** reads an order's history from Kite and records it first.
- **`OrderHistory`**, **`OrderUpdates`** (filtered by order IDs, tags, statuses or only the latest) and **`OpenOrders`** read the recorded orders.
- **`LatestSnapshot`**, **`Latest[T]`**, **`Snapshots`** and **`LastSynced`** read the snapshots.
- **`Prune`** deletes old snapshots, keeping the last of each type.

User IDs are upper-cased, and must be 1-32 letters, digits, `-` or `_`, so
they're safe as file names. Refused requests match `errors.Is(err, portfolio.ErrInvalid)`.

`StoredSessions` uses each user's stored session and never logs in. If a user's
session has expired, their sync fails until kite-cli refreshes it.

The storage is `repository.PortfolioSQLiteRepository`, one per user database:

- `RecordOrderUpdates`, `OrderUpdates`, `OrderHistory`, `LatestOrderUpdate` and `OpenOrders` work on the `order_updates` table.
- `SaveSnapshot`, `LatestSnapshot`, `Snapshots`, `LastSnapshotTimes` and `DeleteSnapshotsBefore` work on the `portfolio_snapshots` table.

`repository.PortfolioSQLiteDirectory` opens each user's database the first time
it's asked for, and keeps it open until `Close`.

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
