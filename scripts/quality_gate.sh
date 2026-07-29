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
# Coverage profiles: the main lockless lane covers ./internal/... ./pkg/... with
# the serial-lane tests SKIPPED; the serial lane emits its own profile so the
# ≥78% threshold is enforced on the MERGED coverage (oro-hwx2/oro-zdpg) — otherwise
# the guarded tests' coverage would be lost from the gate.
GO_COVERAGE_FILE="$QG_DIR/go-coverage.out"
GO_SERIAL_COVERAGE_FILE="$QG_DIR/go-serial-coverage.out"
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

# Tool caches deliberately inherit their environment (or each tool's standard
# external default). Only QG scratch data belongs under TMPDIR/QG_DIR.
export GOMAXPROCS="${ORO_QG_GOMAXPROCS:-2}"

# Resolve repo root node_modules (works from worktrees too). Non-git harness
# tests copy this script into temporary projects, so fall back to the current
# project directory when no git common dir exists.
if [ -n "${ORO_QG_REPO_ROOT_OVERRIDE:-}" ]; then
	# Test seam: relocate the cross-worktree lock/queue to a hermetic dir.
	REPO_ROOT="$ORO_QG_REPO_ROOT_OVERRIDE"
elif QG_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null)"; then
	REPO_ROOT="$(cd "$QG_COMMON_DIR/.." && pwd)"
else
	REPO_ROOT="$PWD"
fi
NODE_BIN="$REPO_ROOT/node_modules/.bin"
# Directory of this script, used to locate the checked-in serial-lane test list
# regardless of the caller's working directory.
SCRIPT_SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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
		if [ ! -d "$artifact" ] || [ -L "$artifact" ]; then
			continue
		fi
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

# check_inherited_quality_gate_lock short-circuits a gate invoked *inside* another
# gate's lock scope (nested / mutation re-invocation): the parent already ran the
# checks, so exit success immediately. Preserved from the pre-oro-hwx2 top-level
# lock acquisition; only the acquisition itself moved to the serial lane.
check_inherited_quality_gate_lock() {
	if quality_gate_lock_is_inherited "$REPO_ROOT/.oro-quality-gate.lock"; then
		exit 0
	fi
}

# neutralize_serial_lane_env actively clears ORO_QG_SERIAL_LANE before the
# concurrent main phase. Callers pass os.Environ() through unfiltered
# (worker.go qualityGateEnv, dispatcher.go qgRunnerEnv), so a leaked =1 would run
# the guarded socket-timing tests concurrently and resurrect the flakiness this
# split removes. The serial lane re-enables it locally (oro-hwx2).
neutralize_serial_lane_env() {
	unset ORO_QG_SERIAL_LANE
}

# run_phase_marker is a TEST-ONLY probe (enabled by ORO_QG_PHASE_MARKER_DIR). It
# records peak concurrency for a phase so the concurrency tests can assert the
# main phase overlaps while the serial lane is mutually exclusive, without running
# the heavy real checks. Production runs never set ORO_QG_PHASE_MARKER_DIR.
run_phase_marker() {
	local phase="$1" sleep_s="$2"
	local dir="$ORO_QG_PHASE_MARKER_DIR"
	local id="${ORO_QG_PROBE_ID:-$$}"
	mkdir -p "$dir/active"
	printf '%s\n' "${ORO_QG_SERIAL_LANE:-}" >"$dir/env.$phase.$id"
	touch "$dir/active/$phase.$id"
	if [ "${sleep_s:-0}" != "0" ]; then
		sleep "$sleep_s"
	fi
	local count
	count=$(find "$dir/active" -maxdepth 1 -name "$phase.*" 2>/dev/null | wc -l | tr -d ' ')
	printf '%s\n' "$count" >"$dir/peak.$phase.$id"
	rm -f "$dir/active/$phase.$id"
}

