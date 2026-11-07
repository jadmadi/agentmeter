// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"math"
	"sort"

	"github.com/jadmadi/agentmeter/internal/stats"
)

// This file holds the CPython-parity numerics the report needs.
//
// Shared-package boundary:
//   - statistics that are already exact in internal/stats are called through
//     the thin wrappers below (pyRound, r1, r2, median, percentile,
//     stableSeed);
//   - the small regression/robustness helpers are implemented here because
//     they mirror a specific Python function shape (returned dict keys,
//     None-vs-zero semantics, operation order). TODO(shared-stats): when
//     internal/stats exposes LinearFit/SolveLinear/RobustZ/Standardize/
//     BootstrapMedianCI with these semantics, delete the local copies and
//     delegate; the call sites are already isolated to this file.

func pyRound(value float64, ndigits int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	return stats.PyRound(value, ndigits)
}

func r1(value float64) float64 { return stats.R1(value) }
func r2(value float64) float64 { return stats.R2(value) }

func median(values []float64) float64 {
	value, _ := stats.Median(values)
	return value
}

func percentile(sortedValues []float64, p float64) float64 {
	value, _ := stats.Percentile(sortedValues, p)
	return value
}

// sumAcc reproduces CPython 3.12+ sum() over ints/floats, which uses
// Neumaier compensated summation. Python's sum() is NOT a plain left-to-right
// float addition any more, and every float aggregate in the builder relies on
// it; a plain += loop drifts by an ulp on long series.
type sumAcc struct {
	result float64
	comp   float64
}

func (acc *sumAcc) add(value float64) {
	total := acc.result + value
	if math.Abs(acc.result) >= math.Abs(value) {
		acc.comp += (acc.result - total) + value
	} else {
		acc.comp += (value - total) + acc.result
	}
	acc.result = total
}

func (acc *sumAcc) value() float64 { return acc.result + acc.comp }

// pySum is sum() over a float slice.
func pySum(values []float64) float64 {
	acc := sumAcc{}
	for _, value := range values {
		acc.add(value)
	}
	return acc.value()
}

// linearFitResult mirrors linear_fit()'s dict.
type linearFitResult struct {
	fixedSeconds         float64
	pureRate             float64
	r2                   *float64
	n                    int64
	interceptConstrained bool
	usable               bool
}

// linearFit ports telemetry_common.linear_fit with the same operation order.
func linearFit(xs, ys []float64, minN int) *linearFitResult {
	n := len(xs)
	if n < minN {
		return nil
	}
	mx := pySum(xs) / float64(n)
	my := pySum(ys) / float64(n)
	sxxAcc := sumAcc{}
	for _, x := range xs {
		delta := x - mx
		sxxAcc.add(delta * delta)
	}
	sxx := sxxAcc.value()
	if sxx == 0 {
		return nil
	}
	sxyAcc := sumAcc{}
	for index, x := range xs {
		sxyAcc.add((x - mx) * (ys[index] - my))
	}
	sxy := sxyAcc.value()
	slope := sxy / sxx
	intercept := my - slope*mx
	if slope <= 0 {
		return nil
	}
	constrained := intercept < 0
	if constrained {
		denomAcc := sumAcc{}
		for _, x := range xs {
			denomAcc.add(x * x)
		}
		denom := denomAcc.value()
		if denom == 0 {
			return nil
		}
		numeratorAcc := sumAcc{}
		for index, x := range xs {
			numeratorAcc.add(x * ys[index])
		}
		slope = numeratorAcc.value() / denom
		intercept = 0.0
		if slope <= 0 {
			return nil
		}
	}
	syyAcc := sumAcc{}
	ssResAcc := sumAcc{}
	for index, y := range ys {
		predicted := intercept + slope*xs[index]
		delta := y - my
		syyAcc.add(delta * delta)
		residual := y - predicted
		ssResAcc.add(residual * residual)
	}
	syy, ssRes := syyAcc.value(), ssResAcc.value()
	var rSquared *float64
	if syy != 0 {
		value := 1 - ssRes/syy
		rSquared = &value
	}
	result := &linearFitResult{
		fixedSeconds:         pyRound(intercept, 2),
		pureRate:             pyRound(1/slope, 1),
		r2:                   nil,
		n:                    int64(n),
		interceptConstrained: constrained,
	}
	if rSquared != nil {
		rounded := pyRound(*rSquared, 2)
		result.r2 = &rounded
	}
	return result
}

// solveLinear ports telemetry_common.solve_linear: partial pivoting, 1e-12
// singular threshold, None on failure.
func solveLinear(matrix [][]float64, rhs []float64) []float64 {
	n := len(rhs)
	work := make([][]float64, n)
	for row := 0; row < n; row++ {
		work[row] = make([]float64, n+1)
		copy(work[row], matrix[row])
		work[row][n] = rhs[row]
	}
	for col := 0; col < n; col++ {
		pivot := col
		best := math.Abs(work[col][col])
		for row := col + 1; row < n; row++ {
			if candidate := math.Abs(work[row][col]); candidate > best {
				best = candidate
				pivot = row
			}
		}
		work[col], work[pivot] = work[pivot], work[col]
		if math.Abs(work[col][col]) < 1e-12 {
			return nil
		}
		for row := col + 1; row < n; row++ {
			factor := work[row][col] / work[col][col]
			for k := col; k <= n; k++ {
				work[row][k] -= factor * work[col][k]
			}
		}
	}
	solution := make([]float64, n)
	for row := n - 1; row >= 0; row-- {
		total := 0.0
		for col := row + 1; col < n; col++ {
			total += work[row][col] * solution[col]
		}
		solution[row] = (work[row][n] - total) / work[row][row]
	}
	return solution
}

