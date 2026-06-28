#!/usr/bin/env bash
set -euo pipefail

errors=0
warnings=0

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Doc Verification Report ==="
echo ""

# Check 1: backtick-wrapped paths in .md files exist on disk
echo "--- Checking file paths referenced in docs ---"
while IFS=: read -r file line content; do
    path=$(echo "$content" | sed -n 's/.*`\([^`]*\)`.*/\1/p')
    if [ -n "$path" ] && [ -f "$ROOT_DIR/$path" ] 2>/dev/null; then
        : # exists
    elif [ -n "$path" ] && [ -d "$ROOT_DIR/$path" ] 2>/dev/null; then
        : # exists
    elif [ -n "$path" ]; then
        echo "WARN: $file:$line: referenced path not found: $path"
        warnings=$((warnings + 1))
    fi
done < <(grep -rn '`[./a-zA-Z_]' "$ROOT_DIR/docs" --include="*.md" 2>/dev/null || true)

# Check 2: MVM_ env vars in docs match code
echo "--- Checking env vars in docs vs code ---"
doc_envs=$(grep -roh 'MVM_[A-Z_]*' "$ROOT_DIR/docs" --include="*.md" | sort -u || true)
code_envs=$(grep -roh 'EnvKey("[^"]*")' "$ROOT_DIR/internal/infra/constants.go" | sed "s/EnvKey(\"//;s/\")//" | while read key; do echo "${MVM_ENV_PREFIX:-MVM_}$key"; done || true)
# Also check for envKey("ASSET_MIRROR") patterns
code_envs2=$(grep -roh 'EnvKey("[^"]*")' "$ROOT_DIR/internal/lib/download/http.go" 2>/dev/null | sed "s/EnvKey(\"//;s/\")//" | while read key; do echo "${MVM_ENV_PREFIX:-MVM_}$key"; done || true)
all_code_envs=$(echo -e "$code_envs\n$code_envs2" | sort -u)

while IFS= read -r env; do
    if [ -n "$env" ]; then
        if ! echo "$all_code_envs" | grep -q "^$env$"; then
            echo "WARN: $env is documented but not consumed in code"
            warnings=$((warnings + 1))
        fi
    fi
done <<< "$doc_envs"

while IFS= read -r env; do
    if [ -n "$env" ]; then
        if ! echo "$doc_envs" | grep -q "^$env$"; then
            echo "WARN: $env is consumed in code but not documented"
            warnings=$((warnings + 1))
        fi
    fi
done <<< "$all_code_envs"

# Check 3: CLI flags in REFERENCES.md
echo "--- Checking CLI flags in REFERENCES.md ---"
if [ -f "$ROOT_DIR/docs/REFERENCES.md" ]; then
    for flag_file in "$ROOT_DIR/internal/cli"/*.go; do
        while IFS= read -r flag; do
            [ -z "$flag" ] && continue
            if ! grep -q "\-\-$flag" "$ROOT_DIR/docs/REFERENCES.md" 2>/dev/null; then
                echo "WARN: flag --$flag defined in $(basename $flag_file) but not found in REFERENCES.md"
                warnings=$((warnings + 1))
            fi
        done < <(grep -oP '"[a-z][a-z-]+"' "$flag_file" | grep -v 'Use\|Aliases\|ValidArgs\|Short\|Long\|Example\|SuggestionsFor\|command\|args\|shell' | tr -d '"' | sort -u || true)
    done
fi

echo ""
echo "=== Summary ==="
echo "Warnings: $warnings"
echo "Errors: $errors"

if [ "$errors" -gt 0 ]; then
    exit 1
fi
exit 0
