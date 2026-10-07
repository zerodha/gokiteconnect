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

## Custom endpoints

Use `Client.DoEnvelope` to add endpoint methods in your own package while reusing
the client's authentication, headers, base URI, HTTP transport, and Kite error
handling. Embed or wrap the client and define your own request and response types:

```go
package customkite

import (
	"context"
	"net/http"
	"net/url"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

type Client struct {
	*kiteconnect.Client
}

type Widget struct {
	ID string `json:"id"`
}

// GetWidget demonstrates a custom endpoint; use your service's path and schema.
func (c *Client) GetWidget(ctx context.Context, id string) (Widget, error) {
	var out Widget
	err := c.DoEnvelopeWithContext(ctx, http.MethodGet, "/custom/widgets",
		url.Values{"id": {id}}, nil, &out)
	return out, err
}
```

Create the wrapper with `&customkite.Client{Client: kc}`, where `kc` is an already
configured `*kiteconnect.Client`. Its public API methods remain available as well.
Custom parameters on existing endpoints can also be sent through these helpers
without changing the library's parameter structs.

Endpoint paths must begin with a single `/`. They are appended to the configured
base URI, preserving any base path prefix. Use `SetBaseURI` when targeting a
different service or gateway; absolute endpoint URLs are rejected. Credentials
must be accepted by the target service. Configure the client before concurrent
use; setters are not synchronized.

- `DoEnvelope` decodes the response's `data` field into your destination and
  returns Kite API errors. Pass `nil` to discard data while checking for errors.
- `Do` returns buffered response bytes and HTTP metadata for custom decoding.
- `DoRaw` sends a caller-encoded body for any method, including DELETE. Put query
  parameters in the URI and set `Content-Type` for non-form payloads, such as JSON.
  Use `ReadEnvelope` if the response uses Kite envelopes.
- Each request method has a `WithContext` variant for cancellation and deadlines.

`Do` and `DoEnvelope` append GET, DELETE, and HEAD parameters to the URL query,
retaining existing and repeated values. Other methods, including PATCH, send a
URL-encoded body. `Client.DoRaw` always sends its payload as a body and preserves
the URI's query string. Existing endpoint methods and the low-level `HTTPClient`
helpers retain their previous behavior: GET and DELETE replace the URL query,
POST and PUT send a form body, and other methods ignore the payload.
Caller headers and parameters are left untouched; client authentication, version,
and user agent headers take precedence. `Do` and `DoRaw` return HTTP error statuses
as responses, so callers must check the status or use `ReadEnvelope`. Response
bodies are already closed; read `HTTPResponse.Body` rather than `Response.Body`.

## Kite ticker usage

```go
package main

import (
	"fmt"
	"time"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
	kitemodels "github.com/zerodha/gokiteconnect/v4/models"
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

## Examples

Check [examples folder](https://github.com/zerodha/gokiteconnect/tree/master/examples) for more examples.

You can run the following after updating the API Keys in the examples:

```bash
go run examples/connect/basic/connect.go
```

## Development

#### Fetch mock responses for testcases

This needs to be run initially

```
git submodule update --init --recursive
```

#### Run unit tests

```
go test -v
```
