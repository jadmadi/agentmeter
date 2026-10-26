#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Generate CPython-derived parity vectors for the internal/stats Go tests.

This script is test data tooling, not runtime code. It imports the Python
reference implementation at the repository root (telemetry_common.py) and
writes JSON vectors to this directory. Re-run it with the same Python version
used by the audit oracle to refresh the vectors:

    python3 tests/parity/gen_stats_vectors.py

Every float is serialized with float.hex() so the vectors round-trip exactly
through strconv.ParseFloat in Go. "None" results are serialized as null.

Python 3.12+ builtin sum() uses Neumaier compensation for floats; the
go_linear_fit diagnostic below reproduces the Go implementation to confirm
that each committed fit vector is stable under the Go algorithm (the only
allowed difference is Python's libm-backed **2 versus Go's x*x, which is
reported and must never change a rounded output).
"""

from __future__ import annotations

import json
import math
import random
import statistics
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
OUT = ROOT / "internal/stats/testdata"
sys.path.insert(0, str(ROOT))

import telemetry_common as tc  # noqa: E402


# ---------------------------------------------------------------------------
# helpers


def hexf(value) -> str | None:
    """Serialize a float (or None) in a bit-exact, JSON-safe form."""
    if value is None:
        return None
    return float(value).hex()


def write(name: str, payload: dict) -> None:
    path = OUT / name
    path.write_text(
        json.dumps(payload, indent=1, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )
    size = path.stat().st_size
    print(f"wrote {path.name:24s} {size:8d} bytes")


def neumaier(values) -> float:
    """CPython 3.12+ builtin sum() for floats (Neumaier compensation)."""
    hi = 0.0
    lo = 0.0
    for x in values:
        t = hi + x
        if abs(hi) >= abs(x):
            lo += (hi - t) + x
        else:
            lo += (x - t) + hi
        hi = t
    if lo != 0 and math.isfinite(lo):
        return hi + lo
    return hi


def go_linear_fit(xs, ys, min_n):
    """Mirror Go's stats.LinearFit using x*x instead of Python's **2."""
    n = len(xs)
    if n == 0 or n < min_n or len(ys) != n:
        return None
    mx = neumaier(xs) / n
    my = neumaier(ys) / n
    sxx = neumaier((x - mx) * (x - mx) for x in xs)
    if sxx == 0:
        return None
    sxy = neumaier((x - mx) * (y - my) for x, y in zip(xs, ys))
    slope = sxy / sxx
    intercept = my - slope * mx
    if slope <= 0:
        return None
    constrained = intercept < 0
    if constrained:
        denom = neumaier(x * x for x in xs)
        if denom == 0:
            return None
        slope = neumaier(x * y for x, y in zip(xs, ys)) / denom
        intercept = 0.0
        if slope <= 0:
            return None
    predicted = [intercept + slope * x for x in xs]
    syy = neumaier((y - my) * (y - my) for y in ys)
    ss_res = neumaier((y - p) * (y - p) for y, p in zip(ys, predicted))
    r2 = 1 - ss_res / syy if syy else None
    return {
        "fixedSeconds": round(intercept, 2),
        "pureRate": round(1 / slope, 1),
        "r2": round(r2, 2) if r2 is not None else None,
        "n": n,
        "interceptConstrained": constrained,
    }


# ---------------------------------------------------------------------------
# PyRound

round_cases: list[dict] = []


def add_round(x: float, ndigits: int) -> None:
    try:
        want = round(x, ndigits)
    except OverflowError:
        # Go has no error return; these are hand-tested as +-Inf.
        return
    round_cases.append({"x": hexf(x), "ndigits": ndigits, "want": hexf(want)})


ROUND_VALUES = [
    0.0, -0.0, 0.5, -0.5, 1.5, -1.5, 2.5, -2.5, 3.5, -3.5, 4.5, -4.5,
    0.05, 0.15, 0.25, 0.35, 0.45, 0.55, 0.65, 0.75, 0.85, 0.95,
    -0.05, -0.15, -0.25, -0.35, -0.45, -0.55, -0.65, -0.75, -0.85, -0.95,
    1.005, 2.675, -1.005, -2.675, 1.25, 1.35, 1.45, 1.55, -1.25, -1.35,
    0.125, 0.375, 0.625, 0.875, -0.125, -0.375,
    1.2345, 1.23456789, 1234.5678, -1234.5678,
    15.0, 25.0, 45.0, 55.0, -15.0, -25.0, -45.0, -55.0,
    123456789.5, 123456789.25, -123456789.5,
    float(2**52), float(2**52 + 1), float(2**53), float(2**53 + 2), float(2**70),
    1e308, 9.9e307, 1.7976931348623157e308, -1e308,
    5e-324, 1e-323, 2.5e-323, 1e-320, 5e-321, 1e-300,
    0.0001, 0.00001, 0.000001, 1e-7, 1e-8, 1e-9, 1e-12,
    999999.999999, 0.999999999999, 1.0000000000000002,
]
ROUND_SMALL_DIGITS = [-6, -4, -2, -1, 0, 1, 2, 4, 6]
ROUND_EXTREME_DIGITS = [-400, -309, -308, -12, 8, 10, 15, 17, 20, 300, 307, 308, 310, 320, 322, 323, 324, 400]

for value in ROUND_VALUES:
    for nd in ROUND_SMALL_DIGITS:
        add_round(value, nd)
    for nd in ROUND_EXTREME_DIGITS:
        add_round(value, nd)

rng_round = random.Random(20240517)
for _ in range(400):
    kind = rng_round.randrange(4)
    if kind == 0:
        x = rng_round.uniform(-1.0, 1.0) * 10.0 ** rng_round.randint(-300, 300)
    elif kind == 1:
        x = rng_round.uniform(-1e6, 1e6)
    elif kind == 2:
        x = float(rng_round.randint(-10**12, 10**12))
    else:
        x = math.ldexp(rng_round.uniform(-1.0, 1.0), rng_round.randint(-1074, 1022))
    if not math.isfinite(x):
        continue
    add_round(x, rng_round.choice([-12, -8, -6, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 8, 10, 15, 17, 20]))

write("pyround.json", {"cases": round_cases})


# ---------------------------------------------------------------------------
# Fsum and Sum

fsum_cases: list[dict] = []
sum_cases: list[dict] = []


def add_fsum(values: list[float]) -> None:
    try:
        want = math.fsum(values)
    except (OverflowError, ValueError):
        return
    fsum_cases.append({"values": [hexf(v) for v in values], "want": hexf(want)})


def add_sum(values: list[float]) -> None:
    sum_cases.append({"values": [hexf(v) for v in values], "want": hexf(sum(values))})


FSUM_EXPLICIT = [
    [],
    [0.0, -0.0],
    [1e-16, 1.0, 1e16],
    [1e16, 1.0, 1e-16],
    [0.1] * 10,
    [0.1] * 100,
    [1e100, 1.0, -1e100],
    [1e308, -1e308, 1e308],
    [1e308, -1e308, 1e-308],
    [1e308, -1e308, -1e308, 1e308],
    [5e-324] * 7,
    [1.7976931348623157e308, -1.7976931348623157e308],
    [float(2**53), 1.0, 1.0, 1.0],
    [float(2**53), -1.0, 1.0, 1.0],
    [1e300, 1e300, -1e300, -1e300, 1.0],
    [math.pi, math.e, -math.pi, -math.e, 1e-17],
    [1.0, -1.0, 1e-300, -1e-300, 1e300, -1e300],
    [float("inf"), 1.0],
    [float("-inf"), 1.0],
    [float("nan")],
    [float("nan"), 1.0],
    [1.0, float("nan"), 2.0],
]
for values in FSUM_EXPLICIT:
    add_fsum(values)

rng_sum = random.Random(777001)
for _ in range(120):
    nvals = rng_sum.randint(0, 40)
    values = []
    for _ in range(nvals):
        exponent = rng_sum.randint(-300, 300)
        values.append(rng_sum.uniform(-1.0, 1.0) * 10.0 ** exponent)
    add_fsum(values)
    add_sum(values)

# Near-cancellation and repeated-fraction patterns are where compensated
# summation diverges from naive left-to-right addition.
for _ in range(30):
    base = rng_sum.uniform(-1e6, 1e6)
    values = [base, -base, rng_sum.uniform(-1e-9, 1e-9), rng_sum.uniform(-1, 1)]
    rng_sum.shuffle(values)
    add_fsum(values)
    add_sum(values)

SUM_EXPLICIT = [
    [],
    [0.0, -0.0],
    [1e16, 1.0, -1e16],
    [1e100, 1.0, -1e100],
    [0.1] * 10,
    [1e16, 1.0, 1e-16],
    [1.0, 1e-16, 1e16],
    [float(2**53), 1.0, 1.0, 1.0],
    [1e308, -1e308, 1e-308],
    [5e-324] * 7,
    [float("inf"), 1.0],
    [float("nan"), 1.0],
]
for values in SUM_EXPLICIT:
    add_sum(values)

write("fsum.json", {"cases": fsum_cases})
write("sum.json", {"cases": sum_cases})


# ---------------------------------------------------------------------------
# Percentile and median

percentile_cases: list[dict] = []
median_cases: list[dict] = []


def add_percentile(values: list[float], p: float) -> None:
    percentile_cases.append(
        {"values": [hexf(v) for v in values], "p": float(p), "want": hexf(tc.percentile(values, p))}
    )


def add_median(values: list[float]) -> None:
    want = statistics.median(values) if values else None
    median_cases.append({"values": [hexf(v) for v in values], "want": hexf(want)})


for values in ([], [7.0], [1.0, 2.0, 3.0, 4.0], [1.0], [1.0, 2.0], [5.0, 1.0, 3.0]):
    for p in (0, 25, 50, 75, 90, 100):
        add_percentile(values, p)

rng_pct = random.Random(31337)
for _ in range(30):
    nvals = rng_pct.randint(1, 50)
    values = sorted(rng_pct.uniform(-1e3, 1e3) for _ in range(nvals))
    p = rng_pct.choice([0.0, 1.0, 5.0, 10.0, 25.0, 33.3, 50.0, 66.7, 75.0, 90.0, 95.0, 99.0, 100.0])
    add_percentile(values, p)

for values in (
    [],
    [1.0],
    [1.0, 2.0],
    [1.0, 2.0, 3.0],
    [1.0, 2.0, 3.0, 4.0],
    [5.0, 1.0, 3.0],
    [1e308, 1e308],
    [-1e308, 1e308],
    [5e-324, 1e-323, 1e-320],
):
    add_median(values)

for _ in range(40):
    nvals = rng_pct.randint(1, 40)
    values = [rng_pct.uniform(-1e6, 1e6) * 10.0 ** rng_pct.randint(-6, 6) for _ in range(nvals)]
    add_median(values)

write("percentile.json", {"cases": percentile_cases})
write("median.json", {"cases": median_cases})


# ---------------------------------------------------------------------------
# Robust z

robust_cases: list[dict] = []


def add_robust(values: list[float], value: float) -> None:
    want = tc.robust_z(values, value)
    robust_cases.append(
        {"values": [hexf(v) for v in values], "value": hexf(value), "want": hexf(want)}
    )


for values, value in (
    ([1.0, 2.0, 3.0, 4.0, 5.0], 5.0),
    ([1.0, 2.0, 3.0, 4.0], 4.0),
    ([1.0, 1.0, 1.0, 1.0, 1.0], 10.0),
    ([1.0, 1.0, 1.0, 1.0, 1.0, 2.0], 2.0),
    (list(map(float, range(1, 21))), 20.0),
    (list(map(float, range(1, 21))), 10.0),
):
    add_robust(values, value)

rng_z = random.Random(424242)
for _ in range(50):
    nvals = rng_z.randint(0, 30)
    values = [rng_z.uniform(-100, 100) for _ in range(nvals)]
    value = rng_z.uniform(-150, 150)
    add_robust(values, value)

write("robust_z.json", {"cases": robust_cases})


# ---------------------------------------------------------------------------
# Linear fit

fit_cases: list[dict] = []
fit_mismatches: list[dict] = []


def add_fit(xs, ys, min_n: int = 30) -> None:
    xs = [float(x) for x in xs]
    ys = [float(y) for y in ys]
    native = tc.linear_fit(xs, ys, min_n=min_n)
    go_style = go_linear_fit(xs, ys, min_n)
    if native != go_style:
        fit_mismatches.append(
            {"native": native, "go": go_style, "n": len(xs), "min_n": min_n}
        )
    if native is None:
        want = None
    else:
        want = {
            "fixedSeconds": hexf(native["fixedSeconds"]),
            "pureRate": hexf(native["pureRate"]),
            "r2": hexf(native["r2"]),
            "n": native["n"],
            "interceptConstrained": bool(native["interceptConstrained"]),
        }
    fit_cases.append(
        {
            "xs": [hexf(x) for x in xs],
            "ys": [hexf(y) for y in ys],
            "minN": min_n,
            "want": want,
        }
    )


xs40 = [float(100 + 20 * i) for i in range(40)]
add_fit(xs40, [2.0 + x / 10.0 for x in xs40])
add_fit(xs40, [-5.0 + x / 10.0 for x in xs40])
add_fit(xs40, [5.0 + x / 10.0 for x in xs40])
add_fit([1.0, 2.0, 3.0], [1.0, 2.0, 3.0])
add_fit([5.0] * 40, [1.0] * 40)
add_fit(xs40, [5.0] * 40)
add_fit(xs40, [100.0 - x / 10.0 for x in xs40])
add_fit(xs40, [1e-200 + i * 1e-216 for i in range(40)])
add_fit([0.0] * 40, [1.0] * 40)
add_fit([0.0, 10.0, 20.0, 30.0, 40.0] * 6, [-1.0 + x / 5.0 for x in ([0.0, 10.0, 20.0, 30.0, 40.0] * 6)])
add_fit(xs40[:30], [1.0 + x / 7.0 for x in xs40[:30]], min_n=30)
add_fit(xs40[:29], [1.0 + x / 7.0 for x in xs40[:29]], min_n=30)
add_fit(xs40[:30], [1.0 + x / 7.0 for x in xs40[:30]], min_n=31)
add_fit(xs40, [1.0 + x / 7.0 for x in xs40], min_n=1)

rng_fit = random.Random(20250101)
for trial in range(40):
    nvals = rng_fit.choice([5, 10, 29, 30, 31, 50, 120, 400])
    xs = [float(rng_fit.randint(20, 200000)) for _ in range(nvals)]
    mode = trial % 5
    if mode == 0:
        ys = [2.0 + x / 8.0 + rng_fit.gauss(0.0, 0.3) for x in xs]
    elif mode == 1:
        ys = [-3.0 + x / 12.0 + rng_fit.gauss(0.0, 1.0) for x in xs]
    elif mode == 2:
        ys = [rng_fit.uniform(0.5, 300.0) for _ in xs]
    elif mode == 3:
        ys = [5.0 + x / 40.0 + rng_fit.gauss(0.0, 5.0) for x in xs]
    else:
        ys = [0.2 + x / 3.0 + rng_fit.gauss(0.0, 0.01) for x in xs]
    add_fit(xs, ys, min_n=rng_fit.choice([5, 30, 30, 30]))

for trial in range(10):
    nvals = rng_fit.choice([30, 40, 60])
    xs = [float(rng_fit.randint(-5000, 5000)) for _ in range(nvals)]
    ys = [-2.0 + x / 50.0 + rng_fit.gauss(0.0, 0.05) for x in xs]
    add_fit(xs, ys)

write("linear_fit.json", {"cases": fit_cases})
print(f"linear_fit native-vs-go mismatches: {len(fit_mismatches)} / {len(fit_cases)}")
for mismatch in fit_mismatches[:5]:
    print("  ", mismatch)


# ---------------------------------------------------------------------------
# SolveLinear

solve_cases: list[dict] = []


def add_solve(matrix, rhs) -> None:
    want = tc.solve_linear(matrix, rhs)
    solve_cases.append(
        {
            "matrix": [[hexf(v) for v in row] for row in matrix],
            "rhs": [hexf(v) for v in rhs],
            "want": None if want is None else [hexf(v) for v in want],
        }
    )


add_solve([], [])
add_solve([[2.0]], [4.0])
add_solve([[3.0, 2.0], [1.0, 2.0]], [5.0, 5.0])
add_solve([[1.0, 2.0], [2.0, 4.0]], [3.0, 6.0])
add_solve([[1.0, 1.0], [1.0, 1.0 + 1e-13]], [2.0, 2.0])
add_solve([[1.0, 1.0], [1.0, 1.0 + 2e-12]], [2.0, 2.0])
add_solve([[0.0, 1.0], [1.0, 0.0]], [2.0, 3.0])
add_solve([[1.0, 2.0, 3.0], [4.0, 5.0, 6.0], [7.0, 8.0, 10.0]], [6.0, 15.0, 25.0])
add_solve([[2.0, 0.0, 0.0], [0.0, 3.0, 0.0], [0.0, 0.0, 4.0]], [2.0, 6.0, 8.0])
add_solve([[1.0, 2.0], [3.0, 4.0], [5.0, 6.0]], [5.0, 11.0])
add_solve([[1.0, 2.0], [2.0, 4.0], [3.0, 6.0]], [3.0, 6.0])

rng_solve = random.Random(5150)
for size in (2, 3, 4, 5):
    for _ in range(3):
        matrix = [[rng_solve.uniform(-10, 10) for _ in range(size)] for _ in range(size)]
        for i in range(size):
            matrix[i][i] += 100.0
        rhs = [rng_solve.uniform(-10, 10) for _ in range(size)]
        add_solve(matrix, rhs)

write("solve_linear.json", {"cases": solve_cases})


# ---------------------------------------------------------------------------
# Standardize

std_cases: list[dict] = []


def add_std(keys: list[str], strata: dict[str, float], weights: dict[str, int]) -> None:
    ordered = {key: strata[key] for key in keys}
    sorted_dict = {key: strata[key] for key in sorted(strata)}
    want_ordered, covered = tc.standardize(ordered, weights)
    want_sorted, covered_sorted = tc.standardize(sorted_dict, weights)
    assert covered == covered_sorted
    std_cases.append(
        {
            "keys": keys,
            "strata": {key: hexf(value) for key, value in strata.items()},
            "weights": {key: int(value) for key, value in weights.items()},
            "want": hexf(want_ordered),
            "wantSorted": hexf(want_sorted),
            "covered": int(covered),
        }
    )


add_std(["a", "b"], {"a": 10.0, "b": 20.0}, {"a": 3, "b": 1})
add_std(["c"], {"c": 10.0}, {"a": 3})
add_std(["a"], {"a": 5.0}, {"a": 0})
add_std([], {}, {"a": 3})
add_std(["a", "b", "c"], {"a": 1.0, "b": 2.0, "c": 3.0}, {"a": 2, "b": 0, "c": 5})
add_std(["b", "a", "c"], {"b": 12.5, "a": 9.25, "c": 33.75}, {"a": 11, "b": 3, "c": 7})
add_std(["b", "a"], {"b": 0.1, "a": 0.7}, {"a": 2, "b": 3})
add_std(["a", "b", "c", "d"], {"a": 5.5, "b": 6.5, "c": 7.5, "d": 8.5}, {"a": 1, "b": 1, "c": 1, "d": 1})
add_std(["x"], {"x": -3.25}, {"x": 9})
add_std(["a", "b"], {"a": 1e-9, "b": 1e9}, {"a": 700, "b": 13})

write("standardize.json", {"cases": std_cases})


# ---------------------------------------------------------------------------
# Bootstrap median CI

boot_cases: list[dict] = []


def add_boot(values, iterations: int, seed: int, alpha: float) -> None:
    want = tc.bootstrap_median_ci(values, iterations=iterations, seed=seed, alpha=alpha)
    boot_cases.append(
        {
            "values": [hexf(v) for v in values],
            "iterations": iterations,
            "seed": seed,
            "alpha": alpha,
            "want": None if want is None else [hexf(want[0]), hexf(want[1])],
        }
    )


add_boot(list(map(float, range(1, 101))), 100, 7, 0.05)
add_boot(list(map(float, range(1, 101))), 200, 42, 0.05)
add_boot([1.0, 2.0, 3.0], 100, 7, 0.05)
add_boot([1.0, 2.0, 3.0, 4.0], 200, 42, 0.05)
add_boot([10.0, 20.0, 30.0, 40.0, 50.0], 200, 0, 0.05)
add_boot([10.0, 20.0, 30.0, 40.0, 50.0], 200, 1, 0.05)
add_boot([10.0, 20.0, 30.0, 40.0, 50.0], 200, 42, 0.10)
add_boot([10.0, 20.0, 30.0, 40.0, 50.0], 200, 42, 0.01)

rng_boot = random.Random(8675309)
for seed in (0, 1, 7, 42, 123, 999999):
    for nvals in (5, 6, 7, 10, 30, 50, 120):
        values = [round(rng_boot.uniform(1.0, 400.0), 3) for _ in range(nvals)]
        add_boot(values, 200, seed, 0.05)

add_boot([1.0, 2.0, 3.0, 4.0, 5.0], 50, 42, 0.05)
add_boot([1.0, 2.0, 3.0, 4.0, 5.0], 1, 42, 0.05)
add_boot([1.0, 2.0, 3.0, 4.0, 5.0], 2, 42, 0.05)

write("bootstrap.json", {"cases": boot_cases})


# ---------------------------------------------------------------------------
# Close and SafeDiv

close_cases: list[dict] = []
div_cases: list[dict] = []


def add_close(a: float, b: float, tol: float) -> None:
    close_cases.append(
        {"a": hexf(a), "b": hexf(b), "tol": hexf(tol), "want": bool(tc.close(a, b, tol))}
    )


for a, b, tol in (
    (1.0, 1.0, 1e-6),
    (1.0, 1.0000001, 1e-6),
    (1.0, 1.001, 1e-6),
    (0.0, 0.0, 1e-6),
    (0.0, 1e-7, 1e-6),
    (100.0, 100.00002, 1e-6),
    (100.0, 100.02, 1e-6),
    (1.0, 1.0 + 1e-3, 1e-3),
    (1.0, 1.0 + 1.1e-3, 1e-3),
    (float("inf"), float("inf"), 1e-6),
    (float("inf"), 1.0, 1e-6),
    (float("-inf"), float("inf"), 1e-6),
    (float("nan"), float("nan"), 1e-6),
    (float("nan"), 1.0, 1e-6),
    (1e308, 1e308, 1e-6),
    (1e308, -1e308, 1e-6),
):
    add_close(a, b, tol)

rng_close = random.Random(31415)
for _ in range(60):
    tol = rng_close.choice([0.0, 1e-9, 1e-6, 1e-3, 1e-2, -1e-6])
    a = rng_close.uniform(-1e3, 1e3)
    b = a * (1.0 + rng_close.choice([0.0, 1e-9, 1e-7, 1e-5, 1e-3, -1e-3]))
    add_close(a, b, tol)


def add_div(value: float, denom: float) -> None:
    want = tc.safe_div(value, denom)
    div_cases.append({"v": hexf(value), "d": hexf(denom), "want": hexf(want)})


for value, denom in (
    (1.0, 2.0),
    (1.0, 0.0),
    (1.0, -0.0),
    (0.0, 0.0),
    (-1.0, 4.0),
    (float("nan"), 2.0),
    (1.0, float("nan")),
    (1.0, float("inf")),
    (float("inf"), 2.0),
    (1e308, 1e-308),
):
    add_div(value, denom)

write("close.json", {"cases": close_cases})
write("safediv.json", {"cases": div_cases})


# ---------------------------------------------------------------------------
# StableSeed

seed_cases = []
for text in (
    "",
    "a",
    "ab",
    "abc",
    "claude-3-5-sonnet-20241022",
    "claude-sonnet-4-20250514",
    "gpt-4o",
    "gpt-5-codex",
    "openai/gpt-4o",
    "مزوّد/نموذج",
    "🚀model",
    "a/b c\td\ne",
    "Ångström",
    "e\u0301",  # e + combining acute
):
    want = sum((index + 1) * ord(ch) for index, ch in enumerate(text)) % 1_000_000
    seed_cases.append({"text": text, "want": want})

write("stable_seed.json", {"cases": seed_cases})


# ---------------------------------------------------------------------------
# pyrandom: MT19937, getrandbits, randrange, random()

RANDOM_OPS = []
for _cycle in range(2):
    for k in (32, 1, 0, 33, 64, 5, 31, 53, 7, 2, 63, 16):
        RANDOM_OPS.append(("bits", k, None))
    for below_n in (1, 2, 5, 7, 12, 100, 1000, 2**31, 2**32, 2**53, 2**63, 2**64 - 1):
        RANDOM_OPS.append(("below", None, below_n))
    for _ in range(3):
        RANDOM_OPS.append(("float", None, None))

RANDOM_SEEDS = [0, 1, 2, 7, 42, 123, 999999, -1, -42, 2**31, 2**32, 2**40 + 123, 2**63 - 1, -(2**63)]

random_cases = []
for seed in RANDOM_SEEDS:
    rng = random.Random(seed)
    ops = []
    for kind, k, below_n in RANDOM_OPS:
        if kind == "bits":
            want = rng.getrandbits(k)
            ops.append({"kind": "bits", "k": k, "want": str(want)})
        elif kind == "below":
            want = rng.randrange(below_n)
            ops.append({"kind": "below", "n": str(below_n), "want": str(want)})
        else:
            want = rng.random()
            ops.append({"kind": "float", "want": hexf(want)})
    random_cases.append({"seed": seed, "ops": ops})

write("pyrandom.json", {"cases": random_cases})


print("done")
