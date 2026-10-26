// SPDX-License-Identifier: AGPL-3.0-only

package stats

import (
	"math"
	"testing"
)

func TestPyRoundTaskCases(t *testing.T) {
	cases := []struct {
		x     float64
		n     int
		want  float64
		label string
	}{
		{2.675, 2, 2.67, "2.675/2 rounds down on the exact binary value"},
		{1.005, 2, 1.0, "1.005/2 rounds down on the exact binary value"},
		{-2.675, 2, -2.67, "negative 2.675/2"},
		{-1.005, 2, -1.0, "negative 1.005/2"},
		{2.685, 2, 2.69, "2.685/2 rounds up"},
		{0.5, 0, 0, "half-to-even 0.5 -> 0"},
		{1.5, 0, 2, "half-to-even 1.5 -> 2"},
		{2.5, 0, 2, "half-to-even 2.5 -> 2"},
		{3.5, 0, 4, "half-to-even 3.5 -> 4"},
		{-0.5, 0, math.Copysign(0, -1), "half-to-even -0.5 -> -0"},
		{-1.5, 0, -2, "half-to-even -1.5 -> -2"},
		{-2.5, 0, -2, "half-to-even -2.5 -> -2"},
		{15.0, -1, 20, "15/-1 half-to-even -> 20"},
		{25.0, -1, 20, "25/-1 half-to-even -> 20"},
		{45.0, -1, 40, "45/-1 half-to-even -> 40"},
		{55.0, -1, 60, "55/-1 -> 60"},
		{-15.0, -1, -20, "-15/-1 -> -20"},
		{-25.0, -1, -20, "-25/-1 -> -20"},
		{2.675, -1, 0, "2.675/-1 -> 0"},
		{1.23456789, 6, 1.234568, "6 decimals"},
		{1.23456789, 4, 1.2346, "4 decimals"},
		{1234.5678, -2, 1200, "negative ndigits"},
		{-1234.5678, -2, -1200, "negative ndigits negative x"},
		{1e308, 2, 1e308, "huge value unchanged at 2 decimals"},
		{1e308, -308, 1e308, "1e308/-308"},
		{9.9e307, -308, 1e308, "9.9e307/-308 rounds up"},
		{1e308, -309, 0, "1e308/-309 -> 0"},
		{5e-324, 323, 0, "smallest subnormal at 323 digits -> 0"},
		{5e-324, 324, 5e-324, "ndigits above 323 returns x unchanged"},
		{0.0, 2, 0.0, "positive zero"},
		{math.Copysign(0, -1), 2, math.Copysign(0, -1), "negative zero"},
	}
	for _, c := range cases {
		got := PyRound(c.x, c.n)
		if !sameFloat(got, c.want) {
			t.Errorf("%s: PyRound(%v, %d) = %s, want %s",
				c.label, c.x, c.n, describeFloat(got), describeFloat(c.want))
		}
	}

	// Negative values that round away from zero keep the negative zero sign.
	if got := PyRound(-0.4, 0); math.Float64bits(got) != math.Float64bits(math.Copysign(0, -1)) {
		t.Errorf("PyRound(-0.4, 0) = %s, want -0.0", describeFloat(got))
	}
	if got := PyRound(-0.001, 2); math.Signbit(got) == false || got != 0 {
		t.Errorf("PyRound(-0.001, 2) = %s, want -0.0", describeFloat(got))
	}

	// Non-finite inputs pass through unchanged.
	for _, x := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		if got := PyRound(x, 3); !sameFloat(got, x) {
			t.Errorf("PyRound(%v, 3) = %v", x, got)
		}
		if got := PyRound(x, 1000); !sameFloat(got, x) {
			t.Errorf("PyRound(%v, 1000) = %v", x, got)
		}
	}

	// CPython raises OverflowError when the rounded result is too large;
	// PyRound documents +Inf because Go has no error channel.
	if got := PyRound(1.5e308, -308); !math.IsInf(got, 1) {
		t.Errorf("PyRound(1.5e308, -308) = %v, want +Inf", got)
	}

	// Inputs are never modified (no slices here, but exercise both signs).
	if got := PyRound(-1.25, 1); got != -1.2 {
		t.Errorf("PyRound(-1.25, 1) = %v, want -1.2", got)
	}
}

