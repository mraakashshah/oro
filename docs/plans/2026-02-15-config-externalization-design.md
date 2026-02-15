# Oro Config Externalization Design

**Date:** 2026-02-15
**Status:** Approved
**Bead:** oro-jzpq (P0)
**Descoped:** `.beads/` externalization → oro-6v9z (P4)

---

## Problem

Oro currently takes over the target project's `.claude/` directory and commits `.beads/` into git. This makes oro unusable in repos that already have their own Claude Code configuration, and pollutes project history with oro-specific files.

**What oro currently writes into the target repo (all tracked in git):**
- `.claude/settings.json` — 15+ hooks across 4 lifecycle phases
- `.claude/hooks/` — 20+ Python/Bash scripts (worktree guards, skill enforcement, context injection, memory capture, prompt injection guard, etc.)
- `.claude/hooks/beacons/` — `architect.md`, `manager.md` role prompts
- `.claude/skills/` — 35 skills
- `.claude/commands/` — `restart-oro`, `toggle-priming`
- `.claude/review-patterns.md` — code review patterns
- `.beads/` — SQLite DB, issues.jsonl, config, formulas, memory
- `docs/handoffs/` — session handoff YAMLs
- `docs/decisions-and-discoveries.md` — architectural log
- `CLAUDE.md` — oro system instructions

---

## Approach

**`--add-dir` + `--settings` injection at agent launch time.**

Claude Code supports:
- `--add-dir <path>` — discovers `.claude/skills/` from additional directories
- `--settings <path>` — additive merge of hook config over project settings
- `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` — loads CLAUDE.md from `--add-dir` paths

Oro agents are launched with these flags pointing to `~/.oro/`, so all oro config lives outside the target repo. The target project's own `.claude/` is preserved and merged additively.

---

## Directory Structure

```
~/.oro/                              # global oro home (ORO_HOME)
  .claude/
    skills/                          # 35 skills (discovered via --add-dir)
      brainstorming/SKILL.md
      test-driven-development/SKILL.md
      ...
    commands/                        # slash commands (restart-oro, toggle-priming)
    CLAUDE.md                        # oro system instructions
  hooks/                             # hook scripts (referenced by absolute paths)
    enforce-skills.sh
    worktree_guard.py
    session_start_extras.py
    memory_capture.py
    ...20+ scripts
  beacons/                           # role prompts
    architect.md
    manager.md
  projects/
    <project-name>/
      settings.json                  # generated hook config (absolute paths)
      handoffs/                      # session YAMLs
      decisions.md                   # architectural log
      review-patterns.md             # code review patterns

<target-project>/                    # UNTOUCHED by oro (except these two)
  .oro/                              # gitignored local anchor
    config.yaml                      # project name, feature toggles
  .beads/                            # stays for now (descoped to oro-6v9z)
  .claude/                           # project's OWN config (if any, untouched)
```

### Key Points

- `ORO_HOME` env var (defaults to `~/.oro/`) provides the root
- Skills go inside `~/.oro/.claude/skills/` so `--add-dir ~/.oro` auto-discovers them
- Hook scripts go to `~/.oro/hooks/` and `settings.json` references them with absolute paths
- Per-project state under `~/.oro/projects/<name>/` — settings, handoffs, decisions
- `.beads/` stays in the target repo for now (descoped to oro-6v9z)

---

## Agent Launch

### Interactive Agents (architect/manager)

Current (`tmux.go:102`):
```go
func execEnvCmd(role string) string {
    return fmt.Sprintf("exec env ORO_ROLE=%s BD_ACTOR=%s GIT_AUTHOR_NAME=%s claude", role, role, role)
}
```

New:
```go
func execEnvCmd(role, project string) string {
    oroHome := oroHomeDir() // ~/.oro or $ORO_HOME
    settingsPath := filepath.Join(oroHome, "projects", project, "settings.json")
    return fmt.Sprintf(
        "exec env ORO_ROLE=%s BD_ACTOR=%s GIT_AUTHOR_NAME=%s ORO_PROJECT=%s "+
            "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 "+
            "claude --add-dir %s --settings %s",
        role, role, role, project,
        oroHome, settingsPath,
    )
}
```

### Workers (claude -p)

Current (`worker.go:1044`):
```go
cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", model)
```

New:
```go
oroHome := os.Getenv("ORO_HOME")
if oroHome == "" { oroHome = filepath.Join(home, ".oro") }
project := os.Getenv("ORO_PROJECT")
settingsPath := filepath.Join(oroHome, "projects", project, "settings.json")

cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", model,
    "--add-dir", oroHome,
    "--settings", settingsPath,
)
```

### Environment Variables

| Var | Purpose | Set by |
|-----|---------|--------|
| `ORO_ROLE` | architect/manager/worker | `execEnvCmd` / worker env |
| `ORO_PROJECT` | project name for path resolution | `execEnvCmd` / worker env |
| `ORO_HOME` | override `~/.oro/` location (optional) | user |
| `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` | enable CLAUDE.md from `--add-dir` | `execEnvCmd` |

