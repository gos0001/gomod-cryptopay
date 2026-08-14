package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// allowedMethods and allowedHeaders are what this API actually uses. Listing
// more would advertise capability the router does not have.
var (
	allowedMethods = strings.Join([]string{http.MethodGet, http.MethodPost, http.MethodOptions}, ", ")
	allowedHeaders = strings.Join([]string{"Content-Type", HeaderAPIKey}, ", ")
)

// CORS answers cross-origin requests from the configured origins.
//
// It must be registered before the API key guard: a browser sends no
// X-Api-Key on a preflight, so an OPTIONS reaching the guard would be answered
// 401 and the real request would never follow. That ordering is a requirement,
// not a preference.
func (m *Middleware) CORS() gin.HandlerFunc {
	cfg := m.cors
	maxAge := strconv.Itoa(int(cfg.MaxAge.Std().Seconds()))

	return func(c *gin.Context) {
		if !cfg.Enabled() {
			c.Next()
			return
		}

		origin := c.GetHeader("Origin")

		// Vary is set for every request that could have carried an Origin, not
		// only for the allowed ones: a cache that stored one origin's response
		// would otherwise serve its Allow-Origin header to another.
		c.Writer.Header().Add("Vary", "Origin")

		if !cfg.allows(origin) {
			// No CORS headers. A preflight from an origin that is not allowed is
			// still answered 204 — the browser blocks the request on the missing
			// headers, and 403 here would only look like a server fault in the
			// console.
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
			c.Next()
			return
		}

		// The origin is echoed rather than answered with "*" even when every
		// origin is allowed, so the response is identical in shape either way and
		// a later change to credentials cannot silently become invalid.
		c.Writer.Header().Set("Access-Control-Allow-Origin", origin)

		// Access-Control-Allow-Credentials is deliberately never set. This API
		// authenticates with a header, never a cookie; allowing credentials would
		// let a hostile page make authenticated requests with a visitor's
		// ambient session and would forbid the wildcard origin outright.

		if c.Request.Method == http.MethodOptions {
			c.Writer.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			c.Writer.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			c.Writer.Header().Set("Access-Control-Max-Age", maxAge)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
