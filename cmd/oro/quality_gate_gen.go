package main

import (
	"bytes"
	"fmt"
	"text/template"

	"oro/pkg/langprofile"
)

// golangciLintTemplate is the .golangci.yml content for Go projects (v2 format).
// This matches oro's own .golangci.yml (192 lines, version 2).
const golangciLintTemplate = `# golangci-lint configuration (v2 format)
version: "2"

run:
  timeout: 5m
  modules-download-mode: readonly

linters:
  default: none
  enable:
    # Correctness
    - staticcheck
    - govet
    - ineffassign
    - unused
    - errcheck

    # Error handling
    - errorlint
    - wrapcheck
    - nilerr
    - errname

    # Complexity
    - gocyclo
    - gocognit
    - funlen
    - nestif

    # Structure
    - gochecknoglobals
    - gochecknoinits
    - testpackage

    # Resource safety
    - bodyclose
    - noctx
    - durationcheck

    # Cleanup
    - unconvert
    - unparam
    - nakedret
    - whitespace

    # Duplication
    - dupl

    # Testing
    - thelper

    # Security
    - gosec

    # Style
    - gocritic
    - revive
    - misspell

    # Performance
    - prealloc

  settings:
    gocyclo:
      min-complexity: 15

    gocognit:
      min-complexity: 20

    funlen:
      lines: 60
      statements: 40
      ignore-comments: true

    nestif:
      min-complexity: 4

    errcheck:
      check-type-assertions: true
      exclude-functions:
        - (io.Closer).Close
        - (*os.File).Close

    wrapcheck:
      ignore-sigs:
        - .Errorf(
        - errors.New(
        - errors.Join(

    nakedret:
      max-func-lines: 10

    dupl:
      threshold: 200

    gocritic:
      enabled-tags:
        - diagnostic
        - style
        - performance
      disabled-checks:
        - hugeParam
        - rangeValCopy
        - commentedOutCode

    revive:
      severity: warning
      rules:
        - name: blank-imports
        - name: context-as-argument
        - name: context-keys-type
        - name: dot-imports
        - name: error-return
        - name: error-strings
        - name: error-naming
        - name: exported
          arguments:
            - checkPrivateReceivers
            - sayRepetitiveInsteadOfStutters
        - name: if-return
        - name: increment-decrement
        - name: var-naming
        - name: var-declaration
        - name: package-comments
        - name: range
        - name: receiver-naming
        - name: time-naming
        - name: unexported-return
        - name: indent-error-flow
        - name: errorf
        - name: empty-block
        - name: superfluous-else
        - name: unreachable-code
        - name: redefines-builtin-id
        - name: get-return
        - name: string-of-int
        - name: early-return
        - name: unnecessary-stmt

    prealloc:
      simple: true
      range-loops: true
      for-loops: true

  exclusions:
    generated: lax
    presets:
      - std-error-handling
    rules:
      - path: _test\.go
        linters:
          - funlen
          - gocyclo
          - gocognit
          - gochecknoglobals
          - wrapcheck
          - noctx
          - unparam
          - dupl
          - prealloc
          - gocritic

      - path: main\.go
        linters:
          - gochecknoinits
          - gochecknoglobals

formatters:
  enable:
    - goimports
  settings:
    gofumpt:
      extra-rules: true
    goimports:
      local-prefixes:
        - oro
`

// pyprojectToolSectionsTemplate contains [tool.*] sections for Python projects.
// These are appended to an existing pyproject.toml or used standalone.
const pyprojectToolSectionsTemplate = `[tool.ruff]
target-version = "py311"
line-length = 120

[tool.ruff.lint]
select = ["E", "F", "W", "I", "N", "UP", "B", "A", "SIM", "RUF"]

[tool.pyright]
pythonVersion = "3.11"
venvPath = "."
venv = ".venv"

[tool.pylint.main]

[tool.pytest.ini_options]
testpaths = ["tests"]
`

// generatePyprojectToolSections returns pyproject.toml tool sections for Python projects.
// Returns empty string if cfg is nil or does not include a "python" language entry.
// The error return is reserved for future template-based generation.
func generatePyprojectToolSections(cfg *langprofile.Config) (string, error) { //nolint:unparam // error reserved for future use
	if cfg == nil {
		return "", nil
	}
	if _, ok := cfg.Languages["python"]; !ok {
		return "", nil
	}
	return pyprojectToolSectionsTemplate, nil
}

// generateGolangciLint returns a .golangci.yml configuration string for Go projects.
// Returns empty string if cfg is nil or does not include a "go" language entry.
// The error return is reserved for future template-based generation.
func generateGolangciLint(cfg *langprofile.Config) (string, error) { //nolint:unparam // error reserved for future use
	if cfg == nil {
		return "", nil
	}
	if _, ok := cfg.Languages["go"]; !ok {
		return "", nil
	}
	return golangciLintTemplate, nil
}

// qualityGateData holds the template variables for quality gate generation.
type qualityGateData struct {
	HasGo     bool
	HasPython bool
}

// generateQualityGateScript produces a quality_gate.sh script tailored to the
// languages detected in cfg. Returns an error if cfg is nil or has no languages.
func generateQualityGateScript(cfg *langprofile.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is nil")
	}
	if len(cfg.Languages) == 0 {
		return "", fmt.Errorf("no languages detected in config")
	}

	_, hasGo := cfg.Languages["go"]
	_, hasPython := cfg.Languages["python"]

	data := qualityGateData{
		HasGo:     hasGo,
		HasPython: hasPython,
	}

	tmpl, err := template.New("quality_gate").Parse(qualityGateTmpl)
	if err != nil {
		return "", fmt.Errorf("parse quality gate template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render quality gate template: %w", err)
	}

	return buf.String(), nil
}

