package main

import (
	"context"
	"fmt"
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
	fmt.Println("Open the following url in your browser:\n", kc.GetLoginURL())

	// Obtain request token after Kite Connect login flow
	// Run a temporary server to listen for callback
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
		fmt.Printf("Error: %v", err)
		return
	}

	// Set access token
	kc.SetAccessToken(data.AccessToken)

	// Get margins
	margins, err := kc.GetUserMargins()
	if err != nil {
		fmt.Printf("Error getting margins: %v", err)
	}
	fmt.Println("margins: ", margins)
}