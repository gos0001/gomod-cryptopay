package ratebudget

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// atClock returns a budget whose clock the test controls.
func atClock(daily int, qps float64, t *time.Time) *Budget {
	b := New(daily, qps)
	b.now = func() time.Time { return *t }
	b.day = b.currentDay()
	return b
}

func TestWaitSpendsBudget(t *testing.T) {
	b := New(10, 0)

	for i := 1; i <= 3; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if b.Spent() != 3 {
		t.Errorf("spent = %d, want 3", b.Spent())
	}
	if b.Remaining() != 7 {
		t.Errorf("remaining = %d, want 7", b.Remaining())
	}
}

// Refusing rather than waiting is the design: the next unit arrives at midnight,
// and a queue of blocked callers would fire all at once when it does.
func TestExhaustionRefusesImmediately(t *testing.T) {
	b := New(2, 0)

	for i := 0; i < 2; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	start := time.Now()
	err := b.Wait(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrDailyBudgetExhausted) {
		t.Fatalf("got %v, want ErrDailyBudgetExhausted", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("took %s — it must refuse, not wait", elapsed)
	}
	if b.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0", b.Remaining())
	}
}

func TestDayBoundaryResetsTheCounter(t *testing.T) {
	now := time.Date(2026, 8, 14, 23, 59, 59, 0, time.UTC)
	b := atClock(2, 0, &now)

	for i := 0; i < 2; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := b.Wait(context.Background()); !errors.Is(err, ErrDailyBudgetExhausted) {
		t.Fatalf("want exhaustion before midnight, got %v", err)
	}

	now = now.Add(2 * time.Second) // past midnight UTC

	if got := b.Spent(); got != 0 {
		t.Errorf("spent = %d after midnight, want 0", got)
	}
	if err := b.Wait(context.Background()); err != nil {
		t.Fatalf("the new day should allow a request: %v", err)
	}
	if got := b.Spent(); got != 1 {
		t.Errorf("spent = %d, want 1", got)
	}
}

// A local clock at 00:30 is still the previous UTC day in some zones; the
// counter must follow UTC, because that is what the provider counts.
func TestDayIsUTCNotLocal(t *testing.T) {
	utcNoon := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	b := atClock(5, 0, &utcNoon)
	if err := b.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Same instant, expressed in a zone 14 hours ahead: a local-date
	// implementation would see a different calendar day and reset.
	utcNoon = utcNoon.In(time.FixedZone("Pacific/Kiritimati", 14*3600))

	if got := b.Spent(); got != 1 {
		t.Fatalf("spent = %d, want the counter to follow UTC and stay at 1", got)
	}
}

// Otherwise a caller whose context dies while waiting on the rate limiter has
// silently consumed quota for a request that was never sent.
func TestCancelledContextDoesNotSpendBudget(t *testing.T) {
	// 1 qps with burst 1: the first call passes, the second must wait ~1s.
	b := New(100, 1)
	if err := b.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	spentAfterFirst := b.Spent()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := b.Wait(ctx); err == nil {
		t.Fatal("want a context error")
	}
	if got := b.Spent(); got != spentAfterFirst {
		t.Fatalf("spent = %d, want %d — the refused call must not be charged", got, spentAfterFirst)
	}
}

func TestAlreadyCancelledContextIsRefusedBeforeSpending(t *testing.T) {
	b := New(10, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := b.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if b.Spent() != 0 {
		t.Fatalf("spent = %d, want 0", b.Spent())
	}
}

// A missing setting must not switch a watcher off, so zero means unlimited.
func TestZeroDailyMeansUnlimited(t *testing.T) {
	b := New(0, 0)

	for i := 0; i < 50; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if b.Remaining() != maxInt {
		t.Errorf("remaining = %d, want an unlimited sentinel rather than 0", b.Remaining())
	}
	if b.Daily() != 0 {
		t.Errorf("daily = %d", b.Daily())
	}
}

func TestZeroQPSIsUnshaped(t *testing.T) {
	b := New(100, 0)

	start := time.Now()
	for i := 0; i < 20; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("20 unshaped calls took %s", elapsed)
	}
}

// The per-second level shapes rather than refuses, and burst 1 means no
// allowance is banked — a quiet period cannot be spent as a spike, which is
// exactly what costs a key suspension.
func TestPerSecondLevelShapesWithoutBursting(t *testing.T) {
	b := New(100, 20) // 20 qps, burst 1

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	// Five calls at 20 qps: the first is immediate, the remaining four wait
	// 50ms each, so around 200ms. A limiter with a larger burst would let all
	// five through at once.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("5 calls at 20 qps took %s — the rate is not being shaped", elapsed)
	}
	if elapsed > 700*time.Millisecond {
		t.Fatalf("5 calls at 20 qps took %s — slower than the configured rate", elapsed)
	}
}

func TestConcurrentWaitIsSafe(t *testing.T) {
	const callers = 40
	b := New(25, 0) // fewer units than callers, so both paths are exercised

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		refused int
		other   error
	)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.Wait(context.Background())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrDailyBudgetExhausted):
				refused++
			default:
				other = err
			}
		}()
	}
	wg.Wait()

	if other != nil {
		t.Fatalf("unexpected error: %v", other)
	}
	if ok != 25 || refused != callers-25 {
		t.Fatalf("allowed=%d refused=%d, want exactly 25 allowed", ok, refused)
	}
	if b.Spent() != 25 {
		t.Fatalf("spent = %d, want 25 — the counter overshot or undershot", b.Spent())
	}
}
