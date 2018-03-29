package kiteconnect

import (
	"net/http"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	apiKey := "api_key"
	client := New(apiKey)

	if client.apiKey != apiKey {
		t.Errorf("Api key is not assigned properly.")
	}
}

func TestClientSetters(t *testing.T) {
	apiKey := "kitefront"
	client := New(apiKey)

	customDebug := true
	customBaseURI := "test"
	customTimeout := 1000 * time.Millisecond
	customAccessToken := "someaccesstoken"
	customHTTPClientTimeout := time.Duration(2000)
	customHTTPClient := &http.Client{
		Timeout: customHTTPClientTimeout,
	}

	// Set custom debug
	client.SetDebug(customDebug)
	if client.debug != customDebug {
		t.Errorf("Debug is not set properly.")
	}

	// Set custom base URI
	client.SetBaseURI(customBaseURI)
	if client.baseURI != customBaseURI {
		t.Errorf("Base URI is not set properly.")
	}

	// Set custom timeout for default http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}

	// Set access token
	client.SetAccessToken(customAccessToken)
	if client.accessToken != customAccessToken {
		t.Errorf("Access token is not set properly.")
	}

	// Set custom HTTP Client
	client.SetHTTPClient(customHTTPClient)
	if client.httpClient.GetClient() != customHTTPClient {
		t.Errorf("Custom HTTPClient is not set properly.")
	}

	// Set timeout for custo http client
	if client.httpClient.GetClient().Timeout != customHTTPClientTimeout {
		t.Errorf("Custom HTTPClient timeout is not set properly.")
	}

	// Set custom timeout for custom http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}
}
