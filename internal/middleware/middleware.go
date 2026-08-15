// Package middleware holds gin middleware shared across routes.
//
// It lives under internal/ rather than pkg/ because it answers with this
// service's response envelope; pkg/ is for things a consumer could import.
package middleware

import (
	"crypto/subtle"

	"github.com/gin-gonic/gin"

	httpserver "github.com/gos0001/gomod-cryptopay/pkg/http_server"
)

// HeaderAPIKey is the header every authenticated call must carry.
const HeaderAPIKey = "X-Api-Key"

type Middleware struct {
	keys [][]byte
}

func New(cfg Config) *Middleware {
	keys := make([][]byte, 0, len(cfg.Keys))
	for _, k := range cfg.Keys {
		// An empty key would compare equal to an absent header and open every
		// route. LoadConfig already refuses one, but this constructor must not
		// rely on having been reached through it.
		if k == "" {
			continue
		}
		keys = append(keys, []byte(k))
	}
	return &Middleware{keys: keys}
}

// APIKey rejects any request that does not present a configured key.
//
// The answer is 401 rather than goauth's 404: this is an integration surface,
// and a merchant whose key is wrong needs to be told so. Hiding the route only
// makes sense for an admin plane nobody is supposed to find.
func (m *Middleware) APIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.keyMatches(c) {
			httpserver.Unauthorized(c, "invalid or missing "+HeaderAPIKey)
			c.Abort()
			return
		}

		c.Next()
	}
}

// keyMatches reports whether the request presents a configured key.
func (m *Middleware) keyMatches(c *gin.Context) bool {
	presented := []byte(c.GetHeader(HeaderAPIKey))

	// Every configured key is compared, and the loop is not short-circuited on a
	// match, so the time taken does not depend on which key matched or on how far
	// into the header the first differing byte is.
	var ok int
	for _, key := range m.keys {
		ok |= subtle.ConstantTimeCompare(presented, key)
	}
	return ok == 1
}
