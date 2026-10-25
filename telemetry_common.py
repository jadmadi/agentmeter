"""Shared numeric primitives and invariants for the telemetry builders.

Schema v3 contract:
- generatedTokens = visible output + reasoning (where the provider separates
  them). cmd's outputTokens is generated-equivalent.
- freshInputTokens + cacheReadTokens + cacheWriteTokens = inputTotal.
- cacheHitRate = cacheReadTokens / inputTotal, in [0, 1].
- totalSpendUsd = successful-step cost + error/empty ledger cost.
- ratio = expected_duration / actual_duration (fraction); ratioPct = x100.
- paceBase = median(ratio); index = ratio / paceBase * 100.
"""

from __future__ import annotations

import math
import random
import statistics

SCHEMA_VERSION = 3

TIMING_REASONS = [
    "plausible",
    "noTiming",
    "tooShort",
    "tooShortDuration",
    "tooLong",
    "slowRate",
    "fastRate",
]


def percentile(sorted_values: list[float], p: float) -> float | None:
    if not sorted_values:
        return None
    if len(sorted_values) == 1:
        return sorted_values[0]
    pos = (len(sorted_values) - 1) * p / 100.0
    lo = int(pos)
    hi = min(lo + 1, len(sorted_values) - 1)
    return sorted_values[lo] + (sorted_values[hi] - sorted_values[lo]) * (pos - lo)


def median_or_none(values):
    values = [v for v in values if v is not None]
    return statistics.median(values) if values else None


def r1(value):
    return None if value is None else round(value, 1)


def r2(value):
    return None if value is None else round(value, 2)


def safe_div(value, denom):
    return value / denom if denom else None


def solve_linear(matrix, rhs):
    n = len(rhs)
    m = [list(matrix[i]) + [rhs[i]] for i in range(n)]
    for col in range(n):
        pivot = max(range(col, n), key=lambda r: abs(m[r][col]))
        m[col], m[pivot] = m[pivot], m[col]
        if abs(m[col][col]) < 1e-12:
            return None
        for r in range(col + 1, n):
            factor = m[r][col] / m[col][col]
            for k in range(col, n + 1):
                m[r][k] -= factor * m[col][k]
    x = [0.0] * n
    for r in range(n - 1, -1, -1):
        x[r] = (m[r][n] - sum(m[r][c] * x[c] for c in range(r + 1, n))) / m[r][r]
    return x


def linear_fit(xs, ys, min_n: int = 30):
    """Fit y = fixed + x / rate with a non-negative fixed term.

    Returns None when the sample is too small, the slope is not positive, or
    the variance is zero. A negative unconstrained intercept is physically
    impossible for a duration; the fit is refit with fixed=0 and flagged.
    """
    n = len(xs)
    if n < min_n:
        return None
    mx = sum(xs) / n
    my = sum(ys) / n
    sxx = sum((x - mx) ** 2 for x in xs)
    if sxx == 0:
        return None
    sxy = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    slope = sxy / sxx
    intercept = my - slope * mx
    if slope <= 0:
        return None
    constrained = intercept < 0
    if constrained:
        denom = sum(x * x for x in xs)
        if denom == 0:
            return None
        slope = sum(x * y for x, y in zip(xs, ys)) / denom
        intercept = 0.0
        if slope <= 0:
            return None
    pred = [intercept + slope * x for x in xs]
    syy = sum((y - my) ** 2 for y in ys)
    ss_res = sum((y - p) ** 2 for y, p in zip(ys, pred))
    r_squared = 1 - ss_res / syy if syy else None
    return {
        "fixedSeconds": round(intercept, 2),
        "pureRate": round(1 / slope, 1),
        "r2": round(r_squared, 2) if r_squared is not None else None,
        "n": n,
        "interceptConstrained": constrained,
    }


def bootstrap_median_ci(values, iterations: int = 200, seed: int = 42, alpha: float = 0.05):
    """Deterministic percentile bootstrap CI for the median.

    Returns [lo, hi] or None when there are fewer than 5 observations. The
    sample size is preserved; callers must still surface n.
    """
    vals = [v for v in values if v is not None and math.isfinite(v)]
    n = len(vals)
    if n < 5:
        return None
    rng = random.Random(seed)
    medians = []
    for _ in range(iterations):
        sample = [vals[rng.randrange(n)] for _ in range(n)]
        medians.append(statistics.median(sample))
    medians.sort()
    lo = medians[int((alpha / 2) * iterations)]
    hi = medians[min(iterations - 1, int((1 - alpha / 2) * iterations) - 1)]
    return [r1(lo), r1(hi)]


