# Memory Retirement Readiness Scan

**Date:** 2026-05-24
**Component:** `cmd/oro`
**Severity:** low

## Symptom

The `oro cards memory-retirement-check` gate needed to prove legacy
`pkg/memory` retirement readiness from two independent signals:

- no `memory_read_events` rows inside the 14-day retirement window
- no production `oro/pkg/memory` imports outside the explicit allowlist

During implementation, two local checks failed:

```text
malformed Go import file must return scan error
```

```text
hardcoded path literal - use ProjectPaths instead:
    if name == "vendor" || name == ".git" || name == ".worktrees" {
```

## Investigation

The first failure came from scanning Go imports with `parser.ImportsOnly`.
That mode is too narrow for this gate: it can successfully parse imports while
ignoring malformed function bodies later in the file.

The second failure came from `cmd/oro/paths_test.go`, which rejects hardcoded
`.worktrees` path literals in production command files.

## Root Cause

Retirement scanning is both an import check and a readiness gate. It must fail
closed on malformed production files, so import-only parsing is insufficient.
It also lives under `cmd/oro`, where path-sensitive literals must use existing
path constants instead of duplicating project layout strings.

## Solution

`cmd/oro/cmd_cards_retirement.go` uses full-file parsing through
`parser.ParseFile(..., 0)` and wraps parser errors as scan errors. The scanner
skips worktrees through the existing `worktreesDirName` constant, while keeping
the allowlist exact:

- `cmd/oro/cmd_cards_check_drift.go`
- `pkg/cards/legacy_writer.go`

## Prevention

When a source scanner is also a release gate, parse the full file unless the
acceptance criteria explicitly allow malformed files. For `cmd/oro` path skips,
reuse existing project path constants before adding string literals.
