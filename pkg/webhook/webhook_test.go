package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

const secret = "0123456789abcdef0123456789abcdef"

type capture struct {
	headers http.Header
	body    []byte
}

// serve builds a sender pointed at a stub, and records what arrived.
func serve(t *testing.T, handler http.HandlerFunc) (*Sender, *capture) {
	t.Helper()

	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.headers = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return New(Config{
		URL:     srv.URL,
		Secret:  secret,
		Timeout: config.Duration(2 * time.Second),
	}), got
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestSendSetsEveryHeader(t *testing.T) {
	s, got := serve(t, ok)

	payload := []byte(`{"event":"invoice.confirmed"}`)
	if err := s.Send(context.Background(), "evt-1", "invoice.confirmed", 3, payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(got.body) != string(payload) {
		t.Errorf("body = %q", got.body)
	}
	for header, want := range map[string]string{
		HeaderEvent:    "invoice.confirmed",
		HeaderEventID:  "evt-1",
		HeaderAttempt:  "3",
		"Content-Type": "application/json",
	} {
		if h := got.headers.Get(header); h != want {
			t.Errorf("%s = %q, want %q", header, h, want)
		}
	}
	if got.headers.Get(HeaderTimestamp) == "" {
		t.Error("timestamp header is missing")
	}
	if !strings.HasPrefix(got.headers.Get(HeaderSignature), "sha256=") {
		t.Errorf("signature = %q", got.headers.Get(HeaderSignature))
	}
}

// Computed independently rather than by calling Sign, so the test would catch
// Sign changing shape.
func TestSignatureMatchesAnIndependentComputation(t *testing.T) {
	s, got := serve(t, ok)

	payload := []byte(`{"a":1}`)
	if err := s.Send(context.Background(), "evt-1", "e", 1, payload); err != nil {
		t.Fatal(err)
	}

	timestamp := got.headers.Get(HeaderTimestamp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if h := got.headers.Get(HeaderSignature); h != want {
		t.Fatalf("signature = %q, want %q", h, want)
	}
}

// The timestamp is inside the signed string precisely so a captured request
// cannot be replayed forever. If it were not, the same body would always give
// the same signature.
func TestTimestampIsPartOfTheSignedString(t *testing.T) {
	payload := []byte(`{"a":1}`)

	a := Sign(secret, "1000", payload)
	b := Sign(secret, "2000", payload)

	if a == b {
		t.Fatal("the same body signed at two times produced the same signature; " +
			"a captured request could then be replayed indefinitely")
	}
}

func TestVerify(t *testing.T) {
	payload := []byte(`{"a":1}`)
	sig := "sha256=" + Sign(secret, "1000", payload)

	if !Verify(secret, "1000", sig, payload) {
		t.Error("a genuine signature should verify")
	}
	if Verify("other-secret", "1000", sig, payload) {
		t.Error("a different secret must not verify")
	}
	if Verify(secret, "1001", sig, payload) {
		t.Error("a different timestamp must not verify")
	}
	if Verify(secret, "1000", sig, []byte(`{"a":2}`)) {
		t.Error("a different body must not verify")
	}
	if Verify(secret, "1000", "sha256=deadbeef", payload) {
		t.Error("a forged signature must not verify")
	}
	if Verify(secret, "1000", "", payload) {
		t.Error("an absent signature must not verify")
	}
}

// A receiver's rejection has to reach the operator's log, so some of the body is
// kept.
func TestNon2xxIsAnErrorCarryingTheResponse(t *testing.T) {
	s, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("unknown event type"))
	})

	err := s.Send(context.Background(), "evt-1", "e", 1, []byte(`{}`))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should carry the status: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown event type") {
		t.Errorf("error should carry the receiver's explanation: %v", err)
	}
}

func TestEvery2xxIsSuccess(t *testing.T) {
	for _, code := range []int{200, 201, 202, 204, 299} {
		s, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
		if err := s.Send(context.Background(), "evt-1", "e", 1, []byte(`{}`)); err != nil {
			t.Errorf("%d should be success: %v", code, err)
		}
	}
}

// A slow receiver must not hold up the queue behind it.
func TestSlowReceiverTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s := New(Config{URL: srv.URL, Secret: secret, Timeout: config.Duration(50 * time.Millisecond)})

	start := time.Now()
	err := s.Send(context.Background(), "evt-1", "e", 1, []byte(`{}`))
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("took %s; the timeout was not honoured", elapsed)
	}
}

// Without a secret the header is absent rather than present and empty: an empty
// signature would look like a signed request that failed to verify.
func TestNoSecretSendsNoSignatureHeader(t *testing.T) {
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.headers = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s := New(Config{URL: srv.URL, Timeout: config.Duration(time.Second)})
	if err := s.Send(context.Background(), "evt-1", "e", 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	if _, present := got.headers[http.CanonicalHeaderKey(HeaderSignature)]; present {
		t.Fatal("the signature header should be absent entirely")
	}
}

func TestAPIKeyHeaderIsSentWhenConfigured(t *testing.T) {
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.headers = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s := New(Config{URL: srv.URL, Secret: secret, APIKey: "gateway-key",
		Timeout: config.Duration(time.Second)})
	if err := s.Send(context.Background(), "evt-1", "e", 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if h := got.headers.Get(HeaderAPIKey); h != "gateway-key" {
		t.Fatalf("api key header = %q", h)
	}
}

func TestDisabledSenderRefusesToSend(t *testing.T) {
	s := New(Config{Timeout: config.Duration(time.Second)})

	if s.Enabled() {
		t.Error("no URL means disabled")
	}
	if err := s.Send(context.Background(), "evt-1", "e", 1, []byte(`{}`)); err == nil {
		t.Fatal("want an error")
	}
}

func loadFrom(t *testing.T, contents string) (Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	f, err := config.Load(config.Path(path))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return LoadConfig(f)
}

// No URL is a legitimate configuration: notifications are simply off, and
// nothing is queued.
func TestLoadConfigAllowsNoURL(t *testing.T) {
	cfg, err := loadFrom(t, `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled() {
		t.Error("no URL should mean disabled")
	}
}

// An unsigned webhook is indistinguishable from a forgery, and the receiver has
// no way to notice that signing was never switched on.
func TestLoadConfigRequiresASecretWithAURL(t *testing.T) {
	_, err := loadFrom(t, `{"webhook": {"url": "https://example.com/hooks"}}`)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("the message should name the missing setting: %v", err)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, contents := range map[string]string{
		"not a URL":    `{"webhook": {"url": "example.com", "secret": "` + secret + `"}}`,
		"short secret": `{"webhook": {"url": "https://x/h", "secret": "abc"}}`,
		"zero timeout": `{"webhook": {"url": "https://x/h", "secret": "` + secret + `", "timeout": "0s"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFrom(t, contents); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
