package middleware

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// CORSConfig governs which browser origins may read this API's responses.
//
// Off by default. Existing deployments must not start answering browsers because
// they pulled a newer image, and a service handling payments is the wrong place
// for a permissive default.
type CORSConfig struct {
	// AllowedOrigins are matched exactly, scheme and port included:
	// "https://shop.example.com" does not cover http, a subdomain, or :8443.
	AllowedOrigins []string `json:"allowed_origins"`

	// AllowAllOrigins answers every origin. Acceptable only because this API
	// never uses cookies, so a hostile page gains nothing it could not get from
	// its own server — with credentials in play it would be a serious hole.
	AllowAllOrigins bool `json:"allow_all_origins"`

	// MaxAge is how long a browser may cache a preflight result. Every uncached
	// preflight is a second round trip before the real request.
	MaxAge config.Duration `json:"max_age"`
}

func LoadCORSConfig(f *config.File) (CORSConfig, error) {
	cfg := CORSConfig{MaxAge: config.Duration(12 * time.Hour)}
	if err := f.Section("cors", &cfg); err != nil {
		return cfg, err
	}

	origins := make([]string, 0, len(cfg.AllowedOrigins))
	for i, o := range cfg.AllowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}

		// A trailing slash is the common mistake: browsers send the origin
		// without one, so "https://shop.example.com/" would silently never
		// match. Rejected rather than trimmed, so the operator's file and the
		// running behaviour agree.
		if strings.HasSuffix(o, "/") {
			return cfg, fmt.Errorf("config: cors.allowed_origins[%d] = %q must not end in a slash; "+
				"a browser sends the origin without one", i, o)
		}
		if o == "*" {
			return cfg, errors.New("config: cors.allowed_origins does not accept \"*\"; " +
				"set cors.allow_all_origins instead, so the choice is visible")
		}

		u, err := url.Parse(o)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			return cfg, fmt.Errorf("config: cors.allowed_origins[%d] = %q must be a scheme and host, "+
				"such as \"https://shop.example.com\"", i, o)
		}

		origins = append(origins, o)
	}
	cfg.AllowedOrigins = origins

	if cfg.MaxAge.Std() < 0 {
		return cfg, errors.New("config: cors.max_age must not be negative")
	}

	return cfg, nil
}

// Enabled reports whether any origin could be allowed. When it is false the
// middleware adds no headers at all, which is what a non-browser deployment
// should look like on the wire.
func (c CORSConfig) Enabled() bool {
	return c.AllowAllOrigins || len(c.AllowedOrigins) > 0
}

// allows reports whether origin may read responses.
func (c CORSConfig) allows(origin string) bool {
	if origin == "" {
		return false
	}
	if c.AllowAllOrigins {
		return true
	}
	for _, allowed := range c.AllowedOrigins {
		// Case-sensitive on purpose: an origin is produced by the browser, not
		// typed by a user, and it is always already normalised.
		if allowed == origin {
			return true
		}
	}
	return false
}
