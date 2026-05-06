# OpenClaw Learnings

## Overview

Personal AI assistant platform built on a **Gateway + Pi Agent** architecture. Enables interaction across multiple messaging channels (WhatsApp, Telegram, Slack, Discord, Signal, iMessage) through a single unified system.

## Architecture: Three-Tier Model

### 1. Gateway (WebSocket Control Plane)

**Location:** `src/gateway/`, `src/index.ts`

- Single long-lived daemon per host
- WebSocket server on `127.0.0.1:18789`
- Protocol: Typed JSON-RPC over WS with JSON Schema validation
- Clients: macOS app, CLI, web UI, nodes
- Manages all messaging surfaces

### 2. Pi Agent (Embedded LLM Runtime)

**Location:** `src/agents/pi-embedded-*`

- Engine: Anthropic's Pi embedded directly
- Model: Claude (Pro/Max recommended)
- Session management: Persistent transcripts in `~/.openclaw/sessions/`
- Tools: Dynamic binding of bash, file I/O, messaging, memory, sandbox

**Lifecycle:**
1. `runEmbeddedPiAgent()` creates session
2. Constructs system prompt + available tools
3. Streams tool calls + text generation
4. `subscribeEmbeddedPiSession()` streams to clients/channels

## Agents: Multi-Agent Framework

**Configuration:**
```yaml
agents:
  list:
    - id: "main"
      name: "Main Agent"
      default: true
      workspace: "~/my-workspace"
      model: "claude-opus-4-5"
      skills: ["github", "coding-agent"]
```

**Key Concepts:**
- `resolveSessionAgentId()` - maps session key to agent ID
- `resolveAgentConfig()` - reads agent-specific model, workspace, skills
- Each agent gets isolated workspace (`~/.openclaw/workspace-<id>`)
- Subagents can spawn via `openclaw_tools.sessions_spawn`

## Skills: Declarative Task Library

**Location:** `src/agents/skills/`, `skills/`

**Skill Definition (YAML Frontmatter):**
```yaml
---
name: github
description: "Interact with GitHub using the `gh` CLI..."
metadata:
  openclaw:
    emoji: "🐙"
    requires: { bins: ["gh"] }
    install:
      - id: "brew"
        kind: "brew"
        formula: "gh"
---
```

**How Skills Work:**
1. Discovery: Load from `skills/*/SKILL.md`
2. Prompt Integration: `buildWorkspaceSkillsPrompt()` creates list
3. Agent scans skills before replying
4. If skill applies, reads it with `read`, then follows it

**Bundled Skills (50+):**
- Integration: `1password`, `bear-notes`, `github`, `slack`
- Coding: `coding-agent`, `nano-pdf`
- Media: `video-frames`, `gifgrep`, `openai-whisper`
- Utilities: `weather`, `obsidian`, `notion`

## Tools: Pi Agent Capabilities

### Core Tool Categories

**File I/O:**
- `read(path, ...lines)` - read file sections
- `write(path, content)` - create/overwrite
- `edit(path, ...edits)` - targeted edits

**Bash Execution:**
- `bash(command)` - run shell commands
- PTY mode for interactive CLIs
- Background returns sessionId for long-running tasks
- Sandbox: isolated Docker container (optional)

**OpenClaw-Specific:**
- `openclaw_gateway.status` - health check
- `openclaw_gateway.agent` - spawn subagent
- `openclaw_gateway.send` - deliver to channel

**Tool Policy:**
- Profile-based: Global + per-agent overrides
- Group-based: Restrict by sender in group DMs
- Plugin allowlist: `group:plugins` enables all plugin tools

## Extensions & Plugins

**Plugin Architecture:**
```json
{
  "id": "msteams",
  "channels": ["msteams"],
  "configSchema": { "type": "object" }
}
```

**Plugin Types:**
1. **Channel Plugins** - messaging integrations (MSTeams, Matrix, LINE)
2. **Tool Plugins** - agent capabilities
3. **Workspace Extensions** - load via `extensions.json`

**Plugin SDK Export:** 391+ symbols including channel adapters, config schemas, utilities.

## System Prompt Construction

**Sections:**
1. Identity - agent name, user info
2. Skills - available SKILL.md entries
3. Memory - `memory_search` + `memory_get`
4. Tools - which bash/file/messaging tools available
5. Messaging - channel-specific conventions
6. Reply Tags - `[[reply_to_current]]`, `[[reply_to:id]]`
7. Time Zone - user's local time
8. Runtime - sandbox info, tool policy

**Prompt Modes:**
- "full" - all sections (main agent)
- "minimal" - reduced (subagents)
- "none" - just identity

## Configuration

**Hierarchy:**
1. `~/.config/openclaw/config.toml` - user config
2. Environment vars - override config
3. `~/.openclaw/sessions/` - agent transcripts (JSONL)
4. `~/.openclaw/agents/<id>/agent/` - agent-specific state

## Unique Architectural Patterns

### Pi-First Design
- Agent logic embedded (not separate service)
- Tools dependency-injected, scoped per session
- System prompt dynamically constructed per context

### Skill-Based Guidance
- Skills are NOT tools—they're instructions
- Agent chooses skill, reads it, follows recipe
- Reduces hallucination vs listing commands

### Sandbox Isolation
- Optional Docker per agent/tool call
- Workspace scoped per-agent
- Safe testing + multi-tenant scenarios

### Multi-Channel Convergence
- Single gateway owns all channels
- Routing by session key or channel allowlist
- Reply tags preserve threading across platforms

### Auth Profile Rotation
- Per-agent model + fallback override
- Cooldown on auth failures
- Round-robin fallback to avoid rate limits

## Key File Organization

```
src/
  agents/                    # Pi agent runtime + tools
    pi-embedded-*            # Runner, subscriber, sandbox
    pi-tools*                # Tool definitions, policy
    skills/                  # Skill loading, prompt building
    system-prompt.ts         # Dynamic prompt construction
  commands/                  # CLI entry points
  channels/                  # Messaging integrations
  gateway/                   # WebSocket server
  plugins/                   # Plugin loader
  plugin-sdk/                # Public exports (391 symbols)
skills/                      # Bundled SKILL.md docs (50+)
extensions/                  # Channel plugins
```

## Core Philosophy

1. Gateway is control plane; assistant is the product
2. Agents are multi-tenant; one per persona/task type
3. Skills guide; tools execute
4. Sandbox by default; trust explicitly
5. Extensibility without modification
6. Type-safe everywhere
7. Functional-first
