# AgentMeter

Local-first analytics for Command Code and OpenCode coding agents.

AgentMeter reads the local session data those tools already write, reconstructs
per-step throughput, tokens, cache, reasoning, cost, tool time and session
health, and renders a filterable HTML report. It is read-only, sends nothing
anywhere, and does not read prompts, assistant text, tool arguments or file
contents.

## Install

Precompiled archives are the primary distribution: download the matching
`agentmeter-<version>-<os>-<arch>.tar.gz` (or `.zip` on Windows), verify it
against `checksums.txt`, extract it, and put `agentmeter` on `PATH`. Each
archive contains `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`, the verbatim
third-party license texts under `third_party/licenses/`, and `README.md`. No
Python, NumPy or Go is needed to run.

```bash
tar -xzf agentmeter-<version>-linux-amd64.tar.gz
# Windows: unzip agentmeter-<version>-windows-amd64.zip
```

To build the same archives from a clone:

```bash
git clone https://github.com/jadmadi/agentmeter
cd agentmeter
./build.sh --all        # six archives + version.json + checksums.txt
./build.sh              # native release + install to ~/.local/bin
```

`./build.sh --target OS/ARCH` writes one bare binary for local use.
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

# Muse (Muse's own sessions)
agentmeter muse --serve
agentmeter muse --watch  # keep serving and rebuild when sources change

# Cross-tool comparison (any pair: oc-cmd, oc-muse, cmd-muse)
agentmeter compare                      # oc-cmd over default live sources
agentmeter compare oc-muse              # explicit pair alias
agentmeter compare data/oc data/cmd     # positional inputs
agentmeter compare --oc data/oc --cmd data/cmd --serve
agentmeter compare --cmd data/cmd --muse data/muse --serve
agentmeter compare --oc ~/.local/share/opencode/opencode.db \
  --cmd ~/.commandcode/projects --watch
```

`--serve` builds once and serves that snapshot. `--watch` implies `--serve`,
polls the source, and rebuilds the static files when a change is detected;
refresh the browser tab to see the new report. Default output is `./data/cmd`,
`./data/oc`, `./data/muse` and `./data/compare`. Each command writes a
self-contained `index.html`, its JSON/JS data files, then optionally serves
the report on `127.0.0.1`.

`agentmeter compare` accepts a pre-built schema v3 report directory or file
for either side, or the tool's live source: an OpenCode SQLite store, or a
Command Code or Muse session root. Any two tools can be compared
(`oc-cmd`, `oc-muse`, `cmd-muse`); sides are arranged in canonical tool
order and `meta.pair` records which pair a document compares. It writes
`comparison-data.json`, `comparison-data.js` and `index.html`. The default
headline is like-for-like:
it pools the model families present in both tools by volume, with
shared-family speed, cost and strata. Family rows show the model match
(side A:side B); only 1:1 rows are a true model-to-model pair, while
N:1 and 1:N rows pool provider variants. `--all-models` switches the headline
to the all-model aggregate, which is context rather than a harness verdict.
The terminal report names the winner and spells out the advantage (for
example, "OpenCode 3.48× faster" or "Command Code 1.24× cheaper"), so cost
ratios never need to be inverted. The JSON keeps raw side A / side B
ratios; speed ratios above 1 favor side A, cost ratios below 1 favor
side A.
Ties are under 5% relative (cache: under 1 pp or 5% relative). The comparison
also shows family/model counts separately, `$ / 1M input`, per-session spend,
low-sample and outlier flags, and the explicit caveat that provider/model
routing dominates speed and cost differences.

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

compare adds:
--all-models     headline uses all models instead of shared families
--json           print comparison-data.json instead of the human summary
--no-color       disable ANSI color
```

Command Code adds `--root` (default `~/.commandcode/projects`); Muse adds
`--root` (default `~/.local/share/muse/sessions`), reading Muse's own
session event logs plus the optional `session-index.db` labels.
OpenCode adds `--db`, `--baseline`
and the timing thresholds. `compare` adds `--oc` (report or OpenCode store),
`--cmd` (report or session root) and `--muse` (report or session root); it
shares `--out`, `--serve`, `--watch`, `--port` and `--no-open`. Tool values
are optional: omit them (or pass a tool flag alone) to use the tool default.
Without an alias, set tool flags select the pair; a single flag pairs that
tool with the first `oc-cmd` default not already present. Positional inputs
fill the pair sides in order, and pair aliases (`oc-cmd`, `oc-muse`,
`cmd-muse` plus `-vs-`/`_vs_`/`+` spellings) are accepted. Run
`agentmeter cmd --help`, `agentmeter oc --help`, `agentmeter muse --help` or
`agentmeter compare --help`.

## Privacy

- Session titles are included by default. Pass `--no-titles` to omit them.
  (Muse titles come from the session index; without it, ids are shown.)
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
supported user path. The Go implementation is the release target. The Muse
builder is Go-only for now: it enforces the same schema v3 contract
(fail-closed on every invariant) and `check.sh` pins its fixture values, but
the differential Python oracle and independent audit are still to come.

The comparison engine consumes those validated reports. Its own tests cover
window intersection, family matching, output-size strata, partial metrics
(Command Code has no per-step wall clock; Muse measures per-call durations
but no active/idle split), deterministic output and
report-vs-live ingestion; `check.sh` also builds and asserts a comparison
from the committed fixtures, plus both Muse pairs.

See [`CONTRACT.md`](CONTRACT.md) for field definitions, censoring, attribution,
spend, and the live-source watermark.

## Layout

```text
cmd/agentmeter/           one Go binary: cmd / oc / muse / compare / version
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
muse-telemetry/
  go/                     Muse Go adapter
  web/                    shared HTML renderer
  testdata/               fixture (Python oracle is Phase 2)
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

`./check.sh` fails closed if Go, NumPy or pytest is missing. It also runs
`scripts/check-licenses.sh`, which verifies SPDX headers and the third-party
manifest.

`./build.sh --all` cross-compiles the six release targets with `CGO_ENABLED=0`
and writes license-bearing archives plus `dist/version.json` and
`dist/checksums.txt`. `./build.sh --target OS/ARCH` writes one bare binary for
local use. The repository intentionally has no GitHub Actions workflow; see
[RELEASING.md](RELEASING.md) for the manual release runbook.

## Security

See [SECURITY.md](SECURITY.md) for supported versions and private reporting.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `./check.sh` before sending a PR.

## License

AGPL-3.0-only. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The release archives include
these files and the verbatim texts under `third_party/licenses/`.

Copyright (C) 2026 Jad Madi · [@jadmadi](https://x.com/jadmadi).

If you run a modified version as a network service, the AGPL requires offering
the corresponding source to its users. The HTML reports link to the upstream
source; if you deploy a modified version, point that link at your source.
