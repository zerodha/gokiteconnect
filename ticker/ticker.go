package kiteticker

import (
	"fmt"
	"net/url"
	"reflect"

	"github.com/gorilla/websocket"
)

type TickerCallbacks struct {
	onTick        func(Tick)
	onMessage     func(int, []byte)
	onNoReconnect func(string)
	onReconnect   func(string)
	onConnect     func()
	onClose       func(string)
	onError       func(error)
}

type Ticker struct {
	apiKey      string
	publicToken string
	userID      string
	isConnected bool

	URL       url.URL
	Conn      *websocket.Conn
	callbacks TickerCallbacks
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

// The message types are defined in RFC 6455, section 11.8.
const (
	// TextMessage denotes a text data message. The text message payload is
	// interpreted as UTF-8 encoded text data.
	TextMessage = 1

	// BinaryMessage denotes a binary data message.
	BinaryMessage = 2

	// CloseMessage denotes a close control message. The optional message
	// payload contains a numeric code and text. Use the FormatCloseMessage
	// function to format a close message payload.
	CloseMessage = 8

	// PingMessage denotes a ping control message. The optional message payload
	// is UTF-8 encoded text.
	PingMessage = 9

	// PongMessage denotes a ping control message. The optional message payload
	// is UTF-8 encoded text.
	PongMessage = 10
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

func (t *Ticker) SetRoot(url.URL) {
	t.URL = tickerURL
}

func (t *Ticker) RegisterCallbacks(callbacks TickerCallbacks) {
	t.callbacks = callbacks
}

func (t *Ticker) prepareTickerURL() {
	query := t.URL.Query()
	query.Add("api_key", t.apiKey)
	query.Add("public_token", t.publicToken)
	query.Add("user_id", t.userID)
	t.URL.RawQuery = query.Encode()
}

func (t *Ticker) Connect() {
	// Prepare ticker URL with required params
	t.prepareTickerURL()
	conn, _, err := websocket.DefaultDialer.Dial(t.URL.String(), nil)
	if err != nil {
		// callback: onError
		t.onError(err)
	}

	// callback: onConnect
	t.Conn = conn
	if t.callbacks.onConnect != nil {
		t.callbacks.onConnect()
	}

	// Receive ticker data
	go t.readMessage()
}

func (t *Ticker) onError(err error) {
	if t.callbacks.onError != nil {
		t.callbacks.onError(err)
	}
}

func (t *Ticker) onConnect() {
	if t.callbacks.onConnect != nil {
		t.callbacks.onConnect()
	}
}

func (t *Ticker) onMessage(messageType int, message []byte) {
	if t.callbacks.onMessage != nil {
		t.callbacks.onMessage(messageType, message)
	}
}

func (t *Ticker) readMessage() {
	for {
		messageType, message, err := t.Conn.ReadMessage()
		if err != nil {
			// callback: onError
			t.onError(err)
			return
		}

		fmt.Println(messageType, len(message), reflect.TypeOf(message))

		// callback: onMessage
		t.onMessage(messageType, message)

		if messageType == websocket.BinaryMessage {
			// fmt.Println(len(message))
			// Parse the binary data
			// callback: onTick
		}
	}
}

func (t *Ticker) Subscribe() {
	t.Conn.WriteMessage(websocket.PingMessage, []byte(""))
}

func (t *Ticker) Unsubscribe() {}

func (t *Ticker) SetMode() {}

func (t *Ticker) resubscribe() {}

func (t *Ticker) parseBinary() {}
