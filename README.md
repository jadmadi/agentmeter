# AgentMeter

Local-first analytics for Command Code and OpenCode coding agents.

AgentMeter reads the local session data those tools already write, reconstructs
per-step throughput, tokens, cache, reasoning, cost, tool time and session
health, and renders a filterable HTML report. It is read-only, sends nothing
anywhere, and does not read prompts, assistant text, tool arguments or file
contents.

## Install

Go 1.26+ is the only user requirement. No Python, no NumPy, no virtualenv.

```bash
go install github.com/jadmadi/agentmeter/cmd/agentmeter@latest
```

Or build from a clone:

```bash
git clone https://github.com/jadmadi/agentmeter
cd agentmeter
go build -trimpath -o agentmeter ./cmd/agentmeter
```

A release binary is a single static file; no runtime dependencies.

## Run

```bash
# Command Code (alias: cmd)
agentmeter cmd --serve

# OpenCode (alias: oc)
agentmeter oc --serve
```

Default output is `./data/cmd` and `./data/oc`. Each command writes a
self-contained `index.html`, `telemetry-data.json` and `telemetry-data.js`, then
optionally serves the report on `127.0.0.1`.

Common flags:

```text
--models LIST    comma-separated, substring model filter
--since DATE     inclusive local date, YYYY-MM-DD
--until DATE     inclusive local date, YYYY-MM-DD
--out DIR        output directory
--no-titles      omit session titles
--full-paths     keep full project paths
--serve          serve the report and open it
--port N         serve port (default 8000)
--no-open        do not open a browser
```

Command Code adds `--root` (default `~/.commandcode/projects`). OpenCode adds
`--db`, `--baseline` and the timing thresholds. Run
`agentmeter cmd --help` or `agentmeter oc --help`.

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

See [`CONTRACT.md`](CONTRACT.md) for field definitions, censoring, attribution,
spend, and the live-source watermark.

## Layout

```text
cmd/agentmeter/           one Go binary: cmd / oc / version
internal/                 shared stats, contract, JSON, emit, sources, reports
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
check.sh                  full correctness gate
requirements-dev.txt      Python oracle only (pytest + numpy)
```

## Development

The Go module uses the standard library plus `modernc.org/sqlite` (pure Go, no
CGO). The Python reference and audits are optional for users; contributors need
`python3 -m pip install -r requirements-dev.txt` only for the oracle gate.

`./check.sh` fails closed if Go, NumPy or pytest is missing. Release builds are
cross-compiled locally with `CGO_ENABLED=0`; the repository intentionally has no
GitHub Actions workflow.

## License

AGPL-3.0-only. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright (C) 2026 Jad Madi · [@jadmadi](https://x.com/jadmadi).

If you run a modified version as a network service, the AGPL requires offering
the corresponding source to its users.
