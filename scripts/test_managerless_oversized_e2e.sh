#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="${ORO_OVERSIZED_E2E_TMPDIR:-/tmp}"
TMP_DIR="$(mktemp -d "$TMP_ROOT/oro-oversized.XXXXXX")"
BIN_DIR="$TMP_DIR/bin"
ORO_BIN="$BIN_DIR/oro"
STATE_DB="$TMP_DIR/state.db"
TMUX_LOG="$TMP_DIR/tmux.log"
RUNTIME_LOG="$TMP_DIR/runtime.log"
DISPATCHER_PID=""
STEP_TIMEOUT_SECONDS="${ORO_OVERSIZED_E2E_STEP_TIMEOUT_SECONDS:-90}"
RUN_TIMEOUT_SECONDS="${ORO_OVERSIZED_E2E_RUN_TIMEOUT_SECONDS:-45}"

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
    print(f"oversized e2e failed: timeout after {timeout:g}s: {' '.join(cmd)}", file=sys.stderr)
    raise SystemExit(124)
PY
}

wait_for_pid_exit() {
	local pid="$1"
	local seconds="$2"
	local deadline=$((SECONDS + seconds))
	local stat=""

	while ((SECONDS < deadline)); do
		if ! kill -0 "$pid" 2>/dev/null; then
			wait "$pid" 2>/dev/null || true
			return 0
		fi
		stat="$(ps -o stat= -p "$pid" 2>/dev/null | tr -d '[:space:]' || true)"
		if [[ "$stat" == Z* ]]; then
			wait "$pid" 2>/dev/null || true
			return 0
		fi
		sleep 0.2
	done
	return 1
}

