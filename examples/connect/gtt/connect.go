package main

import (
	"context"
	"log"
	"net/http"

	kiteconnect "github.com/zerodhatech/gokiteconnect"
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
	log.Println(kc.GetLoginURL())

	// Obtained request token after Kite Connect login flow
	srv := &http.Server{Addr: ":8080"}
	http.HandleFunc("/api/user/callback/kite/", func(w http.ResponseWriter, r *http.Request) {
		requestToken = r.URL.Query()["request_token"][0]
		go srv.Shutdown(context.TODO())
		w.Write([]byte("login successful!"))
		return
	})
	srv.ListenAndServe()

	// Get user details and access token
	data, err := kc.GenerateSession(requestToken, apiSecret)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	// Set access token
	kc.SetAccessToken(data.AccessToken)

	log.Println("Fetching GTTs...")
	orders, err := kc.GetGTTs()
	if err != nil {
		log.Fatalf("Error getting GTTs: %v", err)
	}
	log.Printf("gtt: %v", orders)

	log.Println("Placing GTT...")
	// Place GTT
	gttResp, err := kc.PlaceGTT(kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeBuy,
		Trigger: &kiteconnect.GTTSingleLegTrigger{
			TriggerParams: kiteconnect.TriggerParams{
				TriggerValue: 1,
				Quantity:     1,
				LimitPrice:   1,
			},
		},
	})
	if err != nil {
		log.Fatalf("error placing gtt: %v", err)
	}

	log.Println("placed GTT trigger_id = ", gttResp.TriggerID)

	log.Println("Fetching details of placed GTT...")

	order, err := kc.GetGTT(gttResp.TriggerID)
	if err != nil {
		log.Fatalf("Error getting GTTs: %v", err)
	}
	log.Printf("gtt: %v", order)

	log.Println("Modify existing GTT...")

	gttModifyResp, err := kc.ModifyGTT(gttResp.TriggerID, kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeBuy,
		Trigger: &kiteconnect.GTTSingleLegTrigger{
			TriggerParams: kiteconnect.TriggerParams{
				TriggerValue: 2,
				Quantity:     2,
				LimitPrice:   2,
			},
		},
	})
	if err != nil {
		log.Fatalf("error placing gtt: %v", err)
	}

	log.Println("modified GTT trigger_id = ", gttModifyResp.TriggerID)

	gttDeleteResp, err := kc.DeleteGTT(gttResp.TriggerID)
	if err != nil {
		log.Fatalf("Error getting GTTs: %v", err)
	}
	log.Printf("gtt deleted: %v", gttDeleteResp)
}
