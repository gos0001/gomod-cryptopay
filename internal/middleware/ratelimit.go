package middleware

import (
	"sync"
	"time"
)

// bucketIdleTTL is how long an unused bucket is kept. Without eviction the map
// grows by one entry per distinct address seen, forever.
//
// sweepInterval bounds how often eviction runs.
const (
	bucketIdleTTL = 10 * time.Minute
	sweepInterval = time.Minute
)

// limiter is a token bucket per client address.
//
// Hand-rolled rather than golang.org/x/time/rate because the eviction sweep is
// the part that matters here, and wrapping that around rate.Limiter would be more
// code than the bucket itself.
type limiter struct {
	// ratePerSecond and burst come from PublicConfig; a bucket refills at
	// ratePerSecond and never holds more than burst tokens.
	ratePerSecond float64
	burst         float64

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time

	// now is injectable so the tests can advance time instead of sleeping.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(ratePerMinute float64, burst int) *limiter {
	return &limiter{
		ratePerSecond: ratePerMinute / 60,
		burst:         float64(burst),
		buckets:       make(map[string]*bucket),
		now:           time.Now,
	}
}

// allow takes one token for key, reporting whether there was one to take.
func (l *limiter) allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		// A first request must not be free of accounting, or a client that never
		// repeats an address would bypass the limit entirely.
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		l.sweep(now)
		return true
	}

	b.tokens += now.Sub(b.last).Seconds() * l.ratePerSecond
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets nobody has touched recently. Called from allow while the
// lock is held, so there is no background goroutine to start, stop, or leak.
//
// Rate-limited to once per sweepInterval because the scan is O(buckets): doing it
// on every new address would make a flood from many addresses — exactly the case
// this protects against — quadratic.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now

	for key, b := range l.buckets {
		if now.Sub(b.last) > bucketIdleTTL {
			delete(l.buckets, key)
		}
	}
}
