# Review Pattern Candidate Inbox Design

## Status

Draft design for replacing automatic writes to tracked
`assets/review-patterns.md` with a runtime candidate inbox plus explicit
promotion.

## Problem

Oro review agents can emit `PATTERN: tag: trigger -> fix` lines when an ops
review is approved. Today the dispatcher captures those lines and appends them
directly to `assets/review-patterns.md`.

That preserves useful review knowledge, but it mutates a tracked source file
during normal dispatcher operation. A routine review can leave the user's working
tree dirty even when no human intentionally edited repo files. It also mixes raw
model output with curated project knowledge.

## Current Behavior

Relevant code:

- `pkg/ops/review_prompt.go` reads known patterns through
  `ReviewOpts.ReviewPatterns`, falling back to
  `<ProjectRoot>/assets/review-patterns.md`.
- `pkg/ops/review_prompt.go` asks approved reviewers to emit optional
  `PATTERN:` lines.
- `pkg/ops/review_prompt.go:ExtractPatterns` parses non-empty `PATTERN:` lines.
- `pkg/dispatcher/dispatcher.go:handleReviewResult` extracts patterns from
  approved review feedback.
- `pkg/dispatcher/dispatcher.go:appendReviewPatterns` hardcodes appends to
  `<repoRoot>/assets/review-patterns.md`.
- `cmd/oro/paths.go` already models `ProjectPaths.ReviewPatterns` as
  `assets/review-patterns.md` in standard mode and
  `~/.oro/projects/s-<hash>/review-patterns.md` in stealth mode, but dispatcher
  capture does not use that path.

Two path inconsistencies matter:

1. Review prompt reads are configurable through `ReviewOpts.ReviewPatterns`, but
   dispatcher writes are hardcoded to repo-root assets.
2. Dispatcher reviews pass `ProjectRoot: worktree`, so reviewers read patterns
   from the worker worktree checkout, while appends go to the dispatcher repo
   root.

## Goals

- Keep `assets/review-patterns.md` as the curated, committed pattern library.
- Stop mutating tracked files during normal dispatcher and `oro work` review
  flow.
- Preserve a clean git working tree after normal pattern capture.
- Preserve review learning by capturing raw candidates somewhere durable.
- Make promotion to curated patterns explicit and reviewable.
- Support standard mode and stealth mode without special cases.
- Keep prompt behavior deterministic enough for tests and reproducible reviews.

## Non-Goals

- Do not build a full pattern taxonomy or scoring system.
- Do not require a database migration if a file-based inbox is sufficient.
- Do not auto-commit pattern promotions.
- Do not delete or rewrite existing curated patterns.
- Do not make reviews depend on network services or external stores.

## Proposed Design

Introduce two distinct concepts:

- **Curated patterns:** committed project knowledge used by review prompts.
  Default path remains `assets/review-patterns.md` in standard mode.
- **Candidate patterns:** runtime-captured reviewer suggestions stored outside
  tracked source files until a human promotes them.

Add a project-local candidate inbox path:

```go
type ProjectPaths struct {
    ReviewPatterns          string // curated pattern library
    ReviewPatternCandidates string // runtime candidate inbox
}
```

Path defaults:

- Standard mode:
  - Curated: `<repoRoot>/assets/review-patterns.md`
  - Candidates: `<repoRoot>/.oro/review-pattern-candidates.md`
- Stealth mode:
  - Curated: `<stealthDir>/review-patterns.md`
  - Candidates: `<stealthDir>/review-pattern-candidates.md`

The standard-mode candidate path intentionally lives under `.oro/`, not
`assets/`, because candidate capture is runtime state. If `.oro/` is ignored or
project-local, this preserves a clean git worktree during normal operation.
In this repo `.oro/` is not fully ignored because `.oro/config.yaml` is tracked,
so the implementation must explicitly ignore the candidate inbox files:

```gitignore
/.oro/review-pattern-candidates.md
/.oro/review-pattern-candidates.promoted.md
```

