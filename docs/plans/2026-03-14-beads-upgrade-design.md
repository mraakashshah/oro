# Beads Upgrade: 0.49.5 → 0.60.0

**Date:** 2026-03-14
**Approach:** Minimal — fix what breaks, no new feature adoption
**Status:** Adversarial review PASS (rev 2)

## Context

Oro uses beads (`bd`) for issue tracking throughout: dispatcher work assignment,
worker lifecycle, init bootstrapping, stop/cleanup flows, and pre-commit hooks.
We're 11 versions behind (0.49.5 → 0.60.0). Key upstream changes:

- **v0.56.0:** SQLite + JSONL sync removed, Dolt-only backend
- **v0.59.0:** Daemon infrastructure removed (`bd daemon start/stop` gone)
- **v0.60.0:** `bd bootstrap`, `bd done`, epic close guards

**Verified:** `bd sync` and `bd daemon` are both `unknown command` on 0.60.0.

## Changes

### 1. Remove all `bd daemon` calls

**Risk:** None (daemon doesn't exist in 0.60.0, calls error)

**Go files:**
- `cmd/oro/cmd_cleanup.go:210-218` — `cleanupBdDaemon()` function definition
- `cmd/oro/cmd_cleanup.go:91` — call site in cleanup flow
- `cmd/oro/cmd_stop.go:313-315` — separate `bd daemon stop` call in runStopSequence
- `cmd/oro/cmd_stop.go:261` — doc comment referencing "step 6: stop bd daemon"
- `cmd/oro/cmd_stop_test.go:201-210` — test asserting `bd daemon stop` was called
- `cmd/oro/cmd_cleanup_test.go:666-675` — test asserting `bd daemon stop` was called

**Python/shell files:**
- `.claude/hooks/session_start_extras.py:596-642` — `_restart_bd_daemon_if_unresponsive()`
  calls `bd daemon status` and `bd daemon start`. Remove entire function + its call site.
  (Currently adds 15s+ timeout delay when bd is genuinely unresponsive.)
- `.claude/scripts/watch-loop.sh:18-24,53-60` — references `.beads/daemon.pid`,
  `.beads/daemon.lock`, `.beads/bd.sock`. Remove daemon file cleanup (harmless but dead).

**Decision:** Remove all daemon references. The daemon concept is gone upstream.

### 2. Remove all `bd sync --flush-only` calls

**Risk:** HIGH — `bd sync` is `unknown command` on 0.60.0. 5 call sites will error.

**Verified:** `~/go/bin/bd sync --flush-only` returns exit 1 with
`Error: unknown command "sync" for "bd"` on 0.60.0.

**Call sites:**
- `git/hooks/pre-commit` — blocks every commit if bd sync fails
- `cmd/oro/cmd_stop.go` — called during stop sequence
- `cmd/oro/cmd_dispatcher.go:114` — called during dispatcher stop
- `pkg/dispatcher/beadsource.go:210-217` — `CLIBeadSource.Sync()` method
- `pkg/dispatcher/dispatcher.go` — calls `beads.Sync()` during shutdown

**Test files:**
- `cmd/oro/cmd_dispatcher_test.go:354` — asserts `bd sync --flush-only` was called
- `pkg/dispatcher/beadsource_test.go:378-379` — asserts `--flush-only` flag

**Decision:** Remove all `bd sync` calls entirely. Dolt auto-commits after
each write — no manual flush needed. The `Sync()` method on BeadSource
becomes a no-op (return nil). Pre-commit hook removes the entire bd sync block.

### 3. Fix `initBeadsDB()` detection

**Risk:** Low — wrong detection means re-running `bd init` (idempotent)

**File:** `cmd/oro/cmd_init.go:439-451`

**Change:** Check `.beads/` directory existence instead of `.beads/beads.db`.
With Dolt backend, the directory contains `dolt/`, `metadata.json`, etc.

### 4. Clean up JSONL artifacts

**Risk:** Low — cosmetic/documentation debt

**Files:**
- `.gitattributes` — remove `.beads/issues.jsonl merge=beads` custom merge driver
- `.claude/skills/watching-oro/SKILL.md:127-137` — remove daemon kill instructions
- `.claude/skills/beads/SKILL.md` — remove JSONL references
- `docs/dev-setup.md` — remove "pre-commit runs bd sync --flush-only" reference

**User-level configs (manual update, noted in migration steps):**
- `~/.claude/rules/beads.md` — references `bd sync --flush-only` and `issues.jsonl`
- `~/.claude/projects/.../memory/MEMORY.md` — references JSONL workflow

### 5. Update install command (no code change needed)

**Current** (`cmd/oro/cmd_init.go:76`): `go install ...@latest` — already correct.

**Manual upgrade:** `go install github.com/steveyegge/beads/cmd/bd@latest`

## Migration Steps

1. `go install github.com/steveyegge/beads/cmd/bd@latest`
2. First `bd` command auto-migrates SQLite → Dolt (~seconds)
3. Kill lingering daemon processes: `pkill -f "bd daemon"`
4. Update user configs: remove JSONL references from `~/.claude/rules/beads.md`
5. Update memory: remove JSONL workflow from MEMORY.md
6. `make install && oro stop && oro start`

## Out of Scope

- Adopting `bd update --claim`, `bd done`, `bd bootstrap`
- Direct Dolt SQL integration
- Tracker plugin integrations (GitHub Issues, Linear)

## Testing

- `go test ./cmd/oro/... ./pkg/dispatcher/... -count=1 -timeout 120s`
- `uv run pytest tests/ -v` (Python hooks)
- Pre-commit hook must not block commits
- `oro init` in a fresh directory must succeed
- `oro stop` must succeed without warnings
- `bd ready`, `bd show`, `bd close` must work with Dolt backend
