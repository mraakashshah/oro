prompt-template-interpolation: template uses `<placeholder>` for agent-filled values → prefer `fmt.Sprintf` with real values when the value is known at prompt-assembly time
`hint-duplication`: two places define the same key bindings (help overlay and status bar hints) → consider a single source of truth in a future bead.
`tmux-pre-attach-setup`: need to configure tmux state before attach → use `select-window`/`select-pane` via Runner before the `exec.Command` attach call.
case-insensitive-header-search: matching markdown headers → lowercase both sides, match on lowered text, slice from original to preserve casing.
`scope-creep`: bead does documentation cleanup but also removes 3 unrelated Go features → split into separate beads for traceability
`hook-install-worktree`: when symlinking git hooks via `$(pwd)`, verify behavior in worktree contexts where `.git` is a file, not a directory — worktrees share hooks from `$GIT_COMMON_DIR/hooks/`, so `make install-git-hooks` must be run from the main repo root to be effective for all worktrees.
`dead-code-noop`: function/method calls replaced with blank identifier assignments (`_, _ = fn, arg` or `_ = variable`) → CRITICAL. This silently destroys functionality while still compiling. Common during QG retry when worker misinterprets lint errors. The correct fix for unused variables is to remove the declaration, NOT to assign the function call to `_`.
`idempotent-stat-guard`: filesystem cleanup needs retry safety → `os.Stat` pre-check returning nil on `os.IsNotExist` before the actual operation
`stale-nolint-comment`: changing the derivation of a value → update the nolint justification comment to match the new source
`stealth-detection-duplication`: readProjectName and detectProjectMode share stealth hash logic → extract shared helper when a future bead touches either function.
`path-mode-duality-test`: function handles both relative defaults and absolute stealth paths → test both modes plus the conversion boundary (in-repo absolute → CWD-relative).
`fallback-on-empty-field`: when replacing a hardcoded path with a configurable field, check `if field == ""` and fall back to the legacy default — ensures zero-value Config still works
`lenient-vs-strict-resolution`: two functions resolve the same identity with different "not found" semantics → extract shared core, wrap with policy.
`zero-value-fallback`: new struct field with configurable path → check `if field == "" { use default }` at point of use, keeping zero-value safe for existing callers.
`find-exclusion-abs-passthrough`: absolute path outside repo tree used in `find . -not -path` exclusion → exclusion is unreachable. Consider omitting it from the generated script rather than including a dead clause.
`extracted-default-helper`: pure function with multiple interrelated defaults risks cyclomatic bloat in `withDefaults()` → extract a named helper (e.g., `defaultWorkerCounts`) that resolves defaults + cross-field clamping in isolation.
`double-hash-computation`: function computes identity hash, caller also computes same hash for display → return computed value from inner function to avoid redundant I/O.
`best-effort-symlink`: new worktree asset needs linking → add to `stageAssets` with `slog.Warn` on failure, no error return, Lstat guard for idempotency.
`stale-hardcoded-after-wiring`: replacing a hardcoded value in behavior but leaving it in nearby comments/log strings → search the same function and callers for other instances of the old hardcoded value.
`error-path-db-cleanup`: DB record created before fallible operation → add cleanup call in error path, log but don't block subsequent cleanup steps.
`double-nowFunc-under-lock`: calling `d.nowFunc()` multiple times under a single lock hold → capture once in `now := d.nowFunc()` and reuse.
`coalesce-scan-consistency`: new query scans nullable-with-default column differently from all existing queries in the same file → match the established COALESCE + int scan pattern for consistency.
`vestigial-encoder-field`: all send paths now use sendToWorker → encoder field on trackedWorker is dead code, candidate for removal in a cleanup bead.
`ac-vs-reality-check`: bead AC claims function is unused → grep production callers before deleting
`buffer-before-response`: HTTP handler executes template into `bytes.Buffer` first → prevents partial HTML on error, enables atomic content-type + body write
`buffer-template-render`: template renders to `http.ResponseWriter` incrementally → buffer into `bytes.Buffer`, check error, then `buf.WriteTo(w)` for atomic all-or-nothing HTTP responses.
`like-escape-clause-reminder`: pure escapeLike function escapes with backslash → caller must add `ESCAPE '\'` to the SQL LIKE expression for SQLite to honor the escaping.
