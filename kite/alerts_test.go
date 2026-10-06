package kite

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testUUID = "550e8400-e29b-41d4-a716-446655440000"

func (ts *TestSuite) TestCreateAlert(t *testing.T) {
	t.Parallel()
	alert, err := ts.KiteConnect.CreateAlert(AlertParams{
		Name:             "NIFTY 50",
		Type:             AlertTypeSimple,
		LHSExchange:      "INDICES",
		LHSTradingSymbol: "NIFTY 50",
		LHSAttribute:     "LastTradedPrice",
		Operator:         AlertOperatorGE,
		RHSType:          "constant",
		RHSConstant:      27000,
	})
	if err != nil {
		t.Errorf("Error while creating alert: %v", err)
	}
	if alert.Name != "NIFTY 50" || alert.LHSExchange != "INDICES" {
		t.Errorf("Alert fields not parsed correctly: %+v", alert)
	}
}

func (ts *TestSuite) TestGetAlerts(t *testing.T) {
	t.Parallel()
	alerts, err := ts.KiteConnect.GetAlerts(nil)
	if err != nil {
		t.Errorf("Error while fetching alerts: %v", err)
	}
	if len(alerts) == 0 {
		t.Errorf("No alerts returned")
	}
	if alerts[0].UUID == "" || alerts[0].Name == "" {
		t.Errorf("Alert fields not parsed correctly: %+v", alerts[0])
	}
}

func (ts *TestSuite) TestGetAlert(t *testing.T) {
	t.Parallel()
	alert, err := ts.KiteConnect.GetAlert(testUUID)
	if err != nil {
		t.Errorf("Error while fetching alert: %v", err)
	}
	if alert.UUID != testUUID {
		t.Errorf("Alert UUID mismatch: got %s, want %s", alert.UUID, testUUID)
	}
	if alert.Name == "" {
		t.Errorf("Alert fields not parsed correctly: %+v", alert)
	}
}

func (ts *TestSuite) TestModifyAlert(t *testing.T) {
	t.Parallel()
	alert, err := ts.KiteConnect.ModifyAlert(testUUID, AlertParams{
		Name:             "NIFTY 50",
		Type:             AlertTypeSimple,
		LHSExchange:      "INDICES",
		LHSTradingSymbol: "NIFTY 50",
		LHSAttribute:     "LastTradedPrice",
		Operator:         AlertOperatorGE,
		RHSType:          "constant",
		RHSConstant:      27500,
	})
	if err != nil {
		t.Errorf("Error while modifying alert: %v", err)
	}
	if alert.UUID != testUUID {
		t.Errorf("Alert UUID mismatch: got %s, want %s", alert.UUID, testUUID)
	}
	if alert.RHSConstant != 27500 {
		t.Errorf("Alert RHSConstant not updated: got %v, want 27500", alert.RHSConstant)
	}
}

func (ts *TestSuite) TestDeleteAlerts(t *testing.T) {
	t.Parallel()

	err := ts.KiteConnect.DeleteAlerts(testUUID)
	if err != nil {
		t.Errorf("Error while deleting alert: %v", err)
	}
}

func TestDeleteAlertsPreservesUUIDQuery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("expected DELETE request, got %s", r.Method)
		}
		if got := r.URL.Query()["uuid"]; len(got) != 2 || got[0] != "uuid-1" || got[1] != "uuid-2" {
			t.Fatalf("expected uuid query values to be preserved, got %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":null}`)
	}))
	defer server.Close()

	kc := New("test_api_key")
	kc.SetBaseURI(server.URL)
	kc.SetAccessToken("test_access_token")

	err := kc.DeleteAlerts("uuid-1", "uuid-2")
	if err != nil {
		t.Fatalf("expected delete alerts to succeed, got %v", err)
	}
}

func (ts *TestSuite) TestGetAlertHistory(t *testing.T) {
	t.Parallel()
	history, err := ts.KiteConnect.GetAlertHistory(testUUID)
	if err != nil {
		t.Errorf("Error while fetching alert history: %v", err)
	}
	if len(history) == 0 {
		t.Errorf("No alert history returned")
	}
	if history[0].UUID != testUUID {
		t.Errorf("Alert history UUID mismatch: got %s, want %s", history[0].UUID, testUUID)
	}
}
