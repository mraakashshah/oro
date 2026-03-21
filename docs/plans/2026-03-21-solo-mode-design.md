# Stealth Mode Design: Zero-Footprint Oro

**Date:** 2026-03-21
**Status:** Draft (adversarial review round 2)
**Problem:** Oro currently pollutes repos with 186 tracked files (.claude/, CLAUDE.md, docs/plans/, .beads/issues.jsonl). This prevents using oro on team repos, OSS repos, and client repos where you don't own the codebase.

## Decision: Two Modes

- `oro init` — **Standard mode** (default). Current behavior, everything committed.
- `oro init --stealth` — **Stealth mode**. Zero git-tracked footprint.

**Prior art:** beads upstream (`bd init --stealth`) uses `.git/info/exclude` to hide `.beads/` in the repo root. Our approach goes further — moving data to `~/.oro/` so not even a hidden directory exists in the repo.

## Project Identity

**Standard mode**: continues using name-based keys (`~/.oro/projects/<name>/`) for backwards compatibility. No migration needed.

**Stealth mode**: uses SHA-256 truncated to 16 hex chars of the repo's absolute resolved path. 64-bit collision resistance (~4 billion repos before 50% collision). Stored in `~/.oro/projects/s-<hash>/config.yaml`. The `s-` prefix distinguishes stealth projects from name-based standard projects.

**Mode discovery** (the bootstrap problem): `oro start` computes the hash from CWD, checks `~/.oro/projects/s-<hash>/config.yaml`. If it exists and `mode: stealth`, use stealth paths. If not found, fall back to name-based lookup (standard mode). No local anchor file needed in the repo.

## Where Everything Lives

### Fully External (no repo files at all)

| Artifact | Stealth Location | Standard Location |
|----------|--------------|---------------|
| Beads (dolt data) | `~/.oro/projects/s-<hash>/beads/` | `.beads/` |
| Worktrees | `~/.oro/projects/s-<hash>/worktrees/` | `.worktrees/` |
| Daemon PID/socket | `~/.oro/projects/s-<hash>/` | `~/.oro/projects/<name>/` |
| CLAUDE.md | `~/.claude/projects/<claude-path>/CLAUDE.md` | `./CLAUDE.md` |
| Project config | `~/.oro/projects/s-<hash>/config.yaml` | `~/.oro/projects/<name>/config.yaml` |
| quality_gate.sh | `~/.oro/projects/s-<hash>/quality_gate.sh` | repo root |

### In-Repo but Gitignored (via `.git/info/exclude`)

| Artifact | Stealth Location | Notes |
|----------|--------------|-------|
| Plans & handoffs | `oro-docs/plans/`, `oro-docs/handoffs/` | Blocked from commit by pre-commit hook |
| Hooks | `.claude/settings.local.json` | Additive merge only |
| Skills | `.claude/skills/` | Additive only, never delete existing |

### Git Safety

| Protection | Mechanism |
|------------|-----------|
| `oro-docs/` not tracked | `.git/info/exclude` entry |
| `oro-docs/` can't be committed | Pre-commit hook rejects staged `oro-docs/` files |
| `agent/*` branches can't be pushed | Pre-push hook blocks `agent/*` in stealth mode |
| Existing git hooks preserved | Hooks use wrapper pattern (see below) |
| `--no-verify` bypass | Documented as known limitation; defense-in-depth, not sole protection |

## `oro init --stealth` Behavior

Preconditions: must be in a git repo (`.git/` exists). Fails with error if not.

1. Compute `<hash>` = SHA-256(abs(CWD)) truncated to 16 hex chars (supports monorepos — each subdir gets its own project)
1. Resolve git root via `git rev-parse --show-toplevel` (for `.git/info/exclude` and worktree ops)
1. Create `~/.oro/projects/s-<hash>/` with `config.yaml`:
   ```yaml
   mode: stealth
   project_root: /abs/path/to/repo
   ```
