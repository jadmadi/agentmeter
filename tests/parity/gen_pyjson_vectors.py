#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Generate canonical JSON vectors for the Go pyjson tests.

Run from the repository root:

    python3 tests/parity/gen_pyjson_vectors.py

The expected bytes are produced by CPython itself with the exact call the
OpenCode builder uses for dedup signatures:

    json.dumps(value, sort_keys=True, separators=(",", ":"),
               ensure_ascii=False)

The vector file is committed so `go test` never needs Python.
"""

from __future__ import annotations

import hashlib
import json
import math
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
OUT = ROOT / "internal/pyjson/testdata/canonical_vectors.json"


def canonical(value) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


CASES: list[tuple[str, object]] = [
    ("empty_object", {}),
    ("empty_array", []),
    ("null", None),
    ("true", True),
    ("false", False),
    ("empty_string", ""),
    ("simple_object", {"b": 1, "a": 2}),
    (
        "nested_object",
        {
            "b": 1,
            "a": 2,
            "in": {"z": [1, 2, {"k": "v"}], "y": True},
        },
    ),
    ("int_zero", 0),
    ("int_one", 1),
    ("int_negative", -1),
    ("int_positive", 42),
    ("int_above_2_53", 9007199254740993),
    ("int_int64_max", 9223372036854775807),
    ("int_int64_min", -9223372036854775808),
    ("int_uint64_max", 18446744073709551615),
    (
        "int_float_pair",
        {"i": 1, "f": 1.0, "big_i": 10, "big_f": 10.0},
    ),
    ("float_zero", 0.0),
    ("float_negative_zero", -0.0),
    ("float_one", 1.0),
    ("float_negative_one", -1.0),
    ("float_tenth", 0.1),
    ("float_third", 1.0 / 3.0),
    ("float_pi", math.pi),
    ("float_1e-4", 1e-4),
    ("float_1e-5", 1e-5),
    ("float_1e-7", 1e-7),
    ("float_1e15", 1e15),
    ("float_1e16", 1e16),
    ("float_1e21", 1e21),
    ("float_2_5e-10", 2.5e-10),
    ("float_5e-324", 5e-324),
    ("float_max", 1.7976931348623157e308),
    ("float_1234567890123456", 1234567890123456.0),
    ("float_12345678901234567", 12345678901234567.0),
    ("float_100_5", 100.5),
    ("float_negative_100_5", -100.5),
    ("float_2_pow_53", 2.0**53),
    ("float_999999999999999_9", 999999999999999.9),
    ("string_plain", "hello world"),
    ("string_quote_backslash", 'quote " backslash \\ slash /'),
    ("string_controls", "\x00\x01\x08\x0c\n\r\t\x1f"),
    ("string_html_chars", "<script>&</script>"),
    ("string_del", "\x7f"),
    ("string_c1", "\x80\xa0"),
    ("string_line_separators", "\u2028\u2029"),
    ("string_non_ascii", "é中文🚀"),
    ("string_mixed", "line\nbreak\ttab é"),
    (
        "unicode_keys",
        {
            "é": 1,
            "z": 2,
            "Z": 3,
            "中": 4,
            "": 5,
            "\x01": 6,
            "\x7f": 7,
            "🚀": 8,
            "\u2028": 9,
        },
    ),
    (
        "mixed_array",
        [None, True, False, 1, 1.0, "x", [], {}, [[1]], {"a": [1]}],
    ),
    ("deep_nesting", [[[[[{"a": [[[[1]]]]}]]]]]),
    (
        "opencode_payload",
        {
            "model": {"providerID": "anthropic", "id": "claude-sonnet-4"},
            "tokens": {
                "input": 1234,
                "output": 50,
                "reasoning": 10,
                "cache": {"read": 100, "write": 0},
            },
            "time": {"created": 1750000000000, "completed": 1750000002500},
            "content": [{"type": "text", "title": "こんにちは"}],
            "finish": "stop",
            "agent": "build",
            "cost": 0.012345,
        },
    ),
]


def main() -> int:
    cases = []
    for name, value in CASES:
        text = canonical(value)
        cases.append(
            {
                "name": name,
                "value": value,
                "canonical": text,
                "sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
            }
        )
    document = {
        "generated_by": "tests/parity/gen_pyjson_vectors.py",
        "python": sys.version.split()[0],
        "json_dumps": (
            'json.dumps(value, sort_keys=True, separators=(",", ":"), '
            "ensure_ascii=False)"
        ),
        "cases": cases,
    }
    OUT.write_text(
        json.dumps(document, ensure_ascii=False, indent=1) + "\n", encoding="utf-8"
    )
    print(f"wrote {OUT} ({len(cases)} cases)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
