# Autoresearch Study — Learnings for Oro

**Repo studied:** [karpathy/autoresearch](https://github.com/karpathy/autoresearch)

**Last updated:** 2026-03-18

---

## Executive Summary

Autoresearch is Andrej Karpathy's framework for autonomous AI-driven research on LLM pretraining. It's radically minimal: 3 files, 1 metric, 1 agent, infinite loop. The agent modifies `train.py`, runs a 5-minute training run, evaluates BPB (bits per byte), keeps or reverts, and repeats — targeting ~100 experiments overnight with zero human intervention.

**Key takeaway:** Autoresearch demonstrates that extreme constraint-driven design (one file, one metric, fixed time budget) can produce remarkably effective autonomous agent behavior. Several of its patterns — program-as-instructions, fixed-budget evaluation, git-as-memory — are worth adopting in oro, particularly for our worker prompt assembly and bead evaluation strategy.

---

## Architecture Comparison

| Dimension | Oro | Autoresearch |
|-----------|-----|-------------|
| **Core model** | Multi-agent orchestrator | Single-agent infinite loop |
| **Scope of change** | Full repo (worktree-isolated) | Single file (`train.py`) |
| **Evaluation** | 19-check QG + ops review | Single metric (BPB) + fast-fail |
| **Time budget** | Unbounded (progress timeout at 10m) | Fixed 5 minutes per experiment |
| **Git strategy** | Worktree per bead + rebase + FF merge | Branch per experiment set + revert on regression |
| **Memory** | FTS5 + handoff payloads + memory extraction | `results.tsv` (untracked) + git log |
| **Task tracking** | Beads with deps, AC, priority | None (linear experiment sequence) |
| **Prompt strategy** | 12-section assembled prompt | `program.md` — single markdown file |
| **Failure handling** | Heartbeat timeout + stuck detection + escalation | OOM/crash → log + revert + continue |
| **Autonomy** | Dispatcher-supervised workers | Fully autonomous ("human might be asleep") |

---

## Patterns Worth Adopting

### 1. Program-as-Instructions (Markdown Skill Pattern)

**What autoresearch does:** The entire agent behavior is encoded in `program.md` — a versioned Markdown file that humans iterate on. The agent reads it once and follows it autonomously. Direct quote from README: "the `program.md` file is essentially a super lightweight 'skill'."

**What oro could adopt:** Our worker prompt is assembled programmatically in `pkg/worker/prompt.go` (12 sections, Go code). This is powerful but opaque — changing worker behavior requires Go code changes + rebuild. A hybrid approach:

- Keep the structural prompt assembly (bead context, memory, git info)
- But load behavioral instructions from a Markdown file (`.oro/worker-program.md` or per-language variants)
- Users could customize worker behavior without touching Go code

**Effort:** Low. Add a `loadWorkerProgram()` function that reads `.oro/worker-program.md` and injects it as a prompt section.

**Risk:** Prompt drift if markdown diverges from code expectations. Mitigate by keeping structural sections in Go and behavioral guidance in markdown.

### 2. Fixed-Budget Evaluation

**What autoresearch does:** Every experiment runs for exactly 5 minutes (wall-clock). This makes results comparable across architectures — you're always measuring "what can you achieve in 5 minutes?"

**What oro could adopt:** Our QG has no time budget — a worker can iterate on QG failures indefinitely (up to 3 retries). A time-boxed evaluation per bead attempt would:

- Prevent workers from spending 30+ minutes on a single QG retry
- Make worker efficiency measurable ("how much can you accomplish in N minutes?")
- Enable fair comparison between models (Opus vs Sonnet vs Haiku on the same bead)

**Implementation idea:** Add `maxMinutesPerAttempt` to bead config (or derive from `estimatedMinutes`). Worker gets a countdown. If time expires before QG passes, it's a "timeout" — revert and try a different approach.

**Effort:** Medium. Requires timer integration in worker loop and dispatcher awareness of time-budget outcomes.

**Risk:** Some beads genuinely need longer. Use as a soft signal, not hard kill. Escalate on timeout rather than discard.

### 3. Git-as-Memory (Commit History as Experiment Journal)

**What autoresearch does:** Each successful experiment is a git commit. Failed experiments are reverted. The commit history IS the memory — the agent reads `git log` to see what's been tried.

**What oro could adopt:** Our workers currently rely on handoff payloads and FTS5 memory for context. But git log is an underutilized memory source:
- Include `git log --oneline -20` of the bead branch in the worker prompt
- Workers can see their own (or predecessor's) commit history
- Commit messages become first-class context (not just audit trail)

**Effort:** Very low. Add a `gitLogSection()` to prompt assembly that runs `git log` in the worktree.

**Risk:** None significant. Additive to existing prompt.

### 4. Single-Metric Clarity

**What autoresearch does:** One primary metric (BPB). Lower is better. Period. The agent never has to balance competing objectives.

**What oro could adopt:** Our acceptance criteria are often multi-dimensional ("tests pass AND lint clean AND no regressions"). Workers sometimes get confused about priority. We could adopt:
- **Primary metric per bead:** Define one "north star" check in AC (e.g., "the new test passes")
- **Secondary checks:** QG handles the rest (formatting, linting) but failure doesn't block the primary metric
- This aligns with the "tiered QG" pattern from the pi-autoresearch study

**Effort:** Low. Requires AC schema change (primary vs secondary) and QG result parsing.

**Risk:** Workers might optimize for primary metric while ignoring secondary quality. Mitigate by making secondary failures block merge (not retry).

### 5. Explicit Autonomy Instructions

**What autoresearch does:** `program.md` explicitly states: "NEVER STOP unless interrupted. The human might be asleep." This removes ambiguity about when to ask for help vs. push forward.

**What oro could adopt:** Our worker prompt has constraints but doesn't strongly assert autonomy. Workers sometimes stall asking implicit questions (via STATUS messages) when they should push through. Adding explicit autonomy directives:
- "You have full authority to implement within the acceptance criteria"
- "If stuck, try a different approach — do not wait for guidance"
- "Escalate via HANDOFF only if you've exhausted 3 approaches"

**Effort:** Trivial. Prompt text change in `prompt.go`.

**Risk:** Overly autonomous workers may go off-rails. But we have QG + review as safety nets.

### 6. Results Ledger (Untracked TSV/CSV)

**What autoresearch does:** `results.tsv` tracks every experiment (commit, metric, status, description) but is NOT committed to git. It's a local experiment journal.

**What oro could adopt:** We don't have a per-bead results ledger. Our bead tracker is in-memory. A simple `.oro/results.tsv` per bead (or global) would:
- Track QG pass/fail history with specific failure reasons
- Track time-per-attempt for efficiency analysis
- Enable offline analysis of worker performance patterns
- Not pollute git history

**Effort:** Low. Write one line per QG attempt from dispatcher.

**Risk:** File management (rotation, cleanup). Mitigate with size limits.

### 7. Revert-on-Regression Strategy

**What autoresearch does:** If BPB doesn't improve, `git reset` to the last good state. Only forward progress is kept. This creates a clean, monotonically-improving branch.

**What oro could adopt:** Our workers commit incrementally but don't revert failed attempts. If a QG retry fails, the worker tries again on top of the failed code. A revert-on-failure strategy would:
- Give each retry a clean slate (revert to last QG-passing state)
- Prevent accumulation of failed patches
- Make the final diff cleaner (only successful changes)

**Implementation:** On QG failure, dispatcher tells worker to `git reset --hard` to the last QG-passing commit (or to the initial worktree state if no pass yet).

**Effort:** Medium. Requires tracking "last known good" commit per bead and modifying retry flow.

**Risk:** Loses potentially useful partial progress. Mitigate by storing the diff of reverted work in the retry feedback.

---

## Patterns Considered but Not Recommended

### Single-File Modification Scope
Autoresearch limits the agent to modifying one file. This is effective for ML research (where `train.py` encapsulates everything) but would cripple software engineering workers that need to touch multiple files (tests, implementation, config, docs).

### No Checkpointing
Autoresearch has no mid-run checkpoints (5-minute runs don't need them). Oro workers can run for 20+ minutes; ralph-loop handoffs serve the same purpose but better.

### Linear Experiment Sequence
Autoresearch runs experiments sequentially (one GPU, one agent). Oro's multi-agent parallel execution is strictly better for software engineering where beads are independent.

### Untracked Results (Not in Git)
While a local results ledger is useful (see recommendation #6), autoresearch's choice to never commit results means they can be lost. For oro, critical state should remain in SQLite or committed files.

---

## Gaps in Autoresearch That Oro Already Solves

1. **No multi-agent** — Oro dispatches N workers concurrently
2. **No dependency management** — Oro's bead deps prevent ordering violations
3. **No quality review** — Autoresearch uses only BPB; oro has 19 checks + ops review
4. **No context exhaustion handling** — Autoresearch agents just stop; oro has ralph loops
5. **No merge coordination** — Autoresearch stays on one branch; oro merges to main
6. **No memory system** — Autoresearch relies on git log; oro has FTS5 + TF-IDF
7. **No escalation** — Autoresearch agents are fully autonomous; oro escalates blockers

---

## Philosophical Observations

### Constraint as Capability
Autoresearch's most powerful insight is that **tight constraints enable autonomy**. By limiting scope to one file, one metric, and a fixed time budget, the agent can be fully autonomous without complex supervision. Oro's workers have more freedom (full repo access, multi-file changes) which requires more supervision (QG, review, escalation). There's a spectrum here — and oro could offer "constrained mode" beads where workers have autoresearch-like restrictions for simpler tasks.

### Program-as-Artifact
The README calls `program.md` "the main thing you iterate on." This inverts the typical relationship where the system is the artifact and prompts are configuration. For research workflows, the program IS the artifact — the system is just infrastructure. Oro could adopt this for research-oriented beads or experimental features.

### Overnight Autonomy
Autoresearch is designed for overnight runs (~100 experiments while sleeping). Oro is designed for supervised sessions. The autoresearch model suggests a "fire-and-forget" mode for oro where:
- User defines beads before bed
- Oro processes them overnight
- User reviews results in the morning
This mostly works today but could be hardened with better auto-recovery and morning summary generation.

---

## Open Questions

- [ ] Should we add a "constrained mode" for simple beads (one-file scope, time-budgeted)?
- [ ] Could `program.md`-style behavioral instructions reduce worker prompt brittleness?
- [ ] Is revert-on-QG-failure better than retry-on-top for reducing QG cycle counts?
- [ ] Should we track per-bead experiment results in a local ledger for analysis?
- [ ] Could explicit "never stop" autonomy instructions reduce unnecessary worker stalls?
