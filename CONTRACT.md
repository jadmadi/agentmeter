# Telemetry Analytics Contract (schema v3)

The Go builder is the supported runtime. The Python builder remains the
reference implementation and independent audit oracle. Both emit
`schemaVersion: 3`; the Go builder validates the contract before writing and
fails closed: no JSON is written if an invariant is violated.

## Canonical definitions

| Field | Meaning |
| --- | --- |
| `freshInputTokens` | Provider input excluding cache, per model semantics. |
| `inputRawTokens` | The provider's raw input field, before normalization. |
| `inputTotal` | `freshInputTokens + cacheReadTokens + cacheWriteTokens`. |
| `inputTokens` | Alias for `inputTotal` (canonical total). |
| `generatedTokens` | Visible output + reasoning where separated. |
| `visibleOutputTokens` | Visible output only; null when the source does not separate it. |
| `cacheHitRate` | `cacheReadTokens / inputTotal`, always in `[0, 1]`. |
| `costUsd` | Recorded cost for successful step rows. |
| `errorCostUsd` | Cost of skipped/error/empty ledger rows (duplicates excluded). |
| `totalSpendUsd` | `costUsd + errorCostUsd`. |
| `ratio` | `expected_duration / actual_duration` (fraction). |
| `ratioPct` | `ratio × 100`. |
| `paceBase` | Median `ratio`; `paceBasePct = paceBase × 100`. |
| `index` | `ratio / paceBase × 100` (100 = typical). |

cmd's `outputTokens` includes reasoning, so it is generated-equivalent.
OpenCode's `outputTokens` is visible-only; its `generatedTokens` includes
reasoning. Consumers should use `generatedTokens` for cross-tool rate/spend
comparisons.

## Invariants enforced by every build

- `sum(byModel.steps) == totals.steps`.
- `sum(byModel.generatedTokens) == totals.generatedTokens`.
- `sum(cost.byModel.recordedCost) == totals.costUsd`.
- `totals.totalSpendUsd == totals.costUsd + cost.errorCostUsd`.
- `freshInputTokens + cacheReadTokens + cacheWriteTokens == inputTotal`.
- `cacheReadTokens <= inputTotal`; `0 <= cacheHitRate <= 1`.
- `byModel.cacheHitRate == cost.byModel.cacheHitRate` per model.
- `byModel.inputTotal == cost.byModel.inputTotal` per model.
- `timed.all.n == sum(byModel.timedN) == meta.timing.plausible`.
- `sum(session.steps) == totals.steps`; `sum(session.costUsd) == totals.costUsd`.
- `sum(project.steps) == totals.steps`; project labels are unique.
- `sum(tool.shareCalls) == 1`; `sum(tool.timeShare) <= 1`.
- `sum(daily.nAll) == totals.steps`; daily/hourly generated and cost sums match.
- `sum(daily.totalSpendUsd) + undated error cost == totals.totalSpendUsd`.
- No negative token/second/cost totals; no NaN/infinity.

## Live-source watermark

Both builders record `meta.sourceWatermark` (maximum source timestamp,
scanned rows, final rows, duplicates, and file count for cmd). Audits ignore
rows newer than the watermark, so a standalone audit remains exact even when
the source log or database keeps growing after the build. `check.sh` still
builds and audits in the same run for the strongest evidence.

## Statistical rules

- `fit_rate` uses `linear_fit` with `n >= 30`. A negative intercept is refit
  with `fixedSeconds = 0` and flagged `interceptConstrained`.
- A fit is `usable` only when `n >= 30` and `r2 >= 0.50`. The pace index uses
  the first usable fit in this order: model/kind, model/all, global/kind.
- `medianCI` is a deterministic 95% percentile bootstrap (200 resamples).
- `rated`/`lowSample` mark `timedN >= 30` / `< 30`.
- `standardizedRate` is direct standardization over turn type × output-size
  strata (20-100, 100-300, 300-1k, 1k-5k, 5k+), weighted by the global stratum
  distribution. It is descriptive, not causal.

## Censoring and extremes

- The timed distribution uses `0.5s <= duration <= 180s` (cmd) or `300s`
  (OpenCode), `>= 20` generated tokens, and `3..400` tokens/s.
- `maxIncluded` is the timed maximum. `maxObserved` is the fastest valid
  duration/token row outside the timed set (e.g. `fastRate`).
- `maxRejected` / `maxRejectedReason` expose invalid timestamp artifacts.
- `peaks` and `lowestRate` use valid rates (`plausible`, `fastRate`, `slowRate`);
  `slowest` is longest duration; `rejectedExtremes` lists `tooShort`,
  `tooShortDuration`, and `noTiming` artifacts with reasons.
- Counts for every excluded reason are in `meta.timing` (both tools) and
  `sessionHealth.timing`.

## Tool attribution

- Every call is attributed, not just the step's primary tool.
- Visible/reasoning/generated tokens and cost are split equally across a step's
  calls; the step's primary tool remains available for filtering.
- OpenCode allocates overlapping call intervals proportionally to the union, so
  `tools[].timeShare` and `runtime.byTool[].weightedShare` are additive and sum
  to <= 1. cmd cannot observe per-call runtime; `timeShare` is null and
  `runtime` is `"unavailable"`.

## Decision analytics

- `daily`/`hourly` cover all steps (`nAll`) and timed steps (`nTimed`), with
  cost, error cost, total spend, cache hit rate, sessions, rate/ratio/index,
  `ratioN`, 7-day moving averages, and a robust anomaly ledger.
- `dailyByModel` carries the per-model daily rate/spend/cache series.
- Sessions carry direct and `rollup*` (subagent-inclusive) cost/steps/tokens,
  outcome, error/empty/ledger counts, skipped/duplicate spend.
- `compactionImpact` compares the nearest timed steps before/after each
  compaction: context, rate, cost, and deltas.
- `fork` reports duplicate steps/tokens/cost and sessions with duplicates.

## Verification

```bash
gofmt -l .
go vet ./...
go test ./...
./check.sh   # Go build/tests + Python oracle + fixture audit
```

Current independent audit coverage: Command Code 10,737 checks, OpenCode
19,083 checks. Both audits exit non-zero on any mismatch and include the shared
contract invariants. The audits need `numpy` for independent least-squares
price checks. The Go CLI uses the standard library plus pure-Go SQLite; the
Python reference uses only the standard library.
