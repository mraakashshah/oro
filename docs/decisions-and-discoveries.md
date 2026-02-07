# Decisions and Discoveries

## 2026-02-07: bash ((PASS++)) kills scripts under set -e
**Tags:** #bash #gotcha #set-e
**Context:** quality_gate.sh silently exited after the first check with no error message.
**Discovery:** `((PASS++))` returns exit code 1 when PASS is 0 (0 is falsy in bash arithmetic). Combined with `set -e`, this silently kills the script. No error, no output — just stops.
**Fix:** Use `PASS=$((PASS + 1))` instead. This always returns exit 0.
**Implications:** Never use `((var++))` in `set -e` scripts. This is a well-known bash trap but produces zero diagnostic output, making it hard to debug.

## 2026-02-07: go-arch-lint v3 config — correct key is mayDependOn, excludeFiles uses regex
**Tags:** #go #tooling #go-arch-lint
**Context:** Tried `canDependOn`, `anyDependOn` — both rejected as unknown keys. `excludeFiles` uses Go regex, not globs — `**` is invalid regex.
**Discovery:** v3 config uses `mayDependOn` for dependency rules. `excludeFiles` takes Go regex patterns: `"yap/.+"` works, `"yap/**"` doesn't. Internal packages importing themselves (e.g., test files) requires `internal` in its own `mayDependOn` list.
**Implications:** Always check `go-arch-lint schema` for valid config keys. Use `.+` not `**` in excludeFiles.

## 2026-02-07: biome v2 config — no ignore field in files section
**Tags:** #tooling #biome #json
**Context:** biome v2.3.11 rejected `ignores` key in `files` section. Only `includes`, `maxSize`, `ignoreUnknown`, `experimentalScannerIgnores` are valid.
**Discovery:** biome v2 removed the simple `ignore` field. VCS integration (`useIgnoreFile: true`) handles gitignored dirs, but submodules and tracked dirs need explicit scoping in the CLI command or `experimentalScannerIgnores`.
**Fix:** Scope biome in the quality gate command: `biome check --files-ignore-unknown=true docs/ .github/ .beads/ *.json` rather than scanning `.`.
**Implications:** When biome can't exclude via config, scope via CLI args. Always run `biome migrate --write` after version bumps.

## 2026-02-07: Quality gate scoping — never scan . for tools that walk directories
**Tags:** #tooling #quality-gate #architecture
**Context:** gofumpt, goimports, biome, go-arch-lint all hung or failed when scanning `.` because `references/` and `yap/` contain thousands of files from submodules and reference repos.
**Decision:** Every tool in quality_gate.sh must be explicitly scoped to source directories (GO_DIRS, explicit paths) — never `.` or `./...` for tools that walk the filesystem. Go toolchain (`go test ./...`, `go build ./...`) is fine because Go respects module boundaries.
**Implications:** When adding new tools to the gate, always specify explicit directories. Test with the full repo, not just src dirs.

## 2026-02-07: golangci-lint v2 gofumpt version mismatch
**Tags:** #go #tooling #golangci-lint #gofumpt
**Context:** During oro-fza foundation setup, files formatted by standalone `gofumpt` (v0.9.2) were still flagged as "not properly formatted" by golangci-lint v2.8.0's bundled gofumpt formatter.
**Decision:** Don't enable gofumpt as a golangci-lint formatter. Run standalone `gofumpt` in the quality gate and Makefile instead. Keep golangci-lint for linting only.
**Implications:** Formatting and linting are separate concerns with separate tools. Version coupling between golangci-lint's bundled formatters and standalone tools causes false positives. Also: golangci-lint v2 moved formatters to a `formatters:` section (not `linters:`), and requires `version: "2"` at the top of the config.

## 2026-02-07: Add reflection step to finishing-work
**Tags:** #skills #workflow #feedback-loops
**Context:** Reviewed aleiby/claude-skills/tackle — its reflect phase logs friction after every PR as queryable data
**Decision:** Added Step 4 (Reflect) to finishing-work skill. Captures off-script moments, slowdowns, and improvement suggestions before cleanup.
**Implications:** Skills can self-improve over time if friction is consistently logged. "Clean run" should be rare — most work has learnable friction.

## 2026-02-07: Skip autoskill pattern (user-correction-driven learning)
**Tags:** #skills #decisions #philosophy
**Context:** Reviewed AI-Unleashed/Claude-Skills/autoskill — watches for user corrections during sessions and proposes skill edits
**Decision:** Not adopted. We prefer self-directed reflection (agent notices its own friction) over user-directed correction harvesting.
**Implications:** The reflect step in finishing-work is our feedback loop. Keep it self-directed.

## 2026-02-07: Memory system — SQLite + hybrid extraction, not JSONL
**Tags:** #memory #architecture #decisions
**Context:** Resolving open questions in memory system spec. JSONL was proposed for simplicity but retrieval (finding the right 3 memories for a 200-token prompt budget) is the hard problem, not storage. Keyword grep can't rank results.
**Decision:** Single SQLite DB (`.oro/state.db`) for both runtime state and memories. FTS5 for BM25 ranked search. Embeddings column reserved for future semantic search. Hybrid extraction: worker self-report markers (real-time) + daemon post-session extraction (background) + periodic consolidation. LanceDB rejected — no Go SDK.
**Implications:** One DB, one Go driver, one dependency. Memories not git-tracked (SQLite binary doesn't diff). Human-curated knowledge goes to `docs/decisions-and-discoveries.md`. CC-v3's retrieval architecture on a local-first backend.

## 2026-02-07: Create review-docs and review-implementation skills
**Tags:** #skills #review #quality
**Context:** Reviewed Xexr/marketplace review-documentation (1200-line multi-LLM orchestration) and review-implementation skills
**Decision:** Created two lean skills (<300 words each). Extracted structured review categories and severity-weighted output format from Xexr. Skipped multi-LLM dispatch, mermaid diagrams, execution checklists — Claude Code only.
**Implications:** Doc review and implementation review are separate concerns with different triggers. Both use read-only review phase before fixes.
