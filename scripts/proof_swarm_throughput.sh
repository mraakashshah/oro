#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oro-throughput-proof.XXXXXX")"
PROJECT="throughput-proof-$$"
STATE_DB="$TMP_DIR/state.db"
PROOF_REPO="$TMP_DIR/repo"
BIN_DIR="$TMP_DIR/bin"
ORO_BIN="$BIN_DIR/oro"
DISPATCHER_PID=""
STEP_TIMEOUT_SECONDS="${ORO_PROOF_STEP_TIMEOUT_SECONDS:-90}"
RUN_TIMEOUT_SECONDS="${ORO_PROOF_RUN_TIMEOUT_SECONDS:-180}"
STOP_TIMEOUT_SECONDS="${ORO_PROOF_STOP_TIMEOUT_SECONDS:-30}"

cleanup() {
  if [[ -n "$DISPATCHER_PID" ]] && kill -0 "$DISPATCHER_PID" 2>/dev/null; then
    (
      cd "$PROOF_REPO" 2>/dev/null || exit 0
      ORO_HUMAN_CONFIRMED=1 run_with_timeout "$STOP_TIMEOUT_SECONDS" "$ORO_BIN" stop --force >/dev/null 2>&1 || true
    )
    wait_for_pid_exit "$DISPATCHER_PID" "$STOP_TIMEOUT_SECONDS" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$TMP_DIR" 2>/dev/null || true
  rm -rf "$TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$BIN_DIR"

export ORO_HOME="$TMP_DIR/oro-home"
export ORO_PROJECT="$PROJECT"
export ORO_DB_PATH="$STATE_DB"
export ORO_PID_PATH="$TMP_DIR/oro.pid"
export ORO_SOCKET_PATH="$TMP_DIR/oro.sock"
export ORO_AGENT_RUNTIME="codex"
export PATH="$BIN_DIR:$PATH"
mkdir -p "$ORO_HOME"

cd "$ROOT"

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
    print(f"proof failed: timeout after {timeout:g}s: {' '.join(cmd)}", file=sys.stderr)
    raise SystemExit(124)
PY
}

capture_with_timeout() {
  local seconds="$1"
  shift
  python3 - "$seconds" "$@" <<'PY'
import subprocess
import sys

timeout = float(sys.argv[1])
cmd = sys.argv[2:]
try:
    result = subprocess.run(cmd, timeout=timeout, text=True, stdout=subprocess.PIPE)
except subprocess.TimeoutExpired:
    print(f"proof failed: timeout after {timeout:g}s: {' '.join(cmd)}", file=sys.stderr)
    raise SystemExit(124)
sys.stdout.write(result.stdout)
raise SystemExit(result.returncode)
PY
}

extract_ps_pid() {
  local line="$1"
  local pid=""
  read -r pid _ <<<"$line"
  printf '%s' "$pid"
}

wait_for_pid_exit() {
  local pid="$1"
  local seconds="$2"
  local deadline=$((SECONDS + seconds))
  local stat=""

  while (( SECONDS < deadline )); do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" 2>/dev/null || true
      return 0
    fi
    stat="$(ps -o stat= -p "$pid" 2>/dev/null | tr -d '[:space:]' || true)"
    if [[ "$stat" == Z* ]]; then
      wait "$pid" 2>/dev/null || true
      return 0
    fi
    sleep 0.5
  done
  return 1
}

if [[ "$(extract_ps_pid '  123 go test ./cmd/oro')" != "123" ]]; then
  printf 'proof failed: residual PID parser self-check failed\n' >&2
  exit 1
fi

cat >"$BIN_DIR/codex" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

prompt="${*: -1}"
if [[ "$prompt" == *"VERDICT: APPROVED"* || "$prompt" == *"code review"* || "$prompt" == *"review"* ]]; then
  printf 'deterministic proof ops review approved\n'
  printf 'VERDICT: APPROVED\n'
  exit 0
fi

bead="${ORO_WORKER_BEAD_ID:-unknown}"
mkdir -p proof
printf 'completed %s\n' "$bead" >"proof/${bead}.txt"
git config user.name "Oro Proof Worker"
git config user.email "oro-proof@example.invalid"
git add "proof/${bead}.txt"
git commit -m "proof: complete ${bead}" >/dev/null
printf 'deterministic proof worker completed %s\n' "$bead"
SH
chmod +x "$BIN_DIR/codex"

run_with_timeout "$STEP_TIMEOUT_SECONDS" make stage-assets >/dev/null
run_with_timeout "$STEP_TIMEOUT_SECONDS" go build -o "$ORO_BIN" ./cmd/oro
run_with_timeout "$STEP_TIMEOUT_SECONDS" git clone --quiet --local "$ROOT" "$PROOF_REPO"
git -C "$PROOF_REPO" config gc.auto 0

cd "$PROOF_REPO"
run_with_timeout "$STEP_TIMEOUT_SECONDS" git checkout -B proof-main >/dev/null
git config user.name "Oro Proof"
git config user.email "oro-proof@example.invalid"
git config gc.auto 0
cat >scripts/quality_gate.sh <<'SH'
#!/usr/bin/env bash
set -euo pipefail
git status --short >/dev/null
printf 'proof quality gate pass\n'
SH
chmod +x scripts/quality_gate.sh
git add scripts/quality_gate.sh
git commit -m "proof: use bounded quality gate" >/dev/null

for bead in oro-proof-a oro-proof-b oro-proof-c; do
  run_with_timeout "$STEP_TIMEOUT_SECONDS" "$ORO_BIN" task create \
    --id "$bead" \
    --title "Proof task $bead" \
    --priority 0 \
    --tier fast \
    --acceptance "Test: proof/${bead}.txt | Cmd: test -f proof/${bead}.txt | Assert: file exists" >/dev/null
