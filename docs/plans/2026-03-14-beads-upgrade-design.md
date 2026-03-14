# Beads Upgrade: 0.49.5 → 0.60.0

**Date:** 2026-03-14
**Approach:** Minimal — fix what breaks, no new feature adoption

## Context

Oro uses beads (`bd`) for issue tracking throughout: dispatcher work assignment,
worker lifecycle, init bootstrapping, stop/cleanup flows, and pre-commit hooks.
We're 11 versions behind (0.49.5 → 0.60.0). Key upstream changes:

- **v0.56.0:** SQLite + JSONL sync removed, Dolt-only backend
- **v0.59.0:** Daemon infrastructure removed (`bd daemon start/stop` gone)
- **v0.60.0:** `bd bootstrap`, `bd done`, epic close guards

## Changes

### 1. Remove `cleanupBdDaemon()` calls

**Risk:** None (daemon doesn't exist in 0.60.0, calls would error)
**Mitigation:** Make the call a no-op or remove entirely

**Files:**
- `cmd/oro/cmd_cleanup.go:210-218` — `cleanupBdDaemon()` function
- `cmd/oro/cmd_cleanup.go:91` — call site in cleanup flow
- `cmd/oro/cmd_stop.go:313-315` — call site in stop flow
- `cmd/oro/cmd_stop.go:261` — doc comment referencing step 6
- `cmd/oro/cmd_stop_test.go:201-210` — test asserting `bd daemon stop`
- `cmd/oro/cmd_cleanup_test.go:666-675` — test asserting `bd daemon stop`

**Decision:** Remove the function and all references entirely. The daemon
concept is gone upstream — no point keeping dead code.

### 2. Fix `initBeadsDB()` detection

**Risk:** Low — wrong detection means re-running `bd init` (which is idempotent)
**Mitigation:** Check for `.beads/` directory existence instead of `beads.db`

**Current code** (`cmd/oro/cmd_init.go:439-451`):
```go
func initBeadsDB(projectRoot string) {
    dbPath := filepath.Join(projectRoot, ".beads", "beads.db")
    if _, err := os.Stat(dbPath); err == nil {
        return // already initialized
    }
    // ... runs bd init
}
```

**New code:**
```go
func initBeadsDB(projectRoot string) {
    beadsDir := filepath.Join(projectRoot, ".beads")
    if _, err := os.Stat(beadsDir); err == nil {
        return // already initialized
    }
    // ... runs bd init
}
```

**Decision:** Check `.beads/` dir. With Dolt, the directory contains `dolt/`
subdirectory, `metadata.json`, etc. — no single canonical file to check.
Directory existence is the right signal.

### 3. Update pre-commit hook for JSONL removal

**Risk:** High if not handled — every `git commit` would fail
**Mitigation:** Make `bd sync --flush-only` graceful (no-op on failure)

**Current hook** (`git/hooks/pre-commit`):
```bash
if ! bd sync --flush-only >/dev/null 2>&1; then
    echo "Error: Failed to flush bd changes to JSONL" >&2
    exit 1
fi
```

**New hook:**
```bash
# Best-effort JSONL flush — no-op if command doesn't exist (bd ≥0.56)
bd sync --flush-only >/dev/null 2>&1 || true
```

Also remove any `git add .beads/issues.jsonl` staging since JSONL is no longer
the sync mechanism — Dolt handles its own versioning.

**Decision:** Fail-open. Dolt is the source of truth now, JSONL is legacy.
The pre-commit hook should not block commits over beads sync.

### 4. Update install command version

**Current** (`cmd/oro/cmd_init.go:76`):
```go
{InstallCmd: "go", InstallArgs: []string{"install", "github.com/steveyegge/beads/cmd/bd@latest"}}
```

**Decision:** Keep `@latest` — it already points to 0.60.0. No change needed
for the install command itself. The manual upgrade step is:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
# or
brew upgrade beads
```

### 5. Fix tests asserting `bd daemon stop`

**Files:**
- `cmd/oro/cmd_stop_test.go` — Remove assertion that `bd daemon stop` was called
- `cmd/oro/cmd_cleanup_test.go` — Remove assertion that `bd daemon stop` was called

**Decision:** Delete the assertions and any test setup that expects daemon calls.
Update the expected call count in cleanup/stop test sequences.

## Migration Steps (for existing installations)

1. `go install github.com/steveyegge/beads/cmd/bd@latest` (or `brew upgrade beads`)
2. First `bd` command auto-migrates SQLite → Dolt (one-time, ~seconds)
3. Kill any lingering `bd daemon` processes: `pkill -f "bd daemon"`
4. `oro stop && oro start` to pick up new oro binary

## Out of Scope

- Adopting `bd update --claim` (atomic claim)
- Adopting `bd done` (close alias)
- Adopting `bd bootstrap` (init replacement)
- Direct Dolt SQL integration (Approach C)
- Tracker plugin integrations (GitHub Issues, Linear)

These can be adopted incrementally in future work.

## Testing

- All existing `go test ./cmd/oro/... ./pkg/dispatcher/...` must pass
- Pre-commit hook must not block commits
- `oro init` in a fresh project must succeed
- `oro stop` must succeed without daemon errors
- `bd ready`, `bd show`, `bd close` must work with Dolt backend
