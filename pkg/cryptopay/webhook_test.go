package cryptopay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const hookSecret = "0123456789abcdef0123456789abcdef"

const payload = `{"event":"invoice.confirmed","invoice_id":"9f1c","status":"confirmed",` +
	`"network":"tron","symbol":"USDT","pay_amount":"10500100","decimals":6}`

// sign computes the signature independently of pkg/webhook, so these tests would
// catch the signing scheme changing shape rather than following it.
func sign(t *testing.T, secret, timestamp, body string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// delivery builds a request the way the service sends one.
func delivery(t *testing.T, secret, body string, at time.Time) *http.Request {
	t.Helper()

	timestamp := strconv.FormatInt(at.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, sign(t, secret, timestamp, body))
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderEvent, EventConfirmed)
	req.Header.Set(HeaderEventID, "evt-1")
	req.Header.Set(HeaderAttempt, "2")
	return req
}

// run sends req through a handler and reports the status and what the callback
// saw.
func run(t *testing.T, req *http.Request, fn func(context.Context, Event) error, opts ...WebhookHandlerOption) int {
	t.Helper()
	w := httptest.NewRecorder()
	WebhookHandler(hookSecret, fn, opts...).ServeHTTP(w, req)
	return w.Code
}

func TestGenuineDeliveryReachesTheHandler(t *testing.T) {
	var got Event

	code := run(t, delivery(t, hookSecret, payload, time.Now()),
		func(_ context.Context, e Event) error {
			got = e
			return nil
		})

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got.Event != EventConfirmed || got.InvoiceID != "9f1c" {
		t.Errorf("payload not decoded: %+v", got)
	}
	if got.ID != "evt-1" {
		t.Errorf("event id = %q; it is what deduplication keys on", got.ID)
	}
	if got.Attempt != 2 {
		t.Errorf("attempt = %d", got.Attempt)
	}
	if string(got.Raw) != payload {
		t.Error("Raw should carry the body exactly as it arrived")
	}
	if amount, ok := got.PayAmountBig(); !ok || amount.Int64() != 10500100 {
		t.Errorf("pay amount = %q", got.PayAmount)
	}
}

// Each of these is a forgery or a replay, and each gets the same 401: telling a
// forger which check they failed is free help.
func TestRejections(t *testing.T) {
	tests := map[string]func() *http.Request{
		"no signature at all": func() *http.Request {
			req := delivery(t, hookSecret, payload, time.Now())
			req.Header.Del(HeaderSignature)
			return req
		},
		"no timestamp": func() *http.Request {
			req := delivery(t, hookSecret, payload, time.Now())
			req.Header.Del(HeaderTimestamp)
			return req
		},
		"signed with another secret": func() *http.Request {
			return delivery(t, "0000000000000000000000000000000f", payload, time.Now())
		},
		"body changed after signing": func() *http.Request {
			req := delivery(t, hookSecret, payload, time.Now())
			req.Body = httptest.NewRequest(http.MethodPost, "/hooks",
				strings.NewReader(`{"event":"invoice.confirmed","invoice_id":"OTHER"}`)).Body
			return req
		},
		"forged signature": func() *http.Request {
			req := delivery(t, hookSecret, payload, time.Now())
			req.Header.Set(HeaderSignature, "sha256=deadbeef")
			return req
		},
		"replayed an hour later": func() *http.Request {
			return delivery(t, hookSecret, payload, time.Now().Add(-time.Hour))
		},
		"timestamp far in the future": func() *http.Request {
			return delivery(t, hookSecret, payload, time.Now().Add(time.Hour))
		},
		"timestamp is not a number": func() *http.Request {
			req := delivery(t, hookSecret, payload, time.Now())
			req.Header.Set(HeaderTimestamp, "yesterday")
			return req
		},
	}

	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			called := false
			code := run(t, build(), func(context.Context, Event) error {
				called = true
				return nil
			})

			if code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			if called {
				t.Fatal("the handler ran on a delivery that did not verify")
			}
		})
	}
}

// The whole reason the timestamp is inside the signed string: a captured request
// must not stay valid forever. Without this check the signature alone would.
func TestToleranceIsWhatStopsAReplay(t *testing.T) {
	old := delivery(t, hookSecret, payload, time.Now().Add(-30*time.Minute))

	if code := run(t, old, func(context.Context, Event) error { return nil }); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want the replay rejected", code)
	}

	// Same request, accepted once the window is widened past its age — proving
	// it was the age that rejected it and not the signature.
	replay := delivery(t, hookSecret, payload, time.Now().Add(-30*time.Minute))
	code := run(t, replay, func(context.Context, Event) error { return nil }, WithTolerance(time.Hour))
	if code != http.StatusOK {
		t.Fatalf("status = %d with an hour of tolerance", code)
	}
}

// A receiver whose clock is a little behind the sender's must still work.
func TestSmallClockDriftIsTolerated(t *testing.T) {
	req := delivery(t, hookSecret, payload, time.Now().Add(30*time.Second))

	if code := run(t, req, func(context.Context, Event) error { return nil }); code != http.StatusOK {
		t.Fatalf("status = %d; half a minute of drift should be fine", code)
	}
}

// 400, not 401: the sender authenticated correctly and sent something this
// version cannot read. That is a different problem from a forgery.
func TestVerifiedButUnparseableBodyIs400(t *testing.T) {
	code := run(t, delivery(t, hookSecret, `not json`, time.Now()),
		func(context.Context, Event) error { return nil })

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

// 500 so the service retries: a database that is briefly unavailable must not
// consume the notification.
func TestHandlerErrorIs500(t *testing.T) {
	code := run(t, delivery(t, hookSecret, payload, time.Now()),
		func(context.Context, Event) error { return errors.New("database is down") })

	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	big := `{"event":"invoice.confirmed","padding":"` + strings.Repeat("x", 4096) + `"}`

	code := run(t, delivery(t, hookSecret, big, time.Now()),
		func(context.Context, Event) error { return nil },
		WithMaxBody(1024))

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the body refused", code)
	}
}

func TestOnlyPOSTIsAccepted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/hooks", nil)

	if code := run(t, req, func(context.Context, Event) error { return nil }); code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", code)
	}
}

// A receiver that rejects everything because of a mistyped secret looks exactly
// like one nobody is calling — unless the failures are reported.
func TestErrorLogSeesRejections(t *testing.T) {
	var logged []error

	run(t, delivery(t, "wrong-secret-000000000000000000", payload, time.Now()),
		func(context.Context, Event) error { return nil },
		WithErrorLog(func(err error) { logged = append(logged, err) }))

	if len(logged) != 1 || !errors.Is(logged[0], ErrBadSignature) {
		t.Fatalf("logged = %v", logged)
	}
}

func TestVerifyRequestForCustomRouters(t *testing.T) {
	req := delivery(t, hookSecret, payload, time.Now())
	body := []byte(payload)

	event, err := VerifyRequest(hookSecret, req, body, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.InvoiceID != "9f1c" {
		t.Errorf("got %+v", event)
	}

	if _, err := VerifyRequest("another-secret-00000000000000", req, body, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("got %v, want ErrBadSignature", err)
	}
}
