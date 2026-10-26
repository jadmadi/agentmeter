# AgentMeter

Local-first analytics for Command Code and OpenCode coding agents.

Two local, read-only analytics reports for AI coding sessions:

- **`command-code-telemetry/`** — Command Code JSONL transcripts.
- **`opencode-telemetry/`** — OpenCode's SQLite store.

Both reconstruct per-step throughput, token/cache/reasoning usage, cost, tool
attribution, session health, time patterns and model comparisons, then render a
filterable HTML page. Neither builder sends data anywhere or reads prompts,
assistant text, tool arguments or file contents. The OpenCode store is opened
read-only.

## Requirements

- Python 3.10+.
- Builders: Python standard library only.
- Audits and `check.sh`: `pytest` and `numpy`.

```bash
python3 -m pip install -r requirements-dev.txt
```

## Quick start

```bash
git clone https://github.com/jadmadi/agentmeter
cd agentmeter

# Command Code
python3 command-code-telemetry/py/telemetry.py --serve

# OpenCode (auto-detects the store on Linux/macOS/Windows)
python3 opencode-telemetry/py/telemetry.py --serve --port 8001
```

Each command writes `data/telemetry-data.*` next to its report and opens
`http://127.0.0.1:<port>/index.html`. Build only with no `--serve`; the printed
`index.html` path is self-contained, including when `--out` points elsewhere.

## Privacy

- Session titles are included by default. Pass `--no-titles` to omit them.
- Project basenames are the default; `--full-paths` is opt-in.
- `data/` is gitignored, but generated JSON/JS, the HTML page and CSV exports can
  still contain session titles and project names. Review before sharing.
- No prompts, assistant text, tool arguments or file contents are read or
  emitted.

## Correctness

Both builders target schema v3 and fail closed on contract violations. The
independent audits re-read the sources and verify totals, timing, fits, prices,
sessions, buckets, extremes and invariants.

Install the dev prerequisites once; the full gate fails closed without NumPy.

```bash
python3 -m pip install -r requirements-dev.txt
python3 -m pytest -q
(cd command-code-telemetry/py && python3 telemetry.py && python3 audit.py)
(cd opencode-telemetry/py && python3 telemetry.py && python3 audit.py)
./check.sh   # all of the above
```

See [`CONTRACT.md`](CONTRACT.md) for field definitions, censoring, attribution,
spend, and the live-source watermark.

## Layout

```text
command-code-telemetry/   Command Code builder, renderer, audit
  py/                     Python reference builder + audit
  web/                    shared HTML renderer
opencode-telemetry/       OpenCode builder, renderer, audit
  py/                     Python reference builder + audit
  web/                    shared HTML renderer
telemetry_common.py       shared math + fail-closed invariants
tests/                    shared math, invariants, synthetic fixtures
CONTRACT.md               schema v3 analytics contract
AGENTS.md                 agent runbook + working contract
check.sh                  local full correctness gate
requirements-dev.txt      pytest + numpy for audits
```

## License

AGPL-3.0-only. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright (C) 2026 Jad Madi · [@jadmadi](https://x.com/jadmadi).

If you run a modified version as a network service, the AGPL requires offering
the corresponding source to its users.
