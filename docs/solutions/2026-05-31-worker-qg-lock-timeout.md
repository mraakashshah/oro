# Worker QG Lock Timeout

**Date:** 2026-05-31
**Component:** pkg/worker quality gate runner
**Severity:** high

## Symptom

Worker quality gates repeatedly failed before running checks:

```text
Waiting for another quality gate to finish...
FAIL: timed out waiting for quality gate lock: /Users/as21/codehouse/oro/.oro-quality-gate.lock
```

The failure fingerprint was `qg:511c34a9f1fa4c658d644589` and appeared across multiple beads.

## Investigation

Incident notes and Oro logs showed the same lock-timeout fingerprint on `oro-v6k5`, `oro-nfdk`, `oro-pk96`, `oro-hc9j`, `oro-v5wa`, and `oro-835q`. The repository also had abandoned lock and queue directories, but running `./scripts/quality_gate.sh` recovered those by archiving the stale lock before executing checks.

The remaining timeout path was in `pkg/worker/worker.go`: `qualityGateEnv` scrubbed inherited `ORO_QG_LOCK_TIMEOUT_SECONDS`, then injected `ORO_QG_LOCK_TIMEOUT_SECONDS=300` for every worker-run gate.

## Root Cause

The quality gate script serializes full gates across sibling worktrees. A live full gate can legitimately hold the repo-wide lock for more than five minutes, especially under concurrent worker load. The worker-level 300 second timeout overrode the script default and caused queued gates to fail while waiting for a valid holder.

## Solution

Keep scrubbing inherited `ORO_QG_LOCK_TIMEOUT_SECONDS` so ambient test settings do not leak into worker subprocesses, but do not inject a replacement timeout. Let the quality gate script own lock wait policy and stale-lock recovery.

Regression coverage: `pkg/worker/worker_test.go:TestRunQualityGate_DoesNotInjectLockTimeout`.

## Prevention

Do not add short worker-side lock wait limits for serialized full quality gates. If lock wait behavior needs to change, update `scripts/quality_gate.sh` and `cmd/oro/quality_gate_gen.go` together and cover the behavior in the quality gate harness.
