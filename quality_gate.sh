#!/usr/bin/env bash
# =============================================================================
# Oro Quality Gate — Parallel lanes with early exit
#
# Architecture: 4 independent lanes (Go, Python, Shell, Docs) run in
# parallel. Within each lane, checks run in tiers — independent checks
# within a tier run in parallel, and the lane bails on first tier failure.
# Wall-clock time ≈ max(lane) instead of sum(all checks).
# =============================================================================

set -euo pipefail

# Unset git hook env vars that leak into test subprocesses.
unset GIT_DIR GIT_WORK_TREE

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
# shellcheck disable=SC2034
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Temp directory for all check outputs (cleaned up on exit)
QG_DIR=$(mktemp -d "${TMPDIR:-/tmp}/qg-$$-XXXXXX")
trap 'rm -rf "$QG_DIR"' EXIT

# =============================================================================
# PRIMITIVES
# =============================================================================

header() {
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo " $1"
    echo "═══════════════════════════════════════════════════════════════"
}

# Run a single check. Returns 0 on pass, 1 on fail.
# Output goes to the lane's captured stdout.
check() {
    local name="$1"
    local cmd="$2"
    local slug
    slug=$(echo "$name" | tr ' ()/.' '------')
    local out="$QG_DIR/check-${slug}-${RANDOM}.out"

    printf '%b▶%b %-30s' "$BLUE" "$NC" "$name"

    if eval "$cmd" > "$out" 2>&1; then
        printf '%b✓ PASS%b\n' "$GREEN" "$NC"
        return 0
    else
        printf '%b✗ FAIL%b\n' "$RED" "$NC"
        head -20 "$out"
        return 1
    fi
}

