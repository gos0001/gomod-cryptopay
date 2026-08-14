package tron

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
	"github.com/gos0001/gomod-cryptopay/pkg/ratebudget"
)

// serve builds a client pointed at a stub. The handler receives every request so
// a test can assert on the query it produced.
func serve(t *testing.T, handler http.HandlerFunc) (*Client, *[]*http.Request) {
	t.Helper()

	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(r.Context()))
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return New(Config{
		APIURL:  srv.URL,
		APIKey:  "test-key",
		QPS:     0, // unshaped: these tests are about parsing, not pacing
		Timeout: config.Duration(5 * time.Second),
	}), &seen
}

func json200(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// Copied from a real response recorded in docs/chain-apis.md.
const feedPage = `{
  "success": true,
  "data": [
    {
      "transaction_id": "261d7e7a525fc80599791a990cb175d2fb2298bd08a165a2497143e1fbadb47c",
      "token_info": {"symbol": "USDT", "address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "decimals": 6, "name": "Tether USD"},
      "block_timestamp": 1772649612000,
      "from": "TJQQLsfYvwK1gJyET4C7hvPdJ2YyNcAUbL",
      "to": "TMuA6YqfCeX8EhbfYEg5y7S4DqzSJireY9",
      "type": "Transfer",
      "value": "10500100"
    }
  ],
  "meta": {"fingerprint": "TmGrm87pzf4zaAujPjBkuGVzsoitXnZABM", "page_size": 1}
}`

func TestTRC20TransfersParsesAPage(t *testing.T) {
	c, _ := serve(t, json200(feedPage))

	page, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "TMuA6Yq"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(page.Transfers) != 1 {
		t.Fatalf("got %d transfers", len(page.Transfers))
	}
	got := page.Transfers[0]

	if got.TxID != "261d7e7a525fc80599791a990cb175d2fb2298bd08a165a2497143e1fbadb47c" {
		t.Errorf("tx id = %q", got.TxID)
	}
	if got.Value.Cmp(big.NewInt(10_500_100)) != 0 {
		t.Errorf("value = %s", got.Value)
	}
	if got.Decimals != 6 || got.Symbol != "USDT" {
		t.Errorf("token info = %d decimals, %q", got.Decimals, got.Symbol)
	}
	if got.ContractAddress != "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" {
		t.Errorf("contract = %q", got.ContractAddress)
	}
	if want := time.UnixMilli(1772649612000).UTC(); !got.BlockTime.Equal(want) {
		t.Errorf("block time = %s, want %s", got.BlockTime, want)
	}
	if page.Fingerprint != "TmGrm87pzf4zaAujPjBkuGVzsoitXnZABM" {
		t.Errorf("fingerprint = %q", page.Fingerprint)
	}
}

// Ascending order matters: the caller advances a cursor, and newest-first would
// force the whole page to be buffered before anything could be committed.
func TestTRC20TransfersQueryShape(t *testing.T) {
	c, seen := serve(t, json200(feedPage))

	_, err := c.TRC20Transfers(context.Background(), TransfersQuery{
		Address:       "TMuA6Yq",
		MinTimestamp:  time.UnixMilli(1772649600000),
		Limit:         50,
		OnlyConfirmed: true,
		Fingerprint:   "abc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q := (*seen)[0].URL.Query()
	for key, want := range map[string]string{
		"only_to":        "true",
		"limit":          "50",
		"order_by":       "block_timestamp,asc",
		"min_timestamp":  "1772649600000",
		"only_confirmed": "true",
		"fingerprint":    "abc",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := (*seen)[0].Header.Get("TRON-PRO-API-KEY"); got != "test-key" {
		t.Errorf("api key header = %q", got)
	}
}

func TestTRC20TransfersOmitsUnsetParams(t *testing.T) {
	c, seen := serve(t, json200(feedPage))

	if _, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "TMuA6Yq"}); err != nil {
		t.Fatal(err)
	}

	q := (*seen)[0].URL.Query()
	for _, key := range []string{"min_timestamp", "only_confirmed", "fingerprint"} {
		if q.Has(key) {
			t.Errorf("%s should be absent, got %q", key, q.Get(key))
		}
	}
	if got := q.Get("limit"); got != "200" {
		t.Errorf("limit = %q, want the API maximum", got)
	}
}

// The API answers limit>200 with HTTP 400 and an empty body, so refusing
// client-side is the only way the caller learns why.
func TestTRC20TransfersRefusesOversizedLimitWithoutCallingTheAPI(t *testing.T) {
	c, seen := serve(t, json200(feedPage))

	_, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T", Limit: 201})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), "200") {
		t.Errorf("the message should name the maximum: %v", err)
	}
	if len(*seen) != 0 {
		t.Errorf("made %d requests; it should not have called the API", len(*seen))
	}
}

func TestTRC20TransfersRequiresAnAddress(t *testing.T) {
	c, _ := serve(t, json200(feedPage))

	if _, err := c.TRC20Transfers(context.Background(), TransfersQuery{}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v", err)
	}
}

// Non-Transfer TRC20 events appear in the same feed and move no money in.
func TestTRC20TransfersSkipsOtherEventTypes(t *testing.T) {
	c, _ := serve(t, json200(`{"success": true, "data": [
      {"transaction_id":"a","type":"Approval","value":"1","block_timestamp":1,"token_info":{"decimals":6}},
      {"transaction_id":"b","type":"Transfer","value":"2","block_timestamp":2,"token_info":{"decimals":6}}
    ], "meta": {}}`))

	page, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Transfers) != 1 || page.Transfers[0].TxID != "b" {
		t.Fatalf("got %+v, want only the Transfer record", page.Transfers)
	}
}

