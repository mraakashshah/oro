# Managed Git command boundary verification

Date: 2026-10-26. Terra implementation `478274969ec18ee95e366491ad1863139057d081`,
base `fd5df4b0`, integrated into `feature/factory-core` after independent review.

`source_git.go` now rejects an empty variadic command at both `invoke` and `run`
before accessing state, spending host authority, or invoking Git. The six-line
change preserves all nonempty execution/error behavior and uncertainty freezing.

Eight compiling behavioral RED cases in `TestManagedGitEmptyCommand` exposed:

- Runtime-error and nonzero-exit formatting paths indexing `args[0]` and panicking.
- Empty commands reaching the runtime and spending reservations/scopes/events.
- Lost valid follow-up authority through poisoned adapter state or exhausted H.

GREEN cases cover both methods, nil and empty vectors, forced runtime errors and
nonzero exits, no authority effects, and a valid subsequent operation using the
sole remaining host allowance and its original deadline.

| Check | Result |
| --- | --- |
| Terra command/authority/raw-Git race suite | PASS, 20.752 s |
| Terra package golangci-lint / gofumpt / whitespace | PASS, zero issues |
| Independent Astra committed-diff review | No findings |
| Independent root command/authority race suite | PASS, 7.893 s |
| Package NilAway before/after | Both source_git.go findings removed; three other factory groups remain |

The root command was Go 1.26.10 with race detection, selecting
`^TestManagedGitEmptyCommand$|^TestManagedGitAuthority$`, count 1 and timeout 180 s,
against the exact isolated implementation before fast-forward integration.
Terra additionally selected `TestCandidateExportExactTree` and
`TestCandidateCommitAvoidsFilters` for valid object/transport regression coverage.

Worker evidence is preserved in `/tmp/oro-git-command-guard-evidence/`:
`red.log`, `green.log`, `lint.log`, `nilaway-before.log`, and `nilaway-after.log`.
The coordinator read the RED log and reviewed the source diff, in addition to
the independent Astra review and root GREEN run. Tests remain committed if the
temporary evidence disappears.

The prior full factory coverage of 89.2% applies to `16f55188`, before this guard.
No full repository success or new full-package profile is claimed for this
increment. A combined gate run follows the independent remaining diagnostic
repairs. No native isolation, real-model factory run, or recovery trial occurred.
