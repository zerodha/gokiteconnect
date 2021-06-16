package models

import (
	"encoding/json"
	"testing"

	"github.com/gocarina/gocsv"
)

func TestCustomUnmarshalJSON(t *testing.T) {
	t.Parallel()
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
		if res.Date.IsZero() != j.isZero {
			t.Errorf("Custom time JSON parsing failed. Expected: %v, Got: %v, Test string: %s", j.isZero, res.Date.IsZero(), j.input)
		}
	}
}

func TestCustomUnmarshalCSV(t *testing.T) {
	t.Parallel()
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
		if res[0].Date.IsZero() != j.isZero {
			t.Errorf("Custom time CSV parsing failed. Expected: %v, Got: %v, Test string: %s", j.isZero, res[0].Date.IsZero(), j.input)
		}
	}
}
