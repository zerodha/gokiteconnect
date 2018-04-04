package kiteconnect

import (
	"testing"
)

func (ts *TestSuite) TestGetUserProfile(t *testing.T) {
	profile, err := ts.KiteConnect.GetUserProfile()
	if err != nil || profile.Email == "" {
		t.Errorf("Error while reading user profile. Error: %v", err)
	}
}

func (ts *TestSuite) TestGetUserMargins(t *testing.T) {
	margins, err := ts.KiteConnect.GetUserMargins()
	if err != nil {
		t.Errorf("Error while reading user margins. Error: %v", err)
	}

	if !margins.Equity.Enabled || !margins.Commodity.Enabled {
		t.Errorf("Incorrect margin values.")
	}
}

func (ts *TestSuite) TestGetUserSegmentMargins(t *testing.T) {
	margins, err := ts.KiteConnect.GetUserSegmentMargins("test")
	if err != nil {
		t.Errorf("Error while reading user margins. Error: %v", err)
	}

	if !margins.Enabled {
		t.Errorf("Incorrect segment margin values.")
	}
}
