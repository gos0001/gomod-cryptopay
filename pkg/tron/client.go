// Package tron is a TronGrid client covering exactly what a payment watcher
// needs: incoming TRC20 transfers to one address, and the two block heads that
// bracket finality.
//
// The shapes here were established by live request, not from documentation —
// see docs/chain-apis.md. Two of those findings drive the design:
//
//   - The TRC20 transfer feed carries no block number, only a block timestamp.
//     There is therefore no per-transfer lookup to be done; finality is decided
//     by comparing a transfer's timestamp against the solidified head's, which
//     keeps a polling cycle at a flat two requests however many transfers arrive.
//
//   - Two error shapes coexist. The /v1/… endpoints answer with a `success`
//     field, while /wallet/… endpoints do not, and a 429 arrives as
//     {"Error": "…"} with a capital E. A client that knows only one of these
//     reports the wrong thing.
//
// Zero domain imports.
package tron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/ratebudget"
)

var (
	// ErrRateLimited means the per-second ceiling was crossed. The key is
	// suspended for roughly 27 seconds afterwards, so this is not a "retry
	// immediately" error.
	ErrRateLimited = errors.New("tron: rate limited")

	// ErrForbidden is the keyless-mode penalty or a rejected key: TronGrid
	// answers 403 and blocks for around 30 seconds. Distinct from rate limiting
	// because the fix is different — configure a key.
	ErrForbidden = errors.New("tron: forbidden")

	// ErrBadRequest covers the API refusing the query itself. Notably limit>200
	// arrives as HTTP 400 with an empty body, so there is nothing to unwrap.
	ErrBadRequest = errors.New("tron: bad request")

	// ErrUnexpectedResponse means the body did not parse, or `success` was false
	// without an explanation.
	ErrUnexpectedResponse = errors.New("tron: unexpected response")
)

type Client struct {
	cfg    Config
	budget *ratebudget.Budget
	http   *http.Client
}

// New builds the client and its own budget: the daily quota is a property of the
// API key, so it belongs with whatever holds the key.
func New(cfg Config) *Client {
	return &Client{
		cfg:    cfg,
		budget: ratebudget.New(cfg.DailyRequestBudget, cfg.QPS),
		http:   &http.Client{Timeout: cfg.Timeout.Std()},
	}
}

// Budget exposes the limiter so a caller can log remaining quota.
func (c *Client) Budget() *ratebudget.Budget { return c.budget }

// Transfer is one incoming TRC20 transfer.
//
// There is no block number, because the feed does not carry one, and no log
// index, because one record is one transfer.
type Transfer struct {
	TxID            string
	From            string
	To              string
	Value           *big.Int
	ContractAddress string
	Symbol          string
	Decimals        int32
	BlockTime       time.Time
}

// TransfersQuery describes one page request.
type TransfersQuery struct {
	// Address is the watched receiving address, in base58.
	Address string
	// MinTimestamp bounds the scan; zero means no lower bound.
	MinTimestamp time.Time
	// Limit caps the page. Zero uses MaxPageLimit.
	Limit int
	// OnlyConfirmed restricts the feed to transfers at or below the solidified
	// head — verified to be TronGrid's own finality line.
	OnlyConfirmed bool
	// Fingerprint continues a previous page.
	Fingerprint string
}

type TransfersPage struct {
	Transfers []Transfer
	// Fingerprint is empty on the last page.
	Fingerprint string
}

