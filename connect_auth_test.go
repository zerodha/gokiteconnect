package kiteconnect

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
)

// TestEnctokenAuthorization verifies that enctoken is used for authorization
// when it is set and accessToken is not set.
func TestEnctokenAuthorization(t *testing.T) {
	apiKey := "test_api_key"
	client := New(apiKey)
	enctoken := "my_enc_token"
	client.SetEncToken(enctoken)

	// Activate non-default client
	httpmock.ActivateNonDefault(client.httpClient.GetClient().client)
	defer httpmock.DeactivateAndReset()

	// Verify that baseURI is updated
	if client.baseURI != "https://kite.zerodha.com/oms" {
		t.Errorf("Expected baseURI to be https://kite.zerodha.com/oms, got %s", client.baseURI)
	}

	// Mock response
	httpmock.RegisterResponder("GET", client.baseURI+"/user/profile",
		func(req *http.Request) (*http.Response, error) {
			// Verify Authorization header
			auth := req.Header.Get("Authorization")
			expected := fmt.Sprintf("enctoken %s", enctoken)
			if auth != expected {
				return nil, fmt.Errorf("Authorization header mismatch. Expected: %s, Got: %s", expected, auth)
			}
			return httpmock.NewStringResponse(200, `{"status": "success", "data": {}}`), nil
		},
	)

	// Call an API method
	_, err := client.GetUserProfile()
	if err != nil {
		t.Errorf("Failed to get user profile: %v", err)
	}
}

// TestPrecedence verifies that apiKey+accessToken takes precedence over enctoken.
func TestPrecedence(t *testing.T) {
	apiKey := "test_api_key"
	client := New(apiKey)
	accessToken := "access_token"
	client.SetAccessToken(accessToken)
	enctoken := "should_be_ignored"
	client.SetEncToken(enctoken)

	// Activate non-default client
	httpmock.ActivateNonDefault(client.httpClient.GetClient().client)
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder("GET", client.baseURI+"/user/profile",
		func(req *http.Request) (*http.Response, error) {
			// Verify Authorization header
			auth := req.Header.Get("Authorization")
			expected := fmt.Sprintf("token %s:%s", apiKey, accessToken)
			if auth != expected {
				return nil, fmt.Errorf("Authorization header mismatch. Expected: %s, Got: %s", expected, auth)
			}
			return httpmock.NewStringResponse(200, `{"status": "success", "data": {}}`), nil
		},
	)

	_, err := client.GetUserProfile()
	if err != nil {
		t.Errorf("Failed to get user profile: %v", err)
	}
}
