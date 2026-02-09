# Bash tool dies silently after git worktree remove

**Date:** 2026-02-08
**Component:** Claude Code bash tool + git worktrees
**Severity:** high

## Symptom

Every bash command returns exit code 1 with zero output. Even `echo hello`, `pwd`, `ls /` produce no output and fail. The bash tool becomes completely unusable for the remainder of the session.

## Investigation

- Tried `cd /` — exit code 1, no output
- Tried `/bin/bash -c 'echo test'` — exit code 1, no output
- Tried `/usr/bin/git -C /path log` — exit code 1, no output
- Tried spawning a Task subagent with Bash — same behavior
- Non-bash tools (Glob, Read, Grep) continued working normally

## Root Cause

The Claude Code Bash tool persists the shell's working directory across calls. The session had `cd`'d into a git worktree directory (`.worktrees/bead-oro-by8/`), then ran `git worktree remove` which deleted that directory from the filesystem.

On macOS/Darwin, when a process's cwd is deleted:
- The kernel marks the cwd as invalid
- **All** subsequent command executions fail silently (exit 1, no output)
- This is not recoverable within the same shell session — even `cd /` fails because the shell itself can't execute anything

The Bash tool starts each command from the persisted cwd. Since that cwd no longer exists, every command immediately fails.

## Solution

Start a new Claude Code session. The fresh session gets a valid cwd.

## Prevention

1. **Never `cd` into a worktree.** Use absolute paths for all operations inside worktrees. The system instructions already say this: "Use absolute paths and avoid usage of `cd`."

2. **If you must work inside a worktree, `cd` back before removing it:**
   ```bash
   cd /path/to/main/repo && git worktree remove .worktrees/bead-xyz
   ```

3. **The safe worktree removal sequence:**
   ```bash
   # From the main repo (never cd into the worktree)
   git worktree remove .worktrees/bead-xyz
   git stash  # if dirty
   git rebase main bead/xyz
   git merge --ff-only bead/xyz
   git branch -d bead/xyz
   git stash pop
   ```

## Related

- Learnings entry `oro-06v`: "Remove worktrees BEFORE attempting rebase"
- The `using-git-worktrees` skill should enforce absolute-path-only access
