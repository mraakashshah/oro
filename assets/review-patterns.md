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
test-hook-on-prod-struct: adding a `testXxxFn func()` field on a production struct that is read without holding the struct's mutex → either guard the read with the same lock or gate the field behind `_test.go`-only extension.
worker-internal-monologue: test comments read like chat-thinking ("wait", "let me reconsider", "actually") → strip before landing; comments describe the scenario, not the author's deliberation.
backoff-fn-injection: new retry/recovery path needs deterministic tests → add `somethingBackoffFn func(int) time.Duration` field on Dispatcher, gate real backoff through `d.somethingBackoff(n)`, let tests set it to `return 0`. Mirror the `loopPanicBackoffFn` pattern (dispatcher.go:619-633).
misnamed-negative-test: test name claims to verify a failure path but body only smokes the success path → rename to match what's asserted, or delete if redundant with the primary contract test.
helper-migration-leaves-redundant-setup: replacing `sql.Open` with a helper that encapsulates PRAGMA setup → search the call site for manual `PRAGMA journal_mode` / `busy_timeout` execs that are now no-ops and remove them in the same edit.
redundant-pragma-after-openDB: test opens via `dbutil.OpenDB` but keeps the old explicit `PRAGMA journal_mode=WAL`/`busy_timeout` calls from its pre-migration state → remove the redundant Execs, rely on OpenDB's contract.
misleading-nolint-justification: adding `//nolint:foo // reason` whose reason doesn't match the file's actual code → verify the justification is true at the time of writing, or drop it.
wrapcheck-vs-thin-delegation: AC asks for `return other.Fn()` single-line delegation but `wrapcheck` is enabled → either add `//nolint:wrapcheck` with justification or accept a redundant outer wrap; document choice in bead.
dbutil-openDB-probe-tautology: WAL-verification test using dbutil.OpenDB as the probe → probe may set WAL itself, masking regressions in the unit under test; use a raw sql.Open probe or file-header inspection.
ac-contradiction-resolution: AC signature and edges conflict → satisfy the concrete edge case in tests, note the deviation.
optional-capability-interface: core interface shouldn't force capabilities all impls support → extract separate interface (e.g., VocabPersister) and type-assert at the call site.
interface-unused-until-followup-bead: introducing interface + renaming concrete in one bead, widening callers in follow-ups → keep concrete receivers in call sites this bead to preserve shape compatibility; land widening after dependents rebase.
interface-widen-struct-rename: widening Store field to interface named same as existing concrete struct → rename struct (e.g. Embedder→TFIDFEmbedder), define interface + optional extension, add compile-time assertions in test file
parenthetical-token-noise: comma-split field has inline `(annotations)` with dots or numbers → strip `\s*\([^)]*\)` from each token before path heuristics
cli-doc-existing-directive: documentation bead exposing already-implemented protocol behavior → CLI test with stateful mock dispatcher; semantic correctness lives in the dispatcher package's own tests.
stale-ac-field-name: AC references field name that conflicts with linter → update doc comments to match the actually-chosen identifier, not the AC's original spec
placeholder-digest-registry: static registry ships with placeholder hashes → mark with `TODO(bead-id)` so future bead that fills in real values is easy to locate.
yaml-tri-state-bool: YAML field must distinguish unset/true/false → `*bool` with `XOrDefault()` accessor returning the default when nil (keeps zero-value round-trip safe).
installer-test-hook: local-tarball test needs to bypass curl+checksum → add `_ORO_TARBALL_OVERRIDE` env var branch + PATH-prepended mock binary writing to a log file.
ac-test-vs-assert-mismatch: AC "Test" column enumerates N tests but "Assert" column describes N+1 behaviors → either add the missing test or prune the assertion from AC before closing.
vendored-arch-specific-static-archive: checking in a `.a` under a `cgo && darwin` tag when the archive is arm64-only → either narrow tag to `arm64` or fetch/bundle per-arch in installer before the epic lands.
cancel-vs-result-race: buffered resultCh + select on ctx.Done → document that the resource from a late-arriving result may need disposal if it owns external handles.
rate-limiter-default-injection: production worker needs configurable rate limiter for testing → declare struct field as `interface{ Wait(context.Context) error }`, fall back to `rate.NewLimiter(rate.Limit(N), 1)` when nil, tests inject `rate.NewLimiter(rate.Inf, 1)` for instant runs
best-effort-retention-prelude: synchronous trim before periodic background work → log via existing channel, never return error, no new goroutine
ad-hoc-cli-split: extraction library + thin `cmd/main.go` flag wrapper → keeps pure logic testable while satisfying "extract.go or equivalent" CLI requirement.
swallowed-handler-error: conn-handler dispatches to sub-handler returning error → assign to `_` → log-and-drop becomes silent-drop, lose observability on write failures.
