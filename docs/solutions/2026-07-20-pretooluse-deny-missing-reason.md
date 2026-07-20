# PreToolUse denial fails without a reason

**Date:** 2026-07-20

**Component:** Codex destructive-command guard

**Severity:** high

## Symptom

A destructive Bash command produces both a block warning and a hook error:

```text
PreToolUse hook (failed)
warning: BLOCKED: Bash command classified as destructive (git merge).
error: PreToolUse hook returned permissionDecision:deny without a non-empty permissionDecisionReason
```

The malformed denial fails open, so the warning does not reliably enforce the
intended block.

## Investigation

The active global hook in `~/.codex/config.toml` runs
`~/.oro/hooks/destructive_command_guard.py` for Bash. The installed hook and
`assets/hooks/destructive_command_guard.py` were byte-identical and emitted
`permissionDecision: "deny"` plus a top-level `systemMessage`, but no nested
`permissionDecisionReason`.

Codex v0.144.6's `hooks/src/engine/output_parser.rs` requires a non-empty reason
beside a nested deny decision. The same parser explicitly rejects
`permissionDecision: "ask"`. PreToolUse input also has no stable field proving
that a conversational approval occurred, so mechanically denying every
`git merge` traps approved merges in the same denial loop.

## Root Cause

The guard implemented the UI warning field but omitted the required decision
reason. Its classifier also treated recoverable, approval-gated merges as
unconditional denials even though the stateless hook could not recognize the
subsequent approval.

## Solution

`build_decision` now places the same non-empty explanation in both
`hookSpecificOutput.permissionDecisionReason` and `systemMessage`. `git merge`
and `git merge --abort` are left to the explicit-approval workflow instead of
the stateless mechanical guard. Irrecoverable commands remain denied.

Regression tests assert the complete Codex denial shape and ensure merges do
not enter an impossible approval loop.

## Prevention

- Test complete event-specific hook envelopes, including conditional fields.
- Compare hook output against the installed Codex parser, not only JSON syntax.
- Do not mechanically deny approval-gated operations unless the hook has a
  reliable channel for observing that approval.
- Verify tracked hook mirrors with `cmp` commands whose failures cannot be
  masked by a later successful shell command.

## Related

- `docs/learnings/codex-hook-schemas.md`
- `docs/solutions/2026-07-20-invalid-session-start-json.md`
