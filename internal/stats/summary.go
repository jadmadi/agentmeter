// SPDX-License-Identifier: AGPL-3.0-only

package stats

import (
	"math"
	"sort"
)

// Percentile mirrors telemetry_common.percentile for a pre-sorted slice:
//
//	pos = (len(values) - 1) * p / 100.0
//	lo  = int(pos)
//	hi  = min(lo + 1, len(values) - 1)
//	values[lo] + (values[hi] - values[lo]) * (pos - lo)
//
// It reports false for an empty slice (Python returns None). p is a
// percentage in [0, 100]; out-of-range values would index out of bounds in
// Python, so this implementation reports false instead of panicking.
func Percentile(sortedVals []float64, p float64) (float64, bool) {
	n := len(sortedVals)
	if n == 0 {
		return 0, false
	}
	if n == 1 {
		return sortedVals[0], true
	}
	pos := float64(n-1) * p / 100.0
	lo := int(pos)
	hi := lo + 1
	if hi > n-1 {
		hi = n - 1
	}
	if lo < 0 || lo >= n {
		return 0, false
	}
	return sortedVals[lo] + (sortedVals[hi]-sortedVals[lo])*(pos-float64(lo)), true
}

// Median mirrors statistics.median for floats: the middle value for an odd
// count and the average of the two middle values for an even count. The input
// is not modified. An empty slice reports false (Python raises
// StatisticsError). NaNs are not filtered; like Python's statistics.median
// they simply participate in the sort, and Python's builders never produce
// NaN samples. Use MedianOrNone for the None-filtering helper.
func Median(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	return medianSorted(sorted), true
}

// MedianOrNone mirrors telemetry_common.median_or_none. Python filters None
// before taking the median; Go models that filter by dropping NaNs, the only
// missing-value marker a []float64 can carry. An empty (or all-NaN) slice
// reports false, matching Python's None return.
func MedianOrNone(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := make([]float64, 0, len(values))
	for _, v := range values {
		if math.IsNaN(v) {
			continue
		}
		sorted = append(sorted, v)
	}
	if len(sorted) == 0 {
		return 0, false
	}
	sort.Float64s(sorted)
	return medianSorted(sorted), true
}

// medianSorted returns the median of an ascending slice. The caller must
// guarantee a non-empty slice.
func medianSorted(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	i := n / 2
	return (sorted[i-1] + sorted[i]) / 2
}

// RobustZ mirrors telemetry_common.robust_z: a modified z-score using the
// median and the median absolute deviation, scaled by 0.6745.
//
// It reports false when fewer than five finite observations are available,
// when value is not finite, or when the MAD is zero (Python returns None in
// each case).
func RobustZ(values []float64, value float64) (float64, bool) {
	vals := make([]float64, 0, len(values))
	for _, v := range values {
		if !isFinite(v) {
			continue
		}
		vals = append(vals, v)
	}
	if len(vals) < 5 || !isFinite(value) {
		return 0, false
	}
	sort.Float64s(vals)
	med := medianSorted(vals)

	deviations := make([]float64, len(vals))
	for i, v := range vals {
		deviations[i] = math.Abs(v - med)
	}
	sort.Float64s(deviations)
	mad := medianSorted(deviations)
	if mad == 0 {
		return 0, false
	}
	return 0.6745 * (value - med) / mad, true
}
