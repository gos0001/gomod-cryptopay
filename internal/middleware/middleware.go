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

// ContextPublic marks a request that was admitted without an API key. Read by
// invoice_create, which then refuses the fields an anonymous caller has no
// business setting.
const ContextPublic = "cryptopay_public_request"

type Middleware struct {
	keys    [][]byte
	cors    CORSConfig
	public  PublicConfig
	limiter *limiter
}

func New(cfg Config, cors CORSConfig, public PublicConfig) *Middleware {
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
	return &Middleware{
		keys:    keys,
		cors:    cors,
		public:  public,
		limiter: newLimiter(public.RatePerMinute, public.Burst),
	}
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

// APIKeyOrPublic admits a request that carries a valid key, and — when
// public_api.invoice_create is on — one that carries none.
//
// One route with two modes rather than a second public route: the keyed path
// keeps behaving exactly as it did, which is what makes this safe to add to a
// released service.
func (m *Middleware) APIKeyOrPublic() gin.HandlerFunc {
	return func(c *gin.Context) {
		if m.keyMatches(c) {
			c.Next()
			return
		}

		// A wrong key is a wrong key. Falling through to the public path would
		// turn a typo in a backend's configuration into anonymous traffic that
		// silently loses external_id and metadata.
		if c.GetHeader(HeaderAPIKey) != "" {
			httpserver.Unauthorized(c, "invalid "+HeaderAPIKey)
			c.Abort()
			return
		}

		if !m.public.InvoiceCreate {
			httpserver.Unauthorized(c, "invalid or missing "+HeaderAPIKey)
			c.Abort()
			return
		}

		// ClientIP is only as trustworthy as app.trusted_proxies makes it: with
		// no proxy configured it is the socket's peer address, which cannot be
		// forged over TCP. Left trusting every proxy — gin's default — an
		// attacker would mint a fresh bucket per request with one header.
		if !m.limiter.allow(c.ClientIP()) {
			httpserver.TooManyRequests(c, "too many invoice requests from this address")
			c.Abort()
			return
		}

		c.Set(ContextPublic, true)
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

// IsPublic reports whether the request was admitted without a key.
func IsPublic(c *gin.Context) bool {
	public, ok := c.Get(ContextPublic)
	if !ok {
		return false
	}
	is, _ := public.(bool)
	return is
}
