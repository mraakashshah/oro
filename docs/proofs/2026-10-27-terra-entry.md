# Frozen Terra entry verification

Date: 2026-10-27. This closes only `oro-mf-terra-entry`, not model execution or
unattended factory qualification.

The eleven reviewed worker commits from `e4cdf455` through `9c81b740` are
integrated through `56666d04`. Root confirmed empty cmd/oro and pkg/factory diffs
between the worker final tip and integration, and from integration through the
later documentation tip `ad2c6dc3`. Independent Astra review found no remaining
findings after the credential-alias fixes.

Public start publishes the optional helper identity/reference through
`factoryPublicationModelHelper`; active replay compares the committed input.
`newFactoryCmd` registers `newFactoryModelCmd`. The actual frozen helper reads the
bounded canonical request and verifies active runtime, admitted model path/hash
and its own executable before explicit adapter-unavailable. Readiness and real
cleanup compare optional configuration values, including independently decoded
pointers. No credential content is opened by publication or replay.

Worker final affected CLI covered race passed in 67.491 seconds and factory
covered race in 21.434 seconds. Root independently ran the exact final worker tip
`9c81b74063f8d14d7ee3c66f927fd6810597e843` under the protected harness:

```sh
go test -race -coverpkg=./cmd/oro,./pkg/factory \
  -coverprofile=/private/tmp/oro-terra-entry-final-reviewed-root.out \
  ./cmd/oro ./pkg/factory \
  -run '^(TestFactoryTerraEntry|TestFactoryDockerCleanup|TestManagedAdmissionView|TestManagedHostOldReservation|TestModelResultCodec|TestManagedDockerVolumeCreate|TestFactoryDependencyBoundary)$' \
  -count=1 -timeout=300s
```

Result: PASS, 48.575 seconds harness; CLI 38.408 seconds, factory 22.052 seconds.
These are scoped tests and coverage, not full-package coverage. The actual public
publisher/controller/cleanup fixture and frozen child executable are exercised;
model invocation fields remain shape-only until the counted producer exists.

The merged `terra-entry-integrated-gate` is terminal PASS: 630.606 seconds,
22 checks passed, zero failed, 85.6% merged coverage. It covers formatting, lint,
nil safety, dead exports, architecture, internal/pkg tests, build/vet/security,
Python/docs and serial timing. The default gate omits full cmd tests and native
opt-ins; mutation is disabled. Evidence is under
`/private/tmp/oro-factory-verify-ldhnhnvl/`, using the two mode prefixes above.

Credential containment regressions include symlink aliases, dangling parents,
existing physical case aliases and ambiguous prospective state aliases. Invalid
references fail before allocation; existing disjoint outside parents remain
accepted. Tests use generated references, not real credentials. No native Docker,
launchctl, VM or real provider calls ran. The entry does not establish counted M
authority, live transport, tool execution, autonomous recovery or delivery.
