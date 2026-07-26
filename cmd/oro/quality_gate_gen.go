package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

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
	HasGo      bool
	HasPython  bool
	OroDocsDir string // biome scan path; defaults to "docs/"
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
		HasGo:      hasGo,
		HasPython:  hasPython,
		OroDocsDir: "docs/",
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

// readQGConfig reads a langprofile.Config from configPath.
// Returns nil (no error) if configPath is empty or the file does not exist.
func readQGConfig(configPath string) (*langprofile.Config, error) {
	if configPath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(configPath) //nolint:gosec // configPath is trusted caller input
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", configPath, err)
	}
	var cfg langprofile.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", configPath, err)
	}
	return &cfg, nil
}

// writeQualityGateScript renders quality_gate.sh to w using paths for dynamic
// substitution in biome scan paths.
// Language detection reads paths.ConfigYAML; falls back to shell-only when absent.
// An empty docs path falls back to docs/.
func writeQualityGateScript(w io.Writer, paths ProjectPaths) error {
	cfg, err := readQGConfig(paths.ConfigYAML)
	if err != nil {
		return err
	}

	oroDocsDir := paths.OroDocsDir
	if oroDocsDir == "" {
		oroDocsDir = "docs/"
	}

	// Convert absolute docs paths to CWD-relative for repository-local scans.
	if paths.RepoRoot != "" {
		oroDocsDir = cwdRelative(paths.RepoRoot, oroDocsDir)
	}

	var hasGo, hasPython bool
	if cfg != nil {
		_, hasGo = cfg.Languages["go"]
		_, hasPython = cfg.Languages["python"]
	}

	data := qualityGateData{
		HasGo:      hasGo,
		HasPython:  hasPython,
		OroDocsDir: oroDocsDir,
	}

	tmpl, err := template.New("quality_gate").Parse(qualityGateTmpl)
	if err != nil {
		return fmt.Errorf("parse quality gate template: %w", err)
	}

	if err := tmpl.Execute(w, data); err != nil {
		return fmt.Errorf("render quality gate template: %w", err)
	}

	return nil
}

// cwdRelative converts an absolute dir to a "./" prefixed path relative to root.
// If dir is already relative, outside the repo tree (starts with ".."), or Rel
// fails, it is returned unchanged. This preserves stealth-mode absolute paths
// (outside the repo) while fixing standard-mode paths for find exclusions.
func cwdRelative(root, dir string) string {
	if !filepath.IsAbs(dir) {
		return dir
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return dir
	}
	return "./" + rel
}

// writeQualityGateScriptFile generates quality_gate.sh at paths.QualityGate.
// Skips if the file already exists (unless force is true). Uses atomic write
// (tmp + rename) for safety.
func writeQualityGateScriptFile(paths ProjectPaths, force bool) error {
	if !force && fileExists(paths.QualityGate) {
		return nil
	}

	// Skip generation only when config.yaml is absent (cfg == nil).
	// A config with zero languages still gets a shell-only script.
	cfg, err := readQGConfig(paths.ConfigYAML)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(paths.QualityGate), 0o750); err != nil {
		return fmt.Errorf("creating scripts/ dir: %w", err)
	}
	tmp := paths.QualityGate + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) //nolint:gosec // quality gate script must be executable
	if err != nil {
		return fmt.Errorf("open temp quality gate: %w", err)
	}
	if writeErr := writeQualityGateScript(f, paths); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(tmp) //nolint:gosec // tmp is our own temp file
		return writeErr
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp) //nolint:gosec // tmp is our own temp file
		return fmt.Errorf("close quality gate: %w", err)
	}
	if err := os.Rename(tmp, paths.QualityGate); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck,gosec // best-effort cleanup
		return fmt.Errorf("rename quality gate: %w", err)
	}
	return nil
}

