# Counted controller startup intent

Date: 2026-10-26. Integrated commit `c8dc8169` has exact two-file content parity
with Terra's final `eadcaa80` (initial source `db08067f`, test followups
`c9bb320d` and `eadcaa80`). Independent Astra review accepted this reservation
precursor with no remaining findings.

The private producer commits controller token, canonical scope and event in one
transaction after checking active runtime identity/event, current supervisor and
generation, original work deadline, frozen backoff, no cleanup and positive
quiescence of every retained scope. Replacement retains the deadline and original
supervisor incarnation. Failure returns no scope and rolls back state.

Review corrected an important test defect: the controller limit is now frozen
before real admission rather than changed on an in-memory journal. Tests use
separate journal connections for concurrency, verify the actual commit-failure
boundary, and cover full counter/scope/event/revision rollback, scope/event caps,
granted/revoked old scopes, activation corruption, expiry, stopping, cleanup,
retirement and reopen.

| Check | Result |
| --- | --- |
| Terra final focused race | PASS, 24.514 s |
| Independent root final branch race | PASS, 25.245 s |
| Integrated core focused race | PASS, 26.031 s package / 32.464 s harness |
| Integrated package golangci-lint | PASS, zero issues / 10.644 s harness |
| Independent Astra final review | Accepted exact precursor, no findings |

The race command used Go 1.26.10:

```sh
go test -race ./pkg/factory \
  -run '^TestManagedControllerReservation$|^TestManagedActivation$|^TestManagedScopeJournal$' \
  -count=1 -timeout=240s
```

Integrated evidence has prefixes `controller-intent-integrated` and
`controller-intent-lint` in `/private/tmp/oro-factory-verify-ldhnhnvl/`.
Terra reported a compiling behavioral RED showing that the generic scope path
accepted an unactivated, uncounted controller. That terminal output was not
archived; it is author-reported evidence. The generic path remains open in this
precursor. The next grant-enforcement leaf must close it and preserve its own
RED evidence.

The prior full factory race/89.0% coverage and 15-pass/one-failure repository gate
apply to `f83e17f9`, before this producer. Only the focused integrated tests and
package lint above qualify the new commit. The five production caller gaps remain;
there is no claim of a passing full repository gate.

No supervisor calls this producer yet. Authenticated HELLO/GO, real launch,
supervision, CLI integration, native execution, delivery and automatic recovery
are outside this acceptance. No Docker/VM/launchd effects occurred.
