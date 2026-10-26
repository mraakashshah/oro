# Managed CLI launch and publication foundations

Date: 2026-10-26. These are reviewed startup components, not native execution,
candidate delivery or self-healing qualification.

## Integrated changes and evidence

| Commit / component | Independent root verification |
| --- | --- |
| `8ace9029`, gofumpt-only repair | Exact formatter diff reproduced, then clean |
| `e208d2ba`, verified pending sentinel | Activation/admission race PASS, 29.057 s |
| `9e1e6877`, authenticated supervisor wrapper | Launch/host scope race PASS, 6.677 s |
| `0e5d7fc6`, immutable runtime publication | Publication/config race PASS, 3.319 s |
| `fb2fd519`, recording peer failure cleanup | Peer/wrapper race PASS, 8.092 s |
| `0e38106b`, registered hidden commands | Final command race PASS, 23.149 s |

The wrapper preserves the existing host Invoke/PUBLISHED lifecycle. Controller
startup instead ends after authenticated GO and retains the live controller.
An initial mutex wait could exceed cleanup time; the reviewed fix uses a
context-aware gate, preserves earlier caller deadlines and retains tombstones.
Actual post-HELLO revocation, uncertain bootout/probe, cross-instance replay and
ordinary host delegation are covered. Two independent Astra reviews accepted
final Terra `4b14f85d` without remaining findings.

Publication retains one pinned Oro executable and a bounded canonical runtime
bundle under an immutable intent and lock. A real helper was SIGKILLed while its
stage remained and final Oro was absent, then publication recovered. Active
verification uses the committed reference after original Oro and intent removal,
without recreation or journal changes. Astra accepted Terra `d1753566`.
Publication itself does not activate or release capacity. Its fixture has no real
budget ledger; the public-start integration must prove actual budget wiring.

Hidden `_supervise` and `_controller` commands are registered production callers
of Supervise and RunManagedController. Tests use a real process-group controller,
kernel-authenticated socket, real host wrappers, Git, and pinned recording Docker
and Oro helpers. They reach a ready seeded workspace, preserve one attempt and
return candidate-stage unavailable. A post-GO test now requires proof that the
revocation actually happened and the child failed for the expected grant denial;
zero attempts alone had allowed a false positive. Helpers have bounded lifetimes
and failure-safe cleanup, and sanitized useful service diagnostics stay within
4096 bytes. Complexity/length suppressions were removed by private helper
extraction. Both independent reviews accepted final Terra `7c2a5136`.

The initial command RED was observed in the transcript but not archived. Later
diagnostic/revocation regressions and final race/lint/formatter evidence are in
`/tmp/oro-managed-followup-*` and `/tmp/oro-managed-refactor-*`. Other logs use
`/tmp/oro-runtime-pending-*`, `/tmp/oro-controller-final-*`,
`/tmp/factory-publish-review-*` and `/tmp/oro-peer-*`.

## Broader verification boundaries

At source revision `d3ea8d2d`, the full shuffled factory race passed in 477.308
seconds with 82.3% statement coverage. The first repository gate failed formatting;
the worker had checked lint but not standalone gofumpt. That claim was corrected
and the formatter repair was verified separately.

At `e208d2ba`, the gate passed fifteen checks and failed only seven dead exports.
At `0e5d7fc6`, factory-related CLI shuffled race passed in 78.565 seconds package /
89.672 seconds harness; the gate passed fifteen checks and failed only five dead
exports in 169.172 seconds. Formatting, lint and production NilAway passed.
Remaining APIs were AdmitManaged, ActivateManaged, Supervise,
RunManagedController and DecodeDockerCleanupResult. The new command integration
must acquire its own fresh integrated result; earlier checks do not qualify it.

