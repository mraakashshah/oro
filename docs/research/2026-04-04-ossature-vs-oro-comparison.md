# Ossature vs Oro: Architectural Comparison

## Context

This report compares two spec-driven autonomous code generation systems:
- **Ossature** — An open-source Python harness that turns specs into build plans executed step-by-step by an LLM
- **Oro** — A Go-based autonomous agent swarm that decomposes work into beads and coordinates multiple Claude workers

Both solve the same fundamental problem: **how do you get an LLM to build software reliably?** They arrive at remarkably different architectures.

---

## Part 1: Ossature in Depth

### Philosophy
Ossature is a **compiler for specifications**. You write a spec (SMD format), optionally an architecture doc (AMD format), and Ossature compiles that into a deterministic build plan that an LLM executes under tight constraints. The human writes the what; the machine figures out the how — but the human reviews the plan before anything runs.

### Core Workflow
```
ossature init → ossature new → ossature validate → ossature audit → ossature build
```

### Spec System (SMD + AMD)

**SMD (Spec Markdown)** — The requirement document:
- Metadata: `@id`, `@status`, `@priority`, `@depends`
- Required sections: Overview, Requirements (with accepts/returns/errors), Examples (with input/output)
- Optional: Goals, Non-Goals, Constraints, Acceptance Criteria, Notes
- Each requirement is a typed function signature with error conditions
- Examples serve as both documentation and implicit test cases

**AMD (Architecture Markdown)** — The structural blueprint:
- Maps to a specific SMD via `@spec`
- Defines Components (name, path, interface code block, dependencies)
- Defines Data Models (name, definition code block)
- Flow diagrams showing data movement
- External dependencies (libraries)
- Multiple AMDs can describe one spec (components merged)

**Key insight:** Specs are the source of truth. AMD is optional — if absent, the LLM infers interfaces from the SMD. This means you can start with just requirements and add architecture later.

### Validation (Structural, No LLM)
- Parse all SMD/AMD files
- Check required fields, enum values, section structure
- Validate dependency graph (all `@depends` targets exist, no cycles)
- Build topological sort into "levels" (parallelizable groups)
- Output: `.ossature/graph.toml`

### Audit (LLM-Powered Review)

**Per-spec audit** — An LLM reviewer checks each spec for:
- ERROR: Contradictions, ambiguities causing incompatible implementations, critical gaps, infeasibility, spec-arch mismatch
- WARNING: Could lead to wrong behavior depending on LLM interpretation
- INFO: Worth clarifying but any reasonable implementation works
- Explicitly does NOT flag: implementation details, obvious solutions, stylistic preferences, missing AMD

**Cross-spec audit** — Interface compatibility check across dependent specs

**Auto-fix loop** — Up to 3 cycles: audit → LLM edits spec → re-audit. The fixer agent has sandboxed tools (read_file, grep_file, edit_file) confined to the spec directory.

**Context generation:**
- Project brief (~200 words, included in every build prompt)
- Per-spec briefs (2-3 sentences each)
- Interface extraction (from AMD) or inference (LLM from SMD)
- Interface propagation: AMD-backed specs have stable interfaces; SMD-only specs cascade when dependencies change

### Plan Generation

**Per-spec planning** — LLM receives the spec, architecture, audit findings, and context file inventory. Produces ordered tasks with:
- Single responsibility (1-3 files max)
- Local dependency indices (within the spec)
- Spec/arch section references
- Verification command (compile, lint, test)
- Output files list

**Global merge** — Per-spec plans combined into global plan:
- Sequential global IDs (001, 002, ...)
- Cross-spec dependencies (first task of spec B depends on last task of spec A)
- `inject_files`: outputs from earlier tasks available to later tasks
- `cross_spec_interfaces`: interface contracts from dependency specs
- Written to `.ossature/plan.toml` — **human-editable before build**

### Build Execution

**Implementer agent** — Per-task LLM with sandboxed tools:
- `write_file`, `edit_file`, `read_file`, `read_lines`, `grep_file`
- `list_files`, `run_command` (validated: no shell expansion, no path traversal)
- `copy_context_file`, `read_context_file` (for binary assets, reference docs)

**Fix loop** — If verification fails:
1. Capture error output
2. Spawn fresh fixer agent (separate from implementer, no accumulated context)
3. Fixer reads files + error, makes targeted edits
4. Re-verify
5. Repeat up to `max_fix_attempts` (default 3)

**Incremental builds:**
- `input_hash` = SHA256(prompt + context_files)
- `output_hash` = SHA256(owned output files)
- Unchanged inputs → skip task
- Changed outputs cascade to dependent tasks
- Editing one spec doesn't rebuild unrelated specs

**Build modes:**
- DEFAULT: pause on failure
- STEP: pause after every task
- AUTO: run to completion, stop on failure
- AUTO_SKIP: run everything possible, skip failures

