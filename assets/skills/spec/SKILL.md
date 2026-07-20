---
name: spec
description: Use when the user asks to make a spec, spec out work, run a "deepspec", or brainstorm a validated task graph
---

# Spec

Two modes, auto-detected. Both produce the same output: a validated task dependency graph.

## Scope Assessment

Before mode detection, check: does the request describe **multiple independent subsystems** (e.g., "build X with auth, billing, and notifications")? If yes, decompose first — each subsystem gets its own spec → plan → implementation cycle. Don't try to spec everything at once.

If the request is a single coherent feature (even if cross-cutting), proceed to mode detection.

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

---

## Quick Mode

Research → leverage pass + inline review → decompose. No design doc. No subagent. Same context throughout.

### Step 1 — Research

Read affected code. Mandatory gate: no proposals without citing files read.

- Read the functions/types being changed
- `grep` for all interface implementations, callers, and test mocks
- Note what files must change for compilation

### Step 2 — Internal Leverage + Inline Adversarial Review

Run the Internal Leverage Pass, then self-review in the same context. Run these checks before decomposing:

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

Collaborative design + leverage pass → adversarial validation → task decomposition. Produces a committed design doc.

### Stage 1 — Brainstorm (`brainstorming` skill)

Invoke the `brainstorming` skill. Follow it completely:

- Research prior art first (mandatory gate — no proposals without citing files read)
- One question at a time
- Run the Internal Leverage Pass before finalizing the design
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
