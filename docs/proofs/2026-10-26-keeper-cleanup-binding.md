# Keeper cleanup authority: Terra implementation and Astra review

Date: 2026-10-26. Implementation commit `16f551881796541ce651c2142e124fee531ade1b`,
base `447eeb67`. Integrated by fast-forward into `feature/factory-core`.

## Change and ownership

A direct `gpt-6.1-terra` agent implemented the saved Astra contract in isolated
branch `feature/factory-keeper-cleanup-binding`. A fresh `gpt-6-astra` agent
reviewed the committed diff read-only. Root coordinated integration and performed
independent verification. No managed Oro model invocation, controller fault trial,
Docker/VM qualification, or autonomous-factory delivery is claimed.

Only two implementation files changed:

- `pkg/factory/managed_cleanup.go`: reservation and permit reload invoke
  `requireCleanupKeeperBinding` in the current transaction. It loads the canonical
  keeper through `readManagedDockerKeeper`, including dependent volume/scope/
  reservation validation. Bound keepers require full exact Binding equality for
  every operation, including inherited labels and lifetime. Unbound keepers allow
  only exact inspection with ContainerID/CreatedAt cleared. No adoption or unknown
  create replay is introduced. Non-keeper behavior is unchanged.
- `pkg/factory/managed_cleanup_process_test.go`: replace the synthetic keeper
  scenario with real keeper records. Cover exact operations, drift denial,
  immutable counters on failure, canonical permit tampering, HELLO/grant rereads,
  prepared/dispatching/acknowledged-unbound cases, and missing/corrupt records.

## Verification evidence

Terra reported an existing baseline PASS at 1.178 seconds, then observed RED:
`inspect accepted added`, `stop accepted added`, `remove accepted added`, changed
authority counts, returned/granted tampered permits, and accepted missing/corrupt
keeper records. Erroneous positive reservations then blocked subsequent work via
host quiescence. Raw worker logs were not captured to files; these RED observations
are the worker's tool-output report, not independently archived root logs.

| Check | Result |
| --- | --- |
| Terra cleanup/label/source race regressions | PASS, 64.437 s |
| Terra package golangci-lint and gofumpt | PASS, zero issues / no formatting differences |
| Independent Astra committed-diff review | No Critical, Important, or Minor findings |
| Independent root cleanup authority race before integration | PASS, 12.439 s |
| Root full factory shuffled race after integration | PASS, 364.538 s package / 369.406 s harness; 89.2% statements |
| Root repository quality gate | FAIL, 154.021 s; 14 checks passed, 2 failed |
| Comparison with previous repository-gate diagnostic set | Exact match: six NilAway diagnostics and three dead exports; no new findings |

Root full-package command:

```sh
GOTOOLCHAIN=go1.26.10 go test -race -shuffle=on -p 2 \
  -coverprofile=<private-evidence-dir>/cleanup-binding-factory-full.out \
  ./pkg/factory -count=1 -timeout=600s
```

It ran through the existing protected verification harness with original and
final HEAD both `16f55188` and both working-tree snapshots clean. Coverage reports
`requireCleanupKeeperBinding` 92.3%, reservation 93.3%, and permit reload 91.3%.
The local evidence directory is `/private/tmp/oro-factory-verify-ldhnhnvl`; prefixes
are `cleanup-binding-factory-full` and `cleanup-binding-gate` for logs, profiles,
and JSON result metadata. Tests remain committed if temporary evidence disappears.

## Limits and continuation

Astra noted coverage limits, not defects: no direct dropped keeper-table case,
corrupted volume dependency exercised through cleanup, or former keeper
final-release lifecycle case; unbound positive controls cover reservation rather
than subsequent HELLO/reload. These do not establish native cleanup qualification.

The existing full-gate failures are preserved in the
[earlier verification record](2026-10-26-factory-resumption.md). No warnings were
suppressed and no dummy API consumers were added. The static-tier failure prevents
the repository gate's broad Go runtime/security/build tiers from running.

The task remains tracked in progress under the existing global qualification
constraints. The implementation itself is accepted and integrated; do not repeat
it without a new defect. Continue with the outstanding nil-safety findings and
real managed startup/controller wiring in the
[current checkpoint](../plans/2026-10-26-factory-resumption.md). The full factory
objective remains unachieved.
