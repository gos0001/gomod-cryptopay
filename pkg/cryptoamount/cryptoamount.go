// Package cryptoamount converts between a token's smallest units and the
// decimal string a human reads.
//
// Every amount in this service is a big.Int of smallest units. Nothing is ever
// a float: 0.1 has no binary representation, and a token transfer that is one
// unit off does not get credited. The conversion happens exactly twice per
// value — once at the API boundary on the way in, once on the way out.
//
// Zero domain imports; this is arithmetic, not policy.
package cryptoamount

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// MaxDecimals bounds what this package will scale by. Well above the 18 of an
// ERC20 and the 6 of a TRC20 stablecoin, low enough that a hostile or
// mis-typed configuration cannot ask for a 10^100000 multiplier.
const MaxDecimals = 36

var (
	ErrEmpty           = errors.New("cryptoamount: empty string")
	ErrMalformed       = errors.New("cryptoamount: not a decimal number")
	ErrNegative        = errors.New("cryptoamount: negative amount")
	ErrTooPrecise      = errors.New("cryptoamount: more fractional digits than the token has")
	ErrDecimalsInvalid = errors.New("cryptoamount: decimals out of range")
)

// pow10 returns 10^n as a big.Int.
func pow10(n int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// Parse converts a decimal string such as "10.5" into smallest units for a
// token with the given number of decimals.
//
// It rejects rather than rounds when the input carries more precision than the
// token has. "1.0000001" of a 6-decimal token is not 1.000000 — it is a caller
// who believes they are asking for something this chain cannot express, and
// silently truncating would make the invoice they get back differ from the one
// they asked for.
func Parse(s string, decimals int32) (*big.Int, error) {
	if decimals < 0 || decimals > MaxDecimals {
		return nil, ErrDecimalsInvalid
	}

	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrEmpty
	}

	// A leading '+' is accepted; '-' is not. Amounts here are payments, and a
	// negative one is a caller error rather than a refund.
	if strings.HasPrefix(s, "-") {
		return nil, ErrNegative
	}
	s = strings.TrimPrefix(s, "+")

	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if hasFrac && strings.Contains(fracPart, ".") {
		return nil, fmt.Errorf("%w: %q", ErrMalformed, s)
	}

	// ".5" and "5." are both accepted, but "" on both sides is not.
	if intPart == "" && fracPart == "" {
		return nil, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	if !isDigits(intPart) || !isDigits(fracPart) {
		return nil, fmt.Errorf("%w: %q", ErrMalformed, s)
	}

	// Trailing zeros carry no value, so "1.500000000" is fine for a 6-decimal
	// token even though it is written with nine places.
	significant := strings.TrimRight(fracPart, "0")
	if int32(len(significant)) > decimals {
		return nil, fmt.Errorf("%w: %q has %d significant fractional digits, the token has %d",
			ErrTooPrecise, s, len(significant), decimals)
	}

	// Right-pad the fraction to the token's scale and read the whole thing as
	// one integer. No division, so no rounding decision to get wrong.
	padded := fracPart + strings.Repeat("0", int(decimals)-min(len(fracPart), int(decimals)))
	if int32(len(padded)) > decimals {
		padded = padded[:decimals]
	}

	units, ok := new(big.Int).SetString(intPart+padded, 10)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	return units, nil
}

// Format renders smallest units as a decimal string, trimming trailing zeros
// but always keeping at least one digit on each side of the point.
//
// The output round-trips through Parse for the same decimals.
func Format(units *big.Int, decimals int32) string {
	if units == nil {
		return "0"
	}
	if decimals < 0 || decimals > MaxDecimals {
		return units.String()
	}
	if decimals == 0 {
		return units.String()
	}

	neg := units.Sign() < 0
	abs := new(big.Int).Abs(units)

	whole, frac := new(big.Int).QuoRem(abs, pow10(decimals), new(big.Int))

	fracStr := frac.String()
	if pad := int(decimals) - len(fracStr); pad > 0 {
		fracStr = strings.Repeat("0", pad) + fracStr
	}
	fracStr = strings.TrimRight(fracStr, "0")

	out := whole.String()
	if fracStr != "" {
		out += "." + fracStr
	}
	if neg {
		out = "-" + out
	}
	return out
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
