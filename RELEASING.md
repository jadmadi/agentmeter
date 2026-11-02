# Releasing AgentMeter

AgentMeter releases are manual; the repository intentionally has no GitHub
Actions workflow. Every release is built from a clean checkout of an annotated
tag, and each archive carries the AGPL license, `NOTICE`,
`THIRD_PARTY_NOTICES.md` and the verbatim third-party texts.

## Prerequisites

- Go 1.26+
- `tar`, `zip`, and `sha256sum` (or `shasum`)
- Python 3 with pytest and numpy for `./check.sh`
  (`python3 -m pip install -r requirements-dev.txt`)

## 1. Prepare the commit

- Merge the release changes; ensure `git status --porcelain` is empty.
- Do not build from a tree containing `data/`, `dist-repos/`, `.sila/` or
  `dist/`. They are gitignored and may contain local telemetry or stale
  binaries. Use a clean clone or worktree.

## 2. Run the gate

```bash
./check.sh
```

This runs gofmt, the license checks, `go vet`, the Go tests, the Python oracle
and all fixture audits.

## 3. Tag

```bash
git tag -a vX.Y.Z -m "AgentMeter vX.Y.Z"
git push origin vX.Y.Z
```

`./build.sh --release-artifact` requires an exact annotated SemVer tag and a
clean working tree.

## 4. Build the archives

```bash
./build.sh --release-artifact --all
```

Output in `dist/`:

- `agentmeter-X.Y.Z-linux-amd64.tar.gz`, `...-linux-arm64.tar.gz`
- `agentmeter-X.Y.Z-darwin-amd64.tar.gz`, `...-darwin-arm64.tar.gz`
- `agentmeter-X.Y.Z-windows-amd64.zip`, `...-windows-arm64.zip`
- `version.json`, `checksums.txt`

Each archive contains the binary, `LICENSE`, `NOTICE`,
`THIRD_PARTY_NOTICES.md`, `README.md`, `version.json` and
`third_party/licenses/`.

## 5. Verify

```bash
sha256sum -c dist/checksums.txt
tar -tzf dist/agentmeter-X.Y.Z-linux-amd64.tar.gz
unzip -l dist/agentmeter-X.Y.Z-windows-amd64.zip
```

Extract one archive and check:

```bash
./agentmeter version
# license: AGPL-3.0-only
# source: https://github.com/jadmadi/agentmeter
go version -m ./agentmeter
```

`go version -m` must show `vcs.modified=false` and the module set in
`third_party/licenses/modules.txt` (plus platform-specific `go-isatty` and
`go-strftime` on darwin/windows). If a new module appears, add its license
text, update `modules.txt` and `THIRD_PARTY_NOTICES.md`, then re-run
`./check.sh`.

## 6. Publish

Create a GitHub release for the tag and attach:

- the six archives
- `checksums.txt`
- `version.json`

GitHub supplies the source archives for the tag. That source availability is
the AGPL corresponding-source offer; do not upload a source archive that omits
`third_party/licenses/` or the notice files.

## 7. Smoke test

Download one archive from the release, verify it against `checksums.txt`,
extract it, run `agentmeter version` and generate a sample report. Confirm the
footer shows the copyright, AGPL identifier and source link.

## If you fork

AGPL section 13 requires a modified network deployment to offer its users the
Corresponding Source of that modified version. Update the source link in the
four HTML report shells and `cmd/agentmeter/main.go` to your repository before
publishing reports.