### State Persistence
Everything saved to `.ossature/`:
- `graph.toml` — dependency graph
- `manifest.toml` — checksums of all spec files
- `plan.toml` — the build plan (editable)
- `state.toml` — per-task input/output hashes
- `tasks/001-slug/prompt.md` — exact prompt sent
- `tasks/001-slug/response.md` — LLM response
- `tasks/001-slug/output.toml` — files created + verification result
- `tasks/001-slug/fix-N-{prompt,response}.md` — fix attempts

### Provider Support
Via pydantic-ai: Anthropic, OpenAI, Google, Mistral, Groq, Cohere, OpenRouter, xAI, DeepSeek, Fireworks, Together, GitHub, Ollama. Per-role model overrides (audit, planner, build, fixer, brief, interface).

### Architecture Summary
```
Human writes SMD/AMD specs
        ↓
Structural validation (no LLM)
        ↓
LLM audit (find spec defects) → auto-fix loop
        ↓
Context generation (briefs, interfaces)
        ↓
LLM plan generation (per-spec → global merge)
        ↓
Human reviews/edits plan.toml
        ↓
LLM executes tasks (sandboxed tools) → fix loop on failure
        ↓
Incremental state tracking (hash-based invalidation)
```

---

## Part 2: Oro in Depth

### Philosophy
Oro is a **software factory** with a deeply structured spec-to-ship pipeline. Before a single line of code is written, work flows through brainstorming → premortem → design doc → adversarial review → beadcraft decomposition. Only then does the autonomous swarm take over: a dispatcher coordinates a pool of Claude-powered workers that execute beads in isolated git worktrees, enforcing quality mechanically (19-check quality gate, ops-agent code review, FF-only merges) and learning across sessions (memory system with FTS5 search, dreaming for synthesis).

### Core Workflow
```
brainstorm → premortem → design doc → adversarial review → beadcraft → oro start → TDD → QG → ops review → merge → dream
```

### The Spec Pipeline (Skills Chain)

Oro's spec pipeline is encoded as a chain of skills, each a mandatory gate. The `workflow-routing` skill detects intent and routes to the right chain:

| User Intent | Skill Chain |
|-------------|-------------|
| Research | `explore` → document findings |
| Plan | `brainstorming` → `premortem` → `writing-plans` |
| Spec/Decompose | `spec` (which invokes brainstorming + adversarial-spec-review + beadcraft) |
| Build | `executing-beads` → `finishing-work` |
| Fix | `systematic-debugging` → `test-driven-development` |

#### Stage 1: Brainstorming (Mandatory Research Gate)

The `brainstorming` skill enforces **research before proposals**:

1. **Research prior art** — Mandatory gate. Must grep `docs/decisions&discoveries.md`, read internal references (`docs/plans/`, related code), search externally when relevant, and present a research summary citing specific files read. Cannot proceed without citing sources.
2. **Understand the idea** — One question at a time (never multiple). Prefer multiple choice. Focus on purpose, constraints, success criteria.
3. **Explore approaches** — 2-3 options with trade-offs. Lead with recommended. YAGNI ruthlessly.
4. **Premortem each decision** — Before committing to ANY design choice, stress-test with Tiger/Paper Tiger/Elephant framework (Shreyas Doshi). A decision without a premortem is a guess.
5. **Present design incrementally** — 200-300 word sections, validate each with user.
6. **Document** — Write to `docs/plans/YYYY-MM-DD-<topic>-design.md` with resolved premortems.
7. **Adversarial review gate** — Mandatory before implementation. Spawn fresh-context subagent.
8. **Implementation handoff** — Transition to `writing-plans` skill.

**Red flags:** Proposing without citing files, jumping to implementation, asking 5 questions at once, committing to a decision without premortem.

#### Stage 2: Premortem (Per-Decision AND Plan-Level)

The `premortem` skill runs at two levels:

- **Decision-level** (during brainstorming) — Quick checklist before each design choice
- **Plan-level** (before implementation) — Deep analysis of the full integrated design

**Risk categories (Shreyas Doshi framework):**
- `[TIGER]` — Clear threat that will hurt if not addressed
- `[PAPER]` — Looks threatening but probably fine
- `[ELEPHANT]` — Thing nobody wants to talk about

**Two-pass verification** — Critical: don't flag risks on pattern-matching alone. Pass 1 gathers hypotheses, Pass 2 verifies each by reading ±20 lines, checking for fallbacks, confirming it's in scope. Every tiger must include evidence with file:line.

#### Stage 3: Adversarial Spec Review (6-Check Gate)

The `adversarial-spec-review` skill is the hardest gate. Run by a **fresh-context subagent** (the spec author is the worst reviewer). The core question:

> "Imagine every bead passes its QG, every review approves, every merge succeeds. The feature still doesn't work. Why?"

**The six checks:**

