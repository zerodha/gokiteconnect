package kiteconnect

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func (ts *TestSuite) TestGetGTTs(t *testing.T) {
	t.Parallel()
	gttOrders, err := ts.KiteConnect.GetGTTs()
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	for _, gttOrder := range gttOrders {
		if gttOrder.ID == 0 {
			t.Errorf("Error while parsing order id in GTT orders. %v", err)
		}
	}
}

func (ts *TestSuite) TestGetGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.GetGTT(123)
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.ID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestModifyGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.ModifyGTT(123, GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: TransactionTypeBuy,
		Trigger: &GTTSingleLegTrigger{
			TriggerParams: TriggerParams{
				TriggerValue: 2,
				Quantity:     2,
				LimitPrice:   2,
			},
		},
	})
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestPlaceGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.PlaceGTT(GTTParams{
		Tradingsymbol:   "INFY",
		Exchange:        "NSE",
		LastPrice:       800,
		TransactionType: TransactionTypeBuy,
		Trigger: &GTTSingleLegTrigger{
			TriggerParams: TriggerParams{
				TriggerValue: 1,
				Quantity:     1,
				LimitPrice:   1,
			},
		},
	})
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

func (ts *TestSuite) TestDeleteGTT(t *testing.T) {
	t.Parallel()
	gttOrder, err := ts.KiteConnect.DeleteGTT(123)
	if err != nil {
		t.Errorf("Error while fetching GTT orders. %v", err)
	}
	if gttOrder.TriggerID != 123 {
		t.Errorf("Error while parsing order id in GTT order. %v", err)
	}
}

// customGTTTrigger demonstrates that third-party Trigger implementations do not
// need to implement TrailingPoints to keep working.
type customGTTTrigger struct{ GTTSingleLegTrigger }

type gttRoundTripper func(*http.Request) (*http.Response, error)

