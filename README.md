# Telemetry

Two local, read-only analytics reports for AI coding sessions:

- **`cmd-telemetry/`** — Command Code JSONL transcripts.
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
git clone <repo> telemetry
cd telemetry

# Command Code
python3 cmd-telemetry/telemetry.py --serve

# OpenCode (auto-detects the store on Linux/macOS/Windows)
python3 opencode-telemetry/telemetry.py --serve --port 8001
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

```bash
pytest -q
(cd cmd-telemetry && python3 telemetry.py && python3 audit.py)
(cd opencode-telemetry && python3 telemetry.py && python3 audit.py)
./check.sh   # all of the above
```

See [`CONTRACT.md`](CONTRACT.md) for field definitions, censoring, attribution,
spend, and the live-source watermark.

## Layout

```text
cmd-telemetry/          Command Code builder, renderer, audit, README
opencode-telemetry/     OpenCode builder, renderer, audit, README
telemetry_common.py     shared math + fail-closed invariants
tests/                  shared math, invariants, cmd/OpenCode fixtures
CONTRACT.md             schema v3 analytics contract
check.sh                local full correctness gate
```

## License

AGPL-3.0-only. See [LICENSE](LICENSE).

Copyright (C) 2026 Jad Madi.

If you run a modified version as a network service, the AGPL requires offering
the corresponding source to its users.