func TestR1R2(t *testing.T) {
	if got := R1(1.24); got != 1.2 {
		t.Errorf("R1(1.24) = %v, want 1.2", got)
	}
	if got := R2(1.236); got != 1.24 {
		t.Errorf("R2(1.236) = %v, want 1.24", got)
	}
	requireFloat(t, "R1(1.25)", R1(1.25), 1.2) // exact tie -> even
	requireFloat(t, "R1(1.35)", R1(1.35), 1.4)
	requireFloat(t, "R1(-1.25)", R1(-1.25), -1.2)
	requireFloat(t, "R2(2.675)", R2(2.675), 2.67)
	requireFloat(t, "R2(1.005)", R2(1.005), 1.0)
	requireFloat(t, "R2(0)", R2(0), 0)
	if got := R2(-0.001); math.Float64bits(got) != math.Float64bits(math.Copysign(0, -1)) {
		t.Errorf("R2(-0.001) = %s, want -0.0", describeFloat(got))
	}
}

func TestFsumCases(t *testing.T) {
	if got := Fsum(nil); got != 0 {
		t.Errorf("Fsum(nil) = %v, want 0", got)
	}
	if got := Fsum([]float64{-0.0}); math.Float64bits(got) != 0 {
		t.Errorf("Fsum([-0.0]) = %s, want +0.0", describeFloat(got))
	}
	if got := Fsum([]float64{0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}); got != 1.0 {
		t.Errorf("Fsum(10*0.1) = %v, want 1.0", got)
	}
	requireFloat(t, "Fsum cancel", Fsum([]float64{1e100, 1.0, -1e100}), 1.0)
	requireFloat(t, "Fsum 1e16", Fsum([]float64{1e-16, 1.0, 1e16}),
		vecFloat(t, "0x1.1c37937e08001p+53"))
	if got := Fsum([]float64{1e308, -1e308, 1e308}); got != 1e308 {
		t.Errorf("Fsum intermediate cancellation = %v, want 1e308", got)
	}
	if got := Fsum([]float64{math.Inf(1), 1.0}); !math.IsInf(got, 1) {
		t.Errorf("Fsum([+Inf, 1]) = %v, want +Inf", got)
	}
	if got := Fsum([]float64{math.Inf(-1), 1.0}); !math.IsInf(got, -1) {
		t.Errorf("Fsum([-Inf, 1]) = %v, want -Inf", got)
	}
	if got := Fsum([]float64{math.Inf(1), math.Inf(-1)}); !math.IsNaN(got) {
		t.Errorf("Fsum([+Inf, -Inf]) = %v, want NaN (CPython raises ValueError)", got)
	}
	if got := Fsum([]float64{math.NaN(), 1.0}); !math.IsNaN(got) {
		t.Errorf("Fsum([NaN, 1]) = %v, want NaN", got)
	}
	// Intermediate overflow: CPython raises OverflowError; the Go signature
	// has no error channel, so Fsum returns the overflowed infinity.
	if got := Fsum([]float64{1e308, 1e308, -1e308}); !math.IsInf(got, 1) {
		t.Errorf("Fsum(overflow) = %v, want +Inf", got)
	}

	input := []float64{1e16, 1.0, 1e-16}
	before := append([]float64(nil), input...)
	Fsum(input)
	for i := range input {
		if input[i] != before[i] {
			t.Fatalf("Fsum modified its input at %d", i)
		}
	}
}

func TestSumCases(t *testing.T) {
	if got := Sum(nil); got != 0 {
		t.Errorf("Sum(nil) = %v, want 0", got)
	}
	if got := Sum([]float64{0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}); got != 1.0 {
		t.Errorf("Sum(10*0.1) = %v, want 1.0 (Neumaier)", got)
	}
	requireFloat(t, "Sum cancel", Sum([]float64{1e16, 1.0, -1e16}), 1.0)
	requireFloat(t, "Sum cancel 100", Sum([]float64{1e100, 1.0, -1e100}), 1.0)
	if got := Sum([]float64{math.NaN(), 1.0}); !math.IsNaN(got) {
		t.Errorf("Sum([NaN, 1]) = %v, want NaN", got)
	}
	input := []float64{1e16, 1.0, -1e16}
	Sum(input)
	if input[0] != 1e16 || input[1] != 1.0 || input[2] != -1e16 {
		t.Fatalf("Sum modified its input: %v", input)
	}
}

