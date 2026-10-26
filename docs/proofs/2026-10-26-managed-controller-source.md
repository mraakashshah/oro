# Managed controller worker source integration

Date: 2026-10-26. Integrated `d3ea8d2d` has exact four-file parity with reviewed
Terra `4a008b48`, following initial implementation `41436cdf`.

The actual public entry now follows:

```text
RunManagedController → controller.drive → controller.attempt
→ managed Git target/export → workspace reservation
→ production source encoding → frozen seed → eight preparation calls
→ ready workspace → persisted base/workspace → candidate-unavailable interruption
```

Entry opens existing managed storage, binds the concrete process-owner guard,
and supports pending delivery state only. It does not admit, activate, reset
attempts, acquire the supervisor lock or load legacy host programs. Constructors
and subsequent configuration reads must match the activated runtime reference.
Reserved/preparing workspace replay cannot redispatch preparation; ready replay
reexports the same SHA and checks running keeper continuity under its deadline.

The production-path test uses a real process-group leader and real admission,
activation, counted scope and grant. A strict recording HostRuntime routes six
Git calls and eight Docker/READY preparation calls through existing adapters,
checking source tar bytes, seed identity, exact keeper and original deadlines.
The durable outcome is one spent attempt, a ready opaque workspace and `running`
state with a discoverable candidate-stage interruption. No receipt, commit or
promotion is manufactured.

Two independent Astra reviews found and verified these followups:

- Typed-nil HostRuntime could panic after spending an attempt. Its behavioral
  RED demonstrated the panic and changed state; the boundary now rejects before
  configuration reads or attempt mutation.
- A configuration-change test did not establish ready keeper continuity. The
  added case reaches real running inspection and rejects changed StartedAt.
- Manually binding another store did not establish public-entry ownership.
  Constructor-reader revocation now verifies the public entry cannot save its
  first attempt, with snapshot/events/reservations unchanged and no host effects.
- Happy-path evidence now requires the candidate interruption stage specifically.

Both final independent reviews accepted the scoped integration without findings.
Worker requested race matrix passed in 42.995 seconds with zero package lint
issues; root independently ran `TestManagedControllerWorkerCheckout` with race
detection, count one and Go 1.26.10, passing in 20.145 seconds. The initial
revision's full non-race package passed in 190.305 seconds; that result does not
qualify the followup or replace integrated race verification.

Evidence: `/tmp/oro-controller-source-red.log`,
`/tmp/oro-controller-typednil-red.log`,
`/tmp/oro-controller-source-followup-green-final.log`, and
`/tmp/oro-controller-source-followup-lint-final.log`.

Full integrated race and gate are running at `d3ea8d2d` in
`/private/tmp/oro-factory-verify-ldhnhnvl/`, prefixes
`controller-source-integrated-full` / `controller-source-integrated-gate`,
sessions `71024` / `82922`. Inspect terminal results before restarting or
attributing the previous revision's full profile to this entry.

This is production code exercised with a recording runtime. CLI/service launch,
native source preparation, candidate execution, independent acceptance, delivery,
retry/promotion/Docker recovery and sustained unattended operation remain
unqualified. Direct engineering coordination is not Oro autonomy evidence.