func (f gttRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestGTTTrailingRequests(t *testing.T) {
	single := GTTSingleLegTrigger{TriggerParams: TriggerParams{TriggerValue: 950, LimitPrice: 949, Quantity: 1}}
	oco := GTTOneCancelsOtherTrigger{
		Lower: TriggerParams{TriggerValue: 950, LimitPrice: 949, Quantity: 1},
		Upper: TriggerParams{TriggerValue: 1100, LimitPrice: 1099, Quantity: 2},
	}
	cases := []struct {
		name                               string
		trigger                            Trigger
		kind                               GTTType
		values, prices, quantities, points []float64
	}{
		{"legacy custom", &customGTTTrigger{single}, GTTTypeSingle, []float64{950}, []float64{949}, []float64{1}, nil},
		{"legacy OCO", &oco, GTTTypeOCO, []float64{950, 1100}, []float64{949, 1099}, []float64{1, 2}, nil},
		{"trailing single", &GTTTrailingSingleLegTrigger{GTTSingleLegTrigger: single, TrailingPoint: 5}, GTTTypeTrailingSingle, []float64{950}, []float64{949}, []float64{1}, []float64{5}},
		{"trailing OCO", &GTTTrailingOneCancelsOtherTrigger{GTTOneCancelsOtherTrigger: oco, TrailingPoint: 5}, GTTTypeTrailingOCO, []float64{950, 1100}, []float64{949, 1099}, []float64{1, 2}, []float64{5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, tx := range []string{TransactionTypeBuy, TransactionTypeSell} {
				for _, method := range []string{http.MethodPost, http.MethodPut} {
					t.Run(tx+"/"+method, func(t *testing.T) {
						client := New("test-key")
						client.SetHTTPClient(&http.Client{Transport: gttRoundTripper(func(r *http.Request) (*http.Response, error) {
							if r.Method != method {
								t.Errorf("method = %s, want %s", r.Method, method)
							}
							wantPath := "/gtt/triggers"
							if method == http.MethodPut {
								wantPath += "/123"
							}
							if r.URL.Path != wantPath {
								t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
							}
							if err := r.ParseForm(); err != nil {
								t.Fatal(err)
							}
							if r.Form.Get("type") != string(tc.kind) {
								t.Errorf("type = %s, want %s", r.Form.Get("type"), tc.kind)
							}
							var condition GTTCondition
							if err := json.Unmarshal([]byte(r.Form.Get("condition")), &condition); err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(condition.TriggerValues, tc.values) || !reflect.DeepEqual(condition.TrailingPoints, tc.points) {
								t.Errorf("unexpected condition: %+v", condition)
							}
							if condition.Exchange != "NSE" || condition.Tradingsymbol != "INFY" || condition.LastPrice != 1000 {
								t.Errorf("missing condition fields: %+v", condition)
							}
							var fields map[string]json.RawMessage
							if err := json.Unmarshal([]byte(r.Form.Get("condition")), &fields); err != nil {
								t.Fatal(err)
							}
							if _, ok := fields["initial_trigger_price"]; ok {
								t.Error("initial_trigger_price must not be sent")
							}
							if _, ok := fields["trailing_points"]; ok != (tc.points != nil) {
								t.Error("unexpected trailing_points presence")
							}
							var orders []struct {
								Price           float64 `json:"price"`
								Quantity        float64 `json:"quantity"`
								TransactionType string  `json:"transaction_type"`
								OrderType       string  `json:"order_type"`
								Product         string  `json:"product"`
							}
							if err := json.Unmarshal([]byte(r.Form.Get("orders")), &orders); err != nil {
								t.Fatal(err)
							}
							if len(orders) != len(tc.prices) {
								t.Fatalf("orders = %d, want %d", len(orders), len(tc.prices))
							}
							for i, order := range orders {
								if order.Price != tc.prices[i] || order.Quantity != tc.quantities[i] || order.TransactionType != tx || order.OrderType != OrderTypeLimit || order.Product != ProductCNC {
									t.Errorf("order %d: %+v", i, order)
								}
							}
							return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: ioutil.NopCloser(strings.NewReader(`{"status":"success","data":{"trigger_id":123}}`))}, nil
						})})
						params := GTTParams{Exchange: "NSE", Tradingsymbol: "INFY", LastPrice: 1000, TransactionType: tx, Trigger: tc.trigger}
						var response GTTResponse
						var err error
						if method == http.MethodPost {
							response, err = client.PlaceGTT(params)
						} else {
							response, err = client.ModifyGTT(123, params)
						}
						if err != nil {
							t.Fatal(err)
						}
						if response.TriggerID != 123 {
							t.Errorf("trigger_id = %d", response.TriggerID)
						}
					})
				}
			}
		})
	}
}

func TestGTTTrailingResponses(t *testing.T) {
	for _, kind := range []GTTType{GTTTypeTrailingSingle, GTTTypeTrailingOCO} {
		t.Run(string(kind), func(t *testing.T) {
			for _, list := range []bool{false, true} {
				client := New("test-key")
				client.SetHTTPClient(&http.Client{Transport: gttRoundTripper(func(r *http.Request) (*http.Response, error) {
					values := "[960]"
					if kind == GTTTypeTrailingOCO {
						values = "[960,1100]"
					}
					data := `{"id":123,"type":"` + string(kind) + `","condition":{"trigger_values":` + values + `,"trailing_points":[5],"initial_trigger_price":950}}`
					if list {
						data = "[" + data + "]"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: ioutil.NopCloser(strings.NewReader(`{"status":"success","data":` + data + `}`))}, nil
				})})
				var gtt GTT
				if list {
					items, err := client.GetGTTs()
					if err != nil {
						t.Fatal(err)
					}
					if len(items) != 1 {
						t.Fatalf("items = %d", len(items))
					}
					gtt = items[0]
				} else {
					var err error
					gtt, err = client.GetGTT(123)
					if err != nil {
						t.Fatal(err)
					}
				}
				if gtt.Type != kind || gtt.Condition.InitialTriggerPrice != 950 || !reflect.DeepEqual(gtt.Condition.TrailingPoints, []float64{5}) {
					t.Errorf("unexpected trailing response: %+v", gtt)
				}
			}
		})
	}
}
