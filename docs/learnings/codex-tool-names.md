# Codex Tool Names in PreToolUse/PostToolUse Hooks

Empirically captured from Codex CLI v0.128.0 sessions on 2026-05-06.

## Claude → Codex Tool Name Mapping

| Claude `tool_name` | Codex `tool_name` | Notes |
|--------------------|-------------------|-------|
| `Read`             | `Bash`            | Codex reads files via shell: `cat`, `sed -n`, `nl -ba` |
| `Write`            | `apply_patch`     | New-file creation uses `*** Add File:` patch format |
| `Edit`             | `apply_patch`     | File modification uses `*** Update File:` patch format |
| `Bash`             | `Bash`            | Identical — shell command execution |
| `Grep`             | `Bash`            | Codex searches via `rg`, `grep` shell commands |
| `Glob`             | `Bash`            | Codex lists files via `rg --files`, `find` |
| `WebFetch`         | `Bash`            | Expected via `curl`/`wget`; no dedicated fetch tool seen |

## Hook Event Structure

### Key field differences

| Field              | Claude             | Codex                     |
|--------------------|--------------------|---------------------------|
| Event type key     | `hook_type`        | `hook_event_name`         |
| Event type values  | `PreToolUse` etc.  | `PreToolUse` etc. (same)  |
| `tool_name`        | ✓ present          | ✓ present (same values)   |
| `tool_input`       | ✓ present          | ✓ present (differs — see below) |
| `cwd`              | top-level          | top-level                 |
| `session_id`       | ✓                  | ✓                         |
| `turn_id`          | —                  | ✓ (Codex-only)            |
| `transcript_path`  | —                  | ✓ (Codex-only, can be null) |
| `model`            | —                  | ✓ (Codex-only)            |
| `permission_mode`  | —                  | ✓ (Codex-only)            |
| `tool_use_id`      | —                  | ✓ (Codex-only)            |
| `tool_result`      | PostToolUse only   | `tool_response` (Codex-only key name) |

### PreToolUse sample — Bash (file read via cat)

```json
{
  "session_id": "019dff9b-a083-7e30-8376-42049bf7623d",
  "turn_id": "019dff9b-a090-7722-b700-d42687909ef1",
  "transcript_path": null,
  "cwd": "/tmp/codex-hook-capture",
  "hook_event_name": "PreToolUse",
  "model": "gpt-5.5",
  "permission_mode": "bypassPermissions",
  "tool_name": "Bash",
  "tool_input": {
    "command": "cat /tmp/codex-hook-capture/hooks.json"
  },
  "tool_use_id": "call_X3WjFKTotyBVBYFr6T4i9hLN"
}
```

### PreToolUse sample — apply_patch (file creation)

```json
{
  "session_id": "019dff9b-a083-7e30-8376-42049bf7623d",
  "turn_id": "019dff9b-a090-7722-b700-d42687909ef1",
  "transcript_path": null,
  "cwd": "/tmp/codex-hook-capture",
  "hook_event_name": "PreToolUse",
  "model": "gpt-5.5",
  "permission_mode": "bypassPermissions",
  "tool_name": "apply_patch",
  "tool_input": {
    "command": "*** Begin Patch\n*** Add File: test_output.txt\n+hello world\n*** End Patch\n"
  },
  "tool_use_id": "call_FHSnm2TIMMjBerakflYa05CT"
}
```

### PostToolUse sample — Bash (includes tool_response)

```json
{
  "hook_event_name": "PostToolUse",
  "tool_name": "Bash",
  "tool_input": { "command": "cat /tmp/codex-hook-capture/hooks.json" },
  "tool_response": "{ ... file contents ... }",
  "tool_use_id": "call_X3WjFKTotyBVBYFr6T4i9hLN"
}
```

## tool_input shapes per tool

### `Bash`

```json
{ "command": "<shell command string>" }
```

- `command` contains the full shell string, including file paths, flags, pipes.
- No separate `file_path` field. File path must be parsed from `command`.

### `apply_patch`

```json
{ "command": "<freeform patch content>" }
```

Patch format uses `*** Begin Patch` / `*** End Patch` delimiters:
- `*** Add File: <relative-path>` — create new file
- `*** Update File: <absolute-or-relative-path>` — modify existing file
- `@@` context marker before diff hunks
- `+line` for additions, `-line` for removals (like unified diff)

## Implications for oro-search-hook (bead 10)

The current `oro-search-hook` intercepts Claude's `Read` events and reads `tool_input.file_path` directly.

For Codex compatibility, three changes are needed:

1. **Accept `hook_event_name`** in addition to (or instead of) `hook_type`  
2. **Watch `tool_name: "Bash"`** (not `"Read"`) for file-read interception  
3. **Parse file path from `tool_input.command`** — no clean `file_path` field exists  
   - Example: `cat /path/to/file` → extract `/path/to/file`
   - Example: `sed -n '1,260p' pkg/foo.go` → extract `pkg/foo.go`
   - Codex internally classifies read commands with a `parsed_cmd.path` field, but this is NOT exposed in hook events

## Hook configuration format

Codex hooks are configured inline in `config.toml` via `-c` or in a project-level `hooks.json`.

### config.toml inline (via `-c` flag)

```
-c 'hooks.PreToolUse=[{matcher="",hooks=[{type="command",command="/path/to/script",timeout=10}]}]'
```

### hooks.json (project-level, same format as Claude's settings.json)

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "/path/to/script", "timeout": 30 }]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "apply_patch",
        "hooks": [{ "type": "command", "command": "/path/to/script" }]
      }
    ]
  }
}
```

The `matcher` field is a regex matched against `tool_name`. An empty string `""` matches all tools.

## Internal session log format vs hook format

Codex JSONL session files (`~/.codex/sessions/`) use a different representation:

| Session JSONL (`response_item`) | Hook event (`tool_name`) |
|---------------------------------|--------------------------|
| `function_call.name = "exec_command"` | `tool_name = "Bash"` |
| `function_call.name = "update_plan"` | Not hook-visible (internal) |
| `function_call.name = "write_stdin"` | Not hook-visible (internal) |
| `function_call.name = "view_image"` | `tool_name = "view_image"` (unconfirmed) |
| `function_call.name = "spawn_agent"` | Not hook-visible (internal) |

The `exec_command` internal name is exposed as `Bash` in hook events. The `apply_patch` name is consistent between internal and hook representations.

## Additional Codex tool names seen in session logs (not hook-intercepted)

From scanning `~/.codex/sessions/2026/`:
- `exec_command` — shell execution (hook-visible as `Bash`)
- `apply_patch` — file edits (hook-visible as `apply_patch`)
- `update_plan` — internal plan tracking
- `write_stdin` — write to an interactive process stdin
- `view_image` — view image files
- `spawn_agent` / `send_input` / `close_agent` / `resume_agent` / `wait_agent` — multi-agent coordination
- `_search_issues` / `_search_prs` — GitHub issue/PR search (plugin tools)
