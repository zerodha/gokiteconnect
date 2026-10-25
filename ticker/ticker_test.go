package kiteticker

import (
	"bytes"
	"encoding/base64"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/gokiteconnect/v4/models"
)

func TestParseTick(t *testing.T) {
	tt := []struct {
		name    string
		pkt     []byte
		expTick models.Tick
	}{
		{
			name: "quote packet",
			pkt:  loadPacket(t, "../mock_responses/ticker_quote.packet"),
			expTick: models.Tick{
				Mode:               "quote",
				InstrumentToken:    408065,
				IsTradable:         true,
				IsIndex:            false,
				Timestamp:          models.Time{},
				LastTradeTime:      models.Time{},
				LastPrice:          1573.15,
				LastTradedQuantity: 1,
				TotalBuyQuantity:   256511,
				TotalSellQuantity:  360503,
				VolumeTraded:       1175986,
				TotalBuy:           0,
				TotalSell:          0,
				AverageTradePrice:  1570.33,
				OI:                 0,
				OIDayHigh:          0,
				OIDayLow:           0,
				NetChange:          0,
				OHLC: models.OHLC{
					Open:  1569.15,
					High:  1575,
					Low:   1561.05,
					Close: 1567.8,
				},
				Depth: models.Depth{},
			},
		},
		{
			name: "full packet",
			pkt:  loadPacket(t, "../mock_responses/ticker_full.packet"),
			expTick: models.Tick{
				Mode:               "full",
				InstrumentToken:    408065,
				IsTradable:         true,
				IsIndex:            false,
				Timestamp:          models.Time{Time: time.Unix(1625461887, 0)},
				LastTradeTime:      models.Time{Time: time.Unix(1625461887, 0)},
				LastPrice:          1573.7,
				LastTradedQuantity: 7,
				TotalBuyQuantity:   256443,
				TotalSellQuantity:  363009,
				VolumeTraded:       1192471,
				TotalBuy:           0,
				TotalSell:          0,
				AverageTradePrice:  1570.37,
				OI:                 0,
				OIDayHigh:          0,
				OIDayLow:           0,
				NetChange:          5.900000000000091,
				OHLC: models.OHLC{
					Open:  1569.15,
					High:  1575,
					Low:   1561.05,
					Close: 1567.8,
				},
				Depth: models.Depth{
					Buy: [5]models.DepthItem{
						{Price: 1573.4, Quantity: 5, Orders: 1},
						{Price: 1573, Quantity: 140, Orders: 2},
						{Price: 1572.95, Quantity: 2, Orders: 1},
						{Price: 1572.9, Quantity: 219, Orders: 7},
						{Price: 1572.85, Quantity: 50, Orders: 1},
					},
					Sell: [5]models.DepthItem{
						{Price: 1573.7, Quantity: 172, Orders: 3},
						{Price: 1573.75, Quantity: 44, Orders: 3},
						{Price: 1573.85, Quantity: 302, Orders: 3},
						{Price: 1573.9, Quantity: 141, Orders: 2},
						{Price: 1573.95, Quantity: 724, Orders: 5},
					},
				},
			},
		},
	}

	for _, tc := range tt {
		tick, err := parsePacket([]byte(tc.pkt))
		require.Nil(t, err)

		require.Equal(t, tc.expTick, tick)
	}
}

func loadPacket(t *testing.T, fname string) []byte {
	file, err := ioutil.ReadFile(fname)
	require.Nil(t, err)

	pkt, err := base64.StdEncoding.DecodeString(string(file))
	require.Nil(t, err)

	return pkt
}

func TestTickerSetDebug(t *testing.T) {
	ticker := New("test_api_key", "test_access_token")

	// Check if default debug is false
	require.False(t, ticker.debug, "default debug should be false")

	// Set custom debug to true
	require.NotPanics(t, func() {
		ticker.SetDebug(true)
	})
	require.True(t, ticker.debug, "debug should be true after SetDebug(true)")

	// Set custom debug to false
	require.NotPanics(t, func() {
		ticker.SetDebug(false)
	})
	require.False(t, ticker.debug, "debug should be false after SetDebug(false)")
}

func TestTickerDebugLogging(t *testing.T) {
	ticker := New("test_api_key", "test_access_token")

	var logBuf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOutput)

	// When debug is false (default), Resubscribe should not emit logs
	require.False(t, ticker.debug)
	err := ticker.Resubscribe()
	require.NoError(t, err)
	require.Empty(t, logBuf.String(), "should not produce log output when debug is false")

	// Enable debug and verify log output is captured
	ticker.SetDebug(true)
	logBuf.Reset()
	err = ticker.Resubscribe()
	require.NoError(t, err)
	require.Contains(t, logBuf.String(), "Subscribe again:", "should produce log output when debug is true")

	// Disable debug again and verify no logs are emitted
	ticker.SetDebug(false)
	logBuf.Reset()
	err = ticker.Resubscribe()
	require.NoError(t, err)
	require.Empty(t, logBuf.String(), "should not produce log output after SetDebug(false)")
}

func TestTickerDebugLoggingWithTokens(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				break
			}
		}
	}))
	defer s.Close()

	u, err := url.Parse(s.URL)
	require.NoError(t, err)
	u.Scheme = "ws"

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	require.NoError(t, err)
	defer conn.Close()

	ticker := New("test_api_key", "test_access_token")
	ticker.Conn = conn
	ticker.subscribedTokens[12345] = ModeFull

	var logBuf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOutput)

	// Test with debug = true and tokens present
	ticker.SetDebug(true)
	err = ticker.Resubscribe()
	require.NoError(t, err)
	require.Contains(t, logBuf.String(), "Subscribe again:  [12345] map[12345:full]")

	// Test with debug = false
	ticker.SetDebug(false)
	logBuf.Reset()
	err = ticker.Resubscribe()
	require.NoError(t, err)
	require.Empty(t, logBuf.String(), "should not produce log output when debug is false")
}
