# Third-party license texts

This directory carries the verbatim copyright notices and license texts for
the third-party software that can be linked into AgentMeter release binaries.
`THIRD_PARTY_NOTICES.md` at the repository root is the human-readable summary;
this directory is the authoritative text.

## Layout

| Path | Component | License |
| --- | --- | --- |
| `go/LICENSE`, `go/PATENTS` | Go standard library and runtime | BSD-3-Clause + Go PATENTS |
| `PSF-2.0.txt` | CPython implementation references | PSF-2.0 |
| `github.com/dustin/go-humanize/LICENSE` | go-humanize | MIT |
| `github.com/google/uuid/LICENSE` | google/uuid | BSD-3-Clause |
| `github.com/mattn/go-isatty/LICENSE` | go-isatty | MIT |
| `github.com/ncruces/go-strftime/LICENSE` | go-strftime | MIT |
| `github.com/remyoudompheng/bigfft/LICENSE` | bigfft | BSD-3-Clause |
| `golang.org/x/sys/LICENSE`, `PATENTS` | x/sys | BSD-3-Clause |
| `modernc.org/libc/LICENSE`, `LICENSE-3RD-PARTY.md` | modernc libc | BSD-3-Clause |
| `modernc.org/mathutil/LICENSE` | modernc mathutil | BSD-3-Clause |
| `modernc.org/memory/LICENSE`, `LICENSE-GO`, `LICENSE-MMAP-GO`, `LICENSE-LOGO` | modernc memory | BSD-3-Clause |
| `modernc.org/sqlite/LICENSE`, `LICENSE-3RD-PARTY.md`, `LICENSE-SQLITE`, `LICENSE-SQLITE_VEC` | modernc sqlite | BSD-3-Clause |

The modernc `LICENSE-3RD-PARTY.md` files are the consolidated notices for the
code transpiled into those modules, including SQLite (public domain),
sqlite-vec (MIT), musl libc (MIT), Go-derived code, and other upstream
notices. Do not summarize or edit those files.

`modules.txt` is the machine-readable module inventory used by
`scripts/check-licenses.sh` and release verification.

## Refreshing

The files are copied verbatim from the module cache and the Go toolchain:

```bash
go mod download
GOMODCACHE="$(go env GOMODCACHE)"
GOROOT="$(go env GOROOT)"
cp "$GOROOT/LICENSE" "$GOROOT/PATENTS" third_party/licenses/go/
# Repeat for each module directory listed above.
```

After changing dependencies:

1. Update `modules.txt` (module, version, license, linked targets).
2. Copy the new `LICENSE`/`NOTICE`/`PATENTS` files.
3. Update `THIRD_PARTY_NOTICES.md`.
4. Run `./check.sh`.

The CPython license text is the `LICENSE.txt` shipped with CPython and is
named `PSF-2.0.txt` here for clarity.