cleanup() {
	if [[ -n "$DISPATCHER_PID" ]] && kill -0 "$DISPATCHER_PID" 2>/dev/null; then
		ORO_HUMAN_CONFIRMED=1 "$ORO_BIN" dispatcher stop --force >/dev/null 2>&1 || true
		wait_for_pid_exit "$DISPATCHER_PID" 15 >/dev/null 2>&1 || true
	fi
	chmod -R u+w "$TMP_DIR" 2>/dev/null || true
	rm -rf "$TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$BIN_DIR" "$TMP_DIR/oro-home"

export ORO_HOME="$TMP_DIR/oro-home"
export ORO_DB_PATH="$STATE_DB"
export ORO_PID_PATH="$TMP_DIR/oro.pid"
export ORO_SOCKET_PATH="$TMP_DIR/oro.sock"
export ORO_BEADSOURCE_MODE="sqlite"
export ORO_AGENT_RUNTIME="codex"
export ORO_TEST_BIN="$ORO_BIN"
export ORO_TEST_RUNTIME_LOG="$RUNTIME_LOG"
export PATH="$BIN_DIR:$PATH"

cat >"$BIN_DIR/tmux" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${ORO_TEST_TMUX_LOG:?}"
exit 0
SH
chmod +x "$BIN_DIR/tmux"
export ORO_TEST_TMUX_LOG="$TMUX_LOG"

cat >"$BIN_DIR/codex" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

prompt="${*: -1}"
printf 'codex %s\n' "$*" >>"${ORO_TEST_RUNTIME_LOG:?}"

if [[ "$prompt" == *"task decomposition agent"* ]]; then
	parent="oro-oversized-e2e"
	child_a="oro-oversized-e2e-child-a"
	child_b="oro-oversized-e2e-child-b"
	ac_a="Test: pkg/dispatcher/managerless_oversized_e2e_test.go:TestChildA | Cmd: go test ./pkg/dispatcher -run TestChildA | Assert: child A passes"
	ac_b="Test: pkg/ops/managerless_oversized_e2e_test.go:TestChildB | Cmd: go test ./pkg/ops -run TestChildB | Assert: child B passes"

	"${ORO_TEST_BIN:?}" task update "$parent" --type epic >/dev/null
	"${ORO_TEST_BIN:?}" task create --id "$child_a" --title "Oversized e2e child A" --type task --parent "$parent" --acceptance "$ac_a" --estimate 5 >/dev/null
	"${ORO_TEST_BIN:?}" task create --id "$child_b" --title "Oversized e2e child B" --type task --parent "$parent" --acceptance "$ac_b" --estimate 5 >/dev/null
	"${ORO_TEST_BIN:?}" task dep add "$parent" "$child_a" --type blocks >/dev/null
	"${ORO_TEST_BIN:?}" task dep add "$parent" "$child_b" --type blocks >/dev/null
	printf 'VERDICT: resolved\n'
	exit 0
fi

printf 'deterministic oversized e2e worker noop\n'
exit 0
SH
chmod +x "$BIN_DIR/codex"
cp "$BIN_DIR/codex" "$BIN_DIR/claude"

cd "$ROOT"
run_with_timeout "$STEP_TIMEOUT_SECONDS" go build -o "$ORO_BIN" ./cmd/oro

oversized_ac=$'Test: scripts/test_managerless_oversized_e2e.sh | Cmd: ./scripts/test_managerless_oversized_e2e.sh | Assert: oversized task decomposes without tmux\nRead: pkg/dispatcher/dispatcher.go:checkBeadReady\nRead: pkg/ops/decompose_prompt.go:buildDecomposePrompt\nRead: pkg/protocol/types.go:CountDistinctModules'
"$ORO_BIN" task create \
	--id oro-oversized-e2e \
	--title "Managerless oversized e2e fixture" \
	--priority 0 \
	--type task \
	--acceptance "$oversized_ac" >/dev/null

"$ORO_BIN" start --daemon-only \
	--workers 1 \
	--max-workers 1 \
	--progress-timeout 20s \
	--ops-review-timeout 20s \
	--review-stall-timeout 20s >"$TMP_DIR/dispatcher.log" 2>&1 &
DISPATCHER_PID=$!

started=""
for _ in {1..60}; do
	if "$ORO_BIN" directive start >/dev/null 2>&1; then
		started="1"
		break
	fi
	sleep 0.25
done
if [[ -z "$started" ]]; then
	printf 'oversized e2e failed: dispatcher did not accept start directive\n' >&2
	cat "$TMP_DIR/dispatcher.log" >&2
	exit 1
fi

python3 - "$STATE_DB" "$TMUX_LOG" "$RUN_TIMEOUT_SECONDS" <<'PY'
import json
import sqlite3
import sys
import time
from pathlib import Path

db_path = sys.argv[1]
tmux_log = Path(sys.argv[2])
deadline = time.time() + float(sys.argv[3])
parent_id = "oro-oversized-e2e"
last = {}


def fetch_snapshot():
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cur = conn.cursor()
    parent = cur.execute(
        "SELECT id, type, status FROM beads WHERE id=? AND deleted=0",
        (parent_id,),
    ).fetchone()
    children = cur.execute(
        """
        SELECT id, acceptance_criteria
        FROM beads
        WHERE parent_id=? AND deleted=0
        ORDER BY id
        """,
        (parent_id,),
    ).fetchall()
    deps = cur.execute(
        """
        SELECT depends_on_id
        FROM bead_deps
        WHERE bead_id=? AND type IN ('blocks', 'conditional-blocks')
        ORDER BY depends_on_id
        """,
        (parent_id,),
    ).fetchall()
    ops_runs = cur.execute(
        """
        SELECT id, type, bead_id, status, verdict, error
        FROM ops_runs
        WHERE bead_id=?
        ORDER BY id
        """,
        (parent_id,),
    ).fetchall()
    pending_oversized = cur.execute(
        """
        SELECT COUNT(*)
        FROM escalations
        WHERE bead_id=? AND type='OVERSIZED_BEAD' AND status='pending'
        """,
        (parent_id,),
    ).fetchone()[0]
    conn.close()
    return {
        "parent": dict(parent) if parent else None,
        "children": [dict(row) for row in children],
        "deps": [row["depends_on_id"] for row in deps],
        "ops_runs": [dict(row) for row in ops_runs],
        "pending_oversized": pending_oversized,
    }


while time.time() < deadline:
    try:
        last = fetch_snapshot()
    except sqlite3.Error:
        time.sleep(0.25)
        continue

    parent = last["parent"] or {}
    child_ids = {row["id"] for row in last["children"]}
    child_ac_ok = all(
        all(marker in row["acceptance_criteria"] for marker in ("Test:", "Cmd:", "Assert:"))
        for row in last["children"]
    )
    resolved_ops = [
        row
        for row in last["ops_runs"]
        if row["type"] == "decompose"
        and row["status"] == "resolved"
        and row["verdict"] == "resolved"
    ]

    if (
        parent.get("type") == "epic"
        and child_ids == {"oro-oversized-e2e-child-a", "oro-oversized-e2e-child-b"}
        and child_ac_ok
        and set(last["deps"]) == child_ids
        and len(resolved_ops) == 1
        and last["pending_oversized"] == 0
    ):
        tmux_text = tmux_log.read_text() if tmux_log.exists() else ""
        forbidden = ("send-keys", "load-buffer", "paste-buffer")
        if any(token in tmux_text for token in forbidden):
            print("oversized e2e failed: tmux manager paste attempt observed", file=sys.stderr)
            print(tmux_text, file=sys.stderr)
            raise SystemExit(1)
        print("managerless oversized e2e passed")
        raise SystemExit(0)

    time.sleep(0.25)

print("oversized e2e failed: acceptance conditions not met", file=sys.stderr)
print(json.dumps(last, indent=2, sort_keys=True), file=sys.stderr)
if tmux_log.exists():
    print("tmux log:", file=sys.stderr)
    print(tmux_log.read_text(), file=sys.stderr)
raise SystemExit(1)
PY
