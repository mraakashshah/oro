# Codex rejects compact SessionStart hook output

**Date:** 2026-07-20

**Component:** Codex hooks / post-compaction context restoration

**Severity:** medium

## Symptom

Codex reports the following error when a session resumes after compaction:

```text
SessionStart hook (failed)
error: hook returned invalid session start JSON output
```

Normal startup may still succeed because the failing hook is registered only for
the `compact` matcher.

## Investigation

The global and ordinary project `SessionStart` hooks emitted valid JSON, so a
fresh process did not reproduce the failure. The compact-only registration in
`.codex/hooks.json` led to `.codex/hooks/session_start_compact.py`. Its two
context-producing branches returned a top-level `additionalContext` field.

Comparing that output with the installed Codex hook parser and
`docs/learnings/codex-hook-schemas.md` showed that Codex validates the event
envelope strictly. Process staleness and concurrent Oro workers were considered,
but neither explained the invalid payload and neither was needed to reproduce
the schema mismatch.

## Root Cause

`SessionStart` context must be nested under `hookSpecificOutput`, with the event
name included:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "..."
  }
}
```

The compact hook instead emitted `{"additionalContext":"..."}`. Codex rejects
unknown top-level fields for this event, producing the generic invalid-JSON
message even though the bytes are syntactically valid JSON.

## Solution

`assets/hooks/session_start_compact.py:_session_start_output` now builds the
strict event envelope, and both the live-context and saved-state branches use
it. The `.claude/hooks` and `.codex/hooks` mirrors are staged from the canonical
asset.

`tests/test_session_start_compact.py` asserts the exact top-level keys and event
name. `tests/test_context_e2e.py` verifies that saved pre-compaction state is
restored through the nested context field.

## Prevention

- Assert complete hook envelopes, not only the presence of context text.
- Test matcher-specific paths independently; a clean startup does not exercise
  `SessionStart(compact)`.
- Keep `assets/hooks` canonical and run `make stage-assets` so installed and
  project-local copies remain identical.
- When the error says invalid hook JSON, validate the event-specific schema in
  addition to checking that `json.loads` succeeds.

## Related

- `docs/learnings/codex-hook-schemas.md`
