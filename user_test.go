package kiteconnect

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

func (ts *TestSuite) TestGenerateSessionSetsAccessToken(t *testing.T) {
	t.Parallel()

	session, err := ts.KiteConnect.GenerateSession("test_request_token", "test_api_secret")
	if err != nil {
		t.Fatalf("Error while generating user session. Error: %v", err)
	}

	if session.AccessToken == "" {
		t.Fatal("Expected access token in generated session")
	}

	if ts.KiteConnect.accessToken != session.AccessToken {
		t.Errorf("Expected client access token to be set to %q, got %q", session.AccessToken, ts.KiteConnect.accessToken)
	}

	if session.UserSessionTokens.AccessToken != session.AccessToken {
		t.Errorf("Expected deprecated UserSessionTokens.AccessToken to mirror session.AccessToken")
	}
}

func (ts *TestSuite) TestRenewAccessTokenSetsAccessToken(t *testing.T) {
	t.Parallel()

	session, err := ts.KiteConnect.RenewAccessToken("test_refresh_token", "test_api_secret")
	if err != nil {
		t.Fatalf("Error while renewing access token. Error: %v", err)
	}

	if session.AccessToken == "" {
		t.Fatal("Expected access token in renewed session")
	}

	if ts.KiteConnect.accessToken != session.AccessToken {
		t.Errorf("Expected client access token to be set to %q, got %q", session.AccessToken, ts.KiteConnect.accessToken)
	}
}

func (ts *TestSuite) TestInvalidateAccessToken(t *testing.T) {
	t.Parallel()
	sessionLogout, err := ts.KiteConnect.InvalidateAccessToken()
	if err != nil || !sessionLogout == true {
		t.Errorf("Error while invalidating user session. Error: %v", err)
	}
}
