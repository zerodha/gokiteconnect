package kiteconnect

import (
	"fmt"
	"net/url"

	"github.com/gorilla/websocket"
)

type TickerCallbacks struct {
	onTick        func(string) string
	onMessage     func(string) string
	onNoReconnect func(string) string
	onReconnect   func(string) string
	onConnect     func(string) string
	onClose       func(string) string
	onError       func(string) string
}

type Ticker struct {
	apiKey      string
	publicToken string
	userID      string
	conn        *websocket.Conn
	isConnected bool
	URL         url.URL
}

type Mode string

type Tick struct {
	mode              Mode
	token             int64
	isTradeable       bool
	volume            int64
	lastQuantity      int64
	totalBuyQuantity  int64
	totalSellQuantity int64
	lastPrice         float64
	averagePrice      float64
	openPrice         float64
	highPrice         float64
	lowPrice          float64
	closePrice        float64
	depth             struct {
		buy  []TickDepth
		sell []TickDepth
	}
}

type TickDepth struct {
	price    float64
	orders   int64
	quantity int64
}

const (
	ModeFull  Mode = "full"
	ModeLTP   Mode = "ltp"
	ModeQuote Mode = "quote"
)

var (
	tickerURL = url.URL{Scheme: "wss", Host: "websocket.kite.trade"}
)

func NewTicker(apiKey string, publicToken string, userID string) *Ticker {
	ticker := &Ticker{
		apiKey:      apiKey,
		publicToken: publicToken,
		userID:      userID,
		isConnected: false,
		URL:         tickerURL,
	}

	return ticker
}

func (ticker *Ticker) SetRoot(url.URL) {
	ticker.URL = tickerURL
}

func (ticker *Ticker) prepareTickerURL() {
	query := ticker.URL.Query()
	query.Add("api_key", ticker.apiKey)
	query.Add("public_token", ticker.publicToken)
	query.Add("user_id", ticker.userID)
	ticker.URL.RawQuery = query.Encode()
}

func (ticker *Ticker) Connect() {
	// Add query params
	ticker.prepareTickerURL()
	conn, _, err := websocket.DefaultDialer.Dial(ticker.URL.String(), nil)
	if err != nil {
		fmt.Println("some error, shall not pass.")
		// callback: onError
	}

	// callback: onConnect
	ticker.conn = conn
	go ticker.readMessage()
}

func (ticker *Ticker) readMessage() {
	for {
		messageType, message, err := ticker.conn.ReadMessage()
		if err != nil {
			// callback: onError
			return
		}

		// callback: onMessage

		if messageType == websocket.BinaryMessage {
			fmt.Println(len(message))
			// Parse the binary data
			// callback: onTick
		}
	}
}

func (ticker *Ticker) Subscribe() {
	// ticker.conn.WriteMessage(websocket.TextMessage, []byte(`{"a":"mode","v":["quote",[265]]}`))
}

func (ticker *Ticker) Unsubscribe() {

}

func (ticker *Ticker) SetMode() {

}

func (ticker *Ticker) resubscribe() {

}

func (ticker *Ticker) parseBinary() {

}
