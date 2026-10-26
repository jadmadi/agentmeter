# SPDX-License-Identifier: AGPL-3.0-only
import importlib.util
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]


def load_helpers(package: str):
    path = ROOT / package / "py" / "telemetry.py"
    spec = importlib.util.spec_from_file_location(package.replace("-", "_"), path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture(scope="module", params=["command-code-telemetry", "opencode-telemetry"])
def helpers(request):
    return load_helpers(request.param)


def test_percentile_empty(helpers):
    assert helpers.percentile([], 50) is None


def test_percentile_single_value(helpers):
    assert helpers.percentile([7.0], 90) == 7.0


@pytest.mark.parametrize(
    ("p", "expected"),
    [(0, 1.0), (50, 2.5), (100, 4.0)],
)
def test_percentile_interpolates(helpers, p, expected):
    assert helpers.percentile([1.0, 2.0, 3.0, 4.0], p) == expected


def test_rounding_helpers(helpers):
    assert helpers.r1(1.24) == 1.2
    assert helpers.r2(1.236) == 1.24
    assert helpers.r1(None) is None
    assert helpers.r2(None) is None
