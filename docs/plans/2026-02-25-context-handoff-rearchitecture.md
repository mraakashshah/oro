# Context Handoff Rearchitecture

**Date:** 2026-02-25
**Status:** Design (pre-implementation)

## Context

`inject_context_usage.py` causes agents to quit early — it fires a directive "stop and compact" message before every tool use in every session. Root cause investigation confirmed it is:

1. Redundant — Claude Sonnet 4.5/4.6/Haiku 4.5 already receive native context awareness after every tool call (`<system_warning>Token usage: X/Y; Z remaining</system_warning>`)
2. Incorrect — `budget` is never passed by Claude Code; it always falls back to the hardcoded 200k default
3. Racing with the dispatcher — pane_monitor already monitors context and signals handoff; inject_context_usage fires first, more aggressively
4. Stale post-compaction — reads last transcript usage entry, which is pre-compaction until the next API call writes a fresh entry
5. Unscoped — fires in every session (main, worker, architect, manager) via blank matcher
6. Badly worded — "Do NOT start new multi-step work" causes workers to abandon in-progress tasks

**Goal:** Seamless context boundary transitions. Worker hits threshold → structured state capture → graceful stop → new worker continues. No crashes, no abandoned work.

**Non-goal (this phase):** Memory system integration. Handoffs will be designed to feed memory, but memory quality is a separate concern.

---

## Architecture

Three additive layers. Each is independent; upper layers add capability, not replace lower ones.

### Layer 1 — Native context awareness (always present)

Claude Sonnet 4.5+, Sonnet 4.6, Haiku 4.5 receive after every tool call:
```xml
<system_warning>Token usage: 35000/200000; 165000 remaining</system_warning>
```

At session start:
```xml
<budget:token_budget>200000</budget:token_budget>
```

Workers already know their context state. No hook needed to tell them.

**What we add:** Worker prompt instruction telling agents what to do when remaining context is low:

```
Context thresholds — read from <system_warning> after each tool call.
Soft thresholds match the current model's thresholds.json value
(opus=65%, sonnet=50%, haiku=40% used). Hard stop is +20%.

Soft threshold (opus 65%, sonnet 50%, haiku 40% used):
- Finish your current atomic step, then write a handoff and stop.
- An "atomic step" means: complete the current file edit, run its
  verification (test/lint), and record the result. Do NOT start
  another edit-verify cycle after this one.
- Invoke the create-handoff skill, then stop gracefully.
- Do not start new work after this threshold.

Hard threshold (opus 85%, sonnet 70%, haiku 60% used) — HARD STOP:
- Stop after your very next tool call, regardless of where you are.
- Write a minimal handoff (goal + files modified + next steps).
- Any in-progress work will be captured by the compaction safety net.
```

This replaces inject_context_usage.py's job correctly — via prompt, not hook injection.

### Layer 2 — Dispatcher monitoring (additive when dispatcher present)

Unchanged mechanism, fixed message wording, **registered in settings.json**:

```
context_pct_writer.py (PostToolUse, ORO_ROLE scoped)
  → writes ~/.oro/panes/<role>/context_pct

pane_monitor.go (every 5s)
  → reads context_pct
  → at threshold: writes handoff_requested signal file

pane_handoff_reminder.py (PreToolUse, ORO_ROLE scoped)
  → detects handoff_requested
  → injects: "Dispatcher has signaled context threshold.
              Finish your current step, then write a handoff and stop.
              Do not abandon in-progress work."
```

Message fix: remove "CRITICAL" / "You MUST" language. Replace with "finish current step, then hand off."

> **Wiring fix:** `pane_handoff_reminder.py` is currently dead code — exists as a file but is not
> registered in `settings.json` or `cmd_init.go`. Must be registered in PreToolUse (blank matcher;
> the script itself checks `ORO_ROLE` and exits fast when unset).

This layer is additive and **scoped to architect/manager panes only** (requires `ORO_ROLE` env var). Workers in swarm mode communicate with the dispatcher via protocol messages (MsgHandoff), not file-based signaling — they get Layer 1 + Layer 3. When dispatcher is absent (e.g. `oro work`), Layer 1 handles it alone.

### Layer 3 — Structured state capture (all sessions, PreCompact + SessionStart)

Safety net: if an agent hits Claude Code's compaction threshold without having handed off, PreCompact extracts state from the transcript and saves it to disk. SessionStart (post-compaction) reads the saved state and creates continuation beads.

