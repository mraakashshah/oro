---
name: git-commits
description: Use when creating git commits -- determines type, scope, and message from staged changes
---

# Git Commits

## Format

```
<type>(<scope>): <imperative description>
```

**Imperative mood**: "add feature" not "added feature" or "adds feature".

## Type Selection

| Type | When | Tier |
|------|------|------|
| `feat` | New functionality, new files with exports | 1 — full context |
| `fix` | Bug fix, error handling correction | 1 — full context |
| `refactor` | Restructure without behavior change | 2 — brief context |
| `test` | Add/update tests only | 2 — brief context |
| `docs` | Documentation, comments | 3 — summary only |
| `chore` | Dependencies, config, tooling | 3 — summary only |
| `perf` | Performance improvement | 1 — full context |
| `ci` | CI/CD pipeline changes | 3 — summary only |
| `build` | Build system, packaging | 3 — summary only |
| `style` | Formatting, linting (no logic change) | 3 — summary only |

## Scope Detection

Infer from the primary area of change:

- File path: `internal/auth/token.go` → `auth`
- Directory: `cmd/bd/` → `bd`
- Feature: `.claude/skills/` → `skills`
- Cross-cutting: omit scope — `feat: add retry logic across services`

## Tiers

**Tier 1** (feat, fix, perf): Body with what changed and why. List affected files if 3+.

**Tier 2** (refactor, test, build, ci): One-line body if helpful. Skip if obvious.

**Tier 3** (docs, style, chore): Subject line only. No body needed.

## Rules

- Subject line: max 72 characters
- No period at end of subject
- No co-author credits or AI attribution
- No generic messages ("update files", "fix stuff", "WIP")
- Scope in the commit, not the description: `feat(auth): add token refresh` not `feat: add auth token refresh`
- One logical change per commit when possible

## Examples

```bash
# Tier 1 — full context
git commit -m "feat(skills): add 4 skills from openclaw and beads"

# Tier 1 — with body
git commit -m "$(cat <<'EOF'
fix(sync): resolve race condition in concurrent token refresh

Token store was not mutex-protected during refresh. Multiple goroutines
could trigger simultaneous refreshes, causing token overwrites.
EOF
)"

# Tier 2
git commit -m "refactor(storage): extract query builder from sqlite module"

# Tier 3
git commit -m "docs: update cross-reference notes"
git commit -m "chore: bump golangci-lint to v1.62"
```

## Red Flags

- Committing without reviewing `git diff --staged`
- Generic messages that don't explain the "why"
- Mixing unrelated changes in one commit
- Adding co-author footers or AI attribution
