# Codex Hook Schemas — Claude Code Hook Input JSON Reference

> **Purpose**: Document the full JSON input shape for each Claude Code hook event, with explicit
> confirm/deny on `token_usage`, `context_pct`, `rollout_cursor`, and `turn_token`. Drives bead-14
> path selection (approach a vs b for context-aware hook design).
>
> **Sources**: `assets/hooks/*.py`, `.claude/hooks/*.py`, `cmd/oro-search-hook/main.go`,
> `pkg/agentruntime/codex/codex.go`, test fixtures in `*_test.go` and `test_*.py` files.
> Corroborated by claude-mem hook protocol study (`docs/learnings/claude-mem.md`).

---

## TL;DR — Special Fields

| Field | Present in hook stdin? | Where it lives instead |
|---|---|---|
| `token_usage` | **ABSENT** from all events | Transcript JSONL (`message.usage`) |
| `context_pct` | **ABSENT** from all events | `~/.oro/panes/<role>/context_pct` file |
| `rollout_cursor` | **ABSENT** from all events | Not observed anywhere |
| `turn_token` | **ABSENT** from all events | Not observed anywhere |

None of these four fields appear in hook stdin for any event type. Context tracking in Oro reads
them from the transcript file or writes them to pane files — they are never passed through hook
input JSON.

---

## Event: `SessionStart`

Fires when a Claude Code session begins (new, resumed after `/clear`, or post-compaction).

### Input Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `session_id` | string | yes | Unique session identifier |
| `source` | string | yes | `"startup"` \| `"resume"` \| `"clear"` \| `"compact"` |
| `role` | string | no | ORO_ROLE value (e.g., `"architect"`, `"manager"`, `"worker-abc"`). Present when `ORO_ROLE` env var is set by `oro start`. |

### token_usage / context_pct / rollout_cursor / turn_token
All four fields are **ABSENT** from SessionStart input.

### Example Fixture
```json
{
  "session_id": "sess-abc123",
  "source": "startup"
}
```

### Hook Scripts
- `session_start_extras.py` — reads `source` to skip repriming on compact; reads `role` for pane handoff lookup
- `session_start_compact.py` — reads `session_id` and `role` to restore post-compaction state
- `session_start_global.py` — consumes stdin silently (no fields used)
- `enforce_skills.py` — reads nothing from stdin at SessionStart (no-op input)

### Output Shape
```json
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "...",
    "systemMessage": "..."
  }
}
```

---

## Event: `PreToolUse`

Fires before any tool call. The hook can approve (empty JSON `{}`), deny, or inject additional context.

### Input Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `hook_type` | string | yes | Always `"PreToolUse"` |
| `tool_name` | string | yes | e.g., `"Bash"`, `"Read"`, `"Task"`, `"Edit"`, `"Write"`, `"Grep"`, `"WebFetch"` |
| `tool_input` | object | yes | Tool-specific (see sub-tables below) |
| `cwd` | string | no | Bash tool's active working directory. Present when `tool_name == "Bash"`. **Not** the hook process's CWD. |

#### `tool_input` for Bash
| Field | Type | Notes |
|---|---|---|
| `command` | string | The full shell command string |
| `cwd` | string | Bash tool CWD (also top-level `cwd`; both present) |

#### `tool_input` for Read
| Field | Type | Notes |
|---|---|---|
| `file_path` | string | Absolute or relative path |
| `offset` | number | Optional — start line (1-indexed) |
| `limit` | number | Optional — max lines to read |

#### `tool_input` for Task
| Field | Type | Notes |
|---|---|---|
| `prompt` | string | Agent prompt content |
| `run_in_background` | boolean | Whether agent runs in background |
| `subagent_type` | string | Optional — agent type (e.g., `"general-purpose"`) |

### token_usage / context_pct / rollout_cursor / turn_token
All four fields are **ABSENT** from PreToolUse input.

### Example Fixture
```json
{
  "hook_type": "PreToolUse",
  "tool_name": "Read",
  "tool_input": {
    "file_path": "/project/src/main.go",
    "offset": 50,
    "limit": 100
  }
}
```

### Hook Scripts
- `no_cd_guard.py` — reads `tool_name`, `tool_input.command`, top-level `cwd`
- `worktree_guard.py` — reads `tool_name`, `tool_input.command`, top-level `cwd`
- `rebase_worktree_guard.py` — reads `tool_name`, `tool_input.command`
- `enforce_worktree.py` — reads `tool_name`, `tool_input.prompt`, `tool_input.run_in_background`
- `enforce_skills.py` — reads `tool_name`
- `pane_handoff_reminder.py` — reads nothing from stdin (uses env vars only)
- `cmd/oro-search-hook` (Go) — reads `hook_type`, `tool_name`, `tool_input`

