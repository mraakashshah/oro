# bd callsite inventory

Bead: `oro-bucr`

Purpose: verify whether `pkg/dispatcher.BeadSource` is the only bd shell-out
seam and enumerate every direct bd shell-out with a resolution marker.

Markers:

- `route-through-interface`: should become `beadstore.Store` or an equivalent
  native Oro API before bd is removed from runtime paths.
- `accepted-legacy`: intentionally remains only during the Dolt/bd migration
  window, then is removed or deleted in the phase called out below.

## Canonical seam

| File | Calls | Marker | Resolution |
| --- | --- | --- | --- |
| `pkg/dispatcher/beadsource.go` | formerly `bd ready`, `bd list`, `bd show`, `bd close`, `bd defer`, `bd undefer`, `bd update`, `bd create`, `bd export` | deleted | Removed by Phase 10 bead `oro-37t5`; production dispatcher/work paths now construct native SQLite stores. |

Dispatcher callers use `d.beads.*` or helper functions that take
`beadstore.Store` for normal assignment, close, create, update, ready, export,
and epic closure flows. `cmd/oro/cmd_start.go` and `cmd/oro/cmd_work.go`
construct native SQLite stores for production.

## Direct Go shell-outs that bypass BeadSource

| File | Direct bd use | Marker | Resolution |
| --- | --- | --- | --- |
| `cmd/oro/cmd_cleanup.go` | formerly `bd list --status=in_progress --json`; `bd update <id> --status=open` | migrated | `cleanupBeads()` now opens the native SQLite state DB and calls `beadstore.SQLiteStore.InProgress()` plus `Update()`; remaining bd strings in `cmd_cleanup_test.go` are legacy fake-runner fixtures only. |
| `pkg/mg/data/source.go` | formerly `bd --version`; `bd list`; `bd context`; `bd show --current`; `bd doctor --agent`; `bd show <id> --long` | migrated | `oro mg` now constructs a native `beadstore.Store` when available; JSONL mode reads local snapshots only and detail comes from the loaded snapshot rather than `bd show`. |
| `pkg/mg/data/mutate.go` | formerly `bd update`; `bd update --claim`; `bd close`; `bd create` | migrated | mg mutations now call `beadstore.Store` methods directly: `Update`, `Close`, `Create`, and `Show` for claim ownership checks. |
| `cmd/oro/cmd_mg.go` | formerly `exec.LookPath("bd")`; loaded through mg CLI source | migrated | `resolveSource()` prefers the native SQLite store and falls back only to a local JSONL snapshot; it does not check for or execute `bd`. |
| `pkg/dispatcher/health.go` | formerly `bd dolt status` | migrated | Phase 10 removed runtime Dolt status probing/recovery; remaining health loop path is a no-op until the obsolete Dolt ticker fields are removed. |
| `pkg/dispatcher/dolt_recovery.go` | formerly `bd dolt start`; `bd import <backup>` | deleted | Removed by Phase 10 bead `oro-37t5`. SQLite recovery is handled through native state DB backups/runbooks, not bd import. |
| `cmd/oro/cmd_stop.go` | formerly `bd dolt commit` | migrated | Phase 10 blocker `oro-zy25` removed legacy bd flush on stop; Phase 10 cleanup removed stale stop-all Dolt prose/fields. |
| `cmd/oro/cmd_init.go` | formerly checked `bd --version` and ran `bd init --agents-template /dev/null` | migrated | Phase 10 blocker `oro-kk5f` removed live bd/Dolt initialization; keep this row as historical inventory until the broader Phase 10 cleanup deletes the surrounding legacy Dolt helpers. |
| `cmd/oro/cmd_doctor.go` | formerly ran legacy bd reinitialization commands | migrated | Phase 10 blocker `oro-p10d` removed the operator-visible legacy Dolt recovery command. Future native SQLite doctor checks should be added without bd/Dolt repair paths. |
| `cmd/oro/cmd_bd.go` | formerly `oro bd` wrapper located and `exec`ed bd with optional stealth `--db` | deleted | Removed by Phase 10 bead `oro-37t5`. |
| `cmd/oro/preflight.go` | standard `oro start` preflight no longer requires `bd`; SQLite daemon preflight requires `claude` and `git` only | migrated | Removed bd from required tools in Phase 10 blocker `oro-58f5`; keep this row as historical inventory until Phase 10 deletes or rewrites the surrounding legacy callsites. |
| `cmd/oro/cmd_bead_migrate.go` | `bd export` when `oro bead migrate-from-dolt` runs without `--from-jsonl` or `--from-fixture` | accepted-legacy | Keep only as a migration import/audit fallback while bd/Dolt remains one possible source snapshot. Operational runbooks should prefer the preserved JSONL export through `--from-jsonl`; this is not a dispatcher, worker, mg, or post-cutover runtime beadstore path. Remove or deprecate after Phase 11 replaces the remaining bd/Dolt import dependency. |

