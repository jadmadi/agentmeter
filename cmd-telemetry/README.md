# Command Code session telemetry

A local, model-agnostic report for Command Code session transcripts. It reconstructs
per-step throughput, token and cache usage, recorded cost, tool mix, session health and
time patterns, then renders an interactive page you can filter and share.

- Works with **every model** in the logs (23 in the current dataset).
- No API keys, no network calls, no third-party Python packages.
- **No prompts, assistant text, tool arguments or file contents** are read or emitted.
  Only metadata, token counts, cost, timestamps, model/tool names and session metadata.

## Quick start

```bash
cd ~/.commandcode/cmd-telemetry
python3 telemetry.py --serve
```

That scans `~/.commandcode/projects/**/*.jsonl`, writes `data/telemetry-data.*`, starts a
local server and opens:

```text
http://127.0.0.1:8000/index.html
```

Build only (then open `index.html` yourself):

```bash
python3 telemetry.py
```

Useful variants:

```bash
# one or more models (substring match on the model id)
python3 telemetry.py --models deepseek,qwen

# a date window (local dates)
python3 telemetry.py --since 2026-09-01 --until 2026-10-22

# privacy: omit session titles (project basenames are the default)
python3 telemetry.py --no-titles

# opt in to full project paths
python3 telemetry.py --full-paths

# different port / don't open a browser
python3 telemetry.py --serve --port 8080 --no-open
```

## What the report answers

- **How fast is each model, really?** Effective tok/s per step, median and percentile
  distribution, marginal decode rate, and fixed per-request overhead.
- **Why do some steps look slow?** Output length, turn type (tool call vs final answer),
  context size, tool mix, and the fixed cost paid by every request.
- **Where does the money go?** Recorded cost by model, token-cost composition (fresh
  input, cache read, cache write, output), cache savings, cost per step and per 1M output.
- **When is it slower?** Daily and hourly pace index, weekday × hour heatmap, and the
  effect of output length versus clock time.
- **What is happening in sessions?** Top sessions by output, wall-clock span, output per
  minute, entrypoint (interactive vs headless), hook failures, compactions, model changes.
- **What data exists?** A coverage panel lists what was recorded and what the transcript
  cannot provide.

## Filters

The filter bar applies to every KPI, chart, note and table, and to the step explorer:

- **Period:** All / 7d / 30d / 90d, or a custom From–To date span.
- **Model:** all models or one.
- **Tool:** primary tool of the step, or final answers.
- **Turn:** all turns, tool calls, or final answers.
- **Entry:** interactive or print (headless/subagent) sessions.
- **Project:** project basename (or full path with `--full-paths`).

The **Step explorer** is sortable and paginated (50 rows), and **Download CSV** exports
every filtered step — not just the visible page.

## How a step is timed

Each assistant message in a session JSONL carries `usage`, `model`, `effort` and a
`timestamp`. Duration is:

```text
parent entry commit time  ->  this reply commit time
```

That includes TTFT/prefill, provider queueing and generation, and excludes tool execution.
The tool result shares the assistant's commit timestamp, so per-step tool runtime is not
recoverable from the transcript. Hook PreToolUse/PostToolUse timestamps give a separate
"tool window" estimate where hooks are installed.

Timed rows pass `0.5s <= duration <= 180s`, at least 20 output tokens, and
`3 <= tok/s <= 400`. Forked sessions reuse message ids; those copies are deduplicated.

### Marginal decode and fixed overhead

For each model and turn type, the builder fits:

```text
duration = fixed_seconds + output_tokens / marginal_rate
```

The marginal rate is the model's decode speed once the output is long enough to amortise
the fixed cost. A model can show 60 tok/s effective on a 1k-token answer and 120 tok/s
marginal on a 10k-token one.

### Pace index

`pace index = expected_duration / actual_duration`, normalised so the median timed step
is 100. A value of 120 means the step was 20% faster than its own output size and turn
type predict; 80 means 20% slower. This is what the daily, hourly and heatmap views use.

### Price fitting

For each model, recorded `costUsd` is fitted against four token categories:

```text
cost = nonCachedInput × freshRate
     + cacheRead × cacheReadRate
     + cacheWrite × cacheWriteRate
     + output × outputRate
```

Rates are `$ / 1M tokens`, solved by least squares. Provider usage fields differ:
DeepSeek's `inputTokens` includes cache reads, while Kimi's and some other providers
report fresh input only. The fit tries both decompositions and chooses the plausible one
(cache read ≤ fresh input) with the largest cache discount, so cache-hit rates are
computed against the correct total input per model. Models with fewer than 30
observations, or no plausible fit, are left as **unattributed** recorded cost in the
composition charts. Cache savings are a counterfactual: what cache reads would have cost
at the fitted fresh-input rate.

