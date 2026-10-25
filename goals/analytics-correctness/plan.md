# Plan: Analytics Correctness and Decision Depth

## Execution Steps

- [x] **Phase 1 — Canonical contract and fail-closed invariants**
  - Define one schema (v3) for both tools: token semantics, cache
    semantics, spend, ratio, rates, tool attribution, error accounting.
  - Add `validate_report(data)` to both builders; a build fails on any
    invariant violation (sums, bounds, NaN/inf, label collisions).
  - Add `telemetry_common.py` for shared math: percentile, median,
    non-negative-intercept fit, bootstrap median CI, robust z, and
    direct standardization.
  - Unit-test every shared function and the validator.

- [x] **Phase 2 — Correctness fixes in both pipelines**
  - cmd: one canonical input total per model; fix `byModel.cacheHitRate`;
    expose `inputSemantics` and `inputSemanticsSource`; add a
    cacheRead/input heuristic for unfitted models.
  - Both: constrain fit intercepts to >= 0; flag constrained fits; only
    use fits with adequate R² in the pace index.
  - Both: `peaks`, `slowest`, `longest` from all rows with a `reason`;
    add `maxIncluded`, `maxObserved`, and full timing counts (cmd).
  - Both: per-call tool attribution; OpenCode allocates overlapping tool
    time additively; shares must sum to <= 1.
  - OpenCode: unify `errorSteps`/`ledgerSteps`; add `totalSpend`.
  - cmd: add a skipped/error ledger and timing breakdown for
    zero-output, no-timestamp, duplicate, and filtered rows.
  - cmd: preserve full project paths and disambiguate basename
    collisions; include all projects.
  - Both: ratio is a fraction, `ratioPct` is percent, `index` is the
    normalized pace; add `ratioN`. Add `schemaVersion` and align field
    names (`generatedTokens`, `perMillionGenerated`).

- [x] **Phase 3 — Decision analytics**
  - Daily and hourly: all-step cost/tokens/sessions/errors/cache plus
    timed rate/index, 7-day moving averages, and `nAll`/`nTimed`.
  - Per-model daily series: rate, cost, errors, cache hit.
  - Model table: bootstrap 95% CI for the median rate, `rated` and
    `lowSample` flags, and a composition-standardized rate.
  - Anomaly ledger: robust z (MAD) for daily rate, cost, error rate,
    and cache-hit drops.
  - Subagent rollups: root-session cost/steps/tokens include descendants;
    add `outcome` and wasted-cost fields.
  - Compaction impact: before/after rate, context, and cost per step.
  - Add `CONTRACT.md` documenting common fields and units.

- [x] **Phase 4 — Renderers, audits, verification, closure**
  - Update both `index.html` files for corrected fields, total spend,
    error/ledger views, true peaks/slowest reasons, min-n/CI markers,
    daily cost/anomalies, subagent rollups, and additive tool shares.
  - Add `cmd-telemetry/audit.py` and extend `opencode-telemetry/audit.py`
    with independent invariant checks.
  - Add `check.sh` and update the AGENTS.md runbook.
  - Run pytest, both builds, both audits, `sila gate`; write evidence
    with `Verdict: PASS`; certify; reconcile; handoff; sync.