# run_serial_lane runs the concurrency-flaky guarded tests (the oro-sjp8 canonical
# list) under the cross-worktree FIFO lock, with ORO_QG_SERIAL_LANE=1 so the
# qgserial guards actually run them. Acquiring the lock HERE — not around the whole
# gate — is the core of oro-hwx2: the main phase above ran lockless and concurrent.
# Writes pass:fail to $QG_DIR/serial.rc.
run_serial_lane() {
	if ! acquire_quality_gate_lock; then
		echo "0:1" >"$QG_DIR/serial.rc" 2>/dev/null || true
		return 1
	fi
	export ORO_QG_SERIAL_LANE=1
	if [ -n "${ORO_QG_PHASE_MARKER_DIR:-}" ]; then
		run_phase_marker serial "${ORO_QG_SERIAL_SLEEP:-0}"
		return 0
	fi
	header "SERIAL TIMING LANE (guarded socket/timing tests)"
	local dispatcher_dir="$SCRIPT_SELF_DIR/../pkg/dispatcher"
	local list="$dispatcher_dir/testdata/serial_lane_tests.txt"
	local names run_filter
	if [ -n "${ORO_QG_SERIAL_LANE_RUN_OVERRIDE:-}" ]; then
		# Test seam: narrow the lane to a specific -run filter (e.g. the regression
		# canary) so the serial-lane failure path can be proven cheaply.
		run_filter="$ORO_QG_SERIAL_LANE_RUN_OVERRIDE"
	elif [ ! -d "$dispatcher_dir" ]; then
		# Script copied into a non-oro harness project: no guarded tests to run.
		echo "0:0" >"$QG_DIR/serial.rc"
		return 0
	elif [ ! -f "$list" ]; then
		# In the oro repo the list is the ONLY place the guarded tests run — a
		# missing list must fail the gate, never silently pass (fail-closed).
		echo "FAIL: serial-lane test list missing: $list (guarded socket/timing tests would not run)" >&2
		echo "0:1" >"$QG_DIR/serial.rc"
		return 1
	else
		names=$(grep -vE '^[[:space:]]*#' "$list" | grep -vE '^[[:space:]]*$' | sed -E 's/^[[:space:]]+|[[:space:]]+$//g')
		# shellcheck disable=SC2086 # intentional word-splitting: one -run alternative per name
		run_filter=$(printf '^%s$|' $names)
		run_filter="${run_filter%|}"
		if [ -z "$run_filter" ]; then
			echo "FAIL: serial-lane test list is empty: $list" >&2
			echo "0:1" >"$QG_DIR/serial.rc"
			return 1
		fi
	fi
	# Mirror the main suite's race policy so the guarded tests keep -race coverage
	# (several are explicit data-race tests); the main suite skips -race only on
	# darwin/arm64 and when mutation is skipped (scripts/quality_gate.sh go lane).
	local race_flag="" goos goarch
	goos=$(go env GOOS)
	goarch=$(go env GOARCH)
	if [ "${ORO_SKIP_MUTATION:-}" != "1" ] && ! { [ "$goos" = "darwin" ] && [ "$goarch" = "arm64" ]; }; then
		race_flag="-race"
	fi
	# -race requires cgo on every OS except darwin (go's own build-time
	# exemption; see cmd/go/internal/work/init.go). This script can itself be
	# invoked as a subprocess of a CGO-free `go test` run (e.g. the CI
	# "CGO-free Build" job runs `CGO_ENABLED=0 go test ./...`, and
	# TestConcurrentGatesNoTimingFlakeSerialLaneCatchesRegression shells out to
	# this script from inside that process) — an inherited CGO_ENABLED=0 would
	# then make -race fail outright with "go: -race requires cgo" on
	# linux/amd64 CI runners even though the guarded tests have nothing to do
	# with the CGO-free build under test. Force cgo on for just this
	# invocation whenever -race needs it.
	#
	# The default is a bare `env` rather than an empty array: this script's
	# shebang is #!/bin/sh, which on macOS is bash 3.2, where expanding an
	# empty array under `set -u` aborts with "unbound variable". A bare `env`
	# prefix is a no-op, so the array is never empty on any path.
	local force_cgo=(env)
	if [ -n "$race_flag" ] && [ "$goos" != "darwin" ]; then
		force_cgo=(env CGO_ENABLED=1)
	fi
	# Emit a coverage profile so the guarded tests' coverage is merged back into
	# the ≥78% threshold check (enforce_go_coverage_threshold); otherwise moving
	# them out of the main lane would silently erode measured coverage.
	# shellcheck disable=SC2086 # race_flag is intentionally word-split (empty or -race)
	if GOFLAGS=-buildvcs=false "${force_cgo[@]}" go test $race_flag -coverprofile="$GO_SERIAL_COVERAGE_FILE" \
		./pkg/dispatcher -run "$run_filter" -count=1; then
		echo "1:0" >"$QG_DIR/serial.rc"
	else
		echo "0:1" >"$QG_DIR/serial.rc"
	fi
}

