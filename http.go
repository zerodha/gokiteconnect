package kiteconnect

/*
	HTTP helper functions with methods for parsing Kite style JSON envelopes.
*/

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// HTTPClient represents an HTTP client.
type HTTPClient interface {
	Do(method, rURL string, params url.Values, headers http.Header) (HTTPResponse, error)
	DoRaw(method, rURL string, reqBody []byte, headers http.Header) (HTTPResponse, error)
	DoEnvelope(method, url string, params url.Values, headers http.Header, obj interface{}) error
	DoJSON(method, url string, params url.Values, headers http.Header, obj interface{}) (HTTPResponse, error)
	GetClient() *httpClient
}

// httpClient is the default implementation of HTTPClient.
type httpClient struct {
	client *http.Client
	hLog   *log.Logger
	debug  bool
}

// HTTPResponse contains the buffered body and metadata of an HTTP response.
// Response.Body has already been read and closed; use Body to access its contents.
type HTTPResponse struct {
	Body     []byte
	Response *http.Response
}

type errorEnvelope struct {
	Status    string      `json:"status"`
	ErrorType string      `json:"error_type"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data"`
}

type envelope struct {
	Data interface{} `json:"data"`
}

// NewHTTPClient returns a self-contained HTTP request object
// with underlying keep-alive transport.
func NewHTTPClient(h *http.Client, hLog *log.Logger, debug bool) HTTPClient {
	if hLog == nil {
		hLog = log.New(os.Stdout, "base.HTTP: ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	if h == nil {
		h = &http.Client{
			Timeout: time.Duration(5) * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   10,
				ResponseHeaderTimeout: time.Second * time.Duration(5),
			},
		}
	}

	return &httpClient{
		hLog:   hLog,
		client: h,
		debug:  debug,
	}
}

func (h *httpClient) Do(method, rURL string, params url.Values, headers http.Header) (HTTPResponse, error) {
	if params == nil {
		params = url.Values{}
	}

	return h.DoRaw(method, rURL, []byte(params.Encode()), headers)
}

// DoRaw executes an HTTP request using the existing query/form conventions:
// GET and DELETE replace the URL query, POST and PUT send a form body, and other
// methods ignore reqBody. Use Client.DoRaw to send a body with any method.
func (h *httpClient) DoRaw(method, rURL string, reqBody []byte, headers http.Header) (HTTPResponse, error) {
	var postBody io.Reader
	if method == http.MethodPost || method == http.MethodPut {
		postBody = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequest(method, rURL, postBody)
	if err != nil {
		h.hLog.Printf("Request preparation failed: %v", err)
		return HTTPResponse{}, NewError(NetworkError, "Request preparation failed.", nil)
	}
	if headers != nil {
		req.Header = headers
	}
	if req.Header.Get("Content-Type") == "" && (method == http.MethodPost || method == http.MethodPut) {
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodGet || method == http.MethodDelete {
		req.URL.RawQuery = string(reqBody)
	}
	return h.sendRequest(req.Context(), method, req)
}

func (h *httpClient) doParamsWithContext(ctx context.Context, method, rURL string, params []byte, headers http.Header) (HTTPResponse, error) {
	if method == "" {
		method = http.MethodGet
	}
	if method == http.MethodGet || method == http.MethodDelete || method == http.MethodHead {
		return h.doRequestWithContext(ctx, method, rURL, nil, string(params), headers)
	}
	return h.doRawWithContext(ctx, method, rURL, params, headers)
}

func (h *httpClient) doRawWithContext(ctx context.Context, method, rURL string, reqBody []byte, headers http.Header) (HTTPResponse, error) {
	return h.doRequestWithContext(ctx, method, rURL, reqBody, "", headers)
}

func (h *httpClient) doRequestWithContext(ctx context.Context, method, rURL string, reqBody []byte, query string, headers http.Header) (HTTPResponse, error) {
	resp := HTTPResponse{}
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, rURL, bytes.NewReader(reqBody))
	if err != nil {
		h.hLog.Printf("Request preparation failed: %v", err)
		return resp, NewError(NetworkError, "Request preparation failed.", nil)
	}

	if headers != nil {
		req.Header = headers.Clone()
	}

	// If a content-type isn't set, set the default one.
	if req.Header.Get("Content-Type") == "" {
		if len(reqBody) > 0 || (method != http.MethodGet && method != http.MethodDelete && method != http.MethodHead) {
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
		}
	}

	// Preserve any query string supplied in the URL, including repeated keys.
	if query != "" {
		if req.URL.RawQuery != "" {
			req.URL.RawQuery += "&"
		}
		req.URL.RawQuery += query
	}

	return h.sendRequest(ctx, method, req)
}

func (h *httpClient) sendRequest(ctx context.Context, method string, req *http.Request) (HTTPResponse, error) {
	resp := HTTPResponse{}
	r, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		h.hLog.Printf("Request failed: %v", err)
		return resp, NewError(NetworkError, "Request failed.", nil)
	}

	defer r.Body.Close()

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		h.hLog.Printf("Unable to read response: %v", err)
		return resp, NewError(DataError, "Error reading response.", nil)
	}

	resp.Response = r
	resp.Body = body
	if h.debug {
		h.hLog.Printf("%s %s -- %d %v", method, req.URL.RequestURI(), resp.Response.StatusCode, req.Header)
	}

	return resp, nil
}

// DoEnvelope makes an HTTP request and parses the JSON response (fastglue envelop structure)
func (h *httpClient) DoEnvelope(method, url string, params url.Values, headers http.Header, obj interface{}) error {
	resp, err := h.Do(method, url, params, headers)
	if err != nil {
		return err
	}

	err = readEnvelope(resp, obj)
	if err != nil {
		if _, ok := err.(Error); !ok {
			h.hLog.Printf("Error parsing JSON response: %v", err)
		}
	}

	return err
}

func readEnvelope(resp HTTPResponse, obj interface{}) error {
	return ReadEnvelope(resp, obj)
}

// ReadEnvelope decodes the data field of a buffered Kite response into obj.
// Pass a pointer to the destination, or nil to discard data. HTTP error status
// codes are decoded into Error with the server's code, error_type, message, and
// data. A missing HTTP response or invalid JSON produces a DataError.
func ReadEnvelope(resp HTTPResponse, obj interface{}) error {
	if resp.Response == nil {
		return NewError(DataError, "Missing HTTP response.", nil)
	}
	// Successful request, but error envelope.
	if resp.Response.StatusCode >= http.StatusBadRequest {
		var e errorEnvelope
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return NewError(DataError, "Error parsing response.", nil)
		}

		return newError(e.ErrorType, e.Message, resp.Response.StatusCode, e.Data)
	}

	// We now unmarshal the body.
	envl := envelope{}
	envl.Data = obj

	if err := json.Unmarshal(resp.Body, &envl); err != nil {
		return NewError(DataError, "Error parsing response.", nil)
	}

	return nil
}

// DoJSON makes an HTTP request and parses the JSON response.
func (h *httpClient) DoJSON(method, url string, params url.Values, headers http.Header, obj interface{}) (HTTPResponse, error) {
	resp, err := h.Do(method, url, params, headers)
	if err != nil {
		return resp, err
	}

	// We now unmarshal the body.
	if err := json.Unmarshal(resp.Body, &obj); err != nil {
		h.hLog.Printf("Error parsing JSON response: %v | %s", err, resp.Body)
		return resp, NewError(DataError, "Error parsing response.", nil)
	}

	return resp, nil
}

// GetClient return's the underlying net/http client.
func (h *httpClient) GetClient() *httpClient {
	return h
}
