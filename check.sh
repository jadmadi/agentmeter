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

echo "== license notices =="
./scripts/check-licenses.sh

echo "== go vet =="
go vet ./...

echo "== go test =="
go test -count=1 ./...

echo "== python oracle tests =="
python3 -m pytest -q

echo "== build agentmeter =="
./build.sh --release --no-install >/dev/null
bin/agentmeter version
bin/agentmeter version | grep -q '^license: AGPL-3.0-only$'
bin/agentmeter version | grep -q '^source: https://github.com/jadmadi/agentmeter$'

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

echo "== go: muse fixture build + invariant checks =="
bin/agentmeter muse --root "muse-telemetry/testdata/fixture" --out "$tmp/go-muse" >/dev/null
bin/agentmeter muse --root "muse-telemetry/testdata/fixture" --out "$tmp/go-muse-opts" \
  --models muse-spark-x --since 2026-11-01 --until 2026-11-01 --no-titles --full-paths >/dev/null
python3 - "$tmp/go-muse/telemetry-data.json" <<'PY'
import json
import sys

report = json.load(open(sys.argv[1]))
assert report["schemaVersion"] == 3, report["schemaVersion"]
totals = report["totals"]
assert totals["steps"] == 5, totals["steps"]
assert report["timed"]["all"]["n"] == 5
assert totals["generatedTokens"] == 1950, totals["generatedTokens"]
assert totals["reasoningTokens"] == 450, totals["reasoningTokens"]
assert totals["visibleOutputTokens"] == totals["generatedTokens"] - totals["reasoningTokens"]
assert totals["inputTokens"] == 21250, totals["inputTokens"]
assert totals["freshInputTokens"] == 5650, totals["freshInputTokens"]
assert totals["costUsd"] == 0, totals["costUsd"]
assert totals["errorCostUsd"] == 0
assert totals["totalSpendUsd"] == 0
for model in report["byModel"]:
    assert model["inputIncludesCache"] is True, model
    assert model["inputSemanticsSource"] == "normalized", model
    assert model["provider"] == "meta", model
assert report["meta"]["includeTitles"] is True
assert report["meta"]["sessions"] == 2
counts = report["meta"]["counts"]
assert counts["subagents"] == 1, counts
assert counts["omissions"] == 1, counts
assert counts["incomplete"] == 1, counts
health = report["sessionHealth"]
assert health["subagents"]["count"] == 1, health["subagents"]
assert health["omissions"]["count"] == 1, health["omissions"]
assert health["incomplete"]["count"] == 1, health["incomplete"]
assert health["unmatchedToolCommits"] == 0, health["unmatchedToolCommits"]
reasons = sorted(entry["reason"] for entry in report["skipLedger"])
assert reasons == ["duplicate", "empty", "incomplete"], reasons
sessions = {entry["sessionId"]: entry for entry in report["sessions"]}
assert sessions["sess-main-aa"]["outcome"] == "clean", sessions["sess-main-aa"]
assert sessions["sess-cc"]["outcome"] == "unknown", sessions["sess-cc"]
assert sessions["sess-main-aa"]["title"] == "Main fixture session"
assert sessions["sess-main-aa"]["branch"] == "feat-x"
assert sessions["sess-main-aa"]["subagentSteps"] == 1
coverage = report["coverage"]
assert coverage["titles"] is True and coverage["branches"] is True, coverage
assert coverage["subagents"] is True and coverage["entrypoints"] is False, coverage
print("muse checks passed")
PY

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
    assert document["tools"]["coverage"][side] > 0.999
assert len(document["verdicts"]["shared"]) == 6
assert len(document["verdicts"]["all"]) == 6
like = document["likeForLike"]
assert like["available"] in (True, False)
if like["available"]:
    for side in ("oc", "cmd"):
        assert like["totals"][side]["steps"] <= document["totals"][side]["steps"]
assert document["timeline"]["days"], "timeline has no days"
print("comparison checks passed")
PY

echo "== go: muse pair comparisons =="
bin/agentmeter compare --cmd "$tmp/go-cmd" --muse "$tmp/go-muse" --out "$tmp/go-compare-cm" >/dev/null
bin/agentmeter compare oc-muse "$tmp/go-oc" "$tmp/go-muse" --out "$tmp/go-compare-om" >/dev/null
python3 - "$tmp/go-compare-cm/comparison-data.json" "$tmp/go-compare-om/comparison-data.json" <<'PY'
import json
import sys

for path in sys.argv[1:]:
    document = json.load(open(path))
    assert document["kind"] == "comparison", path
    assert document["comparisonVersion"] == 1, path
    pair = document["meta"]["pair"]
    assert len(pair) == 2 and "muse" in pair, (path, pair)
    for side in pair:
        totals = document["totals"][side]
        families = sum(family[side]["steps"] for family in document["models"]["families"])
        calls = sum(category[side]["calls"] for category in document["tools"]["normalized"])
        assert families == totals["steps"], (path, side, families, totals["steps"])
        assert calls == totals["toolCalls"], (path, side, calls, totals["toolCalls"])
        assert document["tools"]["coverage"][side] > 0.999, (path, side)
    assert len(document["verdicts"]["shared"]) == 6, path
    assert len(document["verdicts"]["all"]) == 6, path
    like = document["likeForLike"]
    assert like["available"] in (True, False), path
    if like["available"]:
        for side in pair:
            assert like["totals"][side]["steps"] <= document["totals"][side]["steps"]
    assert document["timeline"]["days"], path
print("muse pair checks passed")
PY

echo "== all correctness checks passed =="
