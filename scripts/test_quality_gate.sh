#!/usr/bin/env bash
# =============================================================================
# Test harness for quality_gate.sh config-driven behavior
# =============================================================================

set -euo pipefail

if [ -n "${LC_ALL:-}" ] && ! locale -a 2>/dev/null | grep -qx "$LC_ALL"; then
	export LC_ALL=C
	export LANG=C
fi

# The bootstrap regression deliberately invokes the gate with PATH restricted
# to macOS system directories. The rest of this harness uses project tools, so
# restore Homebrew's tool directory when the harness itself was started with
# that restricted PATH.
if [ -d /opt/homebrew/bin ]; then
	PATH="/opt/homebrew/bin:$PATH"
	export PATH
fi

GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

PASS=0
FAIL=0

test_case() {
	local name="$1"
	local test_fn="$2"

	printf '%b▶%b %-50s' "$BLUE" "$NC" "$name"

	set +e
	$test_fn
	local result=$?
	set -e

	if [ $result -eq 0 ]; then
		printf '%b✓ PASS%b\n' "$GREEN" "$NC"
		PASS=$((PASS + 1))
	else
		printf '%b✗ FAIL%b\n' "$RED" "$NC"
		FAIL=$((FAIL + 1))
	fi
}

# Test 1: quality_gate.sh reads .oro/config.yaml when present
# shellcheck disable=SC2317,SC2329
test_reads_config_when_present() {
	local tmpdir oldpwd
	tmpdir=$(mktemp -d)
	oldpwd="$PWD"

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	cd "$tmpdir"

	# Create minimal Go project with only one formatter in config
	cat >go.mod <<'EOF'
module test
go 1.22
EOF

	mkdir -p .oro
	cat >.oro/config.yaml <<'EOF'
languages:
  go:
    formatters:
      - gofumpt
EOF

	# Copy quality_gate.sh
	cp "$SCRIPT_DIR/quality_gate.sh" .

	# Run and capture output
	output=$(./quality_gate.sh 2>&1 || true)

	# Verify it's reading from config
	# When reading config, it should ONLY run gofumpt (not goimports)
	# The current hardcoded version runs both gofumpt and goimports
	if echo "$output" | grep -q "gofumpt" && ! echo "$output" | grep -q "goimports"; then
		cd "$oldpwd"
		return 0
	else
		echo "Expected quality_gate.sh to read from .oro/config.yaml and only run gofumpt"
		echo "Output should have gofumpt but NOT goimports"
		cd "$oldpwd"
		return 1
	fi
}

# Test 2: quality_gate.sh falls back to hardcoded when config missing
# shellcheck disable=SC2317,SC2329
test_fallback_when_config_missing() {
	local tmpdir oldpwd
	tmpdir=$(mktemp -d)
	oldpwd="$PWD"

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	cd "$tmpdir"

	# Create minimal Go project (no .oro/config.yaml)
	cat >go.mod <<'EOF'
module test
go 1.22
EOF

	# Copy quality_gate.sh
	cp "$SCRIPT_DIR/quality_gate.sh" .

	# Run and verify it works with hardcoded checks
	output=$(./quality_gate.sh 2>&1 || true)

	# Should still detect Go and run both formatters (hardcoded behavior)
	if echo "$output" | grep -q "Detected: Go project" && echo "$output" | grep -q "gofumpt" && echo "$output" | grep -q "goimports"; then
		cd "$oldpwd"
		return 0
	else
		echo "Expected quality_gate.sh to fall back to hardcoded detection with both formatters"
		cd "$oldpwd"
		return 1
	fi
}

# Test 3: Tool not installed results in SKIP with warning
# shellcheck disable=SC2317,SC2329
test_skip_when_tool_missing() {
	local tmpdir oldpwd
	tmpdir=$(mktemp -d)
	oldpwd="$PWD"

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	cd "$tmpdir"

	# Create config with non-existent tool alongside existing tool
	mkdir -p .oro
	cat >.oro/config.yaml <<'EOF'
languages:
  go:
    formatters:
      - gofumpt
      - nonexistent-formatter-xyz
EOF

	cat >go.mod <<'EOF'
module test
go 1.22
EOF

	# Copy quality_gate.sh
	cp "$SCRIPT_DIR/quality_gate.sh" .

	# Run and verify SKIP behavior
	output=$(./quality_gate.sh 2>&1 || true)

	# Should show SKIP for missing tool (doesn't matter if gate passes/fails due to other checks)
	# The important thing is: missing tool shows SKIP, not FAIL
	if echo "$output" | grep -q "nonexistent-formatter-xyz.*SKIP"; then
		cd "$oldpwd"
		return 0
	else
		echo "Expected SKIP when tool not installed"
		echo "Output did not contain 'nonexistent-formatter-xyz.*SKIP'"
		cd "$oldpwd"
		return 1
	fi
}

# Test: startup hygiene removes only terminal-escape artifacts at the repo root.
# shellcheck disable=SC2317,SC2329
test_repo_root_rejects_terminal_escape_artifacts() {
	local tmpdir artifact normal_dir live_lock ordinary_file harness
	tmpdir=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf -- '$tmpdir'" RETURN

	artifact="$tmpdir"/$'\033]8;;file:artifact'
	normal_dir="$tmpdir/normal"
	live_lock="$tmpdir/.oro-quality-gate.lock"
	ordinary_file="$tmpdir/ordinary-file"
	harness="$tmpdir/sweep.sh"
	mkdir -p "$artifact" "$normal_dir" "$live_lock"
	: >"$ordinary_file"

	{
		printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
		awk '/^sweep_repo_root_escape_artifacts\(\) \{/{capture=1} capture {print} capture && /^}/{exit}' "$SCRIPT_DIR/quality_gate.sh"
		printf '%s\n' "sweep_repo_root_escape_artifacts \"\$REPO_ROOT\""
	} >"$harness"
	chmod +x "$harness"

	REPO_ROOT="$tmpdir" "$harness"
	if [ -e "$artifact" ] || [ ! -d "$normal_dir" ] || [ ! -d "$live_lock" ] || [ ! -f "$ordinary_file" ]; then
		echo 'Expected only the OSC-8 artifact directory to be removed'
		return 1
	fi
}

# Test: the checked-in gate itself is acceptable to its shfmt lane.
# shellcheck disable=SC2317,SC2329
test_quality_gate_shell_source_is_shfmt_clean() {
	shfmt -ln bash -d "$SCRIPT_DIR/quality_gate.sh"
}

# =============================================================================
# Trap EXIT Tests (oro-bl44): mutation testing cleanup on interrupt
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Test: quality_gate.sh run_go_mutation_test has trap EXIT handler
# shellcheck disable=SC2317,SC2329
test_quality_gate_mutation_trap_present() {
	# Extract run_go_mutation_test function body (up to 60 lines after definition)
	# and verify a trap EXIT handler is present
	if grep -A 90 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | grep -q 'trap.*EXIT'; then
		return 0
	fi
	echo "FAIL: quality_gate.sh run_go_mutation_test() has no trap EXIT handler"
	echo "  Mutated source files will remain on disk if go-mutesting is killed"
	return 1
}

# shellcheck disable=SC2317,SC2329
test_beadstore_import_boundary_rejects_forbidden_imports() {
	local tmpdir oldpwd output
	tmpdir=$(mktemp -d)
	oldpwd="$PWD"

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	mkdir -p "$tmpdir/scripts" "$tmpdir/pkg/beadstore"
	cp "$SCRIPT_DIR/check-beadstore-imports.sh" "$tmpdir/scripts/"
	cat >"$tmpdir/pkg/beadstore/bad.go" <<'EOF'
package beadstore

import (
	"oro/pkg/dispatcher"
	"oro/pkg/ops"
	"oro/pkg/worker"
)

var _ = dispatcher.ErrLocked
EOF

	cd "$tmpdir"
	set +e
	output=$(scripts/check-beadstore-imports.sh 2>&1)
	result=$?
	set -e
	cd "$oldpwd"

	if [ "$result" -eq 0 ]; then
		echo "Expected forbidden dispatcher import to fail"
		return 1
	fi
	for expected in \
		'pkg/beadstore/bad.go:4:.*oro/pkg/dispatcher' \
		'pkg/beadstore/bad.go:5:.*oro/pkg/ops' \
		'pkg/beadstore/bad.go:6:.*oro/pkg/worker'; do
		if ! echo "$output" | grep -q "$expected"; then
			echo "Expected output to include $expected, got:"
			echo "$output"
			return 1
		fi
	done
}

# shellcheck disable=SC2317,SC2329
test_beadstore_import_boundary_allows_protocol_import() {
	local tmpdir oldpwd
	tmpdir=$(mktemp -d)
	oldpwd="$PWD"

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	mkdir -p "$tmpdir/scripts" "$tmpdir/pkg/beadstore"
	cp "$SCRIPT_DIR/check-beadstore-imports.sh" "$tmpdir/scripts/"
	cat >"$tmpdir/pkg/beadstore/good.go" <<'EOF'
package beadstore

import "oro/pkg/protocol"

var _ = protocol.Bead{}
EOF

	cd "$tmpdir"
	scripts/check-beadstore-imports.sh
	cd "$oldpwd"
}

# Test: quality_gate.sh mutation cleanup preserves pre-existing unstaged edits.
# shellcheck disable=SC2317,SC2329
test_quality_gate_mutation_cleanup_preserves_unstaged_work() {
	local mutation_body
	mutation_body=$(grep -A 100 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -100)

	if ! grep -q 'restore_go_mutation_worktree()' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh lacks restore_go_mutation_worktree helper"
		return 1
	fi
	if ! grep -A 20 'restore_go_mutation_worktree()' "$SCRIPT_DIR/quality_gate.sh" | grep -q 'git apply'; then
		echo "FAIL: restore_go_mutation_worktree does not reapply pre-existing unstaged changes"
		return 1
	fi
	if ! echo "$mutation_body" | grep -q 'pre_mutation_patch'; then
		echo "FAIL: run_go_mutation_test does not capture pre-existing unstaged changes"
		return 1
	fi
	if ! echo "$mutation_body" | grep -q 'restore_go_mutation_worktree'; then
		echo "FAIL: run_go_mutation_test does not restore via safe helper"
		return 1
	fi
	if echo "$mutation_body" | grep -q 'git checkout -- pkg/ internal/ cmd/'; then
		echo "FAIL: run_go_mutation_test still directly resets source directories"
		echo "  Direct reset wipes pre-existing unstaged work; use restore_go_mutation_worktree."
		return 1
	fi
	return 0
}

