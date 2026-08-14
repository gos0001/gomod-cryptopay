package postgres

import (
	"math/big"
	"testing"
)

// amounts renders a set of nonces as the held-amount strings the query would
// return: ascending, base + n*step.
func amounts(base, step int64, nonces ...int64) []string {
	out := make([]string, 0, len(nonces))
	for _, n := range nonces {
		out = append(out, big.NewInt(base+n*step).String())
	}
	return out
}

func TestLowestFreeNonce(t *testing.T) {
	const base, step = 10_000_000, 100

	tests := []struct {
		name      string
		held      []string
		nonceMax  int32
		wantNonce int32
		wantOK    bool
	}{
		{
			name:      "nothing held takes the base amount",
			held:      nil,
			nonceMax:  1000,
			wantNonce: 0,
			wantOK:    true,
		},
		{
			name:      "the first gap wins",
			held:      amounts(base, step, 0, 1, 2),
			nonceMax:  1000,
			wantNonce: 3,
			wantOK:    true,
		},
		{
			name:      "a hole in the middle is reused before the tail",
			held:      amounts(base, step, 0, 1, 3, 4),
			nonceMax:  1000,
			wantNonce: 2,
			wantOK:    true,
		},
		{
			name:      "a hole at zero is reused",
			held:      amounts(base, step, 1, 2, 3),
			nonceMax:  1000,
			wantNonce: 0,
			wantOK:    true,
		},
		{
			name:     "a full space is exhausted, not wrapped",
			held:     amounts(base, step, 0, 1, 2),
			nonceMax: 3,
			wantOK:   false,
		},
		{
			name:      "the last slot is usable",
			held:      amounts(base, step, 0, 1),
			nonceMax:  3,
			wantNonce: 2,
			wantOK:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nonce, pay, ok := lowestFreeNonce(
				big.NewInt(base), big.NewInt(step), tc.nonceMax, tc.held)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if nonce != tc.wantNonce {
				t.Fatalf("nonce = %d, want %d", nonce, tc.wantNonce)
			}
			wantPay := big.NewInt(base + int64(tc.wantNonce)*step)
			if pay.Cmp(wantPay) != 0 {
				t.Fatalf("pay amount = %s, want %s", pay, wantPay)
			}
		})
	}
}

// An operator who changes an asset's step leaves invoices behind whose amounts
// sit between the new grid points. Those amounts are still genuinely held, but
// they occupy no slot in the new grid — treating them as slot 0 would make the
// allocator skip an amount that is actually free.
func TestLowestFreeNonceIgnoresOffGridAmounts(t *testing.T) {
	const base, step = 10_000_000, 100

	held := []string{
		"10000050", // half a step above base: from an older, finer step
		"10000000", // slot 0, genuinely taken
		"10000150", // between slots 1 and 2
	}

	nonce, pay, ok := lowestFreeNonce(big.NewInt(base), big.NewInt(step), 1000, held)
	if !ok {
		t.Fatal("want an allocation")
	}
	if nonce != 1 {
		t.Fatalf("nonce = %d, want 1 — only slot 0 is on-grid and taken", nonce)
	}
	if want := big.NewInt(10_000_100); pay.Cmp(want) != 0 {
		t.Fatalf("pay = %s, want %s", pay, want)
	}
}

// Amounts below the base can be returned when a previous invoice used a lower
// base; they must not shift the walk.
func TestLowestFreeNonceIgnoresAmountsBelowBase(t *testing.T) {
	held := []string{"9999999", "10000000"}

	nonce, _, ok := lowestFreeNonce(big.NewInt(10_000_000), big.NewInt(100), 10, held)
	if !ok || nonce != 1 {
		t.Fatalf("nonce = %d, ok = %v; want 1, true", nonce, ok)
	}
}

func TestLowestFreeNonceAt18Decimals(t *testing.T) {
	base, _ := new(big.Int).SetString("10000000000000000000", 10) // 10 tokens
	step, _ := new(big.Int).SetString("100000000000000", 10)      // 0.0001

	slot0 := base.String()
	slot1 := new(big.Int).Add(base, step).String()

	nonce, pay, ok := lowestFreeNonce(base, step, 1000, []string{slot0, slot1})
	if !ok {
		t.Fatal("want an allocation")
	}
	if nonce != 2 {
		t.Fatalf("nonce = %d, want 2", nonce)
	}

	want := new(big.Int).Add(base, new(big.Int).Mul(step, big.NewInt(2)))
	if pay.Cmp(want) != 0 {
		t.Fatalf("pay = %s, want %s", pay, want)
	}
}

func TestLowestFreeNonceRejectsDegenerateInput(t *testing.T) {
	b, s := big.NewInt(100), big.NewInt(10)

	if _, _, ok := lowestFreeNonce(nil, s, 10, nil); ok {
		t.Error("nil base must not allocate")
	}
	if _, _, ok := lowestFreeNonce(b, nil, 10, nil); ok {
		t.Error("nil step must not allocate")
	}
	if _, _, ok := lowestFreeNonce(b, big.NewInt(0), 10, nil); ok {
		t.Error("zero step must not allocate")
	}
	if _, _, ok := lowestFreeNonce(b, s, 0, nil); ok {
		t.Error("zero nonce_max must not allocate")
	}
}

// Garbage in the held list must not be read as slot 0 and silently push the
// allocation one slot along.
func TestLowestFreeNonceSkipsUnparsableAmounts(t *testing.T) {
	held := []string{"not-a-number", "10000000"}

	nonce, _, ok := lowestFreeNonce(big.NewInt(10_000_000), big.NewInt(100), 10, held)
	if !ok || nonce != 1 {
		t.Fatalf("nonce = %d, ok = %v; want 1, true", nonce, ok)
	}
}
