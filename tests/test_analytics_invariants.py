# SPDX-License-Identifier: AGPL-3.0-only
import copy
import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from telemetry_common import (
    SCHEMA_VERSION,
    bootstrap_median_ci,
    linear_fit,
    robust_z,
    standardize,
    validate_common,
)

def test_linear_fit_exact_relationship():
    xs = [100 + 20 * i for i in range(40)]
    ys = [2.0 + x / 10.0 for x in xs]
    fit = linear_fit(xs, ys, min_n=30)
    assert fit is not None
    assert fit["fixedSeconds"] == 2.0
    assert fit["pureRate"] == 10.0
    assert fit["r2"] == 1.0
    assert fit["interceptConstrained"] is False


def test_linear_fit_constrains_negative_intercept():
    xs = [100 + 20 * i for i in range(40)]
    ys = [-5.0 + x / 10.0 for x in xs]
    fit = linear_fit(xs, ys, min_n=30)
    assert fit is not None
    assert fit["interceptConstrained"] is True
    assert fit["fixedSeconds"] == 0.0
    assert fit["pureRate"] > 0
    assert fit["r2"] is not None


def test_linear_fit_rejects_small_or_flat_samples():
    assert linear_fit([1, 2, 3], [1, 2, 3], min_n=30) is None
    assert linear_fit([5] * 40, [1.0] * 40, min_n=30) is None


def test_bootstrap_median_ci_is_deterministic_and_brackets_median():
    values = list(range(1, 101))
    first = bootstrap_median_ci(values, iterations=100, seed=7)
    second = bootstrap_median_ci(values, iterations=100, seed=7)
    assert first == second
    assert first[0] <= 50.5 <= first[1]
    assert bootstrap_median_ci([1, 2, 3], iterations=100) is None


def test_robust_z_flags_outlier():
    values = list(range(1, 21))
    assert robust_z(values, 20) is not None
    assert abs(robust_z(values, 10)) < 0.1
    assert robust_z([1, 1, 1, 1, 1], 10) is None


def test_standardize_direct_weights():
    value, covered = standardize({"a": 10, "b": 20}, {"a": 3, "b": 1})
    assert value == 12.5
    assert covered == 4
    value, covered = standardize({"c": 10}, {"a": 3})
    assert value is None
    assert covered == 0


def good_report():
    return {
        "schemaVersion": SCHEMA_VERSION,
        "meta": {"timing": {"plausible": 2}},
        "totals": {
            "steps": 2,
            "generatedTokens": 10,
            "freshInputTokens": 1,
            "inputTotal": 3,
            "cacheReadTokens": 2,
            "cacheWriteTokens": 0,
            "costUsd": 1.0,
            "totalSpendUsd": 1.5,
        },
        "byModel": [
            {
                "model": "m",
                "steps": 2,
                "generatedTokens": 10,
                "freshInputTokens": 1,
                "inputTotal": 3,
                "cacheReadTokens": 2,
                "cacheWriteTokens": 0,
                "cacheHitRate": 0.6667,
                "timedN": 2,
            }
        ],
        "cost": {
            "recorded": 1.0,
            "errorCostUsd": 0.5,
            "totalSpendUsd": 1.5,
            "byModel": [
                {
                    "model": "m",
                    "recordedCost": 1.0,
                    "generatedTokens": 10,
                    "inputTotal": 3,
                    "cacheHitRate": 0.6667,
                }
            ],
        },
        "cache": {"inputTokens": 3, "cacheReadTokens": 2, "hitRate": 0.6667},
        "timed": {"all": {"n": 2, "maxIncluded": 10, "maxObserved": 12}},
        "sessions": [{"steps": 2, "costUsd": 1.0}],
        "projects": [{"name": "p", "steps": 2}],
        "tools": [{"tool": "t", "shareCalls": 1.0, "timeShare": 0.5}],
        "daily": [
            {
                "nAll": 2,
                "generatedTokens": 10,
                "costUsd": 1.0,
                "totalSpendUsd": 1.5,
                "ratio": 1.0,
                "ratioPct": 100.0,
                "index": 100.0,
            }
        ],
        "hourly": [{"nAll": 2, "totalSpendUsd": 1.5}],
    }


def test_validate_common_accepts_good_report():
    assert validate_common(good_report()) == []


@pytest.mark.parametrize(
    "mutate",
    [
        lambda d: d["totals"].__setitem__("totalSpendUsd", 2.0),
        lambda d: d["byModel"][0].__setitem__("cacheHitRate", 0.9),
        lambda d: d["cache"].__setitem__("hitRate", 1.5),
        lambda d: d["tools"][0].__setitem__("shareCalls", 0.5),
        lambda d: d["projects"][0].__setitem__("name", None),
        lambda d: d["daily"][0].__setitem__("generatedTokens", 9),
    ],
)
def test_validate_common_rejects_broken_report(mutate):
    report = copy.deepcopy(good_report())
    mutate(report)
    assert validate_common(report)


@pytest.mark.parametrize(
    "path",
    [
        ROOT / "command-code-telemetry" / "data" / "telemetry-data.json",
        ROOT / "opencode-telemetry" / "data" / "telemetry-data.json",
    ],
)
def test_generated_reports_satisfy_invariants(path):
    if not path.exists():
        pytest.skip("generated report not present")
    data = json.loads(path.read_text(encoding="utf-8"))
    assert validate_common(data) == []
