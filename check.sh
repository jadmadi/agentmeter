#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Full correctness gate: shared tests, both builds, both independent audits.
set -euo pipefail
cd "$(dirname "$0")"

echo "== pytest =="
pytest -q

echo "== command-code-telemetry: build + audit =="
(
  cd command-code-telemetry
  python3 telemetry.py
  python3 audit.py
)

echo "== opencode-telemetry: build + audit =="
(
  cd opencode-telemetry
  python3 telemetry.py
  python3 audit.py
)

echo "== all correctness checks passed =="