The same ignore entries must be added anywhere `oro init` configures gitignore
behavior for target projects. The clean-worktree goal is not satisfied until
standard-mode capture leaves `git status --porcelain` empty.

## Runtime Flow

### Review Prompt Input

Review prompts should read curated patterns from `ReviewOpts.ReviewPatterns`.

Candidate patterns are optional prompt input. The first implementation should
not include the candidate inbox in reviewer prompts by default. Raw candidates
are not curated yet; feeding them back immediately risks reinforcing noisy or
duplicative model output.

A later flag can opt into recent local candidates, but that is out of scope for
the first change.

### Pattern Capture

Replace `appendReviewPatterns` with candidate capture:

```go
func (d *Dispatcher) appendReviewPatternCandidates(
    ctx context.Context,
    beadID string,
    workerID string,
    candidates []string,
) error
```

The function writes to `d.reviewPatternCandidatesPath`, not
`repoRoot/assets/review-patterns.md`.

Implementation must update both the helper and the call site:

- `pkg/dispatcher/dispatcher.go:handleReviewResult` currently calls
  `d.appendReviewPatterns` after `ops.ExtractPatterns`.
- `pkg/dispatcher/dispatcher.go:appendReviewPatterns` currently hardcodes
  `<repoRoot>/assets/review-patterns.md`.

Both must be replaced in the same task. It is not enough to add the new helper
if the old call remains wired.

Each record should include enough provenance to audit later:

```markdown
<!-- oro-review-pattern-candidate
bead: oro-123
worker: worker-2
captured_at: 2026-05-05T12:34:56Z
-->
- tag: trigger -> fix
```

Use append-only file writes for the first version. The inbox is human-readable,
diffable, and easy to promote from. File creation errors remain non-blocking:
log `append_review_pattern_candidates_failed` and allow the approved review to
proceed.

To avoid interleaved records from parallel approved reviews, build the complete
record in memory and write it with one `f.WriteString(record)` call after opening
the file with `O_APPEND|O_CREATE|O_WRONLY`.

The non-blocking guarantee must be tested at the message boundary: even when
candidate capture fails, the worker still receives the approved
`MsgReviewResult`, and rejection counters are cleared as in the successful path.

### Promotion

Add an explicit promotion path, initially minimal:

```bash
oro review-patterns candidates
oro review-patterns promote
```

`candidates` prints the candidate inbox path and current candidate records.

`promote` should:

1. Read candidates from `ReviewPatternCandidates`.
2. Normalize each candidate by trimming whitespace and removing duplicate
   bullets/provenance.
3. Compare normalized candidates against existing curated
   `ReviewPatterns`.
4. Append only new candidates to the curated file.
5. Preserve the inbox by default, but mark promoted records or move them to
   `review-pattern-candidates.promoted.md`.

The first version can require `--all` to promote every candidate. Selective
interactive promotion can come later.

Both commands must resolve paths through `ResolvePaths(repoRoot)`. They must not
hardcode `.oro/review-pattern-candidates.md` or `assets/review-patterns.md`,
because stealth mode stores both files under the stealth project directory.

The new command group must be registered on the production root command in
`cmd/oro/root.go` through `newRootCmd()`. Unit tests against a standalone Cobra
command are not sufficient; at least one smoke test must execute through
`newRootCmd()` so `oro review-patterns candidates` cannot ship as an unregistered
command.

### `oro work`

The standalone `oro work` review loop does not currently append patterns. Keep
capture out of `oro work` for the first change unless resolved project paths are
already available at the capture point.

However, `oro work` should still pass the resolved `ReviewPatterns` path into
`ops.ReviewOpts`. Otherwise stealth-mode `oro work` reviewers miss the curated
pattern library because they read from the worktree instead of the stealth
project directory.

The invariant is: `oro work` must not write to tracked
`assets/review-patterns.md` during normal operation, and its prompt read path
should match the resolved curated pattern path.

## API and Config Changes

### Dispatcher Config

Add:

