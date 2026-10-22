package kite

import (
	"testing"
)

func (ts *TestSuite) TestGetUserProfile(t *testing.T) {
	t.Parallel()
	profile, err := ts.KiteConnect.GetUserProfile()
	if err != nil || profile.Email == "" || profile.UserID == "" {
		t.Errorf("Error while reading user profile. Error: %v", err)
	}
}

func (ts *TestSuite) TestGetFullUserProfile(t *testing.T) {
	t.Parallel()
	fullProfile, err := ts.KiteConnect.GetFullUserProfile()
	if err != nil || fullProfile.Email == "" || fullProfile.UserID == "" {
		t.Errorf("Error while reading full user profile. Error: %v", err)
	}
}

func (ts *TestSuite) TestGetUserMargins(t *testing.T) {
	t.Parallel()
	margins, err := ts.KiteConnect.GetUserMargins()
	if err != nil {
		t.Errorf("Error while reading user margins. Error: %v", err)
	}

	if !margins.Equity.Enabled || !margins.Commodity.Enabled {
		t.Errorf("Incorrect margin values.")
	}
}

func (ts *TestSuite) TestGetUserSegmentMargins(t *testing.T) {
	t.Parallel()
	margins, err := ts.KiteConnect.GetUserSegmentMargins("test")
	if err != nil {
		t.Errorf("Error while reading user margins. Error: %v", err)
	}

	if !margins.Enabled {
		t.Errorf("Incorrect segment margin values.")
	}
}

// newIsolatedClient returns a client that shares the suite's mocked HTTP
// transport but not its state, for parallel tests that mutate the client.
func (ts *TestSuite) newIsolatedClient() *Client {
	kc := New("test_api_key")
	kc.SetHTTPClient(ts.KiteConnect.httpClient.GetClient().client)
	kc.SetBaseURI(ts.KiteConnect.baseURI)
	kc.quotePacer = nil
	return kc
}

func (ts *TestSuite) TestGenerateSessionSetsAccessToken(t *testing.T) {
	t.Parallel()

	kc := ts.newIsolatedClient()
	session, err := kc.GenerateSession("test_request_token", "test_api_secret")
	if err != nil {
		t.Fatalf("Error while generating user session. Error: %v", err)
	}

	if session.AccessToken == "" {
		t.Fatal("Expected access token in generated session")
	}

	if kc.accessToken != session.AccessToken {
		t.Errorf("Expected client access token to be set to %q, got %q", session.AccessToken, kc.accessToken)
	}

	if session.UserSessionTokens.AccessToken != session.AccessToken {
		t.Errorf("Expected deprecated UserSessionTokens.AccessToken to mirror session.AccessToken")
	}
}

func (ts *TestSuite) TestRenewAccessTokenSetsAccessToken(t *testing.T) {
	t.Parallel()

	kc := ts.newIsolatedClient()
	session, err := kc.RenewAccessToken("test_refresh_token", "test_api_secret")
	if err != nil {
		t.Fatalf("Error while renewing access token. Error: %v", err)
	}

	if session.AccessToken == "" {
		t.Fatal("Expected access token in renewed session")
	}

	if kc.accessToken != session.AccessToken {
		t.Errorf("Expected client access token to be set to %q, got %q", session.AccessToken, kc.accessToken)
	}
}

func (ts *TestSuite) TestInvalidateAccessToken(t *testing.T) {
	t.Parallel()
	sessionLogout, err := ts.KiteConnect.InvalidateAccessToken()
	if err != nil || !sessionLogout == true {
		t.Errorf("Error while invalidating user session. Error: %v", err)
	}
}
