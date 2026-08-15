package cryptopay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HeaderAPIKey is the header every authenticated call carries.
const HeaderAPIKey = "X-Api-Key"

// apiPrefix is where every authenticated endpoint lives. /healthz sits outside
// it and outside the key.
const apiPrefix = "/api/v1"

// maxErrorBody bounds how much of a failed response is read. The answer may come
// from a proxy rather than from the service, and those are sometimes HTML pages
// of unbounded size.
const maxErrorBody = 8 << 10

// Client talks to one cryptopay service.
//
// Safe for concurrent use. Create one per service and share it — the underlying
// http.Client pools connections, and a client per request throws that away.
type Client struct {
	baseURL   string
	apiKey    string
	http      *http.Client
	userAgent string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient supplies the transport, for callers who need their own timeouts,
// proxy, TLS configuration or instrumentation. It replaces the default entirely,
// including its timeout.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithTimeout sets the per-request timeout on the default transport. Ignored
// when WithHTTPClient supplied one — that client's own timeout applies.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// WithUserAgent identifies your service in the payment service's access log,
// which is what makes "who is hammering /invoices" answerable.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// New builds a client for the service at baseURL, authenticating with apiKey.
//
// The key belongs on a server. Every endpoint here is authenticated, and the key
// grants listing every invoice, cancelling any of them and reading orphan
// transfers — so it must never reach a browser, a mobile app, or anything else a
// user can read.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		http:      &http.Client{Timeout: 30 * time.Second},
		userAgent: "gomod-cryptopay-client",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// do performs one request and decodes the {"data": ...} envelope into out.
//
// body is marshalled when non-nil; query is appended when non-empty; out may be
// nil for responses nobody reads. The returned int is the status code, which
// CreateInvoice needs to tell 201 from 200.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) (int, error) {
	endpoint := method + " " + path

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("cryptopay: %s: encode request: %w", endpoint, err)
		}
		reader = bytes.NewReader(encoded)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, fmt.Errorf("cryptopay: %s: build request: %w", endpoint, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	// /healthz takes no key, and sending one there would put the credential in
	// the logs of every load balancer that probes it.
	if c.apiKey != "" && strings.HasPrefix(path, apiPrefix) {
		req.Header.Set(HeaderAPIKey, c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Returned unwrapped in kind, so errors.Is against context.Canceled and
		// context.DeadlineExceeded still reaches the caller.
		return 0, fmt.Errorf("cryptopay: %s: %w", endpoint, err)
	}
	defer func() {
		// Drained before closing so the connection can be reused rather than
		// dropped, which matters for a client that polls.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 300 {
		return resp.StatusCode, apiError(resp, endpoint)
	}

	if out == nil {
		return resp.StatusCode, nil
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return resp.StatusCode, fmt.Errorf("cryptopay: %s: decode response: %w", endpoint, err)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("cryptopay: %s: decode data: %w", endpoint, err)
	}
	return resp.StatusCode, nil
}

// apiError turns a non-2xx response into an *APIError, reading the message out
// of the envelope when there is one.
func apiError(resp *http.Response, endpoint string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	var envelope struct {
		Error string `json:"error"`
	}
	// Ignored on purpose: a response from a proxy is not JSON, and that is not a
	// second failure to report — the status code is the answer either way.
	_ = json.Unmarshal(raw, &envelope)

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    envelope.Error,
		Endpoint:   endpoint,
	}
}
