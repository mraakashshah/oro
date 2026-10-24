# Retire the unused runtime reservation lifecycle

Task: `oro-fg-retire-reservations`. Callgraph inspected at
`84f56489dec10d1e11e66b74e7863c699b608e06`.

## Decision

Remove the closed implementation in `pkg/storage/runtime_reservation.go`,
`runtime_manifest.go`, and `runtime_identity.go`, together with their three
dedicated test files. These APIs have no production entry point. Their tests
exercise a proposed lifecycle that the application never adopted.

The unattended factory needs to persist a random execution-scope identity before
creating a process or container. The retired reservation request instead requires
an existing process identity, legacy assignment and worker identities, and a
second catalog/manifest state machine. Connecting it solely to satisfy the export
scanner would introduce competing lifecycle authority. Removing that unused
implementation keeps the new execution boundary's ownership contract explicit.

This retires Go APIs, not stored data. It is not a completed execution boundary or
a claim that the factory can run unattended.

## Callgraph evidence

Whole-repository Go searches covered exported types, constants, functions and
methods, private implementation helpers, and dedicated test helpers. No caller
outside the six files depends on the removed symbols. The dependency closure is:

| Removed group | References within the closure |
| --- | --- |
| `ReserveRuntime`, reservation request/result/catalog interface, catalog acquire/transition/release methods, typed errors | Reservation implementation and its tests; manifest and identity values supply its journal records |
| Manifest read/write, state transitions, roots, atomic-file helpers, validation, finalization types/interface | Reservation implementation, manifest implementation, and their dedicated tests; the finalizer interface has no production implementation |
| `RuntimeIdentity`, validation and `MatchesObserved`, identity errors | Reservation and manifest validation, plus the three dedicated tests |

The similarly named `config.GitHubAppIdentityConfig` fields named
`RuntimeIdentity` belong to GitHub authentication and remain unchanged.
`ProcessIdentity` is a separate type: its `Matches` and `InspectProcessIdentity`
are still used by pause ownership and weekly cleanup reconciliation. Those
implementations and tests remain.

`OpenRuntime` and `StartLeasedCommand` do not call the retired lifecycle; they
retain their existing lease behavior. They also have no application entry point
and are outside this bounded retirement. This change does not present them as
production integration.

The initial exact `check_dead_exports` function from `scripts/quality_gate.sh`
reported `ReserveRuntime` and `MatchesObserved` as dead exports. It also reported
`CheckRuntimeBudget`, which is deliberately retained for the separate managed
host-admission implementation. No suppression, dummy caller, or renamed dormant
API is added.

## Compatibility boundary

`catalog.go` is unchanged. `CatalogSchemaVersion` remains 5, including the exact
`runtime_reservations` and `runtime_reservation_roots` table definitions and their
constraints. Catalog open performs no new cleanup or migration of reservations.
Existing manifests, roots, evidence, and catalog rows are retained. There is no
automatic reclamation of the retired records.

`TestCatalogPreservesRetiredRuntimeReservations` uses raw SQL, independent of the
removed APIs, to populate active and interrupted reservations with all identity,
path, state, and timestamp columns. Both records have cache, temporary, and
evidence roots. It snapshots every catalog schema object and every reservation
and root column, then checks exact equality across two catalog reopen cycles.
It also checks schema version 5 and byte preservation of eight files: the two
manifest artifacts and one payload under each of six roots.

The compatibility fixture was run successfully before removal. Its purpose is
to preserve existing behavior; the existing dead-export findings supply the
failing check for the retirement itself.

## Verification

Before removal, the compatibility fixture passed with Go 1.26.10. After removal,
the full storage package passed with the race detector, including the unchanged
lease, pause, PID-reuse, controller, budget, and cleanup tests:

```sh
GOTOOLCHAIN=go1.26.10 go test -race ./pkg/storage -count=1 -timeout=120s
```

The storage package passed in 5.739 seconds. The focused CLI race suite passed in
1.826 seconds with this exact test selection:

```sh
GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro \
  -run '^(TestOpenStorageCatalogUsesGlobalCatalogPath|TestStandaloneStoragePauseStopsCommands|TestStorageCleanDevToolsCLI|TestStorageStatusCommand|TestDevCleanupHealthProjection|TestStorageCleanDefaultsToDryRun|TestStorageCleanOroHomeUsesAllowlistedEvidencePlan)$' \
  -count=1 -timeout=120s
```

The exact dead-export check afterward reported one remaining finding,
`CheckRuntimeBudget`; neither retired export remains. The full gate is not yet
green from this slice alone.

Storage and focused CLI checks run through the existing private-state
verification harness and unchanged macOS containment profile. No live factory
is started and no broad CLI cleanup suite is run for this retirement.
