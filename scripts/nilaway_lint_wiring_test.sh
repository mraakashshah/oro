#!/usr/bin/env bash
# Regression test for blocking NilAway lint wiring.

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
makefile="${repo_root}/Makefile"
qg_script="${repo_root}/scripts/quality_gate.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

[ -f "$makefile" ] || fail "Makefile not found"
[ -f "$qg_script" ] || fail "scripts/quality_gate.sh not found"

make_nilaway_output="$(cd "$repo_root" && make -n nilaway 2>&1 || true)"
if ! grep -q 'nilaway .*pretty-print=false.*exclude-test-files.*include-pkgs=oro.*\./cmd/\.\.\..*\./internal/\.\.\..*\./pkg/\.\.\.' <<<"$make_nilaway_output"; then
	fail "make -n nilaway does not invoke the expected NilAway command"
fi

lint_output="$(cd "$repo_root" && make -n lint 2>&1 || true)"
if ! grep -q 'golangci-lint run --timeout 5m' <<<"$lint_output"; then
	fail "make -n lint does not run golangci-lint"
fi
if ! grep -q 'make.*nilaway' <<<"$lint_output"; then
	fail "make -n lint does not run the nilaway target"
fi

if ! grep -q '"nilaway"[[:space:]]*"nilaway .*pretty-print=false.*exclude-test-files.*include-pkgs=oro.*\./cmd/\.\.\..*\./internal/\.\.\..*\./pkg/\.\.\."' "$qg_script"; then
	fail "Go tier 2 does not include a blocking NilAway check"
fi

echo "PASS: NilAway is wired into blocking lint surfaces"
