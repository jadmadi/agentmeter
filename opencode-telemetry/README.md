# OpenCode session telemetry

A local, model-agnostic report for OpenCode's SQLite store. It reconstructs
per-step throughput, token and cache usage, reasoning share, tool runtime, cost,
session health and time patterns, then renders an interactive page you can
filter and share.

- Works with **every model** present in the store.
- No API keys, no network calls, no third-party Python packages.
- **No prompts, assistant text, tool arguments or file contents** are read or
  emitted. Only metadata, token counts, cost, timestamps, model/tool names and
  session metadata.
- Opens the database **read-only**; it never writes to OpenCode's store.

## Requirements

- Python 3.10+.
- The builder uses only the standard library.
- The independent audit and `check.sh` need `pytest` and `numpy`
  (`python3 -m pip install -r requirements-dev.txt`).
- Requires an OpenCode store with the v2 `session_v2`/`session_message` tables.
  V1-only stores are rejected with a clear message. Use `--db` for a nonstandard
  location; Linux, macOS and Windows defaults are auto-detected.

## Privacy

The report is local-only and opens the store read-only. It reads metadata, not
prompts or messages. Session titles are included by default; pass `--no-titles`
to omit them. Project basenames are the default; `--full-paths` is opt-in.
`data/` is gitignored, but `telemetry-data.*` and CSV exports can still contain
titles and project names — do not publish them without reviewing.

## Quick start

```bash
git clone <repo> telemetry
cd telemetry
python3 opencode-telemetry/telemetry.py --serve
```

The default database is auto-detected on Linux, macOS and Windows; override
with `--db /path/to/opencode.db`. It writes `data/telemetry-data.*`, copies
`index.html` when `--out` points elsewhere, starts a local server and opens:

```text
http://127.0.0.1:8000/index.html
```

Build only (then open the printed `index.html` yourself):

```bash
python3 opencode-telemetry/telemetry.py
```

Useful variants:

```bash
# one or more models (substring match on provider/model)
python3 telemetry.py --models deepseek,muse

# a date window (local dates)
python3 telemetry.py --since 2026-09-01 --until 2026-10-22

# a different store
python3 telemetry.py --db /path/to/opencode.db

# privacy: omit session titles and keep project basenames (default)
python3 telemetry.py --no-titles

# opt in to full project paths
python3 telemetry.py --full-paths

# a fixed pace-index baseline window
python3 telemetry.py --baseline 2026-09-01..2026-09-30

# custom timing thresholds
python3 telemetry.py --min-seconds 0.4 --max-seconds 240 --max-rate 600

# different port / don't open a browser
python3 telemetry.py --serve --port 8080 --no-open
```

## What the report answers

- **How fast is each model, really?** Generated tokens/s (visible + reasoning),
  visible-only tokens/s, median and percentile distribution, marginal decode
  rate, and fixed per-request overhead.
- **How much of the work is reasoning?** Reasoning share of generated tokens,
  steps with reasoning, median reasoning tokens, and the speed difference
  between thinking and non-thinking steps.
- **How much time is the model versus the tools?** OpenCode timestamps tool
  execution separately, so the report splits wall-clock step time into model
  generation, unioned tool runtime, and an overhead residual, overall and per
  tool call.
- **Why do some steps look slow?** Output length, turn type (tool call vs final
  answer), context size, tool mix, and the fixed cost paid by every request.
- **Where does the money go?** Recorded cost by model, token-cost composition
  (fresh input, cache read, cache write, generated output), net cache savings,
  cost per step and per 1M generated tokens.
- **When is it slower?** Daily and hourly pace index, weekday × hour heatmap,
  and the effect of output length versus clock time.
- **What is happening in sessions?** Top sessions by generated tokens, active
  vs wall-clock time, tokens per active minute, root versus subagent sessions,
  model switches, compactions and tool errors.
