#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Strict Python-vs-Go report parity checks.

The only normalized field is meta.generatedAt. Everything else, including
list order, nulls and numeric values, must match exactly.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

JS_PREFIX = re.compile(r"^window\.[A-Z_]+ = ")


def load_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def normalize(report: dict) -> dict:
    report = json.loads(json.dumps(report))
    meta = report.get("meta")
    if isinstance(meta, dict):
        meta.pop("generatedAt", None)
    return report


def load_js(path: Path) -> dict:
    text = path.read_text(encoding="utf-8").strip()
    match = JS_PREFIX.match(text)
    if not match:
        raise SystemExit(f"{path}: missing window.<TOOL> = prefix")
    payload = text[match.end() :]
    if payload.endswith(";"):
        payload = payload[:-1]
    return json.loads(payload)


def first_diff(a, b, path: str = "$") -> str | None:
    if type(a) is not type(b) and not (
        isinstance(a, (int, float))
        and not isinstance(a, bool)
        and isinstance(b, (int, float))
        and not isinstance(b, bool)
    ):
        return f"{path}: type {type(a).__name__} != {type(b).__name__}"
    if isinstance(a, dict):
        if set(a) != set(b):
            missing = sorted(set(a) - set(b))
            extra = sorted(set(b) - set(a))
            return f"{path}: keys differ (missing={missing}, extra={extra})"
        for key in a:
            diff = first_diff(a[key], b[key], f"{path}.{key}")
            if diff:
                return diff
        return None
    if isinstance(a, list):
        if len(a) != len(b):
            return f"{path}: length {len(a)} != {len(b)}"
        for i, (left, right) in enumerate(zip(a, b)):
            diff = first_diff(left, right, f"{path}[{i}]")
            if diff:
                return diff
        return None
    if isinstance(a, float):
        if a != b:
            return f"{path}: {a!r} != {b!r}"
        return None
    if a != b:
        return f"{path}: {a!r} != {b!r}"
    return None


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("expected")
    parser.add_argument("actual")
    parser.add_argument("--js", action="store_true", help="compare telemetry-data.js bundles")
    parser.add_argument("--html", action="store_true", help="compare index.html bytes")
    args = parser.parse_args()

    expected = Path(args.expected)
    actual = Path(args.actual)

    if args.html:
        left = expected.read_bytes()
        right = actual.read_bytes()
        if left != right:
            print(f"FAIL html: {expected} != {actual}", file=sys.stderr)
            return 1
        print(f"OK html: {expected.name}")
        return 0

    if args.js:
        left_data = load_js(expected)
        right_data = load_js(actual)
    else:
        left_data = load_json(expected)
        right_data = load_json(actual)

    diff = first_diff(normalize(left_data), normalize(right_data))
    if diff:
        print(f"FAIL {expected} != {actual}: {diff}", file=sys.stderr)
        return 1
    print(f"OK {expected.name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
