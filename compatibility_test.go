package kiteconnect_test

import (
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"testing"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

type legacyTransport func(*http.Request) (*http.Response, error)

func (f legacyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// These expectations describe the existing HTTPClient API, including methods
// that ignore payloads and GET/DELETE replacing the URL's query string.
func TestLegacyHTTPClientRequestCompatibility(t *testing.T) {
	for _, helper := range []string{"Do", "DoRaw", "DoEnvelope", "DoJSON"} {
		for _, method := range []string{"", http.MethodGet, http.MethodDelete, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodOptions, "post"} {
			for _, payload := range []string{"", "ids=1&ids=2"} {
				t.Run(helper+"/"+method+"/"+payload, func(t *testing.T) {
					headers := http.Header{"X-Custom": {"kept"}}
					wantQuery, wantBody, wantType := "mode=batch&ids=old", "", ""
					switch method {
					case http.MethodGet, http.MethodDelete:
						wantQuery = payload
					case http.MethodPost, http.MethodPut:
						wantBody, wantType = payload, "application/x-www-form-urlencoded"
					}
					client := kiteconnect.NewHTTPClient(&http.Client{Transport: legacyTransport(func(r *http.Request) (*http.Response, error) {
						wantMethod := method
						if wantMethod == "" {
							wantMethod = http.MethodGet
						}
						body := []byte(nil)
						if r.Body != nil {
							var err error
							body, err = ioutil.ReadAll(r.Body)
							if err != nil {
								t.Fatal(err)
							}
						}
						if r.Method != wantMethod || r.URL.RawQuery != wantQuery || string(body) != wantBody || r.Header.Get("Content-Type") != wantType {
							t.Errorf("request = %s %s, body %q, Content-Type %q; want %s, query %q, body %q, Content-Type %q",
								r.Method, r.URL, body, r.Header.Get("Content-Type"), wantMethod, wantQuery, wantBody, wantType)
						}
						return &http.Response{StatusCode: http.StatusOK, Body: ioutil.NopCloser(strings.NewReader(`{"data":null}`))}, nil
					})}, nil, false)
					endpoint := "https://api.example.test/items?mode=batch&ids=old"
					var params url.Values
					if payload != "" {
						params = url.Values{"ids": {"1", "2"}}
					}
					var err error
					switch helper {
					case "Do":
						_, err = client.Do(method, endpoint, params, headers)
					case "DoRaw":
						_, err = client.DoRaw(method, endpoint, []byte(payload), headers)
					case "DoEnvelope":
						err = client.DoEnvelope(method, endpoint, params, headers, nil)
					case "DoJSON":
						_, err = client.DoJSON(method, endpoint, params, headers, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
					if headers.Get("Content-Type") != wantType {
						t.Errorf("caller Content-Type = %q, want legacy value %q", headers.Get("Content-Type"), wantType)
					}
				})
			}
		}
	}
}

func TestBuiltinEndpointPreservesBaseURI(t *testing.T) {
	client := kiteconnect.New("test-key")
	client.SetAccessToken("test-token")
	client.SetBaseURI("https://api.example.test/gateway/")
	client.SetHTTPClient(&http.Client{Transport: legacyTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/gateway//user/profile" || r.Header.Get("Authorization") != "token test-key:test-token" {
			t.Errorf("built-in request = %s, %v", r.URL, r.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: ioutil.NopCloser(strings.NewReader(`{"data":{"user_id":"test-user"}}`))}, nil
	})})
	out, err := client.GetUserProfile()
	if err != nil || out.UserID != "test-user" {
		t.Fatalf("GetUserProfile = %+v, %v", out, err)
	}
}
