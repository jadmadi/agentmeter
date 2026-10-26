// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"fmt"
	"math"
	"sort"

	"github.com/jadmadi/agentmeter/internal/stats"
)

// obj builds a report object from alternating key/value pairs. Using it keeps
// the Go literals visually aligned with the Python dicts they replace, which is
// the main defence against a silently missing schema field.
func obj(pairs ...any) map[string]any {
	if len(pairs)%2 != 0 {
		panic(fmt.Sprintf("obj: odd number of arguments (%d)", len(pairs)))
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			panic(fmt.Sprintf("obj: key %d is %T, not string", i/2, pairs[i]))
		}
		out[key] = pairs[i+1]
	}
	return out
}

// frac is Python's round(value, ndigits) for schema values.
func frac(value float64, ndigits int) float64 { return stats.PyRound(value, ndigits) }

// r1/r2 are Python's r1/r2 helpers for known-present values.
func r1(value float64) float64 { return stats.R1(value) }
func r2(value float64) float64 { return stats.R2(value) }

// optFrac rounds an optional value, preserving "missing" as null.
func optFrac(value *float64, ndigits int) any {
	if value == nil {
		return nil
	}
	return stats.PyRound(*value, ndigits)
}

// optR1/optR2 round an optional value to 1/2 decimals.
func optR1(value *float64) any { return optFrac(value, 1) }
func optR2(value *float64) any { return optFrac(value, 2) }

// optInt renders an optional int.
func optInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// ptr returns a pointer to a copy of value.
func ptr[T any](value T) *T { return &value }

// orZero is Python's `value or 0`.
func orZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// orZeroInt is Python's `value or 0` for integers.
func orZeroInt(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// median returns statistics.median for a float sample.
func median(values []float64) (float64, bool) { return stats.Median(values) }

// medianAny returns statistics.median or null.
func medianAny(values []float64) any {
	if value, ok := stats.Median(values); ok {
		return value
	}
	return nil
}

// medianR1 returns r1(statistics.median(values)) or null.
func medianR1(values []float64) any {
	if value, ok := stats.Median(values); ok {
		return stats.R1(value)
	}
	return nil
}

// medianR2 returns r2(statistics.median(values)) or null.
func medianR2(values []float64) any {
	if value, ok := stats.Median(values); ok {
		return stats.R2(value)
	}
	return nil
}

// medianRounded returns round(statistics.median(values), ndigits) or null.
func medianRounded(values []float64, ndigits int) any {
	if value, ok := stats.Median(values); ok {
		return stats.PyRound(value, ndigits)
	}
	return nil
}

// percentileAny returns r1(percentile(sorted, p)) or null.
func percentileAny(sorted []float64, p float64) any {
	if value, ok := stats.Percentile(sorted, p); ok {
		return stats.R1(value)
	}
	return nil
}

// percentileR2 returns r2(percentile(sorted, p)) or null.
func percentileR2(sorted []float64, p float64) any {
	if value, ok := stats.Percentile(sorted, p); ok {
		return stats.R2(value)
	}
	return nil
}

// sumFloats is Python's builtin sum() over floats (CPython 3.12+ uses a
// compensated Neumaier accumulator).
func sumFloats(values []float64) float64 { return stats.Sum(values) }

// fsumFloats is math.fsum.
func fsumFloats(values []float64) float64 { return stats.Fsum(values) }

// sumInts is exact integer addition.
func sumInts(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}

// sortedFloats returns a sorted copy.
func sortedFloats(values []float64) []float64 {
	out := make([]float64, len(values))
	copy(out, values)
	sort.Float64s(out)
	return out
}

// safeDiv returns value/denom or null for a zero denominator.
func safeDiv(value, denom float64) any {
	if denom == 0 {
		return nil
	}
	return value / denom
}

// safeDivRound returns round(value/denom, ndigits) or null.
func safeDivRound(value, denom float64, ndigits int) any {
	if denom == 0 {
		return nil
	}
	return stats.PyRound(value/denom, ndigits)
}

// pyMin returns the smallest value, or nil for an empty slice.
func pyMin(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	smallest := values[0]
	for _, value := range values[1:] {
		if value < smallest {
			smallest = value
		}
	}
	return &smallest
}

// pyMax returns the largest value, or nil for an empty slice.
func pyMax(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	largest := values[0]
	for _, value := range values[1:] {
		if value > largest {
			largest = value
		}
	}
	return &largest
}

// isFinite reports whether value is neither NaN nor an infinity.
func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
