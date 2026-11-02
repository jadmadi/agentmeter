// SPDX-License-Identifier: AGPL-3.0-only
// Portions derived from CPython's Modules/mathmodule.c (PSF-2.0).
// See THIRD_PARTY_NOTICES.md and third_party/licenses/PSF-2.0.txt.

package stats

import "math"

// Fsum mirrors CPython's math.fsum for a sequence of float64 values.
//
// It is a direct port of the Shewchuk partial-sums algorithm in
// Modules/mathmodule.c: each addition is split into a rounded high part and
// an exact low part, the partials stay non-overlapping and increasing in
// magnitude, and the final collapse applies CPython's half-even fix-up. The
// result therefore matches Python bit for bit for finite inputs whose
// intermediate partial sums stay finite.
//
// Special-value handling follows the same code paths:
//
//   - NaN inputs produce NaN.
//   - A single infinity dominates a finite sum and is returned.
//   - An intermediate overflow of finite values returns the overflowed ±Inf;
//     CPython raises OverflowError instead.
//   - A mix of +Inf and -Inf returns NaN; CPython raises ValueError.
//
// The last two differences are unreachable for report inputs and are the only
// places where the Go result is defined rather than identical to Python.
func Fsum(values []float64) float64 {
	var (
		partials   []float64
		specialSum float64
		infSum     float64
		lo         float64
	)

	for _, x := range values {
		xsave := x

		// Shewchuk step: fold x into the sorted, non-overlapping partials.
		// Writing p[i] while reading p[j] is safe because i <= j.
		i := 0
		for j := 0; j < len(partials); j++ {
			y := partials[j]
			if math.Abs(x) < math.Abs(y) {
				x, y = y, x
			}
			hi := x + y
			yr := hi - x
			lo = y - yr
			if lo != 0 {
				partials[i] = lo
				i++
			}
			x = hi
		}
		partials = partials[:i]

		if x != 0 {
			if !isFinite(x) {
				// A non-finite x arises either from intermediate overflow
				// (xsave is finite) or from an inf/NaN input.
				if isFinite(xsave) {
					// CPython: OverflowError("intermediate overflow in fsum").
					return x
				}
				if math.IsInf(xsave, 0) {
					infSum += xsave
				}
				specialSum += xsave
				partials = partials[:0]
			} else {
				partials = append(partials, x)
			}
		}
	}

	if specialSum != 0 {
		if math.IsNaN(infSum) {
			// CPython: ValueError("-inf + inf in fsum").
			return math.NaN()
		}
		return specialSum
	}

	hi := 0.0
	if n := len(partials); n > 0 {
		n--
		hi = partials[n]
		// Collapse from the top until the sum becomes inexact.
		for n > 0 {
			x := hi
			y := partials[n-1]
			n--
			hi = x + y
			yr := hi - x
			lo = y - yr
			if lo != 0 {
				break
			}
		}
		// Make half-even rounding work across multiple partials. Without
		// this, sum([1e-16, 1, 1e16]) would round down instead of up.
		if n > 0 && ((lo < 0 && partials[n-1] < 0) || (lo > 0 && partials[n-1] > 0)) {
			y := lo * 2
			x := hi + y
			yr := x - hi
			if y == yr {
				hi = x
			}
		}
	}
	return hi
}
