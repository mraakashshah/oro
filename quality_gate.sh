#!/usr/bin/env bash
# =============================================================================
# Oro Quality Gate — Comprehensive quality checks
# =============================================================================

set -euo pipefail

# Unset git hook env vars that leak into test subprocesses.
# When called from pre-push/pre-commit hooks, git sets GIT_DIR and
# GIT_WORK_TREE which cause test-created git repos to reference the
# parent repo instead of their own .git directories.
unset GIT_DIR GIT_WORK_TREE

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
# shellcheck disable=SC2034
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Counters
PASS=0
FAIL=0

check() {
    local name="$1"
    local cmd="$2"

    printf '%b▶%b %-30s' "$BLUE" "$NC" "$name"

    if eval "$cmd" > /tmp/check-output.txt 2>&1; then
        printf '%b✓ PASS%b\n' "$GREEN" "$NC"
        PASS=$((PASS + 1))
    else
        printf '%b✗ FAIL%b\n' "$RED" "$NC"
        head -20 /tmp/check-output.txt
        FAIL=$((FAIL + 1))
    fi
}

header() {
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo " $1"
    echo "═══════════════════════════════════════════════════════════════"
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

header "ORO QUALITY GATE"

echo ""
echo "Running all quality checks..."
if $HAS_GO; then echo "  Detected: Go project"; fi
if $HAS_PYTHON; then echo "  Detected: Python project"; fi
if $HAS_SHELL; then echo "  Detected: Shell scripts"; fi
echo ""

# =============================================================================
# GO CHECKS
# =============================================================================

if $HAS_GO; then

    GO_DIRS="cmd internal pkg"

    # Stage embedded assets (go:embed in cmd/oro requires _assets/ dir)
    make stage-assets 2>/dev/null || true

    header "GO TIER 1: FORMATTING"
    check "gofumpt" "test -z \"\$(gofumpt -l $GO_DIRS 2>/dev/null)\""
    check "goimports" "test -z \"\$(goimports -l $GO_DIRS 2>/dev/null)\""

    header "GO TIER 2: LINTING"
    check "golangci-lint" "GOFLAGS=-buildvcs=false golangci-lint run --timeout 5m ./cmd/... ./internal/... ./pkg/..."

    header "GO TIER 3: DEAD CODE"
    # Detect exported functions in pkg/ and internal/ only referenced from test files.
    # shellcheck disable=SC2317,SC2329
    check_dead_exports() {
        local dead_found=0
        local checked=0
        local skipped=0

        # Collect all exported function declarations from non-test Go files.
        # Handles both standalone: func FuncName(...)
        # and methods:             func (r *Type) FuncName(...)
        while IFS=: read -r file lineno line; do
            # Extract function name: last word before the opening paren
            local func_name
            func_name=$(echo "$line" | sed -E 's/^func[[:space:]]+(\([^)]*\)[[:space:]]+)?([A-Z][A-Za-z0-9_]*).*/\2/')
            if [ -z "$func_name" ]; then
                continue
            fi

            checked=$((checked + 1))

            # Check for //oro:testonly suppression in the godoc block above.
            # Scan upward through consecutive comment lines.
            local suppressed=false
            local scan=$((lineno - 1))
            while [ "$scan" -ge 1 ]; do
                local scan_line
                scan_line=$(sed -n "${scan}p" "$file")
                if echo "$scan_line" | grep -q '//oro:testonly'; then
                    suppressed=true
                    break
                elif echo "$scan_line" | grep -qE '^[[:space:]]*//' ; then
                    # Still in a comment block, keep scanning
                    scan=$((scan - 1))
                else
                    break  # Non-comment line — stop
                fi
            done
            if $suppressed; then
                skipped=$((skipped + 1))
                continue
            fi

            # Search for this function name in non-test Go files, excluding
            # the declaration line and comment-only lines (godoc, etc.).
            # We look in pkg/, internal/, AND cmd/ so that wiring from cmd/
            # or same-file callers count.
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
    check "dead exports" "check_dead_exports"

    header "GO TIER 4: ARCHITECTURE"
    if [ -f ".go-arch-lint.yml" ]; then
        check "go-arch-lint" "go-arch-lint check --project-path ."
    fi

    header "GO TIER 5: TESTING"
    COVERAGE_FILE="/tmp/oro-coverage-$$.out"
    check "go test" "GOFLAGS=-buildvcs=false go test -race -shuffle=on -p 2 -coverprofile=$COVERAGE_FILE ./internal/... ./pkg/... && go tool cover -func=$COVERAGE_FILE | grep total | awk '{print \$3}' | sed 's/%//' | awk '{if (\$1 < 85) exit 1}'"
    check "coverage" "go tool cover -func=$COVERAGE_FILE | tail -1"
    rm -f "$COVERAGE_FILE"

    header "GO TIER 6: SECURITY"
    check "govulncheck" "govulncheck ./..."

    header "GO TIER 7: BUILD"
    check "go build" "go build -buildvcs=false ./..."
    check "go vet" "go vet ./..."

    # Clean staged embedded assets
    make clean-assets 2>/dev/null || true

fi

# =============================================================================
# SHELL CHECKS
# =============================================================================

if $HAS_SHELL; then

    header "SHELL: LINT"
    check "shellcheck" "find . -name '*.sh' -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -exec shellcheck --severity=info {} +"

fi

# =============================================================================
# DOCS & CONFIG CHECKS
# =============================================================================

header "DOCS & CONFIG"
check "markdownlint" "markdownlint --config .markdownlint.yml 'docs/**/*.md' '*.md' --ignore references --ignore yap --ignore archive"
check "yamllint" "find . \\( -name '*.yml' -o -name '*.yaml' \\) -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -not -path './node_modules/*' | xargs yamllint -d relaxed --no-warnings"
# Only check paths that actually exist (worktrees may not have all directories)
BIOME_PATHS=""
for p in docs/ .github/ .beads/; do
    [ -d "$p" ] && BIOME_PATHS="$BIOME_PATHS $p"
done
# Check for JSON files in project root
if compgen -G "*.json" > /dev/null 2>&1; then
    BIOME_PATHS="$BIOME_PATHS *.json"
fi
if [ -n "$BIOME_PATHS" ]; then
    # shellcheck disable=SC2086
    check "biome (json)" "biome check --files-ignore-unknown=true $BIOME_PATHS"
fi

# =============================================================================
# PYTHON CHECKS
# =============================================================================

if $HAS_PYTHON; then

    header "PYTHON TIER 1: FORMATTING"
    check "ruff format" "ruff format --check ."

    header "PYTHON TIER 2: LINTING"
    check "ruff check" "ruff check ."
    if command -v pylint >/dev/null 2>&1; then
        check "pylint" "find . -name '*.py' -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' | xargs pylint --disable=all --enable=E"
    fi

    header "PYTHON TIER 3: TYPE CHECKING"
    if command -v pyright >/dev/null 2>&1 && pyright --version >/dev/null 2>&1; then
        check "pyright" "pyright"
    fi

    header "PYTHON TIER 4: TESTING"
    if compgen -G "tests/test_*.py" > /dev/null 2>&1 || compgen -G "tests/**/test_*.py" > /dev/null 2>&1; then
        check "pytest" "uv run pytest"
    fi

fi

# =============================================================================
# SUMMARY
# =============================================================================

header "SUMMARY"

echo ""
printf '%bPassed:%b %d\n' "$GREEN" "$NC" "$PASS"
printf '%bFailed:%b %d\n' "$RED" "$NC" "$FAIL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    printf '%bQuality gate FAILED%b\n' "$RED" "$NC"
    exit 1
else
    printf '%bQuality gate PASSED%b\n' "$GREEN" "$NC"
    exit 0
fi
