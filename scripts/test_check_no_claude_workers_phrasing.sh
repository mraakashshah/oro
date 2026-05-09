#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHECK="$SCRIPT_DIR/check_no_claude_workers_phrasing.sh"

PASS=0
FAIL=0

test_case() {
	local name="$1"
	local fn="$2"
	printf '> %-60s' "$name"
	if "$fn"; then
		printf 'PASS\n'
		PASS=$((PASS + 1))
	else
		printf 'FAIL\n'
		FAIL=$((FAIL + 1))
	fi
}

test_clean_files_pass() {
	"$CHECK" "$REPO_ROOT/assets/ORO_AGENT.md" "$REPO_ROOT/README.md"
}

test_bad_phrase_fails() {
	local tmp rc=0
	tmp=$(mktemp)
	# Combine to form the disallowed phrase at runtime (not literally in source)
	printf '%s %s\n' "Claude" "workers run tasks" >"$tmp"
	"$CHECK" "$tmp" || rc=$?
	rm -f "$tmp"
	[ "$rc" -ne 0 ]
}

test_self_check_passes() {
	"$CHECK" self-check
}

test_compat_section_excluded() {
	local tmp
	tmp=$(mktemp)
	# Write a compat-marked block that contains the disallowed phrase
	printf '<!-- begin-compat -->\n%s %s\n<!-- end-compat -->\n' \
		"Claude" "workers are excluded here" >"$tmp"
	local rc=0
	"$CHECK" "$tmp" || rc=$?
	rm -f "$tmp"
	[ "$rc" -eq 0 ]
}

test_case "clean files pass (ORO_AGENT.md, README.md)" test_clean_files_pass
test_case "Claude-workers phrasing fails" test_bad_phrase_fails
test_case "self-check passes (script checks itself)" test_self_check_passes
test_case "compat sections excluded from scan" test_compat_section_excluded

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