- **What happened to failed steps?** Error and empty messages are kept in a
  separate ledger with their cost and wall-clock time; they are excluded from
  the speed rows.
- **What is TTFT?** For steps with block timestamps, the report separates the
  first reasoning/tool block from the remaining decode time.
- **What data exists?** A coverage panel lists what was recorded and what the
  store cannot provide.

## Filters

The filter bar applies to every KPI, chart, note and table, and to the step
explorer:

- **Period:** All / 7d / 30d / 90d, or a custom From–To date span.
- **Model:** all models or one (provider-qualified to disambiguate duplicates).
- **Variant:** `max`, `xhigh`, `high`, `default`, `low`, `none` — the model's
  reasoning-effort variant, a major speed factor.
- **Tool:** primary tool of the step, or final answers. Tool aggregates are
  per call; a multi-tool step is split across its calls.
- **Turn:** all turns, tool calls, or final answers.
- **Entry:** root sessions or subagent sessions.
- **Project:** grouped by full directory path, displayed as the basename (or
  the full path with `--full-paths`). Duplicate basenames are disambiguated in
  the label.

The **Step explorer** is sortable and paginated (50 rows), and **Download CSV**
exports every filtered step — not just the visible page. The CSV includes a
`timed` flag; rate columns are blank for untimed steps.

## How a step is timed

Each OpenCode assistant message is one step. The store records three
millisecond clocks:

```text
time.created    request sent
time.streamed   model finished streaming (tool execution starts here)
time.completed  step finished, including tool execution
```

The report uses:

- **LLM seconds** = `streamed - created`. This includes prefill/TTFT, provider
  queueing and generation, and excludes tool execution.
- **Total step seconds** = `completed - created`.
- **Tool seconds** = the **union** of the step's tool intervals. Overlapping or
  parallel calls are counted once; permission waits and gaps are not counted
  as tool execution.
- **Tool span / gap** = the first-start-to-last-end span minus the union.
- **Overhead** = `total - LLM - tool union`; this is the residual commit,
  scheduling and client time.
- **TTFT** = first reasoning/tool block start minus `created`, when the block
  timestamp is meaningful. The remaining `LLM - TTFT` is the decode window.
- **Per-call tool time** is each tool’s own `ran → completed` interval; the
  step-level tool share uses the union, while the tool table uses per-call
  time.

When `streamed` is missing (older OpenCode versions), a tool-free step falls
back to `completed`; a step with tools is left untimed because generation and
tool execution cannot be separated.

The report reads the v2 `session_message` store. At audit time, one v1-only
OpenCode session has **27 assistant messages** that exist only in the legacy
`message`/`part` tables: 7,289 visible + 490 reasoning tokens, $0 recorded
cost, and no `streamed` clock. They are excluded from every aggregate and from
the audit. Every reported number is exact for the declared `session_message`
scope; merging the 27 legacy rows would require a separate v1 loader and is
not done here.

### Errors and empty steps

Assistant messages with `finish=error` or zero generated tokens are not put in
the speed rows. They are written to `errorLedger` with their timestamp, model,
project, agent, variant, finish, tokens, recorded cost, LLM/total/tool seconds
and tool-call count. The page shows a separate ledger panel and KPI totals. This
keeps stalled or failed requests from silently disappearing or distorting the
timed distribution.

### Tool attribution

Tool aggregates are per call, not per step. A step with two tool calls splits
its visible, reasoning and generated tokens and its cost equally between them;
each call keeps its own `ran → completed` runtime and error flag. The step’s
primary tool is still used by the step explorer filter. `runtime` uses the
union of the step’s tool intervals, so parallel calls do not double-count time.
Overlapping calls are additionally allocated proportionally to that union, so
`tools[].timeShare` is additive and sums to <= 1; the raw `runtime.byTool`
shares use the same allocation.

### Token normalization

OpenCode stores tokens in normalized form:

