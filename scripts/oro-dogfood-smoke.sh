#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ITERATIONS=3
WORKERS=2
INTERVAL="1s"
STATE_DIR=""
ORO_BIN_ARG=""
SCENARIO="default"
STEP_TIMEOUT_SECONDS="${ORO_DOGFOOD_STEP_TIMEOUT_SECONDS:-90}"
RUN_TIMEOUT_SECONDS="${ORO_DOGFOOD_RUN_TIMEOUT_SECONDS:-180}"
STOP_TIMEOUT_SECONDS="${ORO_DOGFOOD_STOP_TIMEOUT_SECONDS:-30}"

usage() {
	cat <<'USAGE'
usage: scripts/oro-dogfood-smoke.sh [flags]

Flags:
  --iterations N      finite monitor iterations (default: 3)
  --workers N         worker target and max-workers value (default: 2)
  --interval DURATION monitor interval, e.g. 500ms or 2s (default: 1s)
  --state-dir PATH    isolated state/artifact directory (default: mktemp)
  --oro-bin PATH      existing oro binary to copy into the isolated state dir
  --scenario NAME     default or reliability-v2 (default: default)
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--iterations)
		ITERATIONS="$2"
		shift 2
		;;
	--workers)
		WORKERS="$2"
		shift 2
		;;
	--interval)
		INTERVAL="$2"
		shift 2
		;;
	--state-dir)
		STATE_DIR="$2"
		shift 2
		;;
	--oro-bin)
		ORO_BIN_ARG="$2"
		shift 2
		;;
	--scenario)
		SCENARIO="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf 'unknown flag: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
done

case "$SCENARIO" in
default | reliability-v2) ;;
*)
	printf 'unknown scenario: %s\n' "$SCENARIO" >&2
	exit 2
	;;
esac

if [[ -z "$STATE_DIR" ]]; then
	STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oro-dogfood-smoke.XXXXXX")"
fi
mkdir -p "$STATE_DIR/bin" "$STATE_DIR/logs"
STATE_DIR="$(cd "$STATE_DIR" && pwd)"

PROJECT="dogfood-smoke-$$"
REPO_DIR="$STATE_DIR/repo"
BIN_DIR="$STATE_DIR/bin"
ORO_BIN="$BIN_DIR/oro"
STATE_DB="$STATE_DIR/state.db"
DISPATCHER_LOG="$STATE_DIR/logs/dispatcher.log"
MONITOR_LOG="$STATE_DIR/logs/monitor.log"
ASSERT_LOG="$STATE_DIR/logs/assert.log"
DISPATCHER_PID=""
STOPPED=0

export ORO_HOME="$STATE_DIR/oro-home"
export ORO_PROJECT="$PROJECT"
export ORO_DB_PATH="$STATE_DB"
export ORO_PID_PATH="$STATE_DIR/oro.pid"
export ORO_SOCKET_PATH="$STATE_DIR/oro.sock"
export ORO_AGENT_RUNTIME="codex"
export ORO_DAEMON_SKIP_PREFLIGHT=1
export CODEX_HOME="$STATE_DIR/codex-home"
export PATH="$BIN_DIR:$PATH"
mkdir -p "$ORO_HOME/hooks" "$CODEX_HOME"
cat >"$ORO_HOME/hooks/oro-search-hook" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
exit 0
SH
chmod +x "$ORO_HOME/hooks/oro-search-hook"

run_with_timeout() {
	local seconds="$1"
	shift
	python3 - "$seconds" "$@" <<'PY'
import subprocess
import sys

timeout = float(sys.argv[1])
cmd = sys.argv[2:]
try:
    raise SystemExit(subprocess.run(cmd, timeout=timeout).returncode)
except subprocess.TimeoutExpired:
    print(f"dogfood failed: timeout after {timeout:g}s: {' '.join(cmd)}", file=sys.stderr)
    raise SystemExit(124)
PY
}

