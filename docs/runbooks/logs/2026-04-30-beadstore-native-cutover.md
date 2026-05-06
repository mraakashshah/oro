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
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task status
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task ready --json > /tmp/oro-native-ready.json
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task blocked --json > /tmp/oro-native-blocked.json
jq -e 'type == "array"' /tmp/oro-native-ready.json
jq -e 'type == "array"' /tmp/oro-native-blocked.json
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task show "$representative_id" --json |
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

The controlled smoke task was `native-cutover-smoke-20260430T192811Z`.

The gate used:

```bash
scripts/check-phase8-no-writers.py
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task create --id "$test_id" --title "Native cutover smoke" --type task --priority 4
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task show "$test_id" --json | jq -e --arg id "$test_id" '.id == $id'
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task close "$test_id" --reason "Native cutover smoke passed"
ORO_BEADSOURCE_MODE=sqlite "$oro_bin" task show "$test_id" --json | jq -e '.status == "closed"'
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
- Dispatcher/workers remain stopped for Phase 8 until the restart tasks execute.

## Cutover Completion Update

On 2026-05-01, Phase 8 cutover was completed with native SQLite as the active
bead store.

Committed fixes and docs:

- `66703059` fixed sqlite dispatcher assignment by skipping legacy bd/Dolt
  recovery only when `ORO_BEADSOURCE_MODE=sqlite`.
- `2250d25f` finalized the cutover `PATH` construction and repo-local
  `.envrc` switch to `ORO_BEADSOURCE_MODE=sqlite`.

Verification:

```text
scripts/check-bd-version.sh: exit 0
dispatcher_count before restart: 0
scripts/check-phase8-no-writers.py before mutation/restart: active_writer_count=0
ORO_BEADSOURCE_MODE before migration/restart gates: empty
sqlite3 "$state_db" 'PRAGMA integrity_check;': ok
native invariant gate: exit 0
ready JSON shape: array
blocked JSON shape: array
controlled native create/show/close smoke: passed
local scripts/quality_gate.sh after dispatcher fix: passed 20/0
```

Controlled worker restart proof:

- Dispatcher started in sqlite mode from a generated cutover `PATH` where
  `command -v bd` fails.
- Targeted worker assignment succeeded for
  `native-worker-smoke-20260430t213044` at event `789083`.
- Targeted worker assignment succeeded for
  `native-worker-smoke-20260430t213245` at event `789100`; this exposed that
  the initial minimal `PATH` also removed QG tools.
- Full tool `PATH` excluding only `bd` was generated at
  `/tmp/oro-sqlite-cutover-bin-full.l5Z1eJ`; it resolved `oro`, `claude`,
  `git`, Homebrew `bash`, Go, Python, shell, and lint tools while `bd` stayed
  absent.
- Full-`PATH` targeted worker assignment succeeded for
  `native-worker-smoke-20260430t213801` at event `789138` and reached
  `awaiting_review` without missing-tool failures. This proves process
  environment and targeted assignment, not a full worker/QG/merge lifecycle.

Final live state:

- Repo-local `.envrc` exports `ORO_BEADSOURCE_MODE=sqlite`.
- Phase 8 (`oro-ect4`) is closed in the native SQLite bead store.
- Phase 9 (`oro-cpv0`) is in progress and time-gated unless the operator
  rewrites or waives its observation children.
- Dispatcher is running in sqlite mode as PID `79558` with `--workers 0` from
  the generated bd-free cutover `PATH`.
- bd/Dolt is retained as import-source/audit/rollback reference only, not as a
  cutover veto.