```text
input       fresh input, excluding cache
cache.read  cached input tokens
cache.write cached input written this step (0 in this dataset)
output      visible output, excluding reasoning
reasoning   thinking tokens, stored separately
```

So:

```text
generated = output + reasoning
total input = input + cache.read + cache.write
```

The recorded cost fits `output + reasoning` at the output rate (R² = 1.00 for
the largest paid model here), which confirms that reasoning is billed as output.
The report's primary rate is **generated tokens/s**; the visible-only rate is
shown for comparison.

## Why some steps are excluded

The store contains steps whose timestamps do not represent a real generation:
replayed messages, resumed steps, cached responses and old-version artifacts
that can show thousands of tokens in a few milliseconds.

A step is **timed** only when all of these default constraints hold:

```text
0.5s <= LLM seconds <= 300s
generated tokens >= 20
3 <= generated / LLM seconds <= 400
```

In the current store this keeps 23,064 of 30,895 token-bearing steps (74.7%).
The rest are counted in `meta.timing`:

- `scanned` — assistant rows read from the store (31,165)
- `considered` — rows passing the model/date filters (31,165 with no filters)
- `noCreated` — rows with no `time.created` timestamp (0)
- `empty` — unique rows with zero generated tokens (128)
- `errorSteps` — unique rows with `finish=error` (134)
- `ledger` — unique error/empty rows (150; the two flags overlap)
- `rows` — rows in the main step table (30,895)
- `duplicates` — normalized fork copies skipped (120)
- `fingerprintCollisions` — same timing/token fingerprint but a different
  normalized payload; these are kept, never deduplicated (0)
- `noTiming` — no usable `streamed` timestamp (1,101)
- `tooShort` — fewer than 20 generated tokens (33)
- `tooShortDuration` — below the minimum duration (5,877)
- `tooLong` / `stalled` — above the maximum duration (7)
- `slowRate` — below the minimum rate (16)
- `fastRate` — above the maximum rate (797)
- `outOfRange` — too short/long duration (5,884)
- `implausibleRate` — slow/fast rate (813)

`duplicates + ledger + rows = considered`. The timing buckets sum to `rows`.
`scanned` is the whole store; the other counts follow the model/date filters.
Fork copies are removed only when the normalized payload is identical and a
non-fork original is present; two legitimate steps that merely share a
fingerprint are both kept.

The thresholds are CLI flags (`--min-seconds`, `--max-seconds`,
`--min-tokens`, `--min-rate`, `--max-rate`) and are recorded in
`meta.timing.thresholds`. The 400 tok/s ceiling is the default; the peaks table
says so explicitly rather than presenting replay artifacts as records.

### Marginal decode and fixed overhead

For each model, the builder fits:

```text
LLM seconds = fixed_seconds + generated_tokens / marginal_rate
```

The marginal rate is the model's decode speed once the output is long enough to
amortise the fixed cost. The fixed term contains prefill, TTFT and provider
queueing. Models with fewer than 30 timed steps are left unfitted.

### Pace index

`pace index = expected_duration / actual_duration`, normalised so the median
timed step is 100. A value of 120 means the step was 20% faster than its own
generated length and turn type predict; 80 means 20% slower. This is what the
daily, hourly and heatmap views use.

By default the baseline is the selected timed rows. Pass
`--baseline YYYY-MM-DD..YYYY-MM-DD` to fit the baseline on a reference window;
`meta.paceBaseline` records the window and the page keeps that baseline fixed
instead of recomputing it from the filtered rows.

## Reasoning and time decomposition

Reasoning tokens are a large share of the work (about 52% of all generated
tokens in this dataset). The report measures that share and the median reasoning
tokens per step.

It does **not** claim a thinking-time split. For most providers, the reasoning
block's `time.completed` coincides with the end of the whole stream, so a
"thinking seconds" metric would be misleading. The reliable decomposition is
model time versus tool time, which comes from the tool blocks' own clocks.