dump_detail() {
	local rc="${1:-1}"
	printf '\n--- dogfood detail (exit %s) ---\n' "$rc" >&2
	printf 'state_dir=%s\nrepo_dir=%s\noro_bin=%s\nstate_db=%s\n' "$STATE_DIR" "$REPO_DIR" "$ORO_BIN" "$STATE_DB" >&2
	if [[ -x "$ORO_BIN" && -d "$REPO_DIR" ]]; then
		(
			cd "$REPO_DIR" 2>/dev/null || exit 0
			printf '\n[oro status --json]\n' >&2
			"$ORO_BIN" status --json >&2 || true
			printf '\n[oro health --json]\n' >&2
			"$ORO_BIN" health --json >&2 || true
		)
	fi
	if [[ -f "$STATE_DB" ]]; then
		printf '\n[state invariant counts]\n' >&2
		python3 - "$STATE_DB" >&2 <<'PY' || true
import sqlite3
import sys

db = sys.argv[1]
conn = sqlite3.connect(db)
queries = {
    "active_assignments": "SELECT COUNT(*) FROM assignments WHERE status='active'",
    "open_recovery_quarantines": "SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'",
    "open_qg_incidents": "SELECT COUNT(*) FROM qg_failure_incidents WHERE status='open'",
    "failed_or_stale_ops_runs": "SELECT COUNT(*) FROM ops_runs WHERE status IN ('failed','stale')",
    "open_seeded_work": "SELECT COUNT(*) FROM beads WHERE id LIKE 'oro-dogfood-%' AND status != 'closed'",
}
for name, query in queries.items():
    try:
        value = conn.execute(query).fetchone()[0]
    except sqlite3.Error as exc:
        value = f"error: {exc}"
    print(f"{name}={value}")
conn.close()
PY
	fi
	for log in "$DISPATCHER_LOG" "$MONITOR_LOG" "$ASSERT_LOG"; do
		if [[ -f "$log" ]]; then
			printf '\n[%s tail]\n' "$log" >&2
			tail -80 "$log" >&2 || true
		fi
	done
}

stop_factory() {
	if [[ "$STOPPED" = "1" || ! -x "$ORO_BIN" || ! -d "$REPO_DIR" ]]; then
		return 0
	fi
	STOPPED=1
	if [[ -n "$DISPATCHER_PID" ]]; then
		kill -INT "$DISPATCHER_PID" 2>/dev/null || true
		for _ in $(seq 1 "$((STOP_TIMEOUT_SECONDS * 2))"); do
			if ! kill -0 "$DISPATCHER_PID" 2>/dev/null; then
				return 0
			fi
			sleep 0.5
		done
		kill -TERM "$DISPATCHER_PID" 2>/dev/null || true
	fi
}

on_exit() {
	local rc=$?
	if [[ "$rc" -ne 0 ]]; then
		dump_detail "$rc"
	fi
	stop_factory
	printf 'dogfood artifacts:\n  state_dir=%s\n  repo_dir=%s\n  oro_bin=%s\n  state_db=%s\n  dispatcher_log=%s\n  monitor_log=%s\n  assert_log=%s\n' \
		"$STATE_DIR" "$REPO_DIR" "$ORO_BIN" "$STATE_DB" "$DISPATCHER_LOG" "$MONITOR_LOG" "$ASSERT_LOG"
	exit "$rc"
}
trap on_exit EXIT

if [[ -n "$ORO_BIN_ARG" ]]; then
	cp "$ORO_BIN_ARG" "$ORO_BIN"
	chmod +x "$ORO_BIN"
else
	(
		cd "$ROOT"
		run_with_timeout "$STEP_TIMEOUT_SECONDS" go build -o "$ORO_BIN" ./cmd/oro
	)
fi

cat >"$BIN_DIR/codex" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

prompt="${*: -1}"
prompt_lc="$(printf '%s' "$prompt" | tr '[:upper:]' '[:lower:]')"
if [[ "$prompt_lc" == *"verdict: approved"* || "$prompt_lc" == *"code review"* || "$prompt_lc" == *"review"* ]]; then
	printf 'deterministic dogfood review approved\n'
	printf 'VERDICT: APPROVED\n'
	exit 0
fi

bead="${ORO_WORKER_BEAD_ID:-unknown}"
printf 'deterministic dogfood worker completed %s without source changes\n' "$bead"
SH
chmod +x "$BIN_DIR/codex"
cp "$BIN_DIR/codex" "$BIN_DIR/claude"