| # | Check | What it does |
|---|-------|-------------|
| 1 | **Write Acceptance Test First** | Before reading spec in detail, write the epic's machine-verifiable test. If you can't write it, the spec is underspecified (CRITICAL). |
| 2 | **Trace Call Chain (Wiring Audit)** | For every new component, trace the ACTUAL call chain from entry point through real source files. Identify exact location where new code must be called. Must read actual source, not descriptions. |
| 3 | **Requirements Traceability Matrix** | Map every acceptance criterion to: which bead delivers it, which test verifies it. No bead = GAP. No test = GAP. Partial coverage = GAP. |
| 4 | **Negative Space Analysis** | For each component: What happens on error? Bad input? Concurrently? When dependencies are unavailable? What's the cleanup/rollback story? |
| 5 | **Red Team** | Actively construct scenarios where all individual beads pass but the feature doesn't work. Hunt for: unwired components, format mismatches, test-only code, config gaps, import cycles, order dependencies, partial migrations. |
| 6 | **Integration Point Inventory** | List every existing file/function that MUST be touched. Check which beads cover them. Files not in any bead's `Read:` field = GAP. |

**Verdict rules:**
- Any wiring gap → FAIL
- Any red team scenario with no covering bead → FAIL
- Acceptance test can't be written → FAIL
- 3+ uncovered criteria → FAIL
- FAIL → fix gaps, re-run (Ralph Loop). PASS → proceed to decomposition.

**Structured YAML output** with: verdict, acceptance_test, traceability matrix, wiring_gaps, negative_space, red_team_scenarios, integration_points.

#### Stage 4: Beadcraft (Decomposition with Rule of Five)

The `beadcraft` skill decomposes validated specs into beads. Three modes: Decompose, Create, Review.

**Bead Anatomy — every bead requires:**
```
Test: path:FnName | Cmd: test_cmd | Assert: expected
Read: file1.go:Symbol1, file2.go:Symbol2
Signature: func Name(ctx context.Context, arg Type) (Result, error)
Edges: nil input → ErrInvalid; timeout → context.DeadlineExceeded
```

**Rule of Five — 5 critique passes before any bead is emitted:**

| Pass | Question |
|------|----------|
| P1 Zero Ambiguity | Are signatures, types, and error conditions exact? |
| P2 TDD Inputs/Outputs | Does acceptance have concrete test data — specific inputs and expected outputs? |
| P3 Context Minimization | Does `Read:` list exact files and symbols? Nothing more, nothing less? |
| P4 Boundary Check | If this crosses IPC/RPC/package boundaries, are protocol details specified? |
| P5 Adversarial | What would a zero-context worker misunderstand? Fix it. |

If any pass fails, revise and re-run from P1.

**Size heuristics** — Too large if: >7 min estimate, >1 test file or >4 source files, title contains "and", acceptance needs multiple unrelated assertions. If too large → promote to epic, decompose recursively.

**Smell catalog** — No acceptance, vague title, missing Read, stale in_progress, missing estimate, oversized, missing Edges.

#### Stage 5: Spec Mode Selection

The `spec` skill auto-detects mode:

**Quick mode** (single package, <=5 beads, no arch decisions):
- Research → inline adversarial + premortem → beadcraft decompose
- No design doc, no subagent. Same context throughout.

**Full mode** (cross-cutting, unclear requirements, >5 beads):
- `brainstorming` skill (full pipeline) → committed design doc
- `adversarial-spec-review` (fresh-context subagent) → PASS/FAIL gate
- `beadcraft` decompose → bead dependency graph

### Work Decomposition (Beads + Epics)

**Beads** — Atomic work items tracked by `bd` CLI:
- Types: task, bug, feature, epic
- Fields: priority (P0-P4), acceptance criteria (with Test/Cmd/Assert/Read/Signature/Edges), dependencies, status
- Lifecycle: open → in_progress → blocked → closed

**Epics** — Hierarchical decomposition:
- First worker decomposes epic into child beads
- Children merge to epic branch (not main)
- Epic branch merges to main when all children complete + acceptance test passes
- Epic branch created lazily (only when first child completes)

### Execution Pipeline

#### Per-Bead TDD Cycle (executing-beads skill)

Each bead follows a rigid 9-step cycle:

1. **PICK** — `bd ready` → `bd show <id>` → `bd update --status in_progress`
2. **PARSE** — Extract Test/Cmd/Assert from acceptance. If missing or vague: STOP, update bead, ask user.
3. **RED** — Write failing test from acceptance criteria. Verify failure is for expected reason.
4. **GREEN** — Minimal implementation to pass test. No other tests broken.
5. **REFACTOR** — Clean up while green. Plus:
   - **Integration side-effect check** — What fires when this runs? Do tests exercise real chain? Can failure leave orphaned state?
   - **Spec check** — `review-implementation` against acceptance criteria.
6. **QUALITY GATE** — 19-check gate. Fix all issues.
7. **ATOMIC COMMIT** — One commit per bead: `<type>(<scope>): <desc> (bd-<id>)`
8. **CLOSE** — `bd close <id> --reason "Tests pass, gate clean. Commit: <hash>"`
9. **CONTEXT CHECKPOINT** — Monitor context usage. Green (0-10 pairs) → next bead. Orange (16-20) → handoff now.

