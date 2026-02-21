---
name: spec
description: Use when the user says "make a spec", "spec out X", or "brainstorm X" — turns intent into a validated bead dependency graph
---

# Spec

## Overview

Three-stage pipeline: collaborative design → adversarial validation → bead decomposition. Output is a reviewed design doc and a ready-to-execute bead tree.

## Stages

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

### Stage 3 — Decompose (`bead-craft` Decompose mode)

Invoke `bead-craft` in Decompose mode on the validated design doc:

- Create the epic bead
- Decompose into task beads with full anatomy (Test: | Cmd: | Assert: | Read: | Signature: | Edges:)
- Rule of Five on every bead
- Wire dependencies with `bd dep add`

Present the bead tree to the user. **Wait for confirmation before declaring done.**

## Output

```
docs/plans/YYYY-MM-DD-<topic>-design.md   ← committed
bd show <epic-id>                          ← confirmed bead tree
```

## Red Flags

- Proposing approaches in Stage 1 without citing files read
- Skipping Stage 2 ("the design is obvious")
- Running adversarial review in the same context that wrote the spec
- Moving to Stage 3 before the adversarial review returns PASS
- Presenting the bead tree without waiting for user confirmation