# merge_coverage_profiles writes $1 from the remaining profile args, summing the
# execution count of each identical coverage block. Correct for `go tool cover`
# percentage (a block counts as covered if covered in ANY input). Preserves the
# first `mode:` header and block order.
merge_coverage_profiles() {
	local out="$1"
	shift
	awk '
		FNR == 1 && $1 == "mode:" { if (!seen_mode) { print; seen_mode = 1 } next }
		{
			block = $1 " " $2
			if (!(block in counts)) { order[++n] = block }
			counts[block] += $3
		}
		END { for (i = 1; i <= n; i++) print order[i], counts[order[i]] }
	' "$@" >"$out"
}

# enforce_go_coverage_threshold enforces the ≥78% gate on the MERGED main+serial
# coverage. Runs AFTER the serial lane. Writes pass:fail to $QG_DIR/coverage.rc.
enforce_go_coverage_threshold() {
	if [ ! -f "$GO_COVERAGE_FILE" ]; then
		# No main coverage profile (e.g. go test failed earlier); nothing to add.
		echo "0:0" >"$QG_DIR/coverage.rc"
		return 0
	fi
	if ! should_enforce_go_coverage_threshold; then
		echo "0:0" >"$QG_DIR/coverage.rc"
		return 0
	fi
	local merged="$QG_DIR/go-coverage-merged.out"
	if [ -f "$GO_SERIAL_COVERAGE_FILE" ]; then
		merge_coverage_profiles "$merged" "$GO_COVERAGE_FILE" "$GO_SERIAL_COVERAGE_FILE"
	else
		cp "$GO_COVERAGE_FILE" "$merged"
	fi
	local filtered="$merged.filtered"
	grep -v -e 'oro/pkg/dashboard/' "$merged" >"$filtered"
	local cov
	cov=$(go tool cover -func="$filtered" | grep total | awk '{print $3}' | sed 's/%//')
	echo "Coverage (main + serial lane merged): ${cov}%"
	if [ "$(echo "$cov < 78" | bc -l)" -eq 1 ]; then
		echo "FAIL: coverage ${cov}% is below 78% threshold"
		echo "0:1" >"$QG_DIR/coverage.rc"
		return 1
	fi
	echo "1:0" >"$QG_DIR/coverage.rc"
}

# Nested-gate short-circuit, then neutralize any leaked serial-lane env before the
# concurrent, lockless main phase. The FIFO lock is acquired later, only for the
# serial timing lane (run_serial_lane).
check_inherited_quality_gate_lock
neutralize_serial_lane_env

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
qg_shell_source_files() {
	qg_source_files '*.sh'
}

