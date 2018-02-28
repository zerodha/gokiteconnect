package kiteconnect

import (
	"strings"
	"time"
)

// Time is custom time format used in all responses
type Time struct {
	time.Time
}

// Array of knows time formats
var ctLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05-0700"}

// UnmarshalJSON parses JSON time string with custom time formats
func (t *Time) UnmarshalJSON(b []byte) (err error) {
	var pTime time.Time
	s := strings.TrimSpace(strings.Trim(string(b), "\""))

	if len(s) == 0 || s == "null" {
		t.Time = pTime
		return nil
	}

	// Iterate through known layouts and parse time
	for _, l := range ctLayouts {
		pTime, err = time.Parse(l, s)
		if err == nil && !pTime.IsZero() {
			break
		}
	}

	t.Time = pTime
	return nil
}

// UnmarshalCSV converts CSV string field internal date
func (t *Time) UnmarshalCSV(s string) (err error) {
	var pTime time.Time
	s = strings.TrimSpace(s)
	for _, l := range ctLayouts {
		pTime, err = time.Parse(l, s)
		if err == nil && !pTime.IsZero() {
			break
		}
	}

	t.Time = pTime
	return nil
}
