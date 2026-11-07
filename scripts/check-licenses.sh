#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Verify AGPL headers, third-party notices and release license files.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() {
    echo "license check: $*" >&2
    exit 1
}

required=(
    LICENSE
    NOTICE
    THIRD_PARTY_NOTICES.md
    third_party/licenses/README.md
    third_party/licenses/modules.txt
    third_party/licenses/PSF-2.0.txt
    third_party/licenses/go/LICENSE
    third_party/licenses/go/PATENTS
)
for file in "${required[@]}"; do
    [[ -f "$file" ]] || fail "missing $file"
done

grep -q 'GNU AFFERO GENERAL PUBLIC LICENSE' LICENSE ||
    fail "LICENSE is not the AGPL text"
grep -q 'Copyright (C) 2026 Jad Madi' NOTICE ||
    fail "NOTICE is missing the project copyright"
grep -q 'AGPL-3.0-only' NOTICE ||
    fail "NOTICE is missing the AGPL identifier"
grep -q 'AGPL-3.0-only' THIRD_PARTY_NOTICES.md ||
    fail "THIRD_PARTY_NOTICES.md is missing the AGPL identifier"
grep -q 'PSF-2.0' THIRD_PARTY_NOTICES.md ||
    fail "THIRD_PARTY_NOTICES.md is missing the CPython notice"

check_spdx() {
    local file="$1"
    grep -q 'SPDX-License-Identifier: AGPL-3.0-only' "$file" ||
        fail "missing AGPL SPDX header: $file"
}

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    while IFS= read -r file; do
        check_spdx "$file"
    done < <(git ls-files --cached --others --exclude-standard -- '*.go' '*.py' '*.sh' '*.html')
else
    while IFS= read -r file; do
        check_spdx "$file"
    done < <(find . -type f \
        \( -name '*.go' -o -name '*.py' -o -name '*.sh' -o -name '*.html' \) \
        -not -path './.git/*' -not -path './third_party/*' \
        -not -path './data/*' -not -path './dist/*' -not -path './dist-repos/*')
fi

manifest="third_party/licenses/modules.txt"
while IFS='|' read -r path version license targets; do
    if [[ -z "$path" || "$path" == \#* ]]; then
        continue
    fi
    [[ -n "$version" && -n "$license" && -n "$targets" ]] ||
        fail "malformed modules.txt entry: $path"
    [[ -f "third_party/licenses/$path/LICENSE" ]] ||
        fail "missing license file for $path"
done < "$manifest"

modules="$(go mod edit -json | python3 -c 'import json, sys; data = json.load(sys.stdin); print("\n".join(req["Path"] + "|" + req["Version"] for req in data.get("Require", [])))')"
while IFS='|' read -r path version; do
    if [[ -z "$path" ]]; then
        continue
    fi
    grep -Fq "${path}|${version}|" "$manifest" ||
        fail "go.mod module missing from modules.txt: $path $version"
done <<< "$modules"

for file in \
    command-code-telemetry/web/index.html \
    opencode-telemetry/web/index.html \
    muse-telemetry/web/index.html \
    dsh-telemetry/web/index.html \
    internal/compare/web/index.html; do
    grep -q 'class="license"' "$file" ||
        fail "missing visible license footer: $file"
    grep -q 'https://github.com/jadmadi/agentmeter' "$file" ||
        fail "missing source URL in report footer: $file"
done

echo "license checks passed"
