// SPDX-License-Identifier: AGPL-3.0-only

// Package stats implements the numeric primitives shared by the AgentMeter
// Go builders. Every function mirrors the corresponding Python reference in
// telemetry_common.py (and the CPython primitives it relies on) so a Go
// report can be compared against the Python oracle value for value.
//
// Parity rules followed here:
//
//   - Rounding for schema values goes through PyRound, never math.Round.
//   - Plain float accumulations use Sum, which matches CPython 3.12+
//     builtin sum() for floats (Neumaier compensation).
//   - Cost/spend accumulations that Python performs with math.fsum use
//     Fsum, a direct port of CPython's Shewchuk partials algorithm.
//   - Median, percentile, fits, robust z-scores and standardization keep
//     Python's operation order and its rounded return values.
//
// Where CPython raises (math.fsum overflow, round overflow, randrange(0)) the
// Go API has no error channel. Those cases are documented on each function;
// they cannot occur for the valid report inputs the builders pass in.
package stats

import "math"

// Close mirrors telemetry_common.close for non-None values: both values must
// be finite and their difference must fit the relative/absolute tolerance.
// Python's None equality branch has no Go equivalent; callers that model a
// missing value with a pointer must handle the nil case themselves.
func Close(a, b, tol float64) bool {
	if !isFinite(a) || !isFinite(b) {
		return false
	}
	return math.Abs(a-b) <= math.Max(math.Abs(tol), math.Abs(tol*b))
}

// SafeDiv mirrors telemetry_common.safe_div: a zero denominator (including
// negative zero) reports a missing value instead of dividing. NaN and
// infinite denominators are truthy in Python and divide normally.
func SafeDiv(value, denom float64) (float64, bool) {
	if denom == 0 {
		return 0, false
	}
	return value / denom, true
}

// stableSeedModulus is the modulus used by the Python stable_seed helpers
// in both telemetry builders.
const stableSeedModulus int64 = 1_000_000

// StableSeed mirrors the builders' stable_seed helper:
//
//	sum((index + 1) * ord(ch) for index, ch in enumerate(text)) % 1_000_000
//
// The index counts Unicode code points, not bytes, which matters for
// non-ASCII model and project names. text is expected to be valid UTF-8
// (Python strings always are); invalid bytes contribute U+FFFD. The returned
// seed is in [0, 999_999].
func StableSeed(text string) int64 {
	var total int64
	index := int64(0)
	for _, r := range text {
		index++
		total += index * int64(r)
	}
	return total % stableSeedModulus
}

// Sum returns the float sum of values using the same compensated algorithm
// as CPython's builtin sum() for floats (Neumaier summation, CPython 3.12+).
//
// Python's sum([int, ...]) is exact arbitrary-precision integer addition;
// this API only receives float64s. For integral values whose partial sums
// stay below 2**53 the result is exact and therefore identical. Above that
// range, exact integer sums are not representable in float64 at all.
func Sum(values []float64) float64 {
	var acc floatSum
	for _, v := range values {
		acc.Add(v)
	}
	return acc.Value()
}

// floatSum is CPython's CompensatedSum (Modules/bltinmodule.c): a Neumaier
// compensated running sum.
type floatSum struct {
	hi float64
	lo float64
}

// Add accumulates x into the running sum.
func (s *floatSum) Add(x float64) {
	t := s.hi + x
	if math.Abs(s.hi) >= math.Abs(x) {
		s.lo += (s.hi - t) + x
	} else {
		s.lo += (x - t) + s.hi
	}
	s.hi = t
}

// Value collapses the compensation. The checks mirror cs_to_double: keep a
// negative result's sign and never let the compensation turn an infinite
// sum into a NaN.
func (s *floatSum) Value() float64 {
	if s.lo != 0 && isFinite(s.lo) {
		return s.hi + s.lo
	}
	return s.hi
}

// isFinite reports whether v is neither NaN nor an infinity.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
