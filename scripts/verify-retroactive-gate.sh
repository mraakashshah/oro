#!/usr/bin/env bash
# verify-retroactive-gate.sh — §11.4 retroactive premortem gate end-to-end smoke test.
#
# Drives the actual oro CLI to exercise the §18.6 verify-premortem-gate flow:
#   1. create an epic
#   2. batch 6 children → assert gate_state='eligible' after the 6th
#   3. assert oro work --auto on a child is refused with kind=premortem_required
#   4. assert a premortem bead is auto-spawned with parent_id=$EPIC
#   5. close it with verdict='proceed' → assert gate_state='satisfied'
#   6. assert oro work --auto (--dry-run) on a child is now accepted
#
# Uses ORO_DB_PATH to point at an isolated temp DB so it does not touch the
# project's bead store. Exits non-zero on any assertion mismatch.
#
# Usage: scripts/verify-retroactive-gate.sh

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

# Build the oro binary (needs `make build` so embedded assets are staged).
echo "==> Building oro binary"
make -C "$repo_root" build >/dev/null

ORO_BIN="$repo_root/oro"
if [[ ! -x "$ORO_BIN" ]]; then
  echo "FATAL: oro binary not found at $ORO_BIN after make build" >&2
  exit 2
fi

# Hermetic temp DB so this script does not touch the live bead store.
TMP_DIR="$(mktemp -d -t orogate-verify-XXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT
export ORO_DB_PATH="$TMP_DIR/state.db"
export ORO_PID_PATH="$TMP_DIR/oro.pid"
export ORO_SOCKET_PATH="$TMP_DIR/oro.sock"
export ORO_PROJECT=""

# find_first_bead <type> [<parent_id>] — print the ID of the first bead matching
# type (and optional parent_id), or empty if none. Reads `oro bead list --json`.
find_first_bead() {
  local want_type="$1"
  local want_parent="${2:-}"
  python3 -c '
import json, sys
beads = json.load(sys.stdin)
want_type, want_parent = sys.argv[1], sys.argv[2] or None
for b in beads:
    if b.get("type") != want_type:
        continue
    if want_parent and b.get("parent_id") != want_parent:
        continue
    print(b["id"])
    break
' "$want_type" "$want_parent"
}

assert_eq() {
  local label="$1"
  local got="$2"
  local want="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label = '$got', want '$want'" >&2
    exit 1
  fi
  echo "PASS: $label = '$got'"
}

# ------------------------------------------------------------------
# 1. Create epic.
# ------------------------------------------------------------------
echo "==> Step 1: create epic"
EPIC="$("$ORO_BIN" bead create --title="Verify epic" --type=epic --acceptance-criteria="n/a")"
echo "    EPIC=$EPIC"

# ------------------------------------------------------------------
# 2. Batch 6 children — gate must trip on the 6th.
# ------------------------------------------------------------------
echo "==> Step 2: batch 6 children under epic"
for i in 1 2 3 4 5 6; do
  "$ORO_BIN" bead create \
    --title="child-$i" --type=task --parent="$EPIC" \
    --acceptance-criteria="Test: ok | Cmd: true" >/dev/null
done

GATE="$("$ORO_BIN" bead gate-state "$EPIC" | tr -d '[:space:]')"
assert_eq "gate_state after 6th child" "$GATE" "eligible"

# ------------------------------------------------------------------
# 3. oro work --auto on a child must refuse with kind=premortem_required.
# ------------------------------------------------------------------
echo "==> Step 3: oro work --auto on a child must be refused"
CHILD="$("$ORO_BIN" bead list --json | find_first_bead task "$EPIC")"
if [[ -z "$CHILD" ]]; then
  echo "FAIL: no task child of $EPIC found in bead list" >&2
  exit 1
fi
WORK_LOG="$TMP_DIR/work-refused.log"
set +e
"$ORO_BIN" work --auto --dry-run "$CHILD" >"$WORK_LOG" 2>&1
WORK_RC=$?
set -e
if [[ $WORK_RC -eq 0 ]]; then
  echo "FAIL: oro work --auto $CHILD exited 0; expected non-zero refusal" >&2
  cat "$WORK_LOG" >&2
  exit 1
fi
if ! grep -q "premortem_required" "$WORK_LOG"; then
  echo "FAIL: oro work --auto $CHILD did not surface kind=premortem_required" >&2
  cat "$WORK_LOG" >&2
  exit 1
fi
echo "PASS: oro work --auto refused (rc=$WORK_RC, kind=premortem_required)"

# ------------------------------------------------------------------
# 4. A premortem bead must be auto-spawned with parent_id=$EPIC.
# ------------------------------------------------------------------
echo "==> Step 4: premortem bead must be auto-spawned"
PM="$("$ORO_BIN" bead list --json | find_first_bead premortem "$EPIC")"
if [[ -z "$PM" ]]; then
  echo "FAIL: no auto-spawned premortem with parent_id=$EPIC" >&2
  "$ORO_BIN" bead list --json >&2
  exit 1
fi
echo "PASS: auto-spawned premortem PM=$PM (parent_id=$EPIC)"

# ------------------------------------------------------------------
# 5. Close premortem with verdict='proceed' → gate_state='satisfied'.
# ------------------------------------------------------------------
echo "==> Step 5: close premortem with verdict=proceed"
"$ORO_BIN" bead premortem-close "$PM" --verdict=proceed --reason="all clear" >/dev/null
GATE="$("$ORO_BIN" bead gate-state "$EPIC" | tr -d '[:space:]')"
assert_eq "gate_state after verdict=proceed" "$GATE" "satisfied"

# ------------------------------------------------------------------
# 6. oro work --auto on the same child must now be accepted.
# ------------------------------------------------------------------
echo "==> Step 6: oro work --auto must now be accepted"
WORK_LOG="$TMP_DIR/work-accepted.log"
set +e
"$ORO_BIN" work --auto --dry-run "$CHILD" >"$WORK_LOG" 2>&1
WORK_RC=$?
set -e
if [[ $WORK_RC -ne 0 ]]; then
  echo "FAIL: oro work --auto --dry-run $CHILD exited $WORK_RC; expected 0" >&2
  cat "$WORK_LOG" >&2
  exit 1
fi
if grep -q "premortem_required" "$WORK_LOG"; then
  echo "FAIL: oro work --auto $CHILD still cites premortem_required after satisfied gate" >&2
  cat "$WORK_LOG" >&2
  exit 1
fi
echo "PASS: oro work --auto accepted (rc=0)"

echo ""
echo "==> §11.4 retroactive premortem gate end-to-end: ALL CHECKS PASSED"