def robust_z(values, value):
    """Modified z-score using the median and MAD (0.6745 scaling)."""
    vals = [v for v in values if v is not None and math.isfinite(v)]
    if len(vals) < 5 or value is None or not math.isfinite(value):
        return None
    med = statistics.median(vals)
    mad = statistics.median([abs(v - med) for v in vals])
    if mad == 0:
        return None
    return 0.6745 * (value - med) / mad


def standardize(stratum_values: dict, global_weights: dict):
    """Direct standardization across strata present for a group.

    `stratum_values` maps a stratum key to a median; `global_weights` maps
    the same keys to the population count. Missing strata are dropped and
    the remaining weights renormalized, so the result is always a weighted
    mean of observed strata. Returns (value, covered_weight) or (None, 0).
    """
    covered = sum(global_weights.get(k, 0) for k in stratum_values)
    if not covered:
        return None, 0
    total = sum(v * global_weights.get(k, 0) for k, v in stratum_values.items())
    return total / covered, covered


def close(a, b, tol: float = 1e-6) -> bool:
    if a is None or b is None:
        return a == b
    if not (math.isfinite(a) and math.isfinite(b)):
        return False
    return abs(a - b) <= max(abs(tol), abs(tol * b))


def validate_common(data: dict) -> list[str]:
    """Fail-closed invariants shared by both telemetry reports."""
    errors: list[str] = []

    def chk(cond, msg):
        if not cond:
            errors.append(msg)

    def nonneg(value, label):
        chk(
            isinstance(value, (int, float)) and math.isfinite(value) and value >= 0,
            f"{label} must be a finite non-negative number, got {value!r}",
        )

    chk(data.get("schemaVersion") == SCHEMA_VERSION,
        f"schemaVersion must be {SCHEMA_VERSION}")
    totals = data.get("totals") or {}
    by_model = data.get("byModel") or []
    cost = data.get("cost") or {}
    cost_by_model = cost.get("byModel") or []
    cache = data.get("cache") or {}
    timed = data.get("timed") or {}
    t_all = timed.get("all") or {}
    sessions = data.get("sessions") or []
    projects = data.get("projects") or []
    tools = data.get("tools") or []
    daily = data.get("daily") or []
    hourly = data.get("hourly") or []

    for field in (
        "steps", "generatedTokens", "freshInputTokens", "inputTotal",
        "cacheReadTokens", "cacheWriteTokens", "costUsd", "totalSpendUsd",
    ):
        nonneg(totals.get(field), f"totals.{field}")

    chk(totals["freshInputTokens"] + totals["cacheReadTokens"] + totals["cacheWriteTokens"]
        == totals["inputTotal"],
        "totals.inputTotal != fresh + cacheRead + cacheWrite")
    chk(totals["steps"] == sum(m.get("steps", 0) for m in by_model),
        "sum(byModel.steps) != totals.steps")
    chk(close(totals["generatedTokens"], sum(m.get("generatedTokens", 0) for m in by_model)),
        "sum(byModel.generatedTokens) != totals.generatedTokens")
    chk(
        close(
            totals["costUsd"],
            sum(m.get("recordedCost", m.get("costUsd", 0)) for m in cost_by_model),
        ),
        "sum(cost.byModel.recordedCost) != totals.costUsd",
    )
    chk(close(cost.get("recorded", 0), totals["costUsd"]),
        "cost.recorded != totals.costUsd")
    chk(close(totals["totalSpendUsd"], totals["costUsd"] + cost.get("errorCostUsd", 0)),
        "totalSpendUsd != costUsd + errorCostUsd")

    chk(0 <= (cache.get("hitRate") or 0) <= 1,
        "cache.hitRate outside [0, 1]")
    chk(cache.get("cacheReadTokens", 0) <= cache.get("inputTokens", 0),
        "cache.cacheReadTokens > cache.inputTokens")

    cost_by_model_map = {c.get("model"): c for c in cost_by_model}
    for m in by_model:
        model = m.get("model")
        c = cost_by_model_map.get(model)
        chk(c is not None, f"missing cost row for model {model!r}")
        if c is None:
            continue
        chk(m.get("inputTotal") == m.get("freshInputTokens", 0) + m.get("cacheReadTokens", 0)
            + m.get("cacheWriteTokens", 0),
            f"byModel[{model}].inputTotal != fresh + cacheRead + cacheWrite")
        chk(close(m.get("cacheHitRate"), c.get("cacheHitRate")),
            f"byModel[{model}].cacheHitRate != cost.byModel cacheHitRate")
        chk(close(m.get("inputTotal"), c.get("inputTotal")),
            f"byModel[{model}].inputTotal != cost.byModel.inputTotal")
        chk(close(m.get("generatedTokens"), c.get("generatedTokens")),
            f"byModel[{model}].generatedTokens != cost.byModel.generatedTokens")

    chk(t_all.get("n", 0) <= totals["steps"],
        "timed.all.n > totals.steps")
    chk(t_all.get("n", 0) == sum(m.get("timedN", 0) for m in by_model),
        "timed.all.n != sum(byModel.timedN)")
    timing = data.get("meta", {}).get("timing") or {}
    if "plausible" in timing:
        chk(t_all.get("n", 0) == timing["plausible"],
            "timed.all.n != meta.timing.plausible")
    if t_all.get("n"):
        nonneg(t_all.get("maxIncluded"), "timed.all.maxIncluded")
        nonneg(t_all.get("maxObserved"), "timed.all.maxObserved")

    chk(close(sum(s.get("steps", 0) for s in sessions), totals["steps"]),
        "sum(session.steps) != totals.steps")
    chk(close(sum(s.get("costUsd", 0) for s in sessions), totals["costUsd"], tol=1e-3),
        "sum(session.costUsd) != totals.costUsd")
    chk(sum(p.get("steps", 0) for p in projects) == totals["steps"],
        "sum(project.steps) != totals.steps")
    labels = [p.get("name") for p in projects]
    chk(len(labels) == len(set(labels)), "project labels are not unique")
    for project in projects:
        chk(
            isinstance(project.get("name"), str) and bool(project.get("name")),
            "project name is missing or empty",
        )

    if tools:
        chk(close(sum(t.get("shareCalls", 0) for t in tools), 1.0, tol=1e-6),
            "sum(tool.shareCalls) != 1")
        for t in tools:
            chk(0 <= (t.get("shareCalls") or 0) <= 1,
                f"tool {t.get('tool')!r} shareCalls outside [0, 1]")
            if t.get("timeShare") is not None:
                chk(0 <= t["timeShare"] <= 1,
                    f"tool {t.get('tool')!r} timeShare outside [0, 1]")
        chk(sum(t.get("timeShare") or 0 for t in tools) <= 1.0 + 1e-6,
            "sum(tool.timeShare) > 1")

    chk(sum(d.get("nAll", 0) for d in daily) == totals["steps"],
        "sum(daily.nAll) != totals.steps")
    chk(close(sum(d.get("generatedTokens", 0) for d in daily), totals["generatedTokens"]),
        "sum(daily.generatedTokens) != totals.generatedTokens")
    chk(close(sum(d.get("costUsd", 0) for d in daily), totals["costUsd"], tol=1e-4),
        "sum(daily.costUsd) != totals.costUsd")
    chk(
        close(
            sum(d.get("totalSpendUsd", 0) for d in daily)
            + cost.get("undatedErrorCostUsd", 0),
            totals["totalSpendUsd"],
            tol=1e-3,
        ),
        "sum(daily.totalSpendUsd) + undated error cost != totals.totalSpendUsd",
    )
    chk(
        close(
            sum(h.get("totalSpendUsd", 0) for h in hourly)
            + cost.get("undatedErrorCostUsd", 0),
            totals["totalSpendUsd"],
            tol=1e-3,
        ),
        "sum(hourly.totalSpendUsd) + undated error cost != totals.totalSpendUsd",
    )
    chk(sum(h.get("nAll", 0) for h in hourly) == totals["steps"],
        "sum(hourly.nAll) != totals.steps")

    for section in ("daily", "hourly"):
        for row in data.get(section) or []:
            for key in ("ratio", "ratioPct", "index"):
                if row.get(key) is not None:
                    chk(math.isfinite(row[key]), f"{section}.{key} is not finite")
                    nonneg(row[key], f"{section}.{key}")

    return errors
