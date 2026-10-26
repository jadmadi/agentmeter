// SPDX-License-Identifier: AGPL-3.0-only

package stats

import (
	"sort"

	"github.com/jadmadi/agentmeter/internal/stats/pyrandom"
)

// BootstrapMedianCI mirrors telemetry_common.bootstrap_median_ci: a
// deterministic percentile bootstrap confidence interval for the median.
//
// Non-finite observations are dropped first (Python filters None and
// non-finite values); fewer than five remaining observations reports false
// (Python returns None). Samples are drawn with CPython's random.Random(seed)
// MT19937 stream and randrange(n), then the medians are sorted and indexed
// exactly as Python does:
//
//	lo = medians[int((alpha/2) * iterations)]
//	hi = medians[min(iterations-1, int((1-alpha/2) * iterations) - 1)]
//
// Both bounds are rounded to one decimal with R1 (Python r1). iterations must
// be positive; index bounds use Python list indexing (negative indices wrap),
// and malformed bounds such as alpha far outside (0, 1) report false rather
// than panicking.
func BootstrapMedianCI(values []float64, iterations int, seed int64, alpha float64) ([2]float64, bool) {
	var out [2]float64
	vals := make([]float64, 0, len(values))
	for _, v := range values {
		if !isFinite(v) {
			continue
		}
		vals = append(vals, v)
	}
	n := len(vals)
	if n < 5 || iterations <= 0 {
		return out, false
	}

	rng := pyrandom.New(seed)
	medians := make([]float64, iterations)
	sample := make([]float64, n)
	for i := 0; i < iterations; i++ {
		for j := 0; j < n; j++ {
			sample[j] = vals[rng.RandRange(n)]
		}
		// The sample is overwritten on the next iteration, so sort in place.
		sort.Float64s(sample)
		medians[i] = medianSorted(sample)
	}
	sort.Float64s(medians)

	loIdx := int((alpha / 2) * float64(iterations))
	hiIdx := int((1-alpha/2)*float64(iterations)) - 1
	if hiIdx > iterations-1 {
		hiIdx = iterations - 1
	}
	loIdx, okLo := pythonListIndex(loIdx, iterations)
	hiIdx, okHi := pythonListIndex(hiIdx, iterations)
	if !okLo || !okHi {
		return out, false
	}
	out[0] = R1(medians[loIdx])
	out[1] = R1(medians[hiIdx])
	return out, true
}

// pythonListIndex applies Python's negative list indexing and reports false
// when the (possibly wrapped) index would raise IndexError. Alpha values far
// outside (0, 1) can produce such indices; the real callers never do.
func pythonListIndex(index, length int) (int, bool) {
	if index < 0 {
		index += length
	}
	if index < 0 || index >= length {
		return 0, false
	}
	return index, true
}
