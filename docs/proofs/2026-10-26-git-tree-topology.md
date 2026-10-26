# Git directory topology verification

Date: 2026-10-26. Terra source commit `717261ab14ec521b26c311260f63ac0d3b397ce4`,
base `47827496`; integrated as `e16b7079` after documentation-only target commits.

The private encoder now returns errors for nil directory nodes or a nil output
receiver. Child errors propagate with an empty OID, and `buildSourceTree` returns
the error before its callers can accept the result or invoke host writes. Valid
sorting, hashing, links, byte bounds, and raw SHA-1/SHA-256 identities are unchanged.
Earlier siblings can leave partial local output on an error; no production caller
accepts that output when the error is non-nil.

A compiling RED regression reproduced a runtime nil-pointer panic through the
old encoder's nil child. Final tests cover nil root, nil child, deeper nil child,
and nil output, plus existing raw-Git round trips for valid trees. These malformed
internal states are defensive encoder cases, not evidence that a valid external
source vector can construct such a topology.

| Check | Result |
| --- | --- |
| Terra baseline existing raw-tree tests | PASS |
| Terra compiling panic regression before fix | FAIL as expected, runtime nil-pointer panic |
| Terra topology/raw-Git race suite | PASS, 19.206 s |
| Terra package golangci-lint / formatting | PASS, zero issues |
| Independent Astra review | No findings |
| Independent root topology/raw-Git race suite | PASS, 19.373 s |
| Package NilAway comparison | Only the directory topology finding removed; known unrelated findings remain |
| Root integrated Git command/topology/raw-transport race run | PASS, 26.509 s package / 30.693 s harness |
| Root exact repository NilAway lane | Three production findings remain: keeper raw array, listing sizes, CLI prepared pointer |

The targeted race command selects `TestSourceGitDirectoryTopology`,
`TestCandidateExportExactTree`, `TestCandidateCommitAvoidsFilters`, and
`TestCandidateInternalLinkExactTree`, with Go 1.26.10, count 1 and timeout 180 s.
The two implementation files are `source_git_objects.go` and new
`source_git_objects_test.go`; no public API or checker suppression was added.
The conventional test-package annotation only permits private-seam access.

Evidence is in `/tmp/oro-git-tree-topology-evidence-47827496/`:
`baseline-existing.log`, `red.log`, `green-final.log`, `lint.log`,
`nilaway-before.log`, `nilaway-after.log`, and `nilaway-diff.log`.
`baseline.log` captured a concurrent RED state and is not the positive baseline.
The coordinator read the panic and comparison logs and ran independent GREEN.

Integrated evidence uses `git-guards-integrated` and `git-guards-nilaway-gate`
under `/private/tmp/oro-factory-verify-ldhnhnvl`. The exact static command is
`nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/...`.
An earlier default `nilaway ./pkg/factory` run also reported test-file diagnostics;
that different reporting scope is not the repository gate policy. No tests or
checkers were weakened to obtain the gate-scope result. The three previously
reported dead exports remain an outstanding production-wiring requirement.

No full repository, native isolation, autonomous delivery, or recovery
qualification follows from this component result. The source and regression tests
are committed so the checks remain reproducible if temporary evidence disappears.
