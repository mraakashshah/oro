# Oro Gotchas

## Build
- **Always `make build`**, never `go build` directly — `embed.go` needs `_assets/` staged by `make stage-assets`
- Pre-commit hook runs golangci-lint + go test; pre-push runs full quality gate

## Worktrees
- `no_cd_guard` hook blocks `cd` into worktrees — use `git -C .worktrees/X` or `go test -C`
- Workers branch from old main → rebase before merging: `git -C .worktrees/X rebase main agent/X`
- Worktree may be cleaned up mid-session — check existence before assuming CWD is valid

## Workers
- Worker timeout: use `--timeout 20m` (default 15m too short for complex tasks)
- Workers at >45% context may degrade — kill proactively if stuck
- `oro work` requires acceptance criteria to be set on the task

## Tasks
- `oro task close <id>` then let the pre-commit hook sync task metadata before adding task state for clean commits
- Close multiple tasks one at a time; `oro task close` accepts exactly one id.
- Stale tasks may reappear after task metadata sync — re-close if needed
- `oro task create --parent <epic>` sets hierarchy only. It does not add dependency edges; add `oro task dep add <epic> <child>` explicitly when the epic must wait for the child.
- **Never use `interactive task editing`** — it opens `$EDITOR` which agents cannot use. Use `oro task update` with flags.

## Shutdown
- `oro stop` requires an interactive TTY — use `oro attach` first, then stop from there

## Dispatcher
- `clearBeadTracking` does NOT clear `worktreeByBead` (intentional for respawn)
- Worker dead-code pattern: workers may replace function calls with `_, _ = fn, arg` during QG retry — fixed with prompt constraints

## Testing
- `go test ./pkg/dispatcher/...` needs 120s+ timeout (60s too tight)
- UDS test spawners must accept connections in a loop (pollForSocket consumes probes)
- Reap child processes with `go cmd.Wait()` to avoid zombie timeouts
