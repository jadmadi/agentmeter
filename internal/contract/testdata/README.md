<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# contract testdata

Fixtures for the shared schema v3 validator.

- `empty-cmd-report.json` — the report produced by
  `command-code-telemetry/py/telemetry.py` against an empty project root:

  ```bash
  TZ=UTC python3 telemetry.py --root <empty-dir> --out <tmp-out>
  ```

- `empty-oc-report.json` — the report produced by
  `opencode-telemetry/py/telemetry.py` against an OpenCode v2 database with the
  `session_v2` and `session_message` tables and no rows:

  ```bash
  TZ=UTC python3 telemetry.py --db <empty.db> --out <tmp-out>
  ```

Both documents were generated on 2026-10-26 with CPython 3.15.1 and schema v3.
Only volatile metadata was normalized: `meta.generatedAt` is fixed at the Unix
epoch, and `meta.rootLabel` / `meta.dbLabel` are replaced with `~/...` paths.
Everything else, including the 24 zeroed hourly rows a real empty build emits,
is verbatim builder output.

Both fixtures pass `telemetry_common.validate_common` in Python and must pass
`contract.ValidateCommon` / `contract.ValidateCmd` / `contract.ValidateOC` in
Go with zero issues.
