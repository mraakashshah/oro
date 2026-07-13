# Ops Timeout Orphaned the Runtime Grandchild

**Date:** 2026-07-13
**Component:** `pkg/ops`
**Severity:** high

## Symptom

An ops run could be marked failed after its timeout while the real Codex agent
continued running in the abandoned worktree. The Node launcher was terminated,
but its `codex-darwin-arm64` child survived and kept consuming resources.

## Investigation

`ExecSpawner` used `exec.CommandContext`, and `opsProcess.Kill` called
`Process.Kill`. Both paths target only the direct launcher PID. A first-pass
process-group kill in `opsProcess.Kill` fixed explicit timeout and idle-wedge
kills, but a real cancellation test still hung in `Wait`: `CommandContext` had
installed its own direct-child cancellation callback, which could run before
the higher-level `ctx.Done()` branch killed the process group.

## Root Cause

The runtime launcher and the actual agent were separate processes in the same
inherited process group as Oro. Killing only the launcher's PID orphaned the
agent. The surviving child also retained the subprocess output pipe, so reaping
could block or race the failed verdict.

## Solution

`ExecSpawner` now starts every ops subprocess in a dedicated process group with
`syscall.SysProcAttr{Setpgid: true}`. `opsProcess.Kill` sends `SIGKILL` to the
negative process-group ID and falls back to `Process.Kill`. The command's
`Cancel` callback uses that same group-aware kill method.

Timeout, context-cancellation, and idle-wedge paths now wait for the existing
`Wait` call to finish before publishing `VerdictFailed`. Regression tests fork
a child that ignores `SIGTERM`, assert that explicit kill and context
cancellation remove the full group, and gate verdict publication on reaping.

## Prevention

When `exec.CommandContext` launches a wrapper that can fork, configure both a
dedicated process group and a group-aware `Cmd.Cancel` callback. Process-group
support in an explicit `Kill` method alone does not cover context cancellation.

## Related

- Task `oro-7djy`
- Memory pattern `feedback_zombie_reaping`
