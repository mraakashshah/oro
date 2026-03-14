---
name: spec
description: Use when the user says "make a spec", "spec out X", or "brainstorm X" — turns intent into a validated bead dependency graph
---

# Spec

Two modes, auto-detected. Both produce the same output: a validated bead dependency graph.

## Mode Detection

**Full** (default) — any of: cross-cutting (2+ packages), architectural decisions, unclear requirements, >5 beads likely.

**Quick** — all of: single package, well-understood change, <=5 beads, no architectural decisions.

Announce which mode: "Using **quick spec** — single package, well-understood change." or "Using **full spec** — cross-cutting change, needs design doc."

---

## Quick Mode

Research → inline review → decompose. No design doc. No subagent. Same context throughout.

### Step 1 — Research

Read affected code. Mandatory gate: no proposals without citing files read.

- Read the functions/types being changed
- `grep` for all interface implementations, callers, and test mocks
- Note what files must change for compilation

### Step 2 — Inline Adversarial + Premortem

Self-review in the same context. Run these checks before decomposing:

| Check | Question |
|-------|----------|
| **Backward compat** | Does this break any existing implementations? Grep all impls/callers. |
| **Test sufficiency** | Happy path, error path, edge cases — all covered? |
| **Missing files** | What files must change that aren't obvious? (test mocks, integration tests) |
| **Blast radius** | What's the worst that happens if this is wrong? Rollback plan? |
| **Out of scope** | What are you explicitly NOT doing? Note follow-ups. |

Write findings inline. If any check reveals the change is bigger than expected → switch to Full mode.

### Step 3 — Decompose (`beadcraft`)

Invoke `beadcraft` in Decompose mode on the research + review findings. Same quality bar as full mode: Rule of Five, full bead anatomy, wired dependencies.

Present bead tree. Proceed to execution automatically.

### Output

```
bd show <epic-id>    ← confirmed bead tree (no design doc)
```

---

## Full Mode

Collaborative design → adversarial validation → bead decomposition. Produces a committed design doc.

### Stage 1 — Brainstorm (`brainstorming` skill)

Invoke the `brainstorming` skill. Follow it completely:

- Research prior art first (mandatory gate — no proposals without citing files read)
- One question at a time
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

Present the bead tree. Proceed to execution automatically.

### Output

```
docs/plans/YYYY-MM-DD-<topic>-design.md   ← committed
bd show <epic-id>                          ← confirmed bead tree
```

---

## Red Flags

- Proposing approaches without citing files read (both modes)
- Using Quick mode for cross-cutting changes ("it's simple enough")
- Skipping adversarial checks in Quick mode ("the change is obvious")
- Running Full adversarial review in the same context that wrote the spec
- Stopping to ask for confirmation instead of proceeding to execution
