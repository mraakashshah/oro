# Code Search System for Oro Sessions

**Date:** 2026-02-07
**Bead:** oro-w2u
**Status:** Spec complete

## Problem

Oro sessions (Architect, Manager, Worker) consume excessive tokens when exploring codebases. A Worker reading a 500-line Go file burns 3,000-20,000 tokens on raw source when it only needs function signatures. Multiply by the dozens of files touched per task, and token waste dominates session cost and context window pressure.

CC-v3 solved this with a three-layer hook stack that intercepts Read and Grep calls. Their data shows 26-99% token savings depending on the layer used. Oro needs the same capability, adapted for a Go-based codebase.

## Prior Art: Continuous Claude v3

Source: `yap/reference/Continuous-Claude-v3/dot-claude/hooks/src/`

### Layer 1: TLDR Read Enforcer (`tldr-read-enforcer.ts`)

- **Intercepts:** `Read` tool calls via `PreToolUse` hook
- **Mechanism:** Returns `permissionDecision: 'deny'` with AST summary as the denial reason, effectively replacing raw file content with structured context
- **Bypass conditions:** Non-code files (JSON, YAML, MD), test files, small files (<3KB / ~100 lines), explicit offset/limit reads, hook/skill files
- **Three modes:**
  - `structure`: Names-only (functions + classes with line numbers and first-line docstrings). 99% token savings. Default.
  - `context`: Focused on a target symbol from search context. 87% savings. Used when search router found a specific target.
  - `extract`: Full AST dump with call graph, CFG, DFG. 26% savings. Used for advanced flow analysis.
- **Cross-hook communication:** Reads search context from `/tmp/claude-search-context/{session}.json` written by the smart search router. Context expires after 30 seconds.
- **Backend:** Python TLDR daemon (HTTP/socket) for cached AST parsing. 50ms response vs 500ms CLI.

### Layer 2: Smart Search Router (`smart-search-router.ts`)

- **Intercepts:** `Grep` tool calls via `PreToolUse` hook
- **Classifies queries into three types:**
  - **Structural:** Code patterns (`def X`, `class Y`, decorators, imports) -- routes to AST-grep
  - **Literal:** Exact identifiers (snake_case, CamelCase, SCREAMING_CASE), regex, file paths -- routes to TLDR search / ripgrep
  - **Semantic:** Natural language queries ("how does auth work?", questions) -- routes to FAISS embeddings via daemon
- **Symbol resolution:** Looks up pattern in TLDR index to determine if it is a function, class, variable, import, or decorator. Uses heuristics as fallback (verb prefixes = function, PascalCase = class, SCREAMING_CASE = constant).
- **Cross-file enrichment:** Looks up callers (impact analysis) and definition location for the target symbol. Stores this context for the TLDR read enforcer.

### Layer 3: AST-grep (MCP Server)

- Exposed as an MCP server in `.claude/mcp_config.json`
- Handles structural pattern matching (e.g., `def $FUNC($$$):`)
- Language-specific -- needs grammar per language

### Key CC-v3 Architecture Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Hook language | TypeScript | Claude Code hooks run as shell commands; TS compiles to JS for Node |
| AST backend | Python TLDR daemon (tree-sitter) | Python has mature tree-sitter bindings; daemon amortizes startup |
| Cross-hook IPC | JSON files in `/tmp/` | Simple, no daemon dependency for IPC itself |
| Semantic search | FAISS + BGE embeddings | Optional layer; daemon builds index on startup |
| Hook interception | `permissionDecision: 'deny'` | Blocks the original tool call and injects summary as the denial reason |

## Design Decisions for Oro

### Decision 1: Implementation Language

| Option | Pros | Cons | Verdict |
|--------|------|------|---------|
| **A. Pure Go (tree-sitter bindings)** | Single binary, no runtime deps, fast startup | `tree-sitter/go-tree-sitter` exists but is younger than Python bindings; must vendor grammars per language; CGo required | **Recommended for day-two** |
| **B. Go wrapper calling CLI tools** | Leverage existing `ast-grep` CLI, `rg`, Go `go/ast` stdlib; no CGo; simpler build | Extra process spawns (~50ms each); no daemon caching; harder to get 99% savings without AST parsing | **Recommended for day-one** |
| **C. Polyglot (Go + Python TLDR daemon)** | Reuse CC-v3's proven Python TLDR daemon directly | Two runtimes; Python dependency breaks Go-only promise; deployment complexity | Rejected |

