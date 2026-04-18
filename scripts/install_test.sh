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

# ── Test 7: bundle_libs — dylibs copied to prefix/lib/ with mode 0644 ────────
echo "Test 7: bundle_libs copies dylibs to prefix/lib/ with mode 0644 and codesigns each"

BTEST_TMPDIR=$(mktemp -d)
FAKE_STAGE="${BTEST_TMPDIR}/stage"
FAKE_TGZ="${BTEST_TMPDIR}/fake.tar.gz"
MOCK_BIN="${BTEST_TMPDIR}/mockbin"
CODESIGN_LOG="${BTEST_TMPDIR}/codesign.log"
PREFIX_DIR="${BTEST_TMPDIR}/prefix"

mkdir -p "${FAKE_STAGE}" "${MOCK_BIN}" "${PREFIX_DIR}"

# Create placeholder files (empty stubs — install just copies bytes)
for f in oro oro-dash oro-search-hook libonnxruntime.dylib libtokenizers.dylib sqlite-vec.dylib; do
	printf '\x00' >"${FAKE_STAGE}/${f}"
done

tar -czf "${FAKE_TGZ}" -C "${FAKE_STAGE}" \
	oro oro-dash oro-search-hook \
	libonnxruntime.dylib libtokenizers.dylib sqlite-vec.dylib

# Mock codesign writes each invocation to CODESIGN_LOG
printf '#!/usr/bin/env bash\necho "codesign $*" >> "%s"\n' "${CODESIGN_LOG}" >"${MOCK_BIN}/codesign"
chmod +x "${MOCK_BIN}/codesign"

# Run installer with tarball override and custom prefix
if _ORO_TARBALL_OVERRIDE="${FAKE_TGZ}" PATH="${MOCK_BIN}:${PATH}" \
	bash "${INSTALL_SCRIPT}" --prefix "${PREFIX_DIR}" --version v0.1.0 >/dev/null 2>&1; then
	pass "install.sh succeeded with --prefix and tarball override"
else
	fail "install.sh run" "non-zero exit with --prefix and tarball override"
fi

# Assert dylibs landed in prefix/lib/ with mode 0644
for lib in libonnxruntime.dylib libtokenizers.dylib sqlite-vec.dylib; do
	LIB_PATH="${PREFIX_DIR}/lib/${lib}"
	if [[ -f "${LIB_PATH}" ]]; then
		pass "${lib} present in prefix/lib/"
	else
		fail "${lib} missing" "${LIB_PATH} not found"
	fi
	perms=$(stat -f '%Lp' "${LIB_PATH}" 2>/dev/null || echo "unknown")
	if [[ "${perms}" == "644" ]]; then
		pass "${lib} has mode 0644"
	else
		fail "${lib} permissions" "expected 644 got ${perms}"
	fi
done

# Assert codesign was invoked for each dylib
for lib in libonnxruntime.dylib libtokenizers.dylib sqlite-vec.dylib; do
	if grep -q "${lib}" "${CODESIGN_LOG}" 2>/dev/null; then
		pass "codesign invoked for ${lib}"
	else
		fail "codesign missing" "${lib} not found in codesign log"
	fi
done

rm -rf "${BTEST_TMPDIR}"

# ── Test 8: sqlite-vec missing in tarball → warning logged, install succeeds ──
echo "Test 8: missing sqlite-vec.dylib in tarball logs warning and install continues"

T8_TMPDIR=$(mktemp -d)
T8_STAGE="${T8_TMPDIR}/stage"
T8_TGZ="${T8_TMPDIR}/fake.tar.gz"
T8_PREFIX="${T8_TMPDIR}/prefix"

mkdir -p "${T8_STAGE}" "${T8_PREFIX}"

# Tarball WITHOUT sqlite-vec.dylib
for f in oro oro-dash oro-search-hook libonnxruntime.dylib libtokenizers.dylib; do
	printf '\x00' >"${T8_STAGE}/${f}"
done
tar -czf "${T8_TGZ}" -C "${T8_STAGE}" \
	oro oro-dash oro-search-hook libonnxruntime.dylib libtokenizers.dylib

