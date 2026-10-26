// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"math"
	"testing"
)

// Vectors below were generated with CPython 3.15 (TZ=UTC) from
// telemetry_common: linear_fit, robust_z, standardize, bootstrap_median_ci,
// and the builtin round(). They pin the exact operation order and rounding.

func TestPyRoundVectors(t *testing.T) {
	cases := []struct {
		value    float64
		digits   int
		expected float64
	}{
		{2.675, 2, 2.67},
		{0.5, 0, 0},
		{1.5, 0, 2},
		{2.5, 0, 2},
		{-0.5, 0, 0},
		{1.9861805, 6, 1.98618},
		{47.25, 1, 47.2},
		{1.23456789e-7, 6, 0},
		{1234567.891234, 4, 1234567.8912},
	}
	for _, testCase := range cases {
		if got := pyRound(testCase.value, testCase.digits); got != testCase.expected {
			t.Errorf("pyRound(%v, %d) = %v, want %v", testCase.value, testCase.digits, got, testCase.expected)
		}
	}
}

func TestPySumIsCompensated(t *testing.T) {
	// CPython 3.12+ sum() is Neumaier-compensated: 1e16 + 1 - 1e16 == 1.0,
	// while a naive left-to-right loop returns 0.
	values := []float64{1e16, 1, -1e16}
	if got := pySum(values); got != 1.0 {
		t.Errorf("pySum = %v, want 1.0", got)
	}
	naive := 0.0
	for _, value := range values {
		naive += value
	}
	if naive != 0 {
		t.Fatalf("naive sum = %v, expected the known drift", naive)
	}
	// Long series of cents must stay exact enough to round identically.
	long := make([]float64, 10000)
	for index := range long {
		long[index] = 0.01
	}
	if got, want := pySum(long), 100.0; got != want {
		t.Errorf("pySum(0.01 x 10000) = %v, want %v", got, want)
	}
}

func TestLinearFitVectors(t *testing.T) {
	xs := make([]float64, 40)
	for index := range xs {
		xs[index] = float64(100 + 20*index)
	}
	exact := make([]float64, 40)
	constrained := make([]float64, 40)
	for index, x := range xs {
		exact[index] = 2.0 + x/10.0
		constrained[index] = -5.0 + x/10.0
	}
	fit := linearFit(xs, exact, 30)
	if fit == nil {
		t.Fatal("exact fit is nil")
	}
	if fit.fixedSeconds != 2.0 || fit.pureRate != 10.0 || fit.r2 == nil || *fit.r2 != 1.0 || fit.interceptConstrained {
		t.Errorf("exact fit = %+v", *fit)
	}
	neg := linearFit(xs, constrained, 30)
	if neg == nil {
		t.Fatal("constrained fit is nil")
	}
	if !neg.interceptConstrained || neg.fixedSeconds != 0 || neg.pureRate <= 0 {
		t.Errorf("constrained fit = %+v", *neg)
	}
	// A noisy sample generated with Python.
	noisy := make([]float64, 40)
	for index, x := range xs {
		noisy[index] = 1.0 + float64(index%7) + x/12.5
	}
	noisyFit := linearFit(xs, noisy, 30)
	if noisyFit == nil {
		t.Fatal("noisy fit is nil")
	}
	if noisyFit.fixedSeconds != 3.59 || noisyFit.pureRate != 12.4 {
		t.Errorf("noisy fit = %+v, want fixed=3.59 rate=12.4", *noisyFit)
	}
	if noisyFit.r2 == nil || *noisyFit.r2 != 0.99 {
		t.Errorf("noisy r2 = %v, want 0.99", noisyFit.r2)
	}
	if linearFit([]float64{1, 2, 3}, []float64{1, 2, 3}, 30) != nil {
		t.Error("small sample must be nil")
	}
	flat := make([]float64, 40)
	for index := range flat {
		flat[index] = 1.0
	}
	if linearFit(make([]float64, 40), flat, 30) != nil {
		t.Error("zero-variance x must be nil")
	}
}

