// Package kiteticker provides kite ticker access using callbacks.
package kiteticker

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	kiteconnect "github.com/zerodhatech/gokiteconnect/v3"
)

// OHLC represents OHLC packets.
type OHLC struct {
	InstrumentToken uint32
	Open            float64
	High            float64
	Low             float64
	Close           float64
}

// LTP represents OHLC packets.
type LTP struct {
	InstrumentToken uint32
	LastPrice       float64
}

// DepthItem represents a single market depth entry.
type DepthItem struct {
	Price    float64
	Quantity uint32
	Orders   uint32
}

// Depth represents a group of buy/sell market depths.
type Depth struct {
	Buy  [5]DepthItem
	Sell [5]DepthItem
}

// Tick represents a single packet in the market feed.
type Tick struct {
	Mode            Mode
	InstrumentToken uint32
	IsTradable      bool
	IsIndex         bool

	Timestamp          kiteconnect.Time
	LastTradeTime      kiteconnect.Time
	LastPrice          float64
	LastTradedQuantity uint32
	TotalBuyQuantity   uint32
	TotalSellQuantity  uint32
	VolumeTraded       uint32
	TotalBuy           uint32
	TotalSell          uint32
	AverageTradePrice  float64
	OI                 uint32
	OIDayHigh          uint32
	OIDayLow           uint32
	NetChange          float64

	OHLC  OHLC
	Depth Depth
}

// Ticker is a Kite connect ticker instance.
type Ticker struct {
	Conn *websocket.Conn

	apiKey      string
	accessToken string

	url                 url.URL
	callbacks           callbacks
	lastPingTime        time.Time
	autoReconnect       bool
	reconnectMaxRetries int
	reconnectMaxDelay   time.Duration
	connectTimeout      time.Duration

	reconnectAttempt int

	subscribedTokens map[uint32]Mode
}

// Mode represents available ticker modes.
type Mode string

// callbacks represents callbacks available in ticker.
type callbacks struct {
	onTick        func(Tick)
	onMessage     func(int, []byte)
	onNoReconnect func(int)
	onReconnect   func(int, time.Duration)
	onConnect     func()
	onClose       func(int, string)
	onError       func(error)
	onOrderUpdate func(kiteconnect.Order)
}

type tickerInput struct {
	Type string      `json:"a"`
	Val  interface{} `json:"v"`
}

type message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

const (
	// Segment constants.
	NseCM = 1 + iota
	NseFO
	NseCD
	BseCM
	BseFO
	BseCD
	McxFO
	McxSX
	Indices

	// ModeLTP subscribes for last price.
	ModeLTP Mode = "ltp"
	// ModeFull subscribes for all the available fields.
	ModeFull Mode = "full"
	// ModeQuote represents quote mode.
	ModeQuote Mode = "quote"

	// Mode empty is used internally for storing tokens which doesn't have any modes
	modeEmpty Mode = "empty"

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

	// packet length for each mode.
	modeLTPLength              = 8
	modeQuoteIndexPacketLength = 28
	modeFullIndexLength        = 32
	modeQuoteLength            = 44
	modeFullLength             = 184

	// Message types
	messageError = "error"
	messageOrder = "order"

	// Auto reconnect defaults
	// Default maximum number of reconnect attempts
	defaultReconnectMaxAttempts = 300
	// Auto reconnect min delay. Reconnect delay can't be less than this.
	reconnectMinDelay time.Duration = 5000 * time.Millisecond
	// Default auto reconnect delay to be used for auto reconnection.
	defaultReconnectMaxDelay time.Duration = 60000 * time.Millisecond
	// Connect timeout for initial server handshake.
	defaultConnectTimeout time.Duration = 7000 * time.Millisecond
	// Interval in which the connection check is performed periodically.
	connectionCheckInterval time.Duration = 2000 * time.Millisecond
	// Interval which is used to determine if the connection is still active. If last ping time exceeds this then
	// connection is considered as dead and reconnection is initiated.
	dataTimeoutInterval time.Duration = 5000 * time.Millisecond
)

var (
	// Default ticker url.
	tickerURL = url.URL{Scheme: "wss", Host: "ws.kite.trade"}
)

