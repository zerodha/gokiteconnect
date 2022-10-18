package kiteticker

import (
	"encoding/base64"
	"os"
	"testing"
	"time"

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
		tick, err := parsePacket(tc.pkt)
		require.Nil(t, err)

		require.Equal(t, tc.expTick, tick)
	}
}

func loadPacket(t *testing.T, fname string) []byte {
	file, err := os.ReadFile(fname)
	require.Nil(t, err)

	pkt, err := base64.StdEncoding.DecodeString(string(file))
	require.Nil(t, err)

	return pkt
}
