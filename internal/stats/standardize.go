// SPDX-License-Identifier: AGPL-3.0-only

package stats

import "sort"

// Stratum is one (key, median) pair for StandardizeOrdered. Keeping the keys
// in a slice lets callers reproduce Python's dict insertion order exactly,
// which matters for last-bit float parity of the weighted sum.
type Stratum struct {
	Key   string
	Value float64
}

// Standardize mirrors telemetry_common.standardize: direct standardization
// across the strata present for a group.
//
// stratumValues maps a stratum key to a median; globalWeights maps the same
// keys to the population count. Missing weights count as zero and the
// remaining weights are renormalized, so the result is a weighted mean of the
// observed strata. It reports false with covered == 0 when no stratum carries
// weight (Python returns (None, 0)).
//
// Go maps are unordered, so the weighted sum is accumulated over the keys
// sorted lexicographically. Python accumulates in dict insertion order; when
// that differs, the last bit of the result can differ. Callers that need
// CPython-identical summation order must use StandardizeOrdered.
func Standardize(stratumValues map[string]float64, globalWeights map[string]int) (float64, int, bool) {
	keys := make([]string, 0, len(stratumValues))
	for key := range stratumValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return standardizeKeys(keys, stratumValues, globalWeights)
}

// StandardizeOrdered is Standardize with an explicit accumulation order
// (typically Python's dict insertion order). strata may contain a key once;
// duplicate keys would be summed twice, so callers should pass unique keys.
func StandardizeOrdered(strata []Stratum, globalWeights map[string]int) (float64, int, bool) {
	keys := make([]string, len(strata))
	values := make(map[string]float64, len(strata))
	for i, s := range strata {
		keys[i] = s.Key
		values[s.Key] = s.Value
	}
	return standardizeKeys(keys, values, globalWeights)
}

func standardizeKeys(keys []string, stratumValues map[string]float64, globalWeights map[string]int) (float64, int, bool) {
	covered := 0
	for _, key := range keys {
		covered += globalWeights[key]
	}
	if covered == 0 {
		return 0, 0, false
	}
	var total floatSum
	for _, key := range keys {
		total.Add(stratumValues[key] * float64(globalWeights[key]))
	}
	return total.Value() / float64(covered), covered, true
}
