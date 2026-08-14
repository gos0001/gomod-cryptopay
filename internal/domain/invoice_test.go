package domain

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestPendingReachesEveryOtherStatus(t *testing.T) {
	for _, next := range []InvoiceStatus{
		InvoiceStatusDetected,
		InvoiceStatusConfirmed,
		InvoiceStatusExpired,
		InvoiceStatusCancelled,
	} {
		inv := Invoice{Status: InvoiceStatusPending}
		if err := inv.Transition(next); err != nil {
			t.Errorf("pending -> %s: %v", next, err)
		}
	}
}

// The window between "seen on chain" and "buried deep enough" is exactly where
// an expiry job would otherwise void an invoice whose money has already
// arrived.
func TestDetectedCannotExpire(t *testing.T) {
	inv := Invoice{Status: InvoiceStatusDetected}

	err := inv.Transition(InvoiceStatusExpired)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

// A reorg un-mines the transfer, and the invoice owes money again.
func TestDetectedFallsBackToPending(t *testing.T) {
	inv := Invoice{Status: InvoiceStatusDetected}

	if err := inv.Transition(InvoiceStatusPending); err != nil {
		t.Fatalf("detected -> pending must be allowed for reorgs: %v", err)
	}
}

func TestTerminalStatusesAreFinal(t *testing.T) {
	for _, from := range []InvoiceStatus{
		InvoiceStatusConfirmed, InvoiceStatusExpired, InvoiceStatusCancelled,
	} {
		if !from.IsTerminal() {
			t.Errorf("%s should report terminal", from)
		}
		for _, to := range []InvoiceStatus{
			InvoiceStatusPending, InvoiceStatusDetected, InvoiceStatusConfirmed,
			InvoiceStatusExpired, InvoiceStatusCancelled,
		} {
			if to == from {
				continue // same-status is a no-op, covered separately
			}
			inv := Invoice{Status: from}
			if err := inv.Transition(to); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s -> %s should be refused, got %v", from, to, err)
			}
		}
	}
}

// Watchers re-observe the same transfer every poll; re-applying the state they
// already wrote must not be an error.
func TestSameStatusIsANoOp(t *testing.T) {
	inv := Invoice{Status: InvoiceStatusConfirmed}

	if err := inv.Transition(InvoiceStatusConfirmed); err != nil {
		t.Fatalf("re-applying a settled status must be idempotent: %v", err)
	}
}

func TestTransitionRejectsUnknownStatus(t *testing.T) {
	inv := Invoice{Status: InvoiceStatusPending}

	if err := inv.Transition(InvoiceStatus("paid")); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestAmountMatchesWindow(t *testing.T) {
	// 10.0000 USDT at 6 decimals, with a 0.0001 step.
	pay := big.NewInt(10_000_000)
	step := big.NewInt(100)
	inv := Invoice{PayAmount: pay}

	tests := []struct {
		name  string
		value *big.Int
		want  bool
	}{
		{"exact", big.NewInt(10_000_000), true},
		{"one unit over", big.NewInt(10_000_001), true},
		{"last unit inside the window", big.NewInt(10_000_099), true},
		{"the next invoice's amount", big.NewInt(10_000_100), false},
		{"one unit short", big.NewInt(9_999_999), false},
		{"gross underpayment", big.NewInt(1), false},
		{"gross overpayment", big.NewInt(20_000_000), false},
		{"zero", big.NewInt(0), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inv.AmountMatches(tc.value, step); got != tc.want {
				t.Fatalf("value %s: got %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// A nil or non-positive step would otherwise make the window degenerate and
// match either nothing or everything, depending on the comparison order.
func TestAmountMatchesRejectsDegenerateInput(t *testing.T) {
	inv := Invoice{PayAmount: big.NewInt(100)}

	if inv.AmountMatches(big.NewInt(100), nil) {
		t.Error("nil step must not match")
	}
	if inv.AmountMatches(big.NewInt(100), big.NewInt(0)) {
		t.Error("zero step must not match")
	}
	if inv.AmountMatches(nil, big.NewInt(1)) {
		t.Error("nil value must not match")
	}
	if (Invoice{}).AmountMatches(big.NewInt(100), big.NewInt(1)) {
		t.Error("an invoice with no PayAmount must not match")
	}
}

// 18-decimal tokens overflow int64 at everyday amounts; the whole reason
// amounts are big.Int rather than a fixed-width integer.
func TestAmountMatchesAt18Decimals(t *testing.T) {
	pay, _ := new(big.Int).SetString("10000000000000000000", 10) // 10 tokens
	step, _ := new(big.Int).SetString("100000000000000", 10)     // 0.0001
	inv := Invoice{PayAmount: pay}

	inside := new(big.Int).Add(pay, big.NewInt(1))
	if !inv.AmountMatches(inside, step) {
		t.Error("a value one unit above pay must match")
	}

	outside := new(big.Int).Add(pay, step)
	if inv.AmountMatches(outside, step) {
		t.Error("the next invoice's amount must not match")
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	inv := Invoice{ExpiresAt: now}

	if inv.IsExpired(now.Add(-time.Second)) {
		t.Error("not expired before the deadline")
	}
	if inv.IsExpired(now) {
		t.Error("the deadline itself is still inside the window")
	}
	if !inv.IsExpired(now.Add(time.Second)) {
		t.Error("expired after the deadline")
	}
	if (Invoice{}).IsExpired(now) {
		t.Error("an invoice with no deadline never expires")
	}
}
