# Spec: Port Oracle research foundation from oro-26yy to main

**Epic:** `oro-f9cx` (P1) · **Date:** 2026-07-18 · **Author:** operator + Claude
**Supersedes:** the abandoned rebase of `epic/oro-26yy` (see bug `oro-k8zp`)

## Context

Epic `oro-26yy` ("Align Oro agent roles and routing defaults", 31/32 children
closed) never merged to `main`. Its branch `epic/oro-26yy` diverged from `main`
on both sides (main +231 commits, epic +125), so a fast-forward is impossible and
a rebase is blocked by the epic-rebase-child execution gap filed as `oro-k8zp`.

Rather than force a ~140-commit rebase across core dispatcher/worker/ops code, we
port only what is genuinely missing from `main`.

## Verified Current State (2026-07-18, `git diff main..epic/oro-26yy`)

`oro-26yy` is **~95% already on main**. These landed independently (83 patch-id
matched commits + main's own evolution):

| Feature | File(s) | On main? |
|---|---|---|
| Janitor detection | `pkg/janitor/detect.go` | ✅ present |
| Audit prompt / repo manifest | `pkg/ops/audit_prompt.go`, `repo_manifest.go` | ✅ present |
| Routing config / agent matrix | `pkg/config/agent.go`, `.oro/config.yaml`, `pkg/agentmodel` | ✅ present (main more evolved) |
| Merge / retry escalation | dispatcher retry, ops resolver | ✅ present |
| Task-type-in-assignments, memory extractor | dispatcher, cmd/oro | ✅ present |
| **Oracle research-prompt foundation** | `pkg/worker/oracle_prompt.go` + wiring | ❌ **absent — the only real delta** |

The **only** net-new production capability stranded on the epic branch is the
Oracle research-prompt foundation:

- `pkg/worker/oracle_prompt.go` — `AssembleOraclePrompt` / `OraclePromptParams`
  (main has zero oracle production code)
- wiring in `cmd/oro/cmd_work.go` and `pkg/worker/worker.go` (route research-typed
  assignments to the Oracle prompt; enforce a read-only Oracle runtime boundary)

Built by five now-closed `oro-26yy` children: `oro-k3jy`, `oro-c5pf`, `oro-s2mr`,
`oro-j9mh`, `oro-kaj0`.

## Why it matters

The Oracle foundation is **load-bearing for the active epic `oro-9nqr`** ("Give Oro
Oracles context-efficient repository search"). Its open tasks `oro-04gq` (Render
repository navigation in Oracle prompts) and `oro-cxwj` (Map assignment search
context into Oracle prompts) extend an Oracle prompt that does not exist on main.
Both are now wired to depend on this port's acceptance task (`oro-qv8n`).

## Scope

**In:** port the Oracle foundation onto current main (re-integration against main's
evolved `worker.go`/`cmd_work.go`/dispatcher, not a blind cherry-pick); re-verify
that each already-landed feature truly matches on main; retire `oro-26yy` as
superseded.

**Out:** replaying the 42 diverged commits; touching the superseded routing-config
test churn; any `oro-9nqr` feature work beyond the foundation dependency; any
`git merge` of `epic/oro-26yy`.

## Task Graph

```
Port (real work)                          Verify (already-landed)
  oro-0wf4 prompt core ──┬─> oro-bst4 worker routing      oro-e2tc janitor
                         ├─> oro-3nj8 dispatch routing     oro-6c0c audit/manifest
                         └─> oro-hh1t read-only boundary   oro-qbj0 routing matrix
                                    │                       oro-4dwf merge/retry escalation
   oro-bst4/3nj8/hh1t/0wf4 ──> oro-qv8n live acceptance ──> oro-04gq, oro-cxwj (oro-9nqr)
                                    │
  all 9 ─────────────────────> oro-7k02 integrate + retire oro-26yy
```

## Sequencing Rationale

`oro-0wf4` (pure prompt assembly) is a clean additive port with no conflict, so it
goes first and unblocks the three wiring tasks. Wiring tasks are re-integrations
against main's evolved code, done in parallel. `oro-qv8n` proves the end-to-end
path and is the anchor `oro-9nqr` depends on. The four verify tasks are independent
and can run anytime. `oro-7k02` is the final gate: only after everything lands does
`oro-26yy` close as superseded (no branch merge — the epic ref is retired, and the
stale `agent/oro-1nn0-fixes` worktree is cleaned up).

## Definition of Done

1. `AssembleOraclePrompt` exists on main and is invoked for research assignments in
   both the worker and dispatcher/cmd_work paths under a read-only runtime boundary.
2. `go test ./... -run 'Oracle'` passes on main; the live acceptance test passes.
3. All four verify tasks confirm parity (or file follow-ups for any real gap).
4. `oro-9nqr`'s `oro-04gq` and `oro-cxwj` are unblocked.
5. `oro-26yy` is closed as superseded; `epic/oro-26yy` is never merged.