**Recommendation:** Start with **Option B** (Go wrapper + CLI tools) for day-one. Go's `go/ast` stdlib handles Go files natively with zero dependencies. For non-Go files, shell out to `ast-grep` (Rust binary, available via `cargo install` or brew). Migrate to **Option A** (pure Go tree-sitter) when multi-language AST needs justify the CGo complexity.

**Rationale:** Oro's codebase is primarily Go. Go's `go/ast` package provides production-grade parsing for Go files without any external dependency. This covers the majority case. For the minority case (reading TypeScript, Python, or other files in referenced projects), `ast-grep` CLI is a well-tested structural search tool that already exists in the CC-v3 stack.

### Decision 2: TLDR Strategy

| Option | Pros | Cons | Verdict |
|--------|------|------|---------|
| **A. AST-based summarization** | Accurate structure extraction; language-aware; proven in CC-v3 | Needs parser per language; more complex | **Recommended** |
| **B. Line-count heuristics** | Zero deps; works on any file | Misses structure; no function/class extraction; limited savings | Rejected |
| **C. Hybrid (AST for Go, heuristics for others)** | Best of both; pragmatic | Two code paths to maintain | Acceptable fallback |

**Recommendation:** **Option A** for Go files (via `go/ast`), falling back to **Option C** for non-Go files. The Go stdlib parser is zero-dependency and extracts function signatures, type declarations, interfaces, and struct fields natively. For non-Go files where no parser is available, fall back to a simple heuristic: return the first N lines (package/import block) plus any lines matching `^func |^type |^var |^const` patterns via ripgrep.

### Decision 3: Search Routing

When to use each tool:

| Query Type | Detection | Tool | Token Efficiency |
|------------|-----------|------|-----------------|
| **Structural** | Code keywords (`func`, `type`, `interface`, decorators, imports) | `ast-grep` CLI or `go/ast` query | High (returns only matching nodes) |
| **Literal** | Identifiers (CamelCase, snake_case), regex, file paths | `rg` (ripgrep) | Medium (returns matching lines + context) |
| **Semantic** | Natural language, questions, multi-word conceptual queries | Future: embeddings | Highest (returns ranked relevant snippets) |

The classifier from CC-v3 is directly portable -- it is a set of regex patterns that categorize the grep query. The Go implementation is straightforward.

### Decision 4: Hook Architecture

Claude Code hooks receive JSON on stdin and emit JSON on stdout. The hook binary:

1. Reads `PreToolUse` event from stdin
2. Checks `tool_name` (Read or Grep)
3. Applies bypass rules (small files, config files, test files, explicit offset/limit)
4. For intercepted calls: returns `permissionDecision: 'deny'` with structured summary as `permissionDecisionReason`
5. For bypassed calls: returns `{}` (allow)

**Implementation:** A single Go binary (`oro-search-hook`) compiled and placed in `.claude/hooks/`. The `settings.json` registers it for `PreToolUse` events. The binary handles both Read interception (TLDR) and Grep interception (search routing) based on `tool_name`.

**Cross-hook IPC:** Same as CC-v3 -- JSON files in `/tmp/oro-search-context/`. The search router writes context; the read enforcer reads it. 30-second TTL.

### Decision 5: Day-One Scope vs Future

**Day-one (MVP):**
- Go file TLDR read enforcer using `go/ast` (structure mode only: function signatures, type declarations, interface methods, struct fields with line numbers)
- Bypass rules: small files (<3KB), non-code files, test files, explicit offset/limit, `.claude/` directory
- Token savings target: 90%+ for Go files over 100 lines
- Single compiled Go binary, no external dependencies beyond Go stdlib

**Day-two:**
- Smart search router (classify Grep queries, route to ast-grep or ripgrep with suggestions)
- Cross-hook context passing (search router informs read enforcer of target symbol)
- `context` mode (focused on a specific function/type when search context is available)
- Non-Go file support via `ast-grep` CLI fallback

**Day-three (future):**
- Pure Go tree-sitter bindings for multi-language AST parsing
- Semantic search layer (embeddings + vector similarity)
- Daemon mode for caching parsed ASTs across reads
- `extract` mode with call graph analysis
- Impact analysis (callers of a function across the codebase)

### Decision 6: Token Budget Targets

