#!/usr/bin/env bash
# =============================================================================
# Oro Quality Gate — Full quality check with Go and Python tiers
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
        ((PASS++))
    else
        printf '%b✗ FAIL%b\n' "$RED" "$NC"
        head -20 /tmp/check-output.txt
        ((FAIL++))
    fi
}

header() {
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo " $1"
    echo "═══════════════════════════════════════════════════════════════"
}

# =============================================================================
# DETECT PROJECT TYPE
# =============================================================================

HAS_GO=false
HAS_PYTHON=false

if [ -f "go.mod" ]; then
    HAS_GO=true
fi

if [ -f "pyproject.toml" ] || [ -f "setup.py" ] || [ -f "requirements.txt" ]; then
    HAS_PYTHON=true
fi

header "ORO QUALITY GATE"

echo ""
echo "Running all quality checks..."
if $HAS_GO; then echo "  Detected: Go project"; fi
if $HAS_PYTHON; then echo "  Detected: Python project"; fi
echo ""

# =============================================================================
# GO CHECKS
# =============================================================================

if $HAS_GO; then

    # Go source directories (avoid scanning references/, yap/, etc.)
    GO_DIRS="cmd internal pkg"

    # Tier 1: Fast checks (< 5s)
    header "GO TIER 1: FORMATTING"
    check "gofumpt" "! gofumpt -l $GO_DIRS 2>/dev/null | grep -q '^'"
    check "goimports" "! goimports -l $GO_DIRS 2>/dev/null | grep -q '^'"

    # Tier 2: Linting (< 30s)
    header "GO TIER 2: LINTING"
    check "golangci-lint" "golangci-lint run --timeout 5m ./cmd/... ./internal/... ./pkg/..."

    # Tier 3: Testing (variable)
    header "GO TIER 3: TESTING"
    check "go test" "go test -race -shuffle=on ./..."
    check "coverage" "go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | grep total | awk '{print \$3}' | sed 's/%//' | awk '{if (\$1 < 70) exit 1}'"

    # Tier 4: Security (< 30s)
    header "GO TIER 4: SECURITY"
    check "govulncheck" "govulncheck ./..."

    # Tier 5: Build verification
    header "GO TIER 5: BUILD"
    check "go build" "go build ./..."
    check "go vet" "go vet ./..."

fi

# =============================================================================
# PYTHON CHECKS
# =============================================================================

if $HAS_PYTHON; then

    # Tier 1: Formatting
    header "PYTHON TIER 1: FORMATTING"
    check "ruff format" "ruff format --check ."

    # Tier 2: Linting
    header "PYTHON TIER 2: LINTING"
    check "ruff check" "ruff check ."

    # Tier 3: Testing
    header "PYTHON TIER 3: TESTING"
    check "pytest" "uv run pytest"

fi

# =============================================================================
# SUMMARY
# =============================================================================

header "SUMMARY"

echo ""
printf "${GREEN}Passed:${NC} %d\n" "$PASS"
printf "${RED}Failed:${NC} %d\n" "$FAIL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    printf '%bQuality gate FAILED%b\n' "$RED" "$NC"
    exit 1
else
    printf '%bQuality gate PASSED%b\n' "$GREEN" "$NC"
    exit 0
fi