// qualityGateTmpl is the bash template for the generated quality_gate.sh.
// {{if .HasGo}} and {{if .HasPython}} sections are included only when the
// respective language is detected in the project config.
const qualityGateTmpl = `#!/usr/bin/env bash
# =============================================================================
# Oro Quality Gate — generated by oro init
#
# Architecture: independent lanes run in parallel. Within each lane, checks
# run in tiers — independent checks within a tier run in parallel, and the
# lane bails on first tier failure.
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

# Resolve repo root node_modules (works from worktrees too).
REPO_ROOT="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"
NODE_BIN="$REPO_ROOT/node_modules/.bin"

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
{{if .HasGo}}
# =============================================================================
# LANE: GO
# =============================================================================

# shellcheck disable=SC2317
lane_go() {
    local pass=0 fail=0

    local GO_DIRS="cmd internal pkg"
    make stage-assets 2>/dev/null || true

    # --- Tier 1: Formatting (parallel) ---
    header "GO TIER 1: FORMATTING"
    parallel_checks \
        "gofumpt" "test -z \"\$(go tool gofumpt -l $GO_DIRS 2>/dev/null)\"" \
        "goimports" "test -z \"\$(go tool goimports -l $GO_DIRS 2>/dev/null)\""
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; make clean-assets 2>/dev/null || true; return; fi

    # --- Tier 2: Lint (parallel) ---
    header "GO TIER 2: LINT"
    parallel_checks \
        "golangci-lint" "GOFLAGS=-buildvcs=false golangci-lint run --timeout 5m ./cmd/... ./internal/... ./pkg/..."
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; make clean-assets 2>/dev/null || true; return; fi

    # --- Tier 3: Test + Build (parallel) ---
    header "GO TIER 3: TEST + BUILD"

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

    parallel_checks \
        "go test + coverage" "go_test_with_coverage" \
        "go build" "go build -buildvcs=false ./..." \
        "go vet" "go vet ./..."
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))

    make clean-assets 2>/dev/null || true
    echo "${pass}:${fail}" > "$QG_DIR/go.rc"
}
{{end}}{{if .HasPython}}
# =============================================================================
# LANE: PYTHON
# =============================================================================

# shellcheck disable=SC2317
lane_python() {
    local pass=0 fail=0

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
    parallel_checks "ruff check" "ruff check ."
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

    echo "${pass}:${fail}" > "$QG_DIR/python.rc"
}
{{end}}
# =============================================================================
# LANE: SHELL + DOCS (lightweight, combined into one lane)
# =============================================================================

# shellcheck disable=SC2317
lane_other() {
    local pass=0 fail=0

    local has_shell=false
    if compgen -G "*.sh" > /dev/null || compgen -G "ad_hoc/*.sh" > /dev/null; then has_shell=true; fi

    if $has_shell; then
        header "SHELL: LINT"
        if check "shellcheck" "find . -name '*.sh' -not -path './references/*' -not -path './archive/*' -not -path './.worktrees/*' -exec shellcheck --severity=info {} +"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    fi

    header "DOCS & CONFIG"
    local BIOME_PATHS=""
    for p in docs/ .github/ .beads/; do
        [ -d "$p" ] && BIOME_PATHS="$BIOME_PATHS $p"
    done
    if compgen -G "*.json" > /dev/null 2>&1; then
        BIOME_PATHS="$BIOME_PATHS *.json"
    fi

    local docs_checks=(
        "markdownlint" "$NODE_BIN/markdownlint-cli2 --config .markdownlint.yml 'docs/**/*.md' '*.md' '!references/**' '!archive/**'"
        "yamllint" "find . \( -name '*.yml' -o -name '*.yaml' \) -not -path './references/*' -not -path './archive/*' -not -path './.worktrees/*' -not -path './node_modules/*' | xargs yamllint -d relaxed --no-warnings"
    )
    if [ -n "$BIOME_PATHS" ]; then
        # shellcheck disable=SC2086
        docs_checks+=("biome (json)" "$NODE_BIN/biome check --files-ignore-unknown=true $BIOME_PATHS")
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
{{if .HasGo}}echo "  Detected: Go project"
{{end}}{{if .HasPython}}echo "  Detected: Python project"
{{end}}echo ""

{{if .HasGo}}lane_go > "$QG_DIR/go.out" 2>&1 &
PID_GO=$!
{{end}}{{if .HasPython}}lane_python > "$QG_DIR/python.out" 2>&1 &
PID_PY=$!
{{end}}lane_other > "$QG_DIR/other.out" 2>&1 &
PID_OT=$!

{{if .HasGo}}wait "$PID_GO" 2>/dev/null || true
{{end}}{{if .HasPython}}wait "$PID_PY" 2>/dev/null || true
{{end}}wait "$PID_OT" 2>/dev/null || true

{{if .HasGo}}cat "$QG_DIR/go.out" 2>/dev/null || true
{{end}}cat "$QG_DIR/other.out" 2>/dev/null || true
{{if .HasPython}}cat "$QG_DIR/python.out" 2>/dev/null || true
{{end}}
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
`