**Mid-bead decomposition** — If during RED the bead needs multiple unrelated tests: STOP, promote to epic, create children, wire deps, return to step 1 with first child.

#### Work Bead (Worktree-Isolated Execution)

The `work-bead` skill adds worktree isolation to the TDD cycle:

1. Pick → 2. Create worktree (`.worktrees/bead-<id>`) → 3. Parse acceptance → 4. RED → 5. GREEN → 6. REFACTOR → 7. GATE → 8. COMMIT → 9. CLOSE → 10. Rebase onto main (in worktree) → 11. Remove worktree → 12. FF-only merge → 13. Push → 14. Delete branch

### Dispatcher (Central Orchestrator — Swarm Mode)

167KB, ~5200 lines. The brain of the swarm:
- UDS server for worker connections (line-delimited JSON)
- Polls `bd ready` for work items
- Assigns beads to idle workers with context injection:
  - Top 5 relevant memories (BM25 + RRF hybrid search)
  - FTS5 code search results
  - Recent git log on target branch
  - Previous feedback (on retries)
- Monitors heartbeats (10s interval, 45s timeout)
- Manages merge coordination (two-level locking)
- Spawns ops agents for judgment tasks
- Maintains SQLite runtime state (8 tables)
- Enforces quality gates before merge

**State machine:** Inert → Running → (Paused | Stopping) → exit

### Workers (Swarm Mode Task Executors)

Each worker:
1. Spawned as `oro worker --socket /tmp/oro.sock --id w-01`
2. Connects via UDS, sends heartbeat
3. Receives ASSIGN with bead + memory context
4. Spawns `claude -p <prompt>` subprocess
5. 12-section prompt: role, bead, feedback, memories, code search, git log, TDD guidelines, QG process, review process, merge instructions
6. Monitors output for `[MEMORY]` markers (real-time extraction)
7. Tracks context usage (0-100%)
8. Sends DONE, HANDOFF (context exhaustion), or STATUS messages

**Ralph Loop** — On context exhaustion:
- Worker writes HANDOFF (learnings, decisions, files modified, summary)
- Dispatcher assigns new worker to same worktree with handoff context
- Work continues without loss

### Quality Gates (Mechanical Enforcement)

19 integrated checks, generated per-project at `oro init`:
- Go: `go test -race`, `golangci-lint`, `gofumpt`, `goimports`, `go vet`, `govulncheck`
- Shell: `shellcheck`
- Python: `ruff`, `pyright`
- Markdown: `markdownlint`
- YAML: `yamllint`
- JavaScript: `biome`

Gate runs at three points:
1. Worker runs it inside worktree (pre-review)
2. Dispatcher runs it before merge (pre-merge)
3. Epic acceptance: entire epic branch tested together

### Ops Agents (Judgment Tasks)

Short-lived `claude -p` processes for tasks requiring LLM judgment:
- **OpsReview** (Opus) — Code review against acceptance criteria + spec. Outputs APPROVED or REJECTED with feedback.
- **OpsMerge** (Opus) — Merge conflict resolution
- **OpsDiagnosis** (Opus) — Stuck worker diagnosis
- **OpsWriteAC** (Opus) — Generate acceptance criteria for incomplete beads
- **OpsDecompose** (Opus) — Break oversized beads into children
- **OpsDream** (Haiku) — Memory consolidation
- **OpsEscalation** (Sonnet) — One-shot triage
- **OpsEpicFix** (Opus) — Diagnose epic acceptance test failures

Review flow: Worker sends READY_FOR_REVIEW → OpsReview agent → APPROVED (merge) or REJECTED (feedback → retry, max 2 cycles → escalate to Manager)

### Merge Coordination

**Two-level locking:**
1. Per-target rebase lock (serialize rebases to same branch)
2. Global FF lock (serialize all fast-forward merges)

**Flow:** Rebase onto target → resolve conflicts via ops agent → FF-only merge → clean up worktree → close bead

**Linear history enforced** — no merge commits, ever.

### Memory System (Cross-Session Learning)

**Three-tier architecture:**
1. Bead annotations (in bd database)
2. Handoffs (YAML files in worktrees)
3. Project memory (SQLite FTS5 table)

**Memory types:** lesson, decision, gotcha, pattern, preference, summary, self_report

**Search:** BM25 ranking + TF-IDF embeddings + Jaccard dedup + RRF (reciprocal rank fusion)

**Dreaming** — Every 10 completed beads:
- Haiku ops agent reads entire memories table
- Outputs consolidation actions: MERGE duplicates, DELETE contradictions, CREATE patterns
- Executed against memories table

**Staleness:** Memories >7 days annotated with warning; workers reminded to verify old claims

### Worktree Isolation

Each worker operates in `.worktrees/agent/<beadID>`:
- Own branch: `agent/<beadID>`
- Created from base (main or epic branch)
- Cleaned up after merge
- Ralph loop reuses same worktree across worker respawns

### Dashboard & Monitoring

