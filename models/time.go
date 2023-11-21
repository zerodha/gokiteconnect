package models

const istTimeZone = "Asia/Kolkata"

import (
	"errors"
	"strings"
	"time"
)

// Time is custom time format used in all responses
type Time struct {
	time.Time
}

// List of known time formats
var (
	ctLayouts      = []string{"2006-01-02", "2006-01-02 15:04:05"}
	ctZonedLayouts = []string{"2006-01-02T15:04:05-0700", time.RFC3339}
)

// UnmarshalJSON parses JSON time string with custom time formats
func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), "\""))

	pTime, err := parseTime(s)
	if err != nil {
		return err
	}

	t.Time = pTime
	return nil
}

// UnmarshalCSV converts CSV string field internal date
func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), "\""))
	return t.parseTime(s)
}

func (t *Time) UnmarshalCSV(s string) error {
	s = strings.TrimSpace(s)
	return t.parseTime(s)
}

func (t *Time) parseTime(s string) error {
	if s == "" || s == "null" {
		return nil
	}

	loc, err := time.LoadLocation(istTimeZone)
	if err != nil {
		return err
	}

	var pTime time.Time
	var parseErr error

	layouts := append(ctLayouts, ctZonedLayouts...)

	for _, layout := range layouts {
		pTime, parseErr = time.Parse(layout, s)
		if parseErr == nil && !pTime.IsZero() {
			t.Time = pTime.In(loc)
			return nil
		}
	}

	return errors.New("unknown time format")
}