# Run multiple checks in parallel, preserving output order.
# Sets TIER_PASS and TIER_FAIL for the caller.
# Usage: parallel_checks "name1" "cmd1" "name2" "cmd2" ...
parallel_checks() {
    TIER_PASS=0
    TIER_FAIL=0
    local i=0
    local pids=()
    local tier_id="${RANDOM}${RANDOM}"

    while [ $# -ge 2 ]; do
        local name="$1" cmd="$2"; shift 2
        local pfx="$QG_DIR/pc-${tier_id}-${i}"
        (
            local cmd_out="${pfx}.cmd-out"
            if eval "$cmd" > "$cmd_out" 2>&1; then
                printf '%b▶%b %-30s%b✓ PASS%b\n' "$BLUE" "$NC" "$name" "$GREEN" "$NC" > "${pfx}.display"
                echo "pass" > "${pfx}.rc"
            else
                {
                    printf '%b▶%b %-30s%b✗ FAIL%b\n' "$BLUE" "$NC" "$name" "$RED" "$NC"
                    head -20 "$cmd_out"
                } > "${pfx}.display"
                echo "fail" > "${pfx}.rc"
            fi
        ) &
        pids+=($!)
        i=$((i + 1))
    done

    for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done

    local j=0
    while [ "$j" -lt "$i" ]; do
        local pfx="$QG_DIR/pc-${tier_id}-${j}"
        cat "${pfx}.display" 2>/dev/null || true
        if [ "$(cat "${pfx}.rc" 2>/dev/null || echo fail)" = "pass" ]; then
            TIER_PASS=$((TIER_PASS + 1))
        else
            TIER_FAIL=$((TIER_FAIL + 1))
        fi
        j=$((j + 1))
    done
}

# =============================================================================
# DETECT PROJECT TYPES
# =============================================================================

HAS_GO=false
HAS_PYTHON=false
HAS_SHELL=false

if [ -f "go.mod" ]; then HAS_GO=true; fi
if [ -f "pyproject.toml" ] || [ -f "setup.py" ] || [ -f "requirements.txt" ]; then HAS_PYTHON=true; fi
if compgen -G "*.sh" > /dev/null || compgen -G "ad_hoc/*.sh" > /dev/null; then HAS_SHELL=true; fi

# =============================================================================
# LANE: GO
# =============================================================================

# shellcheck disable=SC2317
lane_go() {
    local pass=0 fail=0

    if ! $HAS_GO; then
        echo "${pass}:${fail}" > "$QG_DIR/go.rc"
        return
    fi

    local GO_DIRS="cmd internal pkg"
    make stage-assets 2>/dev/null || true

    # --- Tier 1: Formatting (parallel) ---
    header "GO TIER 1: FORMATTING"
    parallel_checks \
        "gofumpt" "test -z \"\$(gofumpt -l $GO_DIRS 2>/dev/null)\"" \
        "goimports" "test -z \"\$(goimports -l $GO_DIRS 2>/dev/null)\""
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; make clean-assets 2>/dev/null || true; return; fi

    # --- Tier 2: Lint + Dead Code + Architecture (parallel) ---
    header "GO TIER 2: LINT + DEAD CODE + ARCHITECTURE"

    # Dead exports detector (function must be defined before parallel_checks eval's it)
    # shellcheck disable=SC2329
    check_dead_exports() {
        local dead_found=0
        local checked=0
        local skipped=0

        while IFS=: read -r file lineno line; do
            local func_name
            func_name=$(echo "$line" | sed -E 's/^func[[:space:]]+(\([^)]*\)[[:space:]]+)?([A-Z][A-Za-z0-9_]*).*/\2/')
            if [ -z "$func_name" ]; then
                continue
            fi

            checked=$((checked + 1))

            local suppressed=false
            local scan=$((lineno - 1))
            while [ "$scan" -ge 1 ]; do
                local scan_line
                scan_line=$(sed -n "${scan}p" "$file")
                if echo "$scan_line" | grep -q '//oro:testonly'; then
                    suppressed=true
                    break
                elif echo "$scan_line" | grep -qE '^[[:space:]]*//' ; then
                    scan=$((scan - 1))
                else
                    break
                fi
            done
            if $suppressed; then
                skipped=$((skipped + 1))
                continue
            fi

            local callers
            callers=$(grep -rn --include="*.go" --exclude="*_test.go" "\\b${func_name}\\b" pkg/ internal/ cmd/ \
                | grep -v "^${file}:${lineno}:" \
                | grep -v -E '^[^:]+:[0-9]+:[[:space:]]*//' \
                || true)

            if [ -z "$callers" ]; then
                echo "DEAD EXPORT: ${func_name} in ${file}:${lineno}"
                echo "  Only referenced from test files (or not at all outside its own file)"
                dead_found=$((dead_found + 1))
            fi
        done < <(grep -rn --include="*.go" --exclude="*_test.go" -E '^func[[:space:]]+(\([^)]*\)[[:space:]]+)?[A-Z]' pkg/ internal/)

        echo ""
        echo "Checked ${checked} exported functions, skipped ${skipped} (testonly), found ${dead_found} dead"

        if [ "$dead_found" -gt 0 ]; then
            echo ""
            echo "Fix: wire these functions from production code, remove them, or add //oro:testonly above."
            return 1
        fi
        return 0
    }

    local tier2_checks=(
        "golangci-lint" "GOFLAGS=-buildvcs=false golangci-lint run --timeout 5m ./cmd/... ./internal/... ./pkg/..."
        "dead exports" "check_dead_exports"
    )
    if [ -f ".go-arch-lint.yml" ] && command -v go-arch-lint >/dev/null 2>&1; then
        tier2_checks+=("go-arch-lint" "go-arch-lint check --project-path .")
    fi
    parallel_checks "${tier2_checks[@]}"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; make clean-assets 2>/dev/null || true; return; fi

    # --- Tier 3: Test + Security + Build (parallel) ---
    header "GO TIER 3: TEST + SECURITY + BUILD"

    local COVERAGE_FILE="$QG_DIR/coverage-$$.out"

    # shellcheck disable=SC2329
    go_test_with_coverage() {
        GOFLAGS=-buildvcs=false go test -race -shuffle=on -p 2 \
            -coverprofile="$COVERAGE_FILE" ./internal/... ./pkg/... || return 1
        local cov
        cov=$(go tool cover -func="$COVERAGE_FILE" | grep total | awk '{print $3}' | sed 's/%//')
        echo "Coverage: ${cov}%"
        if [ "$(echo "$cov < 85" | bc -l)" -eq 1 ]; then
            echo "FAIL: coverage ${cov}% is below 85% threshold"
            return 1
        fi
    }

    local tier3_checks=(
        "go test + coverage" "go_test_with_coverage"
        "go build" "go build -buildvcs=false ./..."
        "go vet" "go vet ./..."
    )
    if command -v govulncheck >/dev/null 2>&1; then
        tier3_checks+=("govulncheck" "govulncheck ./...")
    fi
    parallel_checks "${tier3_checks[@]}"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; make clean-assets 2>/dev/null || true; return; fi

    # --- Tier 4: Mutation Testing (sequential, modifies working tree) ---
    if [ "${ORO_SKIP_MUTATION:-}" = "1" ]; then
        header "GO TIER 4: MUTATION TESTING (skipped — ORO_SKIP_MUTATION=1)"
    elif command -v go-mutesting >/dev/null 2>&1; then
        header "GO TIER 4: MUTATION TESTING (incremental)"

        # shellcheck disable=SC2329
        run_go_mutation_test() {
            local changed
            changed=$(git diff --name-only main -- '*.go' 2>/dev/null \
                | grep -v '_test\.go$' \
                | grep -v '_generated\.' \
                | grep -v 'cmd/oro/_assets' \
                || true)
            if [ -z "$changed" ]; then
                echo "No changed Go files to mutate — skipping"
                return 0
            fi
            # Restore source files on exit (handles Ctrl-C, OOM, timeout, and normal exit)
            trap 'git checkout -- pkg/ internal/ cmd/ 2>/dev/null || true' EXIT
            echo "Mutating changed files: $changed"
            local output
            # shellcheck disable=SC2086
            output=$(go-mutesting --exec-timeout=30 $changed 2>&1)
            git checkout -- pkg/ internal/ cmd/ 2>/dev/null || true
            echo "$output"
            local score
            score=$(echo "$output" | grep "The mutation score is" | awk '{print $5}')
            if [ -z "$score" ]; then
                echo "No mutations generated for changed files — skipping"
                return 0
            fi
            if [ "$(echo "$score < 0.75" | bc -l)" -eq 1 ]; then
                echo "FAIL: mutation score $score for changed files is below 0.75 threshold"
                return 1
            fi
            echo "PASS: mutation score $score meets 0.75 threshold"
        }

        if check "go-mutesting" "run_go_mutation_test"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    fi

    make clean-assets 2>/dev/null || true
    echo "${pass}:${fail}" > "$QG_DIR/go.rc"
}

# =============================================================================
# LANE: PYTHON
# =============================================================================

# shellcheck disable=SC2317
lane_python() {
    local pass=0 fail=0

    if ! $HAS_PYTHON; then
        echo "${pass}:${fail}" > "$QG_DIR/python.rc"
        return
    fi

    # --- Tier 1: Formatting ---
    header "PYTHON TIER 1: FORMATTING"
    if check "ruff format" "ruff format --check ."; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
        echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return
    fi

    # --- Tier 2: Linting (parallel) ---
    header "PYTHON TIER 2: LINTING"
    local tier2_checks=("ruff check" "ruff check .")
    if command -v pylint >/dev/null 2>&1; then
        tier2_checks+=("pylint" "find . -name '*.py' -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -not -path './assets/*' -not -path './.venv/*' -not -path './.claude/hooks/*' | xargs pylint --disable=all --enable=E --disable=import-error")
    fi
    parallel_checks "${tier2_checks[@]}"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return; fi

    # --- Tier 3: Type Checking ---
    header "PYTHON TIER 3: TYPE CHECKING"
    if command -v pyright >/dev/null 2>&1 && pyright --version >/dev/null 2>&1; then
        if check "pyright" "pyright"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
            echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return
        fi
    fi

    # --- Tier 4: Testing ---
    header "PYTHON TIER 4: TESTING"
    if compgen -G "tests/test_*.py" > /dev/null 2>&1 || compgen -G "tests/**/test_*.py" > /dev/null 2>&1; then
        if check "pytest" "uv run pytest"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
            echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return
        fi
    fi

    # --- Tier 5: Mutation Testing ---
    if [ "${ORO_SKIP_MUTATION:-}" = "1" ]; then
        header "PYTHON TIER 5: MUTATION TESTING (skipped — ORO_SKIP_MUTATION=1)"
    elif [ -f "cosmic-ray.toml" ] && command -v uv >/dev/null 2>&1; then
        header "PYTHON TIER 5: MUTATION TESTING (incremental)"
        local CR_SESSION="$QG_DIR/cr-$$.sqlite"

        # shellcheck disable=SC2329
        run_mutation_test() {
            local changed
            changed=$(git diff --name-only main -- '*.py' 2>/dev/null \
                | grep -v 'test_' \
                | grep -v '__pycache__' \
                | grep -v 'archive/' \
                || true)
            if [ -z "$changed" ]; then
                echo "No changed Python files to mutate — skipping"
                return 0
            fi
            echo "Changed Python files: $changed"
            uv run cosmic-ray init cosmic-ray.toml "$CR_SESSION" --force 2>&1 && \
            uv run cosmic-ray exec cosmic-ray.toml "$CR_SESSION" 2>&1 && \
            uv run cr-report "$CR_SESSION" 2>&1 && \
            uv run cr-rate "$CR_SESSION" --fail-over 50 2>&1
        }

        if check "cosmic-ray" "run_mutation_test"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    fi

    echo "${pass}:${fail}" > "$QG_DIR/python.rc"
}

# =============================================================================
# LANE: SHELL + DOCS (lightweight, combined into one lane)
# =============================================================================

# shellcheck disable=SC2317
lane_other() {
    local pass=0 fail=0

    if $HAS_SHELL; then
        header "SHELL: LINT"
        if check "shellcheck" "find . -name '*.sh' -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -exec shellcheck --severity=info {} +"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    fi

    header "DOCS & CONFIG"
    # Build biome paths
    local BIOME_PATHS=""
    for p in docs/ .github/ .beads/; do
        [ -d "$p" ] && BIOME_PATHS="$BIOME_PATHS $p"
    done
    if compgen -G "*.json" > /dev/null 2>&1; then
        BIOME_PATHS="$BIOME_PATHS *.json"
    fi

    local docs_checks=(
        "markdownlint" "markdownlint --config .markdownlint.yml 'docs/**/*.md' '*.md' --ignore references --ignore yap --ignore archive"
        "yamllint" "find . \\( -name '*.yml' -o -name '*.yaml' \\) -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -not -path './node_modules/*' | xargs yamllint -d relaxed --no-warnings"
    )
    if [ -n "$BIOME_PATHS" ]; then
        # shellcheck disable=SC2086
        docs_checks+=("biome (json)" "biome check --files-ignore-unknown=true $BIOME_PATHS")
    fi
    parallel_checks "${docs_checks[@]}"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))

    echo "${pass}:${fail}" > "$QG_DIR/other.rc"
}

