package kiteconnect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func (ts *TestSuite) TestGetUserProfile(t *testing.T) {
	profile, err := ts.KiteConnect.GetUserProfile()
	assert.Nil(t, err, "Error while reading user profile")
	assert.NotEqual(t, "", profile.Email, "Error while reading user profile")
}

func (ts *TestSuite) TestGetUserMargins(t *testing.T) {
	margins, err := ts.KiteConnect.GetUserMargins()
	assert.Nil(t, err, "Error while reading user margins")
	assert.Condition(t, func() bool {
		return margins.Equity.Enabled || margins.Commodity.Enabled
	}, "Incorrect margin values.")
}

func (ts *TestSuite) TestGetUserSegmentMargins(t *testing.T) {
	margins, err := ts.KiteConnect.GetUserSegmentMargins("test")
	assert.Nil(t, err, "Error while reading user margins")
	assert.True(t, margins.Enabled, "Incorrect segment margin values.")
}