## What is recorded

Per assistant message:

- `usage`: `inputTokens`, `outputTokens`, `cacheReadTokens`, `cacheWriteTokens`, `costUsd`
- `model`, `effort`, `timestamp`, `parentId`, `id`
- `message.content`: `text`, `thinking`, `tool_use` block types and tool names
- `message.meta`: `messageId`, `source`, `createdAt`, `isAutomated`, `injected`, `isMeta`

Session header: `id`, `version`, `timestamp`, `cwd`.

Sidecar `<id>.meta.json`: `title`, `model`, `traceIds`, `entrypoint`, `goal`, `compaction`.

Other entries:

- `compaction`: `tokensBefore`, summary pointer
- `model_change`: model switches
- `file-history-snapshot`: checkpoint file state
- hook events: `eventName`, `hookName`, `hookType`, `duration`, `exitCode`, `blocked`,
  `toolName`, `toolUseId`
- user messages: `gitBranch` and metadata

The compact step table carries: timestamp, model, primary tool, output tokens, seconds,
input tokens, cache read, cache write, recorded cost, effort, thinking flag, project,
entrypoint, session, tool-call count, thinking-block count and text-block count.

### Not available from the transcript

- Prompts, assistant text, tool arguments/results, file contents — never read.
- True time-to-first-token (only commit times exist).
- Per-step tool execution runtime (tool results share the assistant timestamp).
- Stop reason and retry counts.
- Provider routing beyond the model id.

## Files

| File | Purpose |
|---|---|
| `telemetry.py` | Scanner, timing/cost aggregation and optional local server |
| `index.html` | Self-contained visualizer (no external libraries) |
| `data/telemetry-data.json` | Full aggregate, machine-readable |
| `data/telemetry-data.js` | Same data as `window.CMD_TELEMETRY` for the page |
| `README.md` | This document |

The generated bundle is roughly 1–2 MB because it carries one compact row per step; the
page needs those rows for client-side filtering. `data/` is generated and gitignored —
commit the code, not the report, because the report can contain session titles and
project names.

## Aggregate schema

- `meta`: generation time, root, models, sessions, files, date range, timezone, privacy
  flags, pace base.
- `steps`: one array per assistant step:
  `[epoch_seconds, model_idx, tool_idx, output_tokens, seconds, input_tokens,
  cache_read_tokens, cache_write_tokens, cost_usd, effort_idx, thinking, project_idx,
  entrypoint_idx, session_idx, tool_calls, thinking_blocks, text_blocks]`.
  `seconds = -1` means no usable timing.
- `lookups`: index → model `{id, short, provider}`, tool, effort, project, entrypoint.
- `fits`: per-model `{all, text, tool}` regressions.
- `prices`: per-model fitted `$ / 1M` rates (`fresh`, `cache`, `cacheWrite`, `output`,
  `inputIncludesCache`).
- `sessions`: session metadata aligned with `session_idx`.
- `sessionHealth`: hooks, tool windows, compactions, model changes, file snapshots,
  branches.
- `coverage`: which fields are present.
- Pre-computed aggregates (`totals`, `timed`, `cache`, `cost`, `tools`, `daily`, `hourly`,
  `histogram`, `sizeBuckets`, `context`, `peaks`, `slowest`) remain in the JSON for
  inspection; the page recomputes them for the active filter.

## Caveats

- Timestamps are commit times from the append-only log, not stream events. Durations
  include client-side receipt and provider queueing.
- Output tokens include reasoning tokens. Input tokens include cache reads/writes as
  reported by the provider.
- Cost is the CLI's recorded `costUsd`. Pricing varies by model and era; cross-model
  `$/1M out` is not always like-for-like.
- Hook/tool-window/compaction/model-change stats are full-history and ignore the page
  filters, because they are not part of the compact per-step rows.
- Dates and hours are pinned to the timezone recorded at generation time, shown in the
  header, so the report does not shift if opened in another timezone.
- This is an observational analysis of one machine's history, not a controlled benchmark.
- Numbers move as new sessions are logged. Re-run the builder before sharing.

## Troubleshooting

- **No data:** confirm `--root` points at `~/.commandcode/projects` and that the project
  contains session `.jsonl` files.
- **Port in use:** `--serve --port 8080`.
- **Custom output directory:** the page expects `data/telemetry-data.js`; keep
  `--out ./data` or update the `<script src>` path in `index.html`.
- **Titles or paths in the report:** use `--no-titles` to omit titles (default is on) and
  leave `--full-paths` off to keep project basenames only.
- **Large histories:** the page filters in the browser; generation is a single Python
  pass and does not need extra dependencies.