- HTTP dashboard at `:4444` (SSE for real-time updates)
- Mardi Gras TUI dashboard (worker parade, queue depth, completion rate)
- Event log (SQLite `events` table)
- Swarm health endpoint (`/healthz`)

### Architecture Summary
```
Brainstorming (research gate, 2-3 approaches, per-decision premortems)
        ↓
Design doc (docs/plans/YYYY-MM-DD-<topic>-design.md)
        ↓
Adversarial spec review (6 checks: acceptance test, wiring audit, 
  traceability matrix, negative space, red team, integration points)
        ↓  PASS/FAIL gate (Ralph Loop on FAIL)
Beadcraft decomposition (Rule of Five, bead anatomy, size heuristics)
        ↓
Dispatcher assigns beads to workers (memory + code search injection)
        ↓
Worker TDD cycle (RED → GREEN → REFACTOR → side-effect check → spec check)
        ↓
Quality gate (19 mechanical checks)
        ↓
Ops review (Opus agent, APPROVED/REJECTED with feedback)
        ↓
Rebase + FF-only merge (two-level locking)
        ↓
Memory extraction ([MEMORY] markers + LLM extraction)
        ↓
Dreaming (every 10 beads, Haiku consolidation)
```

---

## Part 3: Key Differences

### 1. Spec Formalism

| Dimension | Ossature | Oro |
|-----------|----------|-----|
| **Format** | Structured markdown (SMD/AMD) with machine-parseable metadata (`@id`, `@status`, `@priority`, `@depends`), typed requirements (accepts/returns/errors), required examples | Design docs in `docs/plans/`, beads with structured acceptance anatomy (Test/Cmd/Assert/Read/Signature/Edges) |
| **Validation** | Structural parser validates fields, enums, dependencies — no LLM needed | No parser-level validation on design docs, but beadcraft's Rule of Five is a 5-pass critique loop on every bead before emission |
| **Audit** | LLM audits each spec for contradictions, ambiguity, gaps. Auto-fix loop (3 cycles). Cross-spec interface compatibility check. | 6-check adversarial review by fresh-context subagent: acceptance test writing, wiring audit (trace actual source), traceability matrix, negative space, red team, integration point inventory |
| **Interface contracts** | Extracted from AMD or inferred from SMD; propagated across spec dependencies | Implicit in `Read:` and `Signature:` fields on beads; workers see code search results but no formal interface propagation |

**Verdict:** Different kinds of rigor. Ossature has formal, machine-parseable specs with structural validation — catching format errors without an LLM. Oro has no spec parser but compensates with a *deeper* adversarial review: 6 checks including wiring audits against actual source code, red team scenarios, and traceability matrices. Ossature asks "is the spec well-formed?" Oro asks "if every bead passes, does the feature still work?" — a harder question.

### 2. Planning & Decomposition

| Dimension | Ossature | Oro |
|-----------|----------|-----|
| **Plan generation** | LLM generates per-spec task plans, merged into global plan with cross-spec wiring | Brainstorming skill (research gate, 2-3 approaches, per-decision premortems) → beadcraft decomposition with Rule of Five |
| **Plan editability** | `plan.toml` is human-editable before build | Beads are live objects in bd, editable anytime. Design doc committed to git for review. |
| **Dependency tracking** | Automatic: cross-spec task dependencies, inject_files, interface propagation | Explicit: `bd dep add`, plus beadcraft's dependency wiring patterns (types before implementations, core before integration) |
| **Quality of decomposition** | LLM decides task granularity. Size heuristic: "1-3 files per task." No formal critique loop. | Rule of Five: 5 critique passes per bead. Size heuristics enforced (>7 min → decompose). Smell catalog catches vague titles, missing Read, etc. |
| **Risk assessment** | None at planning stage | Per-decision premortems during brainstorming (Tiger/Paper Tiger/Elephant). Plan-level premortem before implementation. |

**Verdict:** Ossature automates plan generation (LLM does it); Oro makes decomposition a disciplined human+LLM activity with heavier quality gates. Ossature's automatic cross-spec wiring is elegant. Oro's Rule of Five and per-decision premortems catch decomposition defects that Ossature's LLM planner might miss.

### 3. Execution Model

| Dimension | Ossature | Oro |
|-----------|----------|-----|
| **Parallelism** | Sequential tasks (dependency-ordered), single LLM thread | Multi-worker pool (configurable), concurrent execution in isolated worktrees |
| **Isolation** | Sandboxed file operations (output_dir only) | Full git worktree per worker |
| **Context management** | Fresh agent per task (no accumulated context) | Worker accumulates context until exhaustion, then ralph loop (handoff to fresh worker in same worktree) |
| **TDD discipline** | None — LLM writes code, verification command checks it | Mandatory RED → GREEN → REFACTOR cycle. Failing test MUST exist before implementation. |
| **Fix mechanism** | Separate fixer agent with error output + file contents (up to 3 attempts) | Worker retries with QG feedback + ops review feedback (up to 2 rejection cycles, then human escalation) |
| **Tools available** | 9 sandboxed tools: write_file, edit_file, read_file, read_lines, grep_file, list_files, run_command, copy/read_context_file | Full Claude Code toolset (bash, file ops, web search, MCP servers, git commands, etc.) |