mkdir -p "$REPO_DIR/.oro" "$REPO_DIR/scripts"
git -C "$REPO_DIR" init --quiet
git -C "$REPO_DIR" checkout -B dogfood-main >/dev/null
git -C "$REPO_DIR" config gc.auto 0
git -C "$REPO_DIR" config user.name "Oro Dogfood"
git -C "$REPO_DIR" config user.email "oro-dogfood@example.invalid"
cat >"$REPO_DIR/.oro/config.yaml" <<EOF
project: $PROJECT
agent:
  provider_mode: codex-only
EOF
printf 'dogfood smoke repo\n' >"$REPO_DIR/README.md"
cat >"$REPO_DIR/scripts/quality_gate.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
git status --short >/dev/null
printf 'dogfood quality gate pass\n'
SH
chmod +x "$REPO_DIR/scripts/quality_gate.sh"
git -C "$REPO_DIR" add .oro/config.yaml README.md scripts/quality_gate.sh
git -C "$REPO_DIR" commit -m "dogfood: use bounded quality gate" >/dev/null
git -C "$REPO_DIR" branch -f dogfood-target dogfood-main

cd "$REPO_DIR"

run_with_timeout "$STEP_TIMEOUT_SECONDS" "$ORO_BIN" harness dogfood seed --scenario "$SCENARIO"

DISPATCHER_PID="$(
	python3 - "$DISPATCHER_LOG" "$ORO_BIN" "$WORKERS" "$RUN_TIMEOUT_SECONDS" <<'PY'
import subprocess
import sys

log_path, oro_bin, workers, run_timeout = sys.argv[1:]
log = open(log_path, "ab", buffering=0)
cmd = [
    oro_bin,
    "start",
    "--daemon-only",
    "--workers",
    workers,
    "--max-workers",
    workers,
    "--base-branch",
    "dogfood-main",
    "--progress-timeout",
    f"{run_timeout}s",
    "--ops-review-timeout",
    f"{run_timeout}s",
    "--review-stall-timeout",
    f"{run_timeout}s",
]
proc = subprocess.Popen(cmd, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
print(proc.pid)
PY
)"

started=""
for _ in {1..60}; do
	if "$ORO_BIN" directive start >/dev/null 2>&1; then
		started="1"
		break
	fi
	sleep 0.5
done
if [[ -z "$started" ]]; then
	printf 'dogfood failed: dispatcher did not accept start directive\n' >&2
	exit 1
fi

run_with_timeout "$RUN_TIMEOUT_SECONDS" "$ORO_BIN" harness dogfood run \
	--scenario "$SCENARIO" \
	--iterations "$ITERATIONS" \
	--workers "$WORKERS" \
	--interval "$INTERVAL" | tee "$MONITOR_LOG"

python3 - "$STATE_DB" "$SCENARIO" "$RUN_TIMEOUT_SECONDS" <<'PY'
import sqlite3
import sys
import time

db_path, scenario, timeout = sys.argv[1], sys.argv[2], float(sys.argv[3])
ids = ["oro-dogfood-target-cleanup"] if scenario == "reliability-v2" else ["oro-dogfood-noop-merge"]
deadline = time.time() + timeout
last = None

while time.time() < deadline:
    conn = sqlite3.connect(db_path)
    rows = dict(conn.execute(
        "SELECT id, status FROM beads WHERE id IN (%s)" % ",".join("?" for _ in ids),
        ids,
    ).fetchall())
    events = dict(conn.execute(
        "SELECT type || ':' || COALESCE(bead_id,''), COUNT(*) FROM events GROUP BY type, bead_id"
    ).fetchall())
    blocking_ops = conn.execute(
        "SELECT COUNT(*) FROM ops_runs WHERE status IN ('running','failed','stale')"
    ).fetchone()[0]
    conn.close()
    last = {"rows": rows, "events": events, "blocking_ops": blocking_ops}
    if all(rows.get(bead_id) == "closed" for bead_id in ids):
        if scenario != "reliability-v2" or (
            events.get("merge_noop:oro-dogfood-target-cleanup", 0) > 0
        ):
            if blocking_ops == 0:
                raise SystemExit(0)
    time.sleep(1)

print(f"dogfood failed: seeded work did not close before timeout: {last}", file=sys.stderr)
raise SystemExit(1)
PY

run_with_timeout "$STEP_TIMEOUT_SECONDS" "$ORO_BIN" harness dogfood assert --scenario "$SCENARIO" | tee "$ASSERT_LOG"
