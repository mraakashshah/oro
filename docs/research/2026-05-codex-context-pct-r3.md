# Codex Context Percent R3 Path Selection

Verdict: PATH (b) rollout-polling.

Codex context percentage should be implemented from the worker side by polling
Codex rollout JSONL. Do not implement the Codex path by porting the Claude
`PostToolUse` hook strategy.

## Required Question

The R3 decision was between:

- PATH (a) `PostToolUse` hook: install a Codex equivalent of
  `context_pct_writer.py` if Codex `PostToolUse` exposes token usage.
- PATH (b) rollout-polling: have the Oro worker heartbeat path read Codex
  rollout JSONL and derive `context_pct` if Codex hooks do not expose usage.

R3 evidence chooses PATH (b). The observed Codex `PostToolUse` shape does not
include token usage or any context-progress field.

## Empirical Evidence

`assets/hooks/test_hook_schemas.py` captures the R3 Codex `PostToolUse` fixture
used for hook compatibility tests. Its observed keys are:

```text
tool_name
tool_input
tool_result
transcript_path
tool_use_id
turn_id
```

The fields needed to make PATH (a) viable are absent from that fixture:

```text
token_usage
context_pct
rollout_cursor
turn_token
```

`docs/learnings/codex-hook-schemas.md` records the same R3 result across hook
events: `token_usage`, `context_pct`, `rollout_cursor`, and `turn_token` are
absent from `PostToolUse` and the other hook inputs inspected during the schema
study. It also records the separate worker-mode constraint: Oro Codex workers
currently do not receive the Claude hook event stream at all.

`assets/hooks/context_pct_writer.py` proves why the hook path needs a usage
source. It does not receive token usage directly from hook stdin. Instead, it
expects a hook-provided `transcript_path`, opens that JSONL transcript, reads the
latest assistant `message.usage`, and calculates:

```text
input_tokens + cache_creation_input_tokens + cache_read_input_tokens
```

That is a Claude transcript strategy. Codex fixtures include `transcript_path`,
but the R3 question was whether Codex hook stdin directly exposes token usage
for a PostToolUse-based worker path. It does not. Without a Codex `PostToolUse`
token-usage field, and without a confirmed Codex worker hook stream writing
`<worktree>/.oro/context_pct`, installing this writer for Codex would leave the
worker heartbeat path without a reliable context percentage source.

`pkg/agentruntime/codex/codex.go` confirms the current Codex worker contract:
Oro starts `codex exec --skip-git-repo-check --sandbox workspace-write`, returns
`worker.StreamFormatLineText`, and injects shared instructions through
`BuildBootstrapPrompt`. The adapter does not install hooks, pass a hook config,
or expose a transcript path to the worker.

`pkg/worker/context_pct.go` contains `ParseCodexContextPct`, which accepts a
plain JSON line like `{"context_pct":55}`. That parser is only useful if
something emits such lines on Codex stdout. The current Codex runtime adapter
does not make Codex emit those lines, so this parser is not sufficient as the R3
path.

## Decision

Choose PATH (b): rollout-polling.

The deciding fact is absence, not preference. R3 did not find a token-usage
field on Codex `PostToolUse`, and the existing Claude hook writer depends on a
hook-delivered transcript plus a hook event stream that the Codex worker path
does not currently install. Therefore the next implementation should derive
Codex `context_pct` from rollout JSONL in the worker heartbeat/context watcher
path instead of relying on Codex hook stdin.

## Implementation Consequences

- Add the Codex context source near the existing worker context polling and
  heartbeat logic, not in `pkg/agentruntime/codex/codex.go` alone.
- Use Codex rollout JSONL as the source of truth and write or publish the
  derived percentage where `watchContext` and `trySendHeartbeat` already look
  for context progress.
- Treat `ParseCodexContextPct` as an optional compatibility shim. Keep it only
  if a future shim intentionally emits `{"context_pct":...}` on stdout;
  otherwise remove or bypass it during the implementation task.
