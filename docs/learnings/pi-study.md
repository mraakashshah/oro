# Pi Ecosystem Study — Learnings for Oro

**Repos studied:**
- [badlogic/pi-mono](https://github.com/badlogic/pi-mono/) — The pi agent framework (monorepo)
- [davebcn87/pi-autoresearch](https://github.com/davebcn87/pi-autoresearch) — Autonomous optimization extension for pi

**Last updated:** 2026-03-18

---

## Executive Summary

Pi is a minimal, aggressively extensible terminal coding agent. Where oro is a full orchestration platform (dispatcher, workers, quality gates, merge coordination), pi is a single-agent SDK that pushes everything to extensions. Pi-autoresearch builds on pi's extension system to create an autonomous optimization loop — single agent, one metric, infinite iteration.

**Key takeaway:** Pi's architectural discipline around minimalism and extensibility offers patterns oro could adopt without changing its multi-agent core. The autoresearch extension demonstrates how persistent experiment state (append-only JSONL) and auto-resume loops can be implemented cleanly.

---

## Architecture Comparison

| Dimension | Oro | Pi | Pi-Autoresearch |
|-----------|-----|----|----|
| **Core model** | Multi-agent orchestrator (dispatcher + N workers) | Single-agent SDK + CLI harness | Single-agent autonomous loop |
| **Extensibility** | Config-driven (langprofile, hooks, .oro/config.yaml) | Extension API (register tools, commands, hooks, UI) | Pi extension + skill separation |
| **Task tracking** | Beads (bd CLI, deps, AC, priority, epic hierarchy) | None built-in (intentional) | Segments + JSONL append-only log |
| **Quality gates** | 19-check parallel QG + ops review + retry budgets | Pre-commit hooks only | Backpressure checks (optional .sh) |
| **Git strategy** | Worktree-per-bead + serialized rebase + FF-only merge | None built-in (bash tool) | Branch-per-experiment + auto-revert |
| **Memory** | FTS5 + TF-IDF embeddings + consolidation | Session JSONL + tree branching + compaction | autoresearch.md (living doc) + JSONL history |
| **Context mgmt** | Ralph loops (handoff at threshold, respawn in same worktree) | LLM-based compaction (summarize older messages) | Auto-resume via agent_end hook (rate-limited) |
| **Prompts** | 12-section assembled prompt (role, bead, memory, rules, TDD, QG, git, etc.) | Layered system prompt (base → custom → append → project context → skills → env) | System prompt injection via before_agent_start hook |

---

## Patterns Worth Adopting

### 1. Extension/Plugin Architecture for Ops Agents

**What pi does:** Everything beyond read/bash/edit/write is an extension. Extensions register tools, commands, event hooks, and custom UI widgets via a typed API.

**What oro could adopt:** Our ops agents (review, merge conflict, diagnosis, escalation) are currently hardcoded spawner types. An extension-like pattern would let us:
- Add new ops agent types without modifying dispatcher core
- Let users define custom ops workflows (e.g., domain-specific review checklists)
- Decouple ops prompt assembly from dispatcher lifecycle

**Effort:** Medium. Requires defining an OpsExtension interface and refactoring `pkg/ops/ops.go` to load registered extensions.

**Risk:** Over-engineering if we don't actually need user-defined ops. Consider only if we see demand.

### 2. Session Tree with Branch Navigation

**What pi does:** All conversation history stored in a single JSONL file with `id` + `parentId` fields, forming a tree. Users can `/fork` to branch, `/tree` to navigate. Compaction summarizes but preserves full tree.

**What oro could adopt:** Our handoff system (ralph loops) currently loses the conversation tree — each respawn is a fresh subprocess. Storing the full conversation tree would let us:
- Debug why a worker went off track (replay the decision tree)
- Resume from any point (not just latest handoff)
- Build analytics on worker decision patterns

**Effort:** Low-medium. We already capture worker logs; structuring them as a tree with parent pointers is incremental.

**Risk:** Storage growth. Mitigated by compaction (pi's approach) or retention limits.

### 3. Append-Only JSONL for Experiment/Bead State

**What pi-autoresearch does:** All experiment results go into `autoresearch.jsonl` — one JSON object per line, append-only. Config changes (segment headers) are also entries. State reconstruction reads the file top-to-bottom.

**What oro could adopt:** Our bead tracker state is in-memory (`bead_tracker.go`) and partially in SQLite. An append-only event log per bead would:
- Survive crashes without SQLite WAL recovery
- Enable replay debugging ("what happened to bead X?")
- Decouple state reconstruction from database schema

**Effort:** Low. Could layer on top of existing SQLite store as a write-ahead narrative log.

**Risk:** Dual source of truth (JSONL + SQLite). Must be clearly subordinate to SQLite or replace it.

### 4. Streaming Tool Output with Parsed Metrics

**What pi-autoresearch does:** `run_experiment` streams subprocess output to the agent in real-time (1s intervals), parses `METRIC name=value` lines automatically, and presents extracted metrics as suggested values for logging.

**What oro could adopt:** Our quality gate currently runs as a black box — worker gets pass/fail. Streaming QG output with parsed check results would let workers:
- See which specific check failed (not just "QG failed")
- Self-correct on the specific failing check before retry
- Reduce QG retry cycles

**Effort:** Medium. Requires modifying QG to emit structured output and worker to parse it.

**Risk:** Prompt bloat if too much QG output injected. Mitigate with tail truncation (pi-autoresearch caps at 10 lines / 4KB).

### 5. Backpressure Checks Orthogonal to Primary Metric

**What pi-autoresearch does:** `autoresearch.checks.sh` runs after benchmark passes but is tracked separately. Check failures revert code but don't affect the primary metric.

**What oro could adopt:** Our QG is monolithic — all 19 checks in one pass. Separating into "correctness checks" (tests, types) and "style checks" (formatting, linting) would let us:
- Accept implementations that pass correctness but fail style (fix style in a follow-up)
- Reduce unnecessary retries for cosmetic issues
- Track which check categories are most problematic

**Effort:** Low. QG already has 4 lanes; promoting this to a first-class tiered result is straightforward.

**Risk:** Accepting partially-passing code may accumulate technical debt. Mitigate with a mandatory style-fix pass before merge.

### 6. Auto-Resume with Rate Limiting

**What pi-autoresearch does:** On `agent_end`, the extension auto-resumes with a fresh agent (rate-limited: once per 5 minutes, max 20 turns). Points new agent at state files.

**What oro could adopt:** Our ralph loops handle context exhaustion, but the handoff is dispatcher-driven. If the dispatcher itself stalls (e.g., between polling cycles), a self-healing resume mechanism could:
- Restart stalled workers without dispatcher intervention
- Provide a fallback if UDS connection drops and reconnect fails
- Rate-limit to prevent thrashing

**Effort:** Low. Add a watchdog timer per worker that triggers respawn if no heartbeat AND no explicit shutdown.

**Risk:** Could conflict with existing health checks. Must coordinate with `checkHeartbeats()`.

### 7. Differential TUI Rendering

**What pi does:** Three-strategy differential rendering (full recompute, component-level dirty tracking, manual hints) with CSI 2026 for atomic screen updates.

**What oro could adopt:** `oro-dash` currently re-renders the full view on each update. Differential rendering would:
- Reduce flicker on large dashboards
- Enable smoother real-time status updates
- Support responsive breakpoints more efficiently

**Effort:** High. Would require rewriting `cmd/oro-dash/` rendering layer.

**Risk:** Not a priority unless dashboard UX becomes a bottleneck.

---

## Patterns Considered but Not Recommended

### Skills as Plain CLI + README (over MCP)
Pi uses "skills" (CLI tools with README documentation) instead of MCP protocol. Oro doesn't use MCP either, but our worker prompts are more structured. Switching to a skills model would lose the type safety and structured context injection we have.

### No Built-In Task Tracking
Pi deliberately avoids task tracking ("confuses LLM"). For oro, beads are foundational — they provide acceptance criteria, dependency graphs, and priority ordering that make multi-agent orchestration tractable. Not applicable.

### Single-Agent by Default
Pi's single-agent model is philosophically different from oro. Multi-agent is oro's raison d'etre. However, pi's approach of making multi-agent an extension rather than core is interesting for future configurability.

### Lazy Provider Loading
Pi loads LLM providers on demand. Oro currently supports only Claude via CLI subprocess. If we add direct API calls, lazy loading would be relevant.

---

## Gaps in Pi That Oro Already Solves

1. **No multi-agent coordination** — Oro's dispatcher + worker pool + bead assignment
2. **No quality enforcement** — Oro's 19-check QG + ops review + retry budgets
3. **No merge coordination** — Oro's serialized rebase + FF-only merge
4. **No work decomposition** — Oro's beads with deps, AC, priority, epics
5. **No memory across sessions** — Oro's FTS5 + TF-IDF + consolidation
6. **No failure recovery** — Oro's heartbeat timeout, stuck detection, escalation
7. **No context exhaustion handling** — Oro's ralph loops with handoff payloads

---

## Open Questions

- [ ] Could pi's extension model inspire a plugin system for oro ops agents?
- [ ] Should we adopt append-only event logging per bead alongside SQLite?
- [ ] Is tiered QG (correctness vs. style) worth the complexity?
- [ ] Could pi-autoresearch's auto-resume pattern improve ralph loop reliability?
