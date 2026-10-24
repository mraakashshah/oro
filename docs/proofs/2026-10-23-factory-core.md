# Factory core: first increment proof

The experimental replacement lifecycle is implemented on `feature/factory-core`.
It automatically repairs rejected work, independently verifies a frozen candidate,
promotes that exact commit, and reconciles interrupted or ambiguously acknowledged
promotion. There is no human approval step in that loop.

This establishes the lifecycle for trusted command adapters in disposable or
externally isolated repositories. It does not establish a fully autonomous
production factory. Hostile-code containment, automatic controller supervision,
real-model reliability, deployment, and live cutover remain outstanding.

## Implementation and review

Baseline: `79f6cad693fdcbf83c9f19f96a785b80e6ae5ff7`. The functional implementation
and independent review fixes are committed through
`c11c41fe7f7035cce1c6060bba285eaf9609850b`. Subsequent commits initialize the
ancestor-path buffer explicitly (`0839fc91`) and synchronize a cancellation test
under load (`c7041903`). The final shuffled race/coverage run includes both changes.

The actual execution path is `rootSubcommands` → `newFactoryCmd` → `factory.Run`.
It uses an explicit per-objective state directory and its own SQLite lifecycle.
A source-level dependency test permits only the standard library and `dbutil`.
The primary checkout's existing changes were preserved, and the live dispatcher
was not migrated or started.

Independent design and implementation review found and closed these gaps:

| Failure | Required correction and regression |
| --- | --- |
| Ignored worker output masks bad committed source | Separate pristine acceptance/review checkouts; execute fresh delivered checkout |
| A lock helper exists but the controller never acquires it | Two actual public-controller processes; loser has no state or adapter effects |
| Nested repository path weakens containment | Require the actual Git working-tree root before policy reads or state creation |
| Git filters outlive deadlines or cancellation | Shared bounded subprocess execution, output limits and process-group termination |
| Checked-out target loses its diagnosis during retries | Typed terminal failure, preserving reason and attempt count |
| CAS applies but its success acknowledgment is lost | Observe the real ref; complete confirmed effects or preserve uncertain promotion intent |

Final independent re-review closed every reported finding within this increment's
scope, including the reviewer's original reproductions. This is code review and
fault-injection evidence, not a production availability claim.

## Focused verification

Toolchain for these checks: Go 1.26.5, matching the repository's pinned tooling.

| Check | Result |
| --- | --- |
| `go test -race -shuffle=on -p 3 -coverprofile=<temporary profile> ./pkg/factory -count=1 -timeout=300s` | PASS, 147.313 s, 86.7% statements |
| `go test ./cmd/oro -run '^TestFactoryCLI$' -count=1 -timeout=120s` | PASS, 2.580 s |
| Changed-code golangci-lint over `pkg/factory` and `cmd/oro` | PASS, 0 issues |
| Factory-scoped NilAway after explicit buffer initialization | PASS |
| Pinned gofumpt, diff whitespace checks, changed Markdown lint | PASS |
| `make build`, broad Go build, vet, and CGO-free build | PASS |
| Python suite in an isolated verification environment | PASS, 365 tests |
| Existing serial dispatcher test lane | PASS |

The final race/coverage run used the exact source and tests committed through
`c7041903`, with write containment and temporary Oro state. Its log and profile
are `factory-final.log` and `factory-final-coverage.out` in the verification
directory below. Broad repository verification results are recorded separately.

## Standalone CLI demonstration

The [machine-readable record](2026-10-23-factory-core-cli.json) captures a real
CLI execution outside `go test`. Its temporary repository, binary, policies,
logs, state and delivered checkout were retained at
`/tmp/oro-factory-demo-final.nYBhdi`.

The first worker wrote incorrect subtraction and a misleading, correct ignored
build output. Acceptance executed committed source in a fresh checkout, rejected
it, and supplied a diagnosis. The second worker repaired only after receiving
that diagnosis. Separate acceptance and review approved the exact candidate.

```json
{"id":"standalone-addition","state":"completed","attempts":2,"candidate_sha":"381553c2100aaefee394e023543291319fe9c1fc","target_branch":"delivery"}
```

A fresh checkout of that target returned `5`, `-1`, and `7` for `(2,3)`, `(-4,3)`,
and `(7,0)`. Status and repeated `run` returned the same receipt. Invocation counts
remained worker 2, acceptance 2, reviewer 1. The demonstration's primary branch
and working tree remained unchanged.

The binary SHA-256 is
`503376c2bd4ee3cbb4cc5bbb6f21ad1f406b7d2b2577bd28c21ddd77d4d5cf07`.
Build provenance records the feature worktree at `c11c41fe` with only documentation
edits. Go's embedded VCS revision instead reports the older primary-checkout
revision; both values are retained in the machine-readable record. The proof
uses the explicitly captured build checkout and binary digest, and does not
treat the embedded revision as evidence of the feature source version.

## Repository-wide gate limitations

The full gate is **not green**. After resolving new-file formatting and the
factory NilAway warning, remaining static findings are in unchanged code:

- NilAway findings in `pkg/dbutil/openDB.go`,
  `pkg/dispatcher/review_worker_release.go`, and
  `pkg/dispatcher/startup_recovery.go`.
- Dead exports: `protocol.InitializeBeadSchema`, `storage.CheckRuntimeBudget`,
  `storage.ReserveRuntime`, and `storage.MatchesObserved`.
- `govulncheck` with Go 1.26.5 reports 21 called standard-library vulnerabilities.
  The report identifies fixes in later Go 1.26 patch releases. It also reports
  one imported-package and one required-module vulnerability outside called
  paths. No dependency or toolchain version was changed by this increment.

The gate's fail-fast static tier prevented its normal Go runtime tier. The
equivalent contained runtime/build/security checks were therefore invoked
separately. The broad runtime command passed 41 packages. Eight cases in unchanged
dispatcher tests failed under the verification restrictions: seven could not
execute `/bin/ps`, and one attempted to create shared repository metadata at
`.git/worktrees/cache-a`. These results do not establish failures in an ordinary
uncontained environment. No unrelated dispatcher code was changed.

That loaded run also exposed a timing assumption in the new process test: a
400 ms deadline could correctly expire before its fixture printed readiness.
Commit `c7041903` separates that valid deadline case from a readiness-synchronized
descendant cancellation test. The deterministic delayed-start regression, five
race-enabled repetitions, independent focused race run, and package lint passed.
The final contained factory-only race/coverage run passed and is recorded above.
Verification logs are retained under
`/private/tmp/oro-factory-verify-ldhnhnvl`.

Verification used temporary Oro state with checked-in hooks and beacons, plus
write containment to protect real user configuration. `HOME` and `CODEX_HOME`
were preserved. This containment was a test-harness precaution, not the missing
production execution sandbox.

## Next boundary

The [usage guide](../runbooks/factory-core.md) documents the executable protocol
and resume behavior. The [design](../plans/2026-10-23-factory-core-design.md)
lists cutover prerequisites. Before replacing the live factory, demonstrate
unattended controller restart and descendant containment, real-agent outcomes
against independent held-out acceptance, bounded recurring failures, and reliable
delivery over a sustained run. Resolve the repository gate and toolchain findings
as part of that qualification.
