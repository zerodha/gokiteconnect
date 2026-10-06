package kiteticker

import (
	"testing"
)

// TestTickerEnctoken verifies that enctoken is used in the query params.
func TestTickerEnctoken(t *testing.T) {
	apiKey := "test_api_key"
	accessToken := "test_access_token"
	ticker := New(apiKey, accessToken)
	userID := "test_user_id"
	enctoken := "my_enc_token"
	ticker.SetEncToken(userID, enctoken)

	// Simulate ServeWithContext logic to populate params
	q := ticker.url.Query()
	if ticker.enctoken != "" {
		q.Set("enctoken", ticker.enctoken)
	} else {
		q.Set("api_key", ticker.apiKey)
		q.Set("access_token", ticker.accessToken)
	}
	ticker.url.RawQuery = q.Encode()
	// end simulation

	if ticker.url.Query().Get("enctoken") != enctoken {
		t.Errorf("Expected enctoken to be %s, got %s", enctoken, ticker.url.Query().Get("enctoken"))
	}

	if ticker.url.Query().Get("api_key") != "" {
		t.Errorf("Expected api_key to be empty, got %s", ticker.url.Query().Get("api_key"))
	}
}

// TestTickerDefaultAuth verifies that default auth is used when enctoken is not set.
func TestTickerDefaultAuth(t *testing.T) {
	apiKey := "test_api_key"
	accessToken := "test_access_token"
	ticker := New(apiKey, accessToken)

	// Simulate ServeWithContext logic to populate params
	q := ticker.url.Query()
	if ticker.enctoken != "" {
		q.Set("enctoken", ticker.enctoken)
	} else {
		q.Set("api_key", ticker.apiKey)
		q.Set("access_token", ticker.accessToken)
	}
	ticker.url.RawQuery = q.Encode()
	// end simulation

	if ticker.url.Query().Get("api_key") != apiKey {
		t.Errorf("Expected api_key to be %s, got %s", apiKey, ticker.url.Query().Get("api_key"))
	}

	if ticker.url.Query().Get("access_token") != accessToken {
		t.Errorf("Expected access_token to be %s, got %s", accessToken, ticker.url.Query().Get("access_token"))
	}

	if ticker.url.Query().Get("enctoken") != "" {
		t.Errorf("Expected enctoken to be empty, got %s", ticker.url.Query().Get("enctoken"))
	}
}