> **Design note:** PostCompact is not a Claude Code hook event. This layer uses the proven
> PreCompact + SessionStart(compact) pattern from Continuous-Claude-v3. PreCompact fires
> before compaction; SessionStart fires after with `source: "compact"`.

**`pre_compact.py`** (PreCompact hook):

Parses the transcript JSONL directly to extract structured state. Does NOT try to shape compaction output — extracts state itself.

```python
# Input: {"session_id", "transcript_path", "trigger", "cwd"}
# Output: {"continue": true, "systemMessage": "..."}

# 1. Parse transcript_path JSONL:
#    - Last 5 tool calls (tool name + truncated result)
#    - Files modified (from Write/Edit tool calls)
#    - In-progress bead ID (from bd update --status=in_progress calls)
#    - Recent errors (non-zero exit codes, error patterns)
#    - Last assistant message (truncated to 500 chars)
#
# 2. Write structured handoff to:
#    ~/.oro/compaction-state/<session_id>.yaml
#    Format: {current_task, completed, decisions, learnings,
#             files_modified, next_steps, bead_id, errors}
#
# 3. Save to memory via: oro remember --type=compaction_handoff
#
# 4. Return systemMessage reminding post-compaction agent:
#    "Session was compacted. State saved to ~/.oro/compaction-state/<session_id>.yaml.
#     Run bd ready to check for continuation work."
```

**`session_start_compact.py`** (SessionStart hook, matcher: `compact`):

Fires after compaction completes. Reads the state saved by PreCompact and wires it back in.

```python
# 1. Read ~/.oro/compaction-state/<session_id>.yaml
# 2. If bead_id present AND ORO_WORKER=1:
#    - Create continuation bead via bd create:
#      --title="Continue: <current_task>"
#      --type=task --parent=<bead_id>
#      --description="<next_steps>\n\nFiles: <files_modified>"
#    - Dispatcher picks it up for a fresh worker
# 3. Inject as additionalContext:
#    "Resuming after compaction. Previous state:\n<structured summary>"
# 4. Clean up: delete the compaction-state file
```

---

## Context Pruning (separate feature, same PR)

PostToolUse hook on large tool results. Advisory, not mechanical.

**`context_pruner.py`** (PostToolUse):

- Fires when tool result exceeds configurable threshold (default: 8000 chars for Read, 4000 for Bash)
- Injects: "Large tool output ({N} chars). Summarize key findings in your response rather than relying on verbatim content — keeps context lean for future steps."
- Configurable per-tool in `pruning.json`
- Debounced: max once per 3 tool calls

Works because: model sees `[large tool result]` + `[pruning nudge]` together, and its next response naturally summarizes rather than accumulating.

---

## Model Context Window Budgets

Per Anthropic docs (2026-02-25):

| Model family | Context window |
|---|---|
| Claude Opus 4.6, Sonnet 4.6, Sonnet 4.5, Sonnet 4 | 200k (standard), 1M (beta, tier 4) |
| Claude Haiku 4.5 | 200k |

All current models: 200k standard. The hardcoded 200k in `context_pct_writer.py` is coincidentally correct. However, `context_pct_writer.py` should be updated to read from a `context_budgets.json` config to handle model changes and the 1M beta case.

---

## Threshold Alignment (Go worker ↔ Layer 1 prompt)

The Go worker's `watchContext` (worker.go:685-739) polls `.oro/context_pct` and runs a two-stage response via `handleContextThreshold` (worker.go:741-778):

| Stage | Trigger | Action |
|---|---|---|
| First breach | `pct > threshold` (opus=65%, sonnet=50%, haiku=40%) | Set `w.compacted = true`, write `.oro/compacted` flag, continue |
| Second breach | `pct > threshold` AND `w.compacted` | `SendHandoff()` + `killProc()` (SIGKILL) |

**Problem:** The two-stage logic was designed for `inject_context_usage.py` — first breach told the hook to switch from "compact" to "handoff" message. With that hook removed, the `.oro/compacted` flag is dead. Worse, the Go worker SIGKILLs at second breach (~65% for Opus) before the Layer 1 prompt's 70% soft threshold kicks in.

**Fix:** Repurpose `handleContextThreshold` as the **Layer 1 hard stop enforcement**:

```go
// handleContextThreshold — simplified single-stage kill.
// Layer 1 prompt handles soft threshold (thresholds.json value).
// Go worker enforces hard stop (thresholds.json value + 20).
func (w *Worker) handleContextThreshold(ctx context.Context, wt string, threshold int) bool {
    // threshold loaded from thresholds.json + 20 (e.g. opus=85, sonnet=70, haiku=60)
    pct := readContextPct(wt)
    if pct <= threshold {
        return false
    }
    // Single stage: handoff + kill
    _ = w.SendHandoff(ctx)
    w.killProc()
    return true
}
```

Update `thresholds.json` to hard-stop values (current threshold + 20%):

```json
{"opus": 85, "sonnet": 70, "haiku": 60}
```

Hard stop is per-model: Opus at 85% (15% remaining), Sonnet at 70% (30% remaining), Haiku at 60% (40% remaining). Smaller models degrade faster, so they get killed earlier. The Layer 1 prompt uses the same percentages but as a soft threshold 20 points below the hard stop — Opus soft at 65%, Sonnet soft at 50%, Haiku soft at 40%. Remove the two-stage `w.compacted` flag entirely.

---

## What Gets Deleted

- `.claude/hooks/inject_context_usage.py` — entirely removed
- `.claude/hooks/test_inject_context_usage.py` — removed with it
- `assets/hooks/inject_context_usage.py` — asset copy also removed to prevent reintroduction via `oro init`
- `.oro/compacted` flag mechanism — no longer needed (two-stage logic removed from worker.go)
- `DEBOUNCE_FILE = "/tmp/oro-context-warn-ts"` — gone with inject_context_usage
- `w.compacted` bool in worker.go — replaced by single-stage hard stop

---

## What Gets Changed

- `pane_handoff_reminder.py` — message wording: "finish current step, then hand off" not "CRITICAL/MUST". Also **register in settings.json + cmd_init.go** (currently dead code)
- `context_pct_writer.py` — add `context_budgets.json` lookup for budget source
- `pkg/worker/worker.go` — simplify `handleContextThreshold` to single-stage hard stop at `threshold + 20` (remove two-stage `w.compacted` logic). Remove `.oro/compacted` flag write.
- `thresholds.json` — values unchanged (`{"opus": 65, "sonnet": 50, "haiku": 40}`); Go worker derives hard stop as `threshold + 20`
- `pkg/worker/prompt.go` — add Layer 1 threshold instruction (per-model soft from thresholds.json + hard = soft + 20% + atomic step definition)
- `.claude/settings.json` — remove inject_context_usage.py from PreToolUse; register pane_handoff_reminder.py in PreToolUse; add pre_compact.py (PreCompact); add session_start_compact.py (SessionStart, matcher: `compact`); add context_pruner.py (PostToolUse)
- `cmd/oro/cmd_init.go` — mirror all settings.json hook changes in `buildHookConfig()`
- `.claude/skills/context-checkpoint/SKILL.md` + `assets/skills/context-checkpoint/SKILL.md` — update to reference Layer 1 prompt instruction instead of inject_context_usage.py

---

## What Gets Added

| File | Hook | Scope |
|---|---|---|
| `.claude/hooks/pre_compact.py` | PreCompact | All sessions |
| `.claude/hooks/session_start_compact.py` | SessionStart (matcher: `compact`) | All sessions |
| `.claude/hooks/context_pruner.py` | PostToolUse | All sessions |
| `pruning.json` | — | Config |
| `context_budgets.json` | — | Config |

---

## Failure Modes

| Scenario | Coverage | Behavior |
|---|---|---|
| Standalone `oro work`, context hits soft threshold | Layer 1 only | Native system_warning + prompt instruction triggers graceful handoff. Continuation bead created but requires manual pickup (no dispatcher to assign it). |
| Dispatcher worker, context hits soft threshold | Layer 1 + Layer 3 | Native awareness triggers handoff. Worker sends MsgHandoff via protocol. If ignored, PreCompact + SessionStart(compact) capture state and create continuation bead for dispatcher. |
| Architect/manager pane, context hits threshold | Layer 1 + Layer 2 + Layer 3 | All three layers active. Layer 2 provides dispatcher-level escalation via file-based signaling. |
| Agent ignores soft threshold, hits hard stop | Layer 1 (hard stop) + Go worker | Prompt instruction: "Stop after your very next tool call." Go worker's `handleContextThreshold` SIGKILLs at hard threshold (opus 85%, sonnet 70%, haiku 60%) as enforcement backstop. `SendHandoff()` captures state before kill. |
| Agent ignores all prompts, hits compaction | Layer 3 | PreCompact parses transcript, saves state to disk. SessionStart(compact) reads state, creates continuation bead. |
| Model older than Sonnet 4.5 (no native awareness) | Layer 2 + Layer 3 | Layer 1 degrades (prompt instruction becomes advisory without system_warning data). Layer 2 + Layer 3 still functional. |
| Post-compact stale read in context_pct_writer | — | Brief window (~1 tool call) of inaccurate %; dispatcher tolerates this; no agent-visible impact. |