// An 18-decimal value overflows int64; it must survive as a big.Int.
func TestTRC20TransfersKeepsLargeValues(t *testing.T) {
	c, _ := serve(t, json200(`{"success": true, "data": [
      {"transaction_id":"a","type":"Transfer","value":"7250000000000000001",
       "block_timestamp":1,"token_info":{"decimals":18}}], "meta": {}}`))

	page, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"})
	if err != nil {
		t.Fatal(err)
	}

	want, _ := new(big.Int).SetString("7250000000000000001", 10)
	if page.Transfers[0].Value.Cmp(want) != 0 {
		t.Fatalf("value = %s, want %s", page.Transfers[0].Value, want)
	}
}

func TestTRC20TransfersRejectsUnparsableValue(t *testing.T) {
	c, _ := serve(t, json200(`{"success": true, "data": [
      {"transaction_id":"a","type":"Transfer","value":"not-a-number",
       "block_timestamp":1,"token_info":{"decimals":6}}], "meta": {}}`))

	if _, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"}); !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("got %v", err)
	}
}

func TestTRC20TransfersHonoursSuccessFalse(t *testing.T) {
	c, _ := serve(t, json200(`{"success": false, "error": "something went wrong"}`))

	_, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"})
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("the API's explanation should survive: %v", err)
	}
}