done

"$ORO_BIN" start --daemon-only \
  --workers 3 \
  --max-workers 3 \
  --base-branch proof-main \
  --progress-timeout 45s \
  --ops-review-timeout 45s \
  --review-stall-timeout 45s >"$TMP_DIR/dispatcher.log" 2>&1 &
DISPATCHER_PID=$!

started=""
for _ in {1..60}; do
  if "$ORO_BIN" directive start >/dev/null 2>&1; then
    started="1"
    break
  fi
  sleep 0.5
done
if [[ -z "$started" ]]; then
  printf 'proof failed: dispatcher did not accept start directive\n' >&2
  cat "$TMP_DIR/dispatcher.log" >&2
  exit 1
fi

python3 - "$STATE_DB" "$RUN_TIMEOUT_SECONDS" <<'PY'
import sqlite3
import sys
import time

db_path = sys.argv[1]
deadline = time.time() + float(sys.argv[2])
want = {"oro-proof-a", "oro-proof-b", "oro-proof-c"}
last = {}

while time.time() < deadline:
    try:
        conn = sqlite3.connect(db_path)
        cur = conn.cursor()
        rows = cur.execute(
            "SELECT id, status, close_reason FROM beads WHERE id IN (?,?,?)",
            tuple(sorted(want)),
        ).fetchall()
        event_counts = dict(cur.execute(
            "SELECT type, COUNT(*) FROM events GROUP BY type"
        ).fetchall())
        distinct_workers = cur.execute(
            "SELECT COUNT(DISTINCT worker_id) FROM assignments WHERE bead_id IN (?,?,?)",
            tuple(sorted(want)),
        ).fetchone()[0]
        conn.close()
    except sqlite3.Error:
        time.sleep(0.5)
        continue

    last = {
        "rows": rows,
        "event_counts": event_counts,
        "distinct_workers": distinct_workers,
    }
    closed = {row[0] for row in rows if row[1] == "closed" and "Merged:" in (row[2] or "")}
    if closed == want and event_counts.get("review_approved", 0) >= 3 and event_counts.get("merged", 0) >= 3 and distinct_workers >= 3:
        raise SystemExit(0)
    time.sleep(1)

print(f"proof failed: timed out waiting for three-worker merge proof; last={last}", file=sys.stderr)
raise SystemExit(1)
PY

REPORT="$(capture_with_timeout "$STEP_TIMEOUT_SECONDS" "$ORO_BIN" throughput \
  --window 1h \
  --assert \
  --min-productive-per-assignment 0.25 \
  --max-qg-rejections-per-assignment 0.10 \
  --max-review-rejections-per-assignment 0.10 \
  --max-progress-timeouts-per-assignment 0)"

if ! grep -q "progress_timeouts=0" <<<"$REPORT"; then
  printf 'proof failed: expected zero progress timeouts\n%s\n' "$REPORT" >&2
  exit 1
fi
if grep -q "productive=0" <<<"$REPORT"; then
  printf 'proof failed: expected productive closures\n%s\n' "$REPORT" >&2
  exit 1
fi

ORO_HUMAN_CONFIRMED=1 run_with_timeout "$STOP_TIMEOUT_SECONDS" "$ORO_BIN" stop --force >/dev/null
DISPATCHER_WAIT_TIMED_OUT=""
if ! wait_for_pid_exit "$DISPATCHER_PID" "$STOP_TIMEOUT_SECONDS"; then
  DISPATCHER_WAIT_TIMED_OUT="1"
else
  DISPATCHER_PID=""
fi

RESIDUALS=""
while IFS= read -r candidate; do
  pid="$(extract_ps_pid "$candidate")"
  if [[ -z "$pid" ]]; then
    continue
  fi
  detail="$(ps eww -p "$pid" -o command= 2>/dev/null || true)"
  if [[ "$detail" == *"$PROJECT"* || "$detail" == *"$TMP_DIR"* ]]; then
    RESIDUALS+="${candidate}"$'\n'
  fi
done < <(ps -axo pid=,command= | awk '/oro start|oro dispatcher|oro worker|quality_gate\.sh|go test|ops-review|codex exec/ {print}')
if [[ -n "$RESIDUALS" ]]; then
  printf 'proof failed: residual Oro-owned proof processes remain:\n%s\n' "$RESIDUALS" >&2
  exit 1
fi
if [[ -n "$DISPATCHER_WAIT_TIMED_OUT" ]]; then
  printf 'proof failed: dispatcher did not exit within %ss after stop --force\n' "$STOP_TIMEOUT_SECONDS" >&2
  exit 1
fi
DISPATCHER_PID=""

printf '%s\n' "$REPORT"
python3 - "$STATE_DB" <<'PY'
import sqlite3
import sys

conn = sqlite3.connect(sys.argv[1])
cur = conn.cursor()
assignments = cur.execute("SELECT COUNT(*), COUNT(DISTINCT worker_id) FROM assignments").fetchone()
events = dict(cur.execute("SELECT type, COUNT(*) FROM events GROUP BY type").fetchall())
conn.close()
print(
    "proof_swarm_throughput: PASS "
    f"assignments={assignments[0]} distinct_workers={assignments[1]} "
    f"ready_for_review={events.get('ready_for_review', 0)} "
    f"review_approved={events.get('review_approved', 0)} "
    f"merged={events.get('merged', 0)} "
    f"project={sys.argv[1]}"
)
PY
