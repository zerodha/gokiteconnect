package kiteconnect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Do executes a request to a custom endpoint using the client's authentication,
// headers, base URI, and HTTP client. The uri must begin with a single slash;
// absolute URLs are not accepted. A path prefix in the base URI is preserved.
//
// GET, DELETE, and HEAD parameters are appended to the URI's query string,
// including repeated values. Other methods send params as a URL-encoded body.
// Headers and params are not modified. Client authentication, X-Kite-Version,
// and User-Agent headers take precedence over supplied headers.
//
// HTTP error status codes do not produce an error here. Use DoEnvelope or
// ReadEnvelope to decode Kite responses and handle API errors. The response
// body is fully read into HTTPResponse.Body and closed before returning.
func (c *Client) Do(method, uri string, params url.Values, headers http.Header) (HTTPResponse, error) {
	return c.DoWithContext(context.Background(), method, uri, params, headers)
}

// DoWithContext is Do with a context controlling the request and response read.
// Cancellation returns ctx.Err(); other request errors use the Kite error types.
func (c *Client) DoWithContext(ctx context.Context, method, uri string, params url.Values, headers http.Header) (HTTPResponse, error) {
	rURL, err := c.requestURL(uri)
	if err != nil {
		return HTTPResponse{}, err
	}
	return c.httpClient.GetClient().doParamsWithContext(ctx, method, rURL, []byte(params.Encode()), c.requestHeaders(headers))
}

// DoRaw sends a caller-encoded request body, including for DELETE. Any query
// parameters must be supplied in uri. It otherwise follows Do's authentication,
// header, URL, and response handling. Set Content-Type when sending JSON or
// another non-form encoding; bodies default to application/x-www-form-urlencoded.
func (c *Client) DoRaw(method, uri string, reqBody []byte, headers http.Header) (HTTPResponse, error) {
	return c.DoRawWithContext(context.Background(), method, uri, reqBody, headers)
}

// DoRawWithContext is DoRaw with a context controlling the request and response
// read. Cancellation returns ctx.Err(); other request errors use Kite error types.
func (c *Client) DoRawWithContext(ctx context.Context, method, uri string, reqBody []byte, headers http.Header) (HTTPResponse, error) {
	rURL, err := c.requestURL(uri)
	if err != nil {
		return HTTPResponse{}, err
	}
	return c.httpClient.GetClient().doRawWithContext(ctx, method, rURL, reqBody, c.requestHeaders(headers))
}

// DoEnvelope is Do with Kite envelope decoding. The response's data field is
// decoded into v, which should be a pointer to the caller's response type.
// Pass nil to discard data while still checking for API and decoding errors.
func (c *Client) DoEnvelope(method, uri string, params url.Values, headers http.Header, v interface{}) error {
	return c.DoEnvelopeWithContext(context.Background(), method, uri, params, headers, v)
}

// DoEnvelopeWithContext is DoEnvelope with a context controlling the request
// and response read. It supports the same cancellation errors as DoWithContext.
func (c *Client) DoEnvelopeWithContext(ctx context.Context, method, uri string, params url.Values, headers http.Header, v interface{}) error {
	resp, err := c.DoWithContext(ctx, method, uri, params, headers)
	if err != nil {
		return err
	}
	return ReadEnvelope(resp, v)
}

func (c *Client) requestHeaders(headers http.Header) http.Header {
	out := make(http.Header, len(headers)+3)
	for key, values := range headers {
		key = http.CanonicalHeaderKey(key)
		out[key] = append(out[key], values...)
	}
	out.Set("X-Kite-Version", kiteHeaderVersion)
	out.Set("User-Agent", c.userAgent())
	if c.apiKey != "" && c.accessToken != "" {
		out.Set("Authorization", fmt.Sprintf("token %s:%s", c.apiKey, c.accessToken))
	}
	return out
}

func (c *Client) requestURL(uri string) (string, error) {
	if !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") {
		return "", NewError(InputError, "Endpoint URI must begin with a single slash.", nil)
	}
	return strings.TrimRight(c.baseURI, "/") + uri, nil
}
