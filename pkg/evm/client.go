// Package evm is a minimal JSON-RPC client for the three calls a payment
// watcher makes against an EVM chain: the head, the finalised head, and event
// logs.
//
// Deliberately not go-ethereum: that pulls in a chain implementation to make
// three HTTP calls, and the shapes involved are a handful of hex strings.
//
// Endpoints are rotated per request rather than only on failure. Spreading
// requests across endpoints is the primary purpose — each URL may carry its own
// API key, and rotating them spreads quota — while failover falls out of the
// same mechanism.
//
// Zero domain imports.
package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrRangeTooDeep means the endpoint does not hold history that far back.
	//
	// A configuration problem rather than a transient one: free public nodes
	// serve roughly an hour of logs, so a service that was down longer cannot
	// catch up against them. The fix is an endpoint with history, which is why
	// this error names the situation instead of looking like a random failure.
	ErrRangeTooDeep = errors.New("evm: endpoint does not serve logs that far back")

	// ErrResultLimit means the node refused the query as too large.
	//
	// On the bsc-dataseed.* family this arrives for every eth_getLogs call
	// regardless of range — those nodes simply do not serve logs, however
	// healthy eth_blockNumber makes them look.
	ErrResultLimit = errors.New("evm: node refused the log query as too large")

	ErrRateLimited = errors.New("evm: rate limited")

	// ErrAllEndpointsFailed carries every endpoint's reason, because with
	// rotation in play "the request failed" says nothing about which node.
	ErrAllEndpointsFailed = errors.New("evm: every endpoint failed")

	// ErrRangeTooWide is raised locally: chunking belongs to the caller, which
	// needs to persist a cursor per chunk.
	ErrRangeTooWide = errors.New("evm: block range is wider than log_range")

	ErrUnexpectedResponse = errors.New("evm: unexpected response")

	// ErrFinalizedTagUnsupported means the node does not serve the tag, so the
	// caller should fall back to counting confirmations.
	ErrFinalizedTagUnsupported = errors.New("evm: endpoint does not support the finalized tag")
)

type Client struct {
	cfg  Config
	http *http.Client

	// cursor rotates endpoints. Atomic rather than mutex-guarded: it is a hint,
	// and an occasional duplicate pick costs nothing.
	cursor atomic.Uint64

	mu       sync.Mutex
	cooldown map[string]time.Time

	now func() time.Time
}

func New(cfg Config) (*Client, error) {
	if len(cfg.RPCURLs) == 0 {
		return nil, errors.New("evm: no RPC endpoints configured")
	}
	return &Client{
		cfg:      cfg,
		http:     &http.Client{Timeout: cfg.Timeout.Std()},
		cooldown: make(map[string]time.Time),
		now:      time.Now,
	}, nil
}

// LogRange is the widest block span GetLogs accepts, so a caller can size its
// chunks without duplicating the setting.
func (c *Client) LogRange() uint64 { return c.cfg.LogRange }

// Confirmations is the fallback depth for endpoints without the finalized tag.
func (c *Client) Confirmations() int64 { return c.cfg.Confirmations }

// ReorgDepth is how far a cursor should be rewound at startup.
func (c *Client) ReorgDepth() int64 { return c.cfg.ReorgDepth }

// UseFinalizedTag reports the configured preference.
func (c *Client) UseFinalizedTag() bool { return c.cfg.UseFinalizedTag }

// BlockNumber returns the current head.
func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	var hex string
	if err := c.call(ctx, "eth_blockNumber", []any{}, &hex); err != nil {
		return 0, err
	}
	return parseQuantity(hex)
}

// FinalizedBlockNumber returns the head of the finalised chain.
//
// Measured lag on BSC is one to three blocks, which makes this strictly better
// than counting confirmations: it is the chain's own answer rather than a guess,
// and it is served even by nodes that refuse log queries.
func (c *Client) FinalizedBlockNumber(ctx context.Context) (uint64, error) {
	var head struct {
		Number string `json:"number"`
	}
	if err := c.call(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &head); err != nil {
		if errors.Is(err, ErrUnexpectedResponse) || isUnsupportedTag(err) {
			return 0, fmt.Errorf("%w: %v", ErrFinalizedTagUnsupported, err)
		}
		return 0, err
	}
	if head.Number == "" {
		// Some nodes answer null rather than an error when the tag is unknown.
		return 0, ErrFinalizedTagUnsupported
	}
	return parseQuantity(head.Number)
}