```go
ReviewPatterns          string
ReviewPatternCandidates string
```

`ReviewPatterns` is passed to `ops.ReviewOpts` for reads. If empty, the
reviewer falls back to worktree assets as it does today.

`ReviewPatternCandidates` controls capture. If empty, `New()` derives a
backward-compatible runtime path from `RepoRoot`:
`<RepoRoot>/.oro/review-pattern-candidates.md`.

Production wiring must be tested at the command layer, not only by injecting
dispatcher config in unit tests. `cmd/oro/cmd_start.go` must copy
`projectPaths.ReviewPatterns` and `projectPaths.ReviewPatternCandidates` into
`dispatcher.Config`, and a test must fail if either field is omitted.

### Dispatcher State

Add fields:

```go
reviewPatternsPath          string
reviewPatternCandidatesPath string
```

`handleReviewResult` calls the candidate append function when approved review
feedback includes patterns.

Existing `append_review_patterns_failed` observability consumers must be audited
before the event is renamed. Either update all references to
`append_review_pattern_candidates_failed` or preserve the old event name as a
compatibility alias for one release.

### Start Command

`cmd/oro/cmd_start.go` should pass:

```go
ReviewPatterns:          projectPaths.ReviewPatterns,
ReviewPatternCandidates: projectPaths.ReviewPatternCandidates,
```

The start-command test should cover the real config construction helper used by
production startup, not a standalone struct literal.

### Path Resolution

`cmd/oro/paths.go` should resolve candidate paths for both standard and stealth
mode. Existing path tests should assert both paths.

## Tests

Add focused tests rather than broad end-to-end coverage.

Top-level acceptance command for the implementation:

```bash
go test -run 'TestApprovedReview_WritesCandidateInboxNotAssets|TestPaths_ReviewPatternCandidates_StandardAndStealth|TestReviewPatternsPromote_DedupAppend|TestReviewPatternsRootCommandRegistered|TestReviewPatternCandidateCaptureLeavesGitStatusClean|TestOroWorkPassesReviewPatternsToReviewOpts|TestStartWiresReviewPatternPaths|TestReviewPatternsPromote_CreatesMissingCuratedFile|TestReviewPatternsPromote_LeavesGitStatusClean' ./pkg/dispatcher/... ./cmd/oro/...
```

Expected result: exit 0. The dispatcher test must prove
`assets/review-patterns.md` is unchanged; the clean-status test must prove
standard-mode capture leaves `git status --porcelain` empty.

### `cmd/oro/paths_test.go`

- Standard mode resolves:
  - `ReviewPatterns == <repo>/assets/review-patterns.md`
  - `ReviewPatternCandidates == <repo>/.oro/review-pattern-candidates.md`
- Stealth mode resolves:
  - `ReviewPatterns == <stealth>/review-patterns.md`
  - `ReviewPatternCandidates == <stealth>/review-pattern-candidates.md`

### `pkg/dispatcher/dispatcher_test.go`

- Approved review with `PATTERN:` writes to candidate inbox, not
  `assets/review-patterns.md`.
- That test must exercise the actual `handleReviewResult` call site, not only
  the candidate append helper.
- The curated `assets/review-patterns.md` file's content and mtime remain
  unchanged after an approved review with a `PATTERN:` line.
- The candidate inbox record contains the provenance fields from the spec
  template: `bead:`, `worker:`, and `captured_at:`, plus the candidate text.
- Candidate append failure logs
  `append_review_pattern_candidates_failed` and does not block review approval.
- The failure-path test asserts the worker still receives an approved
  `MsgReviewResult` and rejection counters are cleared.
- Dispatcher passes `cfg.ReviewPatterns` into `ops.ReviewOpts.ReviewPatterns`
  so review reads and configured paths are aligned.
- Existing hardcoded repo-root append tests should be replaced, not preserved.

### `pkg/ops/review_prompt_test.go`

Existing custom `ReviewPatterns` coverage remains valid. Add no candidate inbox
prompt coverage in the first version because candidates are not prompt input.