func TestPercentileSemantics(t *testing.T) {
	if _, ok := Percentile(nil, 50); ok {
		t.Fatal("Percentile(empty) reported ok")
	}
	if got, ok := Percentile([]float64{7}, 90); !ok || got != 7 {
		t.Fatalf("Percentile([7], 90) = (%v, %v)", got, ok)
	}
	values := []float64{1, 2, 3, 4}
	cases := map[float64]float64{0: 1, 25: 1.75, 50: 2.5, 75: 3.25, 90: 3.7, 100: 4}
	for p, want := range cases {
		got, ok := Percentile(values, p)
		if !ok || !sameFloat(got, want) {
			t.Errorf("Percentile(%v, %v) = (%v, %v), want %v", values, p, got, ok, want)
		}
	}
	// The input is not sorted in place and not modified.
	if values[0] != 1 || values[3] != 4 {
		t.Fatalf("Percentile modified its input: %v", values)
	}
}

func TestMedianSemantics(t *testing.T) {
	if _, ok := Median(nil); ok {
		t.Fatal("Median(empty) reported ok")
	}
	values := []float64{4, 1, 3, 2}
	if got, ok := Median(values); !ok || got != 2.5 {
		t.Fatalf("Median(%v) = (%v, %v), want 2.5", values, got, ok)
	}
	if values[0] != 4 || values[1] != 1 || values[2] != 3 || values[3] != 2 {
		t.Fatalf("Median modified its input: %v", values)
	}
	if got, ok := Median([]float64{3, 1, 2}); !ok || got != 2 {
		t.Fatalf("Median odd = (%v, %v), want 2", got, ok)
	}
	if got, ok := Median([]float64{-0.0, 0.0}); !ok || got != 0 {
		t.Fatalf("Median(-0, 0) = (%v, %v), want 0", got, ok)
	}
}

func TestMedianOrNoneSemantics(t *testing.T) {
	got, ok := MedianOrNone([]float64{1, math.NaN(), 2, 3})
	if !ok || got != 2 {
		t.Fatalf("MedianOrNone with NaN = (%v, %v), want 2", got, ok)
	}
	if _, ok := MedianOrNone([]float64{math.NaN()}); ok {
		t.Fatal("MedianOrNone(all NaN) reported ok")
	}
	if _, ok := MedianOrNone(nil); ok {
		t.Fatal("MedianOrNone(nil) reported ok")
	}
}

func TestRobustZSemantics(t *testing.T) {
	values := make([]float64, 20)
	for i := range values {
		values[i] = float64(i + 1)
	}
	z, ok := RobustZ(values, 20)
	if !ok || z <= 0 {
		t.Fatalf("RobustZ(1..20, 20) = (%v, %v), want positive", z, ok)
	}
	z, ok = RobustZ(values, 10)
	if !ok || math.Abs(z) >= 0.1 {
		t.Fatalf("RobustZ(1..20, 10) = (%v, %v), want near zero", z, ok)
	}
	if _, ok := RobustZ([]float64{1, 1, 1, 1, 1}, 10); ok {
		t.Fatal("RobustZ with zero MAD reported ok")
	}
	if _, ok := RobustZ([]float64{1, 2, 3, 4}, 4); ok {
		t.Fatal("RobustZ with <5 values reported ok")
	}
	if _, ok := RobustZ(values, math.Inf(1)); ok {
		t.Fatal("RobustZ with non-finite value reported ok")
	}
	// Non-finite observations are filtered before the count check.
	withInf := append(append([]float64(nil), values...), math.Inf(1))
	a, okA := RobustZ(values, 12)
	b, okB := RobustZ(withInf, 12)
	if !okA || !okB || !sameFloat(a, b) {
		t.Fatalf("RobustZ infinity filtering: (%v,%v) vs (%v,%v)", a, okA, b, okB)
	}
}