### Output Shape
```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "Why the command was blocked",
    "additionalContext": "..."
  },
  "systemMessage": "..."
}
```

Codex requires a non-empty `permissionDecisionReason` beside a nested
`permissionDecision: "deny"`; otherwise it rejects the hook output and fails
open. The top-level `{ "decision": "block", "reason": "..." }` form is the
legacy alternative.

---

## Event: `PostToolUse`

Fires after a tool call completes.

### Input Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `tool_name` | string | yes | Same tool that fired PreToolUse |
| `tool_input` | object | yes | Same as PreToolUse tool_input |
| `tool_result` | string \| object | yes* | Tool's raw output. Used by `prompt_injection_guard.py`, `context_pruner.py`. |
| `tool_output` | string | yes* | Tool output text — used by `validate_agent_completion.py` (Task tool only). |
| `transcript_path` | string | yes | Absolute path to session transcript JSONL |
| `model_key` | string | no | Model family key: `"opus"`, `"sonnet"`, `"haiku"` |
| `budget` | number | no | Context window override in tokens (skips auto-detection from transcript) |

> **`tool_result` vs `tool_output` discrepancy**: Two field names observed for the tool's output.
> `prompt_injection_guard.py` and `context_pruner.py` read `tool_result`; `validate_agent_completion.py`
> reads `tool_output`. Both scripts fail-open when the field is absent. The likely explanation: the
> Claude Code harness sends `tool_response` (per official API) and neither alias matches, but both
> scripts are fault-tolerant. **PostToolUse hooks should read `tool_result` as the primary key;
> `tool_output` should be treated as unreliable unless confirmed against live traffic.**

### token_usage / context_pct / rollout_cursor / turn_token
All four fields are **ABSENT** from PostToolUse hook stdin.

- `token_usage` — Not in hook input. `context_pct_writer.py` derives it by reading the transcript
  JSONL at `transcript_path`, extracting `message.usage.input_tokens +
  cache_creation_input_tokens + cache_read_input_tokens`.
- `context_pct` — Not in hook input. Computed by `context_pct_writer.py` and written to
  `~/.oro/panes/<role>/context_pct`.
- `rollout_cursor` — **ABSENT**. Not observed in any hook script, test, or fixture in this repo.
- `turn_token` — **ABSENT**. Not observed in any hook script, test, or fixture in this repo.

### Transcript JSONL Format (read by context_pct_writer)
The transcript file contains one JSON object per line. Usage data is in assistant messages:
```json
{
  "role": "assistant",
  "message": {
    "model": "claude-opus-4-7",
    "usage": {
      "input_tokens": 95000,
      "cache_creation_input_tokens": 10000,
      "cache_read_input_tokens": 5000,
      "output_tokens": 800
    }
  }
}
```

### Example Fixture
```json
{
  "tool_name": "Bash",
  "tool_input": { "command": "ls -la" },
  "tool_result": "total 16\ndrwxr-xr-x ...",
  "transcript_path": "/Users/user/.claude/sessions/sess-abc123.jsonl",
  "model_key": "sonnet"
}
```

### Hook Scripts
- `context_pct_writer.py` — reads `transcript_path`, `budget`; ignores tool output
- `context_pruner.py` — reads `tool_name`, `tool_result`
- `prompt_injection_guard.py` — reads `tool_name`, `tool_result`
- `validate_agent_completion.py` — reads `tool_name`, `tool_input.prompt`, `tool_output`
- `.claude/hooks/compact_trigger.py` — reads `model_key`
- `.claude/hooks/auto-format.sh` — reads nothing from stdin

### Output Shape
```json
{
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "additionalContext": "..."
  }
}
```

---

## Event: `Stop`

Fires when the Claude Code session is ending. **Cannot inject context** — fires after the final
response is already sent. Output is advisory only.

### Input Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `session_id` | string | yes | Session being stopped |
| `transcript_path` | string | yes | Final transcript path |