---

## Hook Path Resolution

### The Problem

All 20+ hook scripts hardcode relative paths:
- `session_start_extras.py:26` → `KNOWLEDGE_FILE = ".beads/memory/knowledge.jsonl"`
- `session_start_extras.py:28` → `BEACONS_DIR = ".claude/hooks/beacons"`
- `session_start_extras.py:286` → `HANDOFFS_DIR = "docs/handoffs"`
- `memory_capture.py:18` → `KNOWLEDGE_FILE = ".beads/memory/knowledge.jsonl"`

### The Mitigation

Every hook script resolves paths via `ORO_HOME` + `ORO_PROJECT` env vars with fallback to current paths.

Shared resolution pattern at the top of each hook:

```python
import os

def oro_home():
    return os.environ.get("ORO_HOME", os.path.expanduser("~/.oro"))

def oro_project_dir():
    home = oro_home()
    project = os.environ.get("ORO_PROJECT", "")
    if not project:
        return None  # fallback to current paths
    return os.path.join(home, "projects", project)

BEACONS_DIR = os.path.join(oro_home(), "beacons")
HOOKS_DIR = os.path.join(oro_home(), "hooks")
KNOWLEDGE_FILE = ".beads/memory/knowledge.jsonl"  # stays local (descoped)
HANDOFFS_DIR = os.path.join(oro_project_dir(), "handoffs") if oro_project_dir() else "docs/handoffs"
```

### Path Changes Per Hook

| Hook | Current path | New path |
|------|-------------|----------|
| `session_start_extras.py` | `.claude/hooks/beacons/{role}.md` | `$ORO_HOME/beacons/{role}.md` |
| `session_start_extras.py` | `docs/handoffs/` | `$ORO_HOME/projects/$ORO_PROJECT/handoffs/` |
| `memory_capture.py` | `.beads/memory/knowledge.jsonl` | unchanged (beads descoped) |
| `learning_reminder.py` | `.beads/memory/knowledge.jsonl` | unchanged (already has `ORO_KNOWLEDGE_FILE` env var) |
| `learning_analysis.py` | `docs/decisions-and-discoveries.md` | `$ORO_HOME/projects/$ORO_PROJECT/decisions.md` |

### Generated settings.json

Per-project settings.json uses `$HOME` for shell expansion:

```json
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{
        "type": "command",
        "command": "python3 $HOME/.oro/hooks/session_start_extras.py"
      }]
    }]
  }
}
```

`$HOME` expands in shell-executed hook commands. All paths use `$HOME/.oro/hooks/` rather than hardcoded absolute paths.

---

## `oro init` Command

```bash
cd /path/to/target-project
oro init myproject
```

### Steps

1. **Detect project name** — from argument, git remote, or directory name
2. **Create local anchor** in target repo:
   ```yaml
   # .oro/config.yaml
   project: myproject
   created: 2026-02-15
   ```
3. **Add `.oro/` to `.gitignore`** (if not already present)
4. **Create per-project dir** under `~/.oro/projects/myproject/`:
   ```
   settings.json          # generated from embedded template
   handoffs/
   decisions.md
   ```
5. **Generate `settings.json`** — Go template with `{{.OroHome}}` placeholders rendered to `$HOME/.oro/`
6. **First-time `~/.oro/` bootstrap** — if `~/.oro/` doesn't exist:
   - Create directory structure
   - Extract embedded skills, hooks, beacons from the oro binary (`go:embed`)

### Properties

- **Idempotent:** Re-running updates settings.json without wiping handoffs/decisions
- **Distribution:** Skills/hooks/beacons compiled into the `oro` binary via `go:embed` — single binary, no network dependency
- **Output:**
  ```
  ✓ Initialized project "myproject"
    Local anchor: .oro/config.yaml
    Project dir:  ~/.oro/projects/myproject/
    Settings:     ~/.oro/projects/myproject/settings.json

  Run 'oro start' to launch agents.
  ```

---

## Migration Plan

### The Constraint

Oro is both the source code AND an oro-managed project. Migration must work during the transition.

### Phase 1: Build Externalized Support (Code Changes)

All changes in the oro codebase. Nothing moves yet.

1. Add `--add-dir` and `--settings` flags to `execEnvCmd()` and `ClaudeSpawner`
2. Add `ORO_HOME` / `ORO_PROJECT` resolution to all hook scripts (**with fallback to current paths**)
3. Implement `oro init` command with `go:embed` asset extraction
4. Update `oro start` to read project name from `.oro/config.yaml` and pass it through

**The fallback is critical:** Every hook checks `ORO_PROJECT` env var first, falls back to current hardcoded paths if unset. This means:
- Old `oro start` (without flags) still works → hooks use current paths
- New `oro start` (with flags) works → hooks use externalized paths
- Manual `claude` (no oro) still works → hooks use current paths

### Phase 2: Migrate the Oro Repo

Once Phase 1 is deployed and tested:

1. Run `oro init oro` in the oro repo
2. Copy `.claude/skills/` → `~/.oro/.claude/skills/`
3. Copy `.claude/hooks/` → `~/.oro/hooks/`
4. Copy `.claude/hooks/beacons/` → `~/.oro/beacons/`
5. Copy `docs/handoffs/` → `~/.oro/projects/oro/handoffs/`
6. Copy `docs/decisions-and-discoveries.md` → `~/.oro/projects/oro/decisions.md`
7. Verify `oro start` works with externalized config
8. Remove `.claude/skills/`, `.claude/hooks/`, `.claude/settings.json` from git
9. Keep `.beads/` (descoped to oro-6v9z)

### Phase 3: Distribution

Skills/hooks/beacons compiled into the `oro` binary via `go:embed`. `oro init` extracts them to `~/.oro/` on first run. No network dependency, single binary distribution.

### Rollback

If externalization breaks: revert the `oro start` changes (remove `--add-dir` / `--settings` flags). Hooks fall back to current paths. Zero data loss since `.beads/` stays in place.

---

## Worktree Access

Workers run in git worktrees (`.worktrees/bead-<id>/`). All config access is through CLI flags and env vars, not filesystem layout.

```
Worker in worktree (.worktrees/bead-abc123/)
  ├── Has: --add-dir ~/.oro             → skills discovered
  ├── Has: --settings settings.json     → hooks loaded
  ├── Has: ORO_PROJECT=myproject        → hooks resolve paths
  ├── Has: ORO_HOME=~/.oro             → hooks resolve paths
  └── Has: .beads/ (via git worktree)   → beads accessible (stays in repo)
```

- **Skills:** `--add-dir` is absolute, works from any CWD
- **Settings/hooks:** `--settings` is absolute, works from any CWD
- **Hook scripts:** Absolute paths in settings.json
- **`.beads/`:** Git worktrees share the main repo's tracked files
- **`quality_gate.sh`:** Lives in target repo root, available in worktrees via git
- **`.oro/config.yaml`:** NOT in worktrees (gitignored). Not needed — workers get project identity from `ORO_PROJECT` env var

---

## What Stays vs What Moves

### Stays in Target Repo

| Item | Reason |
|------|--------|
| `.beads/` | Descoped (oro-6v9z). Git-tracked sync depends on it. |
| `.oro/config.yaml` | Local anchor — identifies project, holds toggles |
| `quality_gate.sh` | Project-specific quality checks |
| Project's own `CLAUDE.md` | Project-specific instructions for non-oro usage |
| `.claude/settings.local.json` | Personal permission overrides (MCPs, etc.) |

### Moves Out of Target Repo

| Item | Destination |
|------|-------------|
| `.claude/settings.json` | `~/.oro/projects/<name>/settings.json` |
| `.claude/skills/` (35 skills) | `~/.oro/.claude/skills/` |
| `.claude/hooks/` (20+ scripts) | `~/.oro/hooks/` |
| `.claude/hooks/beacons/` | `~/.oro/beacons/` |
| `.claude/commands/` | `~/.oro/.claude/commands/` |
| `.claude/review-patterns.md` | `~/.oro/projects/<name>/review-patterns.md` |
| `CLAUDE.md` (oro instructions) | `~/.oro/.claude/CLAUDE.md` |
| `docs/handoffs/` | `~/.oro/projects/<name>/handoffs/` |
| `docs/decisions-and-discoveries.md` | `~/.oro/projects/<name>/decisions.md` |

### Net Result for a Fresh Target Project

```
<target-project>/
  .oro/config.yaml        # gitignored, 3 lines
  .beads/                 # existing (descoped)
  quality_gate.sh         # project-specific
  <everything else>       # untouched
```

---

## Premortem (Resolved)

### Tigers (mitigated in this design)

| Risk | Mitigation |
|------|-----------|
| Hook scripts hardcode relative paths | All hooks resolve via `ORO_HOME`/`ORO_PROJECT` env vars with fallback to current paths |
| ClaudeSpawner missing flags | Thread `--add-dir` and `--settings` through `Spawn()` |
| bd CWD-based discovery | Descoped to oro-6v9z — `.beads/` stays in target repo for now |

### Elephants (accepted)

| Risk | Status |
|------|--------|
| bd is a separate codebase | Descoped — no bd changes needed in Phase 1 |
| Self-migration chicken-and-egg | Addressed via fallback paths in Phase 1 |

### Paper Tigers (non-issues)

| Risk | Why it's fine |
|------|--------------|
| `--settings`/`--add-dir` in `-p` mode | General CLI flags, not interactive-only |
| `.oro/` name collision | Very specific name, `oro init` can detect and warn |
| `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` stability | Documented in official Claude Code docs; fallback to SessionStart injection |
| Additive `--settings` merge conflicts | Target projects won't have oro-specific hooks |

---

## Dependencies

- **oro-6v9z** (P4): Externalize `.beads/` from target repo (descoped, depends on this bead)
