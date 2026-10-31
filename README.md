# AgentMeter

Local-first analytics for Command Code and OpenCode coding agents.

AgentMeter reads the local session data those tools already write, reconstructs
per-step throughput, tokens, cache, reasoning, cost, tool time and session
health, and renders a filterable HTML report. It is read-only, sends nothing
anywhere, and does not read prompts, assistant text, tool arguments or file
contents.

## Install

Precompiled binaries are the primary distribution: download the matching
`agentmeter-<os>-<arch>` from the GitHub release, verify it against
`checksums.txt`, and put it on `PATH`. No Python, NumPy or Go is needed to run.

To build the same artifacts from a clone:

```bash
git clone https://github.com/jadmadi/agentmeter
cd agentmeter
./build.sh --all        # dist/ + version.json + checksums.txt
./build.sh              # native release + install to ~/.local/bin
```

`go install github.com/jadmadi/agentmeter/cmd/agentmeter@latest` is the
source-install path. Go 1.26+ is the only build requirement.

## Run

```bash
# Command Code (alias: cmd)
agentmeter cmd --serve
agentmeter cmd --watch   # keep serving and rebuild when sources change

# OpenCode (alias: oc)
agentmeter oc --serve
agentmeter oc --watch    # keep serving and rebuild when the store changes

# Cross-tool comparison
agentmeter compare                      # both live sources
agentmeter compare oc-cmd               # explicit pair alias
agentmeter compare data/oc data/cmd     # positional inputs
agentmeter compare --oc data/oc --cmd data/cmd --serve
agentmeter compare --oc ~/.local/share/opencode/opencode.db \
  --cmd ~/.commandcode/projects --watch
```

`--serve` builds once and serves that snapshot. `--watch` implies `--serve`,
polls the source, and rebuilds the static files when a change is detected;
refresh the browser tab to see the new report. Default output is `./data/cmd`,
`./data/oc` and `./data/compare`. Each command writes a self-contained
`index.html`, its JSON/JS data files, then optionally serves the report on
`127.0.0.1`.

`agentmeter compare` accepts a pre-built schema v3 report directory or file
for either side, or the tool's live source: an OpenCode SQLite store, or a
Command Code session root. It writes `comparison-data.json`,
`comparison-data.js` and `index.html`. The comparison uses canonical
`generatedTokens` and `inputTotal`, intersects the two date ranges, matches
short model families, and stratifies throughput by output size.

Common flags:

```text
--models LIST    comma-separated, substring model filter
--since DATE     inclusive local date, YYYY-MM-DD
--until DATE     inclusive local date, YYYY-MM-DD
--out DIR        output directory
--no-titles      omit session titles
--full-paths     keep full project paths
--serve          build once, serve the report, and open it
--watch          watch sources, rebuild, and serve (implies --serve)
--port N         serve port (default 8000)
--no-open        do not open a browser
```

Command Code adds `--root` (default `~/.commandcode/projects`). OpenCode adds
`--db`, `--baseline` and the timing thresholds. `compare` adds `--oc` (report
or OpenCode store) and `--cmd` (report or session root); it shares `--out`,
`--serve`, `--watch`, `--port` and `--no-open`. Both values are optional:
omit them (or pass `--oc` / `--cmd` alone) to use the tool default. Positional
inputs and the `oc-cmd` alias are accepted. Run `agentmeter cmd --help`,
`agentmeter oc --help` or `agentmeter compare --help`.

## Privacy

- Session titles are included by default. Pass `--no-titles` to omit them.
- Project basenames are the default; `--full-paths` is opt-in.
- Generated JSON/JS, the HTML page and CSV exports can still contain session
  titles and project names. Review before sharing.
- No prompts, assistant text, tool arguments or file contents are read or
  emitted.
- The OpenCode store is opened read-only.

## Correctness

Schema v3 is enforced in both builders and both independent audits. Bad sums,
non-additive tool shares, missing spend, stale step rows or a broken invariant
stop the build before a report is written.

```bash
# Go tests (includes Python-oracle parity checks when available)
go test ./...

# Full gate: Go build/tests + Python oracle tests + Go fixture audit
./check.sh
```

Current independent audit coverage: Command Code 10,737 checks, OpenCode 19,083
checks. The Python reference implementation and audits live under each tool's
`py/` directory and are used as a differential oracle; they are not the
supported user path. The Go implementation is the release target.

The comparison engine consumes those validated reports. Its own tests cover
window intersection, family matching, output-size strata, partial metrics
(Command Code has no per-step wall clock), deterministic output and
report-vs-live ingestion; `check.sh` also builds and asserts a comparison
from the committed fixtures.

See [`CONTRACT.md`](CONTRACT.md) for field definitions, censoring, attribution,
spend, and the live-source watermark.

## Layout

```text
cmd/agentmeter/           one Go binary: cmd / oc / compare / version
internal/                 shared stats, contract, JSON, emit, sources, reports
  compare/                comparison model, deltas, normalization, CLI, web
  report/compare/         comparison builder and input ingestion
command-code-telemetry/
  go/                     Command Code Go adapter
  py/                     Python reference builder + audit
  web/                    shared HTML renderer
  testdata/               fixture + expected report
opencode-telemetry/
  go/                     OpenCode Go adapter
  py/                     Python reference builder + audit
  web/                    shared HTML renderer
  testdata/               golden reports
tests/                    Python oracle and parity tooling
CONTRACT.md               schema v3 analytics contract
AGENTS.md                 agent runbook + working contract
build.sh                  native/cross-platform release builds + install
check.sh                  full correctness gate
requirements-dev.txt      Python oracle only (pytest + numpy)
```

## Development

The Go module uses the standard library plus `modernc.org/sqlite` (pure Go, no
CGO). The Python reference and audits are optional for users; contributors need
`python3 -m pip install -r requirements-dev.txt` only for the oracle gate.

`./check.sh` fails closed if Go, NumPy or pytest is missing. `./build.sh --all`
cross-compiles the six release binaries with `CGO_ENABLED=0` and writes
`dist/version.json` plus `dist/checksums.txt`; the repository intentionally has
no GitHub Actions workflow.

## License

AGPL-3.0-only. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright (C) 2026 Jad Madi · [@jadmadi](https://x.com/jadmadi).

If you run a modified version as a network service, the AGPL requires offering
the corresponding source to its users.
