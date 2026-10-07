package kiteconnect_test

import (
	"context"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

type requestTransport func(*http.Request) (*http.Response, error)

func (f requestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func customClient(f requestTransport) *kiteconnect.Client {
	c := kiteconnect.New("test-key")
	c.SetAccessToken("test-token")
	c.SetAppName("extension-test")
	c.SetBaseURI("https://api.example.test/gateway/")
	c.SetHTTPClient(&http.Client{Transport: f})
	return c
}

func customResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"X-Request-Id": {"request-id"}},
		Body:       ioutil.NopCloser(strings.NewReader(body)),
	}
}

// This wrapper lives outside kiteconnect, as a custom endpoint package would.
type extendedClient struct {
	*kiteconnect.Client
}

type customRecord struct {
	Count int `json:"count"`
}

func (c extendedClient) GetCustomRecord() (customRecord, error) {
	var out customRecord
	err := c.DoEnvelope(http.MethodGet, "/custom/records", url.Values{"segment": {"equity"}}, nil, &out)
	return out, err
}

func TestCustomEndpointExtension(t *testing.T) {
	client := customClient(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.String(); got != "https://api.example.test/gateway/custom/records?segment=equity" {
			t.Errorf("URL = %q", got)
		}
		if r.Header.Get("Authorization") != "token test-key:test-token" || r.Header.Get("X-Kite-Version") != "3" {
			t.Errorf("missing client authentication or version: %v", r.Header)
		}
		if !strings.HasSuffix(r.UserAgent(), "/extension-test") {
			t.Errorf("User-Agent = %q", r.UserAgent())
		}
		return customResponse(http.StatusOK, `{"status":"success","data":{"count":42}}`), nil
	})
	out, err := (extendedClient{client}).GetCustomRecord()
	if err != nil || out.Count != 42 {
		t.Fatalf("GetCustomRecord = %+v, %v", out, err)
	}
}

func TestCustomRequestEncoding(t *testing.T) {
	for _, method := range []string{"", http.MethodGet, http.MethodDelete, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			params := url.Values{"i": {"NSE:A&B", "BSE:Hello World"}, "custom_filter": {"custom-value"}}
			before := params.Encode()
			client := customClient(func(r *http.Request) (*http.Response, error) {
				queryMethod := method == "" || method == http.MethodGet || method == http.MethodDelete || method == http.MethodHead
				values := r.URL.Query()
				if values.Get("existing") != "kept" {
					t.Errorf("existing query was lost: %v", values)
				}
				if queryMethod {
					if r.Body != nil && r.Body != http.NoBody {
						t.Error("query parameters sent as a body")
					}
					if want := []string{"original", "NSE:A&B", "BSE:Hello World"}; !reflect.DeepEqual(values["i"], want) {
						t.Errorf("repeated query values = %v, want %v", values["i"], want)
					}
				} else {
					if !reflect.DeepEqual(values["i"], []string{"original"}) {
						t.Errorf("body parameters added to query: %v", values)
					}
					body, err := ioutil.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					values, err = url.ParseQuery(string(body))
					if err != nil || !reflect.DeepEqual(values, params) {
						t.Errorf("form = %v, %v, want %v", values, err, params)
					}
					if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
					}
				}
				if values.Get("custom_filter") != "custom-value" {
					t.Error("custom parameter was lost")
				}
				return customResponse(http.StatusOK, "raw-response"), nil
			})
			resp, err := client.Do(method, "/custom?existing=kept&i=original", params, nil)
			if err != nil || string(resp.Body) != "raw-response" || resp.Response.Header.Get("X-Request-Id") != "request-id" {
				t.Fatalf("Do = %+v, %v", resp, err)
			}
			if params.Encode() != before {
				t.Error("caller parameters mutated")
			}
		})
	}
}