func TestBootstrapMedianCIVectors(t *testing.T) {
	cases := []struct {
		values   []float64
		seed     int64
		expected []float64
	}{
		{[]float64{1, 2, 3, 4, 5}, 7, []float64{1, 5}},
		{[]float64{10.5, 20.25, 30.125, 40, 50.5, 60.75}, 42, []float64{15.4, 50.5}},
		{[]float64{3, 3, 3, 3, 3, 3, 3, 3}, 0, []float64{3, 3}},
		{[]float64{1, 2, 3}, 42, nil},
	}
	for _, testCase := range cases {
		got := bootstrapMedianCI(testCase.values, 200, testCase.seed, 0.05)
		if testCase.expected == nil {
			if got != nil {
				t.Errorf("bootstrap(%v) = %v, want nil", testCase.values, got)
			}
			continue
		}
		if len(got) != 2 || got[0] != testCase.expected[0] || got[1] != testCase.expected[1] {
			t.Errorf("bootstrap(%v, seed %d) = %v, want %v", testCase.values, testCase.seed, got, testCase.expected)
		}
	}
}

func TestRobustZVectors(t *testing.T) {
	values := make([]float64, 20)
	for index := range values {
		values[index] = float64(index + 1)
	}
	if z, ok := robustZ(values, 20, true); !ok || math.Abs(z-1.28155) > 1e-12 {
		t.Errorf("robustZ(20) = %v (%v), want 1.28155", z, ok)
	}
	if z, ok := robustZ(values, 10, true); !ok || math.Abs(z-(-0.06745)) > 1e-12 {
		t.Errorf("robustZ(10) = %v (%v), want -0.06745", z, ok)
	}
	if _, ok := robustZ([]float64{1, 1, 1, 1, 1}, 10, true); ok {
		t.Error("zero MAD must be unavailable")
	}
	spike := []float64{5, 7, 9, 11, 13, 100}
	if z, ok := robustZ(spike, 100, true); !ok || math.Abs(z-20.235) > 1e-12 {
		t.Errorf("robustZ(spike) = %v, want 20.235", z)
	}
	if _, ok := robustZ([]float64{1, 2, 3, 4}, 2, true); ok {
		t.Error("fewer than five values must be unavailable")
	}
}

func TestStandardizeVectors(t *testing.T) {
	value, covered := standardize(
		map[string]float64{"a": 10, "b": 20},
		[]string{"a", "b"},
		map[string]int64{"a": 3, "b": 1},
	)
	if value != 12.5 || covered != 4 {
		t.Errorf("standardize = %v/%v, want 12.5/4", value, covered)
	}
	value, covered = standardize(map[string]float64{"c": 10}, []string{"c"}, map[string]int64{"a": 3})
	if covered != 0 || value != 0 {
		t.Errorf("uncovered stratum = %v/%v", value, covered)
	}
	value, covered = standardize(
		map[string]float64{"x": 1.5, "y": 2.5, "z": 3.5},
		[]string{"x", "y", "z"},
		map[string]int64{"x": 2, "y": 2, "z": 6},
	)
	if math.Abs(value-2.9) > 1e-12 || covered != 10 {
		t.Errorf("standardize = %v/%v, want 2.9/10", value, covered)
	}
}

func TestSolveLinear(t *testing.T) {
	// 2x + y = 5, x + 3y = 10 -> x=1, y=3
	solution := solveLinear([][]float64{{2, 1}, {1, 3}}, []float64{5, 10})
	if solution == nil || math.Abs(solution[0]-1) > 1e-12 || math.Abs(solution[1]-3) > 1e-12 {
		t.Fatalf("solveLinear = %v", solution)
	}
	if solveLinear([][]float64{{0, 0}, {0, 0}}, []float64{1, 1}) != nil {
		t.Error("singular matrix must return nil")
	}
}

func TestProjectLabels(t *testing.T) {
	labels := projectLabelsForPaths([]string{
		"(unknown)", "/another/work/app", "/other/work/app", "/work/alpha",
		"/work/beta/app", "/work/gamma/app", "/work/trailing/",
	}, false)
	want := map[string]string{
		"(unknown)":         "(unknown)",
		"/another/work/app": "app · work",
		"/other/work/app":   "/other/work/app", // label collision falls back to the path
		"/work/alpha":       "alpha",
		"/work/beta/app":    "app · beta",
		"/work/gamma/app":   "app · gamma",
		"/work/trailing/":   "trailing",
	}
	for path, expected := range want {
		if got := labels[path]; got != expected {
			t.Errorf("label(%q) = %q, want %q", path, got, expected)
		}
	}
}
