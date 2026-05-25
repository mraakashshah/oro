# Mutation Testing Requires Explicit Flag

**Date:** 2026-05-25
**Component:** quality gate, worker, dispatcher
**Severity:** medium

## Symptom

Mutation testing could run during worker or dispatcher quality gates even when
the user had not passed `--mutation-testing`.

## Investigation

The quality gate script already documented mutation as disabled by default, but
`ShellQGRunner` and `worker.RunQualityGate` enabled mutation by setting
`ORO_RUN_MUTATION=1` when their `skipMutation` argument was false. The worker's
automatic local QG path passed `skipMutation=false`, so normal worker flow could
opt into mutation without a user-facing flag.

## Root Cause

The opt-in was modeled as ambient environment state instead of a command-line
argument. That made mutation testing possible through inherited or runner-set
environment, which contradicted the CLI contract.

## Solution

`scripts/quality_gate.sh` and generated quality gates now use an internal
`QG_MUTATION_TESTING=true` marker set only while parsing `--mutation-testing`.
Worker and dispatcher runners scrub `ORO_RUN_MUTATION` and pass
`--mutation-testing` explicitly only when mutation is requested. The worker's
automatic local gate now passes `skipMutation=true`.

## Prevention

Keep mutation enablement flag-based. Tests should assert that
`ORO_RUN_MUTATION=1` does not enable mutation and that opt-in runners pass
`--mutation-testing` explicitly.
