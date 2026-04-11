---
name: spec
description: Use when the user says "make a spec", "spec out X", or "brainstorm X" — turns intent into a validated bead dependency graph
---

# Spec

Two modes, auto-detected. Both produce the same output: a validated bead dependency graph.

## Scope Assessment

Before mode detection, check: does the request describe **multiple independent subsystems** (e.g., "build X with auth, billing, and notifications")? If yes, decompose first — each subsystem gets its own spec → plan → implementation cycle. Don't try to spec everything at once.

If the request is a single coherent feature (even if cross-cutting), proceed to mode detection.

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

Collaborative design → consultation → adversarial validation → bead decomposition. Produces a committed design doc.

### Stage 1 — Brainstorm (`brainstorming` skill)

Invoke the `brainstorming` skill. Follow it completely:

- Research prior art first (mandatory gate — no proposals without citing files read)
- One question at a time
- Produce a design doc: `docs/plans/YYYY-MM-DD-<topic>-design.md`
- Commit the design doc before moving to Stage 2

### Stage 2 — Consultation ← GATE

Pressure-test the committed design doc before it goes to adversarial review. Human-in-the-loop stress test — confirm we're building the right thing, not just the thing asked for. Specs without pressure-testing build the wrong thing thoroughly.

**The six forcing questions.** Ask one at a time. Present your recommended answer with each. Do not batch.

1. **The real problem.** What is the underlying problem the design addresses? Is the stated request a proxy for something more important?
2. **Status quo.** How is this solved today — workaround, manual process, absence? Whose pain does it cause, how much?
3. **Desperate specificity.** Who specifically benefits? Name the user, bead type, failure mode, caller — not a persona.
4. **Narrowest wedge.** Is there a version that ships half the scope for most of the benefit?
5. **Do nothing.** What happens if we ship nothing? Is the status quo bad enough to justify this work?
6. **Future-fit.** In 6 months, does this feel like a durable capability or a local patch? If a patch, is a durable version cheaper to build now?

**Assumption ledger.** Maintain a running list of unresolved decisions. Every user answer may surface new ones — add them. Format:

```
LEDGER
- [ ] DECISION: <what needs to be decided>
      DEPENDS_ON: <other decisions this hinges on — if any>
      RECOMMENDATION: <your answer with reasoning>
      ASK: <the one-question form to put to the user>
```

**Reframe hypothesis.** If the forcing questions suggest the design is solving the wrong problem, propose a reframe:

```
REFRAME
Current design: <what the committed doc says we'll build>
Observed framing: <what the real problem looks like>
Proposed reframe: <what to build instead, and why>
Cost delta: <is the reframe more/less work?>
```

Present once. Let the user confirm, reject, or modify. Don't bulldoze. If the reframe is accepted, **update the design doc in place and commit** before proceeding.

**Exit condition:** NOT "looks good enough." The stage exits only when:
1. User has confirmed the design (original or reframed)
2. Ledger has zero unchecked items
3. Answering the last decision did not add new decisions

### Stage 3 — Adversarial Review (`adversarial-spec-review` skill) ← GATE

Spawn a **fresh-context subagent** to run `adversarial-spec-review` on the design doc.

```
Task prompt: "Read docs/plans/<design-doc>. Read the actual source files for
affected packages. Run all 6 checks from the adversarial-spec-review skill.
Return the full output in the specified YAML format."
```

- **FAIL** → fix the gaps identified, re-run the review (Ralph Loop)
- **PASS** → continue to Stage 4

Do not skip this stage. Specs without adversarial review ship broken.

### Stage 4 — Decompose (`beadcraft` Decompose mode)

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
- Skipping Stage 2 Consultation in Full mode ("the design looks fine")
- Invoking adversarial review before the assumption ledger is drained
- Exiting Consultation on "looks good enough" instead of "ledger drained"