// New creates a new ticker instance.
func New(apiKey string, accessToken string) *Ticker {
	ticker := &Ticker{
		apiKey:              apiKey,
		accessToken:         accessToken,
		url:                 tickerURL,
		autoReconnect:       true,
		reconnectMaxDelay:   defaultReconnectMaxDelay,
		reconnectMaxRetries: defaultReconnectMaxAttempts,
		connectTimeout:      defaultConnectTimeout,
		subscribedTokens:    map[uint32]Mode{},
	}

	return ticker
}

// SetRootURL sets ticker root url.
func (t *Ticker) SetRootURL(u url.URL) {
	t.url = u
}

// SetAccessToken set access token.
func (t *Ticker) SetAccessToken(aToken string) {
	t.accessToken = aToken
}

// SetConnectTimeout sets default timeout for initial connect handshake
func (t *Ticker) SetConnectTimeout(val time.Duration) {
	t.connectTimeout = val
}

// SetAutoReconnect enable/disable auto reconnect.
func (t *Ticker) SetAutoReconnect(val bool) {
	t.autoReconnect = val
}

// SetReconnectMaxDelay sets maximum auto reconnect delay.
func (t *Ticker) SetReconnectMaxDelay(val time.Duration) error {
	if val > reconnectMinDelay {
		return fmt.Errorf("ReconnectMaxDelay can't be less than %fms", reconnectMinDelay.Seconds()*1000)
	}

	t.reconnectMaxDelay = val
	return nil
}

// SetReconnectMaxRetries sets maximum reconnect attempts.
func (t *Ticker) SetReconnectMaxRetries(val int) {
	t.reconnectMaxRetries = val
}

// OnConnect callback.
func (t *Ticker) OnConnect(f func()) {
	t.callbacks.onConnect = f
}

// OnError callback.
func (t *Ticker) OnError(f func(err error)) {
	t.callbacks.onError = f
}

// OnClose callback.
func (t *Ticker) OnClose(f func(code int, reason string)) {
	t.callbacks.onClose = f
}

// OnMessage callback.
func (t *Ticker) OnMessage(f func(messageType int, message []byte)) {
	t.callbacks.onMessage = f
}

// OnReconnect callback.
func (t *Ticker) OnReconnect(f func(attempt int, delay time.Duration)) {
	t.callbacks.onReconnect = f
}

// OnNoReconnect callback.
func (t *Ticker) OnNoReconnect(f func(attempt int)) {
	t.callbacks.onNoReconnect = f
}

// OnTick callback.
func (t *Ticker) OnTick(f func(tick Tick)) {
	t.callbacks.onTick = f
}

// OnOrderUpdate callback.
func (t *Ticker) OnOrderUpdate(f func(order kiteconnect.Order)) {
	t.callbacks.onOrderUpdate = f
}

// Serve starts the connection to ticker server. Since its blocking its recommended to use it in go routine.
func (t *Ticker) Serve() {
	for {
		// If reconnect attempt exceeds max then close the loop
		if t.reconnectAttempt > t.reconnectMaxRetries {
			t.triggerNoReconnect(t.reconnectAttempt)
			return
		}

		// If its a reconnect then wait exponentially based on reconnect attempt
		if t.reconnectAttempt > 0 {
			nextDelay := time.Duration(math.Pow(2, float64(t.reconnectAttempt))) * time.Second
			if nextDelay > t.reconnectMaxDelay {
				nextDelay = t.reconnectMaxDelay
			}

			t.triggerReconnect(t.reconnectAttempt, nextDelay)

			time.Sleep(nextDelay)

			// Close the previous connection if exists
			if t.Conn != nil {
				t.Conn.Close()
			}
		}

		// Prepare ticker URL with required params.
		q := t.url.Query()
		q.Set("api_key", t.apiKey)
		q.Set("access_token", t.accessToken)
		t.url.RawQuery = q.Encode()

		// create a dialer
		d := websocket.DefaultDialer
		d.HandshakeTimeout = t.connectTimeout
		conn, _, err := d.Dial(t.url.String(), nil)
		if err != nil {
			t.triggerError(err)

			// If auto reconnect is enabled then try reconneting else return error
			if t.autoReconnect {
				t.reconnectAttempt++
				continue
			}
		}

		// Close the connection when its done.
		defer t.Conn.Close()

		// Assign the current connection to the instance.
		t.Conn = conn

		// Trigger connect callback.
		t.triggerConnect()

		// Resubscribe to stored tokens
		if t.reconnectAttempt > 0 {
			t.Resubscribe()
		}

		// Reset auto reconnect vars
		t.reconnectAttempt = 0

		// Set current time as last ping time
		t.lastPingTime = time.Now()

		// Set on close handler
		t.Conn.SetCloseHandler(t.handleClose)

		var wg sync.WaitGroup

		// Receive ticker data in a go routine.
		wg.Add(1)
		go t.readMessage(&wg)

		// Run watcher to check last ping time and reconnect if required
		if t.autoReconnect {
			wg.Add(1)
			go t.checkConnection(&wg)
		}

		// Wait for go routines to finish before doing next reconnect
		wg.Wait()
	}
}