## Python hook callsites

| File | Direct bd use | Marker | Resolution |
| --- | --- | --- | --- |
| `assets/hooks/session_start_extras.py` | formerly `bd list --status=closed`; `bd ready`; `bd list --status=in_progress`; `bd show <id>` | migrated | Current hook invokes `oro bead list --status=in_progress --json`; worker sessions skip subprocess calls entirely. |
| `assets/hooks/session_start_compact.py` | formerly `bd list --status=in_progress`; continuation bead creation through `bd create` command text | migrated | Current hook no longer contains direct `bd` command strings. |
| `assets/hooks/architect_router.py` | formerly allowed/routed user-entered `bd ...` and watched `bd create` text | migrated | Current hook no longer contains direct `bd` command strings. |
| `assets/hooks/bd_create_notifier.py` | formerly watched `bd create` command text; no bd subprocess | migrated | Current hook no longer contains direct `bd` command strings. |
| `assets/hooks/notify_manager_on_bead_create.py` | formerly watched `bd create` command text; no bd subprocess | migrated | Current hook no longer contains direct `bd` command strings. |
| `assets/hooks/pre_compact.py` | formerly parsed transcript `bd update --status=in_progress`; told user to run `bd ready` | migrated | Current hook no longer contains direct `bd` command strings. |
| `assets/hooks/validate_agent_completion.py` | formerly checked transcript for `bd close`; did not execute bd | migrated | Current hook no longer contains direct `bd` command strings. |

## Prompt and instruction surfaces

These are not shell-outs by themselves, but they can cause agents to emit bead
commands and therefore were part of the prompt/hook migration gate. Current
production prompt and hook surfaces are migrated:

- `pkg/worker/prompt.go`: the Bead Tools section names `oro bead create` and
  `oro bead dep add`; no `bd` command examples remain in the production worker
  prompt.
- `pkg/ops`, `cmd/oro/manager.go`, `cmd/oro/architect.go`,
  `assets/hooks/`, and `assets/skills/`: `rg -n 'bd create|bd update|bd
  dep|bd show|bd close|bd ready|bd list'` returns no current command examples
  outside test-only or historical documentation contexts.

## Test-only legacy references

Tests still mock or assert legacy bd command strings in these areas:

- `cmd/oro/cmd_cleanup_test.go` contains legacy fake-runner fixtures for
  regression coverage; current assertions ensure cleanup does not call bd in
  native SQLite paths.
- Python hook tests under `tests/`

`pkg/mg/data/*_test.go` no longer contains bd command-callsite coverage. It
retains legacy JSON contract fixtures and store-backed tests so JSONL imports
and native store mappings remain compatible during the migration window.

Deleted Phase 10 test surfaces:

- `pkg/dispatcher/beadsource_test.go`
- `pkg/dispatcher/dolt_recovery_test.go`

Migrated Phase 10 test surfaces:

- `pkg/mg/data/*_test.go`
- `cmd/oro/cmd_stop_test.go`

Marker: remaining legacy assertions are `accepted-legacy` until the
corresponding production path is migrated. Then replace tests with
`beadstore.FakeStore` or `oro bead` assertions as appropriate. This matches
§11.3's test migration category.

## Conclusion

`beadstore.Store` is now the dispatcher/work production seam. Historical bd
bypasses have been retired in the runtime paths that gate native SQLite
dispatcher/work operation: cleanup uses native SQLite, mg uses native store or
local JSONL snapshots, stop no longer flushes bd/Dolt, doctor no longer repairs
Dolt, dispatcher health no longer shells to bd, and the `oro bd` wrapper plus
dispatcher CLI beadsource/recovery files are deleted. Remaining references in
this inventory are migration-only import/export fallback surfaces, historical
docs, test fixtures, and stale comments; they are not live worker/dispatcher
coordination paths.
