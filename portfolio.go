package kiteconnect

import (
	"net/url"
)

type Holding struct {
	Tradingsymbol   string `json:"tradingsymbol"`
	InstrumentToken int    `json:"instrument_token"`
	Exchange        string `json:"exchange"`
	ISIN            string `json:"isin"`
	Product         string `json:"product"`

	Quantity           int    `json:"quantity"`
	T1Quantity         int    `json:"t1_quantity"`
	RealisedQuantity   int    `json:"realised_quantity"`
	CollateralType     string `json:"collateral_type"`
	CollateralQuantity int    `json:"collateral_quantity"`

	DayChange           float64 `json:"day_change"`
	DayChangePercentage float64 `json:"day_change_percentage"`

	Price        float64 `json:"price"`
	LastPrice    float64 `json:"last_price"`
	ClosePrice   float64 `json:"close_price"`
	AveragePrice float64 `json:"average_price"`
	Pnl          float64 `json:"pnl"`
}

type Holdings []Holding

type Position struct {
	Tradingsymbol   string `json:"tradingsymbol"`
	InstrumentToken string `json:"instrument_token"`
	Exchange        string `json:"exchange"`
	Product         string `json:"product"`

	Multiplier        string `json:"multiplier"`
	Quantity          string `json:"quantity"`
	OvernightQuantity string `json:"overnight_quantity"`
	AveragePrice      string `json:"average_price"`
	LastPrice         string `json:"last_price"`
	ClosePrice        string `json:"close_price"`
	Pnl               string `json:"pnl"`
	Realised          string `json:"realised"`
	Unrealised        string `json:"unrealised"`
	Value             string `json:"value"`
	M2M               string `json:"m2m"`

	SellM2M          string `json:"sell_m2m"`
	SellQuantity     string `json:"sell_quantity"`
	SellValue        string `json:"sell_value"`
	SellPrice        string `json:"sell_price"`
	NetSellAmountM2M string `json:"net_sell_amount_m2m"`

	BuyM2M          string `json:"buy_m2m"`
	BuyQuantity     string `json:"buy_quantity"`
	BuyValue        string `json:"buy_value"`
	BuyPrice        string `json:"buy_price"`
	NetBuyAmountM2M string `json:"net_buy_amount_m2m"`
}

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
