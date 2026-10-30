#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Full correctness gate: Go build/tests, Python oracle, fixture audits.
set -euo pipefail
cd "$(dirname "$0")"
export TZ=UTC

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: Go 1.26+ is required." >&2
  exit 1
fi
if ! python3 -c 'import numpy' >/dev/null 2>&1; then
  echo "ERROR: the Python audit oracle needs numpy." >&2
  echo "Install: python3 -m pip install -r requirements-dev.txt" >&2
  exit 1
fi
if ! python3 -m pytest --version >/dev/null 2>&1; then
  echo "ERROR: the Python oracle needs pytest." >&2
  echo "Install: python3 -m pip install -r requirements-dev.txt" >&2
  exit 1
fi

echo "== gofmt =="
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "ERROR: unformatted Go files:" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "== go vet =="
go vet ./...

echo "== go test =="
go test -count=1 ./...

echo "== python oracle tests =="
python3 -m pytest -q

echo "== build agentmeter =="
./build.sh --release --no-install >/dev/null
bin/agentmeter version

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fixture="command-code-telemetry/testdata/fixture"

echo "== go: cmd fixture build + python audit =="
bin/agentmeter cmd --root "$fixture" --out "$tmp/go-cmd" >/dev/null
python3 command-code-telemetry/py/audit.py \
  --root "$fixture" --data "$tmp/go-cmd/telemetry-data.json" >/dev/null
bin/agentmeter cmd --root "$fixture" --out "$tmp/go-cmd-opts" \
  --models slow --since 2026-09-02 --until 2026-09-04 --no-titles --full-paths >/dev/null
python3 command-code-telemetry/py/audit.py \
  --root "$fixture" --data "$tmp/go-cmd-opts/telemetry-data.json" >/dev/null

echo "== go: oc fixture build + python audit =="
WRITE_FIXTURE_DB="$tmp/opencode.db" go test ./internal/report/opencode -run '^TestStepTupleContract$' >/dev/null
bin/agentmeter oc --db "$tmp/opencode.db" --out "$tmp/go-oc" >/dev/null
python3 opencode-telemetry/py/audit.py \
  --db "$tmp/opencode.db" --data "$tmp/go-oc/telemetry-data.json" >/dev/null
bin/agentmeter oc --db "$tmp/opencode.db" --out "$tmp/go-oc-opts" \
  --models alpha --since 2026-01-05 --until 2026-01-05 --no-titles --full-paths >/dev/null
python3 opencode-telemetry/py/audit.py \
  --db "$tmp/opencode.db" --data "$tmp/go-oc-opts/telemetry-data.json" >/dev/null

echo "== go: cross-tool comparison =="
bin/agentmeter compare --oc "$tmp/go-oc" --cmd "$tmp/go-cmd" --out "$tmp/go-compare" >/dev/null
python3 - "$tmp/go-compare/comparison-data.json" <<'PY'
import json
import sys

document = json.load(open(sys.argv[1]))
assert document["kind"] == "comparison"
assert document["schemaVersion"] == 3
assert document["comparisonVersion"] == 1
assert document["meta"]["window"]["mode"] in {"overlap", "disjoint", "single", "full"}
for side in ("oc", "cmd"):
    totals = document["totals"][side]
    families = sum(family[side]["steps"] for family in document["models"]["families"])
    calls = sum(category[side]["calls"] for category in document["tools"]["normalized"])
    assert families == totals["steps"], (side, families, totals["steps"])
    assert calls == totals["toolCalls"], (side, calls, totals["toolCalls"])
assert len(document["verdicts"]) == 6
assert document["timeline"]["days"], "timeline has no days"
print("comparison checks passed")
PY

echo "== all correctness checks passed =="
