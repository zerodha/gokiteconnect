package models

import (
	"encoding/json"
	"testing"
	"time"

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
		{"{\"date\":\"null\"}", true},
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

func TestTimeJSONRoundTrip(t *testing.T) {
	t.Parallel()
	type sample struct {
		Set   Time `json:"set"`
		Unset Time `json:"unset"`
	}
	in := sample{Set: Time{Time: time.Date(2026, 10, 22, 9, 15, 3, 0, ExchangeLocation)}}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var out sample
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", data, err)
	}
	if !out.Set.Equal(in.Set.Time) || !out.Unset.IsZero() {
		t.Fatalf("round trip of %s = %#v", data, out)
	}
}