func TestSafeDiv(t *testing.T) {
	if got, ok := SafeDiv(1, 2); !ok || got != 0.5 {
		t.Fatalf("SafeDiv(1,2) = (%v,%v)", got, ok)
	}
	for _, denom := range []float64{0, math.Copysign(0, -1)} {
		if _, ok := SafeDiv(1, denom); ok {
			t.Fatalf("SafeDiv(1,%v) reported ok", denom)
		}
	}
	if got, ok := SafeDiv(math.NaN(), 2); !ok || !math.IsNaN(got) {
		t.Fatalf("SafeDiv(NaN,2) = (%v,%v)", got, ok)
	}
	if got, ok := SafeDiv(1, math.NaN()); !ok || !math.IsNaN(got) {
		t.Fatalf("SafeDiv(1,NaN) = (%v,%v)", got, ok)
	}
	if got, ok := SafeDiv(1, math.Inf(1)); !ok || got != 0 {
		t.Fatalf("SafeDiv(1,Inf) = (%v,%v)", got, ok)
	}
}

func TestCloseSemantics(t *testing.T) {
	cases := []struct {
		a, b, tol float64
		want      bool
	}{
		{1, 1, 1e-6, true},
		{1, 1.0000001, 1e-6, true},
		{1, 1.001, 1e-6, false},
		{100, 100.00002, 1e-6, true},
		{100, 100.02, 1e-6, false},
		{1, 1, 0, true},
		{1, 1.0000001, -1e-6, true},
		{math.Inf(1), math.Inf(1), 1e-6, false},
		{math.Inf(-1), math.Inf(1), 1e-6, false},
		{math.NaN(), math.NaN(), 1e-6, false},
	}
	for _, c := range cases {
		if got := Close(c.a, c.b, c.tol); got != c.want {
			t.Errorf("Close(%v, %v, %v) = %v, want %v", c.a, c.b, c.tol, got, c.want)
		}
	}
}

func TestSolveLinearSemantics(t *testing.T) {
	got, ok := SolveLinear([][]float64{{3, 2}, {1, 2}}, []float64{5, 5})
	if !ok || len(got) != 2 || !sameFloat(got[0], 0) || !sameFloat(got[1], 2.5) {
		t.Fatalf("SolveLinear 2x2 = (%v, %v), want [0 2.5]", got, ok)
	}

	if _, ok := SolveLinear([][]float64{{1, 2}, {2, 4}}, []float64{3, 6}); ok {
		t.Fatal("singular matrix reported solvable")
	}

	x, ok := SolveLinear(nil, nil)
	if !ok || x == nil || len(x) != 0 {
		t.Fatalf("SolveLinear(0x0) = (%v, %v), want empty slice and true", x, ok)
	}

	if _, ok := SolveLinear([][]float64{{1, 2}}, []float64{1, 2}); ok {
		t.Fatal("matrix with fewer rows than rhs reported solvable")
	}
	if _, ok := SolveLinear([][]float64{{1}, {2}}, []float64{1, 2}); ok {
		t.Fatal("short rows reported solvable")
	}

	// Partial pivot threshold: 2e-12 is accepted, 1e-13 is singular.
	x, ok = SolveLinear([][]float64{{1, 1}, {1, 1 + 2e-12}}, []float64{2, 2})
	if !ok || math.Abs(x[0]-2) > 1e-6 || math.Abs(x[1]) > 1e-6 {
		t.Fatalf("SolveLinear near-singular accepted case = (%v, %v)", x, ok)
	}
	if _, ok := SolveLinear([][]float64{{1, 1}, {1, 1 + 1e-13}}, []float64{2, 2}); ok {
		t.Fatal("SolveLinear below pivot threshold reported solvable")
	}

	// The inputs are not modified.
	matrix := [][]float64{{2, 1}, {1, 3}}
	rhs := []float64{3, 5}
	matrixCopy := [][]float64{{2, 1}, {1, 3}}
	rhsCopy := []float64{3, 5}
	SolveLinear(matrix, rhs)
	for r := range matrix {
		for c := range matrix[r] {
			if matrix[r][c] != matrixCopy[r][c] {
				t.Fatalf("SolveLinear modified matrix at %d,%d", r, c)
			}
		}
	}
	for i := range rhs {
		if rhs[i] != rhsCopy[i] {
			t.Fatalf("SolveLinear modified rhs at %d", i)
		}
	}
}

