package cryptoamount

import (
	"errors"
	"math/big"
	"testing"
)

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad test fixture %q", s)
	}
	return v
}

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		decimals int32
		want     string
	}{
		{"whole number", "10", 6, "10000000"},
		{"one decimal place", "10.5", 6, "10500000"},
		{"full precision", "10.123456", 6, "10123456"},
		{"leading zero", "0.000001", 6, "1"},
		{"zero", "0", 6, "0"},
		{"no integer part", ".5", 6, "500000"},
		{"no fractional part", "5.", 6, "5000000"},
		{"explicit plus", "+1.5", 6, "1500000"},
		{"surrounding space", "  1.5  ", 6, "1500000"},
		{"trailing zeros beyond scale", "1.500000000", 6, "1500000"},
		{"eighteen decimals", "10.5", 18, "10500000000000000000"},
		{"one unit at eighteen decimals", "0.000000000000000001", 18, "1"},
		{"zero decimals", "42", 0, "42"},
		{"large whole number", "123456789012345", 18, "123456789012345000000000000000000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in, tc.decimals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if want := mustBig(t, tc.want); got.Cmp(want) != 0 {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// Truncating here would hand the caller an invoice for an amount they did not
// ask for, and they would never see the difference until reconciliation.
func TestParseRejectsExcessPrecision(t *testing.T) {
	if _, err := Parse("1.0000001", 6); !errors.Is(err, ErrTooPrecise) {
		t.Fatalf("want ErrTooPrecise, got %v", err)
	}
	if _, err := Parse("0.5", 0); !errors.Is(err, ErrTooPrecise) {
		t.Fatalf("want ErrTooPrecise for a 0-decimal token, got %v", err)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrEmpty},
		{"only space", "   ", ErrEmpty},
		{"negative", "-1.5", ErrNegative},
		{"letters", "abc", ErrMalformed},
		{"two points", "1.2.3", ErrMalformed},
		{"bare point", ".", ErrMalformed},
		{"scientific notation", "1e6", ErrMalformed},
		{"thousands separator", "1,000", ErrMalformed},
		{"hex", "0x10", ErrMalformed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.in, 6); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseRejectsOutOfRangeDecimals(t *testing.T) {
	if _, err := Parse("1", -1); !errors.Is(err, ErrDecimalsInvalid) {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse("1", MaxDecimals+1); !errors.Is(err, ErrDecimalsInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name     string
		units    string
		decimals int32
		want     string
	}{
		{"whole number", "10000000", 6, "10"},
		{"one decimal place", "10500000", 6, "10.5"},
		{"full precision", "10123456", 6, "10.123456"},
		{"one smallest unit", "1", 6, "0.000001"},
		{"zero", "0", 6, "0"},
		{"zero decimals", "42", 0, "42"},
		{"eighteen decimals", "10500000000000000000", 18, "10.5"},
		{"one unit at eighteen decimals", "1", 18, "0.000000000000000001"},
		{"beyond int64", "123456789012345678901234567890", 18, "123456789012.34567890123456789"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Format(mustBig(t, tc.units), tc.decimals); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatNilIsZero(t *testing.T) {
	if got := Format(nil, 6); got != "0" {
		t.Fatalf("got %q", got)
	}
}

// Every amount crosses this boundary twice — in on create, out on read — so a
// value that does not survive the round trip is a value the merchant sees
// change under them.
func TestRoundTrip(t *testing.T) {
	inputs := []string{
		"0", "1", "10.5", "0.000001", "999999.123456",
		"123456789012345.678901234567890123",
	}

	for _, decimals := range []int32{0, 2, 6, 8, 18, 36} {
		for _, in := range inputs {
			units, err := Parse(in, decimals)
			if err != nil {
				continue // too precise for this scale; covered elsewhere
			}
			out := Format(units, decimals)

			again, err := Parse(out, decimals)
			if err != nil {
				t.Fatalf("Format produced %q which Parse rejects at %d decimals: %v",
					out, decimals, err)
			}
			if again.Cmp(units) != 0 {
				t.Fatalf("round trip at %d decimals: %s -> %q -> %s", decimals, units, out, again)
			}
		}
	}
}
