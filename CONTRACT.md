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
reasoning. Muse's `outputTokens` is generated (visible + reasoning), with
both splits emitted as `visibleOutputTokens` and `reasoningTokens`. Consumers
should use `generatedTokens` for cross-tool rate/spend comparisons.

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

- The timed distribution uses `0.5s <= duration <= 180s` (cmd and muse) or
  `300s` (OpenCode), `>= 20` generated tokens, and `3..400` tokens/s.
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
  to <= 1. cmd derives durations from timestamps and muse measures per-call
  durations, but neither can split runtime across calls or into active/idle;
  `timeShare` is null, `runtime` is `"unavailable"`, and
  `activeMinutes`/`idleMinutes` are null.

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

## Muse source notes

The Muse adapter reads Muse's own session store,
`~/.local/share/muse/sessions/<date>/<session-id>/session.jsonl`, plus
`<session>/subagent/<id>/session.jsonl` child logs in the same format, and
emits the same schema v3 report with the 20-slot Command Code step-tuple
layout. Session titles come from the optional `session-index.db` next to the
root via an explicit label-only column list; its `first_user_prompt` and
`search_text` columns are never selected.

- Steps are `model_completed` records with token usage. Each completion
  pairs with the latest unpaired `model_response_created` before it, and the
  pair's `response_id` attaches `assistant_tool_calls_committed` tool names;
  tool arguments are never read. Only three payload types are accepted
  (`runtime.session`, `session.end`, `session.workspace_branch.observed`);
  user-intent and tool-effect records are ignored, so message content is
  never within reach.
- The skip ledger records unpaired creations as `incomplete`, tokenless
  completions as `empty`, repeated record ids as `duplicate`, and
  timestamp-less completions as `noTimestamp`. There is no `error` reason:
  task-scoped `failed` events describe retried tool calls, not dead steps.
- Token semantics are known, not fitted: `input_tokens` includes cache, so
  `inputSemanticsSource` is always `normalized` with `inputIncludesCache`
  true and fresh input is input minus cache read and write (clamped at zero).
  `output_tokens` includes reasoning, so `visibleOutputTokens` is generated
  minus `reasoningTokens` (reasoning is clamped to output).
- Step durations are the measured per-call `duration_ms`, with the same
  0.5s..180s timed window as Command Code. There is no active/idle split, so
  `activeMinutes`/`idleMinutes` are null and `runtime` is `"unavailable"`;
  per-call runtime is unobservable, so `timeShare` is null.
- Cost is always zero: Muse records no cost data, and spend views read zero,
  never an estimate.
- Child-log rows are attributed to the parent session and flagged subagent;
  a child log never creates its own session. `meta.counts.subagents` counts
  subagent rows, `sessionHealth.subagents` counts contributing child logs,
  and each session carries `subagentSteps`.
- The session outcome is the main log's `session_end` exit reason verbatim
  (`clean`, `crash_inferred`, ...), or `unknown` when the session is still
  running or never recorded one. Child exits are ignored.
- Branch and workspace prefer the latest in-session `workspace_branch`
  observation (the `reference.name` and `workspace_root`, merged per field
  so a branchless event keeps the last known branch) and fall back to the
  session index. Titles are index-only; `--no-titles` drops them.
- Effort is always `unset` and the entrypoint is always `muse`, so
  `coverage.entrypoints` is false. Tool errors are unobservable, so
  `tools[].errRate` is null. Compaction, hook, tool-window and model-change
  signals are unmapped and stay at their reserved zeros.

## Cross-tool comparison (comparison v1)

`agentmeter compare` aggregates two schema v3 reports and never reads prompts,
assistant text, tool arguments or file contents. Inputs are either pre-built
`telemetry-data.json` reports or the tools' live sources (OpenCode SQLite,
Command Code or Muse session root). Any two of the three tools can be
compared; sides are arranged in canonical tool order (oc, cmd, muse), ratios
read side A / side B throughout, and `meta.pair` records the ordered pair.

- Scope: the headline defaults to `likeForLike` (shared model families pooled
  by volume, with data on both sides). The all-model sections remain for
  context and for `--all-models`; `meta.defaultScope` records the default and
  `meta.notes` states that provider/model routing is a confound. Pooling means
  the headline is not a single model-to-model pair; family rows carry
  `modelMatch` (`1:1`, `N:1`, `1:N`, `N:N`, `one-sided`) so true model pairs
  are explicit.
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
- Family rows: each family carries `n.<a>`/`n.<b>` timed steps, p10/p25/p75/p90
  alongside the median, `modelMatch` (model ids per side), `flags`
  (`one-sided`, `low-sample`, `cost-outlier`, `speed-outlier`) and a
  `confidence` marker. Outliers compare a side's per-1k cost or weighted speed
  against that side's shared-family median (2x cost, 3x speed).
- Verdicts: `verdicts.shared` and `verdicts.all` each hold six entries with
  `ratio` (side A / side B), `deltaPct`, `deltaPp`, `winner`, `n`,
  `scope` and `confidence` (`clear`, `weak`, `tie`, `low-sample`, `confounded`,
  `n/a`). Spend per session is always all-model and marked confounded/with a
  note because session mixes differ.
- Ratios and ties: speed ratios above 1 favor side A; cost ratios below 1
  favor side A. Ties are under 5% relative; cache ties are under 1
  percentage point or 5% relative.
- Strata: pooled and per-shared-family throughput over output-size buckets
  `20-100`, `100-300`, `300-1k`, `1k-5k`, `5k+`; a bucket with fewer than
  five timed steps on either side is flagged `lowSample`.
- Partial metrics: Command Code has no per-step wall clock, so
  `activeMinutes`, `idleMinutes` and runtime overhead are null for `cmd`;
  visible-output splits are unavailable because cmd's `outputTokens` includes
  reasoning. Muse measures per-call durations but has no active/idle split,
  so the same fields are null for `muse`; Muse does separate reasoning
  tokens, so its visible-output splits are available. These limits are listed
  in `meta.partialMetrics` and are never filled with estimates.
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
Python reference uses only the standard library. The Muse builder is Go-only
for now: it enforces the same contract fail-closed, and `check.sh` pins its
fixture values, but the differential oracle and independent audit are Phase 2.