1. Initialize beads at `~/.oro/projects/s-<hash>/beads/` via `bd init --db <path>`
1. Generate `quality_gate.sh` at `~/.oro/projects/s-<hash>/quality_gate.sh` (with correct worktree exclusion path)
1. Write `~/.claude/projects/<claude-path>/CLAUDE.md` with oro instructions
1. Merge oro hooks into `.claude/settings.local.json` (additive — read existing JSON, add missing hook entries, preserve all existing entries)
1. Copy oro skills to `.claude/skills/` (additive — skip if skill dir already exists)
1. Create `oro-docs/` directory
1. Add to `.git/info/exclude`: `oro-docs/`, `.claude/settings.local.json`
1. Install git hooks with wrapper pattern (preserve existing hooks)
1. Do NOT create `.oro/` in repo root
1. Do NOT modify `.gitignore`
1. Do NOT create `CLAUDE.md` in repo root
1. Set directory permissions to 0o700 for `~/.oro/projects/s-<hash>/`

**Error handling:**
- `~/.oro/` can't be created → fail with actionable error
- `.git/info/exclude` is read-only → warn, continue (hooks are backup protection)
- `oro-docs/` name collision with existing dir → fail with error, suggest alternative

## `oro init` Standard Behavior

Same as today. Uses name-based `~/.oro/projects/<name>/` keys. No changes.

## `bd` Integration

`bd` CLI uses `--db` flag for path override (not env var — `BEADS_DIR` does not exist in `bd`).