func fitInputs() ([]float64, []float64) {
	xs := make([]float64, 40)
	ys := make([]float64, 40)
	for i := range xs {
		xs[i] = float64(100 + 20*i)
		ys[i] = 2.0 + xs[i]/10.0
	}
	return xs, ys
}

func TestLinearFitSemantics(t *testing.T) {
	xs, ys := fitInputs()
	fit, ok := LinearFit(xs, ys, 30)
	if !ok {
		t.Fatal("LinearFit exact relationship reported missing")
	}
	if !sameFloat(fit.FixedSeconds, 2.0) || !sameFloat(fit.PureRate, 10.0) || !sameFloat(fit.R2, 1.0) {
		t.Fatalf("LinearFit exact = %+v", fit)
	}
	if fit.N != 40 || fit.InterceptConstrained || !fit.HasR2 {
		t.Fatalf("LinearFit exact metadata = %+v", fit)
	}

	// Negative intercept: refit through the origin and flag it.
	negative := make([]float64, len(xs))
	for i, x := range xs {
		negative[i] = -5.0 + x/10.0
	}
	fit, ok = LinearFit(xs, negative, 30)
	if !ok {
		t.Fatal("constrained fit reported missing")
	}
	if !fit.InterceptConstrained || !sameFloat(fit.FixedSeconds, 0) || fit.PureRate <= 0 {
		t.Fatalf("constrained fit = %+v", fit)
	}

	// Too few samples, zero x variance, and non-positive slope report false.
	if _, ok := LinearFit(xs[:29], ys[:29], 30); ok {
		t.Fatal("fit below minN reported ok")
	}
	flat := make([]float64, 40)
	for i := range flat {
		flat[i] = 5
	}
	if _, ok := LinearFit(flat, ys, 30); ok {
		t.Fatal("fit with zero x variance reported ok")
	}
	if _, ok := LinearFit(xs, flat, 30); ok {
		t.Fatal("fit with constant y reported ok")
	}
	decreasing := make([]float64, len(xs))
	for i, x := range xs {
		decreasing[i] = 100.0 - x/10.0
	}
	if _, ok := LinearFit(xs, decreasing, 30); ok {
		t.Fatal("fit with negative slope reported ok")
	}

	// Exactly minN samples are accepted; minN+1 rejects them.
	fit, ok = LinearFit(xs[:30], ys[:30], 30)
	if !ok || fit.N != 30 {
		t.Fatalf("fit at minN boundary = (%+v, %v)", fit, ok)
	}
	if _, ok := LinearFit(xs[:30], ys[:30], 31); ok {
		t.Fatal("fit below minN boundary reported ok")
	}
	if _, ok := LinearFit(xs, ys[:39], 30); ok {
		t.Fatal("fit with mismatched lengths reported ok")
	}

	// syy underflows to zero while the slope stays positive: the fit is
	// valid but r2 is absent (Python's r_squared is None).
	tiny := make([]float64, len(xs))
	for i := range xs {
		tiny[i] = 1e-200 + float64(i)*1e-216
	}
	fit, ok = LinearFit(xs, tiny, 30)
	if !ok {
		t.Fatal("underflowed-syy fit reported missing")
	}
	if fit.HasR2 {
		t.Fatalf("underflowed-syy fit reported HasR2: %+v", fit)
	}

	// A noisy fit still exposes a finite r2.
	noisy := make([]float64, len(ys))
	for i, y := range ys {
		noisy[i] = y + float64((i%5)-2)*0.25
	}
	fit, ok = LinearFit(xs, noisy, 30)
	if !ok || !fit.HasR2 || math.IsNaN(fit.R2) {
		t.Fatalf("noisy fit = (%+v, %v)", fit, ok)
	}
}

