# Muse telemetry

Go adapter for Muse's own sessions (`~/.local/share/muse/sessions`)
analytics. `agentmeter muse` builds the schema v3 report; see `CONTRACT.md`
("Muse source notes") for semantics.

Layout:

- `go/` — CLI surface: flags, report build, summary, serve/watch.
- `web/` — shared report shell (adapted from the Command Code renderer).
- `testdata/fixture/` — committed fixture: two sessions covering tool
  linkage, thinking splits, includes-cache input, subagent attribution,
  session outcomes, event/index labels, omissions, the skip ledger
  (incomplete/empty/duplicate) and the session index join
  (`session-index.db`).

The Python reference builder and independent audit are Phase 2; until then
the Go builder enforces the contract fail-closed and `check.sh` pins the
fixture values. Privacy: only record metadata is read (timestamps, model
ids, usage, durations, tool names, session labels). No prompts, assistant
text, tool arguments, result bodies or file contents are read or emitted;
the index query selects label columns only, never `first_user_prompt` or
`search_text`.