- **Dispatcher (`CLIBeadSource`)**: add a `bdExtraArgs []string` field (not on `ExecCommandRunner` — that's shared). In stealth mode, set to `["--db", "~/.oro/projects/s-<hash>/beads/"]`. Prepended to every `bd` invocation.
- **Workers**: workers shell out to `bd` directly from their worktrees. In stealth mode, the worktree has no `.beads/`. Fix: `oro init --stealth` creates a wrapper script at `~/.oro/projects/s-<hash>/bd-wrapper.sh` that prepends `--db`. The worker prompt includes `alias bd='~/.oro/projects/s-<hash>/bd-wrapper.sh'` or the `AssignPayload` includes a `BdCommand` field with the full path. Simpler: set `BEADS_DB` env var on the worker process (oro controls worker spawn via `procMgr.Spawn`).
- **`oro bd <args>`**: wrapper command that resolves project from CWD, prepends `--db`, then runs `bd`. For standalone terminal use.

## Quality Gate in Stealth Worktrees

In stealth mode, `quality_gate.sh` lives at `~/.oro/projects/s-<hash>/quality_gate.sh`, not in the repo. Workers look for it at `scripts/quality_gate.sh` or `quality_gate.sh` inside their worktree.

Fix: on worktree creation (`WorktreeManager.Create`), if stealth mode, symlink `<worktree>/quality_gate.sh → ~/.oro/projects/s-<hash>/quality_gate.sh`. The symlink is inside the worktree which is external to the repo — no footprint. Alternatively, the worker's `findQualityGateScript` function accepts a `qualityGatePath` override from `AssignPayload`.

## Resolved Paths (`ProjectPaths` struct)

All path-dependent components read from a single `ProjectPaths` struct, populated at startup from mode + config:

```go
type ProjectPaths struct {
    Mode          string // "standard" | "stealth"
    RepoRoot      string // absolute path to repo root
    BeadsDir      string // .beads/ or ~/.oro/projects/s-<hash>/beads/
    WorktreesDir  string // .worktrees/ or ~/.oro/projects/s-<hash>/worktrees/
    OroDocsDir    string // docs/ or oro-docs/
    QualityGate   string // ./quality_gate.sh or ~/.oro/.../quality_gate.sh
    OroProjectDir string // ~/.oro/projects/<name>/ or ~/.oro/projects/s-<hash>/
    ClaudeMD       string // ./CLAUDE.md or ~/.claude/projects/<path>/CLAUDE.md
    ReviewPatterns string // assets/review-patterns.md or ~/.oro/.../review-patterns.md
    ConfigYAML     string // .oro/config.yaml or ~/.oro/projects/s-<hash>/config.yaml
    WorkerProgram  string // ./worker-program.md or ~/.oro/.../worker-program.md
}
```

### Files that need `ProjectPaths` threading (31 identified)

| File | Hardcoded Path | Fix |
|------|---------------|-----|
| `cmd/oro/cmd_cleanup.go:53` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_cleanup.go:253` | `".worktrees"` | `paths.WorktreesDir` |
| `cmd/oro/cmd_stop.go:136` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_stop.go:190` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_work.go:378` | `".worktrees"` | `paths.WorktreesDir` |
| `cmd/oro/cmd_start.go:240` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `cmd/oro/cmd_start.go:410` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_start.go:429` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `cmd/oro/cmd_start.go:519` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_doctor.go:123` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_shell.go:109` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `cmd/oro/paths.go:78` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `cmd/oro/quality_gate_gen.go:533,551` | `".worktrees"` | `paths.WorktreesDir` |
| `cmd/oro/architect.go:26,106` | `"docs/plans/"`, `"docs/handoffs/"` | `paths.OroDocsDir` |
| `pkg/dispatcher/dispatcher.go:510` | `protocol.BeadsDir` | `paths.BeadsDir` |
| `pkg/dispatcher/dispatcher.go:1828` | `filepath.Dir(beadsDir)` for repo root | `paths.RepoRoot` (do NOT derive from beadsDir) |
| `pkg/dispatcher/worktree_manager.go:45,210,251` | `protocol.WorktreesDir` | `worktreesDir` field |
| `pkg/ops/review_prompt.go:128` | `"CLAUDE.md"` | `paths.ClaudeMD` |
| `pkg/ops/review_prompt.go:134` | `".claude/rules/"` | fallback chain: repo `.claude/rules/` then `~/.claude/rules/` |
| `pkg/ops/ac_prompt.go:56` | `"docs/plans/"` | `paths.OroDocsDir + "/plans/"` |
| `pkg/dispatcher/beadsource.go` | no `--db` flag | `bdExtraArgs` on CLIBeadSource |
| `langprofile/detect.go:38` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `langprofile/config.go:102` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `cmd/oro/cmd_doctor.go:123` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_mg.go:115,130` | `".beads"` | `paths.BeadsDir` |
| `cmd/oro/cmd_init.go:440` | `".beads"` | stealth init uses own path |
| `cmd/oro/store.go:42` | `".oro/config.yaml"` | `paths.ConfigYAML` |
| `pkg/mg/data/source.go` | bare `bd list --json` | `bdExtraArgs` or env var |
| `pkg/mg/data/metadata.go:61,94` | `".beads"` | `paths.BeadsDir` |
| `pkg/dispatcher/assign_payload.go:72` | `worker-program.md` from repo root | `paths.WorkerProgram` |
| `cmd/oro/quality_gate_gen.go:542,550` | `".beads/"`, `"docs/"` in script body | template with `paths.BeadsDir`, `paths.OroDocsDir` |

**`GitWorktreeManager` change:** Add `worktreesDir string` field to struct (set at construction). Replace all `filepath.Join(g.repoRoot, protocol.WorktreesDir, ...)` with `filepath.Join(g.worktreesDir, ...)`. Interface unchanged.

**`CLIBeadSource` change:** Add `bdExtraArgs []string` field. Prepend to all `bd` command invocations. Set from `ProjectPaths` at construction.

**`appendReviewPatterns` change:** In stealth mode, write to `paths.ReviewPatterns` (in `~/.oro/`) instead of `assets/review-patterns.md` in the repo. Zero-footprint maintained.

**`readProjectName()` change (bootstrap function):** This is the root of all path resolution. Must become stealth-aware: try `.oro/config.yaml` first (standard mode). If not found, compute hash from CWD and check `~/.oro/projects/s-<hash>/config.yaml` (stealth mode). All downstream callers (`readProjectConfig`, `preflightAndCheckRunning`, `cmd_shell`, `store.go`) inherit the correct mode. Without this, `oro start` on a stealth project would auto-run `oro init` (standard), creating `.oro/` and defeating stealth.

**`oro mg` change:** The Mardi Gras TUI shells out to `bd` independently via `pkg/mg/data/source.go`. In stealth mode, these calls need `--db`. Fix: `oro mg` resolves `ProjectPaths` at startup (same as `oro start`) and passes `bdExtraArgs` to the mg data layer. Or: set `BEADS_DB` env var for the mg subprocess.

**`quality_gate_gen.go` change:** The generated shell script body hardcodes `docs/`, `.beads/`, `.worktrees/` in find exclusions and biome paths. Fix: `writeQualityGateScript` accepts `ProjectPaths` and templates the correct directory names into the generated script.

**`langprofile` change:** Accept `configPath string` parameter instead of deriving from project root. Caller passes `paths.ConfigYAML`.

## Git Hook Wrapper Pattern

To avoid overwriting existing hooks:

```bash
#!/bin/sh
# oro pre-commit wrapper
# Run existing hook first (if it was renamed)
if [ -x .git/hooks/pre-commit.user ]; then
    .git/hooks/pre-commit.user "$@" || exit $?
fi
# oro checks:
# Reject any staged files under oro-docs/
if git diff --cached --name-only | grep -q '^oro-docs/'; then
    echo "error: oro-docs/ files should not be committed (stealth mode)"
    exit 1
fi
```

On install: check `git config core.hooksPath` — if set, install to that directory instead of `.git/hooks/`. If `.git/hooks/pre-commit` (or hooksPath equivalent) exists, rename to `.user` suffix, install wrapper. On `oro deinit`: restore `.user` backup, remove `.git/info/exclude` entries added by oro.

## Edge Cases

- **Repo moved/renamed**: `config.yaml` stores `project_root`. On `oro start`, if CWD doesn't match, warn and offer to update. Hash is recomputed from new CWD → new project dir. Old dir becomes orphaned.
- **Multiple clones**: hash is unique per absolute path, each clone gets independent state.
- **Symlinked repos**: `filepath.EvalSymlinks` before hashing to ensure consistency.
- **CI/CD**: Stealth mode is desktop-only. Documented as such. CI has no `~/.oro/` or `.git/info/exclude`.
- **`oro deinit`**: Removes oro-docs/, .claude/settings.local.json oro entries, .git/hooks wrappers, .git/info/exclude entries. Does NOT delete `~/.oro/projects/s-<hash>/` (user's data). Provides `oro deinit --purge` to also delete external data.
- **No .git/**: `oro init --stealth` requires a git repo. Fails with actionable error.
- **Git submodules**: Detect via `.git` being a file (not dir). Use `git rev-parse --git-common-dir` for `.git/info/exclude` path.
- **Monorepos**: Hash is based on CWD at init time (not `git rev-parse --show-toplevel`). This allows multiple oro projects within one repo. Init step 1 uses CWD for hashing but still discovers git root for `.git/info/exclude` and worktree operations.
- **`oro-docs/` name collision**: Check before creating. Fail with error suggesting `--docs-dir=<alt>` override.
- **Cross-filesystem worktrees**: Warn if `~/.oro/` is on a different filesystem than the repo. Git worktrees across filesystems may be slow.
- **Orphaned projects**: `oro doctor` lists `~/.oro/projects/s-*/` dirs where `project_root` no longer exists.
- **Read-only `.git/info/exclude`**: Warn and continue. Pre-commit hook is the backup protection.
- **`--no-verify` bypass**: Documented. Defense-in-depth — `.git/info/exclude` is primary, hooks are secondary.

## Data Safety

- **Stealth mode backup**: `oro-dzdq.2` (periodic full-state JSONL backup) writes to `~/.oro/projects/s-<hash>/beads/backup/full-state.jsonl`. Critical because `~/.oro/` is the single copy.
- **`~/.oro/` deletion warning**: `oro init --stealth` prints: "All stealth project data lives in ~/.oro/. Back it up."
- **File permissions**: `~/.oro/projects/s-<hash>/` created with 0o700 (owner-only access).

## Testing Strategy

### Unit tests (per component)
- `ProjectPaths` resolution from config (standard + stealth)
- `CLIBeadSource` with `bdExtraArgs` (verify `--db` prepended)
- `GitWorktreeManager` with custom `worktreesDir`
- Git hook wrapper install/uninstall/preserve
- `.git/info/exclude` additive write
- `settings.local.json` additive merge
- Hash computation (symlinks, relative paths, trailing slashes)

### Integration tests
- `oro init --stealth` → verify no repo files created
- `oro init --stealth` → `oro start` → bead assignment → worktree at `~/.oro/` → QG → merge → cleanup
- `oro init --stealth` on repo with existing hooks → hooks preserved
- `oro init --stealth` on repo with existing `.claude/settings.local.json` → merged
- `oro deinit` → verify repo is clean
- Mode discovery: `oro start` in stealth project without `.oro/` anchor
- Repo move: `oro start` after repo path changes

### What NOT to test
- Standard mode behavior (existing tests cover this)
- `bd` internals (`--db` flag is bd's responsibility)

## What Stealth Mode Does NOT Do

- Does not modify `.gitignore`
- Does not create `CLAUDE.md` in repo root
- Does not create `.oro/` in repo root
- Does not track `issues.jsonl` in git
- Does not write to `docs/` in the repo
- Does not push `agent/*` branches
- Does not overwrite existing git hooks
- Does not work in CI/CD (desktop-only, by design)
