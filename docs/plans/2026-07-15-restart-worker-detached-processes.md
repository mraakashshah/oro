# Detached Worker Process Cleanup Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Ensure managed worker replacement terminates detached descendants owned by the exact worker and Oro project socket.

**Architecture:** Production worker commands receive normalized `ORO_SOCKET_PATH` and `ORO_WORKER_ID` ownership entries. `ExecProcessManager` serializes lifecycle changes, kills the tracked process group, scans process environments for the complete ownership tuple, and synchronously kills exact residual matches through injectable scan/kill functions. The stop command reuses the same exact marker-boundary matcher and no longer accepts a project worktree path alone as ownership evidence.

**Tech Stack:** Go `os/exec`, Unix process groups/sessions, `ps eww`, existing `pkg/processenv` environment normalization.

## Global Constraints

- Preserve `Kill`'s unknown-worker error without scanning or killing residuals.
- Require both worker ID and socket/project scope; never match bare `ORO_ROLE`, tool names, or worktree substrings alone.
- Finish tracked-group and residual cleanup before a same-ID replacement process starts.
- Bound residual scan, graceful termination, and force-kill waits.
- Extend the existing duplicate-worker process-group guarantee.

---

### Task 1: Pin detached ownership cleanup behavior

**Files:**
- Modify: `pkg/dispatcher/process_manager_test.go`
- Modify: `cmd/oro/cmd_stop_test.go`

**Interfaces:**
- Consumes: `dispatcher.NewOroProcessManager`, `ExecProcessManager.Spawn`, `ExecProcessManager.Kill`
- Produces: `TestExecProcessManagerKillTerminatesDetachedOwnedProcess` and stricter `TestResidualScanUsesScopedMarkers`

**Step 1: Write the failing test**

Add a helper-process mode to the named dispatcher test. The managed helper starts a `sleep` child with `SysProcAttr.Setsid`, writes its PID, and remains alive. Launch foreign detached processes with the same worker/different socket and the same socket/different worker. Kill the managed worker, start a same-ID replacement, and assert the tracked PID and owned detached PID are gone while both foreign PIDs remain alive. Also assert the production command environment contains the exact socket and worker entries.

Change `TestResidualScanUsesScopedMarkers` so a snapshot matches only when every supplied scoped marker is present with command boundaries. Include negative snapshots for worker-only, socket-only, wrong worker, wrong socket, bare role, tool name, and worktree path.

**Step 2: Run tests to verify they fail**

Run: `go test ./pkg/dispatcher -run '^TestExecProcessManagerKillTerminatesDetachedOwnedProcess$' -count=1 -v`

Expected: FAIL because the detached owned session survives and ownership environment entries are absent.

Run: `go test ./cmd/oro -run '^TestResidualScanUsesScopedMarkers$' -count=1 -v`

Expected: FAIL because the current scanner accepts any one marker.

### Task 2: Add exact ownership markers and matching

**Files:**
- Modify: `pkg/processenv/env.go`
- Modify: `pkg/dispatcher/process_manager.go`
- Create: `pkg/dispatcher/process_manager_residual.go`
- Modify: `cmd/oro/cmd_stop.go`

**Interfaces:**
- Produces: `processenv.WithWorkerOwnership(env []string, socketPath, workerID string) []string`
- Produces: `processenv.WorkerOwnershipMarkers(socketPath, workerID string) []string`
- Produces: `processenv.CommandContainsAllMarkers(command string, markers []string) bool`
- Produces: `ExecProcessManager.SetResidualProcessHooks(scanFn, killFn)` as a test-only injectable seam

**Step 1: Implement environment ownership**

Normalize away inherited duplicates of `ORO_SOCKET_PATH` and `ORO_WORKER_ID`, append the exact constructor socket and worker ID, and use the helper in `NewOroProcessManager`.

**Step 2: Implement managed residual cleanup**

Add a process snapshot type with PID and PGID, default `ps eww` scanning filtered by `CommandContainsAllMarkers`, and a bounded TERM-then-KILL implementation. Serialize `Spawn` and `Kill`; if a same-ID process is tracked, complete primary-group and residual cleanup before starting the replacement. Keep unknown `Kill` as an immediate error.

**Step 3: Tighten stop scanning**

Use environment-inclusive snapshots and require all scoped markers when markers are available. Treat roots only as supplementary evidence after scoped ownership, never sufficient evidence by themselves.

**Step 4: Run focused tests to verify they pass**

Run the two commands from Task 1, then `go test ./pkg/dispatcher ./pkg/processenv -count=1` and the relevant `cmd/oro` tests.

### Task 3: Verify, review, and land

**Files:**
- Review all files above.

**Interfaces:**
- Consumes: task acceptance criteria and project quality gate
- Produces: one atomic conventional commit on `agent/oro-xsmn`

**Step 1: Check integration side effects**

Trace dispatcher restart-worker ordering, duplicate `Spawn`, `Kill` unknown-ID behavior, constructor wiring, and stop residual scanning. Confirm no cleanup scan can observe a just-started same-ID replacement.

**Step 2: Run verification**

Run: `go test ./pkg/dispatcher -run '^TestExecProcessManagerKillTerminatesDetachedOwnedProcess$' -count=1`

Run: `go test ./pkg/dispatcher -count=1`

Run: `go test ./cmd/oro -run '^TestResidualScanUsesScopedMarkers$' -count=1`

Run: `ORO_MUTATION_BASE=epic/oro-tjv2 ./scripts/quality_gate.sh`

Expected: every command exits 0 and the named tests are reported as executed.

**Step 3: Commit**

Stage only the task files and commit with `git commit -m "fix(dispatcher): terminate detached worker processes"`.