| Mode | CC-v3 Claimed | Oro Target (Day-One) | Notes |
|------|---------------|---------------------|-------|
| Structure | 99% | 90-95% | Conservative; Go signatures are verbose (params + return types) |
| Context | 87% | N/A (day-two) | Requires search router integration |
| Extract | 26% | N/A (day-three) | Full AST dump; diminishing returns |
| Search routing | Varies | N/A (day-two) | Savings from avoiding unnecessary reads |

Day-one target: **90% token savings on Go files over 100 lines.** This means a 500-line Go file (~15,000 tokens raw) should produce ~1,500 tokens of structured summary.

## Architecture

```
Claude Code Session (any role)
    |
    v
PreToolUse Hook: oro-search-hook (Go binary)
    |
    +-- tool_name == "Read"?
    |       |
    |       +-- Bypass? (small file, config, test, offset/limit) --> allow ({})
    |       |
    |       +-- Go file? --> go/ast parse --> structure summary --> deny with summary
    |       |
    |       +-- Non-Go code file? --> [day-one: allow] [day-two: ast-grep or heuristic]
    |
    +-- tool_name == "Grep"? (day-two)
    |       |
    |       +-- Classify query (structural / literal / semantic)
    |       |
    |       +-- Store search context to /tmp/oro-search-context/{session}.json
    |       |
    |       +-- Return routing suggestion as denial reason
    |
    +-- other tool --> allow ({})
```

### File Layout

```
.claude/
  hooks/
    oro-search-hook          # Compiled Go binary
  settings.json              # Registers hook for PreToolUse
src/
  cmd/
    oro-search-hook/
      main.go                # Entry point: parse stdin, dispatch by tool_name
  internal/
    codesearch/
      summarize.go           # go/ast-based Go file summarization
      summarize_test.go
      classify.go            # Query classification (structural/literal/semantic)
      classify_test.go
      bypass.go              # Bypass rules (small files, configs, tests)
      bypass_test.go
      context.go             # Cross-hook context IPC (/tmp/ JSON files)
      context_test.go
```

### Hook Registration (settings.json)

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "command": ".claude/hooks/oro-search-hook",
        "timeout": 5000
      }
    ]
  }
}
```

## Accepted Trade-offs and Risks

1. **Go-only on day-one.** Oro's codebase is Go, so this covers the primary case. Non-Go files in referenced projects (CC-v3 TypeScript, Python scripts) will pass through unmodified until day-two. This is acceptable because Workers primarily read Oro source, not reference code.

2. **No daemon on day-one.** Each Read interception spawns a fresh `go/ast` parse. For typical Go files (<1000 lines), this completes in <10ms, well within the 5-second hook timeout. A daemon is only needed when parse times become a bottleneck (large files, many languages, or caching AST across reads).

3. **No semantic search on day-one.** Embeddings require infrastructure (index building, vector store, model). The literal + structural layers provide the majority of token savings. Semantic search is a day-three enhancement.

4. **CGo avoided on day-one.** The `tree-sitter/go-tree-sitter` package requires CGo for the C runtime. By using `go/ast` for Go files and `ast-grep` CLI for others, we keep a pure Go build with no CGo. This simplifies CI, cross-compilation, and the build process.

5. **Bypass list may be too broad or too narrow.** CC-v3 iterated on their bypass list over weeks. We start with their proven list (test files, config files, small files, explicit offset/limit) and tune based on real usage. The bypass logic is isolated in `bypass.go` for easy adjustment.

6. **Hook denial UX.** When the hook denies a Read, Claude sees the summary as an error/denial reason, not as file content. CC-v3 validated that Claude adapts well to this -- it reads the summary and requests specific lines if needed. But if Claude enters a loop of denied reads, we may need a "force read" escape hatch (e.g., reading with `limit=9999`).

7. **All roles get the same stack.** Architect, Manager, and Worker all use the same `oro-search-hook` binary. Workers rarely need deep analysis layers (CFG, DFG), but giving them the full stack avoids role-specific configuration complexity. The structure-only default mode is lightweight enough for all roles.

## Open Questions (to resolve during implementation)

1. **Build integration:** How to compile `oro-search-hook` and place it in `.claude/hooks/` as part of the normal build process. Makefile target? `go generate`?
2. **Go module structure:** Does `oro-search-hook` live in the same Go module as the rest of Oro, or as a separate module? Same module is simpler but couples the hook binary to the main project's dependencies.
3. **ast-grep availability:** Should day-two assume `ast-grep` is installed, or bundle it? It is a Rust binary (~15MB). Could check at hook startup and degrade gracefully.
