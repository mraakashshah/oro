---
name: spec
description: Use when the user asks to make a spec, spec out work, run a "deepspec", or brainstorm a validated task graph
---

# Spec

Two modes, auto-detected. Both produce the same output: a validated task dependency graph.

## Scope Assessment

Before mode detection, split multiple independent subsystems into separate spec → plan → implementation cycles. Treat a cross-cutting but coherent feature as one spec.

## Mode Detection

**Full** (default) — any of: cross-cutting (2+ packages), architectural decisions, unclear requirements, >5 tasks likely.

**Quick** — all of: single package, well-understood change, <=5 tasks, no architectural decisions.

Announce which mode: "Using **quick spec** — single package, well-understood change." or "Using **full spec** — cross-cutting change, needs design doc."

## Internal Leverage Pass

Run this privately after research and before finalizing the design or task graph. It is a decision lens, not a user questionnaire.

- **Direction** — Are we still solving the most useful problem? Which assumptions became stale?
- **Simplification** — What are we optimizing that should not exist? What would a radically simpler or theoretically best product or factory look like?
- **Leverage** — What would cut half the timeline? What would double impact? What changes if money is less constrained than talent?
- **Horizon and scale** — Dream in years, plan in months, evaluate in weeks, ship daily. Is this a prototype for 1x, a build for 10x, or engineering for 100x?

Apply material findings directly to the design, scope, or tasks; do not narrate every answer. If a finding changes the goal, public contract, or hard constraints, ask the user to decide, one material decision at a time, with a recommendation. Do not proceed until it is decided. Continue autonomously when no material decisions remain.

## Internal Premortem

Keep the premortem private and classify verified risks after the leverage pass:

- **Tiger** — a clear threat requiring mitigation
- **Paper Tiger** — looks threatening but existing mitigation makes it acceptable
- **Elephant** — an important concern the design avoids discussing

Verify candidates against code and existing safeguards. Apply verified material risks and mitigations to the design or tasks; use the material-decision gate above only when needed.

---

## Quick Mode

Research → leverage + premortem + inline review → decompose. No design doc or subagent.

### Step 1 — Research

Read affected code. Mandatory gate: no proposals without citing files read.

- Read the functions/types being changed
- `grep` for all interface implementations, callers, and test mocks
- Note what files must change for compilation

### Step 2 — Internal Leverage + Premortem + Adversarial Review

Run the Internal Leverage Pass and Internal Premortem, then self-review in the same context:

| Check | Question |
|-------|----------|
| **Backward compat** | Does this break any existing implementations? Grep all impls/callers. |
| **Test sufficiency** | Happy path, error path, edge cases — all covered? |
| **Missing files** | What files must change that aren't obvious? (test mocks, integration tests) |
| **Blast radius** | What's the worst that happens if this is wrong? Rollback plan? |
| **Out of scope** | What are you explicitly NOT doing? Note follow-ups. |

Write findings inline. If any check reveals the change is bigger than expected → switch to Full mode.

### Step 3 — Decompose (`beadcraft`)

Invoke `beadcraft` in Decompose mode on the research + review findings. Same quality bar as full mode: Rule of Five, full task anatomy, wired dependencies.

Present task tree. Proceed to execution automatically.

### Output

```
oro task show <epic-id>    ← confirmed task tree (no design doc)
```

---

## Full Mode

Collaborative design + reviews → adversarial validation → task decomposition. Produces a committed design doc.

### Stage 1 — Brainstorm (`brainstorming` skill)

Invoke the `brainstorming` skill. Follow it completely:

- Research prior art first (mandatory gate — no proposals without citing files read)
- One question at a time
- Order: Compare approaches → Internal Leverage Pass → brainstorming's single Internal Premortem → finalize; do not run a second premortem pass
- Produce a design doc: `docs/plans/YYYY-MM-DD-<topic>-design.md`
- Commit the design doc before moving to Stage 2

### Stage 2 — Adversarial Review (`adversarial-spec-review` skill) ← GATE

Spawn a **fresh-context subagent** to run `adversarial-spec-review` on the design doc.

```
Task prompt: "Read docs/plans/<design-doc>. Read the actual source files for
affected packages. Run all 6 checks from the adversarial-spec-review skill.
Return the full output in the specified YAML format."
```

- **FAIL** → fix the gaps identified, re-run the review (Ralph Loop)
- **PASS** → continue to Stage 3

Do not skip this stage. Specs without adversarial review ship broken.

### Stage 3 — Decompose (`beadcraft` Decompose mode)

Invoke `beadcraft` in Decompose mode on the validated design doc. Same as Quick Step 3.

Present the task tree. Proceed to execution automatically.

### Output

```
docs/plans/YYYY-MM-DD-<topic>-design.md   ← committed
oro task show <epic-id>                          ← confirmed task tree
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
