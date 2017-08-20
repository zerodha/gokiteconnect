package kiteconnect

import "testing"

func TestNew(t *testing.T) {
	apiKey := "api_key"
	apiSecret := "api_secret"
	client := New(apiKey, apiSecret)

	if client.apiKey != apiKey {
		t.Errorf("Api key is not assigned properly.")
	}

	if client.apiSecret != apiSecret {
		t.Errorf("Api secret is not assigned properly.")
	}

	if client.timeout != timeout {
		t.Errorf("Timeout is not set to default.")
	}
}

func TestClientSetDebug(t *testing.T) {
	apiKey := "kitefront"
	apiSecret := "api_secret"
	client := New(apiKey, apiSecret)
	client.SetDebug(true)

	if client.debug != true {
		t.Errorf("Debug is not set properly.")
	}
}
