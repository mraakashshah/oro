prompt-template-interpolation: template uses `<placeholder>` for agent-filled values → prefer `fmt.Sprintf` with real values when the value is known at prompt-assembly time
`hint-duplication`: two places define the same key bindings (help overlay and status bar hints) → consider a single source of truth in a future bead.
`tmux-pre-attach-setup`: need to configure tmux state before attach → use `select-window`/`select-pane` via Runner before the `exec.Command` attach call.
case-insensitive-header-search: matching markdown headers → lowercase both sides, match on lowered text, slice from original to preserve casing.
`scope-creep`: bead does documentation cleanup but also removes 3 unrelated Go features → split into separate beads for traceability
`hook-install-worktree`: when symlinking git hooks via `$(pwd)`, verify behavior in worktree contexts where `.git` is a file, not a directory — worktrees share hooks from `$GIT_COMMON_DIR/hooks/`, so `make install-git-hooks` must be run from the main repo root to be effective for all worktrees.
