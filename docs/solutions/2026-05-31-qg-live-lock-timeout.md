# QG Live Lock Timeout

**Date:** 2026-05-31
**Component:** quality gate
**Severity:** high

## Symptom

Worker quality gates failed while waiting for another live gate:

```text
Waiting for another quality gate to finish...
FAIL: timed out waiting for quality gate lock: /Users/as21/codehouse/oro/.oro-quality-gate.lock
```

The failure fingerprint was `qg:511c34a9f1fa4c658d644589`.

## Investigation

The repo-wide `.oro-quality-gate.lock` owner file pointed at a live
`quality_gate.sh` process, and the affected bead retry had a FIFO queue ticket
behind that holder. This was valid serialization, not a stale lock.

## Root Cause

`scripts/quality_gate.sh` and the generated template enforced a default
`ORO_QG_LOCK_TIMEOUT_SECONDS` fallback of 1800 seconds, and worker-run gates
also injected a five-minute timeout. Long but healthy gates could therefore
convert valid live contention into a systemic QG incident.

## Solution

Keep stale lock recovery based on owner liveness, but make lock wait timeouts
explicit-only. `ORO_QG_LOCK_TIMEOUT_SECONDS` is still honored in tests and
manual harnesses, but normal worker gates wait for live holders to finish.

## Prevention

When changing quality gate locking, keep `scripts/quality_gate.sh` and
`cmd/oro/quality_gate_gen.go` synchronized and cover both generated and
checked-in script behavior.