func (t *Ticker) handleClose(code int, reason string) error {
	t.triggerClose(code, reason)
	return nil
}

// Trigger callback methods
func (t *Ticker) triggerError(err error) {
	if t.callbacks.onError != nil {
		t.callbacks.onError(err)
	}
}

func (t *Ticker) triggerClose(code int, reason string) {
	if t.callbacks.onClose != nil {
		t.callbacks.onClose(code, reason)
	}
}

func (t *Ticker) triggerConnect() {
	if t.callbacks.onConnect != nil {
		t.callbacks.onConnect()
	}
}

func (t *Ticker) triggerReconnect(attempt int, delay time.Duration) {
	if t.callbacks.onReconnect != nil {
		t.callbacks.onReconnect(attempt, delay)
	}
}

func (t *Ticker) triggerNoReconnect(attempt int) {
	if t.callbacks.onNoReconnect != nil {
		t.callbacks.onNoReconnect(attempt)
	}
}

func (t *Ticker) triggerMessage(messageType int, message []byte) {
	if t.callbacks.onMessage != nil {
		t.callbacks.onMessage(messageType, message)
	}
}

func (t *Ticker) triggerTick(tick Tick) {
	if t.callbacks.onTick != nil {
		t.callbacks.onTick(tick)
	}
}

func (t *Ticker) triggerOrderUpdate(order kiteconnect.Order) {
	if t.callbacks.onOrderUpdate != nil {
		t.callbacks.onOrderUpdate(order)
	}
}

// Periodically check for last ping time and initiate reconnect if applicable.
func (t *Ticker) checkConnection(wg *sync.WaitGroup) {
	for {
		// Sleep before doing next check
		time.Sleep(connectionCheckInterval)

		// If last ping time is greater then timeout interval then close the
		// existing connection and reconnect
		if time.Since(t.lastPingTime) > dataTimeoutInterval {
			// Close the current connection without waiting for close frame
			if t.Conn != nil {
				t.Conn.Close()
			}

			// Increase reconnect attempt for next reconnection
			t.reconnectAttempt++
			// Mark it as done in wait group
			wg.Done()
			return
		}
	}
}

// readMessage reads the data in a loop.
func (t *Ticker) readMessage(wg *sync.WaitGroup) {
	for {
		mType, msg, err := t.Conn.ReadMessage()
		if err != nil {
			t.triggerError(fmt.Errorf("Error reading data: %v", err))
			wg.Done()
			return
		}

		// Update last ping time to check for connection
		t.lastPingTime = time.Now()

		// Trigger message.
		t.triggerMessage(mType, msg)

		// If binary message then parse and send tick.
		if mType == websocket.BinaryMessage {
			ticks, err := t.parseBinary(msg)
			if err != nil {
				t.triggerError(fmt.Errorf("Error parsing data received: %v", err))
			}

			// Trigger individual tick.
			for _, tick := range ticks {
				t.triggerTick(tick)
			}
		} else if mType == websocket.TextMessage {
			t.processTextMessage(msg)
		}
	}
}

