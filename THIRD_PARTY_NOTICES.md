# Third-party notices

AgentMeter is licensed under the GNU Affero General Public License v3.0 only
(AGPL-3.0-only; see `LICENSE` and `NOTICE`). This file lists the third-party
components that can be included in AgentMeter release binaries, or that
AgentMeter source derives from. The verbatim copyright notices and license
texts are in `third_party/licenses/`; those files are authoritative.

## Linked Go modules

This table is the union across the six release targets. The authoritative
list for a specific binary is `go version -m <binary>`. Modules that appear
only in `go.sum` or the module graph but are not linked into a binary are not
listed here.

| Component | Version | License | Linked on | Text |
| --- | --- | --- | --- | --- |
| Go standard library and runtime | 1.26+ | BSD-3-Clause + Go PATENTS | all | `third_party/licenses/go/` |
| github.com/dustin/go-humanize | v1.1.0 | MIT | all | `third_party/licenses/github.com/dustin/go-humanize/LICENSE` |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | all | `third_party/licenses/github.com/google/uuid/LICENSE` |
| github.com/mattn/go-isatty | v0.0.24 | MIT | darwin, windows | `third_party/licenses/github.com/mattn/go-isatty/LICENSE` |
| github.com/ncruces/go-strftime | v1.1.0 | MIT | darwin, windows | `third_party/licenses/github.com/ncruces/go-strftime/LICENSE` |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | all | `third_party/licenses/github.com/remyoudompheng/bigfft/LICENSE` |
| golang.org/x/sys | v0.49.0 | BSD-3-Clause | all | `third_party/licenses/golang.org/x/sys/LICENSE` |
| modernc.org/libc | v1.79.0 | BSD-3-Clause | all | `third_party/licenses/modernc.org/libc/LICENSE` |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause | all | `third_party/licenses/modernc.org/mathutil/LICENSE` |
| modernc.org/memory | v1.12.1 | BSD-3-Clause | all | `third_party/licenses/modernc.org/memory/LICENSE` |
| modernc.org/sqlite | v1.61.0 | BSD-3-Clause | all | `third_party/licenses/modernc.org/sqlite/LICENSE` |

`third_party/licenses/modules.txt` is the machine-readable version of this
table, including which target platforms link each module.

## Embedded upstream code

`modernc.org/sqlite` and `modernc.org/libc` are Go translations of C code and
carry consolidated third-party notices:

- `third_party/licenses/modernc.org/sqlite/LICENSE-3RD-PARTY.md` covers
  SQLite 3.54.1 (public domain), sqlite-vec v0.1.9 (MIT), SQLite
  `test_demovfs.c` (public domain), Go (BSD-3-Clause), go-netdb (MIT), musl
  libc (MIT), NixOS/nixpkgs (MIT), and module-graph-only components.
- `third_party/licenses/modernc.org/libc/LICENSE-3RD-PARTY.md` covers Go
  (BSD-3-Clause), musl libc (MIT), go-netdb (MIT), NixOS/nixpkgs (MIT), and
  other upstream notices.
- `third_party/licenses/modernc.org/sqlite/LICENSE-SQLITE` is SQLite's public
  domain dedication; `LICENSE-SQLITE_VEC` is the sqlite-vec MIT license.
- `internal/zstd/` is the pure-Go Zstandard (`zstd`) stream decompressor from
  the Go standard library (`src/internal/zstd`), governed by Go's BSD-3-Clause
  license (`third_party/licenses/go/LICENSE`).

## CPython-derived source

AgentMeter contains Go code that ports or mirrors CPython behavior for
byte-exact Python parity:

- `internal/stats/fsum.go` — CPython `Modules/mathmodule.c` partial-sums
- `internal/stats/pyround.go` — CPython `Objects/floatobject.c` rounding bounds
- `internal/stats/pyrandom/pyrandom.go` — CPython `Modules/_randommodule.c`
  and `Lib/random.py`
- `internal/pyjson/` — CPython `json.dumps` behavior
- `internal/report/{cmd,muse,dsh}/pynum.go` — CPython-parity numerics
- `internal/source/opencode/canonical.go` — uses `pyjson` for fork signatures

These are source-derived works, not linked CPython code. The CPython license
(PSF-2.0) is in `third_party/licenses/PSF-2.0.txt`. The Shewchuk partial-sums
algorithm itself is public domain; the CPython implementation notice is
retained here.

## Development-only dependencies

`pytest` (MIT) and `numpy` (BSD-3-Clause) are used only by the optional Python
oracle tests. They are not included in release binaries or archives.

## Verification

- `scripts/check-licenses.sh` checks SPDX headers, this file's required
  statements, the module manifest, and the license files.
- `third_party/licenses/modules.txt` is the source-level module inventory.
- `go version -m <binary>` is the per-artifact inventory.
- Release archives include this file and the complete `third_party/licenses/`
  directory.
