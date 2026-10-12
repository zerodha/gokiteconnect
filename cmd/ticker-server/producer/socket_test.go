package producer

import (
	"errors"
	"testing"
)

func TestAuthenticationErrorClassification(t *testing.T) {
	for _, message := range []string{"403 Forbidden", "token expired", "authentication failed"} {
		if !isAuthenticationError(errors.New(message)) {
			t.Fatalf("%q was not classified as authentication failure", message)
		}
	}
	if isAuthenticationError(errors.New("temporary network failure")) {
		t.Fatal("network error was classified as authentication failure")
	}
}
