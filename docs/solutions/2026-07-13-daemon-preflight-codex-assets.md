# SQLite Daemon Bypass Omits Codex Project Assets

**Date:** 2026-07-13
**Component:** `cmd/oro` startup preflight
**Severity:** high

## Symptom

SQLite daemon-only startup succeeded without a Go toolchain, but Codex started
without the Oro project instructions and portable discipline assets. The
regression test failed with:

```text
expected .../project/AGENTS.md to exist: stat .../project/AGENTS.md: no such file or directory
```

## Investigation

The original no-Go fix made `startPreflightAndCheckRunning` pass
`runRepoChecks=false` for the SQLite daemon bypass. In
`preflightAndCheckRunningWith`, that flag guarded the entire call to
`ensureRuntimeProjectAssets`, not only the repository-dependent work. This
avoided building `oro-search-hook`, but it also skipped AGENTS.md extraction,
Codex skill links, Codex rules, and Codex hook configuration.

## Root Cause

One boolean represented two different capabilities:

- whether Oro source-tree checks and a Go-built search hook can run;
- whether portable runtime assets should be installed for the user project.

The daemon bypass lacks the first capability, but it still requires the second.
Gating both operations together made the no-Go fix deterministically remove the
Codex startup contract.

## Solution

`ensureRuntimeProjectAssets` retains its existing signature and full behavior.
It now delegates to a helper whose `installSearchHook` parameter gates only the
`ensureSearchHook` call. `preflightAndCheckRunningWith` always installs runtime
project assets and passes `runRepoChecks` only as the hook-build decision.

`TestStartPreflightAndCheckRunning_DaemonOnlyBypass` supplies `claude` and `git`
but no `go` on `PATH`, selects the Codex runtime, and asserts that AGENTS.md,
Codex rules, and the portable `using-skills` link are installed. The full Codex
discipline parity test verifies that the resulting hook configuration remains
wired end to end.

## Prevention

When bypassing environment-dependent preflight work, gate the smallest operation
that actually needs the missing dependency. Regression tests must assert both
sides of a bypass contract: the unavailable operation is not attempted, and the
portable setup operations still occur.

## Related

- Task `oro-zvx6`
- Regression source: task `oro-6y6p`, commit `0d5ec281`
- Fix: `cmd/oro/cmd_start.go` and `cmd/oro/start_test.go`
