package gokite

import (
	"crypto/sha256"
	"fmt"
	"time"
)

type Client struct {
	apiKey      string
	apiSecret   string
	accessToken string
	debug       bool
	timeout     time.Duration
	baseURI     string
}

const (
	timeout = 7
	baseURI = "https://api.kite.trade"
)

var routes = map[string]string{
	"parameters":     "/parameters",
	"api.validate":   "/session/token",
	"api.invalidate": "/session/token",
	"user.margins":   "/user/margins/{segment}",

	"orders":      "/orders",
	"trades":      "/trades",
	"orders.info": "/orders/{order_id}",

	"orders.place":  "/orders/{variety}",
	"orders.modify": "/orders/{variety}/{order_id}",
	"orders.cancel": "/orders/{variety}/{order_id}",
	"orders.trades": "/orders/{order_id}/trades",

	"portfolio.positions":        "/portfolio/positions",
	"portfolio.holdings":         "/portfolio/holdings",
	"portfolio.positions.modify": "/portfolio/positions",

	"market.instruments.all": "/instruments",
	"market.instruments":     "/instruments/{exchange}",
	"market.quote":           "/instruments/{exchange}/{tradingsymbol}",
	"market.historical":      "/instruments/historical/{instrument_token}/{interval}",
	"market.trigger_range":   "/instruments/{exchange}/{tradingsymbol}/trigger_range",
}

func New(apiKey string, apiSecret string) *Client {
	client := &Client{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		baseURI:   baseURI,
		timeout:   timeout,
	}

	return client
}

func (client *Client) setDebug(debug bool) {
	client.debug = debug
}

func (client *Client) setBaseURI(baseURI string) {
	client.baseURI = baseURI
}

func (client *Client) setTimeout(timeout time.Duration) {
	client.timeout = timeout
}

func (client *Client) setAccessToken(requestToken string) {
	// Get SHA256 checksum
	h := sha256.New()
	h.Write([]byte(client.apiKey + requestToken + client.apiSecret))
	checksum := h.Sum(nil)
	fmt.Println(checksum)
}
