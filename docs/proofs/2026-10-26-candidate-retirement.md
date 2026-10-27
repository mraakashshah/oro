# Ready-candidate retirement

Date: 2026-10-26. Reviewed source and scoped recording-runtime evidence, not
native Docker or autonomous model delivery qualification.

## Integrated implementation

`8cb36db8` integrates Terra's source `79463b35`, controller identity precursor
`84b66987`, final adversarial tests through `9b852818`, and fixture `61b429a4`.
Root counterparts are `d1f80a28`, `e4a819ab`, `1e406129`, `76462cc2`,
`88775ef5`, and `8cb36db8`. Root verified an empty production retirement-file
diff against the reviewed worker source. Earlier keeper and dbutil fixes remain
integrated; worker dependency cherry-picks were not duplicated.

The actual managed controller's execute path prepares a distinct candidate,
reserves retirement, invokes the authenticated cleanup helper, decodes its
receipt through `DecodeDockerCleanupResult`, and records retirement. It still
returns `engineer-adapter-unavailable`. No model result, export or delivery is
manufactured by stopping a container.

Reservation atomically binds irreversible `retiring`, one cleanup token/scope,
the exact full-label permit and one fixed cleanup deadline. Only the exact
durable granted parent controller may remain live; every other non-Docker scope
must be quiescent. Ordinary work and generic cleanup remain fenced while a
candidate is retiring. Canonical row/event history checks reject missing,
duplicate, orphaned or inconsistent retirement records.

Permit reload revalidates immutable snapshot/source/runtime and parent identity.
Retirement may begin after work expiry under recovery authority, without
renewing execution grants. Only execute owns a separately bounded cleanup
context: it may detach the inherited objective deadline exactly, while retaining
earlier caller deadlines, explicit cancellation and HardStop. Direct retirement
never detaches its caller context.

Success requires the helper scope already quiescent, the exact stopped lifetime
and mutation acknowledgement, then an atomic retired/target-quiescent record.
Unknown outcomes and failed final commits stay fenced and cannot redispatch or
renew bounds. Keeper and source volume remain unchanged. This leaf neither
removes resources nor reconciles uncertain retirement automatically.

## Verification and failed evidence

Astra accepted frozen production `79463b35`, final tests `9b852818`, and the
separate fixture `61b429a4` without remaining findings. Review first exposed
missing immutable snapshot validation and false-positive rejection tests; both
were corrected before acceptance.

- Terra's corrected combined race/coverage passed in 392.306 seconds, 57.8%
  package statements, covering retirement, cleanup contexts, generic cleanup and
  controller grants. Child coverage is forwarded by bounded per-case leaders.
- Both real-expiry cases passed individually with race detection in 32.159 and
  33.092 seconds. Their admitted cleanup windows demonstrably span objective
  expiry; production policy limits were not changed.
- Terra's engine/candidate fixture race passed in 216.402 seconds, 54.8%; the
  actual cleanup-helper CLI fixture race passed in 20.815 seconds, 8.8%.
- Root's independent tamper/concurrency/rollback/context race passed in 67.490
  seconds package /72.627 seconds harness, 53.5% scoped package coverage.
- Root's complete managed-command fixture race passed in 27.139 seconds package
  /41.305 seconds harness, 8.8% CLI coverage, including real authenticated helper
  transport, recorded Docker inspect/stop/inspect, and revoked-entry denial.
- Root's merged architecture/classifier/grant/context/namespace race passed in
  16.953 seconds harness at `8cb36db8`.

Two predecessor combined runs failed and remain failed evidence: the first at
306.683 seconds exceeded a shared 240-second child budget and expired a short
fixture before ready; the second at 369.177 seconds failed two objective/cleanup
window mismatches. Per-case owned children and explicit admitted fixture windows
fixed these test defects. No production deadline was extended to obtain a pass.
Package/CLI lint and standalone gofumpt were clean in the scoped worker checks.

Root evidence is under `/private/tmp/oro-factory-verify-ldhnhnvl/`, prefixes
`candidate-retire-critical-reviewed`, `candidate-retire-fixture-cli-reviewed`,
and `candidate-retire-merged-smoke`. These profiles are scoped, not full-suite
coverage. The integrated repository gate `candidate-retire-integrated-gate`
finished FAIL in 618.626 seconds: twenty passes, with Go test and Markdown lint
failures. All static checks, build, vet, CGO-free build and govulncheck passed;
no vulnerabilities were reported. Merged coverage was 85.7%. The factory package
completed in 437.740 seconds, 86.2%, failing only event-quota fixtures whose
synthetic history no longer satisfies the retirement fence's contiguity checks.
Reviewed fixture repair `2924a464` (Terra `28294945`) preserves actual quota
assertions and production validation; its final worker race passed in 41.485
seconds, and root independent affected race passed in 47.805 seconds harness.
Two extra Markdown blank lines were removed and complete Markdown lint passed.
This does not retroactively make the failed gate green. The repaired rerun
`candidate-retire-repaired-gate` passed at Go `2924a464` in 610.559 seconds:
twenty-two passes, zero failures, 85.6% merged coverage. The scoped retirement
and event-fixture tasks are closed against that evidence; parent qualification
remains incomplete. The fresh shuffled full factory race
`candidate-retire-integrated-full-race` passed: 1152.917 seconds package /
1158.115 seconds harness, 86.2% coverage at source Go `2924a464`. Its 1500s
package validation limit and production/child authority bounds were unchanged.
No root verification job remains active. The earlier dependency-boundary failure
stays preserved.

Implementation review maps reservation and deadline rules to
`reserveCandidateRetirement`/`candidateCleanupContext`, exact parent and snapshot
rules to `requireRetirementParent`/`validateRetiringReferences`, replay/fence
rules to `requireNoRetiringCandidate`/`validateCandidateRetirementHistory`, and
positive receipt/quiescence/final commit to `retireCandidate`/
`finishCandidateRetirement`. The actual execute caller and registered CLI helper
are exercised by the reviewed controller and command fixtures. These satisfy
the scoped task criteria, with the approved identity-only extraction preserving
ordinary grant checks. They do not establish the operational items below.

## Remaining operational proof

Native guard/candidate retirement, automatic uncertainty reconciliation, real
Terra/Astra adapters, stopped export, protected acceptance/review, exact Git
promotion and sustained injected-failure trials remain unqualified. Direct
implementation agents and recording Docker are not factory autonomy evidence.
No Docker, launchctl, VM, credentials or live model actions ran for this leaf.
