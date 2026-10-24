#!/usr/bin/env bash
# Exercise the real Makefile and gate launchers without running their workloads.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/oro-toolchain-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT
failures=0

assert_equal() {
	if [ "$1" != "$2" ]; then
		printf 'FAIL: %s: got %q, want %q\n' "$3" "$1" "$2" >&2
		failures=$((failures + 1))
	fi
}

test_toolchain_selection() {
	local fixture="$test_root/project" bin="$test_root/bin" pin override expected actual surface fallback
	local original_path="$PATH"
	mkdir -p "$fixture/scripts" "$bin"
	cp "$repo_root/scripts/quality_gate.sh" "$fixture/scripts/quality_gate.sh"
	cat >"$test_root/probe.mk" <<'EOF'
.PHONY: toolchain-probe
toolchain-probe:
	@go version
EOF
	cat >"$bin/go" <<'EOF'
#!/bin/sh
# Stand in for a newer ambient Go; capture the environment it actually receives.
printf '%s\n' "${GOTOOLCHAIN:-ambient-go1.27.3}"
EOF
	cat >"$bin/sleep" <<'EOF'
#!/bin/sh
printf '%s\n' "${GOTOOLCHAIN:-ambient-go1.27.3}" >"$TOOLCHAIN_REPORT"
EOF
	chmod +x "$bin/go" "$bin/sleep"
	for pin in go1.26.10 go1.26.11; do
		printf 'module fixture\n\ngo 1.26.10\n\ntoolchain %s\n' "$pin" >"$fixture/go.mod"
		for override in unset empty local go1.27.3; do
			expected="$pin"
			case "$override" in local | go1.27.3) expected="$override" ;; esac
			for surface in make gate; do
				actual="$(
					cd "$fixture"
					unset GOTOOLCHAIN ORO_QG_ACTIVE_PID
					case "$override" in
					empty) export GOTOOLCHAIN='' ;;
					unset) ;;
					*) export GOTOOLCHAIN="$override" ;;
					esac
					export PATH="$bin:$original_path"
					if [ "$surface" = make ]; then
						make --no-print-directory -s -f "$repo_root/Makefile" -f "$test_root/probe.mk" toolchain-probe
					else
						export TOOLCHAIN_REPORT="$test_root/report"
						export ORO_QG_PHASE_MARKER_DIR="$test_root/markers"
						export ORO_QG_REPO_ROOT_OVERRIDE="$fixture"
						export ORO_QG_MAIN_SLEEP=0.01 ORO_QG_SERIAL_SLEEP=0
						bash scripts/quality_gate.sh >"$test_root/gate.log" 2>&1 || {
							cat "$test_root/gate.log" >&2
							exit 1
						}
						cat "$TOOLCHAIN_REPORT"
					fi
				)"
				assert_equal "$actual" "$expected" "$surface pin=$pin override=$override"
			done
		done
	done

	# Go permits modules without a toolchain directive; use their minimum version.
	printf 'module fixture\n\ngo 1.26.10\n' >"$fixture/go.mod"
	actual="$(cd "$fixture" && env -u GOTOOLCHAIN PATH="$bin:$original_path" make --no-print-directory -s -f "$repo_root/Makefile" -f "$test_root/probe.mk" toolchain-probe)"
	assert_equal "$actual" go1.26.10 'make module minimum fallback'
	for fallback in minimum no-module; do
		expected=go1.26.10
		if [ "$fallback" = no-module ]; then
			mv "$fixture/go.mod" "$fixture/module.fixture"
			expected=ambient-go1.27.3
		fi
		actual="$(
			cd "$fixture"
			env -u GOTOOLCHAIN -u ORO_QG_ACTIVE_PID PATH="$bin:$original_path" \
				TOOLCHAIN_REPORT="$test_root/report" ORO_QG_PHASE_MARKER_DIR="$test_root/markers" \
				ORO_QG_REPO_ROOT_OVERRIDE="$fixture" ORO_QG_MAIN_SLEEP=0.01 ORO_QG_SERIAL_SLEEP=0 \
				bash scripts/quality_gate.sh >"$test_root/gate.log" 2>&1 || {
				cat "$test_root/gate.log" >&2
				exit 1
			}
			cat "$test_root/report"
		)"
		assert_equal "$actual" "$expected" "gate $fallback fallback"
	done
	actual="$(awk '$1 == "go" { print $2 }' "$repo_root/go.mod")"
	assert_equal "$actual" 1.26.10 'repository security minimum'
	actual="$(awk '$1 == "toolchain" { print $2 }' "$repo_root/go.mod")"
	# Go removes a toolchain directive identical to the minimum during tidy.
	assert_equal "${actual:-go1.26.10}" go1.26.10 'repository preferred toolchain'
}

test_toolchain_selection
[ "$failures" -eq 0 ] || exit 1
echo 'PASS: module toolchain selection and explicit overrides'
