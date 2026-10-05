package models

import (
	"errors"
	"testing"
)

func TestParseInstrumentID(t *testing.T) {
	id, err := ParseInstrumentID(" nse:nifty 50 ")
	if err != nil {
		t.Fatalf("ParseInstrumentID() error = %v", err)
	}
	if id != "NSE:NIFTY 50" {
		t.Fatalf("ParseInstrumentID() = %q, want NSE:NIFTY 50", id)
	}
	id, err = ParseInstrumentID(" bse:bse lgmd60:40st div50 ")
	if err != nil {
		t.Fatalf("ParseInstrumentID() with symbol colon error = %v", err)
	}
	if id != "BSE:BSE LGMD60:40ST DIV50" {
		t.Fatalf("ParseInstrumentID() with symbol colon = %q", id)
	}

	for _, value := range []string{"", "NSE", ":INFY", "NSE:"} {
		if _, err := ParseInstrumentID(value); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ParseInstrumentID(%q) error = %v, want ErrInvalidInput", value, err)
		}
	}
}
