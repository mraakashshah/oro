# Native Beadstore Cutover Operator Log - 2026-04-30

This log records the native-first Phase 8 validation evidence after the operator
decision that bd/Dolt is import source, audit trail, and rollback reference, not
a parity veto.

## Source and Backups

- Target SQLite DB: `/Users/as21/.oro/projects/oro/state.db`
- Migration JSONL backup: `/Users/as21/.oro/migrations/20260430T161222.107490000Z-pre-migration.jsonl`
- Latest reconcile SQLite snapshot before this gate: `/Users/as21/.oro/projects/oro/migrations/20260430T192237Z-pre-invariant-reconcile-state-db`
- Latest reconcile JSONL backup before this gate: `/Users/as21/.oro/migrations/20260430T192242.373978000Z-pre-reconcile-sqlite.jsonl`
- Gate binary: `/tmp/oro-native-cutover`
- Full shell transcript: `/tmp/oro-native-gate-20260430T192810Z.log`

## Validation Gate

The gate used:

```bash
state_db=/Users/as21/.oro/projects/oro/state.db
oro_bin=/tmp/oro-native-cutover
representative_id=oro-ect4.5
export ORO_DB_PATH="$state_db"

scripts/check-phase8-no-writers.py
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead status
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead ready --json > /tmp/oro-native-ready.json
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead blocked --json > /tmp/oro-native-blocked.json
jq -e 'type == "array"' /tmp/oro-native-ready.json
jq -e 'type == "array"' /tmp/oro-native-blocked.json
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead show "$representative_id" --json |
  jq -e --arg id "$representative_id" '.id == $id and (.status | type == "string")'
scripts/check-native-beadstore-invariants.py --db "$state_db"
sqlite3 "$state_db" 'PRAGMA integrity_check;'
sqlite3 "$state_db" 'SELECT COUNT(*) FROM beads WHERE deleted = 0;'
```

Observed output:

```text
active_writer_count=0
open    99
in_progress    2
closed  1626
true
true
true
integrity_check=ok
legacy_foreign_key_violations=27
invalid_status_rows=0
ready_view_mismatches=0
blocked_view_mismatches=0
ready_blocked_overlap=0
active_assignment_in_ready_or_blocked=0
ready_with_unclosed_hard_blocker=0
ok
1727
```

The `legacy_foreign_key_violations=27` value is an accepted legacy import
condition from the migration run. The invariant gate treats it as informational
and fails closed on native ready/blocked/status/assignment mismatches.

## Native Smoke Write

The controlled smoke bead was `native-cutover-smoke-20260430T192811Z`.

The gate used:

```bash
scripts/check-phase8-no-writers.py
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead create --id "$test_id" --title "Native cutover smoke" --type task --priority 4
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead show "$test_id" --json | jq -e --arg id "$test_id" '.id == $id'
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead close "$test_id" --reason "Native cutover smoke passed"
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" bead show "$test_id" --json | jq -e '.status == "closed"'
scripts/check-native-beadstore-invariants.py --db "$state_db"
sqlite3 "$state_db" 'PRAGMA integrity_check;'
scripts/check-phase8-no-writers.py
```

Observed output:

```text
active_writer_count=0
native-cutover-smoke-20260430T192811Z
true
native-cutover-smoke-20260430T192811Z    closed  P4  Native cutover smoke
true
integrity_check=ok
legacy_foreign_key_violations=27
invalid_status_rows=0
ready_view_mismatches=0
blocked_view_mismatches=0
ready_blocked_overlap=0
active_assignment_in_ready_or_blocked=0
ready_with_unclosed_hard_blocker=0
ok
active_writer_count=0
native_gate=pass
```

## Current State

- Native validation passed against SQLite directly.
- Real cutover restart has not been executed in this log.
- `ORO_BEADSOURCE_MODE` has not been globally exported by this log.
- Dispatcher/workers remain stopped for Phase 8 until the restart beads execute.
