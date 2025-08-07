package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

const (
	apiKey    string = "my_api_key"
	apiSecret string = "my_api_secret"
)

func main() {
	// Create a new Kite connect instance
	kc := kiteconnect.New(apiKey)

	var (
		requestToken string
	)

	// Login URL from which request token can be obtained
	fmt.Println("Open the following url in your browser:\n", kc.GetLoginURL())

	// Obtain request token after Kite Connect login flow
	// Run a temporary server to listen for callback
	srv := &http.Server{Addr: ":8080"}
	http.HandleFunc("/api/user/callback/kite/", func(w http.ResponseWriter, r *http.Request) {
		requestToken = r.URL.Query()["request_token"][0]
		log.Println("request token", requestToken)
		go srv.Shutdown(context.TODO())
		w.Write([]byte("login successful!"))
	})
	srv.ListenAndServe()

	// Get user details and access token
	data, err := kc.GenerateSession(requestToken, apiSecret)
	if err != nil {
		fmt.Printf("Error: %v", err)
		return
	}

	// Set access token
	kc.SetAccessToken(data.AccessToken)
	log.Println("data.AccessToken", data.AccessToken)

	// Get margins
	margins, err := kc.GetUserMargins()
	if err != nil {
		fmt.Printf("Error getting margins: %v", err)
	}
	fmt.Println("margins: ", margins)

	// Example: Place a MARKET order with market protection
	orderParams := kiteconnect.OrderParams{
		Exchange:         "NSE",
		Tradingsymbol:    "TATASTEEL",
		TransactionType:  "BUY",
		OrderType:        "MARKET",
		Quantity:         1,
		Product:          "CNC",
		Validity:         "DAY",
		MarketProtection: 2.0, // 2% market protection
		Tag:              "market_protection_example",
	}

	// Place the order
	orderResponse, err := kc.PlaceOrder("regular", orderParams)
	if err != nil {
		fmt.Printf("Error placing order with market protection: %v", err)
	} else {
		fmt.Printf("Order placed successfully with market protection! Order ID: %s\n", orderResponse.OrderID)
	}

	// Example: Place a MARKET order with autoslice for large quantities
	autosliceParams := kiteconnect.OrderParams{
		Exchange:        "NFO",
		Tradingsymbol:   "NIFTY25AUGFUT",
		TransactionType: "BUY",
		OrderType:       "MARKET",
		Quantity:        2625, // Large quantity that might need slicing
		Product:         "NRML",
		Validity:        "DAY",
		Autoslice:       true, // Enable automatic slicing
		Tag:             "autoslice_example",
	}

	// Place the autoslice order
	autosliceResponse, err := kc.PlaceOrder("regular", autosliceParams)
	if err != nil {
		fmt.Printf("Error placing order with autoslice: %v", err)
	} else {
		fmt.Printf("Order placed successfully with autoslice! Order ID: %s\n", autosliceResponse.OrderID)
	}
}
