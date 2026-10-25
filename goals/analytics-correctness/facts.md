# Facts: Analytics Correctness and Decision Depth

## Verified Defects

1. cmd `byModel.cacheHitRate` divides by raw `inp`; `cost.byModel`
   divides by normalized `inputTotal`. For `deepseek-v4-flash` the JSON
   reports 0.9855 and 0.4963 in the same file. The HTML recomputes the
   normalized value, so the JSON contract is the broken surface.
2. cmd `totals.inputTokens` (7.896B) and `cache.inputTokens` (8.144B)
   use different semantics. 13/23 models have no price fit; the math
   silently assumes `inputIncludesCache=True` while the field reports
   `null`.
3. OpenCode has 12 fitted regressions with negative fixed seconds
   (e.g. `deepseek-flash -0.29s`, R²=0.98). The pace index uses them.
4. OpenCode `peaks`/`slowest` are built from `timed` rows only, so the
   true fastest (797 steps >400 tok/s) and slowest (7 steps >300s) are
   absent. `max=400.0` is the cap, not an observation. cmd has the same
   problem at 180s and 400 tok/s and no timing breakdown.
5. OpenCode per-tool `weightedShare` sums to 3.35; shares are not a
   partition because parallel calls divide by the full step time.
6. OpenCode `meta.errorSteps=134` vs `sessionHealth.errorSteps=150`;
   error spend ($0.078) is outside `totals.costUsd`.
7. cmd merges 2 project basenames across 4 paths (~2.6k steps) because
   `full_paths=False` discards parents before grouping.
8. cmd `daily.ratio` is x100; OpenCode `daily.ratio` is a fraction.
9. cmd has no `schemaVersion`, no `audit.py`, and no test for any
   derived metric.

## Canonical Definitions (schema v3)

- `freshInputTokens`: provider input excluding cache when known.
- `inputTotal = freshInputTokens + cacheReadTokens + cacheWriteTokens`.
- `cacheHitRate = cacheReadTokens / inputTotal` (0..1).
- `generatedTokens`: output plus reasoning where the provider separates
  them; cmd `outputTokens` is generated-equivalent.
- `totalSpendUsd = successful-step cost + error/empty ledger cost`.
- `ratio = expected_duration / actual_duration` (fraction).
- `ratioPct = ratio * 100`; `index = ratio / paceBase * 100`.
- `paceBase` is the median ratio; `paceBasePct = paceBase * 100`.
- Tool tokens/cost are attributed per call, not per step; tool time
  allocations are additive and sum to <= step time.
- A fit with a negative intercept is refit with intercept=0 and flagged.
- A fit is used for the pace index only when `n >= 30` and `r2 >= 0.50`.

## Invariants (fail the build)

- Sum of `byModel.steps` == `totals.steps`.
- Sum of token/cost categories == their totals.
- `0 <= cacheHitRate <= 1`; `cacheReadTokens <= inputTotal`.
- `byModel.cacheHitRate == cost.byModel.cacheHitRate` per model.
- `timed.all.n` == plausible count == sum of `byModel.timedN`.
- `timed.all.n <= totals.steps`; `maxIncluded <= threshold`;
  `maxObserved` is over all rows.
- `abs(sum(tool.shareCalls) - 1) <= 1e-6`; tool time shares <= 1.
- Sum of session steps/cost == totals; sum of project steps == totals.
- `errorSteps == finish=error count`; `ledgerSteps == ledger length`.
- `totalSpendUsd == recordedCost + errorCostUsd`.
- No NaN, infinity, or negative token/second/cost values.
- Daily/hourly sums for all-step tokens/cost == totals.
- All project labels are unique.

## Files

- `cmd-telemetry/telemetry.py`, `cmd-telemetry/index.html`,
  `cmd-telemetry/audit.py` (new)
- `opencode-telemetry/telemetry.py`, `opencode-telemetry/index.html`,
  `opencode-telemetry/audit.py`
- `telemetry_common.py` (new shared math)
- `tests/test_telemetry_helpers.py`, `tests/test_analytics_invariants.py`
- `CONTRACT.md`, `check.sh`, `AGENTS.md`
