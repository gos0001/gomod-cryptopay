package cryptopay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "test-key-0123456789abcdef0123"

// stub runs a client against a handler and records what the last request looked
// like.
type stub struct {
	method string
	path   string
	// rawURI is the request line as it went over the wire, before the server
	// decoded any escapes. Escaping can only be checked here: r.URL.Path turns
	// %2F back into a slash.
	rawURI string
	query  string
	header http.Header
	body   []byte
}

func serve(t *testing.T, handler http.HandlerFunc) (*Client, *stub) {
	t.Helper()

	got := &stub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		got.rawURI = r.RequestURI
		got.header = r.Header.Clone()
		got.body, _ = readAll(r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, testKey), got
}

func readAll(r *http.Request) ([]byte, error) {
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

// data writes a success envelope.
func data(w http.ResponseWriter, status int, payload string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"data":%s}`, payload)
}

// fail writes the service's error envelope.
func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, msg)
}

const invoiceJSON = `{"invoice":{
  "id":"9f1c0000-0000-0000-0000-000000000001","external_id":"order-42",
  "network":"tron","symbol":"USDT","contract_address":"TR7N","decimals":6,
  "pay_address":"TWd4","pay_amount":"10.5001","pay_amount_units":"10500100",
  "amount":"10.5","status":"pending","confirmations":0,
  "created_at":"2026-08-15T12:00:00Z","expires_at":"2026-08-15T12:30:00Z"}}`

func TestCreateInvoiceSendsKeyAndBody(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusCreated, invoiceJSON)
	})

	inv, created, err := c.CreateInvoice(context.Background(), CreateInvoiceRequest{
		Network: NetworkTron, Symbol: "USDT", Amount: "10.50", ExternalID: "order-42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !created {
		t.Error("201 means a fresh invoice")
	}
	if got.header.Get(HeaderAPIKey) != testKey {
		t.Errorf("api key header = %q", got.header.Get(HeaderAPIKey))
	}
	if got.method != http.MethodPost || got.path != "/api/v1/invoices" {
		t.Errorf("called %s %s", got.method, got.path)
	}
	if !strings.Contains(string(got.body), `"external_id":"order-42"`) {
		t.Errorf("body = %s", got.body)
	}
	if inv.PayAmount != "10.5001" || inv.PayAmountUnits != "10500100" {
		t.Errorf("got %+v", inv)
	}
}

// 200 rather than 201 is how the service reports a repeated external_id. Losing
// that distinction would hide a double-submit from the caller.
func TestCreateInvoiceReportsAReplay(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusOK, invoiceJSON)
	})

	_, created, err := c.CreateInvoice(context.Background(), CreateInvoiceRequest{
		Network: NetworkTron, Symbol: "USDT", Amount: "10.50", ExternalID: "order-42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Fatal("200 means the invoice already existed")
	}
}

// Omitted optional fields must not reach the service as empty strings: an empty
// external_id is not the same request as no external_id.
func TestCreateInvoiceOmitsEmptyFields(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusCreated, invoiceJSON)
	})

	_, _, err := c.CreateInvoice(context.Background(), CreateInvoiceRequest{
		Network: NetworkTron, Symbol: "USDT", Amount: "1",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"external_id", "expires_in", "description", "metadata", "contract_address"} {
		if strings.Contains(string(got.body), field) {
			t.Errorf("body carries an empty %s: %s", field, got.body)
		}
	}
}

func TestStatusesMapToSentinels(t *testing.T) {
	tests := map[int]error{
		http.StatusBadRequest:          ErrInvalidInput,
		http.StatusUnauthorized:        ErrUnauthorized,
		http.StatusForbidden:           ErrForbidden,
		http.StatusNotFound:            ErrNotFound,
		http.StatusConflict:            ErrConflict,
		http.StatusTooManyRequests:     ErrRateLimited,
		http.StatusInternalServerError: ErrServer,
		http.StatusBadGateway:          ErrServer,
	}

	for status, sentinel := range tests {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				fail(w, status, "the service said no")
			})

			_, err := c.GetInvoice(context.Background(), "9f1c")
			if !errors.Is(err, sentinel) {
				t.Fatalf("got %v, want it to match %v", err, sentinel)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatal("want an *APIError")
			}
			if apiErr.Message != "the service said no" {
				t.Errorf("message = %q", apiErr.Message)
			}
			if !strings.Contains(apiErr.Endpoint, "/api/v1/invoices/") {
				t.Errorf("endpoint = %q, should name the route", apiErr.Endpoint)
			}
		})
	}
}

// The answer may come from a proxy rather than the service, and those are
// sometimes enormous HTML pages.
func TestErrorBodyIsBounded(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
	})

	_, err := c.GetInvoice(context.Background(), "9f1c")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v", err)
	}
	if len(apiErr.Error()) > maxErrorBody {
		t.Fatalf("error message is %d bytes; the body was not bounded", len(apiErr.Error()))
	}
}

// A transport failure is not an APIError: errors.Is against the context reason
// has to keep working, or a caller cannot tell "cancelled" from "rejected".
func TestContextCancellationSurvives(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		data(w, http.StatusOK, invoiceJSON)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.GetInvoice(ctx, "9f1c")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a deadline error", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Error("a transport failure must not be reported as an API error")
	}
}

func TestFilterReachesTheQueryString(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusOK, `{"invoices":[]}`)
	})

	from := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	_, err := c.ListInvoices(context.Background(), InvoiceFilter{
		Status: StatusPending, Network: NetworkBSC, AssetID: 7,
		ExternalID: "order-42", CreatedFrom: from, Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"status=pending", "network=bsc", "asset_id=7",
		"external_id=order-42", "limit=25", "created_from=2026-08-01T10%3A00%3A00Z",
	} {
		if !strings.Contains(got.query, want) {
			t.Errorf("query %q is missing %s", got.query, want)
		}
	}
}

// An unset filter must send no parameters at all: "status=" is a filter on the
// empty status, not the absence of one.
func TestEmptyFilterSendsNoQuery(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusOK, `{"invoices":[]}`)
	})

	if _, err := c.ListInvoices(context.Background(), InvoiceFilter{}); err != nil {
		t.Fatal(err)
	}
	if got.query != "" {
		t.Fatalf("query = %q, want none", got.query)
	}
}

func TestAllInvoicesWalksEveryPage(t *testing.T) {
	pages := []string{
		`{"invoices":[{"id":"a"},{"id":"b"}],"next_cursor":"c1"}`,
		`{"invoices":[{"id":"c"}],"next_cursor":"c2"}`,
		`{"invoices":[{"id":"d"}]}`,
	}
	var cursors []string

	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			data(w, http.StatusOK, pages[0])
		case "c1":
			data(w, http.StatusOK, pages[1])
		default:
			data(w, http.StatusOK, pages[2])
		}
	})

	var ids []string
	for inv, err := range c.AllInvoices(context.Background(), InvoiceFilter{}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ids = append(ids, inv.ID)
	}

	if strings.Join(ids, ",") != "a,b,c,d" {
		t.Fatalf("ids = %v", ids)
	}
	if strings.Join(cursors, ",") != ",c1,c2" {
		t.Fatalf("cursors sent = %v", cursors)
	}
}

// break must stop the walk, not just the loop: otherwise the caller pays for
// every remaining page.
func TestAllInvoicesStopsOnBreak(t *testing.T) {
	requests := 0
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		data(w, http.StatusOK, `{"invoices":[{"id":"a"}],"next_cursor":"more"}`)
	})

	for range c.AllInvoices(context.Background(), InvoiceFilter{}) {
		break
	}

	if requests != 1 {
		t.Fatalf("made %d requests after a break", requests)
	}
}

// A service that keeps returning a cursor with no rows would otherwise spin
// forever.
func TestAllInvoicesStopsOnAnEmptyPage(t *testing.T) {
	requests := 0
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		data(w, http.StatusOK, `{"invoices":[],"next_cursor":"always"}`)
	})

	for range c.AllInvoices(context.Background(), InvoiceFilter{}) {
		t.Fatal("no invoices should have been yielded")
	}

	if requests != 1 {
		t.Fatalf("made %d requests; an empty page must end the walk", requests)
	}
}

func TestAllInvoicesYieldsTheError(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fail(w, http.StatusUnauthorized, "invalid or missing X-Api-Key")
	})

	var seen error
	for _, err := range c.AllInvoices(context.Background(), InvoiceFilter{}) {
		seen = err
	}

	if !errors.Is(seen, ErrUnauthorized) {
		t.Fatalf("got %v", seen)
	}
}

func TestCancelInvoice(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusOK, invoiceJSON)
	})

	if _, err := c.CancelInvoice(context.Background(), "9f1c"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/api/v1/invoices/9f1c/cancel" {
		t.Fatalf("called %s %s", got.method, got.path)
	}
}

// An id is user input by the time it reaches here, and a slash in it must not
// become a different route.
func TestInvoiceIDIsEscaped(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusOK, invoiceJSON)
	})

	_, _ = c.GetInvoice(context.Background(), "../orphans")

	// The slash must travel escaped, so the router matches /invoices/:id with a
	// silly id rather than routing somewhere else entirely.
	if !strings.Contains(got.rawURI, "%2F") {
		t.Fatalf("request line = %q; the id escaped its path segment", got.rawURI)
	}
}

func TestListAssetsAndOrphans(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/assets") {
			data(w, http.StatusOK, `{"assets":[{"network":"tron","symbol":"USDT","decimals":6,"step":"100","nonce_max":1000}]}`)
			return
		}
		data(w, http.StatusOK, `{"orphans":[{"network":"tron","tx_hash":"0xabc","amount_units":"9999"}]}`)
	})

	assets, err := c.ListAssets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step, ok := assets[0].StepBig()
	if !ok || step.Int64() != 100 {
		t.Errorf("step = %v", assets[0].Step)
	}

	orphans, err := c.ListOrphans(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	amount, ok := orphans[0].AmountBig()
	if !ok || amount.Int64() != 9999 {
		t.Errorf("amount = %v", orphans[0].Amount)
	}
	if got.query != "limit=10" {
		t.Errorf("query = %q", got.query)
	}
}

// /healthz takes no key, and sending one puts the credential in the log of every
// load balancer that probes it.
func TestHealthSendsNoKey(t *testing.T) {
	c, got := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusOK, `{"status":"ok","database":"ok"}`)
	})

	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !h.Healthy() {
		t.Error("want healthy")
	}
	if got.header.Get(HeaderAPIKey) != "" {
		t.Error("the key must not be sent to /healthz")
	}
}

// 503 carries the reason in the same envelope as 200. Reporting only the status
// would throw away the one useful thing the call can tell you.
func TestUnhealthyReturnsBothReasonAndError(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		data(w, http.StatusServiceUnavailable, `{"status":"unavailable","database":"unreachable"}`)
	})

	h, err := c.Health(context.Background())

	if !errors.Is(err, ErrServer) {
		t.Fatalf("got %v, want ErrServer", err)
	}
	if h.Database != "unreachable" {
		t.Fatalf("the reason was lost: %+v", h)
	}
}

func TestAmountsAreNeverFloats(t *testing.T) {
	// 18 decimals: the figure below is not representable in a float64, and a
	// client that round-tripped it through one would produce an amount outside
	// the credit window.
	const units = "10500100000000000001"

	var inv Invoice
	if err := json.Unmarshal([]byte(`{"pay_amount_units":"`+units+`"}`), &inv); err != nil {
		t.Fatal(err)
	}

	got, ok := inv.PayAmountUnitsBig()
	if !ok || got.String() != units {
		t.Fatalf("got %v, want %s", got, units)
	}
}

func TestUserAgentAndCustomHTTPClient(t *testing.T) {
	got := &stub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		data(w, http.StatusOK, `{"assets":[]}`)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL+"/", testKey, // trailing slash must not double up
		WithUserAgent("shop/1.4"),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))

	if _, err := c.ListAssets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("User-Agent") != "shop/1.4" {
		t.Errorf("user agent = %q", got.header.Get("User-Agent"))
	}
}