TTFT uses the first reasoning or tool block's `time.created`. Text-only replies
have no block timestamp, and encrypted-reasoning providers sometimes write a
synthetic timestamp; those steps are reported as TTFT n/a rather than zero.

## Price fitting

For each model with at least 30 positive-cost steps, recorded `cost` is fitted
against four token categories:

```text
cost = fresh_input × inputRate
     + cache_read × cacheReadRate
     + cache_write × cacheWriteRate
     + (output + reasoning) × outputRate
```

Rates are `$ / 1M tokens`, solved by least squares. The fit is accepted only
when it has plausible non-negative rates, cache reads cost no more than fresh
input, and R² ≥ 0.85. Free models have no price fit and are shown as `$0.00`
recorded; their token cost is unattributed rather than invented.

Net cache savings are a counterfactual:
`reads × (fresh − cache) − writes × (write − fresh)`. Cache writes can offset
read savings; the report exposes `cache.savings`, `cache.readSavings` and
`cache.writePenalty` separately.

## Files

| File | Purpose |
|---|---|
| `telemetry.py` | SQLite scanner, timing/cost/reasoning aggregation and optional local server |
| `audit.py` | Independent re-derivation and verification against the SQLite source |
| `index.html` | Self-contained visualizer (no external libraries) |
| `data/telemetry-data.json` | Full aggregate, machine-readable |
| `data/telemetry-data.js` | Same data as `window.OPENCODE_TELEMETRY` for the page |
| `README.md` | This document |

The generated bundle is about 6 MB of JSON / 3 MB of JS because it carries one
compact row per step; the page needs those rows for client-side filtering.
`data/` is generated and gitignored — commit the code, not the report, because
the report can contain session titles and project names.

## Verification

`audit.py` deliberately does not import `telemetry.py`. It re-reads the v2
`session_message` store, replays the deduplication, and independently recomputes every count, token sum,
cost sum, timing bucket, per-model statistic, fit, session aggregate, bucket,
time group and peak. It also reconstructs every compact step row from the source
and compares it field by field; this is currently 19,083 assertions for the
default build (filtered builds vary).

```bash
python3 audit.py            # descriptive summary, key values and section totals
python3 audit.py --verbose  # every individual assertion
cd .. && pytest -q          # shared math + invariant tests
./check.sh                  # pytest + both builds + both audits
```

The audit needs `numpy` for an independent least-squares check; the report
builder itself uses only the Python standard library. It reads the filters
recorded in `meta`, so a filtered build can be audited too:

```bash
python3 telemetry.py --models deepseek --since 2026-09-01 --out /tmp/build
python3 audit.py --data /tmp/build/telemetry-data.json
```

The script exits non-zero on any mismatch. The page also exposes its live
recomputed aggregate as `window.__OC_AGG` when opened with `#audit`, so the
browser-side numbers can be compared with the JSON after any UI change.

## Aggregate schema

- `schemaVersion`: 3.
- `meta`: generation time, store path, model/date filters, sessions, scanned
  steps, `fingerprintCollisions`, timing breakdown (`scanned`, `considered`,
  `noCreated`, `empty`, `errorSteps`, `ledger`, `rows`, `duplicates`,
  `plausible`, `noTiming`, `tooShort`, `tooShortDuration`, `tooLong`,
  `stalled`, `slowRate`, `fastRate`), thresholds, timezone, raw `paceBase` and
  `paceBasePct`, baseline, privacy flags, and `sourceWatermark` (max source
  timestamp, scanned rows, final rows, duplicates). The audit ignores rows newer
  than the watermark, so a later audit stays exact on a live store.
- `totals`: steps, tool/text steps, tool calls, visible/reasoning/generated
  tokens, fresh/raw/total input, cache read/write, successful cost, error cost,
  total spend, models. Cost totals carry 6 decimals.