**Verdict:** Fundamentally different philosophies. Ossature: controlled, deterministic, sequential — optimize for reproducibility. Oro: powerful, concurrent, autonomous — optimize for throughput while enforcing TDD discipline. Ossature doesn't mandate TDD; Oro does.

### 4. Quality Assurance

| Dimension | Ossature | Oro |
|-----------|----------|-----|
| **Pre-code spec review** | LLM audit (contradictions, ambiguity, gaps) + cross-spec audit + auto-fix loop | 6-check adversarial review (wiring audit, red team, traceability, negative space) + premortems |
| **Build verification** | Per-task verify command (user-specified: compile, lint, test) | 19-check quality gate (auto-generated, language-aware) at 3 points: pre-review, pre-merge, epic acceptance |
| **Code review** | None — verification command is the sole gate | Ops review agent (Opus) checks against acceptance criteria + spec. APPROVED/REJECTED with feedback. |
| **Post-implementation check** | None | Integration side-effect check (5 questions about what fires, real chains, orphaned state). Spec check via review-implementation. |
| **Fix loop** | Fresh fixer agent per attempt (no accumulated context from implementer) | Rejection feedback injected into worker prompt on retry. Max 2 cycles before human escalation. |
| **Merge safety** | N/A (writes to output_dir, no git) | Two-level locking (per-target rebase + global FF), conflict resolution via ops agent |
| **Human escalation** | None — build fails and stops | Manager pane receives escalations for stuck workers, repeated failures |

**Verdict:** Oro's QA is deeper at every stage. Ossature's spec audit is automated (good) but shallow (LLM opinion). Oro's adversarial review reads actual source and constructs failure scenarios. Ossature has no code review — the verify command is the only gate. Oro has mechanical QG + LLM review + human escalation = defense in depth.

### 5. State & Persistence

| Dimension | Ossature | Oro |
|-----------|----------|-----|
| **Build state** | Hash-based (input/output SHA256) in state.toml. Deterministic: same inputs → same hashes → skip. | SQLite (8 tables: events, assignments, memories, rejection_history, etc.) |
| **Prompt/response capture** | Every prompt + response saved as markdown per task | Event log in SQLite; worker prompts assembled but not saved to disk |
| **Incremental builds** | Hash comparison → skip unchanged tasks. Change one spec → only its tasks regenerated. | No incremental concept at build level; each bead execution is fresh. But context-checkpoint skill monitors usage and triggers proactive handoffs. |
| **Cross-session learning** | None — each build is independent | Memory system (FTS5 + TF-IDF + BM25 + dreaming). Workers receive top-5 relevant memories. Staleness warnings for >7 days. |

**Verdict:** Ossature has superior build-level incrementality — hash-based skipping is precise and cheap. Oro has superior organizational learning — the memory system means the factory genuinely gets smarter. These are complementary, not competing.

### 6. Scope & Philosophy

| Dimension | Ossature | Oro |
|-----------|----------|-----|
| **Target** | Greenfield code generation from specs | Continuous autonomous development on existing codebases |
| **Mental model** | Compiler (spec → plan → code) | Factory (spec → beads → workers → ship → learn) |
| **Human role** | Write specs, review plan, run commands | Brainstorm collaboratively, handle escalations, monitor swarm |
| **Git** | None (writes to output directory) | Deep (worktrees, branches, rebase, FF-merge, epic branches) |
| **Multi-model** | 20+ providers via pydantic-ai, per-role overrides | Claude-only (Opus/Sonnet/Haiku for different roles) — prompts deeply optimized for Claude |
| **Scaling** | Single user, single LLM thread | Multi-worker swarm with dispatcher coordination |
| **Learning** | Stateless between builds | Stateful: memory, dreaming, handoffs, staleness tracking |

---

## Part 4: What Oro Could Adopt

### 1. Structural Spec Validation (HIGH VALUE)

**What:** A lightweight parser for design docs that validates structure without an LLM — like Ossature validates SMD fields, enums, and dependency graph before any LLM call.

**Why:** Oro's adversarial review is excellent but expensive (Opus subagent). A cheap, structural pre-pass could catch "design doc missing acceptance test section" or "referenced bead doesn't exist" before spending tokens on the 6-check review. Ossature proves you can catch a whole class of defects with a parser alone.

**How to adopt:** Define a lightweight schema for `docs/plans/*.md` — required sections (Goal, Components, Acceptance Test, Constraints), metadata fields. Write a Go parser that validates before `adversarial-spec-review` runs. Fail fast on structural issues.

**Risk:** Over-formalizing could slow the brainstorming workflow. Keep the schema minimal — validate what matters (acceptance test exists, components listed), ignore style.

### 2. Cross-Spec Interface Propagation (HIGH VALUE)

**What:** When bead A completes and merges, automatically extract its public interface and inject it into the prompt for dependent bead B.

