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
| `errorCostUsd` | Cost of error ledger rows (`reason in ("error", "both")` on OpenCode breakdowns; all skipped ledger rows in totals and Command Code). |
| `skippedCostUsd` | Cost of all skipped ledger rows (`error`, `empty`, and `both`), emitted on sessions, daily, hourly, and OpenCode `byModel`, `projects`, and `dailyByModel`. |
| `totalSpendUsd` | `costUsd + skippedCostUsd` (or `costUsd + errorCostUsd` where `errorCostUsd` already covers all skipped rows). |
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
- `sum(session.steps) == totals.steps`; `sum(session.generatedTokens) == totals.generatedTokens`; `sum(session.costUsd) == totals.costUsd`.
- `sum(project.steps) == totals.steps`; `sum(project.generatedTokens) == totals.generatedTokens`; `sum(project.costUsd) == totals.costUsd`; project labels are unique.
- `sum(tool.shareCalls) == 1`; `sum(tool.timeShare) <= 1`.
- `sum(daily.nAll) == sum(hourly.nAll) == totals.steps`; `sum(daily.nTimed) == sum(hourly.nTimed) == timed.all.n`; daily and hourly generated and cost sums match `totals`.
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

## Cross-tool comparison (comparison v1)

`agentmeter compare` aggregates two schema v3 reports and never reads prompts,
assistant text, tool arguments or file contents. Inputs are either pre-built
`telemetry-data.json` reports or the tools' live sources (OpenCode SQLite,
Command Code session root).

- Scope: the headline defaults to `likeForLike` (only model families present
  in both tools, with data on both sides). The all-model sections remain for
  context and for `--all-models`; `meta.defaultScope` records the default and
  `meta.notes` states that provider/model routing is a confound.
- Canonical alignment: generation speed is `generatedTokens / timed seconds`;
  output ratios use `generatedTokens`; input ratios use `inputTotal`; cache
  hit rate is `cacheReadTokens / inputTotal`.
- Window: the comparison window is the intersection of the two `meta.dateFrom`
  / `meta.dateTo` ranges (`overlap`). Without an intersection the document is
  `disjoint` and both full ranges are compared; a single dated source is
  `single`.
- Families vs models: `meta.families` counts source families; `meta.models`
  counts source model ids. A shared family can hold several models per tool,
  so family and model counts are intentionally different units.
  `totals.*.models` counts models with windowed steps, so it can be smaller
  than `meta.models`. `likeForLike.families` lists the shared family keys.
- Family rows: each family carries `n.oc`/`n.cmd` timed steps, p10/p25/p75/p90
  alongside the median, `flags` (`one-sided`, `low-sample`, `cost-outlier`,
  `speed-outlier`) and a `confidence` marker. Outliers compare a side's
  per-1k cost or weighted speed against that side's shared-family median
  (2x cost, 3x speed).
- Verdicts: `verdicts.shared` and `verdicts.all` each hold six entries with
  `ratio` (OpenCode / Command Code), `deltaPct`, `deltaPp`, `winner`, `n`,
  `scope` and `confidence` (`clear`, `weak`, `tie`, `low-sample`, `confounded`,
  `n/a`). Spend per session is always all-model and marked confounded/with a
  note because session mixes differ.
- Ratios and ties: speed ratios above 1 favor OpenCode; cost ratios below 1
  favor OpenCode. Ties are under 5% relative; cache ties are under 1
  percentage point or 5% relative.
- Strata: pooled and per-shared-family throughput over output-size buckets
  `20-100`, `100-300`, `300-1k`, `1k-5k`, `5k+`; a bucket with fewer than
  five timed steps on either side is flagged `lowSample`.
- Partial metrics: Command Code has no per-step wall clock, so
  `activeMinutes`, `idleMinutes` and runtime overhead are null for `cmd`;
  visible-output splits are unavailable because cmd's `outputTokens` includes
  reasoning. These limits are listed in `meta.partialMetrics` and are never
  filled with estimates.
- Spend basis: an `overlap` window sums the daily series
  (`totals.*.spendBasis = "window-daily"`); a full-range comparison uses the
  report total (`"full-report"`), which also contains undated ledger cost.
  The like-for-like scope is `"recorded-only"` because skipped ledger cost
  cannot be attributed to a model family. Undated cost is surfaced as
  `totals.*.undatedErrorCostUsd` and in `meta.partialMetrics` when non-zero.
- Cost views: `cost.*` carries `$ / 1k generated`, `$ / 1M generated`,
  `$ / 1M input`, `$ / step`, `$ / session` (recorded), `$ / tool call`, and
  `totalSpendPerSession` (all-model where available).
- Tool mix: raw tool names are preserved; a normalized category
  (`shell`, `read`, `write`, `edit`, `search`, `web`, `agent`, `todo`,
  `final answer`, `other`) carries the cross-tool share comparison.
  `tools.coverage` is the share of calls covered by the categories (1.0 when
  the normalization is exhaustive).
- Artifacts: `comparison-data.json`, `comparison-data.js` and `index.html`.

The builder fails closed. It rejects a non-v3 input, a step tuple of the wrong
length, a report whose model/step/daily sums disagree with its totals, a
comparison whose family steps or normalized calls do not sum back to the
windowed totals, and a tool-share or like-for-like scope that does not add up;
it never writes a document containing NaN, infinities or raw pointers.

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
