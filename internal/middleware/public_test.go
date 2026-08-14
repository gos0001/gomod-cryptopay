package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const testKey = "secret-key-0123456789abcdef"

// publicRouter mirrors the real wiring of POST /api/v1/invoices: one route,
// admitted either by a key or, when configured, anonymously.
//
// The handler reports whether the request was marked public, so a test can tell
// the two admissions apart.
func publicRouter(public PublicConfig) (*gin.Engine, *Middleware) {
	mw := New(Config{Keys: []string{testKey}}, CORSConfig{}, public)

	r := gin.New()
	r.POST("/invoices", mw.APIKeyOrPublic(), func(c *gin.Context) {
		if IsPublic(c) {
			c.Status(http.StatusAccepted)
			return
		}
		c.Status(http.StatusTeapot)
	})
	return r, mw
}

func post(r *gin.Engine, key, forwardedFor string) int {
	req := httptest.NewRequest(http.MethodPost, "/invoices", nil)
	if key != "" {
		req.Header.Set(HeaderAPIKey, key)
	}
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func openPublic() PublicConfig {
	return PublicConfig{InvoiceCreate: true, RatePerMinute: 600, Burst: 100}
}

// The default. Pulling a newer image must not open invoice creation.
func TestPublicCreationIsClosedByDefault(t *testing.T) {
	r, _ := publicRouter(PublicConfig{RatePerMinute: 60, Burst: 10})

	if got := post(r, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

func TestPublicCreationAdmitsAnonymousWhenEnabled(t *testing.T) {
	r, _ := publicRouter(openPublic())

	if got := post(r, "", ""); got != http.StatusAccepted {
		t.Fatalf("status = %d, want the handler to see a public request", got)
	}
}

// The keyed path must behave exactly as it did before the public path existed —
// including not being marked public, which is what preserves external_id and
// metadata for a backend.
func TestKeyedRequestIsNotMarkedPublic(t *testing.T) {
	r, _ := publicRouter(openPublic())

	if got := post(r, testKey, ""); got != http.StatusTeapot {
		t.Fatalf("status = %d, want the keyed branch", got)
	}
}

// A typo in a backend's key must fail loudly. Falling through to the public path
// would silently drop that backend's external_id and metadata instead.
func TestWrongKeyIsRejectedEvenWithPublicEnabled(t *testing.T) {
	r, _ := publicRouter(openPublic())

	if got := post(r, "wrong-key-0123456789abcdef", ""); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

func TestPublicRequestsAreRateLimited(t *testing.T) {
	r, _ := publicRouter(PublicConfig{InvoiceCreate: true, RatePerMinute: 60, Burst: 3})

	for i := 1; i <= 3; i++ {
		if got := post(r, "", ""); got != http.StatusAccepted {
			t.Fatalf("request %d: status = %d, want it inside the burst", i, got)
		}
	}
	if got := post(r, "", ""); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 past the burst", got)
	}
}

// A backend holding a key is not rate limited: it is trusted, and a legitimate
// batch of invoices must not be throttled.
func TestKeyedRequestsAreNotRateLimited(t *testing.T) {
	r, _ := publicRouter(PublicConfig{InvoiceCreate: true, RatePerMinute: 60, Burst: 1})

	for i := 1; i <= 5; i++ {
		if got := post(r, testKey, ""); got != http.StatusTeapot {
			t.Fatalf("request %d: status = %d, want the keyed branch every time", i, got)
		}
	}
}

// X-Forwarded-For is written by the client. With no trusted proxy configured, gin
// must ignore it — otherwise the limit is bypassed by varying one header, which
// is the whole attack this guards against.
func TestForgedForwardedForDoesNotEarnAFreshBucket(t *testing.T) {
	r, _ := publicRouter(PublicConfig{InvoiceCreate: true, RatePerMinute: 60, Burst: 2})
	// gin trusts every proxy by default; the real router calls SetTrustedProxies
	// from app.trusted_proxies, and this reproduces the configured-empty case.
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}

	for i := 1; i <= 2; i++ {
		if got := post(r, "", "10.0.0.1"); got != http.StatusAccepted {
			t.Fatalf("request %d: status = %d", i, got)
		}
	}

	for _, forged := range []string{"10.0.0.2", "203.0.113.7", "198.51.100.1"} {
		if got := post(r, "", forged); got != http.StatusTooManyRequests {
			t.Fatalf("X-Forwarded-For %s bought a fresh bucket: status = %d", forged, got)
		}
	}
}

func TestBucketRefills(t *testing.T) {
	l := newLimiter(60, 2) // one token per second
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("the burst should have been available")
	}
	if l.allow("a") {
		t.Fatal("the bucket should be empty")
	}

	now = now.Add(time.Second)
	if !l.allow("a") {
		t.Fatal("a second should have refilled one token")
	}
}

// Tokens accumulate only up to the burst; an address idle for an hour does not
// earn an hour's worth of requests.
func TestBucketDoesNotAccumulatePastTheBurst(t *testing.T) {
	l := newLimiter(60, 2)
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }

	l.allow("a")
	now = now.Add(time.Hour)

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("the burst should be available after idling")
	}
	if l.allow("a") {
		t.Fatal("more than the burst was granted after an hour idle")
	}
}

// Without eviction the map grows by one entry per address seen, forever.
func TestIdleBucketsAreEvicted(t *testing.T) {
	l := newLimiter(60, 1)
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }

	l.allow("old")

	now = now.Add(bucketIdleTTL + time.Minute)
	l.allow("new")

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, present := l.buckets["old"]; present {
		t.Error("an idle bucket was kept")
	}
	if _, present := l.buckets["new"]; !present {
		t.Error("the active bucket was evicted")
	}
}

func TestLoadPublicConfigRejectsBadValues(t *testing.T) {
	for name, contents := range map[string]string{
		"zero rate":     `{"public_api": {"rate_per_minute": 0}}`,
		"negative rate": `{"public_api": {"rate_per_minute": -1}}`,
		"zero burst":    `{"public_api": {"burst": 0}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPublicConfig(loadFile(t, contents)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestLoadPublicConfigDefaults(t *testing.T) {
	cfg, err := LoadPublicConfig(loadFile(t, `{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InvoiceCreate {
		t.Error("public invoice creation must default to off")
	}
	if cfg.RatePerMinute != 30 || cfg.Burst != 10 {
		t.Errorf("got rate %v burst %d", cfg.RatePerMinute, cfg.Burst)
	}
}
