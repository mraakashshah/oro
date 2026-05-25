# QG Lock Wait False Fingerprint

**Date:** 2026-05-25
**Component:** quality gate
**Severity:** high

## Symptom

QG incident 191 was titled `Waiting for another quality gate to finish...` with fingerprint `qg:f2a91dc9f23e6b5e75572f29`.

## Investigation

The affected bead worktree was present at `.worktrees/oro-5jnx`. Running `./scripts/quality_gate.sh` while another worker owned the repo-wide QG lock reproduced the wait message. Reading `scripts/quality_gate.sh` and `cmd/oro/quality_gate_gen.go` showed the same message was emitted at the top of every `acquire_quality_gate_lock` loop, before the script attempted to acquire the lock.

## Root Cause

The lock wait message was printed before observing actual FIFO queue or lock contention. That meant an unrelated quality gate failure could include the wait text in its output and be classified under the lock-wait fingerprint.

## Solution

`scripts/quality_gate.sh` and the generated quality gate now track whether waiting has already been reported and emit `Waiting for another quality gate to finish...` only after detecting an earlier FIFO ticket or a held active lock.

Regression coverage lives in `cmd/oro/quality_gate_gen_test.go:TestQualityGateRunLockDoesNotReportWaitingWhenUncontended`.

## Prevention

For diagnostic messages used in QG fingerprinting, emit the message only after the condition has been observed. Add regressions for uncontended and contended paths when changing quality gate lock behavior.
