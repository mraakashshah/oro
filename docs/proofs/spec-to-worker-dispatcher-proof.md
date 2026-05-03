# Proof: Spec-to-Worker Dispatcher Workflow

**Task:** oro-spf1a (child of oro-spf1)
**Subject:** sqlite manual-integration dispatcher
**Date:** 2026-05-03

## Summary

This document records evidence that the dispatcher correctly handles the full
spec-to-worker workflow under the sqlite beadstore with manual integration mode
enabled (oro-jxjv). The proof covers: flag parsing, daemon argument forwarding,
config wiring, and the completed-worker escalation path.

## Commits Under Proof

| Commit | Description |
|--------|-------------|
| `f75f4828` | fix(dispatcher): add manual integration mode (oro-jxjv) |
| `b3b13d90` | fix(dispatcher): wire manual integration start path (oro-jxjv) |
| `e675bb11` | test(dispatcher): cover manual integration acceptance (oro-jxjv) |
| `1ddb5e4f` | test(start): prove manual integration daemon handoff (oro-jxjv) |
| `48f0c308` | test(start): cover daemon-only manual flag parse (oro-jxjv) |

## Behavior Under Proof

### 1. Config field

`pkg/dispatcher/dispatcher.go:428` — `Config.ManualIntegration bool` added.
When true, `handleDone` routes to `completeManualIntegration` instead of
launching a background merge goroutine.

### 2. CLI flag — `oro start --manual-integration`

`cmd/oro/cmd_start.go` exposes `--manual-integration` on the `start` command.
The flag flows into `ExecDaemonSpawner.ManualIntegration`, which appends
`--manual-integration` to the daemon child's argv via `buildArgs`. The daemon
process re-parses the flag inside `runDaemonOnly` and passes it to
`buildDispatcherWithReviewTimeouts` as the `manualIntegration bool` parameter,
which writes it into `dispatcher.Config`.

### 3. CLI flag — `oro dispatcher start --manual-integration`

`cmd/oro/cmd_dispatcher.go` exposes the same flag on the `dispatcher start`
subcommand. The flag is forwarded via `SetManualIntegration(true)` on the
injected spawner (interface-guarded for testability).

### 4. Worktree preservation

When `ManualIntegration=true`, `completeManualIntegration`:

- marks the assignment `completed` in the sqlite state DB
- sets the bead status to `blocked` (awaiting coordinator review)
- emits a `manual_integration_required` event to the sqlite event log
- escalates `[ORO-DISPATCH] MANUAL_INTEGRATION: <beadID> — review and merge <branch> from <worktree>` to the manager pane
- **does not** remove the worktree or merge the branch

### 5. Worktree prune safety (related fix)

`pkg/dispatcher/worktree_manager.go` — `Prune` now calls
`registeredWorktreePaths` before removing directories under `.worktrees/`.
Directories that are still registered git worktrees are skipped, preventing
startup pruning from destroying branches preserved for manual review.

## Test Coverage

| Test | File | What it proves |
|------|------|----------------|
| `TestHandleDoneManualIntegrationSkipsMergeAndPreservesWorktree` | `pkg/dispatcher/dispatcher_test.go` | handleDone with ManualIntegration=true emits `manual_integration_required`, sets bead to blocked, does not merge or close, preserves worktree |
| `TestStartManualIntegrationDaemonHandoffForwardsFlagAndConfig` | `cmd/oro/cmd_start_test.go` | daemon-only flag parse, buildArgs includes `--manual-integration`, buildDispatcherWithReviewTimeouts sets Config.ManualIntegration |
| `TestDispatcherStartManualIntegrationFlagConfiguresDaemon` | `cmd/oro/cmd_dispatcher_test.go` | `oro dispatcher start --manual-integration` sets manualIntegration on spawner |
| `TestDispatcherStartAutoMergeDefaultDoesNotEnableManualIntegration` | `cmd/oro/cmd_dispatcher_test.go` | default `oro dispatcher start` leaves manual integration disabled (sqlite mode) |

## Verification

Run these commands to confirm the evidence:

```bash
# Confirm the Config field exists
rg 'ManualIntegration.*bool' pkg/dispatcher/dispatcher.go

# Confirm the completeManualIntegration path
rg 'manual_integration_required' pkg/dispatcher/dispatcher.go

# Confirm the worktree prune guard
rg 'registeredWorktreePaths' pkg/dispatcher/worktree_manager.go

# Run the relevant tests
go test ./pkg/dispatcher/... -run TestHandleDoneManualIntegration -v -timeout 120s
go test ./cmd/oro/... -run 'TestStartManualIntegration|TestDispatcherStartManualIntegration|TestDispatcherStartAutoMerge' -v -timeout 120s
```
