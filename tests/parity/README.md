# Python vs Go parity

`compare.py` is the strict gate for the Go port. It normalizes only
`meta.generatedAt`; list order, nulls and numeric values must match.

```bash
# JSON report
python3 tests/parity/compare.py expected/telemetry-data.json actual/telemetry-data.json

# JS bundle (window.<TOOL> = ...)
python3 tests/parity/compare.py expected/telemetry-data.js actual/telemetry-data.js --js

# HTML shell after the src rewrite
python3 tests/parity/compare.py expected/index.html actual/index.html --html
```

Run both builders with the same `TZ`, root/DB and flags. The golden fixtures
should use `TZ=UTC` so `meta.timezone`, daily/hourly buckets and `dateFrom`
are deterministic.