func TestCustomRequestPreservesURIQueryWithNilParams(t *testing.T) {
	client := customClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery != "mode=compact&i=A%2FB" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		return customResponse(http.StatusOK, `{"data":null}`), nil
	})
	if err := client.DoEnvelope(http.MethodGet, "/custom?mode=compact&i=A%2FB", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCustomRequestHeaderOwnership(t *testing.T) {
	headers := http.Header{
		"authorization":  {"caller-auth"},
		"x-kite-version": {"caller-version"},
		"user-agent":     {"caller-agent"},
		"X-Custom":       {"one", "two"},
	}
	before := headers.Clone()
	token := "test-token"
	client := customClient(func(r *http.Request) (*http.Response, error) {
		if want := []string{"token test-key:" + token}; !reflect.DeepEqual(r.Header.Values("Authorization"), want) {
			t.Errorf("Authorization = %v, want %v", r.Header.Values("Authorization"), want)
		}
		if r.Header.Get("X-Kite-Version") != "3" || !strings.HasSuffix(r.UserAgent(), "/extension-test") {
			t.Errorf("client headers overridden: %v", r.Header)
		}
		if !reflect.DeepEqual(r.Header["X-Custom"], before["X-Custom"]) {
			t.Errorf("custom headers lost: %v", r.Header)
		}
		// Even transport mutation must not touch the caller's header slices.
		r.Header["X-Custom"][0] = "changed"
		return customResponse(http.StatusOK, `{"data":null}`), nil
	})
	for _, next := range []string{"test-token", "rotated-token"} {
		token = next
		client.SetAccessToken(next)
		if err := client.DoEnvelope(http.MethodGet, "/custom", nil, headers, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(headers, before) {
			t.Fatalf("caller headers mutated: %v", headers)
		}
	}
	// SetHTTPClient also supports alternate auth managed by a transport.
	client.SetAccessToken("")
	client.SetHTTPClient(&http.Client{Transport: requestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "caller-auth" {
			t.Errorf("explicit auth without client token = %q", r.Header.Get("Authorization"))
		}
		return customResponse(http.StatusOK, ""), nil
	})})
	if _, err := client.Do(http.MethodGet, "/custom", nil, headers); err != nil {
		t.Fatal(err)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestCustomRawRequestAndEnvelopeError(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(`{"status":"error","error_type":"TokenException","message":"expired","data":{"reason":"expiry"}}`)}
			client := customClient(func(r *http.Request) (*http.Response, error) {
				if r.Body == nil || r.Body == http.NoBody {
					t.Fatal("raw payload was not sent as a body")
				}
				payload, err := ioutil.ReadAll(r.Body)
				if err != nil || string(payload) != `{"name":"custom"}` || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("JSON payload = %q, %v, headers = %v", payload, err, r.Header)
				}
				if r.URL.RawQuery != "mode=compact" {
					t.Errorf("raw payload changed URI query: %q", r.URL.RawQuery)
				}
				resp := customResponse(http.StatusForbidden, "")
				resp.Body = body
				return resp, nil
			})
			resp, err := client.DoRaw(method, "/custom?mode=compact", []byte(`{"name":"custom"}`), http.Header{"Content-Type": {"application/json"}})
			if err != nil || !body.closed {
				t.Fatalf("DoRaw = %v, body closed = %v", err, body.closed)
			}
			err = kiteconnect.ReadEnvelope(resp, nil)
			var apiErr kiteconnect.Error
			if !errors.As(err, &apiErr) || apiErr.Code != http.StatusForbidden || apiErr.ErrorType != kiteconnect.TokenError || apiErr.Message != "expired" {
				t.Fatalf("ReadEnvelope error = %#v", err)
			}
			if !reflect.DeepEqual(apiErr.Data, map[string]interface{}{"reason": "expiry"}) {
				t.Errorf("error data = %v", apiErr.Data)
			}
		})
	}
}

func TestCustomRawDELETEWithContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "delete-context")
	client := customClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.Context().Value(struct{}{}) != "delete-context" {
			t.Errorf("DELETE method or context was lost: %s", r.Method)
		}
		if r.Body == nil || r.Body == http.NoBody {
			t.Fatal("DELETE body is missing")
		}
		payload, err := ioutil.ReadAll(r.Body)
		if err != nil || string(payload) != `{"ids":[1,2]}` {
			t.Errorf("DELETE payload = %q, %v", payload, err)
		}
		if r.URL.RawQuery != "mode=batch" || r.Header.Get("Authorization") != "token test-key:test-token" {
			t.Errorf("DELETE query or authentication changed: %s, %v", r.URL, r.Header)
		}
		return customResponse(http.StatusOK, `{"data":null}`), nil
	})
	_, err := client.DoRawWithContext(ctx, http.MethodDelete, "/custom/items?mode=batch",
		[]byte(`{"ids":[1,2]}`), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCustomEnvelopeDecoding(t *testing.T) {
	t.Run("custom destination", func(t *testing.T) {
		var out []customRecord
		err := kiteconnect.ReadEnvelope(kiteconnect.HTTPResponse{Response: &http.Response{StatusCode: 200}, Body: []byte(`{"data":[{"count":42}]}`)}, &out)
		if err != nil || !reflect.DeepEqual(out, []customRecord{{Count: 42}}) {
			t.Fatalf("ReadEnvelope = %v, %v", out, err)
		}
	})
	for _, test := range []struct {
		name string
		resp kiteconnect.HTTPResponse
	}{
		{"missing response", kiteconnect.HTTPResponse{}},
		{"malformed success", kiteconnect.HTTPResponse{Response: &http.Response{StatusCode: 200}, Body: []byte("invalid")}},
		{"malformed error", kiteconnect.HTTPResponse{Response: &http.Response{StatusCode: 500}, Body: []byte("upstream unavailable")}},
		{"wrong data type", kiteconnect.HTTPResponse{Response: &http.Response{StatusCode: 200}, Body: []byte(`{"data":"wrong"}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out customRecord
			err := kiteconnect.ReadEnvelope(test.resp, &out)
			var apiErr kiteconnect.Error
			if !errors.As(err, &apiErr) || apiErr.ErrorType != kiteconnect.DataError {
				t.Fatalf("ReadEnvelope error = %#v", err)
			}
		})
	}
	t.Run("envelope request returns API error", func(t *testing.T) {
		client := customClient(func(*http.Request) (*http.Response, error) {
			return customResponse(http.StatusBadRequest, `{"error_type":"InputException","message":"invalid input","data":null}`), nil
		})
		var apiErr kiteconnect.Error
		err := client.DoEnvelope(http.MethodPost, "/custom", nil, nil, nil)
		if !errors.As(err, &apiErr) || apiErr.ErrorType != kiteconnect.InputError || apiErr.Code != http.StatusBadRequest {
			t.Fatalf("DoEnvelope error = %#v", err)
		}
	})
}

func TestCustomRequestsRejectAbsoluteURLs(t *testing.T) {
	client := customClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid endpoint reached transport")
		return nil, nil
	})
	for _, uri := range []string{"https://other.example.test/private", "//other.example.test/private", "custom", ""} {
		_, err := client.Do(http.MethodGet, uri, nil, nil)
		var apiErr kiteconnect.Error
		if !errors.As(err, &apiErr) || apiErr.ErrorType != kiteconnect.InputError {
			t.Errorf("Do(%q) error = %#v", uri, err)
		}
	}
}

func TestCustomRequestContext(t *testing.T) {
	for _, kind := range []string{"query", "raw", "envelope"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), struct{}{}, "request-value"))
			defer cancel()
			started := make(chan struct{})
			client := customClient(func(r *http.Request) (*http.Response, error) {
				if r.Context().Value(struct{}{}) != "request-value" {
					t.Error("request context was lost")
				}
				close(started)
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			result := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "query":
					_, err = client.DoWithContext(ctx, http.MethodGet, "/custom", nil, nil)
				case "raw":
					_, err = client.DoRawWithContext(ctx, http.MethodPatch, "/custom", nil, nil)
				case "envelope":
					err = client.DoEnvelopeWithContext(ctx, http.MethodGet, "/custom", nil, nil, nil)
				}
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancelled request error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not cancel")
			}
		})
	}
}

type contextBody struct {
	ctx    context.Context
	read   chan struct{}
	closed bool
}

func (b *contextBody) Read([]byte) (int, error) {
	close(b.read)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *contextBody) Close() error {
	b.closed = true
	return nil
}

func TestCustomRequestCancellationDuringResponseRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &contextBody{ctx: ctx, read: make(chan struct{})}
	client := customClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	result := make(chan error, 1)
	go func() {
		_, err := client.DoWithContext(ctx, http.MethodGet, "/custom", nil, nil)
		result <- err
	}()
	select {
	case <-body.read:
	case <-time.After(time.Second):
		t.Fatal("response read did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !body.closed {
			t.Fatalf("read error = %v, body closed = %v", err, body.closed)
		}
	case <-time.After(time.Second):
		t.Fatal("response read did not cancel")
	}
}

func TestCustomRequestDeadlineAndTransportError(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	client := customClient(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})
	if _, err := client.DoWithContext(ctx, http.MethodGet, "/custom", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	client = customClient(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure")
	})
	_, err := client.Do(http.MethodGet, "/custom", nil, nil)
	var apiErr kiteconnect.Error
	if !errors.As(err, &apiErr) || apiErr.ErrorType != kiteconnect.NetworkError {
		t.Fatalf("transport error = %#v", err)
	}
}

func TestBuiltinEndpointUsesSharedRequestHandling(t *testing.T) {
	client := customClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/gateway/user/profile" || r.Header.Get("Authorization") != "token test-key:test-token" {
			t.Errorf("built-in request = %s, %v", r.URL, r.Header)
		}
		return customResponse(http.StatusOK, `{"data":{"user_id":"test-user"}}`), nil
	})
	client.SetBaseURI("https://api.example.test/gateway")
	out, err := (extendedClient{client}).GetUserProfile()
	if err != nil || out.UserID != "test-user" {
		t.Fatalf("GetUserProfile = %+v, %v", out, err)
	}
}
