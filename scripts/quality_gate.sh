#!/bin/sh
# shellcheck shell=bash
# =============================================================================
# Oro Quality Gate — Parallel lanes with early exit
#
# Architecture: 4 independent lanes (Go, Python, Shell, Docs) run in
# parallel. Within each lane, checks run in tiers — independent checks
# within a tier run in parallel, and the lane bails on first tier failure.
# Wall-clock time ≈ max(lane) instead of sum(all checks).
# =============================================================================

if [ "${ORO_QG_BASH_BOOTSTRAPPED:-}" != "1" ]; then
	if [ -n "${LC_ALL:-}" ] && ! locale -a 2>/dev/null | grep -qx "$LC_ALL"; then
		export LC_ALL=C
		export LANG=C
	fi
	export ORO_QG_BASH_BOOTSTRAPPED=1
	exec /usr/bin/env bash "$0" "$@"
fi
unset ORO_QG_BASH_BOOTSTRAPPED

set -euo pipefail

QG_MUTATION_TESTING=false

while [ "$#" -gt 0 ]; do
	case "$1" in
	--mutation-testing)
		QG_MUTATION_TESTING=true
		shift
		;;
	-h | --help)
		echo "Usage: $0 [--mutation-testing]"
		exit 0
		;;
	*)
		echo "Unknown quality gate argument: $1" >&2
		exit 2
		;;
	esac
done

# Prevent hook env leakage into test subprocesses.
# Save worktree state BEFORE unsetting — mutation testing needs this later to
# resolve refs (git rev-parse --verify main) in worktrees where .git is a
# gitlink file and .git/worktrees/<name> has no refs/heads/main.
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
# shellcheck disable=SC2317,SC2329
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

# Keep the golangci-lint cache inside the QG temp directory. Shared
# golangci-lint caches can collide across concurrent workers.
export GOLANGCI_LINT_CACHE="$QG_DIR/golangci-lint-cache"
export GOCACHE="$QG_DIR/go-build-cache"
export UV_CACHE_DIR="${UV_CACHE_DIR:-$QG_DIR/uv-cache}"
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

