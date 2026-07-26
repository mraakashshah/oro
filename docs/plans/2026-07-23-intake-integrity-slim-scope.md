# Intake Integrity — Slim Scope

**Date:** 2026-07-23
**Supersedes the open scope of:** `docs/plans/2026-07-20-autonomous-work-intake-integrity-design.md`
**Status:** Scope decision — descope the autonomous-proposal-pipeline build down to the enforcement core.

## Why

The epic `oro-intake-integrity` was triggered by **one** incident: a worker on
`oro-bym3` read a stale tmux pane, treated an old dead-export failure as current
evidence, created a malformed P0 bug `oro-8kex` with no acceptance criteria, and
attached it as a blocking dependency on its own assignment. The dispatcher-run
quality gate passed 31s later.

The design's own **non-goal #1** scopes this to *accidental / stale /
confused-deputy / cross-assignment* mutations — explicitly **not** a
hostile-worker sandbox. Against that threat model the invariant that matters is:

> An execution worker cannot create an executable bead or attach a dependency
> whose source is its own assignment.

That invariant is satisfied by a mutation-boundary **denial** plus a
**creation-time contract check** — not by a versioned contract store, an
autonomous classify→repair→materialize→reconcile pipeline, credential-refresh
parity, janitor observability, or a full CLI+UDS+dispatcher+worker+Codex-shim
restart e2e harness.

The epic instead grew to 7 sub-epics (~50 leaf tasks), a 781-line design, and
**59 handoffs over 3 days** with the core packages (`pkg/taskcontract`,
`worker_authority.go`, `proposal_classifier.go`) still nonexistent. That churn
is the tell.

## Adversarial check (codex)

Codex confirmed the cut set but flagged one correction I had wrong: the closed
`identity` epic proves *who* mutated — it does **not deny** the mutation.
Identity without authorization only records the violation. So the narrow
denial slice of `authority` is **load-bearing and must stay**. Also: do not call
the side-effectful `checkBeadReady` from creation; extract its **pure contract
predicate** (preserving exemptions for non-executable and historical beads). And
the regression must exercise **both** denied operations independently — if
creation-validation trips first, the test never proves self-dependency denial.

## Truly-minimal scope — KEEP

| Task | Role | Note |
|---|---|---|
| `oro-intake-mutation-policy` | Deny/route every mutable task command when a worker identity field is present | The denial boundary — covers both "no create" and "no self dep-add" |
| `oro-intake-prompt` | Stop the prompt teaching workers to self-file P0 bugs / self-attach blockers | Cheap behavioral defense-in-depth; removes the incident's instruction |
| `oro-intake-contract-validator` | **Re-scope:** a pure contract predicate + exemptions only | Drop the v2-versioning framing; just the reusable predicate |
| `oro-intake-contract-admission` | **Re-scope:** wire the predicate at **creation** (`runBeadCreate`) | Admission (`checkBeadReady`) blocks execution, not persistence/queue-pollution/false edges — creation-time is necessary |
| `oro-intake-e2e-reject` | **Re-scope:** one focused regression asserting **both** denials independently | Replaces the giant e2e harness |

Already landed (sunk, keep): `identity` (typed dispatcher-owned assignment
identity replacing ambient-env auth) and `evidence` (provenance-bound protocol).

## CUT — descope (close, reversible via reopen)

- **contracts overbuild:** `contract-cli`, `contract-parity` (SQLite/shadow/fake
  parity), `contract-producers`
- **controller (whole autonomous pipeline):** `controller` epic + `classify`,
  `controller-flow`, `materialize-parity`, `materialize-store`, `quarantine`,
  `reconcile`, `repair` — route genuine blockers through the **existing**
  QG-incident pipeline (`evaluateQGFailure`, `ensureQGIncidentBead`) instead
- **authority broad part:** `role-authority` (full role matrix / decomposition +
  dispatcher/ops scopes), `review-readiness` (already covered by `oro-vcw9` /
  `oro-emz2` per design §5.4)
- **e2e harness:** `e2e-contract`, `e2e-final`, `e2e-live`, `e2e-materialize`

## DEFER — park (P1, genuinely deferred not descoped)

- `ops` epic + `health`, `janitor`, `status` — observability/auto-repair; add
  only if quarantines actually accumulate in practice.

## Net

~30 open tasks → **5 core** (kept, 3 of them re-scoped) + 4 deferred. The rest
descoped. If a *malicious* (not accidental) worker threat ever becomes real,
reopen the descoped set — it is not deleted, and its design remains in the
2026-07-20 doc.
