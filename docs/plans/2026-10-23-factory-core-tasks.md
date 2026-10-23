# Factory core implementation tasks

Design: [autonomous lifecycle](2026-10-23-factory-core-design.md). Design gate passed at `97aa3c91` after requiring pristine, separate candidate checkouts for acceptance and review. Native tracking epic: `oro-factory-core`.

The first increment runs on `feature/factory-core`. Its acceptance establishes an experimental lifecycle with trusted command adapters; production cutover still requires the design's isolation, supervision, real-agent and operational evidence.

| Task | Responsibility | Depends on | Acceptance test |
| --- | --- | --- | --- |
| `oro-fc-contract` | Validate public specification | — | `TestAdmissionContract` |
| `oro-fc-policies` | Freeze external policy programs | contract | `TestImmutablePolicies` |
| `oro-fc-store` | Commit snapshot and event transaction | policies | `TestFactoryStore` |
| `oro-fc-budget` | Reserve lifetime attempts | store | `TestDurableAttemptBudget` |
| `oro-fc-lock` | Exclude concurrent controllers | store | `TestFactoryProcessLock` |
| `oro-fc-git` | Create immutable candidates and pristine checkouts | — | `TestCandidateIsolation` |
| `oro-fc-cas` | Promote by expected-old-SHA CAS | git | `TestPromotionCAS` |
| `oro-fc-runner` | Bound subprocess execution | contract | `TestBoundedExecution` |
| `oro-fc-receipts` | Validate independent evidence | runner | `TestCandidateReceipts` |
| `oro-fc-engine` | Drive autonomous repair and delivery | budget, lock, cas, receipts | `TestFactoryAutonomousDeliveryRecovery` |
| `oro-fc-recovery` | Reconcile interrupted promotion | engine | `TestRestartPromotion` |
| `oro-fc-cli` | Wire run/status into the real CLI | recovery | `TestFactoryCLI` |

Each native task contains its exact test command, inputs, expected behavior, source references, signatures and error boundaries. Each task uses a separate red/green cycle and atomic commit. Independent file scopes can proceed concurrently; overlapping edits and Git index mutations are serialized. The existing dispatcher is not used to execute this replacement work because its completion and recovery semantics are under evaluation.

The engine acceptance also starts two actual controller processes through public `Run` for the same objective. While the first worker is blocked, the second must fail without adding attempts/events or starting an adapter; killing the owner must allow bounded resume. A direct lock-helper test alone cannot establish that the lifecycle acquires the lock.

Final proof requires the end-to-end artifact test, all package tests with race detection and coverage, real CLI dispatch tests, formatting, lint, build, and assessment of the repository quality gate. Record actual results and any baseline failures; never replace the acceptance criterion with a weaker command to manufacture success.
