package kiteconnect

import (
	"encoding/json"
	"testing"

	"github.com/gocarina/gocsv"
)

func TestCustomUnmarshalJSON(t *testing.T) {
	type sampleJSON struct {
		Date Time `json:"date"`
	}

	// Test all the valid custom time formatting
	validJSON := []string{
		"{\"date\":\"2006-01-02\"}",
		"{\"date\":\"2006-01-02 15:04:05\"}",
		"{\"date\":\"2006-01-02T15:04:05-0700\"}",
	}

	for _, j := range validJSON {
		res := sampleJSON{}
		json.Unmarshal([]byte(j), &res)
		if res.Date.IsZero() {
			t.Errorf("Custom time JSON parsing failed. Sample JSON: %s", j)
		}
	}

	// Test and invalid format
	invalidJSON := []string{
		"{\"date\":\"2006-01-02:\"}",
	}

	for _, j := range invalidJSON {
		res := sampleJSON{}
		json.Unmarshal([]byte(j), &res)

		if !res.Date.IsZero() {
			t.Errorf("Custom time JSON parsing didn't fail. Sample JSON: %s", j)
		}
	}
}

func TestCustomUnmarshalCSV(t *testing.T) {
	type sampleCSV struct {
		Date Time `csv:"date"`
	}

	// Valid csv
	validCSV := []string{
		"date\n2006-01-02",
		"date\n2006-01-02 15:04:05",
		"date\n2006-01-02T15:04:05-0700",
	}

	for _, j := range validCSV {
		res := []sampleCSV{}
		gocsv.UnmarshalBytes([]byte(j), &res)
		if res[0].Date.IsZero() {
			t.Errorf("Custom time CSV parsing failed. Sample CSV: %s", j)
		}
	}

	// Invalid csv
	invalidCSV := []string{
		"date\n2006-01-02:",
	}

	for _, j := range invalidCSV {
		res := []sampleCSV{}
		gocsv.UnmarshalBytes([]byte(j), &res)
		if !res[0].Date.IsZero() {
			t.Errorf("Custom time CSV parsing not failing. Sample CSV: %s", j)
		}
	}
}