At `0e38106b`, `managed-commands-integrated-race` failed the exact Docker-create
acknowledgment check in TestFactoryManagedCommands/controller_core (90.472 seconds
package / 101.439 seconds harness). Its 23.9% profile is from a failing run and
does not qualify coverage. Whether instrumented helper telemetry contaminated
stderr is under investigation; the production acknowledgment must remain exact.
The integrated gate passed fifteen checks and failed three dead exports in
163.681 seconds: AdmitManaged, ActivateManaged and DecodeDockerCleanupResult.
Formatting, lint and production NilAway passed; later runtime tiers remain
unqualified. Both jobs are terminal. Environment, log and result files remain in
`/private/tmp/oro-factory-verify-ldhnhnvl/`. Protected harness strips native
integration flags; skipped native
fixtures are not evidence. No native launchctl, Docker, VM or model call ran.

The helper failure was reproduced directly: its instrumented executable emitted
`warning: GOCOVERDIR not set, no coverage data emitted` on stderr. Test-only
`278af5fd`, integrated as `cedfc534`, provides a private helper coverage directory
and asserts exact stdout and empty stderr under the restricted environment.
Astra accepted the isolated fix. Root's fresh integrated shuffled CLI race with
coverage passed in 103.080 seconds package / 115.132 seconds harness, 23.8%
statement coverage (`command-coverage-repaired-race` evidence prefix). This is
CLI coverage, not the full factory package profile. A separate worker cleanup
fixture failure did not reproduce under the protected canonical temporary root:
focused cleanup CLI race with coverage passed in 30.089 seconds harness. Do not
weaken admitted executable canonical-path checks to accommodate fixture paths.

## Test maintenance and remaining work

An earlier failed recording test left an orphaned controller peer. Root verified
its exact executable, arguments, PPID and PGID, terminated only that process and
confirmed absence. The peer fixture now cancels and reaps its owned children
before closing authority and has an independent request deadline. Normal runtime
retirement assertions remain before fallback teardown. This manual maintenance
is not an autonomous recovery trial.

Ordinary host retirement is now implemented in `77497aeb` / `6e5c79f6`
(`oro-mf-host-retirement-bound`). Review found and corrected two initial gaps:
Invoke stripped the original caller deadline, and unregister could remove an
active retiring call before quiescence. The final runtime preserves the original
caller bound, permits cleanup after cancellation, and retains retiring ownership
until positive quiescence. Registration cannot reopen a retired scope. Astra's
final review found no blockers. Root's independent race run at Terra `f3aa5dee`
passed three repetitions of retirement/host-scope tests and ordinary recording
GO/PUBLISHED/result/retirement (3.772 seconds package / 16.694 seconds harness).
Evidence prefix is `host-retirement-reviewed-race`. Integrated command checks at
`6e5c79f6` passed in 41.858 seconds harness, evidence prefix
`host-retirement-integrated-race`; the job is terminal. No native launchd was invoked.
Prepared service state is implemented in `de946982` / `51af5307`. It uses the
separate stable service lock, a complete staged initial bundle, immutable intent
and nonce, and exact bounded registration transitions. Review corrected the
missing factory argv, original-deadline crossing and recovered-stage durability
barriers. The final plist is independently parsed and traverses actual Cobra;
aliases, hardlinks, permissions and corrupt stages are rejected. Astra accepted
Terra `b5fd0453`; root independently passed scoped race with coverage in 6.241
seconds package / 18.442 seconds harness (9.9% CLI statements), prefix
`service-state-reviewed-race`. Recovery was exercised, but no injected fsync
failure or power-loss test ran. This layer performs no bootstrap.

Integrated CLI race/coverage at `51af5307` passed in 117.967 seconds package /
130.401 seconds harness, 25.1% CLI statements. The gate failed only the same three
dead exports in 190.617 seconds (fifteen checks passed). Formatting, lint and
NilAway passed. Both jobs are terminal, prefixes `service-state-integrated-race`
and `service-state-integrated-gate`. The registration consumer is implemented in
Terra `cac818ac` but awaits a correction for intermediate lock-path alias/mode
validation; it is not yet accepted or integrated.

Public start/service registration, uncertain-bootstrap ownership reconciliation,
candidate execution, independent acceptance, native qualification and sustained
real-model unattended delivery/recovery remain outstanding.
