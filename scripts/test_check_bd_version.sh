#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PASS=0
FAIL=0

test_case() {
	local name="$1"
	local fn="$2"

	printf '> %-58s' "$name"
	if "$fn"; then
		printf 'PASS\n'
		PASS=$((PASS + 1))
	else
		printf 'FAIL\n'
		FAIL=$((FAIL + 1))
	fi
}

make_bd_shim() {
	local dir="$1"
	local version_output="$2"
	mkdir -p "$dir/bin"
	cat >"$dir/bin/bd" <<EOF
#!/bin/sh
if [ "\${1:-}" = "--version" ]; then
	printf '%s\n' "$version_output"
	exit 0
fi
echo "unexpected bd args: \$*" >&2
exit 64
EOF
	chmod +x "$dir/bin/bd"
}

run_check_with_bd_version() {
	local version_output="$1"
	shift
	local tmpdir output result
	tmpdir=$(mktemp -d)
	make_bd_shim "$tmpdir" "$version_output"

	set +e
	output=$(PATH="$tmpdir/bin:$PATH" "$REPO_ROOT/scripts/check-bd-version.sh" "$@" 2>&1)
	result=$?
	set -e

	rm -rf "$tmpdir"
	printf '%s' "$output"
	return "$result"
}

run_check_without_bd() {
	local tmpdir output result
	tmpdir=$(mktemp -d)

	set +e
	output=$(PATH="$tmpdir:/usr/bin:/bin" "$REPO_ROOT/scripts/check-bd-version.sh" "$@" 2>&1)
	result=$?
	set -e

	rm -rf "$tmpdir"
	printf '%s' "$output"
	return "$result"
}

test_accepts_matching_major_minor() {
	local output
	(
		cd /
		output=$(run_check_with_bd_version "bd version 1.0.2 (Homebrew)")
	)
}

test_accepts_patch_drift_with_same_major_minor() {
	local output
	output=$(run_check_with_bd_version "bd version 1.0.99+abc-dirty")
}

test_rejects_major_minor_drift_with_explicit_message() {
	local output result
	set +e
	output=$(run_check_with_bd_version "bd version v1.1.0")
	result=$?
	set -e

	if [ "$result" -eq 0 ]; then
		echo "expected drift to fail"
		return 1
	fi

	echo "$output" | grep -q "bd version drifted from 1.0 to 1.1; reinstall pinned version or restart from Phase 0"
}

test_override_allows_major_minor_drift() {
	local output
	output=$(run_check_with_bd_version "bd version v1.1.0" --ignore-version-drift)
	echo "$output" | grep -q "bd version drift override accepted: pinned 1.0, current 1.1"
}

test_rejects_missing_bd() {
	local output result
	set +e
	output=$(run_check_without_bd)
	result=$?
	set -e

	if [ "$result" -eq 0 ]; then
		echo "expected missing bd to fail"
		return 1
	fi

	echo "$output" | grep -q "bd not found on PATH; install pinned bd version 1.0.x before Phase 8 migration"
}

test_rejects_unparseable_current_version() {
	local output result
	set +e
	output=$(run_check_with_bd_version "bd version bananas")
	result=$?
	set -e

	if [ "$result" -eq 0 ]; then
		echo "expected invalid SemVer to fail"
		return 1
	fi

	echo "$output" | grep -q "could not parse bd --version output: bd version bananas"
}

test_rejects_unknown_flag() {
	local output result
	set +e
	output=$(run_check_with_bd_version "bd version 1.0.2" --wat)
	result=$?
	set -e

	if [ "$result" -eq 0 ]; then
		echo "expected unknown flag to fail"
		return 1
	fi

	echo "$output" | grep -q "unknown flag: --wat"
}

test_case "accepts matching pinned MAJOR.MINOR" test_accepts_matching_major_minor
test_case "accepts patch drift within pinned MAJOR.MINOR" test_accepts_patch_drift_with_same_major_minor
test_case "rejects MAJOR.MINOR drift with explicit message" test_rejects_major_minor_drift_with_explicit_message
test_case "override allows MAJOR.MINOR drift" test_override_allows_major_minor_drift
test_case "rejects missing bd" test_rejects_missing_bd
test_case "rejects unparseable current SemVer" test_rejects_unparseable_current_version
test_case "rejects unknown flag" test_rejects_unknown_flag

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