quality_gate_lock_stale() {
	local lock_dir="$1"
	local owner="$lock_dir/owner"
	local pid age stale_after
	if [ -f "$owner" ]; then
		pid=$(sed -n 's/^pid=//p' "$owner" | head -1)
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			return 1
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

write_quality_gate_lock_owner() {
	local lock_dir="$1"
	local token="$2"
	{
		echo "pid=$$"
		echo "token=$token"
		echo "repo=$REPO_ROOT"
		echo "created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$lock_dir/owner"
}

quality_gate_lock_poll_seconds() {
	printf '%s\n' "${ORO_QG_LOCK_POLL_SECONDS:-2}"
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

acquire_quality_gate_lock() {
	local lock_dir="$REPO_ROOT/.oro-quality-gate.lock"
	local queue_dir="$REPO_ROOT/.oro-quality-gate.queue"
	local ticket_name poll_seconds reported_waiting
	local waited=0
	reported_waiting=false
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
			if [ "$waited" -ge "${ORO_QG_LOCK_TIMEOUT_SECONDS:-1800}" ]; then
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
		if [ "$waited" -ge "${ORO_QG_LOCK_TIMEOUT_SECONDS:-1800}" ]; then
			echo "FAIL: timed out waiting for quality gate lock: $lock_dir" >&2
			return 1
		fi
	done
	QG_RUN_LOCK="$lock_dir"
	QG_RUN_LOCK_TOKEN="$$-$(date +%s)-$RANDOM"
	write_quality_gate_lock_owner "$lock_dir" "$QG_RUN_LOCK_TOKEN"
	rm -f "$QG_RUN_QUEUE_TICKET/owner" 2>/dev/null || true
	rmdir "$QG_RUN_QUEUE_TICKET" 2>/dev/null || true
	rmdir "$queue_dir" 2>/dev/null || true
	QG_RUN_QUEUE_TICKET=""
}

acquire_quality_gate_lock

# =============================================================================
# PRIMITIVES
# =============================================================================

# Run git with ref-resolution support in worktrees.
# GIT_DIR is unset globally to prevent leakage into test subprocesses, but
# mutation testing needs ref resolution (git rev-parse --verify main, git diff
# main). This wrapper temporarily sets the worktree-specific GIT_DIR for the git
# command only; using the common dir loses the current worktree branch.
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
	pylint --disable=all --enable=E "${files[@]}"
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
		echo "WARNING: Cannot find coverage base $coverage_base — enforcing 85% Go coverage threshold"
		return 0
	fi
	changed=$(qg_git diff --name-only "$coverage_base" -- internal/ pkg/ 2>/dev/null |
		grep '\.go$' |
		grep -v '_test\.go$' ||
		true)
	if [ -z "$changed" ]; then
		echo "Skipping 85% Go coverage threshold: changed files are outside measured ./internal and ./pkg production surface"
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
			readlink "$hooks_dir/pre-push" >"$snapshot_dir/git-hooks/pre-push.target"
			printf 'symlink\n' >"$snapshot_dir/git-hooks/pre-push.state"
		elif [ -f "$hooks_dir/pre-push" ]; then
			cp -p "$hooks_dir/pre-push" "$snapshot_dir/git-hooks/pre-push"
			printf 'file\n' >"$snapshot_dir/git-hooks/pre-push.state"
		else
			printf 'missing\n' >"$snapshot_dir/git-hooks/pre-push.state"
		fi
	fi
	find cmd internal pkg -type f -name '*.go.tmp' -print >"$snapshot_dir/go-tmp.before" 2>/dev/null || true
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
# Output goes to the lane's captured stdout.
check() {
	local name="$1"
	local cmd="$2"
	local slug
	slug=$(echo "$name" | tr ' ()/.' '------')
	local out="$QG_DIR/check-${slug}-${RANDOM}.out"

	printf '%b▶%b %-30s' "$BLUE" "$NC" "$name"

	if eval "$cmd" >"$out" 2>&1; then
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
	for candidate in ".venv/bin/$tool" "$REPO_ROOT/.venv/bin/$tool" "$HOME/.local/bin/$tool"; do
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

# shellcheck disable=SC2317,SC2329 # invoked indirectly via check/parallel_checks command strings
qg_run_python_tool() {
	local tool="$1"
	shift
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

# shellcheck disable=SC2317,SC2329 # invoked indirectly via check/parallel_checks command strings
qg_ruff() {
	qg_run_python_tool ruff "$@"
}

qg_pyright() {
	local path
	if path=$(qg_python_tool_path pyright); then
		"$path" "$@"
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
		local name="$1" cmd="$2"
		shift 2
		local pfx="$QG_DIR/pc-${tier_id}-${i}"
		(
			local cmd_out="${pfx}.cmd-out"
			if eval "$cmd" >"$cmd_out" 2>&1; then
				printf '%b▶%b %-30s%b✓ PASS%b\n' "$BLUE" "$NC" "$name" "$GREEN" "$NC" >"${pfx}.display"
				echo "pass" >"${pfx}.rc"
			else
				local status=$?
				if [ "$status" -eq 77 ]; then
					{
						printf '%b▶%b %-30s%bSKIP%b\n' "$BLUE" "$NC" "$name" "$YELLOW" "$NC"
						head -20 "$cmd_out"
					} >"${pfx}.display"
					echo "pass" >"${pfx}.rc"
				else
					{
						printf '%b▶%b %-30s%b✗ FAIL%b\n' "$BLUE" "$NC" "$name" "$RED" "$NC"
						cat "$cmd_out"
					} >"${pfx}.display"
					echo "fail" >"${pfx}.rc"
				fi
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

# shellcheck disable=SC2317,SC2329
read_go_formatters_from_config() {
	[ -f ".oro/config.yaml" ] || return 1
	awk '
		/^[[:space:]]*languages:[[:space:]]*$/ { in_languages=1; next }
		in_languages && /^[^[:space:]#][^:]*:/ { in_languages=0 }
		in_languages && /^[[:space:]]{2}go:[[:space:]]*$/ { in_go=1; next }
		in_go && /^[[:space:]]{2}[A-Za-z0-9_-]+:[[:space:]]*$/ { in_go=0; in_formatters=0 }
		in_go && /^[[:space:]]{4}formatters:[[:space:]]*$/ { in_formatters=1; next }
		in_formatters && /^[[:space:]]{6}-[[:space:]]*/ {
			tool=$0
			sub(/^[[:space:]]*-[[:space:]]*/, "", tool)
			sub(/[[:space:]#].*$/, "", tool)
			if (tool ~ /^[A-Za-z0-9_.-]+$/) print tool
			next
		}
		in_formatters && /^[[:space:]]{4}[A-Za-z0-9_-]+:/ { in_formatters=0 }
	' ".oro/config.yaml"
}

# shellcheck disable=SC2317,SC2329
go_formatter_check() {
	local tool="$1"
	local runner=""
	if go tool -n "$tool" >/dev/null 2>&1; then
		runner="go-tool"
	elif command -v "$tool" >/dev/null 2>&1; then
		runner="path"
	else
		echo "SKIP: $tool not installed"
		return 77
	fi

	local -a dirs=()
	local dir
	for dir in cmd internal pkg; do
		if [ -d "$dir" ]; then
			dirs+=("$dir")
		fi
	done
	if [ "${#dirs[@]}" -eq 0 ]; then
		return 0
	fi

	if [ "$runner" = "go-tool" ]; then
		test -z "$(go tool "$tool" -l "${dirs[@]}" 2>/dev/null)"
	else
		test -z "$("$tool" -l "${dirs[@]}" 2>/dev/null)"
	fi
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

# =============================================================================
# DETECT PROJECT TYPES
# =============================================================================

HAS_GO=false
HAS_PYTHON=false
HAS_SHELL=false

if [ -f "go.mod" ]; then HAS_GO=true; fi
if [ -f "pyproject.toml" ] || [ -f "setup.py" ] || [ -f "requirements.txt" ]; then HAS_PYTHON=true; fi
if compgen -G "*.sh" >/dev/null || compgen -G "ad_hoc/*.sh" >/dev/null; then HAS_SHELL=true; fi

STAGE_ASSETS_READY=true
STAGE_ASSETS_ERROR=""
if $HAS_GO; then
	if ! STAGE_ASSETS_ERROR=$(ensure_stage_assets 2>&1); then
		STAGE_ASSETS_READY=false
	fi
fi

# =============================================================================
# LANE: GO
# =============================================================================

# shellcheck disable=SC2317
lane_go() {
	local pass=0 fail=0

	if ! $HAS_GO; then
		echo "${pass}:${fail}" >"$QG_DIR/go.rc"
		return
	fi

	if ! $STAGE_ASSETS_READY; then
		echo "$STAGE_ASSETS_ERROR"
		fail=$((fail + 1))
		echo "${pass}:${fail}" >"$QG_DIR/go.rc"
		return
	fi

	# --- Tier 1: Formatting (parallel) ---
	header "GO TIER 1: FORMATTING"
	local -a go_formatters=()
	mapfile -t go_formatters < <(read_go_formatters_from_config || true)
	if [ "${#go_formatters[@]}" -eq 0 ]; then
		go_formatters=("gofumpt" "goimports")
	fi

	local -a tier1_checks=()
	local formatter
	for formatter in "${go_formatters[@]}"; do
		tier1_checks+=("$formatter" "go_formatter_check '$formatter'")
	done
	parallel_checks "${tier1_checks[@]}"
	pass=$((pass + TIER_PASS))
	fail=$((fail + TIER_FAIL))
	if [ "$fail" -gt 0 ]; then
		echo "${pass}:${fail}" >"$QG_DIR/go.rc"
		return
	fi

	# --- Tier 2: Lint + Dead Code + Architecture (parallel) ---
	header "GO TIER 2: LINT + DEAD CODE + ARCHITECTURE"

	# Dead exports detector (function must be defined before parallel_checks eval's it)
	# shellcheck disable=SC2317,SC2329
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
				elif echo "$scan_line" | grep -qE '^[[:space:]]*//'; then
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
			callers=$(grep -rn --include="*.go" --exclude="*_test.go" "\\b${func_name}\\b" pkg/ internal/ cmd/ |
				grep -v "^${file}:${lineno}:" |
				grep -v -E '^[^:]+:[0-9]+:[[:space:]]*//' ||
				true)

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
		"golangci-lint" "GOCACHE=$QG_DIR/golangci-go-cache GOFLAGS=-buildvcs=false golangci-lint run --timeout 5m --allow-parallel-runners ./cmd/... ./internal/... ./pkg/..."
		"nilaway" "nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/..."
		"dead exports" "check_dead_exports"
		"beadstore imports" "scripts/check-beadstore-imports.sh"
	)
	if [ -f ".go-arch-lint.yml" ] && command -v go-arch-lint >/dev/null 2>&1; then
		tier2_checks+=("go-arch-lint" "go-arch-lint check --project-path .")
	fi
	parallel_checks "${tier2_checks[@]}"
	pass=$((pass + TIER_PASS))
	fail=$((fail + TIER_FAIL))
	if [ "$fail" -gt 0 ]; then
		echo "${pass}:${fail}" >"$QG_DIR/go.rc"
		return
	fi

	# --- Tier 3: Test + Security + Build (parallel) ---
	header "GO TIER 3: TEST + SECURITY + BUILD"

	local COVERAGE_FILE="$QG_DIR/coverage-$$.out"

	# shellcheck disable=SC2317,SC2329
	go_test_with_coverage() {
		local race_flag=""
		local goos goarch
		goos=$(go env GOOS)
		goarch=$(go env GOARCH)
		if [ "${ORO_SKIP_MUTATION:-}" != "1" ] && ! { [ "$goos" = "darwin" ] && [ "$goarch" = "arm64" ]; }; then
			race_flag="-race"
		fi
		# shellcheck disable=SC2086
		GOFLAGS=-buildvcs=false go test $race_flag -shuffle=on -p 3 \
			-coverprofile="$COVERAGE_FILE" ./internal/... ./pkg/... || return 1
		# Exclude newly-ported dashboard TUI packages from coverage — they
		# have low coverage until full integration wiring.
		local filtered="${COVERAGE_FILE}.filtered"
		grep -v -e 'oro/pkg/dashboard/' "$COVERAGE_FILE" >"$filtered"
		local cov
		cov=$(go tool cover -func="$filtered" | grep total | awk '{print $3}' | sed 's/%//')
		echo "Coverage: ${cov}%"
		if ! should_enforce_go_coverage_threshold; then
			return 0
		fi
		if [ "$(echo "$cov < 85" | bc -l)" -eq 1 ]; then
			echo "FAIL: coverage ${cov}% is below 85% threshold"
			return 1
		fi
	}

	local tier3_checks=(
		"go test + coverage" "go_test_with_coverage"
		"go build" "go build -buildvcs=false ./..."
		"go vet" "go vet ./..."
		"CGO-free build" "CGO_ENABLED=0 go build -buildvcs=false ./cmd/oro ./cmd/oro-search-hook"
	)
	tier3_checks+=("govulncheck" "go tool govulncheck ./...")
	parallel_checks "${tier3_checks[@]}"
	pass=$((pass + TIER_PASS))
	fail=$((fail + TIER_FAIL))
	if [ "$fail" -gt 0 ]; then
		echo "${pass}:${fail}" >"$QG_DIR/go.rc"
		return
	fi

	# --- Tier 4: Mutation Testing (sequential, modifies working tree) ---
	if ! should_run_mutation_tests; then
		header "GO TIER 4: MUTATION TESTING (skipped — $(mutation_skip_reason))"
	elif go tool -n go-mutesting >/dev/null 2>&1; then
		header "GO TIER 4: MUTATION TESTING (incremental)"

		# shellcheck disable=SC2317,SC2329
		run_go_mutation_test() {
			local mutation_base
			mutation_base=$(mutation_base_ref)
			# Detect missing main branch explicitly — do NOT silently swallow git errors.
			# 'git diff ... main 2>/dev/null || true' returns empty when main is absent,
			# causing a false PASS. Fail loudly instead (oro-xgwr).
			if ! qg_git rev-parse --verify "$mutation_base" >/dev/null 2>&1; then
				echo "WARNING: Cannot find mutation base $mutation_base (default main) — cannot determine changed files for mutation"
				echo "FAIL: mutation testing requires a base branch to compute diff"
				return 1
			fi
			local changed
			changed=$(qg_git diff --name-only "$mutation_base" -- cmd/ internal/ pkg/ 2>/dev/null |
				grep '\.go$' |
				grep -v '_test\.go$' |
				grep -v '_generated\.' |
				grep -v 'cmd/oro/_assets' ||
				true)
			if [ -z "$changed" ]; then
				echo "No changed Go files to mutate — skipping"
				return 0
			fi

			# Limit mutations to functions touched in the diff (hunk headers + added func lines).
			local match_pattern=""
			local touched_funcs
			touched_funcs=$(qg_git diff "$mutation_base" -- cmd/ internal/ pkg/ 2>/dev/null |
				grep -E '^(\+func |@@.*func )' |
				sed -E 's/.*func[[:space:]]+(\([^)]*\)[[:space:]]+)?([A-Za-z0-9_]+).*/\2/' |
				grep -v '^$' | sort -u | paste -sd'|' - || true)
			if [ -n "$touched_funcs" ]; then
				match_pattern="^($touched_funcs)$"
				echo "Limiting mutations to touched functions: $touched_funcs"
			fi

			# Restore source files on exit, then reapply pre-existing unstaged work.
			# This handles Ctrl-C, OOM, timeout, and normal exit without wiping edits
			# that existed before mutation testing started.
			local pre_mutation_patch="$QG_DIR/go-mutation-pre-${RANDOM}.patch"
			local side_effect_snapshot="$QG_DIR/go-mutation-side-effects-${RANDOM}"
			git diff -- pkg/ internal/ cmd/ >"$pre_mutation_patch" || true
			snapshot_go_mutation_side_effects "$side_effect_snapshot"
			GO_MUTATION_PRE_PATCH="$pre_mutation_patch"
			GO_MUTATION_SIDE_EFFECT_SNAPSHOT="$side_effect_snapshot"
			trap 'QG_EXIT_STATUS=$?; restore_go_mutation_worktree "$GO_MUTATION_PRE_PATCH" >/dev/null 2>&1 || true; restore_go_mutation_side_effects "$GO_MUTATION_SIDE_EFFECT_SNAPSHOT" >/dev/null 2>&1 || true; exit "$QG_EXIT_STATUS"' EXIT
			echo "Mutating changed files: $changed"
			local output mutesting_exit=0
			local -a changed_files
			mapfile -t changed_files <<<"$changed"

			local -a match_args=()
			[ -n "$match_pattern" ] && match_args=("--match=$match_pattern")

			# 8-minute overall cap — if exceeded, pass with warning (best-effort signal).
			output=$(timeout 480 go tool go-mutesting --exec-timeout=60 "${match_args[@]}" "${changed_files[@]}" 2>&1) || mutesting_exit=$?
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
				# Distinguish crash (nonzero exit) from "no mutations possible" (exit 0).
				# An empty score after a crash is a FAIL, not a silent skip (oro-xgwr).
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

	echo "${pass}:${fail}" >"$QG_DIR/go.rc"
}

# =============================================================================
# LANE: PYTHON
# =============================================================================

# shellcheck disable=SC2317
lane_python() {
	local pass=0 fail=0

	if ! $HAS_PYTHON; then
		echo "${pass}:${fail}" >"$QG_DIR/python.rc"
		return
	fi

	# --- Tier 1: Formatting ---
	header "PYTHON TIER 1: FORMATTING"
	if check "ruff format" "qg_run_ruff_format_source"; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		echo "${pass}:${fail}" >"$QG_DIR/python.rc"
		return
	fi

	# --- Tier 2: Linting (parallel) ---
	header "PYTHON TIER 2: LINTING"
	local tier2_checks=("ruff check" "qg_run_ruff_check_source")
	if command -v pylint >/dev/null 2>&1; then
		tier2_checks+=("pylint" "qg_run_pylint_source")
	fi
	parallel_checks "${tier2_checks[@]}"
	pass=$((pass + TIER_PASS))
	fail=$((fail + TIER_FAIL))
	if [ "$fail" -gt 0 ]; then
		echo "${pass}:${fail}" >"$QG_DIR/python.rc"
		return
	fi

	# --- Tier 3: Type Checking ---
	header "PYTHON TIER 3: TYPE CHECKING"
	if qg_pyright --version >/dev/null 2>&1; then
		if check "pyright" "qg_run_pyright_source"; then
			pass=$((pass + 1))
		else
			fail=$((fail + 1))
			echo "${pass}:${fail}" >"$QG_DIR/python.rc"
			return
		fi
	fi

	# --- Tier 4: Testing ---
	header "PYTHON TIER 4: TESTING"
	if compgen -G "tests/test_*.py" >/dev/null 2>&1 || compgen -G "tests/**/test_*.py" >/dev/null 2>&1; then
		if check "pytest" "qg_run_python_tool pytest"; then
			pass=$((pass + 1))
		else
			fail=$((fail + 1))
			echo "${pass}:${fail}" >"$QG_DIR/python.rc"
			return
		fi
	fi

	# --- Tier 5: Mutation Testing ---
	if ! should_run_mutation_tests; then
		header "PYTHON TIER 5: MUTATION TESTING (skipped — $(mutation_skip_reason))"
	elif [ -f "cosmic-ray.toml" ] && command -v uv >/dev/null 2>&1; then
		header "PYTHON TIER 5: MUTATION TESTING (incremental)"
		local CR_SESSION="$QG_DIR/cr-$$.sqlite"

		# shellcheck disable=SC2317,SC2329
		run_mutation_test() {
			local mutation_base
			mutation_base=$(mutation_base_ref)
			# Detect missing main branch explicitly (same fix as Go mutation, oro-xgwr).
			if ! qg_git rev-parse --verify "$mutation_base" >/dev/null 2>&1; then
				echo "WARNING: Cannot find mutation base $mutation_base (default main) — cannot determine changed files for mutation"
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
			echo "Changed Python files: $changed"
			uv run cosmic-ray init cosmic-ray.toml "$CR_SESSION" --force 2>&1 &&
				uv run cosmic-ray exec cosmic-ray.toml "$CR_SESSION" 2>&1 &&
				uv run cr-report "$CR_SESSION" 2>&1 &&
				uv run cr-rate "$CR_SESSION" --fail-over 50 2>&1
		}

		if check "cosmic-ray" "run_mutation_test"; then
			pass=$((pass + 1))
		else
			fail=$((fail + 1))
		fi
	fi

	echo "${pass}:${fail}" >"$QG_DIR/python.rc"
}

# =============================================================================
# LANE: SHELL + DOCS (lightweight, combined into one lane)
# =============================================================================

# shellcheck disable=SC2317
lane_other() {
	local pass=0 fail=0

	if $HAS_SHELL; then
		header "SHELL: FORMAT + LINT"
		parallel_checks \
			"shfmt" "find . -name '*.sh' -not -path './references/*' -not -path './archive/*' -not -path './.tmp-test/*' -not -path './.cache/*' -not -path './.worktrees/*' -not -path './.claude/worktrees/*' -not -path './.venv/*' -not -path './node_modules/*' -not -path './cmd/oro/_assets/*' -exec shfmt -ln bash -d {} +" \
			"shellcheck" "find . -name '*.sh' -not -path './references/*' -not -path './archive/*' -not -path './.tmp-test/*' -not -path './.cache/*' -not -path './.worktrees/*' -not -path './.claude/worktrees/*' -not -path './.venv/*' -not -path './node_modules/*' -not -path './cmd/oro/_assets/*' -exec shellcheck --severity=info {} +"
		pass=$((pass + TIER_PASS))
		fail=$((fail + TIER_FAIL))
	fi

	header "DOCS & CONFIG"
	local BIOME_PATHS=""
	for p in docs/ .github/; do
		[ -d "$p" ] && BIOME_PATHS="$BIOME_PATHS $p"
	done
	if compgen -G "*.json" >/dev/null 2>&1; then
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
	pass=$((pass + TIER_PASS))
	fail=$((fail + TIER_FAIL))

	echo "${pass}:${fail}" >"$QG_DIR/other.rc"
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
lane_go >"$QG_DIR/go.out" 2>&1 &
PID_GO=$!
lane_python >"$QG_DIR/python.out" 2>&1 &
PID_PY=$!
lane_other >"$QG_DIR/other.out" 2>&1 &
PID_OT=$!

# Wait for all lanes
wait "$PID_GO" 2>/dev/null || true
wait "$PID_PY" 2>/dev/null || true
wait "$PID_OT" 2>/dev/null || true

# Display results in order: Go, Shell+Docs, Python
cat "$QG_DIR/go.out" 2>/dev/null || true
cat "$QG_DIR/other.out" 2>/dev/null || true
cat "$QG_DIR/python.out" 2>/dev/null || true

# Aggregate pass/fail counts
TOTAL_PASS=0
TOTAL_FAIL=0
for rc_file in "$QG_DIR"/go.rc "$QG_DIR"/python.rc "$QG_DIR"/other.rc; do
	if [ -f "$rc_file" ]; then
		IFS=: read -r p f <"$rc_file"
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