// TRC20Transfers lists incoming TRC20 transfers to one address, **oldest first**.
//
// Ascending order because the caller is advancing a cursor: processing oldest
// first means the cursor only ever moves forward, and an interrupted page can be
// resumed without re-reading what was already handled. Newest-first would need
// the whole page buffered before anything could be committed.
func (c *Client) TRC20Transfers(ctx context.Context, q TransfersQuery) (TransfersPage, error) {
	if q.Address == "" {
		return TransfersPage{}, fmt.Errorf("%w: address is required", ErrBadRequest)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = MaxPageLimit
	}
	if limit > MaxPageLimit {
		// Refused here rather than at the API, which answers 400 with an empty
		// body — an error the caller could not interpret.
		return TransfersPage{}, fmt.Errorf("%w: limit %d exceeds the API maximum of %d",
			ErrBadRequest, limit, MaxPageLimit)
	}

	params := url.Values{}
	params.Set("only_to", "true")
	params.Set("limit", strconv.Itoa(limit))
	params.Set("order_by", "block_timestamp,asc")
	if !q.MinTimestamp.IsZero() {
		params.Set("min_timestamp", strconv.FormatInt(q.MinTimestamp.UnixMilli(), 10))
	}
	if q.OnlyConfirmed {
		params.Set("only_confirmed", "true")
	}
	if q.Fingerprint != "" {
		params.Set("fingerprint", q.Fingerprint)
	}

	endpoint := fmt.Sprintf("%s/v1/accounts/%s/transactions/trc20?%s",
		c.cfg.APIURL, url.PathEscape(q.Address), params.Encode())

	body, err := c.do(ctx, http.MethodGet, endpoint)
	if err != nil {
		return TransfersPage{}, err
	}

	var payload struct {
		Success bool `json:"success"`
		Error   any  `json:"error"`
		Data    []struct {
			TxID      string `json:"transaction_id"`
			From      string `json:"from"`
			To        string `json:"to"`
			Value     string `json:"value"`
			Type      string `json:"type"`
			BlockTime int64  `json:"block_timestamp"`
			TokenInfo struct {
				Symbol   string `json:"symbol"`
				Address  string `json:"address"`
				Decimals int32  `json:"decimals"`
			} `json:"token_info"`
		} `json:"data"`
		Meta struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return TransfersPage{}, fmt.Errorf("%w: parsing the transfer feed: %v", ErrUnexpectedResponse, err)
	}
	if !payload.Success {
		return TransfersPage{}, fmt.Errorf("%w: the API reported failure: %v", ErrUnexpectedResponse, payload.Error)
	}

	out := TransfersPage{
		Transfers:   make([]Transfer, 0, len(payload.Data)),
		Fingerprint: payload.Meta.Fingerprint,
	}
	for _, r := range payload.Data {
		// The feed carries every TRC20 event type; only plain transfers move
		// money into the address.
		if r.Type != "" && r.Type != "Transfer" {
			continue
		}

		value, ok := new(big.Int).SetString(r.Value, 10)
		if !ok {
			return TransfersPage{}, fmt.Errorf("%w: transfer %s has an unparsable value %q",
				ErrUnexpectedResponse, r.TxID, r.Value)
		}

		out.Transfers = append(out.Transfers, Transfer{
			TxID:            r.TxID,
			From:            r.From,
			To:              r.To,
			Value:           value,
			ContractAddress: r.TokenInfo.Address,
			Symbol:          r.TokenInfo.Symbol,
			Decimals:        r.TokenInfo.Decimals,
			BlockTime:       time.UnixMilli(r.BlockTime).UTC(),
		})
	}

	return out, nil
}

// Block is a chain head.
type Block struct {
	Number int64
	Time   time.Time
}

// NowBlock returns the current head.
func (c *Client) NowBlock(ctx context.Context) (Block, error) {
	return c.block(ctx, c.cfg.APIURL+"/wallet/getnowblock")
}

// SolidifiedBlock returns the irreversible head.
//
// This is the finality line for TRON. Measured to sit 18–19 blocks and about 57
// seconds behind the current head, which matches the consensus depth. A transfer
// whose BlockTime is at or before this block's Time cannot be reorganised away.
func (c *Client) SolidifiedBlock(ctx context.Context) (Block, error) {
	return c.block(ctx, c.cfg.APIURL+"/walletsolidity/getnowblock")
}

func (c *Client) block(ctx context.Context, endpoint string) (Block, error) {
	body, err := c.do(ctx, http.MethodPost, endpoint)
	if err != nil {
		return Block{}, err
	}

	// No `success` field on /wallet/… endpoints; an empty block_header is how a
	// failure shows up.
	var payload struct {
		BlockHeader struct {
			RawData struct {
				Number    int64 `json:"number"`
				Timestamp int64 `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Block{}, fmt.Errorf("%w: parsing a block head: %v", ErrUnexpectedResponse, err)
	}

	raw := payload.BlockHeader.RawData
	if raw.Number == 0 {
		return Block{}, fmt.Errorf("%w: block head carries no number", ErrUnexpectedResponse)
	}

	return Block{Number: raw.Number, Time: time.UnixMilli(raw.Timestamp).UTC()}, nil
}

// do spends budget, performs the request, and turns transport and HTTP failures
// into typed errors.
func (c *Client) do(ctx context.Context, method, endpoint string) ([]byte, error) {
	if err := c.budget.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("tron: building the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.cfg.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tron: %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		if readErr != nil {
			return nil, fmt.Errorf("tron: reading the response: %w", readErr)
		}
		return body, nil

	case http.StatusTooManyRequests:
		// Body shape: {"Error":"The key exceeds the frequency limit(15), and the
		// query server is suspended for 27 s"} — capital E, and no `success`.
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, describe(body))

	case http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s (configure tron.api_key; the keyless "+
			"endpoint blocks for 30 seconds per breach)", ErrForbidden, describe(body))

	case http.StatusBadRequest:
		// Empty-bodied on limit>200, which is why the message has to stand on
		// its own rather than quote the body.
		return nil, fmt.Errorf("%w: the API rejected the query%s", ErrBadRequest, suffix(body))

	default:
		return nil, fmt.Errorf("%w: HTTP %d%s", ErrUnexpectedResponse, resp.StatusCode, suffix(body))
	}
}

// describe pulls a message out of either error shape, falling back to the raw
// body so nothing is swallowed.
func describe(body []byte) string {
	if len(body) == 0 {
		return "empty response body"
	}

	var payload struct {
		Error      string `json:"Error"`
		LowerError string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Error != "" {
			return payload.Error
		}
		if payload.LowerError != "" {
			return payload.LowerError
		}
	}

	const maxLen = 200
	if len(body) > maxLen {
		return string(body[:maxLen]) + "…"
	}
	return string(body)
}

func suffix(body []byte) string {
	if len(body) == 0 {
		return " (empty response body)"
	}
	return ": " + describe(body)
}
