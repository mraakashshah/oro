# JSONL fallback inventory

Bead: `oro-rtol`

Purpose: inventory bead-state JSONL snapshots that can feed the §9.9
`--from-jsonl` fallback if Dolt is unrecoverable during Phase 8.

Spec references:

- §9.9 JSONL fallback source priority: `.beads/backup/full-state.jsonl`, recent
  `oro bead export` output, `.beads/issues.jsonl`, then operator-supplied path.
- §12.1 Phase 0 requires this inventory before the migration tool is built.

## Current repo snapshots

| Location | Present now | Tracked | Producer | Use in fallback | Notes |
| --- | --- | --- | --- | --- | --- |
| `.beads/issues.jsonl` | Yes | Yes | `bd export .beads/issues.jsonl` / bd hooks | Third priority | Current repo has this file tracked. `git ls-files .beads/issues.jsonl` confirms it is committed in this checkout. |
| `.beads/export-state.json` | Yes | Yes | `bd export` metadata | Not a bead snapshot | Keep with `.beads/issues.jsonl` for bd state bookkeeping, but do not feed it to `--from-jsonl`. |
| `.beads/backup/full-state.jsonl` | No | Ignored by `.beads/.gitignore` | Dispatcher heartbeat backup via `backupFullState` | First priority when present | Current checkout has no `.beads/backup/` directory. The path is intentionally ignored. |
| `.beads/full-state.jsonl` | No | Not tracked | Legacy `oro doctor recover-dolt` expectation | Compatibility hazard | Current doctor recovery code looks here, but dispatcher backup code writes `.beads/backup/full-state.jsonl`. Resolve this mismatch during migration/fallback implementation. |
| `.beads/memory/knowledge.jsonl` | Yes | Yes | memory/import tooling | Not a bead snapshot | Human learning memory, not bead state. Exclude from migration fallback. |

## Heartbeat backup

`pkg/dispatcher/worker_pool.go` owns the current heartbeat backup path:

- `heartbeatLoop` starts `backupTicker := time.NewTicker(d.cfg.BackupInterval)`.
- `Dispatcher.Config.BackupInterval` defaults to `5 * time.Minute`.
- `backupFullState` calls `d.beads.Export(ctx)`, which is the legacy
  `CLIBeadSource.Export` wrapper around `bd export`.
- Non-empty export output is written to
  `.beads/backup/full-state.jsonl`.
- `maybeChangeDetectionBackup` also calls `backupFullState` when queue size
  changes by at least 5 since the last backup.

This is the best automatic fallback source, but it is only available after a
dispatcher has run long enough to write it. Because the directory is ignored, it
must be collected from the operator's machine, not from git.

## Doctor recovery mismatch

`cmd/oro/cmd_doctor.go` still documents and implements this recovery path:

1. Look for `.beads/full-state.jsonl`.
2. Copy that file to `.beads/issues.jsonl`.
3. Run `bd init --from-jsonl`.

That differs from the dispatcher heartbeat backup path
`.beads/backup/full-state.jsonl` and from §9.9. The migration tool should use
the §9.9 priority order and either update doctor recovery or accept
`.beads/full-state.jsonl` only as a legacy compatibility alias.

## Ad-hoc export artifacts

Repository search found no committed ad-hoc bead-state exports such as
`bd export > foo.jsonl`.

JSONL files that exist in the repo but are not bead-state fallback inputs:

- `pkg/testdata/sample.jsonl`
- `pkg/mg/testdata/sample.jsonl`
- `ad_hoc/memory_eval/*.jsonl`
- `ad_hoc/memory_eval/cmd/compare/testdata/*.jsonl`

Those are test fixtures or memory-evaluation corpora and must not be offered as
migration fallback candidates.

Operator machines may still have ad-hoc exports outside the repo. The migration
tool's `--from-jsonl <path>` flag should accept those paths after validating the
file contains bd bead export rows.

## External Oro project snapshots observed locally

These are outside the current repo and should not be automatic inputs for this
project, but they show path shapes the fallback code may encounter:

- `~/.oro/projects/<project>/beads/issues.jsonl`
- `~/.oro/projects/<project>/beads/backup/issues.jsonl`
- `~/.oro/projects/<project>/beads/backup/{comments,config,dependencies,events,labels}.jsonl`
- `~/.oro/projects/<project>/beads/interactions.jsonl`

Only files with bd bead export row shape should be accepted by
`oro bead migrate-from-dolt --from-jsonl`.

## Recommended fallback priority for implementation

1. Use `.beads/backup/full-state.jsonl` when it exists and parses as bd bead
   export JSONL.
2. Use an explicit operator path from `--from-jsonl <path>`.
3. Use `.beads/issues.jsonl` if it exists, is parseable, and the operator
   accepts that it may be stale or git-derived.
4. Treat `.beads/full-state.jsonl` as a legacy alias only if doctor recovery is
   kept backward-compatible.
5. Reject unrelated JSONL fixtures by schema, not by filename alone.