---

## Bead Dependency Order

1. **Delete inject_context_usage.py** — remove `.claude/hooks/inject_context_usage.py`, `test_inject_context_usage.py`, `assets/hooks/inject_context_usage.py`, settings.json PreToolUse entry, cmd_init.go entry
2. **Simplify worker.go threshold** — remove two-stage `handleContextThreshold`, remove `w.compacted` bool, remove `.oro/compacted` flag write. Single-stage hard stop at threshold.
3. **Update thresholds.json** — keep per-model soft values `{"opus": 65, "sonnet": 50, "haiku": 40}` unchanged; Go worker computes hard stop as `threshold + 20`
4. **Wire + fix pane_handoff_reminder.py** — fix message wording AND register in settings.json PreToolUse + cmd_init.go (currently dead code)
5. **Add context_budgets.json** + update context_pct_writer.py budget lookup
6. **Add pre_compact.py + session_start_compact.py** — PreCompact hook + SessionStart(compact) hook + settings.json + cmd_init.go entries
7. **Add context_pruner.py** + pruning.json + settings.json PostToolUse entry + cmd_init.go
8. **Update worker prompt** (Layer 1 threshold instruction: 30% soft + 15% hard + atomic step definition)
9. **Update context-checkpoint skill** — replace inject_context_usage.py references with Layer 1 description (both `.claude/skills/` and `assets/skills/`)

Beads 1-4 are the crash fix (P0). Beads 5-9 are the feature build (P1-P2).

> **Dual-update rule:** Every bead that adds or removes a hook MUST update both
> `.claude/settings.json` AND `cmd/oro/cmd_init.go:buildHookConfig()`.

---

## Premortem (2026-02-25)

### Mitigations applied

1. **PostCompact doesn't exist** — Replaced with PreCompact (transcript parsing) + SessionStart(compact) (continuation bead creation). Proven pattern from Continuous-Claude-v3.
2. **PreCompact systemMessage can't shape compaction output** — PreCompact now extracts state directly from transcript JSONL. Does not attempt to influence compaction summarizer.
3. **"Atomic step" was undefined** — Defined as "complete current file edit + run verification." Added per-model hard stop (soft threshold + 20%).
4. **Failure modes table was inaccurate** — Fixed to show workers get Layer 1 + Layer 3 (not Layer 2). Layer 2 is architect/manager panes only (ORO_ROLE scoped).

### Adversarial review mitigations (2026-02-25)

5. **pane_handoff_reminder.py is dead code** — Now includes registration in settings.json + cmd_init.go as part of bead 4. Was never invoked before.
6. **Threshold mismatch (Go worker vs Layer 1 prompt)** — Repurposed Go worker's `handleContextThreshold` as single-stage hard stop at `threshold + 20` (opus=85%, sonnet=70%, haiku=60%). Removed two-stage `w.compacted` logic. Layer 1 prompt uses thresholds.json values as soft threshold. thresholds.json values unchanged; Go worker derives hard stop.
7. **cmd_init.go not in scope** — Added dual-update rule: every bead that touches hooks must update both settings.json and cmd_init.go:buildHookConfig().
8. **assets/hooks/ stale copies** — Expanded bead 1 to delete assets/hooks/inject_context_usage.py.
9. **worker.go dead code** — Bead 2 removes `.oro/compacted` flag write and `w.compacted` bool from worker.go.
10. **context-checkpoint skill stale** — Added bead 9 to update skill references.

### Accepted risks

- `context_budgets.json` is YAGNI but low-cost insurance for 1M beta context window.
- `context_pruner.py` is unproven advisory nudge — worst case is no effect, easy to remove.
- `oro work` continuation beads require manual pickup (no dispatcher). Acceptable for standalone mode.
- Compaction-state files may accumulate if session_start_compact.py crashes. Acceptable; add TTL cleanup later if needed.
