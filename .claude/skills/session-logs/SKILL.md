---
name: session-logs
description: Use when the user asks about prior conversations, past sessions, or historical context not in current memory
---

# Session Logs

## Overview

Search and analyze Claude Code session JSONL transcripts. Use this when a user references older conversations or asks what was said before.

## Location

Session logs live at: `~/.claude/projects/*/`

- **`*.jsonl`** — Full conversation transcript per session

## Structure

Each `.jsonl` file contains messages with:

- `type`: "session" (metadata) or "message"
- `timestamp`: ISO timestamp
- `message.role`: "user", "assistant", or "toolResult"
- `message.content[]`: Text, thinking, or tool calls (filter `type=="text"` for human-readable content)

## Common Queries

### List sessions by date and size

```bash
for f in ~/.claude/projects/*/*.jsonl; do
  date=$(head -1 "$f" | jq -r '.timestamp' 2>/dev/null | cut -dT -f1)
  size=$(ls -lh "$f" | awk '{print $5}')
  echo "$date $size $(basename "$f")"
done | sort -r | head -20
```

### Extract user messages from a session

```bash
jq -r 'select(.message.role == "user") | .message.content[]? | select(.type == "text") | .text' <session>.jsonl
```

### Search for keyword in assistant responses

```bash
jq -r 'select(.message.role == "assistant") | .message.content[]? | select(.type == "text") | .text' <session>.jsonl | rg -i "keyword"
```

### Count messages in a session

```bash
jq -s '{
  messages: length,
  user: [.[] | select(.message.role == "user")] | length,
  assistant: [.[] | select(.message.role == "assistant")] | length,
  first: .[0].timestamp,
  last: .[-1].timestamp
}' <session>.jsonl
```

### Tool usage breakdown

```bash
jq -r '.message.content[]? | select(.type == "toolCall") | .name' <session>.jsonl | sort | uniq -c | sort -rn
```

### Search across ALL sessions for a phrase

```bash
rg -l "phrase" ~/.claude/projects/*/*.jsonl
```

### Fast text-only search (low noise)

```bash
jq -r 'select(.type=="message") | .message.content[]? | select(.type=="text") | .text' <session>.jsonl | rg 'keyword'
```

## Tips

- Sessions are append-only JSONL (one JSON object per line)
- Large sessions can be several MB — use `head`/`tail` for sampling
- Requires `jq` and `rg` on PATH

## Red Flags

- Reading session logs without user permission
- Modifying or deleting session log files
- Searching for sensitive data (credentials, tokens) in logs