// qualityGateTmpl is the bash template for the generated quality_gate.sh.
// {{if .HasGo}} and {{if .HasPython}} sections are included only when the
// respective language is detected in the project config.
const qualityGateTmpl = `#!/bin/sh
# shellcheck shell=bash
# =============================================================================
# Oro Quality Gate — generated by oro init
#
# Architecture: independent lanes run in parallel. Within each lane, checks
# run in tiers — independent checks within a tier run in parallel, and the
# lane bails on first tier failure.
# =============================================================================

if [ "${ORO_QG_BASH_BOOTSTRAPPED_PID:-}" != "${BASHPID:-$$}" ]; then
    if grep -n -E '^(<{7}|={7}|>{7})( |$)' "$0" >/dev/null 2>&1; then
        echo "FAIL: quality_gate.sh contains unresolved git conflict markers" >&2
        grep -n -E '^(<{7}|={7}|>{7})( |$)' "$0" >&2 || true
        exit 2
    fi
    if [ -n "${LC_ALL:-}" ] && ! locale -a 2>/dev/null | grep -qx "$LC_ALL"; then
        export LC_ALL=C
        export LANG=C
    fi
    # The legacy marker can be inherited by a fresh /bin/sh launcher. A PID-scoped
    # token survives this one exec but cannot suppress bootstrap in a child launch.
    # Keep the preflight before Bash parses the script; ignore BASH_ENV because a
    # shell hook can recursively launch this gate.
    unset ORO_QG_BASH_BOOTSTRAPPED
    export ORO_QG_BASH_BOOTSTRAPPED_PID=${BASHPID:-$$}
    qg_bash=""
    for candidate in /opt/homebrew/bin/bash /usr/local/bin/bash "$(command -v bash 2>/dev/null || true)"; do
        # shellcheck disable=SC2016 # The candidate Bash, not this sh bootstrap, expands BASH_VERSINFO.
        if [ -x "$candidate" ] && env -u BASH_ENV "$candidate" -c '[ "${BASH_VERSINFO[0]:-0}" -ge 4 ]' >/dev/null 2>&1; then
            qg_bash="$candidate"
            break
        fi
    done
    if [ -z "$qg_bash" ]; then
        echo "FAIL: quality_gate.sh requires Bash 4 or newer; install it (for example, Homebrew bash) or add it to PATH." >&2
        exit 2
    fi
    exec env -u BASH_ENV "$qg_bash" "$0" "$@"
fi
unset ORO_QG_BASH_BOOTSTRAPPED
unset ORO_QG_BASH_BOOTSTRAPPED_PID

set -euo pipefail

# A child process can invoke this script while the parent is still running a
# parallel lint/check lane. The run lock is intentionally acquired later for
# the serial timing lane, so it cannot prevent that recursive launch. Let the
# parent own the terminal PASS/FAIL summary and stop the child before it starts
# a second set of lanes.
if [ -n "${ORO_QG_ACTIVE_PID:-}" ] && [ "$ORO_QG_ACTIVE_PID" != "${BASHPID:-$$}" ] && kill -0 "$ORO_QG_ACTIVE_PID" 2>/dev/null; then
    echo "Nested quality gate invocation detected; using active parent result."
    exit 0
fi
export ORO_QG_ACTIVE_PID=${BASHPID:-$$}

QG_MUTATION_TESTING=false

while [ "$#" -gt 0 ]; do
    case "$1" in
        --mutation-testing)
            QG_MUTATION_TESTING=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--mutation-testing]"
            exit 0
            ;;
        *)
            echo "Unknown quality gate argument: $1" >&2
            exit 2
            ;;
    esac
done

# Unset git hook env vars that leak into test subprocesses.
# Save worktree state first so mutation testing can still resolve refs after
# hook env cleanup.
QG_IS_WORKTREE=false
if [ -f .git ]; then
    QG_IS_WORKTREE=true
    QG_GIT_DIR="$(git rev-parse --git-dir)"
fi
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
QG_STAGE_ASSETS_LOCK=""
QG_RUN_LOCK=""
QG_RUN_LOCK_TOKEN=""
QG_RUN_QUEUE_TICKET=""
QG_EXIT_STATUS=0
# shellcheck disable=SC2329
cleanup_qg() {
    local status=$?
    if [ -n "${QG_RUN_QUEUE_TICKET:-}" ]; then
        rm -f "$QG_RUN_QUEUE_TICKET/owner" 2>/dev/null || true
        rmdir "$QG_RUN_QUEUE_TICKET" 2>/dev/null || true
        rmdir "$(dirname "$QG_RUN_QUEUE_TICKET")" 2>/dev/null || true
    fi
    if [ -n "${QG_STAGE_ASSETS_LOCK:-}" ]; then
        rmdir "$QG_STAGE_ASSETS_LOCK" 2>/dev/null || true
    fi
    if [ -n "${QG_RUN_LOCK:-}" ]; then
        if [ -n "${QG_RUN_LOCK_TOKEN:-}" ] && [ -f "$QG_RUN_LOCK/owner" ]; then
            if grep -qx "token=$QG_RUN_LOCK_TOKEN" "$QG_RUN_LOCK/owner" 2>/dev/null; then
                rm -f "$QG_RUN_LOCK/owner" 2>/dev/null || true
                rmdir "$QG_RUN_LOCK" 2>/dev/null || true
            fi
        else
            rmdir "$QG_RUN_LOCK" 2>/dev/null || true
        fi
    fi
    rm -rf "$QG_DIR"
    return "$status"
}
trap cleanup_qg EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Tool caches deliberately inherit their environment (or each tool's standard
# external default). Only QG scratch data belongs under TMPDIR/QG_DIR.
export GOMAXPROCS="${ORO_QG_GOMAXPROCS:-2}"

# Resolve repo root node_modules (works from worktrees too). Non-git harness
# tests copy this script into temporary projects, so fall back to the current
# project directory when no git common dir exists.
if QG_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null)"; then
    REPO_ROOT="$(cd "$QG_COMMON_DIR/.." && pwd)"
else
    REPO_ROOT="$PWD"
fi
NODE_BIN="$REPO_ROOT/node_modules/.bin"

# Serialize full quality gates across sibling worktrees. The gate already runs
# internal lanes in parallel; concurrent worker gates overload dispatcher socket
# timing tests and create non-actionable QG incidents.
quality_gate_lock_age_seconds() {
    local lock_dir="$1"
    local now mtime
    now=$(date +%s)
    if mtime=$(stat -f %m "$lock_dir" 2>/dev/null); then
        :
    elif mtime=$(stat -c %Y "$lock_dir" 2>/dev/null); then
        :
    else
        return 1
    fi
    if [ "$now" -ge "$mtime" ]; then
        printf '%s\n' "$((now - mtime))"
    else
        printf '0\n'
    fi
}

quality_gate_process_start_time() {
    LC_ALL=C TZ=UTC ps -o lstart= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

quality_gate_lock_owner_matches_process() {
    local owner="$1"
    local pid="$2"
    local recorded_start_time actual_start_time
    recorded_start_time=$(sed -n 's/^start_time=//p' "$owner" | head -1)
    if [ -z "$recorded_start_time" ]; then
        return 2
    fi
    actual_start_time=$(quality_gate_process_start_time "$pid")
    if [ -z "$actual_start_time" ]; then
        return 2
    fi
    if [ "$recorded_start_time" = "$actual_start_time" ]; then
        return 0
    fi
    return 1
}

quality_gate_process_has_descendants() {
    pgrep -P "$1" >/dev/null 2>&1
    case $? in
    0) return 0 ;;
    1) return 1 ;;
    *) return 2 ;;
    esac
}

quality_gate_lock_stale() {
    local lock_dir="$1"
    local owner="$lock_dir/owner"
    local pid parent_pid age stale_after owner_match_status
    if [ -f "$owner" ]; then
        pid=$(sed -n 's/^pid=//p' "$owner" | head -1)
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            quality_gate_lock_owner_matches_process "$owner" "$pid"
            owner_match_status=$?
            if [ "$owner_match_status" -ne 0 ]; then
                if [ "$owner_match_status" -eq 1 ]; then
                    return 0
                fi
                return 1
            fi
            parent_pid=$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d '[:space:]')
            if [ "$parent_pid" != "1" ]; then
                return 1
            fi
            stale_after="${ORO_QG_STALE_LOCK_SECONDS:-600}"
            if ! age=$(quality_gate_lock_age_seconds "$lock_dir"); then
                return 1
            fi
            if [ "$age" -lt "$stale_after" ]; then
                return 1
            fi
            quality_gate_process_has_descendants "$pid"
            case $? in
            1) return 0 ;;
            *) return 1 ;;
            esac
        fi
        return 0
    fi

    stale_after="${ORO_QG_STALE_LOCK_SECONDS:-600}"
    if ! age=$(quality_gate_lock_age_seconds "$lock_dir"); then
        return 1
    fi
    [ "$age" -ge "$stale_after" ]
}

archive_stale_quality_gate_lock() {
    local lock_dir="$1"
    local stale_dir
    stale_dir="${lock_dir}.stale.$(date +%s).$$"
    if mv "$lock_dir" "$stale_dir" 2>/dev/null; then
        echo "WARNING: archived stale quality gate lock: $stale_dir" >&2
        return 0
    fi
    return 1
}

cleanup_archived_stale_quality_gate_locks() {
    local lock_dir="$1"
    local stale_dir age stale_after
    stale_after="${ORO_QG_STALE_LOCK_SECONDS:-600}"
    for stale_dir in "${lock_dir}.stale."*; do
        [ -d "$stale_dir" ] || continue
        if ! age=$(quality_gate_lock_age_seconds "$stale_dir"); then
            continue
        fi
        if [ "$age" -ge "$stale_after" ]; then
            rm -rf "$stale_dir"
        fi
    done
}

write_quality_gate_lock_owner() {
    local lock_dir="$1"
    local token="$2"
    local start_time
    start_time=$(quality_gate_process_start_time "$$")
    {
        echo "pid=$$"
        echo "start_time=$start_time"
        echo "token=$token"
        echo "repo=$REPO_ROOT"
        echo "created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    } >"$lock_dir/owner"
}

quality_gate_lock_poll_seconds() {
    printf '%s\n' "${ORO_QG_LOCK_POLL_SECONDS:-2}"
}

quality_gate_lock_timeout_reached() {
    local waited="$1"
    [ -n "${ORO_QG_LOCK_TIMEOUT_SECONDS:-}" ] && [ "$waited" -ge "$ORO_QG_LOCK_TIMEOUT_SECONDS" ]
}

create_quality_gate_queue_ticket() {
    local queue_dir="$1"
    local ticket_dir
    mkdir -p "$queue_dir"
    while :; do
        ticket_dir="$queue_dir/$(date +%s)-$$-$RANDOM"
        if mkdir "$ticket_dir" 2>/dev/null; then
            {
                echo "pid=$$"
                echo "created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
            } >"$ticket_dir/owner"
            QG_RUN_QUEUE_TICKET="$ticket_dir"
            return 0
        fi
    done
}

quality_gate_queue_ticket_stale() {
    local ticket_dir="$1"
    local owner="$ticket_dir/owner"
    local pid
    if [ ! -f "$owner" ]; then
        return 0
    fi
    pid=$(sed -n 's/^pid=//p' "$owner" | head -1)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        return 1
    fi
    return 0
}

cleanup_stale_quality_gate_queue_tickets() {
    local queue_dir="$1"
    local ticket
    for ticket in "$queue_dir"/*; do
        [ -d "$ticket" ] || continue
        if quality_gate_queue_ticket_stale "$ticket"; then
            rm -f "$ticket/owner" 2>/dev/null || true
            rmdir "$ticket" 2>/dev/null || true
        fi
    done
}

first_quality_gate_queue_ticket() {
    local queue_dir="$1"
    find "$queue_dir" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; 2>/dev/null | LC_ALL=C sort | head -1
}

quality_gate_lock_is_inherited() {
    local lock_dir="$1"
    [ "${ORO_QG_INHERITED_LOCK_DIR:-}" = "$lock_dir" ] &&
        [ -n "${ORO_QG_INHERITED_LOCK_TOKEN:-}" ] &&
        [ -f "$lock_dir/owner" ] &&
        grep -qx "token=$ORO_QG_INHERITED_LOCK_TOKEN" "$lock_dir/owner" 2>/dev/null
}

# OSC-8 terminal hyperlinks can create escaped, top-level artifact directories
# when pasted into shells. Remove only real directories at the repository root;
# do not follow symlinks or inspect paths below that boundary.
sweep_repo_root_escape_artifacts() {
    local repo_root="$1"
    local artifact
    for artifact in "$repo_root"/$'\033]8;;file:'*; do
        [ -d "$artifact" ] && [ ! -L "$artifact" ] || continue
        rm -rf -- "$artifact"
    done
}

sweep_repo_root_escape_artifacts "$REPO_ROOT"

acquire_quality_gate_lock() {
    local lock_dir="$REPO_ROOT/.oro-quality-gate.lock"
    local queue_dir="$REPO_ROOT/.oro-quality-gate.queue"
    local ticket_name poll_seconds reported_waiting
    local waited=0
    reported_waiting=false
    if quality_gate_lock_is_inherited "$lock_dir"; then
        exit 0
    fi
    cleanup_archived_stale_quality_gate_locks "$lock_dir"
    create_quality_gate_queue_ticket "$queue_dir"
    while :; do
        cleanup_stale_quality_gate_queue_tickets "$queue_dir"
        ticket_name=$(basename "$QG_RUN_QUEUE_TICKET")
        if [ "$(first_quality_gate_queue_ticket "$queue_dir")" != "$ticket_name" ]; then
            if [ "$reported_waiting" = false ]; then
                echo "Waiting for another quality gate to finish..."
                reported_waiting=true
            fi
            poll_seconds=$(quality_gate_lock_poll_seconds)
            sleep "$poll_seconds"
            waited=$((waited + poll_seconds))
            if quality_gate_lock_timeout_reached "$waited"; then
                echo "FAIL: timed out waiting for quality gate FIFO queue: $queue_dir" >&2
                return 1
            fi
            continue
        fi
        if mkdir "$lock_dir" 2>/dev/null; then
            break
        fi
        if quality_gate_lock_stale "$lock_dir"; then
            archive_stale_quality_gate_lock "$lock_dir" || true
            continue
        fi
        if [ "$reported_waiting" = false ]; then
            echo "Waiting for another quality gate to finish..."
            reported_waiting=true
        fi
        poll_seconds=$(quality_gate_lock_poll_seconds)
        sleep "$poll_seconds"
        waited=$((waited + poll_seconds))
        if quality_gate_lock_timeout_reached "$waited"; then
            echo "FAIL: timed out waiting for quality gate lock: $lock_dir" >&2
            return 1
        fi
    done
    QG_RUN_LOCK="$lock_dir"
    QG_RUN_LOCK_TOKEN="$$-$(date +%s)-$RANDOM"
    write_quality_gate_lock_owner "$lock_dir" "$QG_RUN_LOCK_TOKEN"
    export ORO_QG_INHERITED_LOCK_DIR="$lock_dir"
    export ORO_QG_INHERITED_LOCK_TOKEN="$QG_RUN_LOCK_TOKEN"
    rm -f "$QG_RUN_QUEUE_TICKET/owner" 2>/dev/null || true
    rmdir "$QG_RUN_QUEUE_TICKET" 2>/dev/null || true
    rmdir "$queue_dir" 2>/dev/null || true
    QG_RUN_QUEUE_TICKET=""
}

# check_inherited_quality_gate_lock short-circuits a gate invoked inside another
# gate's serialized lane. The concurrent main phase itself remains lockless.
check_inherited_quality_gate_lock() {
    if quality_gate_lock_is_inherited "$REPO_ROOT/.oro-quality-gate.lock"; then
        exit 0
    fi
}

neutralize_serial_lane_env() {
    unset ORO_QG_SERIAL_LANE
}

# Generated gates have no Oro-specific guarded tests, but retain the same
# lane-local lock boundary as the checked-in gate so recursive invocations and
# sibling worktrees share one lock contract without serializing the main phase.
run_serial_lane() {
    if ! acquire_quality_gate_lock; then
        echo "0:1" >"$QG_DIR/serial.rc" 2>/dev/null || true
        return 1
    fi
    export ORO_QG_SERIAL_LANE=1
    echo "1:0" >"$QG_DIR/serial.rc"
}

check_inherited_quality_gate_lock
neutralize_serial_lane_env

# =============================================================================
# PRIMITIVES
# =============================================================================

# Run git with ref-resolution support in worktrees.
# shellcheck disable=SC2317,SC2329
qg_git() {
    if $QG_IS_WORKTREE; then
        GIT_DIR="$QG_GIT_DIR" git "$@"
    else
        git "$@"
    fi
}

# shellcheck disable=SC2317,SC2329
qg_source_files() {
    qg_git ls-files -- "$@" \
        ':(exclude)references/**' \
        ':(exclude)archive/**' \
        ':(exclude).tmp-test/**' \
        ':(exclude).cache/**' \
        ':(exclude).worktrees/**' \
        ':(exclude).claude/worktrees/**' \
        ':(exclude).venv/**' \
        ':(exclude)node_modules/**' \
        ':(exclude)cmd/oro/_assets/**'
}

# shellcheck disable=SC2317,SC2329
qg_python_source_files() {
    qg_source_files '*.py' \
        ':(exclude)assets/**' \
        ':(exclude).claude/hooks/**'
}

# shellcheck disable=SC2317,SC2329
qg_yaml_source_files() {
    qg_source_files '*.yml' '*.yaml'
}

# shellcheck disable=SC2317,SC2329
qg_shell_source_files() {
    qg_source_files '*.sh'
}

# shellcheck disable=SC2317,SC2329
qg_run_shellcheck_source() {
    local -a files=()
    mapfile -t files < <(qg_shell_source_files)
    if [ "${#files[@]}" -eq 0 ]; then
        echo "No tracked shell source files"
        return 0
    fi
    shellcheck --severity=info "${files[@]}"
}

# shellcheck disable=SC2317,SC2329
qg_run_ruff_format_source() {
    local -a files=()
    mapfile -t files < <(qg_python_source_files)
    if [ "${#files[@]}" -eq 0 ]; then
        echo "No tracked Python source files"
        return 0
    fi
    qg_ruff format --check "${files[@]}"
}

# shellcheck disable=SC2317,SC2329
qg_run_ruff_check_source() {
    local -a files=()
    mapfile -t files < <(qg_python_source_files)
    if [ "${#files[@]}" -eq 0 ]; then
        echo "No tracked Python source files"
        return 0
    fi
    qg_ruff check "${files[@]}"
}

# shellcheck disable=SC2317,SC2329
qg_run_pylint_source() {
    local -a files=()
    mapfile -t files < <(qg_python_source_files)
    if [ "${#files[@]}" -eq 0 ]; then
        echo "No tracked Python source files"
        return 0
    fi
    qg_run_python_tool pylint --disable=all --enable=E "${files[@]}"
}

# shellcheck disable=SC2317,SC2329
qg_run_pyright_source() {
    local -a files=()
    mapfile -t files < <(qg_python_source_files)
    if [ "${#files[@]}" -eq 0 ]; then
        echo "No tracked Python source files"
        return 0
    fi
    qg_pyright "${files[@]}"
}

# shellcheck disable=SC2317,SC2329
qg_run_yamllint_source() {
    local -a files=()
    mapfile -t files < <(qg_yaml_source_files)
    if [ "${#files[@]}" -eq 0 ]; then
        echo "No tracked YAML source files"
        return 0
    fi
    yamllint -d relaxed --no-warnings "${files[@]}"
}

# shellcheck disable=SC2317,SC2329
should_run_mutation_tests() {
    if [ "${ORO_SKIP_MUTATION:-}" = "1" ]; then
        return 1
    fi
    if [ "${QG_MUTATION_TESTING:-false}" = "true" ]; then
        return 0
    fi
    return 1
}

# shellcheck disable=SC2317,SC2329
mutation_skip_reason() {
    if [ "${ORO_SKIP_MUTATION:-}" = "1" ]; then
        printf 'ORO_SKIP_MUTATION=1'
    else
        printf 'mutation disabled by default; use --mutation-testing'
    fi
}

# shellcheck disable=SC2317,SC2329
mutation_base_ref() {
    if [ -n "${ORO_MUTATION_BASE:-}" ]; then
        printf '%s\n' "$ORO_MUTATION_BASE"
        return 0
    fi
    local branch
    branch=$(qg_git branch --show-current 2>/dev/null || true)
    if should_run_mutation_tests && [ "$branch" = "main" ] && qg_git rev-parse --verify origin/main >/dev/null 2>&1; then
        printf 'origin/main\n'
        return 0
    fi
    printf 'main\n'
}

# shellcheck disable=SC2317,SC2329
should_enforce_go_coverage_threshold() {
    local coverage_base changed
    coverage_base=$(mutation_base_ref)
    if ! qg_git rev-parse --verify "$coverage_base" >/dev/null 2>&1; then
        echo "WARNING: Cannot find coverage base $coverage_base — enforcing 78% Go coverage threshold"
        return 0
    fi
    changed=$(qg_git diff --name-only "$coverage_base" -- internal/ pkg/ 2>/dev/null |
        grep '\.go$' |
        grep -v '_test\.go$' ||
        true)
    if [ -z "$changed" ]; then
        echo "Skipping 78% Go coverage threshold: changed files are outside measured ./internal and ./pkg production surface"
        return 1
    fi
    return 0
}

# shellcheck disable=SC2317,SC2329
restore_go_mutation_worktree() {
    local unstaged_patch="$1"
    local -a restore_paths=()
    local path
    for path in pkg internal cmd; do
        if git ls-files -- "$path/" | grep -q .; then
            restore_paths+=("$path/")
        fi
    done
    if [ "${#restore_paths[@]}" -gt 0 ]; then
        git checkout -- "${restore_paths[@]}" 2>/dev/null || true
    fi
    if [ -s "$unstaged_patch" ]; then
        git apply --3way --whitespace=nowarn "$unstaged_patch"
    fi
}

# shellcheck disable=SC2317,SC2329
go_mutation_hooks_dir() {
    local common_dir
    common_dir=$(git rev-parse --git-common-dir 2>/dev/null || true)
    if [ -z "$common_dir" ]; then
        return 1
    fi
    printf '%s/hooks\n' "$common_dir"
}

# shellcheck disable=SC2317,SC2329
snapshot_go_mutation_side_effects() {
    local snapshot_dir="$1"
    local hooks_dir
    mkdir -p "$snapshot_dir"
    if hooks_dir=$(go_mutation_hooks_dir); then
        # Protect the local .git/hooks/pre-push hook from mutation-test side effects.
        mkdir -p "$snapshot_dir/git-hooks"
        if [ -L "$hooks_dir/pre-push" ]; then
            readlink "$hooks_dir/pre-push" > "$snapshot_dir/git-hooks/pre-push.target"
            printf 'symlink\n' > "$snapshot_dir/git-hooks/pre-push.state"
        elif [ -f "$hooks_dir/pre-push" ]; then
            cp -p "$hooks_dir/pre-push" "$snapshot_dir/git-hooks/pre-push"
            printf 'file\n' > "$snapshot_dir/git-hooks/pre-push.state"
        else
            printf 'missing\n' > "$snapshot_dir/git-hooks/pre-push.state"
        fi
    fi
    find cmd internal pkg -type f -name '*.go.tmp' -print > "$snapshot_dir/go-tmp.before" 2>/dev/null || true
}

# shellcheck disable=SC2317,SC2329
restore_go_mutation_side_effects() {
    local snapshot_dir="$1"
    local hooks_dir state tmp_file
    if [ -f "$snapshot_dir/git-hooks/pre-push.state" ] && hooks_dir=$(go_mutation_hooks_dir); then
        state=$(cat "$snapshot_dir/git-hooks/pre-push.state")
        if [ "$state" = "file" ]; then
            mkdir -p "$hooks_dir"
            rm -f "$hooks_dir/pre-push"
            cp -p "$snapshot_dir/git-hooks/pre-push" "$hooks_dir/pre-push"
        elif [ "$state" = "symlink" ]; then
            mkdir -p "$hooks_dir"
            rm -f "$hooks_dir/pre-push"
            ln -s "$(cat "$snapshot_dir/git-hooks/pre-push.target")" "$hooks_dir/pre-push"
        elif [ "$state" = "missing" ]; then
            rm -f "$hooks_dir/pre-push"
        fi
    fi
    while IFS= read -r tmp_file; do
        if [ -n "$tmp_file" ] && ! grep -Fxq -- "$tmp_file" "$snapshot_dir/go-tmp.before"; then
            rm -f "$tmp_file"
        fi
    done < <(find cmd internal pkg -type f -name '*.go.tmp' -print 2>/dev/null || true)
}

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

qg_python_tool_path() {
    local tool="$1"
    local candidate
    for candidate in ".venv/bin/$tool" "$REPO_ROOT/.venv/bin/$tool"; do
        if [ -x "$candidate" ]; then
            if ! qg_python_tool_path_allowed "$candidate"; then
                continue
            fi
            printf '%s\n' "$candidate"
            return 0
        fi
    done

    candidate=$(command -v "$tool" 2>/dev/null || true)
    if [ -z "$candidate" ]; then
        return 1
    fi
    qg_python_tool_path_allowed "$candidate" || return 1
    printf '%s\n' "$candidate"
}

qg_python_tool_path_allowed() {
    local candidate="$1"
    local resolved="$candidate"
    if command -v realpath >/dev/null 2>&1; then
        resolved=$(realpath "$candidate" 2>/dev/null || printf '%s\n' "$candidate")
    elif [ -L "$candidate" ]; then
        return 1
    fi
    local path_check
    for path_check in "$candidate" "$resolved"; do
        case "$path_check" in
        */.pyenv/shims/* | */pyenv/shims/* | */libexec/pyenv*)
            return 1
            ;;
        esac
    done
}

# shellcheck disable=SC2329 # invoked indirectly via check/parallel_checks command strings
qg_run_python_tool() {
    local tool="$1"
    shift
    # pylint must analyze code inside the project's dependency environment so
    # first-party imports (e.g. files that import pytest) resolve; a global
    # install emits a false import-error (E0401). Prefer uv run for it; --with
    # provides pylint itself when it is not a project dependency.
    if [ "$tool" = "pylint" ] && command -v uv >/dev/null 2>&1; then
        uv run --with pylint pylint "$@"
        return
    fi
    local path
    if path=$(qg_python_tool_path "$tool"); then
        "$path" "$@"
        return
    fi
    if command -v uv >/dev/null 2>&1; then
        uv run "$tool" "$@"
        return
    fi
    echo "SKIP: $tool not installed"
    return 77
}

# shellcheck disable=SC2329 # invoked indirectly via check/parallel_checks command strings
qg_ruff() {
    qg_run_python_tool ruff "$@"
}

qg_pyright() {
    local path active_venv
    if path=$(qg_python_tool_path pyright); then
        active_venv="$REPO_ROOT/.venv"
        if [ -x "$active_venv/bin/python" ]; then
            VIRTUAL_ENV="$active_venv" PATH="$active_venv/bin:$PATH" "$path" "$@"
            return
        fi
        (
            unset VIRTUAL_ENV
            "$path" "$@"
        )
        return
    fi
    echo "SKIP: pyright not installed"
    return 77
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

# Ensure go:embed assets exist without deleting them during another QG run.
# The Makefile target rebuilds cmd/oro/_assets in place via rm -rf, so QG only
# invokes it when assets are missing and serializes that initial staging.
ensure_stage_assets() {
    if [ -f "cmd/oro/_assets/.version" ]; then
        return 0
    fi
    if [ ! -f "cmd/oro/embed.go" ] || ! grep -q "_assets" "cmd/oro/embed.go"; then
        return 0
    fi
    if [ ! -f "Makefile" ] || ! grep -qE '^stage-assets:' "Makefile"; then
        echo "FAIL: cmd/oro embeds _assets but Makefile stage-assets target is unavailable"
        return 1
    fi

    local lock_dir="${TMPDIR:-/tmp}/oro-stage-assets.lock"
    local waited=0
    while ! mkdir "$lock_dir" 2>/dev/null; do
        sleep 0.2
        waited=$((waited + 1))
        if [ "$waited" -ge 300 ]; then
            echo "FAIL: timed out waiting for stage-assets lock at $lock_dir"
            return 1
        fi
    done
    QG_STAGE_ASSETS_LOCK="$lock_dir"

    if [ ! -f "cmd/oro/_assets/.version" ]; then
        if ! make stage-assets; then
            rmdir "$QG_STAGE_ASSETS_LOCK" 2>/dev/null || true
            QG_STAGE_ASSETS_LOCK=""
            return 1
        fi
    fi
    rmdir "$QG_STAGE_ASSETS_LOCK" 2>/dev/null || true
    QG_STAGE_ASSETS_LOCK=""
}
{{if .HasGo}}
# =============================================================================
# LANE: GO
# =============================================================================

# Keep lint diagnostics scoped to this gate invocation. golangci-lint cache
# entries can contain absolute source paths from sibling worktrees.
# shellcheck disable=SC2317,SC2329
run_golangci_lint() {
    local lint_cache="$QG_DIR/golangci-lint-cache"
    mkdir -p "$lint_cache"
    GOLANGCI_LINT_CACHE="$lint_cache" GOFLAGS=-buildvcs=false \
        golangci-lint run --timeout 10m --allow-parallel-runners ./cmd/... ./internal/... ./pkg/...
}

# shellcheck disable=SC2317,SC2329
go_formatter_check() {
    local tool="$1"
    local output=""
    output=$(go tool "$tool" -l $GO_DIRS 2>/dev/null)
    if [ -z "$output" ]; then
        return 0
    fi
    printf '%s\n' "$output"
    return 1
}

# shellcheck disable=SC2317
lane_go() {
    local pass=0 fail=0

    local GO_DIRS="cmd internal pkg"
    if ! $STAGE_ASSETS_READY; then
        echo "$STAGE_ASSETS_ERROR"
        fail=$((fail + 1))
        echo "${pass}:${fail}" > "$QG_DIR/go.rc"
        return
    fi

    # --- Tier 1: Formatting (parallel) ---
    header "GO TIER 1: FORMATTING"
    parallel_checks \
        "gofumpt" "go_formatter_check gofumpt" \
        "goimports" "go_formatter_check goimports"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; return; fi

    # --- Tier 2: Lint (parallel) ---
    header "GO TIER 2: LINT"
    parallel_checks \
        "golangci-lint" "run_golangci_lint"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; return; fi

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
        if ! should_enforce_go_coverage_threshold; then
            return 0
        fi
        if [ "$(echo "$cov < 78" | bc -l)" -eq 1 ]; then
            echo "FAIL: coverage ${cov}% is below 78% threshold"
            return 1
        fi
    }

    parallel_checks \
        "go test + coverage" "go_test_with_coverage" \
        "go build" "go build -buildvcs=false ./..." \
        "go vet" "go vet ./..."
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/go.rc"; return; fi

    # --- Tier 4: Mutation Testing ---
    if ! should_run_mutation_tests; then
        header "GO TIER 4: MUTATION TESTING (skipped — $(mutation_skip_reason))"
    elif go tool -n go-mutesting >/dev/null 2>&1; then
        header "GO TIER 4: MUTATION TESTING (incremental)"

        # shellcheck disable=SC2329
        run_go_mutation_test() {
            local mutation_base
            mutation_base=$(mutation_base_ref)
            if ! qg_git rev-parse --verify "$mutation_base" >/dev/null 2>&1; then
                echo "WARNING: Cannot find mutation base $mutation_base — cannot determine changed files for mutation"
                echo "FAIL: mutation testing requires a base branch to compute diff"
                return 1
            fi
            local changed
            changed=$(qg_git diff --name-only "$mutation_base" -- $GO_DIRS 2>/dev/null |
                grep '\.go$' |
                grep -v '_test\.go$' |
                grep -v '_generated\.' |
                grep -v 'cmd/oro/_assets' ||
                true)
            if [ -z "$changed" ]; then
                echo "No changed Go files to mutate — skipping"
                return 0
            fi
            local -a changed_files
            mapfile -t changed_files <<< "$changed"
            local pre_mutation_patch="$QG_DIR/go-mutation-pre-${RANDOM}.patch"
            local side_effect_snapshot="$QG_DIR/go-mutation-side-effects-${RANDOM}"
            git diff -- $GO_DIRS > "$pre_mutation_patch" || true
            snapshot_go_mutation_side_effects "$side_effect_snapshot"
            GO_MUTATION_PRE_PATCH="$pre_mutation_patch"
            GO_MUTATION_SIDE_EFFECT_SNAPSHOT="$side_effect_snapshot"
            trap 'QG_EXIT_STATUS=$?; restore_go_mutation_worktree "$GO_MUTATION_PRE_PATCH" >/dev/null 2>&1 || true; restore_go_mutation_side_effects "$GO_MUTATION_SIDE_EFFECT_SNAPSHOT" >/dev/null 2>&1 || true; exit "$QG_EXIT_STATUS"' EXIT
            local output mutesting_exit=0
            output=$(timeout 480 go tool go-mutesting --exec-timeout=60 "${changed_files[@]}" 2>&1) || mutesting_exit=$?
            local restore_failed=0
            restore_go_mutation_worktree "$pre_mutation_patch" || restore_failed=1
            restore_go_mutation_side_effects "$side_effect_snapshot" || restore_failed=1
            if [ "$restore_failed" -ne 0 ]; then
                echo "FAIL: failed to restore pre-existing unstaged changes or mutation side effects"
                return 1
            fi
            if [ "$mutesting_exit" -eq 124 ]; then
                echo "WARNING: mutation testing timed out after 8min — skipping score check"
                return 0
            fi
            echo "$output"
            local score
            score=$(echo "$output" | grep "The mutation score is" | awk '{print $5}')
            local total
            total=$(echo "$output" | sed -nE 's/.*total is ([0-9]+).*/\1/p' | tail -1)
            if [ "$total" = "0" ]; then
                echo "No mutations generated for changed files — skipping"
                return 0
            fi
            if [ -z "$score" ]; then
                if [ "$mutesting_exit" -ne 0 ]; then
                    echo "FAIL: go-mutesting crashed (exit $mutesting_exit) — treating as mutation failure"
                    return 1
                fi
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
    if check "ruff format" "qg_run_ruff_format_source"; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
        echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return
    fi

    # --- Tier 2: Linting (parallel) ---
    header "PYTHON TIER 2: LINTING"
    parallel_checks "ruff check" "qg_run_ruff_check_source"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return; fi

    # --- Tier 3: Type Checking ---
    header "PYTHON TIER 3: TYPE CHECKING"
    if qg_pyright --version >/dev/null 2>&1; then
        if check "pyright" "qg_run_pyright_source"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
            echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return
        fi
    fi

    # --- Tier 4: Testing ---
    header "PYTHON TIER 4: TESTING"
    if compgen -G "tests/test_*.py" > /dev/null 2>&1 || compgen -G "tests/**/test_*.py" > /dev/null 2>&1; then
        if check "pytest" "qg_run_python_tool pytest"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
            echo "${pass}:${fail}" > "$QG_DIR/python.rc"; return
        fi
    fi

    # --- Tier 5: Mutation Testing ---
    if ! should_run_mutation_tests; then
        header "PYTHON TIER 5: MUTATION TESTING (skipped — $(mutation_skip_reason))"
    elif [ -f "cosmic-ray.toml" ] && command -v uv >/dev/null 2>&1; then
        header "PYTHON TIER 5: MUTATION TESTING (incremental)"

        # shellcheck disable=SC2329
        run_python_mutation_test() {
            local mutation_base
            mutation_base=$(mutation_base_ref)
            if ! qg_git rev-parse --verify "$mutation_base" >/dev/null 2>&1; then
                echo "WARNING: Cannot find mutation base $mutation_base — cannot determine changed files for mutation"
                echo "FAIL: mutation testing requires a base branch to compute diff"
                return 1
            fi
            local changed
            changed=$(qg_git diff --name-only "$mutation_base" -- '*.py' 2>/dev/null |
                grep -v 'test_' |
                grep -v '__pycache__' |
                grep -v 'archive/' ||
                true)
            if [ -z "$changed" ]; then
                echo "No changed Python files to mutate — skipping"
                return 0
            fi
            local cr_session="$QG_DIR/cr-$$.sqlite"
            uv run cosmic-ray init cosmic-ray.toml "$cr_session" --force &&
                uv run cosmic-ray exec cosmic-ray.toml "$cr_session" &&
                uv run cr-report "$cr_session" &&
                uv run cr-rate "$cr_session" --fail-over 50
        }
        if check "cosmic-ray" "run_python_mutation_test"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
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
        if check "shellcheck" "qg_run_shellcheck_source"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    fi

    header "DOCS & CONFIG"
    local BIOME_PATHS=""
    for p in {{.OroDocsDir}} .github/; do
        [ -d "$p" ] && BIOME_PATHS="$BIOME_PATHS $p"
    done
    if compgen -G "*.json" > /dev/null 2>&1; then
        BIOME_PATHS="$BIOME_PATHS *.json"
    fi

    local docs_checks=(
        "markdownlint" "$NODE_BIN/markdownlint-cli2 --config .markdownlint.yml 'docs/**/*.md' '*.md' '!references/**' '!archive/**'"
        "yamllint" "qg_run_yamllint_source"
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

STAGE_ASSETS_READY=true
STAGE_ASSETS_ERROR=""
{{if .HasGo}}if ! STAGE_ASSETS_ERROR=$(ensure_stage_assets 2>&1); then
    STAGE_ASSETS_READY=false
fi
{{end}}

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

# Keep only the lane-local boundary serialized; all language lanes above ran
# concurrently and lockless.
run_serial_lane || true

# Aggregate pass/fail counts
TOTAL_PASS=0
TOTAL_FAIL=0
expected_rc_files=(
{{if .HasGo}}    "$QG_DIR/go.rc"
{{end}}{{if .HasPython}}    "$QG_DIR/python.rc"
{{end}}    "$QG_DIR/other.rc"
    "$QG_DIR/serial.rc"
)
for rc_file in "${expected_rc_files[@]}"; do
    if [ -f "$rc_file" ]; then
        IFS=: read -r p f < "$rc_file"
        TOTAL_PASS=$((TOTAL_PASS + p))
        TOTAL_FAIL=$((TOTAL_FAIL + f))
    else
        echo "FAIL: missing lane result $(basename "$rc_file")"
        TOTAL_FAIL=$((TOTAL_FAIL + 1))
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
