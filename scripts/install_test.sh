#!/usr/bin/env bash
# install_test.sh — Tests for scripts/install.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SCRIPT="${SCRIPT_DIR}/install.sh"

PASSED=0
FAILED=0

pass() {
	echo "  PASS: $1"
	PASSED=$((PASSED + 1))
}
fail() {
	echo "  FAIL: $1 — $2"
	FAILED=$((FAILED + 1))
}

echo "=== install.sh test suite ==="
echo ""

# ── Test 1: Syntax check ────────────────────────────────────────────────────
echo "Test 1: bash -n syntax check"
if bash -n "${INSTALL_SCRIPT}" 2>&1; then
	pass "syntax is valid"
else
	fail "syntax check" "bash -n reported errors"
fi

# ── Test 2: detect_platform returns valid value ──────────────────────────────
echo "Test 2: detect_platform returns darwin_arm64 or darwin_amd64"
# Source the script to access functions (it won't run main because of the guard)
platform=$(bash -c "source '${INSTALL_SCRIPT}' && detect_platform")
case "${platform}" in
darwin_arm64 | darwin_amd64)
	pass "detect_platform returned '${platform}'"
	;;
*)
	fail "detect_platform" "unexpected value '${platform}'"
	;;
esac

# ── Test 3: --help flag works ────────────────────────────────────────────────
echo "Test 3: --help flag exits cleanly and prints usage"
help_output=$(bash "${INSTALL_SCRIPT}" --help 2>&1)
if echo "${help_output}" | grep -q "dry-run"; then
	pass "--help prints usage with --dry-run mention"
else
	fail "--help" "output did not contain expected usage text"
fi

# ── Test 4: --dry-run prints actions without executing ───────────────────────
echo "Test 4: --dry-run mode prints actions without executing"
dry_output=$(bash "${INSTALL_SCRIPT}" --dry-run --version v0.1.0 2>&1)

# Should contain dry-run markers
if echo "${dry_output}" | grep -q "\[dry-run\]"; then
	pass "dry-run output contains [dry-run] markers"
else
	fail "dry-run markers" "output missing [dry-run] prefixes"
fi

# Should NOT have actually downloaded anything
if echo "${dry_output}" | grep -q "curl -fsSL"; then
	pass "dry-run shows curl command without executing"
else
	fail "dry-run curl" "did not print the curl command"
fi

# Should mention all three binaries
for binary in oro oro-dash oro-search-hook; do
	if echo "${dry_output}" | grep -q "${binary}"; then
		pass "dry-run mentions ${binary}"
	else
		fail "dry-run ${binary}" "output missing reference to ${binary}"
	fi
done

# ── Test 5: --version flag sets version correctly ────────────────────────────
echo "Test 5: --version v0.2.0 uses correct version in URL"
ver_output=$(bash "${INSTALL_SCRIPT}" --dry-run --version v0.2.0 2>&1)
if echo "${ver_output}" | grep -q "oro_0.2.0_darwin"; then
	pass "--version v0.2.0 strips v prefix in archive name"
else
	fail "--version" "archive name did not contain 'oro_0.2.0_darwin'"
fi

# ── Test 6: Unknown flag produces error ──────────────────────────────────────
echo "Test 6: Unknown flag produces error"
# Capture both stdout and stderr; allow non-zero exit
bogus_output=$(bash "${INSTALL_SCRIPT}" --bogus 2>&1 || true)
if echo "${bogus_output}" | grep -q "Unknown option"; then
	pass "unknown flag reports error"
else
	fail "unknown flag" "did not report error for --bogus"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: ${PASSED} passed, ${FAILED} failed ==="
if [[ "${FAILED}" -gt 0 ]]; then
	exit 1
fi
