package cryptopay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// ListAssets returns the tokens the service is configured to watch, with each
// one's step and decimals.
//
// Worth reading at startup rather than hardcoding: step and decimals decide how
// an amount is rendered and compared, and they live in the operator's config
// file, not in your code.
func (c *Client) ListAssets(ctx context.Context) ([]Asset, error) {
	var out struct {
		Assets []Asset `json:"assets"`
	}
	_, err := c.do(ctx, http.MethodGet, apiPrefix+"/assets", nil, nil, &out)
	return out.Assets, err
}

// ListOrphans returns transfers that arrived and matched no invoice, newest
// first. limit of zero uses the service's default; it clamps oversized values.
//
// This is a reconciliation surface, not a background job: a customer who sent
// the wrong amount, or paid an invoice that had already expired, ends up here
// and needs a human. Surfacing it to whoever handles support is usually the
// right integration.
func (c *Client) ListOrphans(ctx context.Context, limit int32) ([]Orphan, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.FormatInt(int64(limit), 10))
	}

	var out struct {
		Orphans []Orphan `json:"orphans"`
	}
	_, err := c.do(ctx, http.MethodGet, apiPrefix+"/orphans", q, nil, &out)
	return out.Orphans, err
}

// Health probes the service. It needs no API key, so it works as a readiness
// check from anywhere that can reach the port.
//
// An unhealthy service answers 503, and this returns both the reason and an
// error matching ErrServer. The body is decoded either way: /healthz answers
// with the same {"data": ...} shape at 503 as at 200, and "which dependency is
// down" is the only useful thing the call can tell you.
func (c *Client) Health(ctx context.Context) (Health, error) {
	const endpoint = http.MethodGet + " /healthz"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return Health{}, fmt.Errorf("cryptopay: %s: build request: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return Health{}, fmt.Errorf("cryptopay: %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Data Health `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	_ = json.Unmarshal(raw, &envelope)

	if resp.StatusCode >= 300 {
		return envelope.Data, &APIError{
			StatusCode: resp.StatusCode,
			Message:    envelope.Data.Status,
			Endpoint:   endpoint,
		}
	}
	return envelope.Data, nil
}
