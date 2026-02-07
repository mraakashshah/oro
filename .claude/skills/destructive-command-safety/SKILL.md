---
name: destructive-command-safety
description: Use when about to run any git state-modifying command, file deletion, or irreversible operation
---

# Destructive Command Safety

## Overview

Irreversible operations deserve explicit confirmation. The cost of pausing is low; the cost of lost work is high.

## The Iron Law

```
NO DESTRUCTIVE COMMANDS WITHOUT EXPLICIT USER CONFIRMATION
```

## Command Classification

### Dangerous — ALWAYS ask first

**File operations:**
- `rm` / `rm -rf` — delete files/directories
- `rmdir` — remove directories
- `unlink` — remove files

**Git state-modifying:**
- `git checkout` — can overwrite uncommitted changes
- `git reset` — can lose commits
- `git clean` — deletes untracked files
- `git stash` — hides changes
- `git rebase` — rewrites history
- `git merge` — modifies branches
- `git push --force` — overwrites remote history
- `git commit --amend` — rewrites last commit

### Safe — No confirmation needed

- `git status`, `git log`, `git diff`
- `git branch` (list only)
- `git show`, `git blame`
- `ls`, `cat`, `find`, `grep`
- `go test`, `uv run pytest`, `ruff check`

## Steps

Before any dangerous command:

1. **Explain** what the command will do
2. **Show** the exact command
3. **Ask** "Should I run this?"
4. **Wait** for explicit "yes" or approval

## Archive vs Delete

When the user says "archive X":
- **MOVE** to archive folder (e.g., `mv X archive/`)
- Do **NOT** delete

When in doubt, prefer archiving over deleting.

## Examples

**WRONG:**
```
"Let me clean that up"
rm -rf /tmp/old-cache/
```

**RIGHT:**
```
"I can delete /tmp/old-cache/. Should I run `rm -rf /tmp/old-cache/`?"
[wait for explicit "yes"]
```

**WRONG:**
```
"Let me restore that file"
git checkout HEAD -- file.go
```

**RIGHT:**
```
"I can restore file.go from git. This will overwrite any uncommitted changes.
Run `git checkout HEAD -- file.go`?"
[wait for user confirmation]
```

## Red Flags - STOP

- About to run `rm` without asking
- About to `git reset --hard` or `git clean -f`
- About to force-push anything
- About to overwrite uncommitted changes
- Rationalizing "it's fine, I'll fix it if something goes wrong"

## Common Rationalizations

| Excuse | Reality |
|--------|---------|
| "It's just temp files" | User might need them. Ask. |
| "I can undo with git" | Not everything is committed. Ask. |
| "This is obviously safe" | You don't know the full context. Ask. |
| "I'll fix it if it breaks" | Prevention > cure. Ask. |