// LogQuery selects logs by block range, contract and topics.
//
// Topics is positional: index 0 is the event signature, and a nil slice at a
// position matches anything there. For an ERC20 transfer to one recipient that
// is {{TopicTransfer}, nil, {paddedRecipient}}.
type LogQuery struct {
	FromBlock uint64
	ToBlock   uint64
	Addresses []string
	Topics    [][]string
}

type Log struct {
	Address     string
	TxHash      string
	BlockNumber uint64
	BlockHash   string
	LogIndex    uint64
	Topics      []string
	Data        string
	// Removed is true for a log that a reorg has un-mined.
	Removed bool
}

// GetLogs fetches logs for a block range no wider than LogRange.
//
// Chunking is refused rather than performed: the caller has to persist a cursor
// per chunk, and a client that silently split the range would take that away —
// after downtime a single call could then span days and return everything or
// nothing.
func (c *Client) GetLogs(ctx context.Context, q LogQuery) ([]Log, error) {
	if q.ToBlock < q.FromBlock {
		return nil, fmt.Errorf("%w: toBlock %d is below fromBlock %d",
			ErrUnexpectedResponse, q.ToBlock, q.FromBlock)
	}
	if span := q.ToBlock - q.FromBlock + 1; span > c.cfg.LogRange {
		return nil, fmt.Errorf("%w: %d blocks requested, limit is %d; chunk the range and "+
			"persist the cursor per chunk", ErrRangeTooWide, span, c.cfg.LogRange)
	}

	filter := map[string]any{
		"fromBlock": quantity(q.FromBlock),
		"toBlock":   quantity(q.ToBlock),
	}
	if len(q.Addresses) > 0 {
		lower := make([]string, 0, len(q.Addresses))
		for _, a := range q.Addresses {
			lower = append(lower, strings.ToLower(a))
		}
		filter["address"] = lower
	}
	if len(q.Topics) > 0 {
		// A nil at a position means "anything"; encoding/json renders that as
		// null, which is what the RPC spec asks for.
		topics := make([]any, 0, len(q.Topics))
		for _, group := range q.Topics {
			switch len(group) {
			case 0:
				topics = append(topics, nil)
			case 1:
				topics = append(topics, strings.ToLower(group[0]))
			default:
				lower := make([]string, 0, len(group))
				for _, t := range group {
					lower = append(lower, strings.ToLower(t))
				}
				topics = append(topics, lower)
			}
		}
		filter["topics"] = topics
	}

	var raw []struct {
		Address     string   `json:"address"`
		TxHash      string   `json:"transactionHash"`
		BlockNumber string   `json:"blockNumber"`
		BlockHash   string   `json:"blockHash"`
		LogIndex    string   `json:"logIndex"`
		Topics      []string `json:"topics"`
		Data        string   `json:"data"`
		Removed     bool     `json:"removed"`
	}
	if err := c.call(ctx, "eth_getLogs", []any{filter}, &raw); err != nil {
		return nil, err
	}

	out := make([]Log, 0, len(raw))
	for _, r := range raw {
		blockNumber, err := parseQuantity(r.BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("%w: log block number: %v", ErrUnexpectedResponse, err)
		}
		logIndex, err := parseQuantity(r.LogIndex)
		if err != nil {
			return nil, fmt.Errorf("%w: log index: %v", ErrUnexpectedResponse, err)
		}

		out = append(out, Log{
			Address:     strings.ToLower(r.Address),
			TxHash:      r.TxHash,
			BlockNumber: blockNumber,
			BlockHash:   r.BlockHash,
			LogIndex:    logIndex,
			Topics:      r.Topics,
			Data:        r.Data,
			Removed:     r.Removed,
		})
	}
	return out, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e rpcError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

// call tries endpoints in rotation until one answers.
//
// An RPC-level error — the node understood the request and refused it — is
// returned immediately rather than retried elsewhere: every endpoint would
// refuse it the same way, and retrying only multiplies the load. Transport
// failures do move on to the next endpoint.
func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	endpoints := c.rotated()

	var reasons []string
	for _, endpoint := range endpoints {
		body, err := c.post(ctx, endpoint, method, params)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.markFailed(endpoint)
			reasons = append(reasons, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}

		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			c.markFailed(endpoint)
			reasons = append(reasons, fmt.Sprintf("%s: unparsable response: %v", endpoint, err))
			continue
		}

		if envelope.Error != nil {
			return classify(endpoint, *envelope.Error)
		}
		if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
			return fmt.Errorf("%w: %s returned no result", ErrUnexpectedResponse, endpoint)
		}
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("%w: decoding the result of %s: %v", ErrUnexpectedResponse, method, err)
		}
		return nil
	}

	return fmt.Errorf("%w: %s", ErrAllEndpointsFailed, strings.Join(reasons, "; "))
}

