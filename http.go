package kiteconnect

/*
	HTTP helper functions with methods for parsing Kite style JSON envelopes.
*/

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HTTPClient represents an HTTP client.
type HTTPClient interface {
	Do(method, rURL string, params url.Values, headers http.Header) (HTTPResponse, error)
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

// HTTPResponse encompasses byte body  + the response of an HTTP request.
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

// Do executes an HTTP request and returns the response.
func (h *httpClient) Do(method, rURL string, params url.Values, headers http.Header) (HTTPResponse, error) {
	var (
		resp       = HTTPResponse{}
		postParams io.Reader
		err        error
	)

	if params == nil {
		params = url.Values{}
	}

	// Encode POST / PUT params.
	if method == http.MethodPost || method == http.MethodPut {
		postParams = strings.NewReader(params.Encode())
	}

	req, err := http.NewRequest(method, rURL, postParams)
	if err != nil {
		h.hLog.Printf("Request preparation failed: %v", err)
		return resp, NewError(NetworkError, "Request preparation failed.", nil)
	}

	if headers != nil {
		req.Header = headers
	}

	// If a content-type isn't set, set the default one.
	if req.Header.Get("Content-Type") == "" {
		if method == http.MethodPost || method == http.MethodPut {
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
		}
	}

	// If the request method is GET or DELETE, add the params as QueryString.
	if method == http.MethodGet || method == http.MethodDelete {
		req.URL.RawQuery = params.Encode()
	}

	r, err := h.client.Do(req)
	if err != nil {
		h.hLog.Printf("Request failed: %v", err)
		return resp, NewError(NetworkError, "Request failed.", nil)
	}

	defer r.Body.Close()

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
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

	// Successful request, but error envelope.
	if resp.Response.StatusCode >= http.StatusBadRequest {
		var e errorEnvelope
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			h.hLog.Printf("Error parsing JSON response: %v", err)
			return NewError(DataError, "Error parsing response.", nil)
		}

		if resp.Response.StatusCode == http.StatusPreconditionRequired {
			return NewError(TPINAuthError, e.Message, e.Data)
		}
		return NewError(e.ErrorType, e.Message, e.Data)
	}

	// We now unmarshal the body.
	envl := envelope{}
	envl.Data = obj

	if err := json.Unmarshal(resp.Body, &envl); err != nil {
		h.hLog.Printf("Error parsing JSON response: %v | %s", err, resp.Body)
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