**Why:** Ossature's `inject_files` and `cross_spec_interfaces` ensure downstream tasks see the exact interfaces produced by upstream tasks. Oro's beadcraft puts `Read:` fields on beads (pointing workers to the right files), and code search finds related code, but there's no *guaranteed* interface contract passing. Workers can produce interfaces that compile but don't match what the dependent bead expects. This is exactly the kind of gap the adversarial review's Check 5 (Red Team) hunts for — but it happens at spec time, not at execution time when the interface already exists.

**How to adopt:** After merge, run a lightweight ops agent (OpsInterface, Haiku) or static analysis (`go doc` output) to extract the public API. Store as a bead annotation or in the assignment. Inject into Section 2 of the worker prompt for dependent beads.

### 3. Hash-Based Incremental Execution (MEDIUM VALUE)

**What:** Track input hashes per bead to detect when a retry would produce the same result.

**Why:** Ossature's `input_hash` = SHA256(prompt + context_files) is elegant. If inputs haven't changed, the task is skipped. Oro doesn't have this — every bead execution is fresh, even retries where nothing changed.

**How to adopt:** Compute hash of assembled prompt + injected context at assignment time. Store in `assignments` table. On retry with same hash, flag it to the manager rather than burning another worker context window.

**Risk:** Oro's execution is interactive (workers explore, read, decide), making it less deterministic than Ossature's tool-call model. Useful mainly for detecting "nothing changed, why are we retrying?"

### 4. Prompt/Response Capture (MEDIUM VALUE)

**What:** Save the assembled worker prompt and (optionally) a summary of worker output for each bead attempt.

**Why:** Ossature saves `prompt.md` and `response.md` per task, making debugging trivial. Oro's event log captures status transitions but not what the worker was actually told or produced. When diagnosing why a worker went wrong, you currently have to reconstruct the prompt from dispatcher state.

**How to adopt:** In `worker.go`, write the assembled prompt to `.oro/prompts/<beadID>/attempt-<N>.md` before spawning Claude. Optionally capture Claude's final output summary. Reference from event log.

### 5. Auto-Fix Loop for Specs (LOW-MEDIUM VALUE)

**What:** When the adversarial review finds gaps, automatically fix them instead of presenting findings for manual resolution.

**Why:** Ossature's audit → auto-fix → re-audit loop (up to 3 cycles) is efficient. Oro's adversarial review returns FAIL with findings, then the human/architect fixes and re-runs. Automating the fix step would speed up the spec pipeline.

**How to adopt:** On FAIL verdict, parse the structured YAML findings. For each wiring gap or missing bead, auto-create the fix bead via beadcraft. For spec text issues, auto-edit the design doc. Re-run adversarial review. Ralph Loop already exists conceptually — just automate the fix step.

**Risk:** Auto-fixing specs is riskier than auto-fixing code (specs encode intent, not just behavior). Keep human-in-the-loop for structural changes; auto-fix only for mechanical gaps (missing bead for integration point, missing `Read:` field).

### 6. Per-Spec Briefing (LOW VALUE)

**What:** Auto-generate a ~200-word project brief and per-spec briefs, included in every worker prompt for context.

**Why:** Ossature generates `project-brief.md` and per-spec briefs that give every task LLM context about the overall project. Oro's workers get code search results and memories, but no structured project summary. A brief could reduce "worker doesn't understand the project" failures.

**How to adopt:** Generate a project brief from README + recent design docs during `oro init` or periodically. Inject into Section 1 of the worker prompt.

**Risk:** Oro's memory system already provides contextual project knowledge. A static brief might conflict with dynamic memories. Low incremental value.

---

## Part 5: What Oro Should NOT Adopt

### 1. Sequential Execution Model

**Why not:** Ossature runs one task at a time. Oro's multi-worker concurrency is a fundamental advantage. Sequential execution would be a massive regression for throughput.

### 2. Sandboxed Tool Restrictions

**Why not:** Ossature limits the LLM to 9 file operations + run_command. Oro workers have full Claude Code — bash, file ops, web search, MCP servers, git commands. The richer toolset is essential for working on real codebases (not just greenfield generation). Oro's quality gate + ops review catch tool misuse; pre-restricting tools would cripple workers.

### 3. No Git Integration

**Why not:** Ossature writes to an output directory with no git awareness. Oro's deep git integration (worktrees, branches, rebase, FF-merge, epic branches) is what makes it a continuous development tool rather than a code generator. This is a fundamental identity difference.

### 4. Fresh Agent Per Task (No Context Accumulation)

**Why not:** Ossature creates a new LLM agent for each task. Oro's workers accumulate context within a session and hand off on exhaustion. This accumulated context is valuable — workers learn about the codebase as they work. The ralph loop preserves this learning. Fresh-per-task would mean re-learning the codebase for every bead.

### 5. No Cross-Session Memory