// robustZ ports telemetry_common.robust_z (modified z-score, MAD scaling).
// The second return value is false where Python returns None.
func robustZ(values []float64, value float64, hasValue bool) (float64, bool) {
	if len(values) < 5 || !hasValue || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	center := median(values)
	deviations := make([]float64, len(values))
	for index, item := range values {
		deviations[index] = math.Abs(item - center)
	}
	mad := median(deviations)
	if mad == 0 {
		return 0, false
	}
	return 0.6745 * (value - center) / mad, true
}

// standardize ports telemetry_common.standardize (direct standardization with
// renormalized observed weights). Keys are visited in `order` so the float
// summation order matches Python's dict insertion order.
func standardize(stratumValues map[string]float64, order []string, globalWeights map[string]int64) (float64, int64) {
	var covered int64
	for _, key := range order {
		covered += globalWeights[key]
	}
	if covered == 0 {
		return 0, 0
	}
	total := 0.0
	for _, key := range order {
		total += stratumValues[key] * float64(globalWeights[key])
	}
	return total / float64(covered), covered
}

// bootstrapMedianCI ports bootstrap_median_ci: a deterministic percentile
// bootstrap using CPython's MT19937 + randrange.
func bootstrapMedianCI(values []float64, iterations int, seed int64, alpha float64) []float64 {
	finite := make([]float64, 0, len(values))
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			finite = append(finite, value)
		}
	}
	n := len(finite)
	if n < 5 {
		return nil
	}
	source := newPyRandom(seed)
	medians := make([]float64, 0, iterations)
	sample := make([]float64, n)
	for iteration := 0; iteration < iterations; iteration++ {
		for index := 0; index < n; index++ {
			sample[index] = finite[source.randrange(n)]
		}
		medians = append(medians, median(sample))
	}
	sort.Float64s(medians)
	lowIndex := int(float64(iterations) * (alpha / 2))
	highIndex := int(float64(iterations)*(1-alpha/2)) - 1
	if highIndex > iterations-1 {
		highIndex = iterations - 1
	}
	if lowIndex < 0 {
		lowIndex = 0
	}
	if highIndex < 0 {
		highIndex = 0
	}
	return []float64{r1(medians[lowIndex]), r1(medians[highIndex])}
}

// stableSeed is telemetry.py's stable_seed(): sum((i+1) * ord(ch)) % 1e6.
func stableSeed(text string) int64 { return int64(stats.StableSeed(text)) }

// newPyRandom builds a CPython-compatible random.Random(seed).
func newPyRandom(seed int64) *pyRandom {
	key := make([]uint32, 0, 2)
	magnitude := uint64(seed)
	if seed < 0 {
		magnitude = uint64(-seed)
	}
	if magnitude == 0 {
		key = append(key, 0)
	}
	for magnitude > 0 {
		key = append(key, uint32(magnitude&0xffffffff))
		magnitude >>= 32
	}
	source := &pyRandom{}
	source.initByArray(key)
	return source
}

// pyRandom is CPython's Mersenne Twister seeding and randrange path.
type pyRandom struct {
	state [624]uint32
	index int
}

func (source *pyRandom) initGenrand(seed uint32) {
	source.state[0] = seed
	for index := 1; index < 624; index++ {
		previous := source.state[index-1]
		source.state[index] = 1812433253*(previous^(previous>>30)) + uint32(index)
	}
	source.index = 624
}

func (source *pyRandom) initByArray(key []uint32) {
	source.initGenrand(19650218)
	i := 1
	j := 0
	k := 624
	if len(key) > k {
		k = len(key)
	}
	for ; k > 0; k-- {
		previous := source.state[i-1]
		source.state[i] = (source.state[i] ^ ((previous ^ (previous >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= 624 {
			source.state[0] = source.state[623]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k := 623; k > 0; k-- {
		previous := source.state[i-1]
		source.state[i] = (source.state[i] ^ ((previous ^ (previous >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= 624 {
			source.state[0] = source.state[623]
			i = 1
		}
	}
	source.state[0] = 0x80000000
}

func (source *pyRandom) uint32() uint32 {
	if source.index >= 624 {
		for index := 0; index < 624; index++ {
			value := (source.state[index] & 0x80000000) | (source.state[(index+1)%624] & 0x7fffffff)
			next := source.state[(index+397)%624] ^ (value >> 1)
			if value&1 != 0 {
				next ^= 0x9908b0df
			}
			source.state[index] = next
		}
		source.index = 0
	}
	value := source.state[source.index]
	source.index++
	value ^= value >> 11
	value ^= (value << 7) & 0x9d2c5680
	value ^= (value << 15) & 0xefc60000
	value ^= value >> 18
	return value
}

// getrandbits reproduces CPython for k <= 32 (the only width randrange needs).
func (source *pyRandom) getrandbits(k uint) uint64 {
	if k == 0 {
		return 0
	}
	if k <= 32 {
		return uint64(source.uint32() >> (32 - k))
	}
	// Wider draws are not used by randrange for our sample sizes.
	higher := source.getrandbits(k - 32)
	return (higher << 32) | uint64(source.uint32())
}

// randrange reproduces Python's _randbelow_with_getrandbits.
func (source *pyRandom) randrange(n int) int {
	if n <= 0 {
		return 0
	}
	width := uint(0)
	for value := n; value > 0; value >>= 1 {
		width++
	}
	candidate := source.getrandbits(width)
	for int64(candidate) >= int64(n) {
		candidate = source.getrandbits(width)
	}
	return int(candidate)
}
