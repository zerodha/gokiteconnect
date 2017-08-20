package kiteconnect

import (
	"net/url"
)

type Holding struct {
	Tradingsymbol       string  `json:"tradingsymbol"`
	Exchange            string  `json:"exchange"`
	InstrumentToken     int     `json:"instrument_token"`
	ISIN                string  `json:"isin"`
	Product             string  `json:"product"`
	DayChange           float64 `json:"day_change"`
	DayChangePercentage float64 `json:"day_change_percentage"`
	Price               float64 `json:"price"`
	AveragePrice        float64 `json:"average_price"`
	LastPrice           float64 `json:"last_price"`
	ClosePrice          float64 `json:"close_price"`
	Quantity            int     `json:"quantity"`
	T1Quantity          int     `json:"t1_quantity"`
	RealisedQuantity    int     `json:"realised_quantity"`
	Pnl                 float64 `json:"pnl"`
	CollateralType      string  `json:"collateral_type"`
	CollateralQuantity  int     `json:"collateral_quantity"`
}

type Holdings []Holding

type Position struct{}

type Positions struct {
	net []Position
	day []Position
}

func (client *Client) GetHoldings() (*Holdings, error) {
	holdings := &Holdings{}
	err := client.get(URIHoldings, client.makeParams(nil), holdings)
	return holdings, err
}

func (client *Client) GetPositions() (*Positions, error) {
	positions := &Positions{}
	err := client.get(URIPositions, client.makeParams(nil), positions)
	return positions, err
}

func (client *Client) ProductModify(p url.Values) (*Position, error) {
	resp := &Position{}
	params := client.makeParams(p)
	err := client.get(URIProductModify, params, resp)
	return resp, err
}
