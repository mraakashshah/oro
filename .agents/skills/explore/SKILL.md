---
name: explore
description: Use when entering an unfamiliar codebase area, starting major work, or needing to understand architecture before making changes
---

# Explore — Internal Codebase Exploration

## Overview

READ-ONLY exploration at three depths. Never modifies code.

## Depths

| Depth | Time | What It Does |
|-------|------|--------------|
| **Quick** | ~1 min | File tree + code structure overview |
| **Deep** | ~5 min | Comprehensive analysis + pattern documentation |
| **Architecture** | ~3 min | Layer detection + call graph + dependency mapping |

## Quick Exploration

Fast orientation. Use when you need to know what's where.

1. **File structure** — `Glob` for project layout
2. **Code structure** — `Grep` for key patterns (main, init, handlers, routes)
3. **Focused search** — if looking for something specific, `Grep` for keywords

## Deep Exploration

Thorough understanding. Use before major work or in unfamiliar code.

1. **Map structure** — file tree, key modules, entry points
2. **Read key files** — main entry, config, core types/interfaces
3. **Trace patterns** — how data flows, how errors are handled, how tests are organized
4. **Document findings** — write to `docs/` or summarize for the user

## Architecture Exploration

System boundaries and dependencies. Use before refactoring or design work.

1. **Identify layers:**
   - **Entry** — handlers, CLI commands, main
   - **Middle** — services, business logic
   - **Leaf** — utilities, helpers, data access

2. **Map call graph** — what calls what across files

3. **Detect issues:**
   - Circular dependencies
   - Overly coupled modules
   - Missing abstractions

4. **Output structure:**
```yaml
layers:
  entry: [cmd/main.go, internal/api/router.go]
  middle: [internal/service/, internal/domain/]
  leaf: [internal/util/, pkg/]
call_graph:
  hot_paths: [request → validate → authorize → execute]
circular_deps: []
```

## Choosing Depth

| Situation | Depth |
|-----------|-------|
| "Where is X?" | Quick |
| "How does X work?" | Deep |
| "How is the system organized?" | Architecture |
| Starting new feature | Deep (focused on relevant area) |
| Preparing for refactor | Architecture |

## Key Principles

1. **READ-ONLY** — never modify code during exploration
2. **Use primitives** — Grep, Glob, Read are your tools
3. **Token-efficient** — don't read entire files; scan structure first, then targeted reads
4. **Output to shared locations** — `docs/` for documentation, inline for quick answers
