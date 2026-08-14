package evm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// stub is an RPC endpoint that records the requests it received.
type stub struct {
	*httptest.Server
	mu    sync.Mutex
	calls []rpcRequest
}

func newStub(t *testing.T, reply func(req rpcRequest) string) *stub {
	t.Helper()

	s := &stub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)

		s.mu.Lock()
		s.calls = append(s.calls, req)
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply(req)))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stub) last() rpcRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

func client(t *testing.T, urls ...string) *Client {
	t.Helper()

	c, err := New(Config{
		RPCURLs:         urls,
		LogRange:        DefaultLogRange,
		Confirmations:   DefaultConfirmations,
		ReorgDepth:      DefaultReorgDepth,
		Timeout:         config.Duration(2 * time.Second),
		FailureCooldown: config.Duration(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func result(v string) string { return `{"jsonrpc":"2.0","id":1,"result":` + v + `}` }

func rpcFail(code int, message string) string {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"error": map[string]any{"code": code, "message": message},
	})
	return string(b)
}

func TestBlockNumber(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return result(`"0x6e7a141"`) })
	c := client(t, s.URL)

	got, err := c.BlockNumber(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0x6e7a141 {
		t.Fatalf("block = %d", got)
	}
	if m := s.last().Method; m != "eth_blockNumber" {
		t.Errorf("method = %q", m)
	}
}

func TestFinalizedBlockNumber(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return result(`{"number":"0x6e7a13e"}`) })
	c := client(t, s.URL)

	got, err := c.FinalizedBlockNumber(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0x6e7a13e {
		t.Fatalf("finalized = %d", got)
	}

	last := s.last()
	if last.Method != "eth_getBlockByNumber" {
		t.Errorf("method = %q", last.Method)
	}
	if len(last.Params) != 2 || last.Params[0] != "finalized" {
		t.Errorf("params = %v, want the finalized tag", last.Params)
	}
}

// Some nodes answer null rather than an error for an unknown tag; the caller has
// to be able to fall back to counting confirmations either way.
func TestFinalizedTagUnsupported(t *testing.T) {
	for name, reply := range map[string]string{
		"null result": `{"jsonrpc":"2.0","id":1,"result":null}`,
		"rpc error":   rpcFail(-32601, "the method eth_getBlockByNumber does not support finalized"),
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, func(rpcRequest) string { return reply })
			c := client(t, s.URL)

			if _, err := c.FinalizedBlockNumber(context.Background()); !errors.Is(err, ErrFinalizedTagUnsupported) {
				t.Fatalf("got %v, want ErrFinalizedTagUnsupported", err)
			}
		})
	}
}

const oneTransferLog = `[{
  "address": "0x55d398326f99059fF775485246999027B3197955",
  "topics": [
    "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
    "0x0000000000000000000000003bb66ddf2301bf0ca7adb8e75890ed2124508cac",
    "0x000000000000000000000000f977814e90da44bfa03b6295a0616a897441acec"
  ],
  "data": "0x000000000000000000000000000000000000000000000000643cd90bf3a30000",
  "blockNumber": "0x6e7a141",
  "transactionHash": "0xabc",
  "blockHash": "0xdef",
  "logIndex": "0x2a",
  "removed": false
}]`

func TestGetLogsParsesALog(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return result(oneTransferLog) })
	c := client(t, s.URL)

	logs, err := c.GetLogs(context.Background(), LogQuery{FromBlock: 100, ToBlock: 200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d logs", len(logs))
	}

	l := logs[0]
	if l.BlockNumber != 0x6e7a141 {
		t.Errorf("block = %d", l.BlockNumber)
	}
	if l.LogIndex != 42 {
		t.Errorf("log index = %d", l.LogIndex)
	}
	// Lowercased so it compares equal to a configured contract address, which
	// asset_seeder also lowercases.
	if l.Address != "0x55d398326f99059ff775485246999027b3197955" {
		t.Errorf("address = %q, want it lowercased", l.Address)
	}
	if l.Topics[0] != TopicTransfer {
		t.Errorf("topic0 = %q", l.Topics[0])
	}
	if l.Removed {
		t.Error("removed should be false")
	}
}

