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
		return
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

	// Alerts examples
	// Create alert
	alert, err := kc.CreateAlert(kiteconnect.AlertParams{
		Name:             "NIFTY 50 Alert",
		Type:             kiteconnect.AlertTypeSimple,
		LHSExchange:      "INDICES",
		LHSTradingSymbol: "NIFTY 50",
		LHSAttribute:     "LastTradedPrice",
		Operator:         kiteconnect.AlertOperatorGE,
		RHSType:          "constant",
		RHSConstant:      27000,
	})
	if err != nil {
		log.Printf("Error creating alert: %v", err)
		return
	}
	fmt.Printf("Created alert: %s (UUID: %s)\n", alert.Name, alert.UUID)

	// Get all alerts
	alerts, err := kc.GetAlerts(nil)
	if err != nil {
		log.Printf("Error fetching alerts: %v", err)
		return
	}
	fmt.Printf("Found %d alerts:\n", len(alerts))
	for _, a := range alerts {
		fmt.Printf("- %s (UUID: %s, Status: %s)\n", a.Name, a.UUID, a.Status)
	}

	// Modify an alert
	modifiedAlert, err := kc.ModifyAlert(alerts[0].UUID, kiteconnect.AlertParams{
		Name:             "Modified NIFTY 50 Alert",
		Type:             kiteconnect.AlertTypeSimple,
		LHSExchange:      "INDICES",
		LHSTradingSymbol: "NIFTY 50",
		LHSAttribute:     "LastTradedPrice",
		Operator:         kiteconnect.AlertOperatorLE,
		RHSType:          "constant",
		RHSConstant:      28000,
	})
	if err != nil {
		log.Printf("Error modifying alert: %v", err)
	} else {
		fmt.Printf("Modified alert: %s (New threshold: %v)\n", modifiedAlert.Name, modifiedAlert.RHSConstant)
	}

	// Delete single alert
	err = kc.DeleteAlerts(alerts[0].UUID)
	if err != nil {
		log.Printf("Error deleting alert: %v", err)
	} else {
		fmt.Println("Alert deleted successfully")
	}
}