func (c *Client) post(ctx context.Context, endpoint, method string, params []any) ([]byte, error) {
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	return body, nil
}

// classify turns a node's refusal into something the caller can branch on.
func classify(endpoint string, e rpcError) error {
	msg := strings.ToLower(e.Message)

	switch {
	case e.Code == -32005, strings.Contains(msg, "limit exceeded"):
		return fmt.Errorf("%w: %s said %q — some public nodes (the bsc-dataseed family "+
			"among them) refuse log queries at any range and are unusable for watching",
			ErrResultLimit, endpoint, e.Message)

	case strings.Contains(msg, "archive"), strings.Contains(msg, "too old"),
		strings.Contains(msg, "beyond the last"):
		return fmt.Errorf("%w: %s said %q — use an endpoint that retains history",
			ErrRangeTooDeep, endpoint, e.Message)

	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"):
		return fmt.Errorf("%w: %s said %q", ErrRateLimited, endpoint, e.Message)

	default:
		return fmt.Errorf("%w: %s said %q (code %d)",
			ErrUnexpectedResponse, endpoint, e.Message, e.Code)
	}
}

func isUnsupportedTag(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "invalid argument") ||
		strings.Contains(msg, "not found")
}

// rotated returns the endpoints to try, starting one further along than the last
// call and with cooled-down ones pushed to the back.
//
// Pushed back rather than dropped: if every endpoint is in cooldown, trying them
// anyway beats refusing to make the request at all.
func (c *Client) rotated() []string {
	n := uint64(len(c.cfg.RPCURLs))
	start := c.cursor.Add(1) % n

	var ready, cooling []string
	for i := uint64(0); i < n; i++ {
		endpoint := c.cfg.RPCURLs[(start+i)%n]
		if c.isCooling(endpoint) {
			cooling = append(cooling, endpoint)
			continue
		}
		ready = append(ready, endpoint)
	}
	return append(ready, cooling...)
}

func (c *Client) isCooling(endpoint string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	until, ok := c.cooldown[endpoint]
	return ok && c.now().Before(until)
}

func (c *Client) markFailed(endpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cooldown[endpoint] = c.now().Add(c.cfg.FailureCooldown.Std())
}

// quantity renders a block number the way the RPC spec wants it: hex, 0x-prefixed,
// no leading zeros.
func quantity(n uint64) string { return "0x" + strconv.FormatUint(n, 16) }

func parseQuantity(s string) (uint64, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if trimmed == "" {
		return 0, fmt.Errorf("%q is not a hex quantity", s)
	}
	n, err := strconv.ParseUint(trimmed, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a hex quantity: %w", s, err)
	}
	return n, nil
}

func truncate(body []byte) string {
	const maxLen = 200
	if len(body) > maxLen {
		return string(body[:maxLen]) + "…"
	}
	if len(body) == 0 {
		return "empty response body"
	}
	return string(body)
}
