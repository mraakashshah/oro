# Factory resumption verification

Date: 2026-10-26. Scoped implementation and verification, not autonomous-factory
qualification. Code commits: `3d63aeb4` and `c2f8e932` on `feature/factory-core`.

## Behavioral repair

A new regression first failed with three specific observations:

- Initial discovery accepted an inherited label whose raw value was JSON null.
- Bound stopped inspection accepted raw null in place of a pinned empty string.
- Bound running inspection accepted the same substitution.

The decoder now rejects null entries in string maps before Go's JSON decoder can
normalize them. Other non-string values fail typed decoding; valid empty strings
remain valid. This aligns keeper inspection with cleanup's existing strict map
parser. Independent Astra review found no further parser issues. The review did
identify the separate durable keeper-to-cleanup permit authority gap captured by
`oro-mf-keeper-cleanup-binding`.

## Commands and observed results

All commands below ran through the existing protected `verify_autonomy.py`
harness using Go 1.26.10, temporary Oro state/caches, and preserved HOME/CODEX_HOME.
The harness lives in `/private/tmp/oro-factory-verify-ldhnhnvl`; its location is
historical evidence, not a permanent installation prerequisite.

| Check | Result |
| --- | --- |
| New raw-value regression before implementation | FAIL as expected, three behavioral failures; 4.359 s harness |
| Focused inherited-label race after initial fix | PASS; 6.110 s package / 10.621 s harness |
| Combined source/keeper/volume/cleanup shuffled race and coverage | PASS; 96.359 s package / 100.906 s harness; 59.3% package coverage from selected tests |
| Focused inherited-label race after extracting scalar validation to satisfy complexity lint | PASS; 6.349 s package / 10.097 s harness |
| Package golangci-lint after refactor | PASS; zero issues / 8.106 s harness |
| Full factory shuffled race and coverage | PASS; 366.800 s package / 370.623 s harness; 89.1% statements |
| Exact archive Biome check and checksum verification | PASS; all ten copied artifact hashes remain unchanged |
| Repository gate after formatting repair | FAIL; 14 checks passed, 2 failed / 153.489 s harness |

The full-package command was:

```sh
GOTOOLCHAIN=go1.26.10 go test -race -shuffle=on -p 2 \
  -coverprofile=<private-evidence-dir>/resume-factory-full.out \
  ./pkg/factory -count=1 -timeout=600s
```

The combined command selected:

```text
TestManagedKeeperInheritedLabels
TestManagedSourceKeeperCreate
TestManagedSourcePrepare
TestManagedDockerVolumeCreate
TestManagedDockerVolumeIntent
TestManagedCleanupAuthority
TestManagedCleanupScopeIntegrity
```

The full-package profile reports `decodeManagedKeeperValue` at 89.5% and the new
`validateManagedKeeperStringMap` at 85.7%. No per-function 100% claim is made.
No native integration flags were enabled, so package PASS does not establish
Docker/launchd/real-model runtime qualification.

Code provenance: the full race command started at documentation HEAD `fad8f74e`
with the nine implementation/formatting files modified. Source content was frozen
before launch; those changes were committed as `3d63aeb4` and `c2f8e932` while the
check ran. Its result records final HEAD `c2f8e932` and a clean working tree.
No source edits occurred during that full test. Thus the initial HEAD alone is
not the tested source identity; the committed combined content is.

Local logs and result metadata use prefixes `resume-keeper-null-red`,
`resume-keeper-null-green`, `resume-keeper-combined`, `resume-keeper-refactor-green`,
`resume-keeper-lint2`, `resume-factory-full`, and `resume-full-gate2` under the
private evidence directory. This document preserves outcomes and reproduction
commands if temporary files disappear; the source tests remain committed.

## Repository gate findings

The initial gate failed gofumpt on six factory-related files and Biome formatting
on six preserved native JSON records. Commit `c2f8e932` fixes only Go formatting
and disables formatting for the exact raw native JSON archive. JSON checking and
the archive SHA256 manifest remain active. Subsequent gate lanes passed Go
formatting/imports, golangci-lint, architecture, beadstore imports, Markdown,
YAML, Biome, Python format/lint/type/tests, and the serial dispatcher lane.

NilAway still reports these locations and data flows:

| File | Diagnostic |
| --- | --- |
| `pkg/factory/managed_docker_keeper_inspect.go:94` | `raw[0]` after a second JSON decode |
| `pkg/factory/source_git.go:96,113` | Indexing variadic `args[0]` on error paths |
| `pkg/factory/source_git_objects.go:142,149` | Potential nil directory child receiver in encode |
| `pkg/factory/source_git_transport.go:79,120` | Potential nil sizes map returned from listing decoder |
| `cmd/oro/cmd_factory_launchd_runtime_darwin.go:70,76` | Potential nil prepared result before request access |

Dead-export checks report:

- `AdmitManaged` in `managed_admission.go`.
- `ReadManagedAdmission` in `managed_admission_view.go`.
- `DecodeDockerCleanupResult` in `docker_cleanup_result.go`.

These APIs still lack production consumers. Actual managed startup and recovery
must consume them; dummy calls or check suppression would not prove integration.
The static-tier failures prevented the gate's broad Go runtime/security/build
tiers from running. The independent factory race run above is additional scoped
evidence, not a substitute for a green repository gate or full Oro acceptance.