- `timed`: `all`, `text`, `tool` summaries with weighted, median, visible median
  and percentiles of generated tok/s, TTFT/decode medians and counts, plus exact
  token/seconds sums.
- `byModel`: per-model steps, rejected timing counts by reason, generated/
  visible/reasoning tokens, reasoning share, fresh input and `inputTotal`,
  cache, cost, timed counts, weighted/median/visible rates, TTFT/decode,
  marginal rate, fixed seconds, R², `fitUsable`, `rated`/`lowSample`,
  `medianCI`, `standardizedRate`/coverage, and error counts.
- `fits`: per-model `{all, text, tool}` regressions, aligned with
  `lookups.models`.
- `fitsGlobal`: the `{text, tool}` fallback fits used when a model/turn has too
  few timed observations. The page uses these for its pace index.
- `prices`: per-model fitted `$ / 1M` rates (`fresh`, `cache`, `cacheWrite`,
  `output`, `inputIncludesCache: false`).
- `cache` and `cost`: overall cache metrics, net read/write savings, and the
  fitted cost composition. `cost.unattributedCost` is recorded cost for models
  without a price fit; `cache.readCost` and `cache.tokenCost` are included for
  auditability.
- `reasoning`: steps with reasoning, aggregate token share among timed steps,
  median reasoning tokens and median visible tokens.
- `runtime`: model/tool/overhead time split, unioned step tool time, per-call
  median/p90 runtime, gap and overhead percentiles, per-tool breakdown.
- `errorLedger`: error and empty assistant messages with cost, time, model,
  project and tool-call count; excluded from `steps` and speed summaries.
- `agents`, `variants`, `thinking`, `entrypoints`: task/agent slices.
- `sessionHealth`: assistant health from the selected window (finishes, tool
  calls/errors, no-stream), event-type counters from the store, OpenCode
  versions, sessions in window/total/root/subagent, ledger totals.
- `coverage`: which fields are present.
- `steps`: one array per assistant step:
  `[epoch_seconds, model_idx, tool_idx, visible_output_tokens, llm_seconds,
  fresh_input_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
  agent_idx, has_reasoning, project_idx, entry_idx, session_idx, tool_calls,
  reasoning_blocks, text_blocks, reasoning_tokens, total_step_seconds,
  tool_seconds, total_input_tokens, variant_idx, finish_idx, ttft_seconds,
  decode_seconds, decode_tok_s, overhead_seconds, tool_span_seconds,
  tool_gap_seconds, [[tool_idx, tool_seconds, error, allocated_seconds], ...],
  ratio, timing_idx]`.
  `-1` means unavailable. The trailing array is per-call tool attribution with
  additive allocated time. Durations are stored to 3 decimals; `cost_usd` is
  the exact recorded float.
- `lookups`: index → model `{id, short, provider}`, all tool-call names,
  agents, variants, full project paths, project labels, entrypoint, finish.
- `sessions`: session metadata aligned with `session_idx`. `id` is a 16-character
  display id (unique in this store), `sessionId` is the full OpenCode id,
  `outputTokens` is visible output, `generatedTokens` is visible + reasoning,
  `inputTokens`/`inputTotal` are canonical total input, and
  `freshInputTokens`/`inputRawTokens` are the fresh provider field. `projectPath`
  is the full directory while `project` is the display label. `activeMinutes`,
  `idleMinutes`, `outputPerMinute` (active) and `wallOutputPerMinute` are
  included; error/empty/ledger steps, skipped spend, duplicate steps, outcome,
  subagent sessions, and `rollup*` cost/steps/tokens are separate per session.
- `daily`/`hourly`: all-step `nAll`, timed `nTimed`, generated tokens, cost,
  error cost, total spend, cache hit rate, sessions, and timed
  rate/ratio/index with `ratioN`.
- `dailyByModel`: per-model daily series (rate, spend, cache, ratio/index).
- `anomalies`, `fork`, `compactionImpact`, `compactionEvents`,
  `modelChangeEvents`, `errorLedger`: derived operational records.
