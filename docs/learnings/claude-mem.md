# Claude-Mem Learnings

> **Source**: [thedotmack/claude-mem](https://github.com/thedotmack/claude-mem) (26K stars)
> **What**: Claude Code plugin that auto-captures session observations, compresses with AI, injects context into future sessions
> **Stack**: TypeScript/Bun, SQLite (WAL + FTS5), Chroma vector DB (via MCP), Claude Agent SDK
> **Reference**: `yap/reference/claude-mem/`

---

## Architecture Overview

```
SessionStart Hook
  → Start worker daemon (port 37777)
  → Query observations + summaries from SQLite
  → Build progressive disclosure context
  → Inject as hookSpecificOutput.additionalContext

UserPromptSubmit Hook
  → Create/resume SDK session in DB
  → Initialize memory agent (Claude Agent SDK subprocess)

PostToolUse Hook (every tool call)
  → Send tool_name + tool_input + tool_response to worker
  → SDK agent generates structured observation asynchronously
  → Store observation in SQLite + sync to Chroma

Stop Hook
  → Extract last assistant message from transcript
  → SDK agent generates session summary
  → Store summary, mark session complete
```

---

## Key Design Patterns

### 1. Progressive Disclosure (Most Important Idea)

**Problem**: Injecting all memories into context wastes tokens (94% waste in naive approach).

**Solution**: 3-layer retrieval pattern:

| Layer | What | Cost | When |
|-------|------|------|------|
| **Index** | Compact table: ID, time, type, title, ~read tokens | ~800 tokens total | Always (SessionStart) |
| **Timeline** | Context around an observation (N before, N after) | ~500 tokens | On demand (MCP tool) |
| **Full detail** | Complete observation (narrative, facts, concepts) | ~500-1000 tokens each | On demand (MCP tool) |

```markdown
### 2026-02-09
**src/services/context/**
| ID | Time | T | Title | Read |
|#2543 | 2:14 PM | 🔴 | Hook timeout: 60s too short | ~155 |
|#2891 | 2:16 PM | 🔵 | Hook timeout configuration | ~120 |
```

Agent scans the lightweight index, decides what's relevant, fetches details only for what it needs. **Token savings: ~10x vs dumping everything.**

**Oro applicability**: Our `ForPrompt()` currently dumps top-5 memories. Could instead inject a compact index and let the worker fetch details via `oro recall <id>`.

### 2. Observation Model (vs Our Memory Model)

**claude-mem observations**:
```
type: bugfix | feature | refactor | discovery | decision | change
title: "Fixed auth token expiration"
subtitle: "JWT exp claim was being ignored"
narrative: "Discovered that the validateToken function..."
facts: ["JWT tokens expire after 24h", "exp claim is Unix timestamp"]
concepts: ["how-it-works", "gotcha", "pattern"]
files_read: ["src/auth.ts"]
files_modified: ["src/auth.ts", "tests/auth.test.ts"]
discovery_tokens: 2500  # tokens spent discovering this
```

**oro memories**:
```
content: "JWT tokens expire after 24h, exp claim is Unix timestamp"
type: lesson | decision | gotcha | pattern | preference
tags: ["auth", "jwt"]
confidence: 0.8
source: cli | self_report | daemon_extracted
```

**Key differences**:
- claude-mem tracks **files touched** per observation (spatial context)
- claude-mem tracks **discovery tokens** (ROI: "cost to discover" vs "cost to read")
- claude-mem has **structured fields** (title/subtitle/narrative/facts) vs our single `content`
- claude-mem has **concepts** (semantic tags) vs our free-form `tags`
- claude-mem generates observations **via AI** (Agent SDK subprocess); we extract via regex/markers

**Takeaway**: Our model is simpler but loses spatial context (which files?) and economic context (how expensive was discovering this?).

### 3. Session Summaries (We Don't Have This)

At session end, claude-mem generates a structured summary:
```
request: "Add JWT authentication"
investigated: "Looked at existing auth middleware, session handling"
learned: "Express middleware ordering matters for auth"
completed: "JWT validation, token refresh endpoint"
next_steps: "Add rate limiting to auth endpoints"
```

**Oro applicability**: Our `HandoffPayload` has `Learnings[]` and `Decisions[]` but not a structured session summary. Adding a summary at session end would give better context continuity.

### 4. Two-ID Session Architecture

```
content_session_id: User's Claude Code session (stable, from hooks)
memory_session_id: SDK agent's session (NULL until first response, then captured)
```

**Why**: The memory agent runs as a separate Claude session. If you used the user's session ID, memory observations would pollute the user's transcript. Separation keeps them clean.

**Oro applicability**: We have `worker.ID` but don't separate the worker's Claude session from any observer/memory agent session. If we add AI-powered memory extraction (like claude-mem does), we'd need this separation.

### 5. Token Economics as First-Class Metric

Every observation tracks:
- `discovery_tokens`: How many tokens were spent to discover/learn this
- `read_tokens` (calculated): How many tokens to read the compressed observation
- `savings`: discovery - read

Displayed to user: "5000 discovery tokens → 200 read tokens (96% savings)"

**Oro applicability**: We don't track this. If we want to justify memory system value, we'd need to measure compression ratio.

### 6. Worker Daemon Architecture

claude-mem runs a persistent Bun daemon on `localhost:37777`:
- All hooks delegate to it via HTTP
- Holds SQLite connection pool
- Manages SDK agent subprocess lifecycle
- Serves web viewer UI (React + SSE)
- Health check endpoint

**Contrast with oro**: Our hooks are stateless binaries (fresh process per invocation). claude-mem's daemon approach is heavier but enables:
- Connection pooling (no SQLite open/close per hook)
- Real-time web viewer
- Session state across hooks
- SDK agent subprocess management

### 7. Chroma Vector Search Integration

```
SQLite (FTS5) ──── hybrid search ──── Chroma (embeddings)
     ↑                                      ↑
  Keyword/metadata                    Semantic similarity
  filtering                           ranking
```

**Strategy pattern**:
1. No query text → SQLite only (metadata filters)
2. Query + Chroma available → Chroma semantic search (90-day recency window)
3. Chroma fails → Fallback to SQLite FTS5

**Intersection algorithm** (HybridSearchStrategy):
1. SQLite metadata filter → candidate IDs
2. Chroma semantic ranking → ranked IDs
3. Intersect: keep only candidates, in Chroma rank order
4. Hydrate from SQLite

**Oro applicability**: Our `HybridSearch` uses RRF (Reciprocal Rank Fusion) to merge FTS5 + vector scores. claude-mem's intersection approach is simpler but loses RRF's score blending. Both are valid; ours is more principled for ranking.

### 8. Mode System (Domain Customization)

JSON mode files define observation types and concepts per domain:
```json
{
  "name": "Code Development",
  "observation_types": [
    { "id": "bugfix", "label": "Bug Fix", "emoji": "🔴" },
    { "id": "feature", "label": "Feature", "emoji": "🟣" }
  ],
  "observation_concepts": [
    { "id": "how-it-works", "label": "How It Works" },
    { "id": "gotcha", "label": "Gotcha" }
  ],
  "prompts": {
    "system_identity": "You are a memory observer...",
    "summary_instruction": "Summarize this session..."
  }
}
```

Modes support inheritance (`code--ko` extends `code`). Available: code, email-investigation, 30+ localized variants.

**Oro applicability**: We hardcode memory types (`lesson`, `decision`, `gotcha`, `pattern`). A mode system could customize types per project type.

---

## Database Schema (Key Tables)

```sql
-- SDK session tracking (two-ID pattern)
CREATE TABLE sdk_sessions (
  id INTEGER PRIMARY KEY,
  content_session_id TEXT UNIQUE,      -- User's Claude session
  memory_session_id TEXT UNIQUE,       -- SDK agent session (NULL initially)
  project TEXT, status TEXT DEFAULT 'active',
  started_at_epoch INTEGER
);

-- Observations (one per tool use, AI-generated)
CREATE TABLE observations (
  id INTEGER PRIMARY KEY,
  memory_session_id TEXT REFERENCES sdk_sessions(memory_session_id),
  project TEXT, type TEXT,
  title TEXT, subtitle TEXT, narrative TEXT,
  facts TEXT,     -- JSON array
  concepts TEXT,  -- JSON array
  files_read TEXT, files_modified TEXT,  -- JSON arrays
  discovery_tokens INTEGER DEFAULT 0,
  created_at_epoch INTEGER
);

-- FTS5 for keyword search
CREATE VIRTUAL TABLE observations_fts USING fts5(
  title, subtitle, narrative, text, facts, concepts,
  content='observations', content_rowid='id'
);
-- Auto-sync triggers: observations_ai, observations_ad, observations_au

-- Session summaries (one per session, AI-generated at Stop)
CREATE TABLE session_summaries (
  id INTEGER PRIMARY KEY,
  memory_session_id TEXT UNIQUE REFERENCES sdk_sessions(memory_session_id),
  project TEXT,
  request TEXT, investigated TEXT, learned TEXT,
  completed TEXT, next_steps TEXT, notes TEXT,
  discovery_tokens INTEGER DEFAULT 0,
  created_at_epoch INTEGER
);

-- User prompts (for semantic search)
CREATE TABLE user_prompts (
  id INTEGER PRIMARY KEY,
  content_session_id TEXT,
  prompt_number INTEGER, prompt_text TEXT,
  created_at_epoch INTEGER
);
```

**SQLite pragmas**: WAL, synchronous=NORMAL, mmap_size=256MB, cache_size=10K pages, foreign_keys=ON.

**Comparison to oro**: We have a single `memories` table with `content`, `type`, `tags`, `confidence`, `embedding`. claude-mem's schema is more normalized (separate tables for observations, summaries, prompts, sessions) and richer (structured fields, file tracking, discovery tokens).

---

## Hook Protocol Details

```json
// hooks.json lifecycle
{
  "SessionStart": [
    "smart-install.js",                           // Cached dep check
    "bun-runner.js worker-service.cjs start",     // Ensure daemon
    "bun-runner.js worker-service.cjs hook claude-code context"  // Inject
  ],
  "UserPromptSubmit": [
    "bun-runner.js worker-service.cjs start",
    "bun-runner.js worker-service.cjs hook claude-code session-init"
  ],
  "PostToolUse": [
    "bun-runner.js worker-service.cjs start",
    "bun-runner.js worker-service.cjs hook claude-code observation"
  ],
  "Stop": [
    "bun-runner.js worker-service.cjs start",
    "bun-runner.js worker-service.cjs hook claude-code summarize",
    "bun-runner.js worker-service.cjs hook claude-code session-complete"
  ]
}
```

**Pattern**: Every hook first ensures the daemon is running, then delegates via HTTP. The daemon holds all state.

**Context injection** returns:
```json
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "## Recent Work\n### 2026-02-09\n..."
  }
}
```

**Stdin parsing trick**: Claude Code doesn't close stdin after writing JSON. claude-mem solves this by attempting JSON.parse after each chunk (JSON is self-delimiting). Safety timeout at 30s.

---

## What Oro Could Adopt

### High Value, Low Effort
1. **Structured session summaries** — Generate at worker completion (request/investigated/learned/completed/next_steps). We already have `HandoffPayload.Learnings` and `.Decisions`; just formalize the format.
2. **File tracking in memories** — Add `files_read` and `files_modified` to our `InsertParams`. Workers know what files they touched.
3. **`oro memories list` with progressive format** — Table output with id, type, truncated content, confidence, age. Let workers scan cheaply.

### Medium Value, Medium Effort
1. **Progressive disclosure for context injection** — Instead of `ForPrompt()` dumping 5 memories, inject a compact index. Add `oro recall --id=<N>` for full detail. Workers self-serve.
2. **Discovery token tracking** — Add `discovery_tokens` to memories table. Measure compression ROI.
3. **Concept-based tagging** — Predefined concept vocabulary (`how-it-works`, `gotcha`, `pattern`, `trade-off`) instead of free-form tags. Better for structured retrieval.

### High Value, High Effort
1. **AI-powered observation extraction** — Use Claude Agent SDK to generate structured observations from worker output, instead of regex/marker extraction. Much richer memories.
2. **Vector search with Chroma/Ollama** — Already planned as `oro-cwz.3`. claude-mem's hybrid strategy pattern is a good reference.

### Probably Not Worth It
1. **Worker daemon for hooks** — Our stateless hook binary is simpler and works. Daemon adds operational complexity for marginal benefit (connection pooling saves ~5ms).
2. **Web viewer UI** — Nice for demos but workers don't need a browser UI.
3. **Mode system** — Over-engineering for a single-project tool. Modes make sense for a plugin marketplace product, not for oro.

---

## Anti-Patterns to Avoid

1. **AI-generated observations on every PostToolUse** — claude-mem runs an SDK agent subprocess for every tool call. At ~100-200 tool calls per session, that's significant API cost. Our marker/regex approach is free. The right middle ground: AI summarization at session end only (not per-tool).

2. **Chroma via MCP subprocess** — Spawning Python via `uvx` for vector search adds ~2s cold start, requires Python runtime, breaks on Windows. Our planned approach (Ollama + SQLite brute-force) is simpler.

3. **bun-runner.js complexity** — Smart-install, daemon management, spawn cooldowns, platform-specific workarounds. Significant operational surface area for something that should be simple.

4. **90-day hardcoded recency window** — claude-mem filters Chroma results to last 90 days. Our time-decay scoring (half-life 30 days) is more nuanced — old memories fade but aren't cut off.

---

## Interesting Technical Details

- **FTS5 trigger sync**: `CREATE TRIGGER observations_ai AFTER INSERT ON observations BEGIN INSERT INTO observations_fts(rowid, ...) SELECT new.id, new.title, ...; END;` — We do this too, good validation.
- **JSON array filtering**: `EXISTS (SELECT 1 FROM json_each(concepts) WHERE value = ?)` — Same approach as our tag filtering.
- **INSERT OR IGNORE for session idempotency** — Same content_session_id → same DB row. Simple and effective.
- **SQLite WAL + mmap_size=256MB** — We should check if our SQLite setup has similar pragmas.
- **Result formatting**: Date-grouped tables with estimated read tokens per row. Good UX pattern for `oro memories list`.
