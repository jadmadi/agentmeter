# DSH telemetry

Go adapter for DeepSeek Harness (`dsh`) sessions (`~/.dsh/sessions`)
analytics. `agentmeter dsh` (or `agentmeter deepseek`) builds the schema v3
report; see `CONTRACT.md` ("DSH source notes") for semantics.

Layout:

- `go/` — CLI surface: flags, report build, summary, serve/watch.
- `web/` — shared report shell (adapted from the Command Code renderer).
- `testdata/fixture/` — committed fixture: two sessions (including a
  `.jsonl.zstd` compressed session with multi-version deduplication and a
  subagent child session) covering step-start/assistant-message timing pairs,
  tool calls and tool errors, thinking splits, normalized uncached input
  tokens, compaction summaries, model changes, session outcomes, skip ledger
  (incomplete/empty/duplicate), and `storages/session_projcache` metadata.

The Go builder enforces the schema v3 contract fail-closed via
`contract.ValidateDsh` and `check.sh` pins the fixture values. Privacy: only
record metadata is read (timestamps, model ids, usage, durations, tool names,
session labels). No prompts, assistant text, tool arguments, result bodies, or
file contents are read or emitted.