// Close tries to close the connection gracefully. If the server doesn't close it
func (t *Ticker) Close() error {
	return t.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// Subscribe subscribes tick for the given list of tokens.
func (t *Ticker) Subscribe(tokens []uint32) error {
	if len(tokens) == 0 {
		return nil
	}

	out, err := json.Marshal(tickerInput{
		Type: "subscribe",
		Val:  tokens,
	})
	if err != nil {
		return err
	}

	// Store tokens to current subscriptions
	for _, ts := range tokens {
		t.subscribedTokens[ts] = modeEmpty
	}

	return t.Conn.WriteMessage(websocket.TextMessage, out)
}

// Unsubscribe unsubscribes tick for the given list of tokens.
func (t *Ticker) Unsubscribe(tokens []uint32) error {
	if len(tokens) == 0 {
		return nil
	}

	out, err := json.Marshal(tickerInput{
		Type: "unsubscribe",
		Val:  tokens,
	})
	if err != nil {
		return err
	}

	// Remove tokens from current subscriptions
	for _, ts := range tokens {
		delete(t.subscribedTokens, ts)
	}

	return t.Conn.WriteMessage(websocket.TextMessage, out)
}

// SetMode changes mode for given list of tokens and mode.
func (t *Ticker) SetMode(mode Mode, tokens []uint32) error {
	if len(tokens) == 0 {
		return nil
	}

	out, err := json.Marshal(tickerInput{
		Type: "mode",
		Val:  []interface{}{mode, tokens},
	})
	if err != nil {
		return err
	}

	// Set mode in current subscriptions stored
	for _, ts := range tokens {
		t.subscribedTokens[ts] = mode
	}

	return t.Conn.WriteMessage(websocket.TextMessage, out)
}

// Resubscribe resubscribes to the current stored subscriptions
func (t *Ticker) Resubscribe() error {
	var tokens []uint32
	modes := map[Mode][]uint32{
		ModeFull:  []uint32{},
		ModeQuote: []uint32{},
		ModeLTP:   []uint32{},
	}

	// Make a map of mode and corresponding tokens
	for to, mo := range t.subscribedTokens {
		tokens = append(tokens, to)
		if mo != modeEmpty {
			modes[mo] = append(modes[mo], to)
		}
	}

	fmt.Println("Subscribe again: ", tokens, t.subscribedTokens)

	// Subscribe to tokens
	if len(tokens) > 0 {
		if err := t.Subscribe(tokens); err != nil {
			return err
		}
	}

	// Set mode to tokens
	for mo, tos := range modes {
		if len(tos) > 0 {
			if err := t.SetMode(mo, tos); err != nil {
				return err
			}
		}
	}

	return nil
}

func (t *Ticker) processTextMessage(inp []byte) {
	var msg message
	if err := json.Unmarshal(inp, &msg); err != nil {
		// May be error should be triggered
		return
	}

	if msg.Type == messageError {
		// Trigger text error
		t.triggerError(fmt.Errorf(msg.Data.(string)))
	} else if msg.Type == messageOrder {
		// Parse order update data
		order := struct {
			Data kiteconnect.Order `json:"data"`
		}{}

		if err := json.Unmarshal(inp, &order); err != nil {
			// May be error should be triggered
			return
		}

		t.triggerOrderUpdate(order.Data)
	}
}

// parseBinary parses the packets to ticks.
func (t *Ticker) parseBinary(inp []byte) ([]Tick, error) {
	pkts := t.splitPackets(inp)
	var ticks []Tick

	for _, pkt := range pkts {
		tick, err := t.parsePacket(pkt)
		if err != nil {
			return nil, err
		}

		ticks = append(ticks, tick)
	}

	return ticks, nil
}

// splitPackets splits packet dump to individual tick packet.
func (t *Ticker) splitPackets(inp []byte) [][]byte {
	var pkts [][]byte
	if len(inp) < 2 {
		return pkts
	}

	pktLen := binary.BigEndian.Uint16(inp[0:2])

	j := 2
	for i := 0; i < int(pktLen); i++ {
		pLen := binary.BigEndian.Uint16(inp[j : j+2])
		pkts = append(pkts, inp[j+2:j+2+int(pLen)])
		j = j + 2 + int(pLen)
	}

	return pkts
}

// Parse parses a tick byte array into a tick struct.
func (t *Ticker) parsePacket(b []byte) (Tick, error) {
	var (
		tk         = binary.BigEndian.Uint32(b[0:4])
		seg        = tk & 0xFF
		isIndex    = seg == Indices
		isTradable = seg != Indices
	)

	// Mode LTP parsing
	if len(b) == modeLTPLength {
		return Tick{
			Mode:            ModeLTP,
			InstrumentToken: tk,
			IsTradable:      isTradable,
			IsIndex:         isIndex,
			LastPrice:       t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[4:8]))),
		}, nil
	}

	// Parse index mode full and mode quote data
	if len(b) == modeQuoteIndexPacketLength || len(b) == modeFullIndexLength {
		var (
			lastPrice  = t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[4:8])))
			closePrice = t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[20:24])))
		)

		tick := Tick{
			Mode:            ModeQuote,
			InstrumentToken: tk,
			IsTradable:      isTradable,
			IsIndex:         isIndex,
			LastPrice:       lastPrice,
			NetChange:       lastPrice - closePrice,
			OHLC: OHLC{
				High:  t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[8:12]))),
				Low:   t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[12:16]))),
				Open:  t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[16:20]))),
				Close: closePrice,
			}}

		// On mode full set timestamp
		if len(b) == modeFullIndexLength {
			tick.Mode = ModeFull
			tick.Timestamp = kiteconnect.Time{time.Unix(int64(binary.BigEndian.Uint32(b[28:32])), 0)}
		}

		return tick, nil
	}

	// Parse mode quote.
	var (
		lastPrice  = t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[4:8])))
		closePrice = t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[40:44])))
	)

	// Mode quote data.
	tick := Tick{
		Mode:               ModeQuote,
		InstrumentToken:    tk,
		IsTradable:         isTradable,
		IsIndex:            isIndex,
		LastPrice:          lastPrice,
		LastTradedQuantity: binary.BigEndian.Uint32(b[8:12]),
		AverageTradePrice:  t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[12:16]))),
		VolumeTraded:       binary.BigEndian.Uint32(b[16:20]),
		TotalBuyQuantity:   binary.BigEndian.Uint32(b[20:24]),
		TotalSellQuantity:  binary.BigEndian.Uint32(b[24:28]),
		OHLC: OHLC{
			Open:  t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[28:32]))),
			High:  t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[32:36]))),
			Low:   t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[36:40]))),
			Close: closePrice,
		},
	}

	// Parse full mode.
	if len(b) == modeFullLength {
		tick.Mode = ModeFull
		tick.LastTradeTime = kiteconnect.Time{time.Unix(int64(binary.BigEndian.Uint32(b[44:48])), 0)}
		tick.OI = binary.BigEndian.Uint32(b[48:52])
		tick.OIDayHigh = binary.BigEndian.Uint32(b[52:56])
		tick.OIDayLow = binary.BigEndian.Uint32(b[56:60])
		tick.Timestamp = kiteconnect.Time{time.Unix(int64(binary.BigEndian.Uint32(b[60:64])), 0)}
		tick.NetChange = lastPrice - closePrice

		// Depth Information.
		var (
			buyPos     = 64
			sellPos    = 124
			depthItems = (sellPos - buyPos) / 12
		)

		for i := 0; i < depthItems; i++ {
			tick.Depth.Buy[i] = DepthItem{
				Quantity: binary.BigEndian.Uint32(b[buyPos : buyPos+4]),
				Price:    t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[buyPos+4:buyPos+8]))),
				Orders:   uint32(binary.BigEndian.Uint16(b[buyPos+8 : buyPos+10])),
			}

			tick.Depth.Sell[i] = DepthItem{
				Quantity: binary.BigEndian.Uint32(b[sellPos : sellPos+4]),
				Price:    t.convertPrice(seg, float64(binary.BigEndian.Uint32(b[sellPos+4:sellPos+8]))),
				Orders:   uint32(binary.BigEndian.Uint16(b[sellPos+8 : sellPos+10])),
			}

			buyPos += 12
			sellPos += 12
		}
	}

	return tick, nil
}

// convertPrice converts prices of stocks from paise to rupees
// with varying decimals based on the segment.
func (t *Ticker) convertPrice(seg uint32, val float64) float64 {
	if seg == NseCD {
		return val / 10000000.0
	}

	return val / 100.0
}
