// SPDX-License-Identifier: AGPL-3.0-only

package stats

import "math"

// Fit is the Go form of telemetry_common.linear_fit's result dictionary.
//
// Python uses None for a missing r2; HasR2 distinguishes "r2 is 0.0" from
// "r2 is absent". FixedSeconds, PureRate and R2 are already rounded to the
// schema precision (2, 1 and 2 decimals) with PyRound.
type Fit struct {
	FixedSeconds         float64
	PureRate             float64
	R2                   float64
	HasR2                bool
	N                    int
	InterceptConstrained bool
}

// SolveLinear mirrors telemetry_common.solve_linear: Gaussian elimination
// with partial pivoting and a 1e-12 singularity threshold.
//
// The input matrix and rhs are not modified. A nil/false result means the
// system is singular under the same threshold Python uses; an empty rhs
// returns an empty (non-nil) solution and true. Rows shorter than len(rhs),
// or a matrix with fewer rows than len(rhs), are rejected rather than
// panic; Python would raise IndexError for those malformed shapes.
func SolveLinear(matrix [][]float64, rhs []float64) ([]float64, bool) {
	n := len(rhs)
	if n == 0 {
		return []float64{}, true
	}
	if len(matrix) < n {
		return nil, false
	}

	// Augmented copy: Python builds [list(matrix[i]) + [rhs[i]] for i in range(n)].
	m := make([][]float64, n)
	for i := 0; i < n; i++ {
		if len(matrix[i]) < n {
			return nil, false
		}
		row := make([]float64, n+1)
		copy(row, matrix[i][:n])
		row[n] = rhs[i]
		m[i] = row
	}

	for col := 0; col < n; col++ {
		// max(range(col, n), key=abs) picks the first maximal row.
		pivot := col
		best := math.Abs(m[col][col])
		for r := col + 1; r < n; r++ {
			if v := math.Abs(m[r][col]); v > best {
				pivot = r
				best = v
			}
		}
		m[col], m[pivot] = m[pivot], m[col]
		if math.Abs(m[col][col]) < 1e-12 {
			return nil, false
		}
		for r := col + 1; r < n; r++ {
			factor := m[r][col] / m[col][col]
			for k := col; k <= n; k++ {
				m[r][k] -= factor * m[col][k]
			}
		}
	}

	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		// Python's sum() over the remaining terms is compensated in 3.12+.
		var acc floatSum
		for c := r + 1; c < n; c++ {
			acc.Add(m[r][c] * x[c])
		}
		x[r] = (m[r][n] - acc.Value()) / m[r][r]
	}
	return x, true
}

// LinearFit mirrors telemetry_common.linear_fit: ordinary least squares for
// y = fixed + x / rate, with a non-negative fixed term.
//
// It reports false when the sample is smaller than minN, when xs has zero
// variance, when the slope is not positive, or when the constrained refit
// has a zero denominator. A negative unconstrained intercept is refit through
// the origin (intercept 0, InterceptConstrained true). The returned values
// are rounded exactly where Python rounds: fixedSeconds to 2 decimals,
// pureRate to 1 decimal and r2 to 2 decimals (HasR2 false when the y variance
// is zero).
//
// xs and ys must have the same length; Python's zip would silently truncate,
// but every builder call site supplies equal-length lists. All internal
// accumulations use Sum, which matches CPython 3.12+'s compensated builtin
// sum() for float terms. Python's builders may pass Python ints for xs, whose
// sum() is exact integer addition; for token counts in the realistic range
// (< 2**53) both forms are exact and identical.
func LinearFit(xs, ys []float64, minN int) (Fit, bool) {
	var empty Fit
	n := len(xs)
	if n == 0 || n < minN || len(ys) != n {
		return empty, false
	}

	mx := Sum(xs) / float64(n)
	my := Sum(ys) / float64(n)

	var sxxAcc floatSum
	for _, x := range xs {
		d := x - mx
		sxxAcc.Add(d * d)
	}
	sxx := sxxAcc.Value()
	if sxx == 0 {
		return empty, false
	}

	var sxyAcc floatSum
	for i, x := range xs {
		sxyAcc.Add((x - mx) * (ys[i] - my))
	}
	slope := sxyAcc.Value() / sxx
	intercept := my - slope*mx
	if slope <= 0 {
		return empty, false
	}

	constrained := intercept < 0
	if constrained {
		var denomAcc floatSum
		for _, x := range xs {
			denomAcc.Add(x * x)
		}
		denom := denomAcc.Value()
		if denom == 0 {
			return empty, false
		}
		var numAcc floatSum
		for i, x := range xs {
			numAcc.Add(x * ys[i])
		}
		slope = numAcc.Value() / denom
		intercept = 0
		if slope <= 0 {
			return empty, false
		}
	}

	predicted := make([]float64, n)
	for i, x := range xs {
		predicted[i] = intercept + slope*x
	}

	var syyAcc floatSum
	for _, y := range ys {
		d := y - my
		syyAcc.Add(d * d)
	}
	syy := syyAcc.Value()

	var residualAcc floatSum
	for i, y := range ys {
		d := y - predicted[i]
		residualAcc.Add(d * d)
	}
	ssRes := residualAcc.Value()

	fit := Fit{
		FixedSeconds:         PyRound(intercept, 2),
		PureRate:             PyRound(1/slope, 1),
		N:                    n,
		InterceptConstrained: constrained,
	}
	if syy != 0 {
		fit.HasR2 = true
		fit.R2 = PyRound(1-ssRes/syy, 2)
	}
	return fit, true
}
