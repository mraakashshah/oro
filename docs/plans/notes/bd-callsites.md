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
| `pkg/dispatcher/beadsource.go` | `bd ready`, `bd list`, `bd show`, `bd close`, `bd defer`, `bd undefer`, `bd update`, `bd create`, `bd export` | accepted-legacy | This is the current production `CLIBeadSource` implementation of the `BeadSource` seam. Phase 1 keeps it as the legacy adapter behind `beadstore.Store`; Phase 10 deletes it. |

Dispatcher callers use `d.beads.*` or helper functions that take `BeadSource`
for normal assignment, close, create, update, ready, export, and epic closure
flows. `cmd/oro/cmd_start.go` and `cmd/oro/cmd_work.go` construct
`dispatcher.NewCLIBeadSource(runner)`, then pass the interface.

## Direct Go shell-outs that bypass BeadSource

| File | Direct bd use | Marker | Resolution |
| --- | --- | --- | --- |
| `cmd/oro/cmd_cleanup.go` | `bd list --status=in_progress --json`; `bd update <id> --status=open` | route-through-interface | Replace `cleanupBeads()` with native store calls: `InProgress()` plus `Update()`. This already appears in §11.2 and must be done before bd removal. |
| `pkg/mg/data/source.go` | `bd --version`; `bd list`; `bd context`; `bd show --current`; `bd doctor --agent`; `bd show <id> --long` | route-through-interface | Replace mg data source with constructor-injected `beadstore.Store` plus any extra native diagnostics needed. §11.2 calls out this rewrite. |
| `pkg/mg/data/mutate.go` | `bd update`; `bd update --claim`; `bd close`; `bd create` | route-through-interface | Same mg rewrite as above. Mutation paths must call Store/native CLI rather than bd. |
| `cmd/oro/cmd_mg.go` | `exec.LookPath("bd")`; loads through mg CLI source | route-through-interface | Remove `.beads`/bd preflight and construct mg data from native Store. §11.2 calls this out. |
| `pkg/dispatcher/health.go` | `bd dolt status` | accepted-legacy | Dolt health is only meaningful in legacy mode. Replace with SQLite ping when native beadstore lands, per §11.2. |
| `pkg/dispatcher/dolt_recovery.go` | `bd dolt start`; `bd import <backup>` | accepted-legacy | Dolt-specific recovery. Delete/replace with SQLite recovery path in Phase 10. |
| `cmd/oro/cmd_stop.go` | `bd dolt commit` | accepted-legacy | Legacy Dolt flush on stop. Remove with Dolt shutdown logic. |
| `cmd/oro/cmd_init.go` | formerly checked `bd --version` and ran `bd init --agents-template /dev/null` | migrated | Phase 10 blocker `oro-kk5f` removed live bd/Dolt initialization; keep this row as historical inventory until the broader Phase 10 cleanup deletes the surrounding legacy Dolt helpers. |
| `cmd/oro/cmd_doctor.go` | `bd init`; `bd init --from-jsonl` | accepted-legacy | Legacy Dolt recovery. Replace with native SQLite integrity/recovery tooling. |
| `cmd/oro/cmd_bd.go` | `oro bd` wrapper locates and `exec`s bd with optional stealth `--db` | accepted-legacy | Delete in Phase 10. §11.2 lists this as a deleted file. |
| `cmd/oro/preflight.go` | standard `oro start` preflight no longer requires `bd`; SQLite daemon preflight requires `claude` and `git` only | migrated | Removed bd from required tools in Phase 10 blocker `oro-58f5`; keep this row as historical inventory until Phase 10 deletes or rewrites the surrounding legacy callsites. |

## Python hook callsites

| File | Direct bd use | Marker | Resolution |
| --- | --- | --- | --- |
| `assets/hooks/session_start_extras.py` | `bd list --status=closed`; `bd ready`; `bd list --status=in_progress`; `bd show <id>` | route-through-interface | Replace with `oro bead ...` or native status APIs in Phase 6. §11.4 already lists this file. |
| `assets/hooks/session_start_compact.py` | `bd list --status=in_progress`; continuation bead creation through `bd create` command text | route-through-interface | Replace with `oro bead list/create` in Phase 6. §11.4 already lists this file. |
| `assets/hooks/architect_router.py` | Allows/routes user-entered `bd ...`; not a direct bd subprocess for bead state, but notifies manager after `bd create` | route-through-interface | Update command routing and notifications to `oro bead ...` during prompt/hook migration. |
| `assets/hooks/bd_create_notifier.py` | Watches for `bd create` command text; no bd subprocess | route-through-interface | Retarget to `oro bead create` or native event table notification. |
| `assets/hooks/notify_manager_on_bead_create.py` | Watches for `bd create` command text; no bd subprocess | route-through-interface | Retarget to `oro bead create` or native event table notification. |
| `assets/hooks/pre_compact.py` | Parses transcript `bd update --status=in_progress`; tells user to run `bd ready` | route-through-interface | Replace transcript pattern and advice with `oro bead` equivalents. |
| `assets/hooks/validate_agent_completion.py` | Checks transcript for `bd close`; does not execute bd | route-through-interface | Replace completion detector with dispatcher/native close signal or `oro bead close` text. |

## Prompt and instruction surfaces

These are not shell-outs by themselves, but they cause agents to emit bd commands
and therefore must be migrated with the hook work:

- `pkg/worker/prompt.go`: `bd create`, `bd update`, `bd dep add`, `bd show`
  examples in decomposition/failure guidance.
- `cmd/oro/manager.go` and `cmd/oro/architect.go`: role guidance still tells
  managers/architects to use `bd`.
- `assets/beacons/*.md` and `assets/skills/*/SKILL.md`: multiple bd command
  examples. §11.4 lists the skill files and requires a Phase 6 hard gate where
  `git grep -l 'bd ' assets/skills/` returns zero files.

## Test-only bd callsites

Tests mock or assert legacy bd command strings in these areas:

- `pkg/dispatcher/beadsource_test.go`
- `pkg/dispatcher/dolt_recovery_test.go`
- `cmd/oro/cmd_cleanup_test.go`
- `cmd/oro/cmd_stop_test.go`
- `cmd/oro/cmd_doctor_test.go`
- `pkg/mg/data/*_test.go`
- Python hook tests under `tests/`

Marker: `accepted-legacy` until the corresponding production path is migrated.
Then replace tests with `beadstore.FakeStore` or `oro bead` assertions as
appropriate. This matches §11.3's test migration category.

## Conclusion

`BeadSource` is the main dispatcher seam, but it is not the only bd seam in the
repo. The concrete bypasses that can affect runtime behavior are cleanup,
legacy mg, Dolt health/recovery/stop/init/doctor, the `oro bd` wrapper, and
Python hook surfaces. The replatform should treat cleanup and mg as
`route-through-interface`; Dolt-specific paths are `accepted-legacy` only until
the planned Phase 10 deletion/replacement.
