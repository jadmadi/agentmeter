#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Cross-build AgentMeter release binaries. Pure Go, CGO disabled.
set -euo pipefail
cd "$(dirname "$0")"

version="${1:-dev}"
mkdir -p dist

for os in linux darwin windows; do
  for arch in amd64 arm64; do
    name="agentmeter-${os}-${arch}"
    out="dist/${name}"
    if [ "$os" = "windows" ]; then
      out="${out}.exe"
    fi
    echo "building ${out}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath \
      -ldflags "-s -w -X main.version=${version}" \
      -o "$out" ./cmd/agentmeter
    if command -v sha256sum >/dev/null 2>&1; then
      (cd dist && sha256sum "$(basename "$out")" > "$(basename "$out").sha256")
    else
      (cd dist && shasum -a 256 "$(basename "$out")" > "$(basename "$out").sha256")
    fi
  done
done

echo "release artifacts in dist/ (version ${version})"