t8_output=$(_ORO_TARBALL_OVERRIDE="${T8_TGZ}" \
	bash "${INSTALL_SCRIPT}" --prefix "${T8_PREFIX}" --version v0.1.0 2>&1) || true
t8_rc=$?

if [[ ${t8_rc} -eq 0 ]]; then
	pass "install succeeds even when sqlite-vec.dylib absent"
else
	fail "install exit code" "expected 0 (non-fatal), got ${t8_rc}"
fi

if echo "${t8_output}" | grep -qi "sqlite-vec\|skipping\|unavailable"; then
	pass "warning logged when sqlite-vec.dylib absent"
else
	fail "warning missing" "expected warning about missing sqlite-vec.dylib"
fi

if [[ ! -f "${T8_PREFIX}/lib/sqlite-vec.dylib" ]]; then
	pass "sqlite-vec.dylib not present when absent from tarball"
else
	fail "unexpected file" "sqlite-vec.dylib should not be installed if absent from tarball"
fi

rm -rf "${T8_TMPDIR}"

# ── test_sqlite_vec_bundled — sqlite-vec.dylib installed 0644, codesigned ─────
test_sqlite_vec_bundled() {
	echo "Test 9: test_sqlite_vec_bundled — sqlite-vec.dylib mode 0644 + adhoc codesign"

	T9_TMPDIR=$(mktemp -d)
	T9_STAGE="${T9_TMPDIR}/stage"
	T9_TGZ="${T9_TMPDIR}/fake.tar.gz"
	T9_MOCK="${T9_TMPDIR}/mockbin"
	T9_SIGN_LOG="${T9_TMPDIR}/codesign.log"
	T9_PREFIX="${T9_TMPDIR}/prefix"

	mkdir -p "${T9_STAGE}" "${T9_MOCK}" "${T9_PREFIX}"

	# Tarball with sqlite-vec.dylib (the name shipped by vendor-sqlite-vec)
	for f in oro oro-dash oro-search-hook sqlite-vec.dylib; do
		printf '\x00' >"${T9_STAGE}/${f}"
	done
	tar -czf "${T9_TGZ}" -C "${T9_STAGE}" \
		oro oro-dash oro-search-hook sqlite-vec.dylib

	# Mock codesign records each invocation
	printf '#!/usr/bin/env bash\necho "codesign $*" >> "%s"\n' "${T9_SIGN_LOG}" >"${T9_MOCK}/codesign"
	chmod +x "${T9_MOCK}/codesign"

	if _ORO_TARBALL_OVERRIDE="${T9_TGZ}" PATH="${T9_MOCK}:${PATH}" \
		bash "${INSTALL_SCRIPT}" --prefix "${T9_PREFIX}" --version v0.1.0 >/dev/null 2>&1; then
		pass "t9: install.sh succeeded"
	else
		fail "t9: install.sh run" "non-zero exit"
	fi

	# Must land at prefix/lib/sqlite-vec.dylib with mode 0644
	T9_LIB="${T9_PREFIX}/lib/sqlite-vec.dylib"
	if [[ -f "${T9_LIB}" ]]; then
		pass "t9: sqlite-vec.dylib present in prefix/lib/"
	else
		fail "t9: sqlite-vec.dylib missing" "${T9_LIB} not found"
	fi

	t9_perms=$(stat -f '%Lp' "${T9_LIB}" 2>/dev/null || echo "unknown")
	if [[ "${t9_perms}" == "644" ]]; then
		pass "t9: sqlite-vec.dylib has mode 0644"
	else
		fail "t9: sqlite-vec.dylib permissions" "expected 644 got ${t9_perms}"
	fi

	# codesign must have been called for sqlite-vec.dylib
	if grep -q "sqlite-vec.dylib" "${T9_SIGN_LOG}" 2>/dev/null; then
		pass "t9: codesign invoked for sqlite-vec.dylib"
	else
		fail "t9: codesign missing" "sqlite-vec.dylib not found in codesign log"
	fi

	rm -rf "${T9_TMPDIR}"
}
test_sqlite_vec_bundled

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: ${PASSED} passed, ${FAILED} failed ==="
if [[ "${FAILED}" -gt 0 ]]; then
	exit 1
fi