func TestStandardizeSemantics(t *testing.T) {
	value, covered, ok := Standardize(
		map[string]float64{"a": 10, "b": 20},
		map[string]int{"a": 3, "b": 1},
	)
	if !ok || covered != 4 || value != 12.5 {
		t.Fatalf("Standardize = (%v, %d, %v), want (12.5, 4, true)", value, covered, ok)
	}
	if _, covered, ok := Standardize(map[string]float64{"c": 10}, map[string]int{"a": 3}); ok || covered != 0 {
		t.Fatalf("Standardize missing coverage = (_, %d, %v)", covered, ok)
	}
	if _, _, ok := Standardize(map[string]float64{"a": 5}, map[string]int{"a": 0}); ok {
		t.Fatal("Standardize zero weight reported ok")
	}
	if _, _, ok := Standardize(nil, nil); ok {
		t.Fatal("Standardize empty reported ok")
	}
	value, covered, ok = StandardizeOrdered(
		[]Stratum{{Key: "b", Value: 0.1}, {Key: "a", Value: 0.7}},
		map[string]int{"a": 2, "b": 3},
	)
	if !ok || covered != 5 || math.Abs(value-0.34) > 1e-15 {
		t.Fatalf("StandardizeOrdered = (%v, %d, %v), want (0.34, 5, true)", value, covered, ok)
	}
}

func TestBootstrapMedianCISemantics(t *testing.T) {
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(i + 1)
	}
	first, ok := BootstrapMedianCI(values, 100, 7, 0.05)
	if !ok {
		t.Fatal("BootstrapMedianCI reported missing")
	}
	second, _ := BootstrapMedianCI(values, 100, 7, 0.05)
	if first != second {
		t.Fatalf("BootstrapMedianCI not deterministic: %v vs %v", first, second)
	}
	if !(first[0] <= 50.5 && 50.5 <= first[1]) {
		t.Fatalf("BootstrapMedianCI = %v does not bracket the median 50.5", first)
	}
	if _, ok := BootstrapMedianCI([]float64{1, 2, 3}, 100, 7, 0.05); ok {
		t.Fatal("BootstrapMedianCI with n<5 reported ok")
	}
	if _, ok := BootstrapMedianCI(values, 0, 7, 0.05); ok {
		t.Fatal("BootstrapMedianCI with zero iterations reported ok")
	}

	// Python allows a negative index to wrap; iterations == 1 therefore
	// produces [r1(median), r1(median)] rather than an error.
	single, ok := BootstrapMedianCI([]float64{10, 20, 30, 40, 50}, 1, 42, 0.05)
	if !ok || single[0] != single[1] {
		t.Fatalf("BootstrapMedianCI(iterations=1) = (%v, %v)", single, ok)
	}

	// An alpha far outside (0, 1) would raise IndexError in Python.
	if _, ok := BootstrapMedianCI(values, 10, 42, 5.0); ok {
		t.Fatal("BootstrapMedianCI with out-of-range alpha reported ok")
	}

	// Non-finite observations are filtered before sampling, exactly like
	// Python's math.isfinite filter.
	withSpecials := append(append([]float64(nil), values...), math.NaN(), math.Inf(1), math.Inf(-1))
	a, okA := BootstrapMedianCI(values, 50, 42, 0.05)
	b, okB := BootstrapMedianCI(withSpecials, 50, 42, 0.05)
	if !okA || !okB || a != b {
		t.Fatalf("BootstrapMedianCI special-value filtering: %v vs %v", a, b)
	}
}

func TestStableSeed(t *testing.T) {
	cases := map[string]int64{
		"":                           0,
		"a":                          97,
		"ab":                         97 + 2*98,
		"gpt-4o":                     1781,
		"claude-3-5-sonnet-20241022": 23677,
		"مزوّد/نموذج":                96040,
	}
	for text, want := range cases {
		if got := StableSeed(text); got != want {
			t.Errorf("StableSeed(%q) = %d, want %d", text, got, want)
		}
	}
	if got := StableSeed("any text"); got < 0 || got >= 1_000_000 {
		t.Errorf("StableSeed out of range: %d", got)
	}
}
