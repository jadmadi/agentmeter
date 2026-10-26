#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# AgentMeter build/release script.
#
# Modes:
#   --release (default) stripped, CGO-free production binary
#   --dev               debug-friendly binary
#   --all               six cross-platform release artifacts in dist/
#   --target os/arch    one cross-platform artifact
#
# Install: --install[=DIR] copies a native build to DIR (default ~/.local/bin).
set -euo pipefail
cd "$(dirname "$0")"

BOLD=$'\033[1m'
GREEN=$'\033[32m'
YELLOW=$'\033[33m'
RED=$'\033[31m'
CYAN=$'\033[36m'
RESET=$'\033[0m'

show_help() {
    cat <<EOF
${BOLD}AgentMeter build.sh${RESET}

${BOLD}USAGE${RESET}
  ./build.sh [MODE] [OPTIONS]

${BOLD}MODES${RESET}
  --release              stripped production build (default)
  --dev                  debug build with symbols
  --all                  linux/darwin/windows x amd64/arm64 into dist/
  --target OS/ARCH       one target, e.g. --target linux/arm64
  --check                run ./check.sh before building
  --release-artifact     require an exact annotated SemVer git tag

${BOLD}OPTIONS${RESET}
  -o, --output PATH      binary path (default bin/agentmeter or dist/...)
  --install[=DIR]        install native build to DIR (default ~/.local/bin)
  --no-install           do not install
  -h, --help             this help

${BOLD}EXAMPLES${RESET}
  ./build.sh                         # release + install to ~/.local/bin
  ./build.sh --dev --no-install      # bin/agentmeter, debug symbols
  ./build.sh --all                   # dist/ + version.json + checksums.txt
  ./build.sh --target darwin/arm64   # one cross-compiled binary
EOF
}

MODE="release"
OUTPUT=""
TARGET=""
BUILD_ALL=false
CHECK=false
RELEASE_ARTIFACT=false
INSTALL=true
if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="${HOME}/.local/bin"
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --release) MODE="release"; shift ;;
        --dev) MODE="dev"; shift ;;
        --all) BUILD_ALL=true; shift ;;
        --target) TARGET="${2:-}"; shift 2 ;;
        --check) CHECK=true; shift ;;
        --release-artifact) RELEASE_ARTIFACT=true; shift ;;
        -o|--output) OUTPUT="${2:-}"; shift 2 ;;
        --install=*) INSTALL_DIR="${1#*=}"; INSTALL=true; shift ;;
        --install) INSTALL=true; shift ;;
        --no-install) INSTALL=false; shift ;;
        -h|--help|help) show_help; exit 0 ;;
        *) echo "${RED}error: unknown argument '$1'${RESET}" >&2; echo "run ./build.sh --help" >&2; exit 1 ;;
    esac
done

if [[ -n "$OUTPUT" && ( "$BUILD_ALL" == true || -n "$TARGET" ) ]]; then
    echo "${RED}error: --output cannot be combined with --all/--target${RESET}" >&2
    exit 1
fi

EXACT_TAG="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
if [[ -n "${AGENTMETER_VERSION:-}" ]]; then
    VERSION="$AGENTMETER_VERSION"
elif [[ "$EXACT_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    VERSION="$EXACT_TAG"
else
    VERSION="dev+$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
    if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then VERSION+="-dirty"; fi
fi
if [[ "$RELEASE_ARTIFACT" == true ]]; then
    if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
        echo "${RED}error: --release-artifact requires an exact SemVer tag${RESET}" >&2
        exit 1
    fi
    if [[ "$(git cat-file -t "$VERSION" 2>/dev/null || true)" != "tag" ]]; then
        echo "${RED}error: --release-artifact requires an annotated git tag${RESET}" >&2
        exit 1
    fi
fi
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
LDFLAGS="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

if [[ "$CHECK" == true ]]; then
    echo "${BOLD}running ./check.sh${RESET}"
    ./check.sh
fi

build_single() {
    local goos="$1" goarch="$2" out="$3"
    mkdir -p "$(dirname "$out")"
    echo "${BOLD}building${RESET} ${CYAN}${out}${RESET} (${goos}/${goarch}, ${MODE})"
    if [[ "$MODE" == "release" ]]; then
        env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -trimpath -ldflags "-s -w ${LDFLAGS}" -o "$out" ./cmd/agentmeter
    else
        env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -trimpath -ldflags "${LDFLAGS}" -o "$out" ./cmd/agentmeter
    fi
}

if [[ "$BUILD_ALL" == true ]]; then
    mkdir -p dist
    targets=("linux/amd64" "linux/arm64" "darwin/amd64" "darwin/arm64" "windows/amd64" "windows/arm64")
    for target in "${targets[@]}"; do
        IFS='/' read -r goos goarch <<< "$target"
        ext=""
        if [[ "$goos" == "windows" ]]; then ext=".exe"; fi
        build_single "$goos" "$goarch" "dist/agentmeter-${goos}-${goarch}${ext}"
    done
    cat > dist/version.json <<EOF
{
  "version": "${VERSION}",
  "commit": "${COMMIT}",
  "built_at": "${DATE}",
  "platforms": ["linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64"]
}
EOF
    (
        cd dist
        rm -f ./*.sha256
        files=(
            agentmeter-linux-amd64
            agentmeter-linux-arm64
            agentmeter-darwin-amd64
            agentmeter-darwin-arm64
            agentmeter-windows-amd64.exe
            agentmeter-windows-arm64.exe
            version.json
        )
        if command -v sha256sum >/dev/null 2>&1; then
            sha256sum "${files[@]}" > checksums.txt
        else
            shasum -a 256 "${files[@]}" > checksums.txt
        fi
    )
    echo "${GREEN}artifacts written to dist/${RESET}"
    exit 0
fi

if [[ -n "$TARGET" ]]; then
    IFS='/' read -r goos goarch <<< "$TARGET"
    if [[ -z "${goos:-}" || -z "${goarch:-}" ]]; then
        echo "${RED}error: --target expects OS/ARCH${RESET}" >&2
        exit 1
    fi
    ext=""
    if [[ "$goos" == "windows" ]]; then ext=".exe"; fi
    out="${OUTPUT:-dist/agentmeter-${goos}-${goarch}${ext}}"
    build_single "$goos" "$goarch" "$out"
    echo "${GREEN}built ${out}${RESET}"
    exit 0
fi

NATIVE_OS="$(go env GOOS)"
NATIVE_ARCH="$(go env GOARCH)"
OUT="${OUTPUT:-bin/agentmeter}"
if [[ "$NATIVE_OS" == "windows" && "$OUT" != *.exe ]]; then OUT="${OUT}.exe"; fi
build_single "$NATIVE_OS" "$NATIVE_ARCH" "$OUT"
echo "${GREEN}built ${OUT}${RESET}"

if [[ "$INSTALL" == true ]]; then
    if ! mkdir -p "$INSTALL_DIR" 2>/dev/null || [[ ! -w "$INSTALL_DIR" ]]; then
        echo "${RED}error: cannot write to ${INSTALL_DIR}${RESET}" >&2
        echo "  try: ./build.sh --install=\${HOME}/.local/bin" >&2
        exit 1
    fi
    target="$INSTALL_DIR/$(basename "$OUT")"
    tmp="${target}.tmp.$$"
    cp "$OUT" "$tmp"
    chmod +x "$tmp"
    mv -f "$tmp" "$target"
    echo "${GREEN}installed ${target}${RESET}"
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) ;;
        *) echo "${YELLOW}note:${RESET} ${INSTALL_DIR} is not in PATH" ;;
    esac
fi
