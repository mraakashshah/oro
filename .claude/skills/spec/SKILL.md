---
name: spec
description: Use when the user asks to make a spec, spec out work, run a "deepspec", or brainstorm a validated task graph
---

# Spec

Auto-detect Quick or Full mode. Both produce validated work using the project's existing task backend.

## Scope Assessment

Split independent subsystems into separate spec → plan → implementation cycles. Keep a coherent cross-cutting feature together.

## Task Backend

Invoke `beadcraft` only when the current project is already Oro-managed, shown by project-local instructions, state, or task IDs. Do not initialize Oro just to use it.

Outside an Oro-managed project, use its native tracker or implementation plan with the same acceptance-criteria and dependency quality bar.

## Mode Detection

**Full** (default) — any of: cross-cutting (2+ packages), architectural decisions, unclear requirements, >5 tasks likely.

**Quick** — all of: single package, well-understood change, <=5 tasks, no architectural decisions.

Announce which mode: "Using **quick spec** — single package, well-understood change." or "Using **full spec** — cross-cutting change, needs design doc."

## Internal Leverage Pass

Run this privately after research. It is a decision lens, not a user questionnaire.

- **Direction** — Still the most useful problem? Which assumptions became stale?
- **Simplification** — What should not exist? What is radically simpler or theoretically best?
- **Leverage** — What cuts half the timeline or delivers double impact? What if money is less constrained than talent?
- **Scale** — Dream in years, plan in months, evaluate in weeks, ship daily. Is this 1x, 10x, or 100x work?

Apply material findings without narrating every answer. If one changes the goal, public contract, or hard constraints, ask the user to decide, one material decision at a time, with a recommendation. Do not proceed until it is decided; continue when no material decisions remain.

## Internal Premortem

Keep the premortem private; classify verified risks after the leverage pass:

- **Tiger** — a clear threat requiring mitigation
- **Paper Tiger** — looks threatening but existing mitigation makes it acceptable
- **Elephant** — an important concern the design avoids discussing

Verify against code and safeguards. Apply verified material risks and mitigations; use the decision gate only when needed.

---

## Quick Mode

Research → leverage/premortem/review → decompose. No design doc or subagent.

### Step 1 — Research

Read affected code; cite files before proposing.

- Read changed functions/types
- Find implementations, callers, and mocks
- Note compilation-required files

### Step 2 — Internal Leverage + Premortem + Adversarial Review

Run the Internal Leverage Pass and Internal Premortem, then self-review:

| Check | Question |
|-------|----------|
| **Backward compat** | Does this break any existing implementations? Grep all impls/callers. |
| **Test sufficiency** | Happy path, error path, edge cases — all covered? |
| **Missing files** | What files must change that aren't obvious? (test mocks, integration tests) |
| **Blast radius** | What's the worst that happens if this is wrong? Rollback plan? |
| **Out of scope** | What are you explicitly NOT doing? Note follow-ups. |

Write findings inline. Switch to Full if scope grows.

### Step 3 — Decompose

Apply Task Backend to the findings: `beadcraft` Decompose in Oro; native task graph or plan elsewhere. Keep acceptance criteria and wired dependencies.

Present task tree. Proceed to execution automatically.

### Output

```
Oro:    oro task show <epic-id>    ← confirmed task tree (no design doc)
Native: <project tracker or implementation plan>
```

---

## Full Mode

Collaborative design → adversarial validation → decomposition, with a committed design doc.

### Stage 1 — Brainstorm (`brainstorming` skill)

Invoke `brainstorming` completely:

- Research prior art; cite files before proposing
- One question at a time
- Order: Compare approaches → Internal Leverage Pass → brainstorming's single Internal Premortem → finalize; do not run a second premortem pass
- Produce a design doc: `docs/plans/YYYY-MM-DD-<topic>-design.md`
- Commit the design doc before moving to Stage 2

### Stage 2 — Adversarial Review (`adversarial-spec-review` skill) ← GATE

Have a **fresh-context subagent** run `adversarial-spec-review` on the design doc.

```
Task prompt: "Read docs/plans/<design-doc>. Read the actual source files for
affected packages. Run all 6 checks from the adversarial-spec-review skill.
Return the full output in the specified YAML format."
```

- **FAIL** → fix the gaps identified, re-run the review (Ralph Loop)
- **PASS** → continue to Stage 3

Do not skip this gate.

### Stage 3 — Decompose

Apply Task Backend to the validated design: `beadcraft` only in Oro; native task graph or plan elsewhere. Use Quick's quality bar.

Present the task tree. Proceed to execution automatically.

### Output

```
docs/plans/YYYY-MM-DD-<topic>-design.md   ← committed
Oro:    oro task show <epic-id>           ← confirmed task tree
Native: <project tracker or implementation plan>
```

---

## Red Flags

- Proposing approaches without citing files read (both modes)
- Using Quick mode for cross-cutting changes ("it's simple enough")
- Skipping adversarial checks in Quick mode ("the change is obvious")
- Running Full adversarial review in the same context that wrote the spec
- Stopping for routine confirmation when no material decision remains
- Narrating every leverage question instead of applying material findings
- Engineering for 100x without evidence that the current scale requires it
