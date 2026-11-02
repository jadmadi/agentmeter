// SPDX-License-Identifier: AGPL-3.0-only
// Portions derived from CPython's Objects/floatobject.c (PSF-2.0).
// See THIRD_PARTY_NOTICES.md and third_party/licenses/PSF-2.0.txt.

package stats

import (
	"math"
	"math/big"
)

// CPython's float.__round__ bound checks (Objects/floatobject.c):
//
//	NDIGITS_MAX = (int)((DBL_MANT_DIG - DBL_MIN_EXP) * 0.30103)  ==  323
//	NDIGITS_MIN = -(int)((DBL_MAX_EXP + 1) * 0.30103)            == -308
//
// For ndigits > 323 CPython returns x unchanged; the bound is derived from
// DBL_MANT_DIG and DBL_MIN_EXP and matters for tiny subnormals such as
// 5e-324, which would otherwise round to zero. For ndigits < -308 every
// finite double rounds to a signed zero.
const (
	pyRoundMaxDigits = 323
	pyRoundMinDigits = -308
)

// pow10Int[n] is 10**n and pow10Rat[n] is the same value as an exact
// rational. They are immutable after package initialization, so concurrent
// readers are safe.
var (
	pow10Int [pyRoundMaxDigits + 1]*big.Int
	pow10Rat [pyRoundMaxDigits + 1]*big.Rat
)

func init() {
	pow10Int[0] = big.NewInt(1)
	pow10Rat[0] = new(big.Rat).SetInt(pow10Int[0])
	for i := 1; i <= pyRoundMaxDigits; i++ {
		pow10Int[i] = new(big.Int).Mul(pow10Int[i-1], big.NewInt(10))
		pow10Rat[i] = new(big.Rat).SetInt(pow10Int[i])
	}
}

// PyRound returns the result of CPython's round(x, ndigits) for a float.
//
// The rounding is performed on the exact binary value of x: x is scaled by
// 10**ndigits using exact rational arithmetic, rounded to the nearest integer
// with ties to even, and converted back to the nearest float64 (which is what
// CPython's correctly-rounded dtoa/strtod pair produces). The sign of a
// result that rounds to zero is preserved, matching Python's -0.0 results.
//
// Parity caveats, both impossible for report data but documented because the
// Go signature has no error return:
//
//   - CPython raises OverflowError when the rounded result is too large to
//     represent (for example round(1.5e308, -308)); PyRound returns ±Inf.
//   - Non-finite inputs are returned unchanged, as in CPython.
//
// Do not replace this with math.Round for schema values.
func PyRound(x float64, ndigits int) float64 {
	if !isFinite(x) {
		return x
	}
	if ndigits > pyRoundMaxDigits {
		return x
	}
	if ndigits < pyRoundMinDigits {
		// Python returns 0.0 * x here, which keeps x's sign.
		return math.Copysign(0, x)
	}
	if x == 0 {
		// Preserves the sign of zero.
		return x
	}

	var exact big.Rat
	exact.SetFloat64(x)

	var scaled big.Rat
	if ndigits >= 0 {
		scaled.Mul(&exact, pow10Rat[ndigits])
	} else {
		scaled.Quo(&exact, pow10Rat[-ndigits])
	}

	n := roundRatHalfEven(&scaled)

	var rounded big.Rat
	if ndigits >= 0 {
		rounded.SetFrac(n, pow10Int[ndigits])
	} else {
		rounded.SetInt(n)
		rounded.Mul(&rounded, pow10Rat[-ndigits])
	}

	out, _ := rounded.Float64()
	if out == 0 {
		// big.Rat has no signed zero; Python's dtoa produces "-0" for a
		// negative value that rounds away.
		out = math.Copysign(0, x)
	}
	return out
}

// R1 mirrors telemetry_common.r1: round(value, 1) for non-None values.
func R1(value float64) float64 {
	return PyRound(value, 1)
}

// R2 mirrors telemetry_common.r2: round(value, 2) for non-None values.
func R2(value float64) float64 {
	return PyRound(value, 2)
}

// roundRatHalfEven rounds an exact rational to the nearest integer, breaking
// ties toward the even integer, and returns the result as a new big.Int.
func roundRatHalfEven(r *big.Rat) *big.Int {
	num := r.Num()
	den := r.Denom()

	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(num, den, rem)
	if rem.Sign() == 0 {
		return q
	}

	// Compare 2*|rem| with den without using floating point.
	twice := new(big.Int).Abs(rem)
	twice.Lsh(twice, 1)
	cmp := twice.Cmp(den)
	if cmp < 0 {
		return q
	}
	if cmp == 0 && q.Bit(0) == 0 {
		return q
	}

	one := big.NewInt(1)
	if rem.Sign() < 0 {
		q.Sub(q, one)
	} else {
		q.Add(q, one)
	}
	return q
}