func TestGetLogsFilterShape(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return result(`[]`) })
	c := client(t, s.URL)

	recipient, err := PadTopic("0xF977814e90dA44bFA03b6295A0616a897441aceC")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.GetLogs(context.Background(), LogQuery{
		FromBlock: 0x10,
		ToBlock:   0x20,
		Addresses: []string{"0x55D398326F99059fF775485246999027B3197955"},
		Topics:    [][]string{{TopicTransfer}, nil, {recipient}},
	})
	if err != nil {
		t.Fatal(err)
	}

	filter, ok := s.last().Params[0].(map[string]any)
	if !ok {
		t.Fatalf("params[0] = %#v", s.last().Params[0])
	}

	if filter["fromBlock"] != "0x10" || filter["toBlock"] != "0x20" {
		t.Errorf("range = %v..%v, want hex quantities", filter["fromBlock"], filter["toBlock"])
	}

	addresses, _ := filter["address"].([]any)
	if len(addresses) != 1 || addresses[0] != "0x55d398326f99059ff775485246999027b3197955" {
		t.Errorf("address filter = %v, want it lowercased", addresses)
	}

	// The nil in the middle is what makes "from anybody, to us" work; sending
	// it as anything other than null would filter on a literal.
	topics, _ := filter["topics"].([]any)
	if len(topics) != 3 {
		t.Fatalf("topics = %v", topics)
	}
	if topics[0] != TopicTransfer {
		t.Errorf("topics[0] = %v", topics[0])
	}
	if topics[1] != nil {
		t.Errorf("topics[1] = %v, want null so any sender matches", topics[1])
	}
	if topics[2] != recipient {
		t.Errorf("topics[2] = %v, want the padded recipient", topics[2])
	}
}

// Chunking belongs to the caller, which persists a cursor per chunk. A client
// that split the range silently would take that away.
func TestGetLogsRefusesTooWideARangeLocally(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return result(`[]`) })
	c := client(t, s.URL)

	_, err := c.GetLogs(context.Background(), LogQuery{FromBlock: 0, ToBlock: c.LogRange()})
	if !errors.Is(err, ErrRangeTooWide) {
		t.Fatalf("got %v, want ErrRangeTooWide", err)
	}
	if s.count() != 0 {
		t.Errorf("made %d requests; the check is local", s.count())
	}

	// Exactly LogRange blocks inclusive must be accepted.
	if _, err := c.GetLogs(context.Background(), LogQuery{FromBlock: 1, ToBlock: c.LogRange()}); err != nil {
		t.Fatalf("a range of exactly log_range should be allowed: %v", err)
	}
}

func TestGetLogsRejectsInvertedRange(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return result(`[]`) })
	c := client(t, s.URL)

	if _, err := c.GetLogs(context.Background(), LogQuery{FromBlock: 200, ToBlock: 100}); err == nil {
		t.Fatal("want an error")
	}
	if s.count() != 0 {
		t.Errorf("made %d requests", s.count())
	}
}

// -32005 is what the bsc-dataseed family answers for every log query; the
// message has to hint that the node may simply be unusable.
func TestResultLimitErrorNamesTheUnusableNodeFamily(t *testing.T) {
	s := newStub(t, func(rpcRequest) string { return rpcFail(-32005, "limit exceeded") })
	c := client(t, s.URL)

	_, err := c.GetLogs(context.Background(), LogQuery{FromBlock: 1, ToBlock: 10})
	if !errors.Is(err, ErrResultLimit) {
		t.Fatalf("got %v, want ErrResultLimit", err)
	}
	if !strings.Contains(err.Error(), "dataseed") {
		t.Errorf("the message should point at the known-bad family: %v", err)
	}
}