# shellcheck disable=SC2317,SC2329
qg_run_shfmt_source() {
	local -a files=()
	mapfile -t files < <(qg_shell_source_files)
	if [ "${#files[@]}" -eq 0 ]; then
		echo "No tracked shell source files"
		return 0
	fi
	shfmt -ln bash -d "${files[@]}"
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
	# Run pylint in the project's dependency environment so first-party imports
	# (e.g. PyYAML) resolve; --with provides pylint itself in that env.
	if command -v uv >/dev/null 2>&1; then
		uv run --with pylint pylint --disable=all --enable=E "${files[@]}"
	else
		pylint --disable=all --enable=E "${files[@]}"
	fi
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
	local output=""
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
		output=$(go tool "$tool" -l "${dirs[@]}" 2>/dev/null)
	else
		output=$("$tool" -l "${dirs[@]}" 2>/dev/null)
	fi
	if [ -z "$output" ]; then
		return 0
	fi
	printf '%s\n' "$output"
	return 1
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

# Keep lint diagnostics scoped to this gate invocation. golangci-lint cache
# entries can contain absolute source paths from sibling worktrees.
# shellcheck disable=SC2317,SC2329
run_golangci_lint() {
	local lint_cache="$QG_DIR/golangci-lint-cache"
	mkdir -p "$lint_cache"
	GOLANGCI_LINT_CACHE="$lint_cache" GOFLAGS=-buildvcs=false \
		golangci-lint run --timeout 10m --allow-parallel-runners ./cmd/... ./internal/... ./pkg/...
}

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
			# NOTE: cmd/ is deliberately NOT scanned for dead exports yet. Adding it
			# surfaces 15 genuinely-unused exported funcs (11 in cmd/oro/tmux.go),
			# and the only ways to make the gate pass today are to add 15
			# //oro:testonly suppressions — the exact debt this check exists to
			# prevent — or to delete them, which is a separate change. The caller
			# search above already includes cmd/, so wiring FROM cmd/ counts.
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
		"golangci-lint" "run_golangci_lint"
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

	local COVERAGE_FILE="$GO_COVERAGE_FILE"

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
		# ./cmd/... is NOT run here yet, and that is a known hole: the merge gate
		# does not test 24k lines of source / 39k lines of tests, which is how the
		# e33f7187 regression reached main. CI does cover it
		# (.github/workflows/ci.yml:73,137) but nothing in the merge path consults CI.
		#
		# Adding `go test ./cmd/...` to this lane was tried and reverted. cmd/oro
		# contains tests that INVOKE THIS SCRIPT (quality_gate_gen_test.go FIFO/lock
		# tests). Running them from inside a gate trips the nested-invocation guard
		# ("Nested quality gate invocation detected; using active parent result"),
		# and they fail — plus collateral failures in the same process. They pass
		# standalone, shuffled, and under -p 3; only nesting breaks them.
		#
		# Prerequisite before retrying: make cmd/oro's gate-invoking tests
		# nested-safe (skip when ORO_QG_ACTIVE_PID is set) or move them to
		# pkg/dispatcher/testdata/serial_lane_tests.txt.
		# Report the main-lane coverage for visibility. The ≥78% THRESHOLD is
		# enforced later, after the serial lane, on the MERGED profile — the
		# serial-lane tests self-skip here, so this partial number would understate
		# coverage and could fail the gate spuriously (enforce_go_coverage_threshold).
		local filtered="${COVERAGE_FILE}.filtered"
		grep -v -e 'oro/pkg/dashboard/' "$COVERAGE_FILE" >"$filtered"
		local cov
		cov=$(go tool cover -func="$filtered" | grep total | awk '{print $3}' | sed 's/%//')
		echo "Coverage (main lane, serial-lane tests excluded): ${cov}%"
	}

	# Scope build/vet/govulncheck to the real tracked module subtrees so they
	# never descend into archive/ — a gitignored, untracked tree of deliberately
	# broken Go fixtures that would fail the gate on cruft that can never merge.
	# (golangci-lint and the CGO-free build already scope explicitly.) The build
	# lane omits the repo root "." because it is a test-only package main
	# (readme_test.go); vet and govulncheck include "." to cover it.
	local go_build_pkgs="./cmd/... ./internal/... ./pkg/... ./tests/..."
	local go_analyze_pkgs="$go_build_pkgs ."

	local tier3_checks=(
		"go test + coverage" "go_test_with_coverage"
		"go build" "go build -buildvcs=false $go_build_pkgs"
		"go vet" "go vet $go_analyze_pkgs"
		"CGO-free build" "CGO_ENABLED=0 go build -buildvcs=false ./cmd/oro ./cmd/oro-search-hook"
	)
	tier3_checks+=("govulncheck" "go tool govulncheck $go_analyze_pkgs")
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
	if command -v uv >/dev/null 2>&1 || command -v pylint >/dev/null 2>&1; then
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
			"shfmt" "qg_run_shfmt_source" \
			"shellcheck" "qg_run_shellcheck_source"
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

# TEST-ONLY: phase-marker mode drives the lock/phase skeleton (main-phase
# concurrency + serialized lane) without the heavy real checks. Production never
# sets ORO_QG_PHASE_MARKER_DIR.
if [ -n "${ORO_QG_PHASE_MARKER_DIR:-}" ]; then
	run_phase_marker main "${ORO_QG_MAIN_SLEEP:-0}"
	run_serial_lane
	exit 0
fi

# Run ONLY the serialized timing lane (skip the main phase). Useful pre-merge and
# for proving the lane catches regressions; exits with the lane's own status.
if [ "${ORO_QG_SERIAL_LANE_ONLY:-}" = "1" ]; then
	run_serial_lane
	serial_fail=1
	if [ -f "$QG_DIR/serial.rc" ]; then
		IFS=: read -r _ serial_fail <"$QG_DIR/serial.rc"
	fi
	[ "${serial_fail:-1}" -gt 0 ] && exit 1
	exit 0
fi

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

# Serial timing lane: the guarded socket/timing tests, run under the FIFO lock so
# they are serialized across sibling worktree gates (oro-hwx2). The main phase
# above ran lockless and concurrent; this is the only serialized segment.
RC_FILES=("$QG_DIR"/go.rc "$QG_DIR"/python.rc "$QG_DIR"/other.rc)
if $HAS_GO; then
	run_serial_lane
	RC_FILES+=("$QG_DIR"/serial.rc)
	# Enforce the coverage threshold on the merged main+serial profile (the serial
	# lane's tests were excluded from the main lane's measurement).
	enforce_go_coverage_threshold
	RC_FILES+=("$QG_DIR"/coverage.rc)
fi

# Aggregate pass/fail counts
TOTAL_PASS=0
TOTAL_FAIL=0
for rc_file in "${RC_FILES[@]}"; do
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
