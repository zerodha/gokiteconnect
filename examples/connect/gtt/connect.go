package main

import (
	"context"
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

	// ==========================================
	// 1. STANDARD SINGLE GTT
	// ==========================================
	log.Println("\n--- Placing Standard Single GTT ---")
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

	order, err := kc.GetGTT(gttResp.TriggerID)
	if err != nil {
		log.Fatalf("Error getting GTT: %v", err)
	}
	log.Printf("gtt fetched: %v", order)

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
		log.Fatalf("error modifying gtt: %v", err)
	}
	log.Println("modified GTT trigger_id = ", gttModifyResp.TriggerID)

	gttDeleteResp, err := kc.DeleteGTT(gttResp.TriggerID)
	if err != nil {
		log.Fatalf("Error deleting GTT: %v", err)
	}
	log.Printf("gtt deleted: %v", gttDeleteResp)

	// ==========================================
	// 2. TRAILING SINGLE LEG GTT
	// ==========================================
	log.Println("\n--- Placing Trailing Single Leg GTT ---")
	gttTslSingleResp, err := kc.PlaceGTT(kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeSell,
		Trigger: &kiteconnect.GTTTrailingSingleLegTrigger{
			GTTSingleLegTrigger: kiteconnect.GTTSingleLegTrigger{
				TriggerParams: kiteconnect.TriggerParams{
					TriggerValue: 750,
					Quantity:     1,
					LimitPrice:   750,
				},
			},
			TrailingPoint: 5, // 5 absolute points
		},
	})
	if err != nil {
		log.Fatalf("error placing trailing single gtt: %v", err)
	}
	log.Println("placed Trailing Single GTT trigger_id = ", gttTslSingleResp.TriggerID)

	orderTslSingle, err := kc.GetGTT(gttTslSingleResp.TriggerID)
	if err != nil {
		log.Fatalf("Error getting Trailing Single GTT: %v", err)
	}
	log.Printf("trailing single gtt fetched: %v", orderTslSingle)

	log.Println("Modify existing Trailing Single GTT...")
	gttTslSingleModifyResp, err := kc.ModifyGTT(gttTslSingleResp.TriggerID, kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeSell,
		Trigger: &kiteconnect.GTTTrailingSingleLegTrigger{
			GTTSingleLegTrigger: kiteconnect.GTTSingleLegTrigger{
				TriggerParams: kiteconnect.TriggerParams{
					TriggerValue: 740,
					Quantity:     2,
					LimitPrice:   740,
				},
			},
			TrailingPoint: 10, // Modified trailing points
		},
	})
	if err != nil {
		log.Fatalf("error modifying trailing single gtt: %v", err)
	}
	log.Println("modified Trailing Single GTT trigger_id = ", gttTslSingleModifyResp.TriggerID)

	gttTslSingleDeleteResp, err := kc.DeleteGTT(gttTslSingleResp.TriggerID)
	if err != nil {
		log.Fatalf("Error deleting Trailing Single GTT: %v", err)
	}
	log.Printf("trailing single gtt deleted: %v", gttTslSingleDeleteResp)

	// ==========================================
	// 3. STANDARD OCO (TWO-LEG) GTT
	// ==========================================
	log.Println("\n--- Placing Standard OCO GTT ---")
	gttOcoResp, err := kc.PlaceGTT(kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeSell,
		Trigger: &kiteconnect.GTTOneCancelsOtherTrigger{
			Lower: kiteconnect.TriggerParams{
				TriggerValue: 750, // Stop Loss
				Quantity:     1,
				LimitPrice:   750,
			},
			Upper: kiteconnect.TriggerParams{
				TriggerValue: 850, // Target
				Quantity:     1,
				LimitPrice:   850,
			},
		},
	})
	if err != nil {
		log.Fatalf("error placing standard oco gtt: %v", err)
	}
	log.Println("placed Standard OCO GTT trigger_id = ", gttOcoResp.TriggerID)

	orderOco, err := kc.GetGTT(gttOcoResp.TriggerID)
	if err != nil {
		log.Fatalf("Error getting Standard OCO GTT: %v", err)
	}
	log.Printf("standard oco gtt fetched: %v", orderOco)

	log.Println("Modify existing Standard OCO GTT...")
	gttOcoModifyResp, err := kc.ModifyGTT(gttOcoResp.TriggerID, kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeSell,
		Trigger: &kiteconnect.GTTOneCancelsOtherTrigger{
			Lower: kiteconnect.TriggerParams{
				TriggerValue: 740,
				Quantity:     2,
				LimitPrice:   740,
			},
			Upper: kiteconnect.TriggerParams{
				TriggerValue: 860,
				Quantity:     2,
				LimitPrice:   860,
			},
		},
	})
	if err != nil {
		log.Fatalf("error modifying standard oco gtt: %v", err)
	}
	log.Println("modified Standard OCO GTT trigger_id = ", gttOcoModifyResp.TriggerID)

	gttOcoDeleteResp, err := kc.DeleteGTT(gttOcoResp.TriggerID)
	if err != nil {
		log.Fatalf("Error deleting Standard OCO GTT: %v", err)
	}
	log.Printf("standard oco gtt deleted: %v", gttOcoDeleteResp)

	// ==========================================
	// 4. TRAILING OCO (TWO-LEG) GTT
	// ==========================================
	log.Println("\n--- Placing Trailing OCO GTT ---")
	gttTslOcoResp, err := kc.PlaceGTT(kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeBuy,
		Trigger: &kiteconnect.GTTTrailingOneCancelsOtherTrigger{
			GTTOneCancelsOtherTrigger: kiteconnect.GTTOneCancelsOtherTrigger{
				Lower: kiteconnect.TriggerParams{
					TriggerValue: 750,
					Quantity:     1,
					LimitPrice:   750,
				},
				Upper: kiteconnect.TriggerParams{
					TriggerValue: 850,
					Quantity:     1,
					LimitPrice:   850,
				},
			},
			TrailingPoint: 10,
		},
	})
	if err != nil {
		log.Fatalf("error placing trailing oco gtt: %v", err)
	}
	log.Println("placed Trailing OCO GTT trigger_id = ", gttTslOcoResp.TriggerID)

	orderTslOco, err := kc.GetGTT(gttTslOcoResp.TriggerID)
	if err != nil {
		log.Fatalf("Error getting Trailing OCO GTT: %v", err)
	}
	log.Printf("trailing oco gtt fetched: %v", orderTslOco)

	log.Println("Modify existing Trailing OCO GTT...")
	gttTslOcoModifyResp, err := kc.ModifyGTT(gttTslOcoResp.TriggerID, kiteconnect.GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: kiteconnect.TransactionTypeBuy,
		Trigger: &kiteconnect.GTTTrailingOneCancelsOtherTrigger{
			GTTOneCancelsOtherTrigger: kiteconnect.GTTOneCancelsOtherTrigger{
				Lower: kiteconnect.TriggerParams{
					TriggerValue: 740,
					Quantity:     2,
					LimitPrice:   740,
				},
				Upper: kiteconnect.TriggerParams{
					TriggerValue: 860,
					Quantity:     2,
					LimitPrice:   860,
				},
			},
			TrailingPoint: 15, // Modified the trailing point
		},
	})
	if err != nil {
		log.Fatalf("error modifying trailing oco gtt: %v", err)
	}
	log.Println("modified Trailing OCO GTT trigger_id = ", gttTslOcoModifyResp.TriggerID)

	gttTslOcoDeleteResp, err := kc.DeleteGTT(gttTslOcoResp.TriggerID)
	if err != nil {
		log.Fatalf("Error deleting Trailing OCO GTT: %v", err)
	}
	log.Printf("trailing oco gtt deleted: %v", gttTslOcoDeleteResp)
}