### `cmd/oro` Review Wiring Tests

- `cmd_start.go` config construction passes `projectPaths.ReviewPatterns` and
  `projectPaths.ReviewPatternCandidates` into `dispatcher.Config`.
- `cmd_work.go:reviewLoop` passes the resolved `ReviewPatterns` path into
  `ops.ReviewOpts.ReviewPatterns`.
- The `cmd_work.go` test should cover stealth mode or a custom resolved
  `ReviewPatterns` path so fallback-to-worktree-assets would fail.

### CLI Tests

For the minimal promotion command:

- `oro review-patterns candidates` prints the resolved candidate path and
  candidate content.
- `oro review-patterns promote --all` appends deduped candidates to the curated
  path.
- Promotion does not duplicate a candidate already present in curated patterns.
- Both commands resolve paths through `ResolvePaths(repoRoot)`.
- A stealth-mode CLI test writes a candidate under `<stealthDir>`, runs
  `promote --all`, and asserts `<stealthDir>/review-patterns.md` is updated.
- A root-command smoke test executes through `newRootCmd()` with args
  `review-patterns candidates`, proving the command is registered.
- If the curated pattern file is absent, `promote --all` creates it.
- Promotion either keeps promoted records inline in the ignored candidate inbox
  or writes `review-pattern-candidates.promoted.md`; whichever behavior ships
  must leave `git status --porcelain` empty afterward.

### Gitignore Tests

- Standard-mode candidate capture leaves `git status --porcelain` empty after
  capture.
- `oro init` already adds `.oro/` to the user's global gitignore through
  `oroGitignoreEntries()`, which covers candidate inbox files in target
  projects. Add or update a test documenting that this remains true.
- The oro repo itself has a tracked `.oro/config.yaml`, so repo-local
  `.gitignore` must explicitly ignore the candidate inbox and any promoted
  sibling files.

## Migration

No automatic migration is required.

Existing uncommitted additions to `assets/review-patterns.md` are already in the
tracked file and should remain a human decision: keep, edit, or revert before
commit. After this change ships, new runtime captures go to the candidate inbox.

If a user already has a stealth-mode `review-patterns.md`, it remains the
curated file. New candidates go beside it as `review-pattern-candidates.md`.

## Rollback

Rollback is straightforward:

- Revert the code change.
- Candidate inbox files remain local runtime state.
- Curated `assets/review-patterns.md` remains unchanged unless the user ran
  promotion.

## Premortem

```yaml
premortem:
  mode: deep
  context: "Move review pattern capture from tracked source file to runtime candidate inbox"

  tigers:
    - risk: "Candidates are captured but never promoted, so the learning loop silently weakens."
      severity: medium
      mitigation_checked: "No existing promotion command or reminder exists."
      mitigation: "Add explicit list/promote command and log candidate capture path in event detail."
    - risk: "Dispatcher continues reading patterns from worker worktree while capture writes elsewhere, preserving the read/write mismatch."
      severity: medium
      mitigation_checked: "Current ReviewOpts call passes ProjectRoot: worktree and no ReviewPatterns."
      mitigation: "Pass cfg.ReviewPatterns into ReviewOpts so curated pattern reads use the resolved canonical path."
    - risk: "Candidate capture moves from tracked assets to .oro but still dirties git status as an untracked file."
      severity: high
      mitigation_checked: "Current .gitignore ignores /.oro/context_pct only; .oro/config.yaml is tracked."
      mitigation: "Explicitly ignore review-pattern candidate inbox files and test git status after capture."
    - risk: "Promotion command exists in tests but is not registered on the production root command."
      severity: high
      mitigation_checked: "Root command registration is centralized in cmd/oro/root.go:newRootCmd."
      mitigation: "Task 4 must register newReviewPatternsCmd() in newRootCmd and include a root-command smoke test."

  elephants:
    - risk: "Some users may expect successful reviews to directly update the committed pattern library."
      mitigation: "Treat direct mutation as a separate explicit promotion step; document the changed behavior."

  paper_tigers:
    - risk: "A file inbox is too primitive."
      reason: "The volume is small, records are human-readable, and avoiding a DB migration keeps the first change low risk."
    - risk: "Not feeding candidates back immediately reduces reviewer quality."
      reason: "Raw model output should be curated before becoming prompt input; committed patterns remain available."
```

