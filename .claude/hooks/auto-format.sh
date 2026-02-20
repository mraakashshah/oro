#!/usr/bin/env bash
# PostToolUse hook: auto-format files after Edit/Write by extension
set -euo pipefail

INPUT=$(cat)

# Extract file_path from tool_input
file_path=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')

if [ -z "$file_path" ] || [ ! -f "$file_path" ]; then
    exit 0
fi

ext="${file_path##*.}"
formatted=""

case "$ext" in
    py)
        if command -v ruff >/dev/null 2>&1; then
            ruff format "$file_path" 2>/dev/null && formatted="ruff format"
            ruff check --fix "$file_path" 2>/dev/null || true
        fi
        ;;
    go)
        if command -v gofmt >/dev/null 2>&1; then
            gofmt -w "$file_path" 2>/dev/null && formatted="gofmt"
        fi
        ;;
    json)
        if command -v biome >/dev/null 2>&1; then
            biome format --write "$file_path" 2>/dev/null && formatted="biome format"
        fi
        ;;
esac

# Report what happened via additionalContext (only if we formatted)
if [ -n "$formatted" ]; then
    cat <<EOF
{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"Auto-formatted ${file_path##*/} with ${formatted}."}}
EOF
fi