# =============================================================================
# MAIN: Run lanes in parallel, aggregate results
# =============================================================================

header "ORO QUALITY GATE"

echo ""
echo "Running quality checks in parallel..."
if $HAS_GO; then echo "  Detected: Go project"; fi
if $HAS_PYTHON; then echo "  Detected: Python project"; fi
if $HAS_SHELL; then echo "  Detected: Shell scripts"; fi
echo ""

# Launch all lanes in parallel, each writing output to a file
lane_go     > "$QG_DIR/go.out"     2>&1 &
PID_GO=$!
lane_python > "$QG_DIR/python.out" 2>&1 &
PID_PY=$!
lane_other  > "$QG_DIR/other.out"  2>&1 &
PID_OT=$!

# Wait for all lanes
wait "$PID_GO" 2>/dev/null || true
wait "$PID_PY" 2>/dev/null || true
wait "$PID_OT" 2>/dev/null || true

# Display results in order: Go, Shell+Docs, Python
cat "$QG_DIR/go.out"     2>/dev/null || true
cat "$QG_DIR/other.out"  2>/dev/null || true
cat "$QG_DIR/python.out" 2>/dev/null || true

# Aggregate pass/fail counts
TOTAL_PASS=0
TOTAL_FAIL=0
for rc_file in "$QG_DIR"/go.rc "$QG_DIR"/python.rc "$QG_DIR"/other.rc; do
    if [ -f "$rc_file" ]; then
        IFS=: read -r p f < "$rc_file"
        TOTAL_PASS=$((TOTAL_PASS + p))
        TOTAL_FAIL=$((TOTAL_FAIL + f))
    fi
done

# Summary
header "SUMMARY"

echo ""
printf '%bPassed:%b %d\n' "$GREEN" "$NC" "$TOTAL_PASS"
printf '%bFailed:%b %d\n' "$RED" "$NC" "$TOTAL_FAIL"
echo ""

if [ "$TOTAL_FAIL" -gt 0 ]; then
    printf '%bQuality gate FAILED%b\n' "$RED" "$NC"
    exit 1
else
    printf '%bQuality gate PASSED%b\n' "$GREEN" "$NC"
    exit 0
fi
