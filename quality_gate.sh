#!/usr/bin/env bash
# =============================================================================
# Oro Quality Gate — Comprehensive quality checks
# =============================================================================

set -euo pipefail

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

    header "GO TIER 1: FORMATTING"
    check "gofumpt" "test -z \"\$(gofumpt -l $GO_DIRS 2>/dev/null)\""
    check "goimports" "test -z \"\$(goimports -l $GO_DIRS 2>/dev/null)\""

    header "GO TIER 2: LINTING"
    check "golangci-lint" "golangci-lint run --timeout 5m ./cmd/... ./internal/... ./pkg/..."

    header "GO TIER 3: ARCHITECTURE"
    check "go-arch-lint" "go-arch-lint check --project-path ."

    header "GO TIER 4: TESTING"
    check "go test" "go test -race -shuffle=on ./..."
    check "coverage" "go test -coverprofile=coverage.out ./internal/... ./pkg/... && go tool cover -func=coverage.out | grep total | awk '{print \$3}' | sed 's/%//' | awk '{if (\$1 < 70) exit 1}'"

    header "GO TIER 5: SECURITY"
    check "govulncheck" "govulncheck ./..."

    header "GO TIER 6: BUILD"
    check "go build" "go build ./..."
    check "go vet" "go vet ./..."

fi

# =============================================================================
# SHELL CHECKS
# =============================================================================

if $HAS_SHELL; then

    header "SHELL: LINT"
    check "shellcheck" "find . -name '*.sh' -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -exec shellcheck {} +"

fi

# =============================================================================
# DOCS & CONFIG CHECKS
# =============================================================================

header "DOCS & CONFIG"
check "markdownlint" "markdownlint --config .markdownlint.yml 'docs/**/*.md' '*.md' --ignore references --ignore yap --ignore archive"
check "yamllint" "find . \\( -name '*.yml' -o -name '*.yaml' \\) -not -path './references/*' -not -path './yap/*' -not -path './archive/*' -not -path './.worktrees/*' -not -path './node_modules/*' | xargs yamllint -d relaxed --no-warnings"
check "biome (json)" "biome check --files-ignore-unknown=true docs/ .github/ .beads/ *.json"

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
    check "pytest" "uv run pytest"

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
