# Full Oro checker runtime: bounded provisioning inventory

Date: 2026-10-24. Read-only research in
`/Users/as21/codehouse/oro/.worktrees/factory-core`; no source, native, network,
Git or tracker changes. This artifact preserves the full autonomous factory
objective. A small fixture, source transfer, or guard qualification does not
replace actual full-tree delivery and qualifying checks.

## Established image evidence

The current image qualifies Go and the watchdog; it does **not yet qualify the
full Oro check environment**. Existing evidence proves Linux/arm64 Go
**1.26.10**, the derived guard image, and the fixed shell/namespace probes.
The production image builder only adds the guard. Presence of the remaining
tools and dependency closure has not been demonstrated.

Sources:

- `docs/proofs/2026-10-23-unattended-preflight.md:34-57`: the pinned base and
  actual `go version go1.26.10 linux/arm64` preflight.
- `cmd/oro-factory-image/build.go:271-282`: the complete minimal Dockerfile and
  two-file context. This builder does not install a check toolchain or packages.
- `/private/tmp/oro-factory-verify-ldhnhnvl/guard-image-native-73164a1597a3e2e24c8119d060a6ddbf/call-08.json`
  and `call-09.json`: base/derived image inspection. Both declare
  `GOLANG_VERSION=1.26.10`, `GOTOOLCHAIN=local`, `GOPATH=/go`, and Go paths;
  environment declarations alone are not additional tool inventory evidence.
- The same fixture's `native-receipt.json`: actual produced-image namespace
  and UID1000 source ownership proof. It is not a full-repository gate proof.

## Concrete supply requirements

| Required supply | Repository evidence |
|---|---|
| Go 1.26.10; Linux C toolchain for race/CGO/tree-sitter; complete module closure | `go.mod`; `scripts/quality_gate.sh:569,1345`; `pkg/codestruct/go_extractor.go:1` and `extractors_nocgo.go:7-11` |
| goimports, gofumpt, govulncheck, shfmt; mutation tool when explicitly requested | Pinned Go `tool` block in `go.mod:80-86`. Shell lane additionally requires **shfmt on PATH** at `scripts/quality_gate.sh:715` |
| golangci-lint **2.10.1**, NilAway **ad240b12fb4c**, go-arch-lint **1.14.0** | Makefile pins the first two; `.github/workflows/ci.yml:142` pins architecture checker |
| Node **>=20**, Biome **2.4.4**, markdownlint-cli2 **0.21.0**, Linux/arm64 optional binary | `package.json:4-7` and `package-lock.json` engine/platform records; 93 locked package entries |
| Python **>=3.13**, PyYAML, pytest, pylint; ruff, pyright, yamllint; cosmic-ray when mutation requested | `pyproject.toml:1-12,41-45`; `scripts/quality_gate.sh:1517-1614`; `.github/workflows/ci.yml:297-327`. CI pins ruff **0.16.0** |
| Bash >=4, Make, Git, shell/core utilities, `bc`, `ps`/`pgrep`; ripgrep, tmux, ast-grep for applicable tests | Gate bootstrap, process/lock helpers and coverage comparison (`quality_gate.sh:29,212,234,645`); CI installation at `ci.yml:101,147`; actual subprocess tests |
| Frozen vulnerability database | `quality_gate.sh:1392` invokes govulncheck with no DB option. Pinned `golang.org/x/vuln@v1.1.4/internal/scan/flags.go:41` defaults to HTTPS; `internal/client/client.go:42-56` supports explicit `-db file:///...`, not a `GOVULNDB` override |

The Go manifest currently has 69 require lines and 231 go.sum lines. These are
manifest observations, not proof of a complete offline module or external-tool
closure. External linters introduce their own build/runtime supply requirements.

Python is not reproducibly provisioned yet: `uv.lock` is ignored
(`.gitignore:76`) and absent from the committed tree. Ruff, pyright and yamllint
are absent from `pyproject.toml`. The local Darwin virtualenv/cache is not a Linux
supply artifact; the project specifically requires Linux `greenlet==3.5.4`.
The untracked local uv.lock is not an admitted dependency policy.

`make setup` is not an offline execution path: it runs npm install, a curl-fed
golangci installer, go install, uv sync and Git-hook installation
(`Makefile:341-369`). Candidate namespaces cannot use this as a provisioning
step or inherit credentials/network access to make it work.

## Concrete layout and checker-policy gaps