# Test: quality_gate.sh mutation cleanup restores hook/temp side effects.
# shellcheck disable=SC2317,SC2329
test_quality_gate_mutation_restores_side_effects() {
	local script="$SCRIPT_DIR/quality_gate.sh"
	local generated="$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"
	local mutation_body trap_line
	mutation_body=$(grep -A 130 'run_go_mutation_test()' "$script" | head -130)
	trap_line=$(echo "$mutation_body" | grep "trap .*EXIT" | head -1 || true)

	for target in "$script" "$generated"; do
		if ! grep -q 'snapshot_go_mutation_side_effects()' "$target"; then
			echo "FAIL: $target lacks mutation side-effect snapshot helper"
			return 1
		fi
		if ! grep -q 'restore_go_mutation_side_effects()' "$target"; then
			echo "FAIL: $target lacks mutation side-effect restore helper"
			return 1
		fi
		if ! grep -q 'git/hooks/pre-push' "$target"; then
			echo "FAIL: $target does not protect the local pre-push hook"
			return 1
		fi
		if ! grep -q '\\*.go.tmp' "$target"; then
			echo "FAIL: $target does not clean up go-mutesting *.go.tmp artifacts"
			return 1
		fi
	done

	if [ -z "$trap_line" ] || ! echo "$trap_line" | grep -q 'restore_go_mutation_side_effects'; then
		echo "FAIL: mutation EXIT trap does not restore hook/temp side effects"
		return 1
	fi
	if ! echo "$mutation_body" | grep -q 'side_effect_snapshot'; then
		echo "FAIL: run_go_mutation_test does not capture mutation side-effect baseline"
		return 1
	fi
	if ! echo "$mutation_body" | grep -q 'restore_go_mutation_side_effects'; then
		echo "FAIL: run_go_mutation_test does not restore mutation side effects after go-mutesting"
		return 1
	fi
	return 0
}

# Test: mutation hook restore preserves symlinked pre-push hooks.
# shellcheck disable=SC2317,SC2329
test_quality_gate_mutation_restores_symlinked_pre_push_hook() {
	local tmpdir oldpwd snapshot_dir target original_target_content
	tmpdir=$(mktemp -d)
	oldpwd="$PWD"

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	mkdir -p "$tmpdir/repo/git/hooks"
	cd "$tmpdir/repo"
	git init -q

	target="$tmpdir/repo/git/hooks/pre-push"
	original_target_content='existing shared hook'
	printf '%s\n' "$original_target_content" >"$target"
	ln -s "$target" .git/hooks/pre-push

	eval "$(
		sed -n '/^go_mutation_hooks_dir()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^snapshot_go_mutation_side_effects()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^restore_go_mutation_side_effects()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
	)"

	snapshot_dir="$tmpdir/snapshot"
	snapshot_go_mutation_side_effects "$snapshot_dir"

	rm .git/hooks/pre-push
	printf '%s\n' 'generated wrapper' >.git/hooks/pre-push

	restore_go_mutation_side_effects "$snapshot_dir"

	if [ ! -L .git/hooks/pre-push ]; then
		echo "FAIL: restored .git/hooks/pre-push is not a symlink"
		cd "$oldpwd"
		return 1
	fi
	if [ "$(readlink .git/hooks/pre-push)" != "$target" ]; then
		echo "FAIL: restored symlink target changed"
		cd "$oldpwd"
		return 1
	fi
	if [ "$(cat "$target")" != "$original_target_content" ]; then
		echo "FAIL: symlink target content was overwritten"
		cd "$oldpwd"
		return 1
	fi

	cd "$oldpwd"
}

# Test: mutation restore trap preserves parent-owned QG_DIR cleanup.
# shellcheck disable=SC2317,SC2329
test_quality_gate_mutation_trap_preserves_qg_dir_cleanup() {
	local mutation_body trap_line
	mutation_body=$(grep -A 100 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -100)
	trap_line=$(echo "$mutation_body" | grep "trap .*EXIT" | head -1 || true)

	if [ -z "$trap_line" ]; then
		echo "FAIL: run_go_mutation_test does not install an EXIT trap"
		return 1
	fi
	if ! echo "$trap_line" | grep -q 'restore_go_mutation_worktree'; then
		echo "FAIL: mutation EXIT trap does not restore the Go mutation worktree"
		return 1
	fi
	if ! grep -q 'trap cleanup_qg EXIT' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: top-level QG_DIR cleanup trap is missing"
		return 1
	fi
	if echo "$trap_line" | grep -Eq 'cleanup_qg|rm -rf.*QG_DIR'; then
		echo "FAIL: mutation EXIT trap cleans QG_DIR from the background Go lane"
		echo "  Expected QG_DIR cleanup to remain owned by the top-level parent trap."
		return 1
	fi
	if ! echo "$trap_line" | grep -q "exit \"\\\$QG_EXIT_STATUS\""; then
		echo "FAIL: mutation EXIT trap does not preserve the original exit status"
		return 1
	fi
	return 0
}

# Test: Makefile mutate-go target has trap EXIT handler
# shellcheck disable=SC2317,SC2329
test_makefile_mutate_go_trap_present() {
	# Extract lines from mutate-go: target to next target and check for trap
	local target_body
	target_body=$(awk '/^mutate-go:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go:/{f=0} f' "$SCRIPT_DIR/../Makefile")
	if echo "$target_body" | grep -q 'trap'; then
		return 0
	fi
	echo "FAIL: Makefile mutate-go target has no trap handler"
	echo "  Mutated source files will remain on disk if killed mid-run"
	return 1
}

# Test: Makefile mutate-go-diff target has trap EXIT handler
# shellcheck disable=SC2317,SC2329
test_makefile_mutate_go_diff_trap_present() {
	# Extract lines from mutate-go-diff: target to next target and check for trap
	local target_body
	target_body=$(awk '/^mutate-go-diff:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go-diff:/{f=0} f' "$SCRIPT_DIR/../Makefile")
	if echo "$target_body" | grep -q 'trap'; then
		return 0
	fi
	echo "FAIL: Makefile mutate-go-diff target has no trap handler"
	echo "  Mutated source files will remain on disk if killed mid-run"
	return 1
}

# =============================================================================
# Missing main branch + crash detection (oro-xgwr)
# =============================================================================

# Test: run_go_mutation_test checks for main branch existence before diffing
# shellcheck disable=SC2317,SC2329
test_mutation_checks_main_branch_existence() {
	# The function must NOT blindly run 'git diff ... main 2>/dev/null || true'.
	# It must detect when main doesn't exist and warn/fail — not silently PASS.
	local fn_body
	fn_body=$(grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -80)

	# Must have a check for the selected mutation base (defaulting to main).
	if echo "$fn_body" | grep -qE 'rev-parse.*verify.*mutation_base|merge-base.*mutation_base|Cannot find mutation base'; then
		return 0
	fi

	echo "FAIL: run_go_mutation_test() does not check for mutation base existence"
	echo "  'git diff --name-only <base> 2>/dev/null || true' silently returns empty"
	echo "  when the base branch is absent → silent PASS (oro-xgwr)"
	return 1
}

# Test: run_go_mutation_test captures go-mutesting exit code to detect crashes
# shellcheck disable=SC2317,SC2329
test_mutation_crash_flagged_as_fail() {
	# When go-mutesting crashes (nonzero exit) with no score output,
	# the function must return nonzero (FAIL), not 0 (silent PASS).
	local fn_body
	fn_body=$(grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -80)

	# Must have exit-code capture for go-mutesting (not just swallow the exit code)
	if echo "$fn_body" | grep -qE 'mutesting_exit|go-mutesting.*\|\|.*[0-9]|\$\? .*mutesting'; then
		return 0
	fi

	echo "FAIL: run_go_mutation_test() does not capture go-mutesting exit code"
	echo "  A crashed go-mutesting (nonzero exit, no score) is indistinguishable"
	echo "  from 'no mutations possible' — both currently return 0 (PASS) (oro-xgwr)"
	return 1
}

# Test: zero generated mutants skip instead of failing a meaningless 0/0 score
# shellcheck disable=SC2317,SC2329
test_mutation_zero_total_skips() {
	local fn_body
	fn_body=$(grep -A 110 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -110)

	# shellcheck disable=SC2016
	if echo "$fn_body" | grep -q 'total is (\[0-9\]+)' &&
		echo "$fn_body" | grep -q '\[ "$total" = "0" \]' &&
		echo "$fn_body" | grep -q 'No mutations generated for changed files'; then
		return 0
	fi

	echo "FAIL: run_go_mutation_test() does not skip zero-total mutation reports"
	echo "  go-mutesting can report score 0 with total 0 for template/test-only changes"
	return 1
}

# Test: missing main branch warning appears in function output text
# shellcheck disable=SC2317,SC2329
test_mutation_missing_main_warning_message() {
	# The warning must include 'Cannot find main branch' (or similar 'main' + warning)
	if grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" |
		grep -qiE 'Cannot find main|main.*not found|main.*missing|WARNING.*main'; then
		return 0
	fi

	echo "FAIL: run_go_mutation_test() does not print a 'Cannot find main branch' warning"
	echo "  Acceptance criteria require the warning message to be present (oro-xgwr)"
	return 1
}

# shellcheck disable=SC2317,SC2329
test_mutation_default_all_contexts_skip() {
	if ! grep -q 'should_run_mutation_tests()' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh lacks should_run_mutation_tests helper"
		return 1
	fi
	if ! grep -q 'mutation disabled by default; use --mutation-testing' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: default mutation skip reason is missing"
		return 1
	fi
	if ! grep -q -- '--mutation-testing' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh lacks --mutation-testing opt-in flag"
		return 1
	fi
	local helper
	helper=$(mktemp)
	sed -n '/^should_run_mutation_tests()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh" >"$helper"
	if ORO_QG_CONTEXT=push /bin/bash -c 'source "$1"; should_run_mutation_tests' _ "$helper"; then
		echo "FAIL: push context enables mutation by default"
		rm -f "$helper"
		return 1
	fi
	if GITHUB_EVENT_NAME=push /bin/bash -c 'source "$1"; should_run_mutation_tests' _ "$helper"; then
		echo "FAIL: GitHub push event enables mutation by default"
		rm -f "$helper"
		return 1
	fi
	rm -f "$helper"
}