// Captured verbatim from a real burst against TronGrid. Capital E, no `success`.
func TestRateLimitedErrorReadsTheCapitalisedErrorKey(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"Error":"The key exceeds the frequency limit(15), and the query server is suspended for 27 s"}`))
	})

	_, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	if !strings.Contains(err.Error(), "suspended for 27 s") {
		t.Errorf("the suspension notice should survive: %v", err)
	}
}

// A 400 with no body must still produce a message that stands on its own.
func TestBadRequestWithEmptyBody(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	_, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), "empty response body") {
		t.Errorf("the message should say the body was empty: %v", err)
	}
}

func TestForbiddenPointsAtTheAPIKey(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "tron.api_key") {
		t.Errorf("the message should name the setting that fixes it: %v", err)
	}
}

const nowBlock = `{"blockID":"abc","block_header":{"raw_data":{"number":85343230,"timestamp":1786700787000}}}`

func TestNowBlockAndSolidifiedBlock(t *testing.T) {
	c, seen := serve(t, json200(nowBlock))

	head, err := c.NowBlock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if head.Number != 85343230 {
		t.Errorf("number = %d", head.Number)
	}
	if want := time.UnixMilli(1786700787000).UTC(); !head.Time.Equal(want) {
		t.Errorf("time = %s, want %s", head.Time, want)
	}

	if _, err := c.SolidifiedBlock(context.Background()); err != nil {
		t.Fatal(err)
	}

	paths := []string{(*seen)[0].URL.Path, (*seen)[1].URL.Path}
	if paths[0] != "/wallet/getnowblock" {
		t.Errorf("head path = %q", paths[0])
	}
	if paths[1] != "/walletsolidity/getnowblock" {
		t.Errorf("solidified path = %q — finality comes from the solidity endpoint", paths[1])
	}
	for i, r := range *seen {
		if r.Method != http.MethodPost {
			t.Errorf("request %d used %s; the wallet endpoints are POST", i, r.Method)
		}
	}
}

// /wallet/… has no `success` field, so an empty header is the only signal.
func TestBlockRejectsEmptyHeader(t *testing.T) {
	c, _ := serve(t, json200(`{}`))

	if _, err := c.NowBlock(context.Background()); !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("got %v", err)
	}
}

// The budget gates every call, so an exhausted quota must stop the request from
// leaving rather than be discovered by the API.
func TestCallsGoThroughTheBudget(t *testing.T) {
	c, seen := serve(t, json200(nowBlock))
	c.budget = ratebudget.New(1, 0) // one unit for the whole client

	if _, err := c.NowBlock(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := c.NowBlock(context.Background())
	if !errors.Is(err, ratebudget.ErrDailyBudgetExhausted) {
		t.Fatalf("got %v, want the budget to refuse the second call", err)
	}
	if len(*seen) != 1 {
		t.Errorf("%d requests reached the server; the refused call must not leave", len(*seen))
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
	cfg, err := loadFrom(t, `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIURL != "https://api.trongrid.io" {
		t.Errorf("api_url = %q", cfg.APIURL)
	}
	if cfg.QPS != DefaultQPS {
		t.Errorf("qps = %v, want %v", cfg.QPS, DefaultQPS)
	}
	if cfg.DailyRequestBudget != DefaultDailyBudget {
		t.Errorf("daily budget = %d", cfg.DailyRequestBudget)
	}
	if cfg.HasAPIKey() {
		t.Error("no key was configured")
	}
}

// Configuring above the ceiling has no upside: every breach costs a 27-second
// blackout.
func TestLoadConfigRefusesQPSAboveTheCeiling(t *testing.T) {
	_, err := loadFrom(t, `{"tron": {"qps": 25}}`)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "27 seconds") {
		t.Errorf("the message should explain the cost: %v", err)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, contents := range map[string]string{
		"empty api_url":   `{"tron": {"api_url": ""}}`,
		"negative qps":    `{"tron": {"qps": -1}}`,
		"negative budget": `{"tron": {"daily_request_budget": -5}}`,
		"zero timeout":    `{"tron": {"timeout": "0s"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFrom(t, contents); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// url.PathEscape on the address, so a malformed one cannot alter the path.
func TestAddressIsEscapedIntoThePath(t *testing.T) {
	c, seen := serve(t, json200(feedPage))

	if _, err := c.TRC20Transfers(context.Background(), TransfersQuery{Address: "T/../wallet"}); err != nil {
		t.Fatal(err)
	}

	got := (*seen)[0].URL.EscapedPath()
	if strings.Contains(got, "/wallet/") {
		t.Fatalf("path = %q — the address escaped its segment", got)
	}
	if _, err := url.Parse(got); err != nil {
		t.Fatalf("path is not a valid URL: %v", err)
	}
}