1. **No `.git`:** the gate enumerates shell/Python/YAML through `git ls-files`
   (`quality_gate.sh:677-705`). Actual dispatcher tests also clone the source
   repository (`pkg/dispatcher/quality_gate_concurrency_test.go:76-92`).
   A trusted file manifest fixes enumeration, but those tests still need reviewed
   synthetic metadata or fixture adaptation—not the original repository's
   `.git`. The gate unsets `GIT_DIR`/`GIT_WORK_TREE`; an ambient override is not a
   solution. Mutation mode additionally requires its explicit base ref.
2. **Read-only source:** `ensure_stage_assets` runs `make stage-assets`, writing
   `cmd/oro/_assets` (`quality_gate.sh:1140-1179`, `cmd/oro/embed.go:9` and
   Makefile stage-assets). The gate also creates lock/queue files at repository
   root. Generated assets and gate state need a separately bounded layout while
   candidate source remains read-only. No currently implemented overlay/layout
   has been established by this research.
3. **Incomplete success:** absent pyright/go-arch-lint/ast-grep can skip checks
   (`quality_gate.sh:1007-1022,1323,1550`; `pkg/codesearch/astgrep_test.go:13-17`).
   Parallel status77 is counted as pass (`quality_gate.sh:1044-1053`). The gate
   excludes command-package tests (`quality_gate.sh:1351-1366`); CI runs them
   separately (`ci.yml:151-161`). Qualification must freeze required lanes and
   reject missing tools, rather than accept only aggregate exit status.
4. **Acceptance authority:** `Check` freezes literal argv/stdin/expected
   stdout/exit (`pkg/factory/managed_contract.go:87-94`), but has no dependency or
   environment fields. Candidate-controlled gate scripts, tests and lint
   configuration cannot be the sole acceptance oracle. Frozen checker inputs
   must live outside candidate source. The implementation repository's trusted
   host quality gate remains distinct from candidate acceptance.

The existing design requires no `.git`, no host mounts, network none, candidate
UID1000, read-only checker source and independent bounded scratch
(`docs/plans/2026-10-23-unattended-factory-design.md:542-626`). Do not fix these
gaps by copying host caches/credentials, copying the original `.git`, making the
checker source writable, or replacing full-tree checks with small fixtures.

Tests also have explicit environment-dependent behavior. For example,
`tests/conftest.py:18-39` skips three hook-dependent files if private Oro hooks
are absent; CI explicitly omits those same files (`ci.yml:327-329`). Required
test policy must record such boundaries rather than silently inherit them.
Mutation testing is off by default and opt-in (`quality_gate.sh:791-808`);
the research does not silently add it to or remove it from required acceptance.

## Smallest recommended trusted offline supply path

Produce a **trusted immutable Linux check-runtime image artifact before
admission**, from an explicit hashed allowlist: tools, complete offline
Go/npm/Python dependencies, vulnerability DB and a frozen checker entrypoint.
Keep the existing minimal guard builder's contract intact; this requires a
separate provisioning step. The final image ID already fits
`RuntimePolicy.ImageDigest`, avoiding host cache mounts or an additional mutable
dependency volume. This is trusted provisioning, never a candidate image build.

The checker entrypoint should use explicit tool paths, private quota-bounded
scratch/home/caches, disabled downloads, the frozen source-file manifest, and
fixed required checks. It must invoke the local vulnerability DB explicitly.
Frozen configuration/test-policy inputs must be separate from candidate edits;
the actual candidate tree remains the program being tested. A reviewed
generated-assets/synthetic-metadata layout remains a prerequisite. The current
gate cannot be claimed to run unchanged.

## Explicitly unmeasured and unresolved

- Linux/arm64 payload bytes, expanded bytes and inode count, including compiled
  external tools, native libraries, npm optional packages and Python wheels.
- Image/daemon retained bytes, any host staging copies, and corresponding
  capacity/accounting changes. Existing source transport limits are not evidence
  that a dependency bundle fits those limits.
- Writable build/test/cache/generated-assets size and inode demand under cold
  caches; read-only dependency access and denied-download behavior.
- Original finite step/objective deadlines and CPU/memory/PID limits needed for
  the **actual full suite**. Existing tiny namespace/UID proof is not a runtime
  measurement for full Oro checks.
- Exact generated-assets and root-lock layout preserving read-only source.
- Synthetic metadata or narrowly adapted repository-dependent tests; no original
  `.git` is supplied or authorized by this recommendation.
- Complete frozen Python/tool version lock and vulnerability DB snapshot identity
  and freshness policy.
- Full-tree qualification with required tools present, mandatory lanes observed,
  no undeclared skips, no network/host mounts/credentials, bounded cleanup, and
  the frozen independent acceptance oracle.

These are next-step prerequisites for actual autonomous full-repository
delivery, not completed provisioning or an authorization to implement/run it.