- `histogram`, `sizeBuckets`, `context`, `contextText`, `peaks`, `slowest`,
  `lowestRate`, `rejectedExtremes`: pre-computed aggregates for inspection;
  the page recomputes the active filter from the compact rows.

## Caveats

- Timestamps are OpenCode's own clocks, not stream events. `created → streamed`
  includes client-side receipt, prefill and provider queueing.
- Generated tokens include reasoning; visible tokens are shown separately. A
  model that thinks more can look slower per visible word without being slower
  per generated token.
- Cost is OpenCode's recorded number. Pricing varies by model, provider and era,
  so cross-model `$/1M gen` is not always like-for-like. Cost totals are stored
  to 6 decimals; the page formats them to cents. Error/empty step cost is kept
  in `errorLedger` and is not in `totals.costUsd`; `totals.totalSpendUsd` adds
  it, and `cost.errorCostUsd` / `cost.totalSpendUsd` make the split explicit.
- Negative fitted intercepts are physically impossible for a duration. They are
  refit with `fixedSeconds = 0`, flagged as `interceptConstrained`, and `usable`
  only when `n >= 30` and `r2 >= 0.50`. The pace index uses only usable fits.
- Generated tokens in `totals` cover every token-bearing step; the `reasoning`
  object's token share and the timed summaries cover only timed steps. The
  report labels the timed vs all-step scopes.
- Session span and wall rate use the integer timestamps exposed in `steps`;
  active time is the sum of step `totalDt`. Neither is a millisecond-exact
  wall-clock measurement.
- The timed distribution is capped at 400 generated tok/s. `peaks` and
  `lowestRate` include valid `fastRate` / `slowRate` rows outside that cap;
  `rejectedExtremes` lists invalid timestamp artifacts with their reason, and
  `slowest` means longest duration. `maxIncluded`, `maxObserved`, and
  `maxRejected` make the censoring explicit.
- `ratio` is a fraction, `ratioPct` is percent, and `index` is the normalized
  pace. Every ratio carries `ratioN`.
- `tools[].timeShare` is an additive allocation of tool time; it never sums
  above 1. `tools[].shareCalls` sums to 1.
- `~` marks a model with fewer than 30 timed steps; `medianCI` is a
  deterministic 95% bootstrap interval. `standardizedRate` is direct
  standardization over turn type × output-size strata, descriptive not causal.
- Reasoning block timestamps do not reliably mark the end of thinking, so the
  report does not split thinking time from answer time. Tool runtime is
  reliable and is reported.
- Context buckets are descriptive, not causal: context size is confounded with
  model, session type and turn type.
- Assistant health follows the build's model/date filters; event-type counters
  (idle, shell, model-switched) are store-wide. The page's client-side filters
  apply to the step rows and the error ledger, not to the health counters.
- This is an observational analysis of one machine's history, not a controlled
  benchmark.
- Numbers move as new sessions are logged. Re-run the builder before sharing.

## Troubleshooting

- **No data:** confirm `--db` points at the OpenCode store and that it contains
  `session_message` rows with `type = 'assistant'`.
- **Database is locked:** the builder opens read-only with a 15s timeout. Retry
  after the current OpenCode turn finishes.
- **Port in use:** `--serve --port 8080`.
- **Custom output directory:** the page expects `data/telemetry-data.js`; keep
  `--out ./data` or update the `<script src>` path in `index.html`.
- **Titles or paths in the report:** use `--no-titles` and leave `--full-paths`
  off to keep project basenames only.
- **Only one model appears:** model filters are substring matches on
  `provider/model`; check the store with
  `sqlite3 ~/.local/share/opencode/opencode.db "select distinct json_extract(data,'$.model.id') from session_message where type='assistant'"`.
- **Large histories:** the page filters in the browser; generation is a single
  Python pass and does not need extra dependencies.
