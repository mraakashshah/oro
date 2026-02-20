# Ralph Loop Design — Verification at Every Stage

**Date:** 2026-02-19
**Status:** Draft

## Problem

The audit of epic auto-decomposition (oro-9kjh) proved that per-bead quality is necessary but not sufficient. Five beads shipped across multiple sessions, each passed its own QG and review, but the feature doesn't work because `BuildEpicDecompositionPrompt` is never called in production. The security fix (oro-7in4.5) was committed on a branch, bead closed, but never merged to main.

Root cause: no verification loop at any level above the individual bead.

## The Ralph Loop

The Ralph Loop (Ralph Wiggum Technique) is an iterative methodology for autonomous AI coding agents:

1. **Fresh context each iteration** — no accumulated failure baggage
2. **External state management** — all progress tracked outside the context window (files, git, beads)
3. **Machine-verifiable exit condition** — a command that returns pass/fail
4. **Backpressure** — rigorous validation (compiler, tests, lint) rejects bad work and forces retry

Oro already implements this per-bead via `oro work`. What's missing is verification at every other stage.

## The Gate Pipeline

```
DESIGN ──→ [design gate] ──→ DECOMPOSE ──→ [coverage gate] ──→ EXECUTE ──→ [bead gate] ──→ INTEGRATE ──→ [epic gate] ──→ DONE
              ↑ loop                          ↑ loop                         ↑ loop                       ↑ loop
           revise spec                    add beads                     fix code                     fix beads
```

Each gate follows the same shape: **verify → if fail → fix → verify → repeat.**

### Gate 1: Design Gate

**When:** After brainstorming a spec, before decomposing into beads.
**Skill:** `adversarial-spec-review` (Checks 1, 4, 5)
**Verifies:**
- Acceptance criteria are machine-verifiable (Cmd: + Assert:)
- Negative cases and error paths are specified
- No unresolved ambiguity
**Fix mechanism:** Architect revises spec based on review feedback.
**Implementation:** Step 7 of `brainstorming` skill. Interactive — human judgment needed.

### Gate 2: Coverage Gate

**When:** After decomposing spec into beads, before execution begins.
**Skill:** `adversarial-spec-review` (Checks 2, 3, 6)
**Verifies:**
- Every acceptance criterion maps to at least one bead
- Wiring beads exist (integration beads connecting components)
- Every integration point in the codebase is covered by a bead
**Fix mechanism:** Create additional beads. Re-run review.
**Implementation:** Same skill, different focus. Run by a fresh-context subagent.

### Gate 3: Bead Gate (exists)

**When:** During per-bead execution.
**Verifies:** Tests pass, lint clean, review approved.
**Fix mechanism:** Retry with feedback, model escalation.
**Implementation:** `oro work` retry loop + `quality_gate.sh` + ops review. Already built.

### Gate 4: Epic Gate

**When:** After all child beads are closed, before closing the epic.
**Verifies:** The epic's own acceptance test passes against main.
**Fix mechanism:** Spawn diagnostic agent → create fix beads → execute → re-verify.
**Implementation:** Future `oro run <epic-id>` command. ~200 lines of Go.

## Mapping to Ralph's Documentation Stack

| Ralph concept | Oro equivalent |
|---|---|
| `@specs/` | `docs/plans/*.md` + epic acceptance criteria |
| `@fix_plan.md` | beads (`bd ready`, `bd list`) |
| `@AGENT.md` | Worker prompt template (12 sections) + CLAUDE.md |
| The bash loop | `oro work` (per-bead), future `oro run` (per-epic) |
| Subagents | Dispatcher + worker swarm |
| Backpressure | `quality_gate.sh` + ops review + adversarial-spec-review |

## What Ships Now vs Later

**Now (this session):**
- `adversarial-spec-review` skill — covers Gates 1 and 2
- Updated `brainstorming` skill — wires in the adversarial review as a mandatory gate
- Updated `bead-craft` skill — epics require executable acceptance criteria

**Later (separate bead):**
- `oro run <epic-id>` command — automates Gate 4 (epic verification loop)
- `oro verify-coverage <epic-id>` — CLI wrapper for Gate 2

## Key Insight

The per-bead Ralph Loop works because the exit condition is machine-verifiable (QG passes). The epic-level loop failed because the exit condition was accounting ("all children closed") not verification ("feature works"). Making every gate's exit condition machine-verifiable — a command that returns pass/fail — is the single most important change.

## Decisions

- **Gates 1-2 are skill-driven (interactive)** — human judgment is valuable at the design and decomposition level. Automating brainstorming loses more than it gains.
- **Gates 3-4 are command-driven (automated)** — execution and integration verification are mechanical and benefit from Ralph's fresh-context retry loop.
- **The adversarial review runs as a fresh-context subagent** — the person who wrote the spec is the worst reviewer of the spec.