**Why not:** Ossature has no memory system — each build is independent. Oro's memory system (FTS5 + dreaming) is a core differentiator. Without it, every worker starts from zero, rediscovering gotchas session after session. Dreaming synthesizes patterns that no single worker could discover.

### 6. Multi-Provider LLM Support

**Why not:** Ossature supports 20+ providers via pydantic-ai. Oro's Claude-only approach is intentional — worker prompts are deeply optimized for Claude's behavior (memory markers, TDD compliance, tool use patterns, context management). Multi-provider would mean lowest-common-denominator prompts. The right move is to excel on Claude, not to be mediocre on everything.

### 7. Automated Plan Generation (Replacing Brainstorming)

**Why not:** Ossature's LLM generates plans automatically from specs. Oro's brainstorming skill is a *collaborative* process: research prior art, one question at a time, 2-3 approaches, per-decision premortems. This produces higher-quality designs because the human's domain knowledge is injected at every decision point. Automating this would trade design quality for speed — the wrong trade-off for autonomous development where bad designs waste expensive worker compute.

### 8. LLM Spec Audit (Replacing Adversarial Review)

**Why not:** Ossature's spec audit asks an LLM to find contradictions and gaps — essentially a "proofread." Oro's adversarial review is fundamentally harder: trace actual call chains in source code, construct red team scenarios where all beads pass but the feature fails, build a traceability matrix mapping every criterion to a bead and test. Ossature's audit is a *review*; Oro's is an *attack*. Don't downgrade.

### 9. Static Language/Framework Configuration

**Why not:** Ossature requires `output.language` and `output.framework` in config. Oro detects languages dynamically via `langprofile` and generates a language-aware quality gate. Better for polyglot repos.

### 10. TOML-Based Plan Files (Replacing bd)

**Why not:** Ossature's plan.toml is elegant but static. Oro's beads in bd are live objects with status, dependencies, history, and acceptance anatomy. bd supports closing, reopening, dependency management, epic hierarchy, and concurrent access from multiple workers. A TOML file can't do any of that.

---

## Part 6: Synthesis

### Where Ossature Wins

1. **Structural validation without LLM** — Cheap, fast, catches a real class of defects
2. **Deterministic incrementality** — Hash-based state means rebuilds are precise
3. **Full auditability** — Every prompt and response saved; you can replay the exact LLM decision
4. **Cross-spec interface propagation** — Downstream tasks guaranteed to see upstream interfaces
5. **Provider flexibility** — Useful for cost optimization and experimentation
6. **Simplicity** — Easy to reason about, debug, and extend

### Where Oro Wins

1. **Deeper spec validation** — 6-check adversarial review with wiring audits against actual source, red teaming, traceability matrices. Harder and more valuable than LLM audit.
2. **Decomposition quality** — Rule of Five, smell catalog, size heuristics. Every bead is stress-tested before emission. Ossature's planner has no quality gate on task quality.
3. **Risk management** — Per-decision premortems and plan-level premortems. Tiger/Paper Tiger/Elephant framework. Ossature has no risk assessment.
4. **TDD discipline** — Mandatory RED → GREEN → REFACTOR. Ossature's implementer writes code and runs a verify command, but doesn't enforce test-first.
5. **Multi-layered QA** — Mechanical QG (19 checks) + LLM review (Opus) + human escalation. Three independent gates vs. Ossature's one (verify command).
6. **Autonomous scale** — Concurrent workers, self-healing, context continuation.
7. **Organizational learning** — Memory system, dreaming, staleness tracking. The factory gets smarter.
8. **Real-codebase development** — Git-native, works on existing code, epic branch management.

### The Fundamental Difference

Ossature is a **compiler**: spec → plan → code. Deterministic, reproducible, auditable. The spec is the source of truth; the code is the output.

Oro is a **factory**: idea → brainstorm → spec → adversarial review → beads → TDD → QG → review → merge → learn. The process is the source of truth; the code is one output, but learning (memories, patterns, premortems) is another.

Ossature optimizes for **correctness of a single generation run**.
Oro optimizes for **correctness over time across many runs**.

### What To Borrow

The highest-value ideas from Ossature for Oro:

1. **Structural validation** — Add a cheap parser pass before the expensive adversarial review
2. **Interface propagation** — Extract and inject public interfaces between dependent beads
3. **Prompt capture** — Save what workers were told for debugging
4. **Hash-based retry detection** — Don't burn a worker context on an identical retry

The things NOT to borrow are the things that make Ossature simpler but less powerful: sequential execution, sandboxed tools, no git, no memory, no TDD mandate, no adversarial review depth.

---

## Verification

This report was produced by deep exploration of:
- `/Users/as21/codehouse/oro/archive/yap/reference/ossature/` — Full source tree (src/, tests/, docs/)
- `/Users/as21/codehouse/oro/` — Full source tree (cmd/, pkg/, docs/)
- Oro skills: brainstorming, spec, adversarial-spec-review, beadcraft, premortem, workflow-routing, writing-plans, executing-beads, work-bead
- All key source files: parsers, models, audit, build, dispatcher, worker, memory, protocol, ops
