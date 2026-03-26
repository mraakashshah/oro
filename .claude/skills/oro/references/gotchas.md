# Oro Gotchas

## Build
- **Always `make build`**, never `go build` directly — `embed.go` needs `_assets/` staged by `make stage-assets`
- Pre-commit hook runs golangci-lint + go test; pre-push runs full quality gate

## Worktrees
- `no_cd_guard` hook blocks `cd` into worktrees — use `git -C .worktrees/X` or `go test -C`
- Workers branch from old main → rebase before merging: `git -C .worktrees/X rebase main agent/X`
- Worktree may be cleaned up mid-session — check existence before assuming CWD is valid

## Workers
- Worker timeout: use `--timeout 20m` (default 15m too short for complex beads)
- Workers at >45% context may degrade — kill proactively if stuck
- `oro work` requires `--acceptance-criteria` to be set on the bead

## Dolt
- **Never `rm -rf` dolt dir** — use `dolt fsck` then `dolt fsck --revive-journal-with-data-loss`
- "multiple .doltcfg directories" error → `rm .beads/.doltcfg` (not the data dir)
- Multiple oro instances: Dolt handles concurrent reads/writes (MVCC), but two dispatchers on the same project will double-assign beads — no cross-process bead lock
- Shared dolt server runs on port 13307 (`~/.oro/dolt/`)

## Beads
- `bd close` then `bd sync` then `git add -f .beads/issues.jsonl` for clean commits
- `bd close X Y Z` supports closing multiple beads at once
- Stale beads may reappear after `bd sync` — re-close if needed
- **Never use `bd create --parent`** — it adds backwards dep (child blocked by epic). Use `bd update <child> --parent <epic>` + `bd dep add <epic> <child>` instead
- **Never use `bd edit`** — it opens `$EDITOR` which agents cannot use. Use `bd update` with flags.

## Shutdown
- `oro stop` requires an interactive TTY — use `oro attach` first, then stop from there
- Stop sequence flushes dolt (`bd dolt commit`) but intentionally leaves dolt server running (standalone `bd` commands still need it)
- Dolt server persists across sessions by design — it's managed by LaunchAgent, not the swarm lifecycle

## Dispatcher
- `clearBeadTracking` does NOT clear `worktreeByBead` (intentional for respawn)
- Worker dead-code pattern: workers may replace function calls with `_, _ = fn, arg` during QG retry — fixed with prompt constraints

## Testing
- `go test ./pkg/dispatcher/...` needs 120s+ timeout (60s too tight)
- UDS test spawners must accept connections in a loop (pollForSocket consumes probes)
- Reap child processes with `go cmd.Wait()` to avoid zombie timeouts
