package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

const allowedOrigin = "https://shop.example.com"

// corsRouter builds a router shaped like the real one: CORS first, then the key
// guard. The ordering is what most of these tests are about.
func corsRouter(cfg CORSConfig) *gin.Engine {
	mw := New(Config{Keys: []string{"secret-key-0123456789abcdef"}}, cfg,
		PublicConfig{RatePerMinute: 60, Burst: 10})

	r := gin.New()
	r.Use(mw.CORS())
	r.POST("/api/v1/invoices", mw.APIKey(), func(c *gin.Context) { c.Status(http.StatusTeapot) })
	return r
}

func request(r *gin.Engine, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/invoices", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func enabled() CORSConfig {
	return CORSConfig{
		AllowedOrigins: []string{allowedOrigin},
		MaxAge:         config.Duration(12 * time.Hour),
	}
}

// The reason CORS is registered before the key guard: a browser sends no
// X-Api-Key on a preflight, so a 401 here would stop the real request from ever
// being made.
func TestPreflightIsAnsweredWithoutAKey(t *testing.T) {
	w := request(corsRouter(enabled()), http.MethodOptions, allowedOrigin)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("allow-origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, HeaderAPIKey) {
		t.Errorf("allow-headers = %q, must list %s or the browser strips it", got, HeaderAPIKey)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "43200" {
		t.Errorf("max-age = %q, want 43200", got)
	}
}

func TestAllowedOriginIsEchoedOnARealRequest(t *testing.T) {
	r := corsRouter(enabled())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/invoices", nil)
	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set(HeaderAPIKey, "secret-key-0123456789abcdef")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the handler to have run", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("allow-origin = %q", got)
	}
}

func TestForeignOriginGetsNoCORSHeaders(t *testing.T) {
	w := request(corsRouter(enabled()), http.MethodOptions, "https://evil.example.com")

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow-origin = %q, want none", got)
	}
	// Still 204: the browser blocks on the missing header, and a 4xx here would
	// read as a server fault in the console.
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

// A cache that stored one origin's response must not hand its Allow-Origin
// header to the next origin.
func TestVaryOriginIsAlwaysSet(t *testing.T) {
	for _, origin := range []string{allowedOrigin, "https://evil.example.com"} {
		w := request(corsRouter(enabled()), http.MethodOptions, origin)
		if got := w.Header().Get("Vary"); !strings.Contains(got, "Origin") {
			t.Errorf("origin %s: Vary = %q", origin, got)
		}
	}
}

// This API authenticates with a header, never a cookie. Allowing credentials
// would let a hostile page act with a visitor's ambient session.
func TestCredentialsAreNeverAllowed(t *testing.T) {
	for _, cfg := range []CORSConfig{enabled(), {AllowAllOrigins: true}} {
		for _, method := range []string{http.MethodOptions, http.MethodPost} {
			w := request(corsRouter(cfg), method, allowedOrigin)
			if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("allow-credentials = %q, must never be set", got)
			}
		}
	}
}

// The default: a deployment that never talks to a browser looks untouched on the
// wire.
func TestDisabledCORSAddsNothing(t *testing.T) {
	w := request(corsRouter(CORSConfig{}), http.MethodOptions, allowedOrigin)

	for _, h := range []string{"Access-Control-Allow-Origin", "Vary", "Access-Control-Max-Age"} {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want none", h, got)
		}
	}
	// With no CORS handling the preflight falls through to the key guard.
	if w.Code == http.StatusNoContent {
		t.Error("preflight should not have been answered by disabled CORS")
	}
}

// Wildcard mode still echoes the origin rather than answering "*", so the shape
// of the response does not depend on the mode.
func TestAllowAllOriginsEchoesTheOrigin(t *testing.T) {
	w := request(corsRouter(CORSConfig{AllowAllOrigins: true}), http.MethodOptions, "https://anything.test")

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.test" {
		t.Fatalf("allow-origin = %q", got)
	}
}

// A request with no Origin is not a browser request; it must be left exactly as
// it was before CORS existed.
func TestRequestWithoutOriginIsUntouched(t *testing.T) {
	w := request(corsRouter(enabled()), http.MethodPost, "")

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow-origin = %q, want none", got)
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want the key guard to have answered 401", w.Code)
	}
}

func TestLoadCORSConfigRejectsBadOrigins(t *testing.T) {
	tests := map[string]string{
		"trailing slash": `{"cors": {"allowed_origins": ["https://shop.example.com/"]}}`,
		"wildcard entry": `{"cors": {"allowed_origins": ["*"]}}`,
		"no scheme":      `{"cors": {"allowed_origins": ["shop.example.com"]}}`,
		"with a path":    `{"cors": {"allowed_origins": ["https://shop.example.com/checkout"]}}`,
		"negative age":   `{"cors": {"max_age": "-1s"}}`,
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadCORSFrom(t, contents); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestLoadCORSConfigDefaultsToDisabled(t *testing.T) {
	cfg, err := loadCORSFrom(t, `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled() {
		t.Error("CORS must be off unless configured")
	}
	if cfg.MaxAge.Std() != 12*time.Hour {
		t.Errorf("max_age default = %s", cfg.MaxAge)
	}
}

func loadCORSFrom(t *testing.T, contents string) (CORSConfig, error) {
	t.Helper()
	return LoadCORSConfig(loadFile(t, contents))
}