func TestRangeTooDeepIsRecognised(t *testing.T) {
	s := newStub(t, func(rpcRequest) string {
		return rpcFail(-32602, "Archive requests require a personal token. Get one at …")
	})
	c := client(t, s.URL)

	_, err := c.GetLogs(context.Background(), LogQuery{FromBlock: 1, ToBlock: 10})
	if !errors.Is(err, ErrRangeTooDeep) {
		t.Fatalf("got %v, want ErrRangeTooDeep", err)
	}
	if !strings.Contains(err.Error(), "retains history") {
		t.Errorf("the message should say what to do: %v", err)
	}
}

func TestRateLimitedFromHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := client(t, srv.URL)

	// A 429 is a transport-level failure here, so with one endpoint it surfaces
	// as every endpoint having failed — with the reason preserved.
	_, err := c.BlockNumber(context.Background())
	if !errors.Is(err, ErrAllEndpointsFailed) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("the reason should survive: %v", err)
	}
}

// Rotation exists to spread requests — and therefore quota — across endpoints,
// so it must happen on every call rather than only after a failure.
func TestEndpointsRotatePerRequest(t *testing.T) {
	a := newStub(t, func(rpcRequest) string { return result(`"0x1"`) })
	b := newStub(t, func(rpcRequest) string { return result(`"0x1"`) })
	c := client(t, a.URL, b.URL)

	for i := 0; i < 6; i++ {
		if _, err := c.BlockNumber(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if a.count() == 0 || b.count() == 0 {
		t.Fatalf("calls landed %d/%d — one endpoint was never used", a.count(), b.count())
	}
	if a.count()+b.count() != 6 {
		t.Fatalf("total calls = %d, want 6", a.count()+b.count())
	}
}

func TestDeadEndpointIsSkippedAfterFailing(t *testing.T) {
	live := newStub(t, func(rpcRequest) string { return result(`"0x1"`) })
	// Closed immediately: connections are refused, which is a transport failure.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	c := client(t, dead.URL, live.URL)

	for i := 0; i < 4; i++ {
		if _, err := c.BlockNumber(context.Background()); err != nil {
			t.Fatalf("call %d should have fallen through to the live endpoint: %v", i, err)
		}
	}
	if live.count() != 4 {
		t.Fatalf("live endpoint served %d of 4 calls", live.count())
	}
	if !c.isCooling(dead.URL) {
		t.Error("the dead endpoint should be in cooldown")
	}
}

func TestAllEndpointsFailedListsEveryReason(t *testing.T) {
	d1 := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	d1.Close()
	d2 := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	d2.Close()

	c := client(t, d1.URL, d2.URL)

	_, err := c.BlockNumber(context.Background())
	if !errors.Is(err, ErrAllEndpointsFailed) {
		t.Fatalf("got %v", err)
	}
	// With rotation in play, "the request failed" says nothing about which node.
	for _, u := range []string{d1.URL, d2.URL} {
		if !strings.Contains(err.Error(), u) {
			t.Errorf("the error should name %s: %v", u, err)
		}
	}
}

// A node that understood the request and refused it will refuse it identically
// everywhere; retrying elsewhere only multiplies load.
func TestRPCErrorIsNotRetriedOnAnotherEndpoint(t *testing.T) {
	a := newStub(t, func(rpcRequest) string { return rpcFail(-32005, "limit exceeded") })
	b := newStub(t, func(rpcRequest) string { return result(`[]`) })
	c := client(t, a.URL, b.URL)

	// Force the first attempt onto a.
	for i := 0; i < 2; i++ {
		_, _ = c.GetLogs(context.Background(), LogQuery{FromBlock: 1, ToBlock: 2})
	}

	if a.count()+b.count() != 2 {
		t.Fatalf("%d calls for 2 requests — an RPC refusal was retried elsewhere", a.count()+b.count())
	}
}

func TestCooldownExpires(t *testing.T) {
	live := newStub(t, func(rpcRequest) string { return result(`"0x1"`) })
	c := client(t, live.URL)

	now := time.Now()
	c.now = func() time.Time { return now }
	c.markFailed(live.URL)

	if !c.isCooling(live.URL) {
		t.Fatal("should be cooling immediately after failing")
	}

	now = now.Add(31 * time.Second)
	if c.isCooling(live.URL) {
		t.Fatal("cooldown should have expired")
	}
}

// Even with every endpoint cooling, the request is attempted: refusing outright
// would turn a transient blip into a permanent outage.
func TestCoolingEndpointsAreStillTriedWhenNothingElseIsLeft(t *testing.T) {
	live := newStub(t, func(rpcRequest) string { return result(`"0x1"`) })
	c := client(t, live.URL)
	c.markFailed(live.URL)

	if _, err := c.BlockNumber(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPadTopic(t *testing.T) {
	got, err := PadTopic("0xF977814e90dA44bFA03b6295A0616a897441aceC")
	if err != nil {
		t.Fatal(err)
	}

	const want = "0x000000000000000000000000f977814e90da44bfa03b6295a0616a897441acec"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if len(got) != 66 {
		t.Fatalf("length = %d, want 66 (0x + 64 hex digits)", len(got))
	}

	// Accepted without the prefix too, and the result is identical.
	bare, err := PadTopic("F977814e90dA44bFA03b6295A0616a897441aceC")
	if err != nil || bare != want {
		t.Fatalf("bare form gave %q, %v", bare, err)
	}
}

func TestPadTopicRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"too short": "0xf977814e",
		"too long":  "0xf977814e90da44bfa03b6295a0616a897441acecff",
		"non-hex":   "0xf977814e90da44bfa03b6295a0616a897441acez",
		"empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PadTopic(in); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func loadFrom(t *testing.T, contents string) (Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	file, err := config.Load(config.Path(path))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return LoadConfig(file)
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadFrom(t, `{"bsc": {"rpc_urls": ["http://localhost:8545"]}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.LogRange != DefaultLogRange {
		t.Errorf("log_range = %d", cfg.LogRange)
	}
	if !cfg.UseFinalizedTag {
		t.Error("the finalized tag should be preferred by default")
	}
	if cfg.Confirmations != DefaultConfirmations || cfg.ReorgDepth != DefaultReorgDepth {
		t.Errorf("confirmations = %d, reorg_depth = %d", cfg.Confirmations, cfg.ReorgDepth)
	}
}

// A service with no endpoint cannot watch anything, and the error should say
// what to put there during development.
func TestLoadConfigRequiresAnEndpoint(t *testing.T) {
	for name, contents := range map[string]string{
		"section absent": `{}`,
		"empty list":     `{"bsc": {"rpc_urls": []}}`,
		"blank entry":    `{"bsc": {"rpc_urls": ["  "]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadFrom(t, contents)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), "8545") {
				t.Errorf("the message should suggest the local node: %v", err)
			}
		})
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, contents := range map[string]string{
		"not a URL":            `{"bsc": {"rpc_urls": ["localhost:8545"]}}`,
		"zero log_range":       `{"bsc": {"rpc_urls": ["http://x"], "log_range": 0}}`,
		"negative confirms":    `{"bsc": {"rpc_urls": ["http://x"], "confirmations": -1}}`,
		"negative reorg depth": `{"bsc": {"rpc_urls": ["http://x"], "reorg_depth": -1}}`,
		"zero timeout":         `{"bsc": {"rpc_urls": ["http://x"], "timeout": "0s"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFrom(t, contents); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestQuantityRoundTrip(t *testing.T) {
	for _, n := range []uint64{0, 1, 15, 16, 255, 0x6e7a141} {
		got, err := parseQuantity(quantity(n))
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if got != n {
			t.Fatalf("%d round-tripped to %d", n, got)
		}
	}
	// No leading zeros, per the RPC spec.
	if q := quantity(16); q != "0x10" {
		t.Errorf("quantity(16) = %q", q)
	}
}