# shellcheck disable=SC2317,SC2329
test_mutation_opt_in_flag_runs() {
	local helper
	helper=$(sed -n '/^should_run_mutation_tests()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh")
	if echo "$helper" | grep -q 'push | pre-push'; then
		echo "FAIL: mutation helper still runs in push/pre-push context by default"
		return 1
	fi
	if echo "$helper" | grep -q 'GITHUB_EVENT_NAME.*push'; then
		echo "FAIL: mutation helper still runs for GitHub push events by default"
		return 1
	fi
	if echo "$helper" | grep -q 'ORO_RUN_MUTATION'; then
		echo "FAIL: mutation helper still honors ambient ORO_RUN_MUTATION"
		return 1
	fi
	local helper_file
	helper_file=$(mktemp)
	printf '%s\n' "$helper" >"$helper_file"
	if ORO_RUN_MUTATION=1 /bin/bash -c 'source "$1"; should_run_mutation_tests' _ "$helper_file"; then
		echo "FAIL: ORO_RUN_MUTATION=1 enables mutation without --mutation-testing"
		rm -f "$helper_file"
		return 1
	fi
	if ! QG_MUTATION_TESTING=true /bin/bash -c 'source "$1"; should_run_mutation_tests' _ "$helper_file"; then
		echo "FAIL: --mutation-testing flag marker does not enable mutation"
		rm -f "$helper_file"
		return 1
	fi
	rm -f "$helper_file"
	if ! grep -q 'go tool -n go-mutesting' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: Go mutation availability must use pinned go tool lookup"
		return 1
	fi
	if grep -q 'elif command -v go-mutesting' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: Go mutation availability still depends on PATH-only go-mutesting lookup"
		return 1
	fi
}

# shellcheck disable=SC2317,SC2329
test_pre_push_leaves_mutation_opt_in() {
	if grep -vE '^[[:space:]]*(#|echo)' "$SCRIPT_DIR/../git/hooks/pre-push" | grep -q 'ORO_RUN_MUTATION=1'; then
		echo "FAIL: pre-push hook enables mutation by default"
		return 1
	fi
	if ! grep -q 'mutation testing disabled by default' "$SCRIPT_DIR/../git/hooks/pre-push"; then
		echo "FAIL: pre-push hook does not advertise mutation default-off behavior"
		return 1
	fi
}

# =============================================================================
# Python mutation: missing main branch check (oro-xgwr)
# =============================================================================

# Test: Python run_mutation_test checks for main branch existence before diffing
# shellcheck disable=SC2317,SC2329
test_python_mutation_checks_main_branch_existence() {
	# The Python mutation function must also check for main branch existence,
	# not blindly run 'git diff ... main 2>/dev/null || true'.
	local fn_body
	fn_body=$(grep -A 40 'run_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -40)

	if echo "$fn_body" | grep -qE 'rev-parse.*verify.*mutation_base|merge-base.*mutation_base|Cannot find mutation base'; then
		return 0
	fi

	echo "FAIL: Python run_mutation_test() does not check for mutation base existence"
	echo "  Same bug as Go mutation — silent PASS when main is absent (oro-xgwr)"
	return 1
}

# Test: Python run_mutation_test prints warning when main branch missing
# shellcheck disable=SC2317,SC2329
test_python_mutation_missing_main_warning_message() {
	if grep -A 40 'run_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" |
		grep -qiE 'Cannot find main|main.*not found|main.*missing|WARNING.*main'; then
		return 0
	fi

	echo "FAIL: Python run_mutation_test() does not print a 'Cannot find main branch' warning"
	echo "  Acceptance criteria require the warning message to be present (oro-xgwr)"
	return 1
}

# =============================================================================
# Worktree ref resolution after env cleanup (oro-w6xz)
# =============================================================================

# Test: quality_gate.sh detects worktrees and provides qg_git for ref resolution
# shellcheck disable=SC2317,SC2329
test_worktree_ref_resolution_after_env_cleanup() {
	# Verify that quality_gate.sh:
	# 1. Unsets GIT_DIR/GIT_WORK_TREE (prevents hook leakage)
	# 2. Detects worktrees via .git gitlink file BEFORE the unset
	# 3. Saves worktree git dirs for later use by mutation testing
	# 4. Provides a qg_git helper that temporarily sets GIT_DIR for ref resolution

	local env_block
	env_block=$(sed -n '/^# Prevent hook env leakage/,/^unset GIT_DIR/p' "$SCRIPT_DIR/quality_gate.sh")

	# Must have: unset GIT_DIR (still needed for hook leakage prevention)
	if ! echo "$env_block" | grep -q 'unset GIT_DIR'; then
		echo "FAIL: quality_gate.sh does not unset GIT_DIR (needed for hook leakage prevention)"
		return 1
	fi

	# Must have: worktree detection via [ -f .git ] (gitlink file check)
	if ! echo "$env_block" | grep -qE '\[ -f \.git \]|-f \.git'; then
		echo "FAIL: quality_gate.sh does not detect worktrees via .git gitlink file"
		echo "  Worktrees need git-common-dir saved before GIT_DIR is unset"
		return 1
	fi

	# Must have: worktree-specific git-dir saved to a shell variable (not exported as GIT_DIR)
	if ! echo "$env_block" | grep -qE 'git rev-parse --git-dir'; then
		echo "FAIL: quality_gate.sh does not save worktree git-dir for worktree ref resolution"
		return 1
	fi

	# Must have: qg_git helper function that uses saved worktree git-dir
	if ! grep -q 'qg_git()' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not define qg_git() helper for worktree ref resolution"
		echo "  Mutation testing needs qg_git to resolve refs without leaking GIT_DIR"
		return 1
	fi

	# Mutation testing must use qg_git (not bare git) for rev-parse and diff
	local go_mutation
	go_mutation=$(grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -80)
	if echo "$go_mutation" | grep -qE '^\s+if ! git rev-parse --verify main'; then
		echo "FAIL: run_go_mutation_test() uses bare 'git' instead of 'qg_git' for rev-parse"
		echo "  In worktrees, qg_git sets GIT_DIR temporarily for ref resolution"
		return 1
	fi

	return 0
}

# Test: qg_git rev-parse --verify main works in a real worktree
# shellcheck disable=SC2317,SC2329
test_worktree_rev_parse_main_functional() {
	# Functional test: create a real git repo + worktree, simulate hook env,
	# run the QG env setup + qg_git helper, then verify ref/branch resolution works.
	#
	# The bug: after 'unset GIT_DIR GIT_WORK_TREE', the worktree's
	# .git/worktrees/<name> dir has no refs/heads/main — it relies on
	# commondir. qg_git temporarily sets GIT_DIR for the git command only.
	local tmpdir
	tmpdir=$(mktemp -d)

	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	# Create a minimal git repo with a main branch
	git init --initial-branch=main "$tmpdir/repo" >/dev/null 2>&1
	git -C "$tmpdir/repo" commit --allow-empty -m "init" >/dev/null 2>&1

	# Create a worktree
	git -C "$tmpdir/repo" worktree add "$tmpdir/wt" -b test-branch >/dev/null 2>&1

	# Verify .git is a gitlink in the worktree
	if [ ! -f "$tmpdir/wt/.git" ]; then
		echo "FAIL: test setup broken — .git is not a gitlink file in worktree"
		git -C "$tmpdir/repo" worktree remove "$tmpdir/wt" 2>/dev/null || true
		return 1
	fi

	# Extract the env setup block (from "# Prevent" comment through "unset GIT_DIR")
	# and the qg_git function from quality_gate.sh
	local env_setup qg_git_fn
	env_setup=$(sed -n '/^# Prevent hook/,/^unset GIT_DIR/p' "$SCRIPT_DIR/quality_gate.sh")
	qg_git_fn=$(sed -n '/^qg_git()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh")

	# If no env setup extracted, fall back to just the unset line
	if [ -z "$env_setup" ]; then
		env_setup=$(grep -m1 'unset GIT_DIR' "$SCRIPT_DIR/quality_gate.sh")
	fi

	# Run the env setup + qg_git in a subshell simulating hook context
	local result
	result=$(bash -c "
		set -euo pipefail
		# Simulate hook env leakage (GIT_DIR set to worktree-specific git dir)
		export GIT_DIR=\"$tmpdir/repo/.git/worktrees/wt\"
		export GIT_WORK_TREE=\"$tmpdir/wt\"

		# Change to worktree directory
		cd \"$tmpdir/wt\"

		# Run the env setup block from quality_gate.sh
		$env_setup

		# Define qg_git helper
		$qg_git_fn

		# Test 1: qg_git can resolve main
		if ! qg_git rev-parse --verify main >/dev/null 2>&1; then
			echo 'FAIL: qg_git rev-parse --verify main failed in worktree'
			exit 0
		fi

		# Test 2: qg_git must preserve the current worktree branch. Using the
		# common git dir reports the primary worktree branch instead.
		if [ \"\$(qg_git branch --show-current)\" != 'test-branch' ]; then
			echo 'FAIL: qg_git branch --show-current did not preserve worktree branch'
			exit 0
		fi

		# Test 3: GIT_DIR must NOT be exported (would leak into test subprocesses)
		if env | grep -q '^GIT_DIR='; then
			echo 'FAIL: GIT_DIR is exported — it would leak into test subprocesses'
			exit 0
		fi

		echo 'PASS'
	" 2>&1)

	# Cleanup worktree
	git -C "$tmpdir/repo" worktree remove "$tmpdir/wt" 2>/dev/null || true

	if [ "$result" = "PASS" ]; then
		return 0
	else
		echo "$result"
		return 1
	fi
}

# shellcheck disable=SC2317,SC2329
test_go_mutation_limits_changed_files_to_qg_go_surface() {
	local mutation_body
	mutation_body=$(grep -A 90 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -90)

	# shellcheck disable=SC2016
	if ! echo "$mutation_body" | grep -q 'qg_git diff --name-only "$mutation_base" -- cmd/ internal/ pkg/'; then
		echo "FAIL: Go mutation changed-file discovery is not limited to cmd/internal/pkg"
		return 1
	fi
	# shellcheck disable=SC2016
	if ! echo "$mutation_body" | grep -q 'qg_git diff "$mutation_base" -- cmd/ internal/ pkg/'; then
		echo "FAIL: Go mutation touched-function discovery is not limited to cmd/internal/pkg"
		return 1
	fi
}

# shellcheck disable=SC2317,SC2329
test_go_mutation_match_pattern_anchors_touched_functions() {
	local mutation_body
	mutation_body=$(grep -A 90 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -90)

	# shellcheck disable=SC2016
	if ! echo "$mutation_body" | grep -Fq 'match_pattern="^($touched_funcs)$"'; then
		echo "FAIL: Go mutation match pattern is not anchored to exact function names"
		echo "  Unanchored names like Closed also mutate AllChildrenClosed"
		return 1
	fi
}

# Test: GIT_DIR/GIT_WORK_TREE do not leak into test subprocesses after QG env setup
# shellcheck disable=SC2317,SC2329
test_worktree_env_no_leak_to_subprocesses() {
	# After the env setup block, neither GIT_DIR nor GIT_WORK_TREE must be exported.
	# The qg_git helper sets GIT_DIR per-command (not globally), preventing leakage.
	local env_block
	env_block=$(head -25 "$SCRIPT_DIR/quality_gate.sh")

	# GIT_DIR must NOT be globally exported (would leak into test subprocesses)
	if echo "$env_block" | grep -q 'export GIT_DIR'; then
		echo "FAIL: quality_gate.sh exports GIT_DIR globally"
		echo "  GIT_DIR must not leak into test subprocesses — use qg_git for per-command isolation"
		return 1
	fi

	# GIT_WORK_TREE must NOT be re-exported after the unset
	if echo "$env_block" | grep -q 'export GIT_WORK_TREE'; then
		echo "FAIL: quality_gate.sh re-exports GIT_WORK_TREE after unsetting it"
		echo "  GIT_WORK_TREE must stay unset to prevent leakage into test subprocesses"
		return 1
	fi

	return 0
}

# =============================================================================
# Shell correctness in mutation testing (oro-koon)
# =============================================================================

# Test: No shellcheck SC2086 disable for $changed in quality_gate.sh
# shellcheck disable=SC2317,SC2329
test_no_sc2086_disable_for_changed() {
	# Look for SC2086 disables that are followed (within 2 lines) by $changed usage.
	# The BIOME_PATHS SC2086 disable is acceptable; only $changed is a problem.
	local mutation_body
	mutation_body=$(grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -80)

	# There should be no SC2086 disable inside the mutation function
	if echo "$mutation_body" | grep -q 'SC2086'; then
		echo "FAIL: run_go_mutation_test() still has a shellcheck SC2086 disable"
		echo "  \$changed should be properly quoted instead of suppressing the warning"
		return 1
	fi
	return 0
}

# Test: $changed is quoted in quality_gate.sh (not used via bare word splitting)
# shellcheck disable=SC2317,SC2329
test_quality_gate_changed_is_quoted() {
	local mutation_body
	mutation_body=$(grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | head -80)

	# Check that go-mutesting invocation does NOT use bare $changed (word-split).
	# It should use either "$changed" or an array like "${changed[@]}".
	# We look for unquoted $changed NOT preceded by a quote or array syntax.
	# shellcheck disable=SC2016
	if echo "$mutation_body" | grep -E 'go-mutesting.*[^"]\$changed[^"]|go-mutesting.*[[:space:]]\$changed$'; then
		echo "FAIL: go-mutesting is called with unquoted \$changed in quality_gate.sh"
		echo "  This causes word splitting and glob expansion"
		return 1
	fi
	return 0
}

# Test: asset staging failures fail closed instead of disappearing as a missing lane rc.
# shellcheck disable=SC2016,SC2317,SC2329
test_quality_gate_stage_assets_fail_closed() {
	if ! grep -q 'ensure_stage_assets()' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh lacks ensure_stage_assets helper"
		return 1
	fi
	if ! grep -q 'QG_STAGE_ASSETS_LOCK=""' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'QG_RUN_LOCK=""' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'trap cleanup_qg EXIT' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q "trap 'exit 130' INT" "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not clean up the stage-assets lock on exit/interrupt"
		return 1
	fi
	if ! grep -q 'acquire_quality_gate_lock()' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'local lock_dir="$REPO_ROOT/.oro-quality-gate.lock"' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'QG_RUN_LOCK="$lock_dir"' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'acquire_quality_gate_lock' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not serialize concurrent quality gates across worktrees"
		return 1
	fi
	if ! grep -q 'cmd/oro embeds _assets but Makefile stage-assets target is unavailable' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not fail closed when embedded assets need missing stage-assets"
		return 1
	fi
	local preflight_line lane_spawn_line
	preflight_line=$(grep -n "STAGE_ASSETS_ERROR=\$(ensure_stage_assets" "$SCRIPT_DIR/quality_gate.sh" | cut -d: -f1 | head -1)
	lane_spawn_line=$(grep -n 'lane_go >.*go.out' "$SCRIPT_DIR/quality_gate.sh" | cut -d: -f1 | head -1)
	if [ -z "$preflight_line" ] || [ -z "$lane_spawn_line" ] || [ "$preflight_line" -ge "$lane_spawn_line" ]; then
		echo "FAIL: quality_gate.sh must stage assets before starting background lanes"
		return 1
	fi
	if ! grep -A 8 "if ! \$STAGE_ASSETS_READY" "$SCRIPT_DIR/quality_gate.sh" | grep -q 'go.rc'; then
		echo "FAIL: lane_go does not write go.rc when parent asset staging fails"
		return 1
	fi
	local tmpdir lockdir
	tmpdir=$(mktemp -d)
	lockdir="$tmpdir/lock"
	mkdir "$lockdir"
	if ! bash -c "
		set -euo pipefail
		QG_DIR=\"$tmpdir/qg\"
		QG_STAGE_ASSETS_LOCK=\"$lockdir\"
		mkdir -p \"\$QG_DIR\"
		$(sed -n '/^cleanup_qg()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh")
		cleanup_qg
		[ ! -d \"$lockdir\" ]
	"; then
		echo "FAIL: cleanup_qg did not remove a held stage-assets lock"
		rm -rf "$tmpdir"
		return 1
	fi
	rm -rf "$tmpdir"
	if ! grep -q 'FAIL: missing lane result' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not fail closed on missing lane rc files"
		return 1
	fi
	if grep -q 'make clean-assets' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh still deletes staged embed assets"
		return 1
	fi
	return 0
}

# Test: golangci-lint must use a worktree-local cache so stale sibling
# diagnostics cannot leak into the active gate, while active findings remain.
# shellcheck disable=SC2016,SC2317,SC2329
test_golangci_lint_isolated_to_active_worktree() {
	local tmpdir fixture active sibling cache harness output
	tmpdir=$(mktemp -d)
	fixture="$tmpdir/fixture"
	active="$fixture/active"
	sibling="$fixture/sibling"
	cache="$fixture/cache"
	harness="$tmpdir/run-lint.sh"
	# shellcheck disable=SC2064
	trap "rm -rf -- '$tmpdir'" RETURN

	mkdir -p "$active/pkg/current" "$sibling/pkg/stale" "$cache"
	printf 'module fixture\n\ngo 1.22\n' >"$active/go.mod"
	printf 'package current\n' >"$active/pkg/current/current.go"
	printf 'package stale\n' >"$sibling/pkg/stale/stale.go"
	printf '%s\n%s\n' \
		"$sibling/pkg/stale/stale.go:1: stale sibling finding" \
		"$active/pkg/current/current.go:1: active finding" >"$cache/diagnostics"

	mkdir -p "$tmpdir/bin"
	cat >"$tmpdir/bin/golangci-lint" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ -f "$GOLANGCI_LINT_CACHE/diagnostics" ]; then
	cat "$GOLANGCI_LINT_CACHE/diagnostics"
fi
printf '%s\n' "$(pwd)/pkg/current/current.go:1: active finding"
exit 1
EOF
	chmod +x "$tmpdir/bin/golangci-lint"

	{
		printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
		printf 'QG_DIR=%q\n' "$tmpdir/qg"
		printf 'mkdir -p "$QG_DIR"\n'
		sed -n '/^run_golangci_lint()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		printf '%s\n' 'run_golangci_lint'
	} >"$harness"
	chmod +x "$harness"

	set +e
	output=$(cd "$active" && PATH="$tmpdir/bin:$PATH" GOLANGCI_LINT_CACHE="$cache" "$harness" 2>&1)
	local status=$?
	set -e
	if [ "$status" -eq 0 ]; then
		echo 'FAIL: fixture lint command unexpectedly succeeded'
		return 1
	fi
	if grep -Fq "$sibling/" <<<"$output"; then
		echo "FAIL: sibling worktree path leaked from lint cache: $output"
		return 1
	fi
	if ! grep -Fq "$active/" <<<"$output"; then
		echo "FAIL: active-worktree lint finding was lost: $output"
		return 1
	fi
	if ! grep -q 'run_golangci_lint()' "$SCRIPT_DIR/quality_gate.sh" || ! grep -q '"golangci-lint" "run_golangci_lint"' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: $SCRIPT_DIR/quality_gate.sh does not use the isolated golangci-lint runner"
		return 1
	fi
	if ! grep -q 'run_golangci_lint()' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo 'FAIL: generated quality gate does not define the isolated golangci-lint runner'
		return 1
	fi
}

# Test: formatter failures must retain the offending file list so distinct
# worker defects do not collapse to the same lane-only QG fingerprint.
# shellcheck disable=SC2317,SC2329
test_go_formatter_failure_prints_files() {
	local tmpdir harness output status
	tmpdir=$(mktemp -d)
	harness="$tmpdir/run-formatter.sh"
	# shellcheck disable=SC2064
	trap "rm -rf -- '$tmpdir'" RETURN

	mkdir -p "$tmpdir/bin" "$tmpdir/pkg"
	cat >"$tmpdir/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = "tool" ] && [ "$2" = "-n" ]; then
	exit 0
fi
if [ "$1" = "tool" ] && [ "$3" = "-l" ]; then
	echo "pkg/unformatted.go"
	exit 0
fi
exit 2
EOF
	chmod +x "$tmpdir/bin/go"

	{
		printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
		sed -n '/^go_formatter_check()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		printf '%s\n' 'go_formatter_check gofumpt'
	} >"$harness"
	chmod +x "$harness"

	set +e
	output=$(cd "$tmpdir" && PATH="$tmpdir/bin:$PATH" "$harness" 2>&1)
	status=$?
	set -e
	if [ "$status" -eq 0 ]; then
		echo "FAIL: formatter fixture unexpectedly passed"
		return 1
	fi
	if ! grep -Fq "pkg/unformatted.go" <<<"$output"; then
		echo "FAIL: formatter failure hid the offending file: $output"
		return 1
	fi
	if grep -q 'test -z.*go tool gofumpt -l' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo "FAIL: generated quality gate hides formatter file lists"
		return 1
	fi
}

# Test: the checked-in gate keeps scratch data under TMPDIR, uses a scoped
# golangci-lint cache, and otherwise inherits shared tool caches.
# shellcheck disable=SC2016,SC2317,SC2329
test_quality_gate_uses_scoped_lint_cache() {
	local gate="$SCRIPT_DIR/quality_gate.sh"
	local cache_override

	if ! grep -q 'QG_DIR=$(mktemp -d "${TMPDIR:-/tmp}/qg-\$\$-XXXXXX")' "$gate"; then
		echo "FAIL: quality_gate.sh does not create its scratch directory below TMPDIR"
		return 1
	fi

	for cache_override in \
		'export GOCACHE=' \
		'export GOMODCACHE=' \
		'export UV_CACHE_DIR=' \
		'GOCACHE=\$QG_DIR/'; do
		if grep -q "$cache_override" "$gate"; then
			echo "FAIL: quality_gate.sh overrides shared cache via $cache_override"
			return 1
		fi
	done
	if ! grep -q 'GOLANGCI_LINT_CACHE="\$lint_cache"' "$gate"; then
		echo 'FAIL: quality_gate.sh does not scope the golangci-lint cache'
		return 1
	fi
}

# Test: a process that times out waiting for the repo-wide QG lock must not
# clean up another process's lock directory.
# shellcheck disable=SC2016,SC2317,SC2329
test_quality_gate_run_lock_timeout_preserves_holder() {
	if ! grep -q 'local lock_dir="$REPO_ROOT/.oro-quality-gate.lock"' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'QG_RUN_LOCK="$lock_dir"' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'local lock_dir="$REPO_ROOT/.oro-quality-gate.lock"' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go" ||
		! grep -q 'QG_RUN_LOCK="$lock_dir"' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo "FAIL: quality gate run lock is not promoted to QG_RUN_LOCK only after acquisition"
		return 1
	fi

	local tmpdir lockdir harness
	tmpdir=$(mktemp -d)
	lockdir="$tmpdir/.oro-quality-gate.lock"
	harness="$tmpdir/run-lock-timeout.sh"
	mkdir "$lockdir"
	{
		echo 'set -euo pipefail'
		printf 'REPO_ROOT=%q\n' "$tmpdir"
		printf 'QG_DIR=%q\n' "$tmpdir/qg"
		echo 'QG_STAGE_ASSETS_LOCK=""'
		echo 'QG_RUN_LOCK=""'
		echo 'ORO_QG_LOCK_TIMEOUT_SECONDS=1'
		echo 'mkdir -p "$QG_DIR"'
		sed -n '/^cleanup_qg()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_poll_seconds()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_timeout_reached()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^create_quality_gate_queue_ticket()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_queue_ticket_stale()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^cleanup_stale_quality_gate_queue_tickets()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^first_quality_gate_queue_ticket()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_is_inherited()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^acquire_quality_gate_lock()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		echo 'trap cleanup_qg EXIT'
		echo 'acquire_quality_gate_lock'
	} >"$harness"

	if bash "$harness" >/dev/null 2>"$tmpdir/err"; then
		echo "FAIL: acquire_quality_gate_lock unexpectedly succeeded against a held lock"
		rm -rf "$tmpdir"
		return 1
	fi
	if [ ! -d "$lockdir" ]; then
		echo "FAIL: timed-out quality gate cleanup removed another process's run lock"
		cat "$tmpdir/err"
		rm -rf "$tmpdir"
		return 1
	fi
	rm -rf "$tmpdir"
	return 0
}

# Test: abandoned lock directories without owner metadata are archived after
# the stale threshold so worker gates do not wait forever on a crashed gate.
# shellcheck disable=SC2016,SC2317,SC2329
test_quality_gate_run_lock_archives_stale_legacy_lock() {
	if ! grep -q 'archive_stale_quality_gate_lock()' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'write_quality_gate_lock_owner()' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'archive_stale_quality_gate_lock()' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go" ||
		! grep -q 'write_quality_gate_lock_owner()' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo "FAIL: quality gate run lock lacks stale lock archival and owner metadata"
		return 1
	fi

	local tmpdir lockdir harness
	tmpdir=$(mktemp -d)
	lockdir="$tmpdir/.oro-quality-gate.lock"
	harness="$tmpdir/run-lock-stale.sh"
	mkdir "$lockdir"
	sleep 2
	{
		echo 'set -euo pipefail'
		printf 'REPO_ROOT=%q\n' "$tmpdir"
		printf 'QG_DIR=%q\n' "$tmpdir/qg"
		echo 'QG_STAGE_ASSETS_LOCK=""'
		echo 'QG_RUN_LOCK=""'
		echo 'ORO_QG_LOCK_TIMEOUT_SECONDS=3'
		echo 'ORO_QG_STALE_LOCK_SECONDS=1'
		echo 'mkdir -p "$QG_DIR"'
		sed -n '/^cleanup_qg()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_age_seconds()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_process_start_time()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_owner_matches_process()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_process_has_descendants()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_stale()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^archive_stale_quality_gate_lock()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^cleanup_archived_stale_quality_gate_locks()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^write_quality_gate_lock_owner()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_poll_seconds()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_timeout_reached()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^create_quality_gate_queue_ticket()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_queue_ticket_stale()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^cleanup_stale_quality_gate_queue_tickets()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^first_quality_gate_queue_ticket()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_is_inherited()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^acquire_quality_gate_lock()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		echo 'trap cleanup_qg EXIT'
		echo 'acquire_quality_gate_lock'
		echo '[ -f "$QG_RUN_LOCK/owner" ]'
		echo 'find "$REPO_ROOT" -maxdepth 1 -name ".oro-quality-gate.lock.stale.*" | grep -q .'
	} >"$harness"

	if ! bash "$harness" >"$tmpdir/out" 2>"$tmpdir/err"; then
		echo "FAIL: acquire_quality_gate_lock did not recover a stale legacy lock"
		cat "$tmpdir/out"
		cat "$tmpdir/err"
		rm -rf "$tmpdir"
		return 1
	fi
	rm -rf "$tmpdir"
	return 0
}

# Test: archived stale run locks are swept on acquisition once they exceed the
# stale threshold, while newer archives and the newly acquired live lock stay.
# shellcheck disable=SC2016,SC2317,SC2329
test_quality_gate_archived_stale_locks_are_garbage_collected() {
	if ! grep -q 'cleanup_archived_stale_quality_gate_locks()' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'cleanup_archived_stale_quality_gate_locks()' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo "FAIL: quality gate does not garbage-collect archived stale run locks"
		return 1
	fi

	local tmpdir lockdir fresh_archive harness old_archive
	tmpdir=$(mktemp -d)
	lockdir="$tmpdir/.oro-quality-gate.lock"
	fresh_archive="${lockdir}.stale.fresh.999"
	harness="$tmpdir/run-lock-archive-gc.sh"
	for old_archive in "${lockdir}.stale.old-one.111" "${lockdir}.stale.old-two.222" "${lockdir}.stale.old-three.333"; do
		mkdir "$old_archive"
		echo 'pid=999999' >"$old_archive/owner"
		touch -t 200001010000 "$old_archive"
	done
	mkdir "$fresh_archive"
	echo 'pid=999999' >"$fresh_archive/owner"
	{
		echo 'set -euo pipefail'
		printf 'REPO_ROOT=%q\n' "$tmpdir"
		printf 'QG_DIR=%q\n' "$tmpdir/qg"
		echo 'QG_STAGE_ASSETS_LOCK=""'
		echo 'QG_RUN_LOCK=""'
		echo 'ORO_QG_STALE_LOCK_SECONDS=10'
		echo 'mkdir -p "$QG_DIR"'
		sed -n '/^cleanup_qg()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_age_seconds()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^cleanup_archived_stale_quality_gate_locks()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_process_start_time()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^write_quality_gate_lock_owner()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_poll_seconds()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_timeout_reached()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^create_quality_gate_queue_ticket()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_queue_ticket_stale()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^cleanup_stale_quality_gate_queue_tickets()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^first_quality_gate_queue_ticket()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^quality_gate_lock_is_inherited()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		sed -n '/^acquire_quality_gate_lock()/,/^}/p' "$SCRIPT_DIR/quality_gate.sh"
		echo 'trap cleanup_qg EXIT'
		echo 'acquire_quality_gate_lock'
		echo '[ -d "$REPO_ROOT/.oro-quality-gate.lock.stale.fresh.999" ]'
		echo '[ -f "$QG_RUN_LOCK/owner" ]'
		echo '! find "$REPO_ROOT" -maxdepth 1 -name ".oro-quality-gate.lock.stale.old-*" | grep -q .'
	} >"$harness"

	if ! bash "$harness" >"$tmpdir/out" 2>"$tmpdir/err"; then
		echo "FAIL: acquire_quality_gate_lock did not garbage-collect archived stale run locks"
		cat "$tmpdir/out"
		cat "$tmpdir/err"
		rm -rf "$tmpdir"
		return 1
	fi
	rm -rf "$tmpdir"
	return 0
}

# Test: Go quality checks cap scheduler fanout by default so parallel worker
# gates do not starve dispatcher integration tests under load.
# shellcheck disable=SC2317,SC2329
test_quality_gate_caps_go_scheduler_fanout() {
	# shellcheck disable=SC2016
	if ! grep -q 'export GOMAXPROCS="${ORO_QG_GOMAXPROCS:-2}"' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not cap Go scheduler fanout for local quality gates"
		return 1
	fi
	if ! grep -q 'ORO_QG_GOMAXPROCS' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not expose ORO_QG_GOMAXPROCS override"
		return 1
	fi
	return 0
}

# shellcheck disable=SC2317,SC2329 # invoked by name through the test runner
test_go_coverage_threshold_skips_uncovered_go_surfaces() {
	local script="$SCRIPT_DIR/quality_gate.sh"
	local gen="$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"
	for file in "$script" "$gen"; do
		if ! grep -q 'should_enforce_go_coverage_threshold()' "$file"; then
			echo "FAIL: $file lacks should_enforce_go_coverage_threshold helper"
			return 1
		fi
		if ! grep -q 'Skipping 78% Go coverage threshold' "$file"; then
			echo "FAIL: $file does not explain skipped Go coverage threshold"
			return 1
		fi
	done
	if ! grep -q 'cov < 78' "$script"; then
		echo "FAIL: quality_gate.sh does not enforce the 78% Go coverage threshold"
		return 1
	fi
	if ! grep -q 'below 78% threshold' "$script"; then
		echo "FAIL: quality_gate.sh does not report the 78% Go coverage threshold"
		return 1
	fi

	local tmpdir helpers oldpwd output_file
	tmpdir=$(mktemp -d)
	helpers="$tmpdir/helpers.sh"
	output_file="$tmpdir/output.txt"
	oldpwd="$PWD"
	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	{
		sed -n '/^qg_git()/,/^}/p' "$script"
		sed -n '/^should_run_mutation_tests()/,/^}/p' "$script"
		sed -n '/^mutation_base_ref()/,/^}/p' "$script"
		sed -n '/^should_enforce_go_coverage_threshold()/,/^}/p' "$script"
	} >"$helpers"

	git init --initial-branch=main "$tmpdir/repo" >/dev/null 2>&1
	cd "$tmpdir/repo"
	git config user.email qg-test@example.invalid
	git config user.name "QG Test"
	mkdir -p cmd/oro internal/core pkg/lib docs .oro
	printf 'package main\n' >cmd/oro/main.go
	printf 'package core\n' >internal/core/core.go
	printf 'package lib\n' >pkg/lib/lib.go
	printf 'baseline\n' >docs/readme.md
	printf 'baseline\n' >.oro/config.yaml
	git add .
	git commit -m init >/dev/null 2>&1
	git checkout -b change >/dev/null 2>&1

	run_helper() {
		bash -c '
			set -euo pipefail
			QG_IS_WORKTREE=false
			source "$1"
			should_enforce_go_coverage_threshold
		' _ "$helpers"
	}

	printf '// cmd-only change\n' >>cmd/oro/main.go
	if run_helper >"$output_file" 2>&1; then
		echo "FAIL: cmd-only changes should skip Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	if ! grep -q 'outside measured ./internal and ./pkg production surface' "$output_file"; then
		echo "FAIL: cmd-only skip did not explain measured production surface"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	git checkout -- cmd/oro/main.go

	printf 'docs change\n' >>docs/readme.md
	if run_helper >"$output_file" 2>&1; then
		echo "FAIL: docs-only changes should skip Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	git checkout -- docs/readme.md

	printf 'config change\n' >>.oro/config.yaml
	if run_helper >"$output_file" 2>&1; then
		echo "FAIL: config-only changes should skip Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	git checkout -- .oro/config.yaml

	printf 'package core\n' >internal/core/core_test.go
	git add internal/core/core_test.go
	if run_helper >"$output_file" 2>&1; then
		echo "FAIL: *_test.go changes should skip Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	git reset -q internal/core/core_test.go
	rm -f internal/core/core_test.go

	printf '// production change\n' >>internal/core/core.go
	if ! run_helper >"$output_file" 2>&1; then
		echo "FAIL: internal non-test Go changes should enforce Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	git checkout -- internal/core/core.go

	printf '// production change\n' >>pkg/lib/lib.go
	if ! run_helper >"$output_file" 2>&1; then
		echo "FAIL: pkg non-test Go changes should enforce Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	git checkout -- pkg/lib/lib.go

	git branch -D main >/dev/null 2>&1
	printf '// missing base\n' >>cmd/oro/main.go
	if ! run_helper >"$output_file" 2>&1; then
		echo "FAIL: missing base ref should enforce Go coverage threshold"
		cat "$output_file"
		cd "$oldpwd"
		return 1
	fi
	cd "$oldpwd"
}

# shellcheck disable=SC2317,SC2329 # invoked by name through the test runner
write_quality_gate_python_helpers() {
	local out="$1"
	sed -n '/^qg_python_tool_path()/,/^# Run multiple checks/p' "$SCRIPT_DIR/quality_gate.sh" |
		sed '$d' >"$out"
}

# shellcheck disable=SC2317,SC2329 # invoked by name through the test runner
write_generated_quality_gate_python_helpers() {
	local out="$1"
	sed -n '/^qg_python_tool_path()/,/^# Run multiple checks/p' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go" |
		sed '$d' >"$out"
}

# shellcheck disable=SC2317,SC2329 # invoked by name through the test runner
run_python_tool_resolution_fixture() {
	local helpers="$1"
	local tmpdir
	tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/qg-python-tools.XXXXXX")
	mkdir -p "$tmpdir/home/.local/bin" "$tmpdir/.pyenv/shims" "$tmpdir/linkbin" "$tmpdir/bin" "$tmpdir/repo/.venv/bin"

	cat >"$tmpdir/.pyenv/shims/ruff" <<'EOS'
#!/bin/sh
echo SHIM_EXECUTED "$@"
exit 99
EOS
	chmod +x "$tmpdir/.pyenv/shims/ruff"
	ln -s "$tmpdir/.pyenv/shims/ruff" "$tmpdir/home/.local/bin/ruff"
	ln -s "$tmpdir/.pyenv/shims/ruff" "$tmpdir/linkbin/ruff"

	cat >"$tmpdir/bin/uv" <<'EOS'
#!/bin/sh
echo UV_FALLBACK "$@"
exit 0
EOS
	chmod +x "$tmpdir/bin/uv"

	local output rc
	output=$(PATH="$tmpdir/linkbin:$tmpdir/bin:/usr/bin:/bin" HOME="$tmpdir/home" REPO_ROOT="$tmpdir/repo" /bin/bash -c 'cd "$REPO_ROOT"; source "$1"; qg_ruff check .' _ "$helpers" 2>&1)
	rc=$?
	if [ "$rc" -ne 0 ] || echo "$output" | grep -q 'SHIM_EXECUTED'; then
		echo "FAIL: qg_ruff executed or failed on symlinked pyenv shim: rc=$rc output=$output"
		rm -rf "$tmpdir"
		return 1
	fi
	if ! echo "$output" | grep -q 'UV_FALLBACK run ruff check .'; then
		echo "FAIL: qg_ruff did not fall back to uv after rejecting pyenv shim: $output"
		rm -rf "$tmpdir"
		return 1
	fi

	output=$(PATH="$tmpdir/bin:/usr/bin:/bin" HOME="$tmpdir/home" REPO_ROOT="$tmpdir/repo" /bin/bash -c 'cd "$REPO_ROOT"; source "$1"; qg_ruff format --check .' _ "$helpers" 2>&1)
	rc=$?
	if [ "$rc" -ne 0 ] || echo "$output" | grep -q 'SHIM_EXECUTED'; then
		echo "FAIL: qg_ruff executed or failed on HOME/.local/bin symlinked pyenv shim: rc=$rc output=$output"
		rm -rf "$tmpdir"
		return 1
	fi

	output=$(PATH="$tmpdir/bin" HOME="$tmpdir/home" REPO_ROOT="$tmpdir/repo" /bin/bash -c 'cd "$REPO_ROOT"; source "$1"; qg_ruff check .' _ "$helpers" 2>&1)
	rc=$?
	if [ "$rc" -ne 0 ] || echo "$output" | grep -q 'SHIM_EXECUTED'; then
		echo "FAIL: qg_ruff executed or failed on symlinked pyenv shim without realpath on PATH: rc=$rc output=$output"
		rm -rf "$tmpdir"
		return 1
	fi
	if ! echo "$output" | grep -q 'UV_FALLBACK run ruff check .'; then
		echo "FAIL: qg_ruff did not fall back to uv after rejecting unresolved symlink: $output"
		rm -rf "$tmpdir"
		return 1
	fi

	output=$(PATH="$tmpdir/bin:/usr/bin:/bin" HOME="$tmpdir/home" REPO_ROOT="$tmpdir/repo" /bin/bash -c 'cd "$REPO_ROOT"; source "$1"; qg_pyright --version' _ "$helpers" 2>&1)
	rc=$?
	if [ "$rc" -ne 77 ] || [ "$output" != "SKIP: pyright not installed" ]; then
		echo "FAIL: qg_pyright should skip without uv fallback when no direct binary exists: rc=$rc output=$output"
		rm -rf "$tmpdir"
		return 1
	fi

	rm -rf "$tmpdir"
}

# Test: Pyright runs in the active worktree virtual environment, not an
# inherited environment from a sibling checkout.
# shellcheck disable=SC2317,SC2329 # invoked by name through the test runner
test_pyright_uses_active_worktree_venv() {
	local helpers tmpdir output rc
	helpers=$(mktemp "${TMPDIR:-/tmp}/qg-pyright-helpers.XXXXXX")
	write_quality_gate_python_helpers "$helpers"
	tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/qg-pyright-venv.XXXXXX")
	# shellcheck disable=SC2064
	trap "rm -f '$helpers'; rm -rf '$tmpdir'" RETURN

	mkdir -p "$tmpdir/worktree/.venv/bin" "$tmpdir/sibling/.venv/bin"
	cat >"$tmpdir/worktree/.venv/bin/python" <<'EOS'
#!/bin/sh
exit 0
EOS
	cat >"$tmpdir/worktree/.venv/bin/pyright" <<'EOS'
#!/bin/sh
if [ "$VIRTUAL_ENV" != "$EXPECTED_VIRTUAL_ENV" ]; then
	echo "wrong VIRTUAL_ENV: $VIRTUAL_ENV"
	exit 1
fi
case ":$PATH:" in
*":$EXPECTED_VIRTUAL_ENV/bin:"*) exit 0 ;;
*) echo "active virtualenv bin directory missing from PATH"; exit 1 ;;
esac
EOS
	chmod +x "$tmpdir/worktree/.venv/bin/python" "$tmpdir/worktree/.venv/bin/pyright"

	output=$(PATH="/usr/bin:/bin" REPO_ROOT="$tmpdir/worktree" VIRTUAL_ENV="$tmpdir/sibling/.venv" EXPECTED_VIRTUAL_ENV="$tmpdir/worktree/.venv" /bin/bash -c 'cd "$REPO_ROOT"; source "$1"; qg_pyright --version' _ "$helpers" 2>&1)
	rc=$?
	if [ "$rc" -ne 0 ]; then
		echo "FAIL: qg_pyright did not bind to the active worktree virtual environment: rc=$rc output=$output"
		return 1
	fi
}

# Test: Python tool resolution avoids pyenv shims and uses deterministic wrappers.
# shellcheck disable=SC2317,SC2329
test_quality_gate_python_tools_avoid_pyenv_shims() {
	local realpath_candidate_pattern="realpath \"\$candidate\""
	if ! grep -q 'qg_python_tool_path()' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh lacks qg_python_tool_path helper"
		return 1
	fi
	if ! grep -q 'pyenv/shims' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not reject pyenv shim paths"
		return 1
	fi
	if ! grep -q 'qg_python_tool_path_allowed()' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q "$realpath_candidate_pattern" "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh does not resolve symlinks before rejecting pyenv shims"
		return 1
	fi
	if ! grep -q 'qg_run_ruff_format_source' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'qg_run_ruff_check_source' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh ruff lanes do not use source-scoped qg_ruff wrappers"
		return 1
	fi
	if ! grep -q 'qg_pyright --version' "$SCRIPT_DIR/quality_gate.sh" ||
		! grep -q 'check "pyright" "qg_run_pyright_source"' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh pyright lane does not use source-scoped qg_pyright wrapper"
		return 1
	fi
	if ! grep -q 'check "pytest" "qg_run_python_tool pytest"' "$SCRIPT_DIR/quality_gate.sh"; then
		echo "FAIL: quality_gate.sh pytest lane does not use qg_run_python_tool wrapper"
		return 1
	fi
	local helpers
	helpers=$(mktemp "${TMPDIR:-/tmp}/qg-helpers.XXXXXX")
	write_quality_gate_python_helpers "$helpers"
	run_python_tool_resolution_fixture "$helpers"
	local rc=$?
	rm -f "$helpers"
	return "$rc"
}

# Test: generated quality gate template includes the same Python tool resolver.
# shellcheck disable=SC2317,SC2329
test_generated_quality_gate_python_tools_avoid_pyenv_shims() {
	local gen="$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"
	local realpath_candidate_pattern="realpath \"\$candidate\""
	if ! grep -q 'qg_python_tool_path()' "$gen"; then
		echo "FAIL: generated quality gate template lacks qg_python_tool_path helper"
		return 1
	fi
	if ! grep -q 'pyenv/shims' "$gen"; then
		echo "FAIL: generated quality gate template does not reject pyenv shim paths"
		return 1
	fi
	if ! grep -q 'qg_python_tool_path_allowed()' "$gen" ||
		! grep -q "$realpath_candidate_pattern" "$gen"; then
		echo "FAIL: generated quality gate template does not resolve symlinks before rejecting pyenv shims"
		return 1
	fi
	if ! grep -q 'qg_run_ruff_format_source' "$gen" ||
		! grep -q 'qg_run_ruff_check_source' "$gen"; then
		echo "FAIL: generated quality gate template ruff lanes do not use source-scoped qg_ruff wrappers"
		return 1
	fi
	if ! grep -q 'qg_pyright --version' "$gen" ||
		! grep -q 'check "pyright" "qg_run_pyright_source"' "$gen"; then
		echo "FAIL: generated quality gate template pyright lane does not use source-scoped qg_pyright wrapper"
		return 1
	fi
	if ! grep -q 'check "pytest" "qg_run_python_tool pytest"' "$gen"; then
		echo "FAIL: generated quality gate template pytest lane does not use qg_run_python_tool wrapper"
		return 1
	fi
	local helpers
	helpers=$(mktemp "${TMPDIR:-/tmp}/qg-generated-helpers.XXXXXX")
	write_generated_quality_gate_python_helpers "$helpers"
	run_python_tool_resolution_fixture "$helpers"
	local rc=$?
	rm -f "$helpers"
	return "$rc"
}

# Test: filesystem-walking quality gate lanes are scoped to tracked source files.
# shellcheck disable=SC2317,SC2329
test_quality_gate_filesystem_walkers_are_source_scoped() {
	local script="$SCRIPT_DIR/quality_gate.sh"
	local gen="$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"

	for file in "$script" "$gen"; do
		if ! grep -q 'qg_python_source_files()' "$file"; then
			echo "FAIL: $file lacks qg_python_source_files helper"
			return 1
		fi
		if ! grep -q 'qg_yaml_source_files()' "$file"; then
			echo "FAIL: $file lacks qg_yaml_source_files helper"
			return 1
		fi
		if ! grep -q 'qg_run_ruff_format_source' "$file" ||
			! grep -q 'qg_run_ruff_check_source' "$file" ||
			! grep -q 'qg_run_pyright_source' "$file"; then
			echo "FAIL: $file Python lanes do not use source-scoped wrappers"
			return 1
		fi
		if ! grep -q 'qg_run_yamllint_source' "$file"; then
			echo "FAIL: $file yamllint lane does not use source-scoped wrapper"
			return 1
		fi
		if grep -q "':(exclude){{.WorktreesDir}}/\*\*'" "$file"; then
			echo "FAIL: $file uses generated WorktreesDir as a git pathspec"
			return 1
		fi
	done

	if grep -q 'qg_ruff format --check \.' "$script" ||
		grep -q 'qg_ruff check \.' "$script" ||
		grep -q 'check "pyright" "qg_pyright"' "$script" ||
		grep -q 'xargs yamllint' "$script"; then
		echo "FAIL: quality_gate.sh still scans repo root for Python/YAML lanes"
		return 1
	fi
	if grep -q 'qg_ruff format --check \.' "$gen" ||
		grep -q 'qg_ruff check \.' "$gen" ||
		grep -q 'check "pyright" "qg_pyright"' "$gen" ||
		grep -q 'xargs yamllint' "$gen"; then
		echo "FAIL: generated quality gate template still scans repo root for Python/YAML lanes"
		return 1
	fi
	for pathspec in \
		':(exclude).tmp-test/**' \
		':(exclude).cache/**' \
		':(exclude).worktrees/**'; do
		if ! grep -Fq -- "$pathspec" "$script"; then
			echo "FAIL: quality_gate.sh qg_source_files does not exclude $pathspec"
			return 1
		fi
		if ! grep -Fq -- "$pathspec" "$gen"; then
			echo "FAIL: generated quality gate qg_source_files does not exclude $pathspec"
			return 1
		fi
	done
}

# Test: invalid LC_ALL values are normalized before invoking Python tools.
# shellcheck disable=SC2317,SC2329
test_quality_gate_invalid_locale_sanitized() {
	if ! head -20 "$SCRIPT_DIR/quality_gate.sh" | grep -q 'locale -a'; then
		echo "FAIL: quality_gate.sh does not validate LC_ALL early"
		return 1
	fi
	if ! head -20 "$SCRIPT_DIR/quality_gate.sh" | grep -q 'export LC_ALL=C'; then
		echo "FAIL: quality_gate.sh does not normalize invalid LC_ALL to C"
		return 1
	fi
	if ! grep -q 'locale -a' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go" ||
		! grep -q 'export LC_ALL=C' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo "FAIL: generated quality gate template does not normalize invalid LC_ALL"
		return 1
	fi
}

# Test: direct ./scripts/quality_gate.sh starts under sh so invalid LC_ALL is
# sanitized before Bash can emit setlocale warnings.
# shellcheck disable=SC2317,SC2329
test_quality_gate_invalid_locale_bootstraps_before_bash() {
	if [ "$(head -1 "$SCRIPT_DIR/quality_gate.sh")" != "#!/bin/sh" ]; then
		echo "FAIL: quality_gate.sh must use /bin/sh bootstrap before Bash"
		return 1
	fi
	if ! head -25 "$SCRIPT_DIR/quality_gate.sh" | grep -q 'ORO_QG_BASH_BOOTSTRAPPED'; then
		echo "FAIL: quality_gate.sh does not guard the Bash bootstrap"
		return 1
	fi
	if ! head -30 "$SCRIPT_DIR/quality_gate.sh" | grep -q 'BASHPID'; then
		echo "FAIL: quality_gate.sh bootstrap does not distinguish Bash subshell PIDs"
		return 1
	fi
	# shellcheck disable=SC2016
	if ! head -45 "$SCRIPT_DIR/quality_gate.sh" | grep -q 'exec env -u BASH_ENV "\$qg_bash" "\$0" "\$@"'; then
		echo "FAIL: quality_gate.sh does not exec its verified Bash after locale normalization"
		return 1
	fi
	if ! grep -q '^const qualityGateTmpl = `#!/bin/sh' "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; then
		echo "FAIL: generated quality gate template must use /bin/sh bootstrap before Bash"
		return 1
	fi
}

# Test: a restricted macOS-style PATH must not select /bin/bash 3.2, which
# cannot run mapfile-based lanes. The bootstrap must locate Bash 4+ before it
# can launch any lane, and its generated counterpart must retain that guard.
# shellcheck disable=SC2317,SC2329
test_quality_gate_bootstrap_selects_bash4_or_fails() {
	local tmpdir unavailable output rc
	tmpdir=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	set +e
	output=$(PATH=/usr/bin:/bin "$SCRIPT_DIR/quality_gate.sh" --help 2>&1)
	rc=$?
	set -e

	if [ "$rc" -ne 0 ] && ! printf '%s\n' "$output" | grep -q 'requires Bash 4 or newer'; then
		echo "FAIL: restricted PATH bootstrap failed without a deterministic Bash 4+ diagnostic"
		printf '%s\n' "$output"
		return 1
	fi
	if [ "$rc" -eq 0 ] && ! printf '%s\n' "$output" | grep -q '^Usage: '; then
		echo "FAIL: restricted PATH bootstrap did not reach quality gate help"
		printf '%s\n' "$output"
		return 1
	fi
	unavailable="$tmpdir/quality_gate.sh"
	sed 's#/opt/homebrew/bin/bash /usr/local/bin/bash#'"$tmpdir"'/missing-bash '"$tmpdir"'/also-missing-bash#' "$SCRIPT_DIR/quality_gate.sh" >"$unavailable"
	chmod +x "$unavailable"
	set +e
	output=$(PATH=/usr/bin:/bin "$unavailable" --help 2>&1)
	rc=$?
	set -e
	if [ "$rc" -ne 2 ] || ! printf '%s\n' "$output" | grep -q 'requires Bash 4 or newer'; then
		echo "FAIL: bootstrap without Bash 4+ did not emit its deterministic diagnostic"
		printf '%s\n' "$output"
		return 1
	fi
	for file in "$SCRIPT_DIR/quality_gate.sh" "$SCRIPT_DIR/../cmd/oro/quality_gate_gen.go"; do
		if ! grep -q 'BASH_VERSINFO\[0\]' "$file"; then
			echo "FAIL: $file does not verify a Bash 4+ interpreter"
			return 1
		fi
		if ! grep -q '/opt/homebrew/bin/bash' "$file"; then
			echo "FAIL: $file does not try Homebrew Bash before PATH Bash"
			return 1
		fi
	done
}

# Test: an inherited bootstrap marker must not make a fresh /bin/sh launch skip
# its one required Bash re-exec. BASH_ENV is deliberately ignored so an ambient
# shell hook cannot recursively launch another quality gate during that exec.
# shellcheck disable=SC2317,SC2329
test_quality_gate_bootstrap_ignores_inherited_shell_state() {
	local tmpdir bash_env marker bash_dir output rc
	tmpdir=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN
	bash_env="$tmpdir/bash_env"
	marker="$tmpdir/bash-env-ran"
	bash_dir="$tmpdir/bin"
	mkdir -p "$bash_dir"
	cat >"$bash_env" <<EOF
printf 'sourced\\n' >"$marker"
EOF
	cat >"$bash_dir/bash" <<EOF
#!/bin/sh
printf 'bootstrap:%s\\n' "\${BASH_ENV:+set}" >"$tmpdir/bash-bootstrap"
exec /bin/bash "\$@"
EOF
	chmod +x "$bash_dir/bash"

	set +e
	output=$(PATH="$bash_dir:$PATH" ORO_QG_BASH_BOOTSTRAPPED=1 BASH_ENV="$bash_env" "$SCRIPT_DIR/quality_gate.sh" --help 2>&1)
	rc=$?
	set -e

	if [ "$rc" -ne 0 ] || ! printf '%s\\n' "$output" | grep -q '^Usage: '; then
		echo "FAIL: inherited bootstrap state did not reach Bash help output (exit $rc)"
		printf '%s\\n' "$output"
		return 1
	fi
	if [ -e "$marker" ]; then
		echo "FAIL: quality_gate.sh sourced inherited BASH_ENV during bootstrap"
		return 1
	fi
}

# Test: a quality gate launched by an active gate must exit before it can start
# another set of lanes. The active parent retains responsibility for the final
# terminal summary.
# shellcheck disable=SC2317,SC2329
test_quality_gate_nested_invocation_exits_before_lanes() {
	local output rc

	set +e
	output=$(/bin/bash -c 'ORO_QG_ACTIVE_PID=$$ "$1" --help; status=$?; :; exit "$status"' _ "$SCRIPT_DIR/quality_gate.sh" 2>&1)
	rc=$?
	set -e

	if [ "$rc" -ne 0 ]; then
		echo "FAIL: nested quality gate exited $rc, want 0"
		printf '%s\\n' "$output"
		return 1
	fi
	if ! printf '%s\\n' "$output" | grep -q '^Nested quality gate invocation detected'; then
		echo "FAIL: nested quality gate did not stop before starting lanes"
		printf '%s\\n' "$output"
		return 1
	fi
	if printf '%s\\n' "$output" | grep -q '^Usage: '; then
		echo "FAIL: nested quality gate continued into its own command processing"
		return 1
	fi
}

# Test: unresolved git conflict markers are reported before Bash parses them.
# shellcheck disable=SC2317,SC2329
test_quality_gate_conflict_markers_fail_preflight() {
	local tmpdir script output
	tmpdir=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '$tmpdir'" RETURN

	script="$tmpdir/quality_gate.sh"
	awk '/^unset ORO_QG_BASH_BOOTSTRAPPED_PID$/ && !inserted {
		print "<<<<<<< Updated upstream"
		print "======="
		print ">>>>>>> Stashed changes"
		inserted = 1
	}
	{ print }' "$SCRIPT_DIR/quality_gate.sh" >"$script"
	chmod +x "$script"

	set +e
	output=$("$script" 2>&1)
	local rc=$?
	set -e

	if [ "$rc" -ne 2 ]; then
		echo "FAIL: conflicted quality_gate.sh exited $rc, want 2"
		echo "$output"
		return 1
	fi
	if ! echo "$output" | grep -q "FAIL: quality_gate.sh contains unresolved git conflict markers"; then
		echo "FAIL: conflicted quality_gate.sh did not print conflict-marker diagnostic"
		echo "$output"
		return 1
	fi
	if echo "$output" | grep -q "syntax error near unexpected token"; then
		echo "FAIL: conflicted quality_gate.sh still reached Bash parser error"
		echo "$output"
		return 1
	fi
}

# Test: Makefile mutate-go-diff git diff has 2>/dev/null
# shellcheck disable=SC2317,SC2329
test_makefile_git_diff_stderr_redirect() {
	local target_body
	target_body=$(awk '/^mutate-go-diff:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go-diff:/{f=0} f' "$SCRIPT_DIR/../Makefile")

	if echo "$target_body" | grep 'git diff' | grep -q '2>/dev/null'; then
		return 0
	fi

	echo "FAIL: Makefile mutate-go-diff git diff is missing 2>/dev/null"
	echo "  stderr gets captured into \$changed when main branch is missing"
	return 1
}

# Test: Makefile $$changed is quoted in mutate-go-diff
# shellcheck disable=SC2317,SC2329
test_makefile_changed_is_quoted() {
	local target_body
	target_body=$(awk '/^mutate-go-diff:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go-diff:/{f=0} f' "$SCRIPT_DIR/../Makefile")

	# Check go-mutesting line doesn't use unquoted $$changed
	# shellcheck disable=SC2016
	if echo "$target_body" | grep 'go-mutesting' | grep -qE '[^"]\$\$changed'; then
		echo "FAIL: Makefile mutate-go-diff uses unquoted \$\$changed with go-mutesting"
		return 1
	fi
	# Check echo/Mutating line doesn't use unquoted $$changed
	# shellcheck disable=SC2016
	if echo "$target_body" | grep 'Mutating' | grep -qE '[^"]\$\$changed'; then
		echo "FAIL: Makefile mutate-go-diff uses unquoted \$\$changed in echo"
		return 1
	fi
	return 0
}

# Test: Makefile mutate-py uses PID-isolated temp path (not hardcoded /tmp/cr-session.sqlite)
# shellcheck disable=SC2317,SC2329
test_makefile_mutate_py_pid_isolated() {
	local target_body
	target_body=$(awk '/^mutate-py:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-py:/{f=0} f' "$SCRIPT_DIR/../Makefile")

	if echo "$target_body" | grep -q '/tmp/cr-session\.sqlite'; then
		echo "FAIL: mutate-py uses hardcoded /tmp/cr-session.sqlite"
		echo "  Concurrent runs will corrupt SQLite. Use PID-isolated path."
		return 1
	fi
	# Verify it uses some kind of PID isolation ($$)
	if echo "$target_body" | grep -q 'cr-session.*\$\$'; then
		return 0
	fi
	echo "FAIL: mutate-py does not use PID-isolated temp path"
	return 1
}

# Test: Makefile mutate-py-full uses PID-isolated temp path
# shellcheck disable=SC2317,SC2329
test_makefile_mutate_py_full_pid_isolated() {
	local target_body
	target_body=$(awk '/^mutate-py-full:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-py-full:/{f=0} f' "$SCRIPT_DIR/../Makefile")

	if echo "$target_body" | grep -q '/tmp/cr-full-session\.sqlite'; then
		echo "FAIL: mutate-py-full uses hardcoded /tmp/cr-full-session.sqlite"
		echo "  Concurrent runs will corrupt SQLite. Use PID-isolated path."
		return 1
	fi
	if echo "$target_body" | grep -q 'cr-full-session.*\$\$'; then
		return 0
	fi
	echo "FAIL: mutate-py-full does not use PID-isolated temp path"
	return 1
}

# Run tests
echo "Testing quality_gate.sh config-driven behavior"
echo "=============================================="

test_case "Reads config when present" test_reads_config_when_present
test_case "Falls back when config missing" test_fallback_when_config_missing
test_case "Skips when tool missing" test_skip_when_tool_missing
test_case "Removes terminal escape artifacts at repo root" test_repo_root_rejects_terminal_escape_artifacts
test_case "quality_gate.sh is shfmt clean" test_quality_gate_shell_source_is_shfmt_clean

echo ""
echo "Testing mutation trap handlers (oro-bl44)"
echo "=============================================="

test_case "quality_gate.sh mutation has trap EXIT" test_quality_gate_mutation_trap_present
test_case "quality_gate.sh mutation preserves unstaged work" test_quality_gate_mutation_cleanup_preserves_unstaged_work
test_case "quality_gate.sh mutation restores hook/temp side effects" test_quality_gate_mutation_restores_side_effects
test_case "quality_gate.sh mutation restores symlinked pre-push hook" test_quality_gate_mutation_restores_symlinked_pre_push_hook
test_case "quality_gate.sh mutation trap preserves QG_DIR cleanup" test_quality_gate_mutation_trap_preserves_qg_dir_cleanup
test_case "Makefile mutate-go has trap" test_makefile_mutate_go_trap_present
test_case "Makefile mutate-go-diff has trap" test_makefile_mutate_go_diff_trap_present

echo ""
echo "Testing missing main branch + crash detection (oro-xgwr)"
echo "=============================================="

test_case "mutation checks main branch existence" test_mutation_checks_main_branch_existence
test_case "mutation crash flagged as FAIL" test_mutation_crash_flagged_as_fail
test_case "mutation zero-total report skips" test_mutation_zero_total_skips
test_case "mutation missing-main warning message" test_mutation_missing_main_warning_message
test_case "mutation skips by default in all contexts" test_mutation_default_all_contexts_skip
test_case "mutation runs only with opt-in flag" test_mutation_opt_in_flag_runs
test_case "pre-push leaves mutation disabled" test_pre_push_leaves_mutation_opt_in

echo ""
echo "Testing Python mutation missing main branch (oro-xgwr)"
echo "=============================================="

test_case "python mutation checks main branch" test_python_mutation_checks_main_branch_existence
test_case "python mutation missing-main warning" test_python_mutation_missing_main_warning_message

echo ""
echo "Testing worktree ref resolution (oro-w6xz)"
echo "=============================================="

test_case "worktree env restores GIT_DIR" test_worktree_ref_resolution_after_env_cleanup
test_case "worktree rev-parse main works" test_worktree_rev_parse_main_functional
test_case "go mutation limits QG Go surface" test_go_mutation_limits_changed_files_to_qg_go_surface
test_case "go mutation anchors touched function match" test_go_mutation_match_pattern_anchors_touched_functions
test_case "worktree env no GIT_WORK_TREE leak" test_worktree_env_no_leak_to_subprocesses

echo ""
echo "Testing beadstore import boundary (oro-8ghm)"
echo "=============================================="

test_case "beadstore import boundary rejects forbidden imports" test_beadstore_import_boundary_rejects_forbidden_imports
test_case "beadstore import boundary allows protocol import" test_beadstore_import_boundary_allows_protocol_import

echo ""
echo "Testing shell correctness in mutation testing (oro-koon)"
echo "=============================================="

test_case "no SC2086 disable for \$changed" test_no_sc2086_disable_for_changed
test_case "quality_gate.sh \$changed is quoted" test_quality_gate_changed_is_quoted
test_case "quality_gate.sh stage-assets failures fail closed" test_quality_gate_stage_assets_fail_closed
test_case "golangci-lint is isolated to active worktree" test_golangci_lint_isolated_to_active_worktree
test_case "formatter failures print offending files" test_go_formatter_failure_prints_files
test_case "quality_gate.sh uses scoped lint cache" test_quality_gate_uses_scoped_lint_cache
test_case "quality_gate.sh run lock timeout preserves holder" test_quality_gate_run_lock_timeout_preserves_holder
test_case "quality_gate.sh archives stale legacy run lock" test_quality_gate_run_lock_archives_stale_legacy_lock
test_case "quality_gate.sh garbage-collects archived stale run locks" test_quality_gate_archived_stale_locks_are_garbage_collected
test_case "quality_gate.sh caps Go scheduler fanout" test_quality_gate_caps_go_scheduler_fanout
test_case "go coverage threshold skips uncovered Go surfaces" test_go_coverage_threshold_skips_uncovered_go_surfaces
test_case "quality_gate.sh Python tools avoid pyenv shims" test_quality_gate_python_tools_avoid_pyenv_shims
test_case "generated quality gate Python tools avoid pyenv shims" test_generated_quality_gate_python_tools_avoid_pyenv_shims
test_case "pyright uses active worktree virtual environment" test_pyright_uses_active_worktree_venv
test_case "quality_gate.sh filesystem walkers are source scoped" test_quality_gate_filesystem_walkers_are_source_scoped
test_case "quality_gate.sh invalid locale sanitized" test_quality_gate_invalid_locale_sanitized
test_case "quality_gate.sh invalid locale bootstraps before bash" test_quality_gate_invalid_locale_bootstraps_before_bash
test_case "quality_gate.sh bootstrap selects Bash 4+ or fails clearly" test_quality_gate_bootstrap_selects_bash4_or_fails
test_case "quality_gate.sh bootstrap ignores inherited shell state" test_quality_gate_bootstrap_ignores_inherited_shell_state
test_case "quality_gate.sh nested invocation exits before lanes" test_quality_gate_nested_invocation_exits_before_lanes
test_case "quality_gate.sh conflict markers fail preflight" test_quality_gate_conflict_markers_fail_preflight
test_case "Makefile git diff has 2>/dev/null" test_makefile_git_diff_stderr_redirect
test_case "Makefile \$\$changed is quoted" test_makefile_changed_is_quoted
test_case "Makefile mutate-py uses PID-isolated path" test_makefile_mutate_py_pid_isolated
test_case "Makefile mutate-py-full uses PID-isolated path" test_makefile_mutate_py_full_pid_isolated

echo ""
printf '%bPassed:%b %d\n' "$GREEN" "$NC" "$PASS"
printf '%bFailed:%b %d\n' "$RED" "$NC" "$FAIL"

if [ "$FAIL" -gt 0 ]; then
	exit 1
fi

exit 0
