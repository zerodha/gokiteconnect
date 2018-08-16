package kiteconnect

import (
	"encoding/json"
	"testing"

	"github.com/gocarina/gocsv"
	"github.com/stretchr/testify/assert"
)

func TestCustomUnmarshalJSON(t *testing.T) {
	type sampleJSON struct {
		Date Time `json:"date"`
	}

	testCases := []struct {
		input  string
		isZero bool
	}{
		{"{\"date\":\"2006-01-02\"}", false},
		{"{\"date\":\"2006-01-02 15:04:05\"}", false},
		{"{\"date\":\"2006-01-02T15:04:05-0700\"}", false},
		{"{\"date\":\"2006-01-02T\"}", true},
	}

	for _, j := range testCases {
		res := sampleJSON{}
		json.Unmarshal([]byte(j.input), &res)
		assert.Equal(t, j.isZero, res.Date.IsZero(), "Custom time JSON parsing failed.")
	}
}

func TestCustomUnmarshalCSV(t *testing.T) {
	type sampleCSV struct {
		Date Time `csv:"date"`
	}

	testCases := []struct {
		input  string
		isZero bool
	}{
		{"date\n2006-01-02", false},
		{"date\n2006-01-02 15:04:05", false},
		{"date\n2006-01-02T15:04:05-0700", false},
		{"date\n2006-01-02:", true},
	}

	for _, j := range testCases {
		res := []sampleCSV{}
		gocsv.UnmarshalBytes([]byte(j.input), &res)
		assert.Equal(t, j.isZero, res[0].Date.IsZero(), "Custom time CSV parsing failed.")
	}
}
