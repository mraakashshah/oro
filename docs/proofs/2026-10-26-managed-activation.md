# Managed activation persistence verification

Date: 2026-10-26. Integrated activation commit `26d3858d`, followed by the
prepared-result guard in `f83e17f9`. This qualifies a persistence prerequisite,
not managed startup, delivery, or self-healing.

Fresh admission now writes bounded canonical pending activation in its admission
transaction. Activation binds the exact runtime path and digest only while the
original admission is pristine and live. Historical reads and identical replay
retain that identity after execution or expiry without granting startup rights.
The future trusted CLI publisher must verify artifact bytes.

Terra began with a compiling behavioral failure: real admission had no
`managed_activation` table. Independent Astra review then found three gaps:
count-only initial-event checks, snapshot revision/canonicality checks outside
the required transaction, and active reads accepting missing or changed history.
Terra reproduced and fixed these before acceptance. The final implementation
validates the original delivery event and the immutable activation event, checks
the snapshot body against its SQL revision in the same transaction, and rejects
missing retained tables. Commit-phase rollback tests assert the error came from
the actual commit boundary. No complexity or nil-safety suppression was added.

The integrated four-file activation content matches Terra's reviewed final
`d00b3c09`; its preceding draft commits were combined into one activation commit.
Root independently ran activation/admission/journal/view race tests successfully
in 27.286 seconds before integration. Terra's final corresponding run passed in
26.984 seconds, with zero package lint issues. Tests also cover concurrency,
capacity, canonical corruption, history, expiry, replay, and rollback.

The separate parser fixes in `1847b365` and `6d572f54` make the decoded keeper
array cardinality and empty Git size-vector invariant explicit. `f83e17f9` checks
for a missing prepared request after retirement/error processing and before
result decoding. Each reproduced its existing production NilAway finding;
Astra reviewed the committed changes without findings.

## Integrated verification

Frozen code: `f83e17f96b39c45aba607b7d1e06d7aa002d0d23`.

| Check | Result |
| --- | --- |
| CLI host-scope, payload and CLI race regressions | PASS, 15.472 s harness |
| Full repository quality gate | FAIL, 169.034 s; 15 checks passed, one failed |
| Repository NilAway and golangci-lint | PASS |
| Full shuffled factory race and coverage | PASS, 409.564 s package; 89.0% statements |

The remaining gate failure is five APIs without production callers:
`AdmitManaged`, `ReadManagedAdmission`, `ActivateManaged`,
`ReadManagedRuntimeReference`, and `DecodeDockerCleanupResult`. Actual startup
and cleanup consumers must resolve these; no dummy references or test-only
annotations were added. Later broad Go gate tiers were not qualified by this
failed gate. Independent package races do not replace them.

Integrated commands used Go 1.26.10, `-race`, count one and isolated writable
caches/state, preserving HOME and CODEX_HOME. The full package command is:

```sh
go test -race -shuffle=on -p 2 -coverprofile=activation-integrated-full.out \
  ./pkg/factory -count=1 -timeout=600s
```

The full harness exited zero in 415.953 seconds at the same frozen code HEAD.
Only the three documentation files for this checkpoint changed during the run;
no production or test source changed. The preceding 89.2% profile belongs to an
older revision and must not be attributed to this larger activation increment.

Evidence environment, streaming log and terminal result files have prefixes
`activation-integrated-full`, `activation-integrated-cli` and
`activation-integrated-gate` in
`/private/tmp/oro-factory-verify-ldhnhnvl/`. Worker behavioral evidence is in
`/tmp/oro-activation-evidence-20261026-terra/`; committed tests preserve the
regressions if temporary files disappear.

No native Docker, VM or launchd qualification, real model delivery trial, or
automatic recovery experiment ran in this increment. Direct Terra engineering
and Astra review are development coordination, not evidence that Oro performed
that coordination autonomously.
