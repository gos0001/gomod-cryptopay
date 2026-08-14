// Package ratebudget enforces two independent limits on outbound requests: a
// steady rate per second, and a hard ceiling per calendar day.
//
// The two behave differently on purpose, and that difference is the whole point
// of the package:
//
//   - The per-second limit WAITS. It is rate shaping; the caller is delayed by a
//     fraction of a second and proceeds. TronGrid's ceiling is 15 requests per
//     second, and exceeding it suspends the API key for around 27 seconds — so
//     the limiter has to stay under the ceiling rather than discover where it is.
//     Burst is 1 for the same reason: a burst is exactly what triggers the
//     suspension.
//
//   - The daily quota REFUSES, immediately. There is nothing to wait for — the
//     next unit arrives at midnight UTC, and a queue of blocked callers would
//     fire all at once the moment it does. A caller that is refused skips this
//     cycle and tries on the next tick.
//
// The daily counter lives in memory. Persisting it would survive a restart, but
// pkg/ may not import storage — that would make this an adapter rather than a
// library. The exposure is small: polling every five seconds spends about a
// third of a 100k daily quota, and a process that restarts often enough to
// matter is not making many requests in between.
//
// Zero domain imports.
package ratebudget

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ErrDailyBudgetExhausted is returned once the calendar day's quota is spent.
// Retrying before midnight UTC cannot succeed.
var ErrDailyBudgetExhausted = errors.New("ratebudget: daily request budget exhausted")

type Budget struct {
	// daily is the quota per UTC day. Zero means unlimited — a missing setting
	// must not silently switch a watcher off.
	daily   int
	limiter *rate.Limiter

	// now is a field so a test can move across a day boundary. Nothing injects
	// it; the constructor sets the real clock.
	now func() time.Time

	mu    sync.Mutex
	day   string // the UTC day the counter belongs to
	spent int
}

// New builds a budget. A daily of zero means unlimited; a qps of zero or less
// means unshaped.
func New(daily int, qps float64) *Budget {
	var limiter *rate.Limiter
	if qps > 0 {
		// Burst 1: no allowance is banked, so a quiet period cannot be spent as
		// a spike later. That spike is what costs a key suspension.
		limiter = rate.NewLimiter(rate.Limit(qps), 1)
	}

	b := &Budget{daily: daily, limiter: limiter, now: time.Now}
	b.day = b.currentDay()
	return b
}

// Wait blocks until one request may be made, or reports why it may not.
//
// The daily check comes first and does not block: there is no point shaping the
// rate of a caller that has no budget left.
func (b *Budget) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := b.consume(); err != nil {
		return err
	}

	if b.limiter == nil {
		return nil
	}

	if err := b.limiter.Wait(ctx); err != nil {
		// The request will not be made, so the unit goes back. Otherwise a
		// cancelled context would quietly eat quota.
		b.refund()
		return err
	}
	return nil
}

// consume reserves one unit of the daily quota.
func (b *Budget) consume() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// The day is rolled here rather than by a background timer: there is no
	// goroutine to leak, and a budget nobody is using needs no upkeep.
	if day := b.currentDay(); day != b.day {
		b.day = day
		b.spent = 0
	}

	if b.daily > 0 && b.spent >= b.daily {
		return ErrDailyBudgetExhausted
	}

	b.spent++
	return nil
}

func (b *Budget) refund() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Only if the day has not rolled underneath us — refunding into a fresh day
	// would push the counter negative for a request charged to the previous one.
	if b.currentDay() == b.day && b.spent > 0 {
		b.spent--
	}
}

// Spent reports how many requests have been charged to the current UTC day.
func (b *Budget) Spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.currentDay() != b.day {
		return 0
	}
	return b.spent
}

// Remaining reports what is left of today's quota. An unlimited budget reports
// math.MaxInt rather than zero, so that "no budget configured" cannot be
// misread as "nothing left".
func (b *Budget) Remaining() int {
	if b.daily <= 0 {
		return maxInt
	}

	spent := b.Spent()
	if spent >= b.daily {
		return 0
	}
	return b.daily - spent
}

// Daily reports the configured quota; zero means unlimited.
func (b *Budget) Daily() int { return b.daily }

// currentDay identifies the UTC calendar day.
//
// Calendar days in UTC, not a sliding window. A sliding window would be the
// more accurate way to bound a rate, but the provider counts calendar days, and
// agreeing with the provider's accounting matters more than being right in the
// abstract.
func (b *Budget) currentDay() string {
	return b.now().UTC().Format(time.DateOnly)
}

const maxInt = int(^uint(0) >> 1)
