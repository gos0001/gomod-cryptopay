package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

func init() { gin.SetMode(gin.TestMode) }

// serve runs one request through a router guarded by the given keys and returns
// the status code.
func serve(t *testing.T, keys []string, header string) int {
	t.Helper()

	mw := New(Config{Keys: keys}, CORSConfig{}, PublicConfig{RatePerMinute: 60, Burst: 10})

	r := gin.New()
	r.GET("/guarded", mw.APIKey(), func(c *gin.Context) { c.Status(http.StatusTeapot) })

	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	if header != "" {
		req.Header.Set(HeaderAPIKey, header)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestAPIKeyAllowsConfiguredKey(t *testing.T) {
	if got := serve(t, []string{"secret-one"}, "secret-one"); got != http.StatusTeapot {
		t.Fatalf("want handler to run (418), got %d", got)
	}
}

func TestAPIKeyAllowsAnyKeyDuringRotation(t *testing.T) {
	keys := []string{"old-key", "new-key"}
	for _, k := range keys {
		if got := serve(t, keys, k); got != http.StatusTeapot {
			t.Fatalf("key %q rejected during rotation: got %d", k, got)
		}
	}
}

func TestAPIKeyRejectsWrongKey(t *testing.T) {
	if got := serve(t, []string{"secret-one"}, "secret-two"); got != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", got)
	}
}

func TestAPIKeyRejectsMissingHeader(t *testing.T) {
	if got := serve(t, []string{"secret-one"}, ""); got != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", got)
	}
}

// A prefix of a valid key must not pass. subtle.ConstantTimeCompare returns 0
// on a length mismatch, but that is worth pinning: an implementation that
// switched to strings.HasPrefix would still pass every other test here.
func TestAPIKeyRejectsPrefixOfValidKey(t *testing.T) {
	if got := serve(t, []string{"secret-one"}, "secret"); got != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", got)
	}
}

// An empty configured key must not turn the header's absence into a pass.
// LoadConfig refuses this, but New must not depend on that having run.
func TestAPIKeyRejectsEmptyHeaderAgainstEmptyKey(t *testing.T) {
	if got := serve(t, []string{""}, ""); got == http.StatusTeapot {
		t.Fatal("an empty key must not admit an empty header")
	}
}

// loadFile writes contents to a temp config file and opens it. Shared with the
// cors and public_api tests, which load their own sections from it.
func loadFile(t *testing.T, contents string) *config.File {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	file, err := config.Load(config.Path(path))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return file
}

// loadFrom loads the api section out of contents.
func loadFrom(t *testing.T, contents string) (Config, error) {
	t.Helper()
	return LoadConfig(loadFile(t, contents))
}

func TestLoadConfigRejectsShortKey(t *testing.T) {
	if _, err := loadFrom(t, `{"api": {"keys": ["tooshort"]}}`); err == nil {
		t.Fatal("want an error for a key below the length floor")
	}
}

// The whole point of a list: both keys work at once, so a rotation has no
// window where neither does.
func TestLoadConfigAcceptsSeveralKeys(t *testing.T) {
	a := "0123456789abcdef0123456789abcdef"
	b := "fedcba9876543210fedcba9876543210"

	cfg, err := loadFrom(t, `{"api": {"keys": ["`+a+`", "`+b+`"]}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Keys) != 2 || cfg.Keys[0] != a || cfg.Keys[1] != b {
		t.Fatalf("got %v", cfg.Keys)
	}
}

// No section and an empty list must both fail. A payment service coming up with
// no keys would be open to the internet.
func TestLoadConfigRequiresKeys(t *testing.T) {
	for name, contents := range map[string]string{
		"section absent": `{}`,
		"empty list":     `{"api": {"keys": []}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFrom(t, contents); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
