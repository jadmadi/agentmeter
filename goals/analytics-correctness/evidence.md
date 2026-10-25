# Evidence: Analytics Correctness and Decision Depth

Date: 2026-10-25
Agent: command-code
Project: telemetry

## Scope delivered

- Schema v3 in both builders, with `validate_common` fail-closed before writing.
- Shared `telemetry_common.py`: percentile, constrained non-negative fit,
  deterministic bootstrap median CI, robust z, direct standardization.
- cmd cache/input contract: one canonical `freshInputTokens` / `inputTotal`
  per model; `byModel.cacheHitRate == cost.byModel.cacheHitRate`; the
  `deepseek-v4-flash` 0.9855 vs 0.4963 contradiction is gone.
- Fit correctness: negative intercepts are constrained to zero and flagged;
  only `n >= 30` and `r2 >= 0.50` fits feed the pace index.
- True extremes: `peaks`, `lowestRate`, `slowest` (duration), and
  `rejectedExtremes` are separated with timing reasons. The 65M tok/s
  timestamp artifact is no longer presented as a peak.
- Tool attribution: every call is attributed; OpenCode time shares are
  allocated additively (sum <= 1) instead of the old 3.35 sum.
- Spend: `errorCostUsd` and `totalSpendUsd` added; OpenCode now distinguishes
  `errorSteps`/`emptySteps`/`ledgerSteps` (134 / 128 / 150).
- cmd project paths retained; basename collisions are disambiguated; all
  projects are emitted.
- Ratio contract: fraction `ratio`, `ratioPct`, `paceBase`, `paceBasePct`,
  and `ratioN` in every bucket.
- Decision analytics: all-step daily/hourly cost/cache/error series, 7-day
  moving averages, per-model daily series, anomalies, subagent rollups,
  session outcomes, compaction impact, fork/duplicate spend, bootstrap CIs,
  low-sample flags, and composition-standardized rates.
- Renderers updated for the v3 tuple, true extremes, total spend, low-sample
  markers, minimum-day gating, project labels, and per-call tool shares.
- New cmd independent audit; OpenCode audit updated and expanded.

## Live-source watermark (follow-up)

A standalone audit after a build initially failed because the live Command
Code JSONL kept growing. Both builders now record `meta.sourceWatermark`
(max timestamp, scanned rows, final rows, files/duplicates), and both audits
ignore rows newer than it. The boundary timestamp is stored as a float, not a
truncated integer.

Regression evidence: a temporary source was built, a newer assistant row was
appended, and the standalone cmd audit still passed (284 checks, 0 failed).
The case is captured permanently in `tests/test_audit_watermark.py`.

## Verification

```text
pytest -q
28 passed

cd cmd-telemetry && python3 telemetry.py
exit 0

cd cmd-telemetry && python3 audit.py
10,737 passed, 0 failed
ALL CHECKS PASSED

cd opencode-telemetry && python3 telemetry.py
exit 0

cd opencode-telemetry && python3 audit.py
19,083 passed, 0 failed
ALL CHECKS PASSED

./check.sh
all correctness checks passed
```

Contract invariants (both builders, fail-closed):

- `sum(byModel.steps) == totals.steps`
- `fresh + cacheRead + cacheWrite == inputTotal`
- `byModel.cacheHitRate == cost.byModel.cacheHitRate`
- `sum(tool.shareCalls) == 1`; `sum(tool.timeShare) <= 1`
- `totalSpendUsd == costUsd + errorCostUsd`
- `sum(session/project/daily/hourly) == totals`
- no NaN/inf/negative totals; project labels unique

## Renderer evidence

Both inline scripts pass `node --check`; both pages consume the Python-provided
`ratio` and `timing` indices, so the UI cannot silently diverge from the JSON.
`cmd-telemetry/index.html` and `opencode-telemetry/index.html` now show total
spend, true valid extremes with reasons, low-sample markers, and all project
labels.

## Reproduce

```bash
./check.sh
```

Verdict: PASS
