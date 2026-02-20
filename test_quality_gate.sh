#!/usr/bin/env bash
# =============================================================================
# Test harness for quality_gate.sh config-driven behavior
# =============================================================================

set -euo pipefail

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
    cat > go.mod <<'EOF'
module test
go 1.22
EOF

    mkdir -p .oro
    cat > .oro/config.yaml <<'EOF'
languages:
  go:
    formatters:
      - gofumpt
EOF

    # Copy quality_gate.sh
    cp "$oldpwd/quality_gate.sh" .

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
    cat > go.mod <<'EOF'
module test
go 1.22
EOF

    # Copy quality_gate.sh
    cp "$oldpwd/quality_gate.sh" .

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
    cat > .oro/config.yaml <<'EOF'
languages:
  go:
    formatters:
      - gofumpt
      - nonexistent-formatter-xyz
EOF

    cat > go.mod <<'EOF'
module test
go 1.22
EOF

    # Copy quality_gate.sh
    cp "$oldpwd/quality_gate.sh" .

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

# =============================================================================
# Trap EXIT Tests (oro-bl44): mutation testing cleanup on interrupt
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Test: quality_gate.sh run_go_mutation_test has trap EXIT handler
# shellcheck disable=SC2317,SC2329
test_quality_gate_mutation_trap_present() {
    # Extract run_go_mutation_test function body (up to 60 lines after definition)
    # and verify a trap EXIT handler is present
    if grep -A 60 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | grep -q 'trap.*EXIT'; then
        return 0
    fi
    echo "FAIL: quality_gate.sh run_go_mutation_test() has no trap EXIT handler"
    echo "  Mutated source files will remain on disk if go-mutesting is killed"
    return 1
}

# Test: Makefile mutate-go target has trap EXIT handler
# shellcheck disable=SC2317,SC2329
test_makefile_mutate_go_trap_present() {
    # Extract lines from mutate-go: target to next target and check for trap
    local target_body
    target_body=$(awk '/^mutate-go:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go:/{f=0} f' "$SCRIPT_DIR/Makefile")
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
    target_body=$(awk '/^mutate-go-diff:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go-diff:/{f=0} f' "$SCRIPT_DIR/Makefile")
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

    # Must have a check for main branch existence (rev-parse, merge-base, or warning message)
    if echo "$fn_body" | grep -qE 'rev-parse.*verify.*main|merge-base.*main|Cannot find main'; then
        return 0
    fi

    echo "FAIL: run_go_mutation_test() does not check for main branch existence"
    echo "  'git diff --name-only main 2>/dev/null || true' silently returns empty"
    echo "  when main branch is absent → silent PASS (oro-xgwr)"
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

# Test: missing main branch warning appears in function output text
# shellcheck disable=SC2317,SC2329
test_mutation_missing_main_warning_message() {
    # The warning must include 'Cannot find main branch' (or similar 'main' + warning)
    if grep -A 80 'run_go_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | \
            grep -qiE 'Cannot find main|main.*not found|main.*missing|WARNING.*main'; then
        return 0
    fi

    echo "FAIL: run_go_mutation_test() does not print a 'Cannot find main branch' warning"
    echo "  Acceptance criteria require the warning message to be present (oro-xgwr)"
    return 1
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

    if echo "$fn_body" | grep -qE 'rev-parse.*verify.*main|merge-base.*main|Cannot find main'; then
        return 0
    fi

    echo "FAIL: Python run_mutation_test() does not check for main branch existence"
    echo "  Same bug as Go mutation — silent PASS when main is absent (oro-xgwr)"
    return 1
}

# Test: Python run_mutation_test prints warning when main branch missing
# shellcheck disable=SC2317,SC2329
test_python_mutation_missing_main_warning_message() {
    if grep -A 40 'run_mutation_test()' "$SCRIPT_DIR/quality_gate.sh" | \
            grep -qiE 'Cannot find main|main.*not found|main.*missing|WARNING.*main'; then
        return 0
    fi

    echo "FAIL: Python run_mutation_test() does not print a 'Cannot find main branch' warning"
    echo "  Acceptance criteria require the warning message to be present (oro-xgwr)"
    return 1
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

# Test: Makefile mutate-go-diff git diff has 2>/dev/null
# shellcheck disable=SC2317,SC2329
test_makefile_git_diff_stderr_redirect() {
    local target_body
    target_body=$(awk '/^mutate-go-diff:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go-diff:/{f=0} f' "$SCRIPT_DIR/Makefile")

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
    target_body=$(awk '/^mutate-go-diff:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-go-diff:/{f=0} f' "$SCRIPT_DIR/Makefile")

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
    target_body=$(awk '/^mutate-py:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-py:/{f=0} f' "$SCRIPT_DIR/Makefile")

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
    target_body=$(awk '/^mutate-py-full:/{f=1} f && /^[a-zA-Z]/ && !/^mutate-py-full:/{f=0} f' "$SCRIPT_DIR/Makefile")

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

echo ""
echo "Testing mutation trap handlers (oro-bl44)"
echo "=============================================="

test_case "quality_gate.sh mutation has trap EXIT" test_quality_gate_mutation_trap_present
test_case "Makefile mutate-go has trap" test_makefile_mutate_go_trap_present
test_case "Makefile mutate-go-diff has trap" test_makefile_mutate_go_diff_trap_present

echo ""
echo "Testing missing main branch + crash detection (oro-xgwr)"
echo "=============================================="

test_case "mutation checks main branch existence" test_mutation_checks_main_branch_existence
test_case "mutation crash flagged as FAIL" test_mutation_crash_flagged_as_fail
test_case "mutation missing-main warning message" test_mutation_missing_main_warning_message

echo ""
echo "Testing Python mutation missing main branch (oro-xgwr)"
echo "=============================================="

test_case "python mutation checks main branch" test_python_mutation_checks_main_branch_existence
test_case "python mutation missing-main warning" test_python_mutation_missing_main_warning_message

echo ""
echo "Testing shell correctness in mutation testing (oro-koon)"
echo "=============================================="

test_case "no SC2086 disable for \$changed" test_no_sc2086_disable_for_changed
test_case "quality_gate.sh \$changed is quoted" test_quality_gate_changed_is_quoted
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