> `stop-checklist.sh` does not parse stdin — it outputs `{}` unconditionally. The fields above are
> inferred from the claude-mem.md reference (claude-mem's Stop hook reads transcript data) and the
> Claude Code hook contract.

### token_usage / context_pct / rollout_cursor / turn_token
All four fields are **ABSENT** from Stop hook stdin.

### Example Fixture (inferred)
```json
{
  "session_id": "sess-abc123",
  "transcript_path": "/Users/user/.claude/sessions/sess-abc123.jsonl"
}
```

### Hook Scripts
- `stop-checklist.sh` — outputs `{}`, does not parse stdin

### Output Shape
```json
{
  "continue": true
}
```
No context injection is possible because the session has ended. The explicit `continue: true`
keeps the hook fail-open if reused for Codex `Stop` or `UserPromptSubmit` input shapes.

---

## Event: `UserPromptSubmit`

Fires when the user submits a prompt. No Oro hook is registered for this event. Schema
documented from the claude-mem reference study only.

### Input Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `session_id` | string | yes | Current session |
| `prompt` | string | yes | The user's message text |

### token_usage / context_pct / rollout_cursor / turn_token
All four fields are **CONFIRMED ABSENT** from UserPromptSubmit input.

UserPromptSubmit fires before any model call, so there is no token usage to report at that point.
No usage data, no context percentage, no rollout cursor, no turn token.

### Hook Scripts
None registered in Oro (`.claude/settings.json` has no `UserPromptSubmit` key).

### Output Shape (if a hook were registered)
```json
{
  "continue": true
}
```

Oro hooks registered on `UserPromptSubmit` must not block the user's prompt submission. If Codex
skips a `Stop` hook during server-side compaction, Oro relies on next-turn handoff capture rather
than Stop-time injection.

---

## Bonus: `PreCompact`

Fires before Claude Code runs context compaction. Documented because Oro actively uses it.

### Input Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `session_id` | string | yes | Session being compacted |
| `transcript_path` | string | yes | Transcript to compact |
| `trigger` | string | yes | Compaction trigger reason |
| `cwd` | string | yes | Current working directory |

### Hook Scripts
- `pre_compact.py` — reads `session_id`, `transcript_path`

### Output Shape
```json
{
  "continue": true,
  "systemMessage": "..."
}
```

---

## Codex Runtime: No Hooks

The Codex runtime (`pkg/agentruntime/codex/codex.go`) **does not use the Claude Code hook system**.

From the source (`codex.go:98-99`):
> "BuildBootstrapPrompt prepends shared Oro guidance for Codex runs **without relying on Claude
> hook surfaces**."

Codex workers:
- Receive NO `SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, or `UserPromptSubmit` events
- Get shared instructions injected directly into the bootstrap prompt via `ORO_AGENT.md`
- Have no hook-based context injection, permission gating, or skill enforcement

**Implication for bead-14**: Any hook-based feature (context tracking, tool gating, skill injection)
must have a Codex-compatible fallback path via prompt injection, or be marked Claude-only.

---

## Cross-Event Field Presence Summary

| Field | SessionStart | PreToolUse | PostToolUse | Stop | UserPromptSubmit |
|---|---|---|---|---|---|
| `session_id` | ✓ | — | — | ✓ | ✓ |
| `source` | ✓ | — | — | — | — |
| `role` | ✓ (opt) | — | — | — | — |
| `hook_type` | — | ✓ | — | — | — |
| `tool_name` | — | ✓ | ✓ | — | — |
| `tool_input` | — | ✓ | ✓ | — | — |
| `cwd` | — | ✓ (Bash) | — | — | — |
| `tool_result` | — | — | ✓ | — | — |
| `tool_output` | — | — | ✓* | — | — |
| `transcript_path` | — | — | ✓ | ✓ | — |
| `model_key` | — | — | ✓ (opt) | — | — |
| `budget` | — | — | ✓ (opt) | — | — |
| `prompt` | — | — | — | — | ✓ |
| **token_usage** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** |
| **context_pct** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** |
| **rollout_cursor** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** |
| **turn_token** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** | **ABSENT** |

`*` `tool_output` is used only by `validate_agent_completion.py` for Task tool events; may be a
naming bug — see PostToolUse discrepancy note above.

---

## Bead-14 Path Decision

Given the confirmed absence of `token_usage`, `context_pct`, `rollout_cursor`, and `turn_token`
from all hook inputs:

- **Path A** (if design assumed these fields in hook stdin): **NOT viable**. These fields are not
  emitted in hook stdin by Claude Code.
- **Path B** (read from transcript + pane files): **Required**. Context tracking must read from
  `transcript_path` (PostToolUse) and derive usage from the JSONL, or read from
  `~/.oro/panes/<role>/context_pct` (written by `context_pct_writer.py`).

The `transcript_path` field in PostToolUse is the single reliable source for token economics.
