#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Full correctness gate: shared tests, both builds, both independent audits.
set -euo pipefail
cd "$(dirname "$0")"

if ! python3 -c 'import numpy' >/dev/null 2>&1; then
  echo "ERROR: the full gate needs numpy for both source audits." >&2
  echo "Install: python3 -m pip install -r requirements-dev.txt" >&2
  exit 1
fi

echo "== pytest =="
python3 -m pytest -q

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