## Task Breakdown

1. **Path model**
   - Add `ReviewPatternCandidates` to `ProjectPaths`.
   - Resolve standard and stealth candidate paths.
   - Add candidate inbox files to repo ignore behavior:
     `/.oro/review-pattern-candidates.md` and
     `/.oro/review-pattern-candidates.promoted.md`.
   - Document and test that `oro init` target-project behavior is already
     covered by the existing global `.oro/` ignore entry from
     `oroGitignoreEntries()`.
   - Update path tests.
   - Add a clean-status test proving standard-mode capture does not create a
     visible untracked file.

2. **Dispatcher config and prompt alignment**
   - Add `ReviewPatterns` and `ReviewPatternCandidates` to
     `dispatcher.Config`.
   - Store resolved paths in `Dispatcher`.
   - Pass `ReviewPatterns` into `ops.ReviewOpts`.
   - Update dispatcher construction in `cmd/oro/cmd_start.go`.
   - Pass resolved `ReviewPatterns` into `ops.ReviewOpts` from
     `cmd/oro/cmd_work.go` as well.
   - Add tests for both production wiring points:
     `cmd_start.go` dispatcher config construction and
     `cmd_work.go:reviewLoop`.

3. **Candidate capture**
   - Replace the `handleReviewResult` call site that invokes
     `appendReviewPatterns`.
   - Replace hardcoded `appendReviewPatterns` with candidate inbox append and
     delete or rename the old helper.
   - Include provenance metadata.
   - Write each candidate record with a single append write.
   - Log candidate append failures under the new event name.
   - Grep for `append_review_patterns_failed` and update consumers or preserve a
     compatibility alias.
   - Update dispatcher tests to prove tracked assets are not touched.
   - Update dispatcher tests to assert candidate provenance fields are present.
   - Update failure-path tests to assert approval still reaches the worker.

4. **Promotion CLI**
   - Add `oro review-patterns candidates`.
   - Add `oro review-patterns promote --all`.
   - Resolve paths through `ResolvePaths(repoRoot)` for both commands.
   - Deduplicate against existing curated patterns.
   - Register `newReviewPatternsCmd()` in `cmd/oro/root.go:newRootCmd`.
   - Add CLI tests for list, promote, and dedupe.
   - Add a stealth-mode promote test and a root-command registration smoke test.
   - Add a missing-curated-file test.
   - Add a post-promotion clean-status test covering the promoted-record
     handling strategy.

5. **Documentation**
   - Update `docs/plans/done/2026-02-13-review-gate-design.md` or add a new
     note that the original direct append behavior has been superseded.
   - Document candidate inbox and promotion behavior in `docs/README.md` only if
     the inbox path is considered operator-visible.

## Acceptance Criteria

- Running an approved review that emits `PATTERN:` does not modify
  `assets/review-patterns.md`.
- The emitted pattern appears in the resolved candidate inbox with bead, worker,
  and timestamp provenance.
- Standard-mode capture leaves `git status --porcelain` empty.
- Review prompts still include curated patterns from the resolved
  `ReviewPatterns` path.
- `oro work` review prompts also receive the resolved `ReviewPatterns` path.
- `oro start` wires both resolved review-pattern paths into dispatcher config.
- Stealth mode writes candidates only under the stealth project directory.
- `oro review-patterns promote --all` appends deduped candidate patterns to the
  curated file.
- `oro review-patterns promote --all` creates the curated file if it is missing
  and leaves git status clean after promotion bookkeeping.
- `oro review-patterns candidates` works through the production root command,
  not only a standalone command constructor.
- Existing review approval flow remains non-blocking if candidate capture fails.
