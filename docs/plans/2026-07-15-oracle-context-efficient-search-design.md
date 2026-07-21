# Context-Efficient Search for Oro Oracles

**Date:** 2026-07-15
**Status:** Design validated (R4 PASS) — bead coverage R1 repairs applied

## Goal

Give every Oro Oracle a deterministic, runtime-agnostic repository-search
path that finds relevant code without loading large source files into the
initial context.

An Oracle research assignment will receive:

1. a compact repository-snapshot search map, filtered against the assigned
   worktree before it is shown;
2. the existing `oro-search-hook` on full-file reads for both Codex and
   Claude routes; and
3. explicit instructions to use targeted range reads for exact evidence.

The result must preserve the Oracle's read-only boundary. Search failures
degrade to `Glob`/`Grep`/targeted reads; they never prevent research from
running. Hook-policy failures do fail closed because silently launching an
Oracle without the promised context guard would make behavior depend on
ambient user configuration.

## Problem

Oro has two complementary repository-navigation mechanisms, but the Oracle
path does not deliberately compose them:

- `pkg/codesearch` provides indexed retrieval that answers "where should I
  look?"
- `oro-search-hook` intercepts broad reads and returns a structural summary,
  answering "how can I inspect this file without consuming all of it?"

Current production behavior has four gaps:

1. `cmd/oro/cmd_work.go:920-923` explicitly skips code-index lookup for
   `research` beads.
2. `pkg/dispatcher/dispatcher.go:6476-6503` populates
   `AssignPayload.CodeSearchContext`, but
   `pkg/worker/worker.go:807-817` does not pass it to
   `OraclePromptParams`.
3. `pkg/worker/oracle_prompt.go:12-20` has no repository-search input or
   search discipline.
4. Hook coverage is runtime-dependent. Codex normally inherits the
   user-global hook block installed by `oro start`; Claude's read-only launch
   intentionally omits project `--settings` at
   `pkg/worker/worker.go:2098-2106`. Neither route has an Oracle-specific,
   tested hook contract.

This is drift from the original lifecycle decision in
`docs/plans/done/2026-02-07-oro-lifecycle.md:227-230`: all roles receive the
same three search layers—indexed retrieval, structural/literal navigation,
and context-saving reads.

## Prior Art and Existing Contracts

- `pkg/codesearch/bypass.go:31-61` already defines the desired read policy:
  allow small files, tests, non-code, configuration, and explicit ranges;
  summarize only broad reads of large code files.
- `cmd/oro-search-hook/main.go:65-174` supports Claude `Read` and a legacy
  Codex `str_replace_based_edit_tool/view` shape. Codex CLI 0.144.4 documents
  `Bash` as the current read surface; its hook result uses
  `hookSpecificOutput.permissionDecision`, so the legacy event is not a valid
  production contract.
- `pkg/dispatcher/dispatcher.go:6846-6859` deliberately uses FTS-only search
  for assignment context, avoiding a second model call and a mutable
  reranker subprocess.
- `cmd/oro/cmd_work.go:855-863` already has a five-second bounded search path,
  but currently uses the reranked `SearchInWorkdir` surface and excludes
  research.
- `pkg/agentruntime/codex/codex.go:91-122` and
  `pkg/worker/worker.go:2098-2106` already enforce read-only Oracle launch
  policies. Search integration must layer onto these boundaries rather than
  weaken them.
- `docs/plans/done/2026-02-07-code-search-spec.md:202-216` records the
  established trade-off: structural summaries provide most token savings,
  range reads are the escape hatch, and every role should share the stack.

## Decisions

| Decision | Choice |
|---|---|
| Runtime scope | Codex and Claude; routing changes must not change Oracle search behavior |
| Initial retrieval | FTS-only, top 8, five-second deadline |
| Query | Normalized title + description + acceptance criteria, capped before lookup |
| Prompt payload | Paths, symbol kinds/names, and line ranges only; no chunk bodies |
| Payload budget | 8 KiB hard cap with UTF-8-safe truncation marker |
| Broad reads | Existing `oro-search-hook` structural summary policy |
| Exact evidence | Explicit line/range read, which the hook already bypasses |
| Search failure | Best-effort empty map plus event/warning; Oracle continues with `Glob`/`Grep` |
| Hook setup failure | Fail Oracle launch with an actionable setup/install error |
| Hook activation | A `SessionStart` probe must succeed for every Oracle launch |
| Index readiness | Setup and launch preflight seed a fresh/empty index synchronously; later refresh remains asynchronous |
| Interactive semantic search | Out of scope for v1; Oracle iterates with `Glob`/`Grep` after the initial indexed map |

## Consultation Record

The user confirmed all six forcing decisions on 2026-07-15. The assumption
ledger is empty.

| Forcing question | Confirmed decision |
|---|---|
| Real problem | Oracle research must be effective and context-efficient regardless of Codex/Claude routing or ambient user configuration |
| Status quo | Accidental hook coverage plus broad reads is an unacceptable reliability gap, not merely a performance issue |
| Specific beneficiary | Dispatcher and standalone `research` beads; the failure is missed symbols or whole-file context consumption before repository evidence |
| Narrowest wedge | Compact initial search map plus reliable hook coverage; no new interactive semantic-search tool or search algorithm |
| Do nothing | Not acceptable because a routing change must not silently change research behavior |
| Future fit | Build a durable Oracle search contract with shared map generation and explicit runtime hook policy, not a prompt-only patch |

## Alternatives Considered

### A. Hook only

Enable `oro-search-hook` for Oracles and make no index changes.

This saves context after the Oracle has found a file, but does not help it
find the right file. Broad `Grep` exploration remains noisy and keyword
dependent. It addresses only half the problem.

### B. Inject existing worker `CodeSearchContext`

Pass the current formatted search chunks directly to `OraclePromptParams`.

This is the smallest code change, but current formatting embeds source
bodies and permits up to 128 KiB. Research starts with a large, possibly
stale context before the Oracle has decided which results matter. That
contradicts the stated context-efficiency goal.

### C. Compact map plus enforced summary reads — selected

Use the existing index to inject only navigation metadata, then ensure the
existing hook summarizes broad reads. This composes two proven mechanisms,
keeps the initial payload small, and lets the Oracle pull exact evidence only
when needed.

## Architecture

```text
research bead
  -> build bounded query (title + description + acceptance criteria)
  -> ensure an initially empty index has been seeded
  -> FTS5 top-8 lookup (5s, best effort)
  -> compact map: path:range + kind + symbol (<= 8 KiB)
  -> discard entries missing from the assigned worktree
  -> Oracle prompt
       -> Glob/Grep for iterative narrowing
       -> broad Claude Read or simple Codex Bash cat
            -> oro-search-hook
                 -> structural summary for large code
                 -> full content for bypassed files
       -> targeted range Read/sed/head/tail for exact cited evidence
```

### 1. Shared Oracle search-map formatter

Add a pure formatter in `pkg/codesearch` so dispatcher and standalone paths
cannot drift:

```go
const OracleSearchContextLimit = 8 * 1024

func BuildOracleQuery(title, description, acceptance string) string
func FormatOracleMap(chunks []ChunkRef, maxBytes int) string
func FilterOracleChunksForWorktree(worktree string, chunks []ChunkRef) []ChunkRef
```

`ChunkRef` is the minimum shared representation:

```go
type ChunkRef struct {
    FilePath           string
    Name, Kind         string
    StartLine, EndLine int
}
```

The rendered format is deliberately body-free:

```text
- pkg/worker/oracle_prompt.go:23-75 — function AssembleOraclePrompt
- cmd/oro/cmd_work.go:911-940 — function spawnAndWaitOutput
```

Ordering follows index rank. Duplicate file/range/symbol tuples are removed
without reordering. Invalid or empty paths are skipped. Line ranges are
normalized for display: positive start/end render as `start-end`; incomplete
ranges render the file path and symbol without inventing evidence lines.
Truncation occurs only at entry boundaries and appends
`[oracle search map truncated]`. The formatter never exceeds `maxBytes`: if
the marker or the first complete entry does not fit, it returns an empty map.

`BuildOracleQuery` has one exact normalization contract: trim each input,
collapse every Unicode whitespace run to one ASCII space, join non-empty
fields with one ASCII space, and retain at most 512 UTF-8 bytes without
splitting a rune. Three empty inputs produce an empty query and no lookup.
The 8 KiB map budget is measured in bytes after UTF-8 rendering.

`FilterOracleChunksForWorktree` is the single boundary used by dispatcher and
standalone callers before `FormatOracleMap`. It canonicalizes the worktree
root, accepts only non-absolute clean relative paths, joins and resolves each
existing candidate (including symlinks), and retains it only when
`filepath.Rel(canonicalRoot, canonicalCandidate)` is neither `..`, prefixed by
`../`, nor absolute, and requires `os.Stat` to report a regular file. It
preserves index rank. An empty/unresolvable root, deleted or non-regular
candidate, absolute path, sibling-worktree path, `..` traversal, or symlink
escape is dropped. Tests cover every one of those cases and pass adversarial
rows through both dispatcher and standalone prompt chains.

### 2. Search-map population

Both assignment modes use the same policy:

- create a five-second child context;
- run FTS-only lookup for the normalized Oracle query;
- request at most eight results;
- adapt results to `[]codesearch.ChunkRef`;
- format with the 8 KiB limit; and
- continue with an empty map on missing index, timeout, or lookup failure.

Before formatting, both callers invoke
`codesearch.FilterOracleChunksForWorktree` on the assigned worktree. The map
is still an index snapshot, not proof that a symbol or line range is current.
The prompt labels it as navigation-only and requires an exact worktree range
read before treating any repository detail as evidence.

The dispatcher already exposes `FTS5Search` through its `CodeIndex` interface.
Standalone `workCodeIndex` gains `FTS5Search`; its research path must not use
`SearchInWorkdir`, because that may spawn the configured model reranker.
Existing implementation-worker behavior remains unchanged.

The dispatcher logs `oracle_search_unavailable` with bead ID and the error.
Standalone `oro work` emits one warning through `logStep`. Neither path puts
the error text in the Oracle prompt. Tests cover timeout and a representative
storage failure (missing or locked database) and assert that assignment still
continues with an empty map.

The dispatcher wiring is explicit and test-covered end to end:

```text
buildAssignPayload (canonical Show detail) -> BuildOracleQuery
  -> CodeIndex.FTS5Search -> FormatOracleMap
  -> protocol.AssignPayload.CodeSearchContext
  -> worker.BuildAssignPrompt
  -> OraclePromptParams.SearchMap
```

The cross-package test passes a real `AssignPayload` into
`worker.BuildAssignPrompt`; it must fail if the final mapping is omitted.
Its incoming ready-bead fixture is deliberately sparse: only
`buildAssignPayload`/`beads.Show` supplies description and acceptance, proving
the production query does not accidentally use title alone.
Standalone adds `FTS5Search` to `workCodeIndex`, assigns
`SearchMap: params.CodeSearchContext` in `assembleLocalWorkPrompt`, and uses a
capturing fake to prove an FTS result appears in the actual spawned research
prompt without its source body. Ordinary non-research work retains
`SearchInWorkdir` and its existing reranker behavior.

### 2a. Index readiness

An index that opens successfully but contains no chunks is not ready. Add a
small `IsPopulated(ctx)` query and a shared bounded readiness helper:

```go
func EnsureCodeIndexReady(ctx context.Context, idx *codesearch.CodeIndex, root string) error
```

The helper checks population and, only when empty, runs the existing atomic
`Index.Build(ctx, root)`. Normal and stealth setup call it synchronously after
opening the project database. `oro start` performs the same bounded preflight
before the dispatcher accepts assignments; its existing background rebuild
continues to refresh a populated index. Standalone `oro work` performs a
bounded synchronous refresh from the repository root before creating the
worktree and spawning research. Launch preflights use a 30-second deadline;
failure is reported through the search-unavailable warning/event and research
continues with an empty map.

This is deliberately a project-snapshot index, not a per-worktree rebuild.
The missing-path filter and targeted evidence read protect against stale
navigation. A fresh-repository fixture containing one Go symbol must produce
a non-empty research map without any prior `oro start` run.

### 3. Oracle prompt contract

`worker.OraclePromptParams` gains `SearchMap string`.
`AssembleOraclePrompt` adds an always-present `Repository Search` section:

- when populated, it displays the compact map;
- when empty, it says the index was unavailable or had no matches and directs
  the Oracle to `Glob`/`Grep`;
- it labels populated results as a possibly stale project snapshot for
  navigation only;
- it explains that broad reads may return structural summaries; and
- it requires targeted range reads before citing repository evidence.

The prompt remains read-only and does not add implementation instructions.
Its section contract changes from eight to nine sections and the golden test
must pin that intentionally.

### 3a. Runtime project identity

Every Oracle launch resolves project identity through one command-layer
helper rather than ambient inheritance:

```go
type runtimeProjectEnv struct { OroHome, Project string }
func ensureRuntimeProjectEnv(repoRoot string) (runtimeProjectEnv, error)
```

The helper preserves explicit non-empty `ORO_HOME`/`ORO_PROJECT`; otherwise it
uses `resolveOroHome()` and `readProjectName(repoRoot)`. It requires an
initialized non-empty project, canonicalizes the home to an absolute path,
sets both process variables, and returns both values for explicit child-env
construction. Resolution failure is actionable and occurs before a daemon,
worker, or Oracle is spawned.

The following public paths invoke it with `currentRepoRoot()` before their
first child or runtime spawner is constructed:

- `startFreshSwarm` and `runDaemonOnly`;
- `runDispatcherStart` before `SpawnDaemon`;
- `runWorkerLaunch` before `SpawnWorker`;
- `runWorker` as the final independent guard for dispatcher-managed or
  directly invoked workers; and
- standalone `runWork` before `newProductionDeps` calls
  `workerSpawnerForRuntime`, with a second guard in `executeWork` for tests and
  other injected/direct callers.

`cleanEnvForDaemon`, `daemonChildEnv`, and `ExecWorkerSpawner` preserve the
two resolved values in explicit child environments. Consequently full start,
daemon-only start, dispatcher start, external worker launch, direct worker,
and standalone work select the same
`$ORO_HOME/projects/$ORO_PROJECT/oracle-settings.json`. A table-driven launch
surface test starts with both variables unset and uses capturing daemon,
worker, and runtime spawners to prove each surface carries the same resolved
values. Its production-constructor seam records the values visible when
`newProductionDeps` constructs `workerSpawnerForRuntime`, so a later
`executeWork` mutation cannot make the test pass. The real `runWorker` entry
point—not only `BuildAssignPrompt`—must be crossed before the managed-worker
case passes.

### 4. Runtime hook activation contract

Hook configuration is not sufficient evidence that hooks are active: Codex
may disable hooks or require trust, and either runtime may load the wrong
profile. Both Oracle profiles therefore include the same `SessionStart`
probe, implemented by `oro-search-hook` itself.

Before spawn, the worker creates a unique probe path and exports it as
`ORO_HOOK_PROBE`. When stdin has `hook_event_name: "SessionStart"`, the hook
atomically writes a marker to that exact path, exits 0, and emits no stdout;
both runtimes define that as successful continuation. All other event shapes
continue through the existing read-event dispatcher. After
`cmd.Start`, the spawner waits at most five seconds for the marker, removes
it, and then begins normal process handling. If the marker does not appear,
the spawner terminates the child and returns an actionable error explaining
that the Oracle hook is disabled, untrusted, or stale. Marker paths are
created in an Oro-owned private temporary directory and are cleaned on
success, failure, and cancellation.

The launch path uses one shared probe helper for Claude and Codex. Tests use a
fake runtime process that reads the generated configuration and actually
invokes the configured `SessionStart` command; an argv-only assertion or a
direct synthetic call to the hook does not satisfy this contract.

The probe never consumes the child wait result. Both spawners wrap the child
in a replayable wait-once process: one goroutine owns the underlying
`Process.Wait`, exposes a `Done` channel to the probe, and stores its result;
every later `Wait` by `monitorSubprocessExit` or standalone `oro work` returns
that same stored result. Tests cover marker success followed by normal wait,
early exit, timeout, and cancellation, and assert the OS process is waited
exactly once.

### 5. Claude hook profile

The ordinary project `settings.json` contains mutation-adjacent hooks and is
therefore inappropriate for a read-only Oracle even though Claude's tool list
already excludes `Write`, `Edit`, and `Bash`.

Oro setup generates a separate project file:

```text
$ORO_HOME/projects/<project>/oracle-settings.json
```

It contains only the `SessionStart -> oro-search-hook` probe and
`PreToolUse: Read -> oro-search-hook` groups. No stop, formatting, capture,
task, or shell hooks are copied. For
`LaunchPolicyReadOnly`, `buildClaudeArgsWithLaunchPolicy` requires this file
and adds `--settings <oracle-settings.json>` while preserving:

- `--safe-mode`;
- `--permission-mode plan`;
- `--tools Read,Glob,Grep,WebSearch,WebFetch`;
- `--no-session-persistence`; and
- no `--add-dir`.

A missing profile or hook binary returns an actionable error telling the
operator to run `oro setup` or reinstall Oro. Default worker launches continue
using the existing project settings unchanged.

### 6. Codex hook override

Codex Oracle launches already use `--sandbox read-only` and `--ephemeral`.
They currently inherit the global Oro hook block, but that is implicit and
can drift with user configuration.

For `LaunchPolicyReadOnly`, the Codex spawner resolves and validates the exact
`$ORO_HOME/hooks/oro-search-hook` file, starts with `--ignore-user-config`,
passes `--enable hooks`, and supplies explicit CLI config for only:

- `SessionStart -> oro-search-hook`; and
- `PreToolUse` with matcher `^Bash$` -> `oro-search-hook`.

One shared `worker.ValidateManagedOracleHook` predicate is used by both
launchers, profile publication, setup health, and public doctor. The hook path
must be canonical, a regular executable rather than a symlink, executable,
owned by the current user, and not group/world writable. Validation fails
closed before spawn. Only after that validation, the read-only automated
Oracle launch passes `--dangerously-bypass-hook-trust`; Codex documents this
flag for automation that independently vets hook sources. Because user config
is ignored, the bypass cannot grant trust to unrelated user hooks.
Authentication still loads through Codex's normal auth store. Managed policy
may still disable hooks, which the runtime `SessionStart` probe detects.

The explicit config is constructed as argv, not shell interpolation. The
launcher preserves `--sandbox read-only`, `--ephemeral`, and the absence of
extra writable directories. Missing or untrusted hook binaries fail with the
same actionable setup error as Claude.

The search hook handles the current Codex event and response contract:

- input: `tool_name: "Bash"` and `tool_input.command`;
- intercept only a single, simple `cat [--] <one-path>` command with no pipe,
  redirection, substitution, or command chaining;
- bypass bounded/read-oriented commands such as `sed -n`, `head -n`,
  `tail -n`, and `rg`;
- fail open on ambiguous shell syntax because this is a context guard, not a
  security boundary; and
- for a supported large code file, return a structural summary and deny the
  broad command with Codex-native
  `hookSpecificOutput.hookEventName = "PreToolUse"` and
  `hookSpecificOutput.permissionDecision = "deny"`, plus a `systemMessage`
  directing a targeted range read.

Claude retains its existing native response format. The global interactive
Codex block emitted by `codexHookConfigBlock` is updated from the obsolete
`str_replace_based_edit_tool` matcher to `^Bash$` for parity, but Oracle
correctness never depends on that ambient block. Tests use captured current
Codex `Bash` fixtures; legacy `view` fixtures cannot satisfy acceptance.

### 7. Setup, refresh, and doctor

Normal and stealth `oro setup/init` paths generate the Claude Oracle settings
file alongside the existing project settings. `oro start` refreshes it during
runtime-asset preflight so upgraded hook paths or formats do not leave stale
profiles. Generation is idempotent and overwrites only the Oro-owned
`oracle-settings.json`; user-owned settings remain untouched.

Shared hook installation/profile refresh occurs before
`codexAssetsRequired` can return from preflight, so Claude-only routing gets
the same refresh. The ambient interactive `codexHookConfigBlock` update stays
behind the Codex-specific branch and is independently tested.

Installation first writes and validates the managed hook binary, then
atomically writes the Oracle profile, so a partial failure never publishes a
profile pointing at an absent binary. The existing search-hook binary remains
the single executable; no second binary is introduced. Both setup's
`setupPhase5Doctor -> runDoctor` health check and the public
`oro doctor -> runDoctorDiagnose` path validate the Oracle profile,
executable/trusted-file properties, and index readiness, and report the setup
command that repairs each missing asset. Both entry points call one shared
`diagnoseOracleSearchAssets` helper, which in turn calls
`worker.ValidateManagedOracleHook`; neither doctor may duplicate or weaken the
trust predicate.

## Error and Security Model

| Failure | Behavior |
|---|---|
| Code index absent/empty | Attempt bounded synchronous seed; on failure warn and continue with an empty map |
| FTS query timeout/error | Log/warn; empty map; continue |
| Malformed search result | Skip result; never invent file or line evidence |
| Deleted, non-regular, absolute, traversal, sibling, or symlink-escaping result | Drop it before formatting; never expose it in the prompt |
| Map exceeds budget | Stop at entry boundary and append truncation marker |
| Hook receives unsupported/malformed read event | Fail open; the read proceeds |
| Hook binary/profile missing before spawn | Fail closed with setup/install remediation |
| Hook activation probe does not arrive | Terminate Oracle; fail closed with disabled/untrusted/stale remediation |
| Hook summarizer cannot parse a supported file | Fail open; the read proceeds |

The hook is a context guard, not a security boundary. Read-only sandbox and
tool policy remain the security boundary. Hook configuration must never add
write-capable tools or writable directories. The Codex trust bypass is limited
to isolated user-config-free Oracle automation after the exact hook file
passes the stated ownership and permission checks; interactive sessions do
not receive it.

Repository search results remain local. The Oracle prompt must not instruct
the model to send indexed source or summaries to web tools. External citations
and repository citations remain separate evidence classes.

## Compatibility and Rollout

All protocol changes are additive: `AssignPayload.CodeSearchContext` already
exists, and `OraclePromptParams.SearchMap` is an internal optional field. Old
assigners that send no context produce the explicit empty-map guidance.

No database migration, index format, task schema, or public CLI change is
required. Fresh setups do gain a bounded initial build. Removing the feature
is a code/config rollback: old Oracles continue with direct
`Glob`/`Grep`/reads.

The setup refresh must land before launchers require the profiles. Dependency
ordering in beadcraft will enforce that sequence.

## Verification

Focused tests must prove:

1. Query normalization is deterministic and bounded.
2. Map formatting contains paths/symbols/ranges, excludes source bodies,
   deduplicates results, and truncates only at UTF-8-safe entry boundaries.
3. The shared worktree filter rejects deleted, non-regular, absolute, sibling,
   traversal, and symlink-escaping rows and both production prompt chains use
   it.
4. Oracle prompt rendering includes populated and empty search guidance while
   preserving the read-only/no-QG contract.
5. Dispatcher research assignments traverse the real payload-to-worker call
   chain and the resulting Oracle prompt contains compact metadata, not chunk
   bodies; timeout/storage failure logs an event and still assigns.
6. Standalone research maps `CodeSearchContext` into the actual captured
   prompt and uses FTS-only lookup, while ordinary work retains its reranker;
   unset `ORO_HOME` is resolved and exported.
7. An empty fresh-repository index is synchronously seeded by setup/start/work
   readiness paths, while a failed build degrades to the documented warning.
8. Full start, daemon-only, dispatcher start, external worker, direct worker,
   and standalone work resolve and propagate identical non-empty
   `ORO_HOME`/`ORO_PROJECT` values when both are initially unset; standalone
   production construction observes them before creating its runtime spawner.
9. Claude read-only args use only the generated Oracle settings, retain all
   sandbox/tool restrictions, and fail if the runtime does not execute the
   configured `SessionStart` probe.
10. Codex read-only args ignore user config, enable hooks, contain current
   `Bash` and `SessionStart` definitions, pass trust bypass only after exact
   file validation, retain read-only/ephemeral restrictions, and fail if the
   runtime does not execute the probe.
11. Normal and stealth setup plus start refresh generate valid, idempotent
   Oracle settings after the hook exists; setup health check and public doctor
   detect missing/untrusted hooks, stale profiles, and an unready index.
12. Captured current Claude `Read` and Codex `Bash cat` fixtures produce
    structural summaries for large code; targeted Claude ranges and Codex
    `sed -n`/`head`/`tail`/`rg` bypass; ambiguous shell input fails open.
13. The full quality gate passes.

The epic acceptance test is deliberately guarded against branch-only and
missing-test false positives:

```text
Cmd: test "$(git branch --show-current)" = main && rg -q '^func TestOracleContextEfficientSearchEndToEnd\(' cmd/oro/oracle_context_search_e2e_test.go && go test ./cmd/oro -run '^TestOracleContextEfficientSearchEndToEnd$' -count=1
Assert: exit code 0
```

`TestOracleContextEfficientSearchEndToEnd` is a small offline aggregator over
three independently executable fixture groups: search-chain, asset/identity,
and runtime-hook integration. Asset/identity is itself a tiny composer over
separate asset-lifecycle and six-surface identity fixtures. Runtime-hook
integration also drives a probed read-only fake through the actual standalone
`spawnAndWaitOutput` consumer and counts one underlying process wait. This
keeps each implementation task bounded while preserving the single guarded
epic command. The fixtures use temporary
repositories containing a searchable Go symbol and a large Go file. They start
by running normal bootstrap and setup, validating
`setupPhase5Doctor -> runDoctor`, then running the public
`runDoctorDiagnose` path against the published assets. A second temporary
repository runs stealth bootstrap and the same asset validation. The test
starts from an empty index, runs the real dispatcher and standalone
prompt-wiring surfaces, and asserts metadata is present while source bodies
are absent. The index fixture also returns deleted, directory/non-regular,
absolute, sibling, traversal, and symlink-escape rows; none may appear in
either prompt. The test exercises `runWork` through the production dependency
constructor, the real `runWorker` entry, and capturing child spawners for full
start, daemon-only, dispatcher start, and external-worker launch with identity
variables initially unset, proving all routes resolve the same profile before
runtime-spawner construction. Its
fake Claude and Codex executables parse the generated runtime configuration,
invoke the configured `SessionStart` and `PreToolUse` commands, and verify the
probe plus broad-read summary and targeted-read bypass behavior. The test does
not require external model credentials. Production's per-launch probe is the
proof that an actual runtime loaded and executed its hook configuration.

## Premortem

```yaml
premortem:
  mode: deep
  context: context-efficient indexed and hook-backed Oracle search

  tigers:
    - risk: Claude receives the ordinary project settings and executes mutation-adjacent hooks during read-only research.
      severity: high
      mitigation_checked: Current read-only launch deliberately omits --settings; the design preserves that intent with a separate search-only profile.
    - risk: Standalone research activates the model reranker and adds latency, cost, or a second mutable subprocess before the Oracle starts.
      severity: high
      mitigation_checked: Current workCodeIndex exposes only SearchInWorkdir; the design adds and requires FTS5Search for research.
    - risk: Full indexed chunks consume the context budget before research begins.
      severity: high
      mitigation_checked: Current worker formatting includes chunk bodies and a 128 KiB cap; the design introduces a body-free 8 KiB map.
    - risk: User-global runtime configuration silently removes or changes Oracle hook behavior.
      severity: medium
      mitigation_checked: Claude gets an isolated profile; Codex ignores user config and gets validated explicit definitions; both must complete a SessionStart probe.
    - risk: A fresh repository opens an empty index, so every search unit test passes with mocks while production supplies no map.
      severity: high
      mitigation_checked: Setup/start/work synchronously seed an empty index and the epic acceptance starts from a real empty database.
    - risk: Full start works because it exports project variables, while daemon-only and external workers cannot resolve the Oracle profile.
      severity: high
      mitigation_checked: Every public launch surface and runWorker independently use the same runtime-project environment helper; acceptance starts with both variables unset.
    - risk: Stale or hostile index paths escape the assigned worktree and appear as navigation hints.
      severity: high
      mitigation_checked: Both callers use one canonical containment filter; acceptance includes deleted, absolute, sibling, traversal, and symlink-escape rows.

  elephants:
    - risk: The feature is called search, but v1 still gives the Oracle no iterative semantic-search tool after its initial map.
    - risk: A stale code index can confidently point at old symbols; the prompt must treat the map as navigation, never evidence.

  paper_tigers:
    - risk: The hook denies broad reads and could prevent exact evidence collection.
      reason: Claude offset/limit and Codex sed/head/tail/rg bypasses allow targeted reads; the Oracle prompt makes this escape hatch explicit.
    - risk: Hook parse failure blocks research.
      reason: Existing hook behavior fails open on malformed events, stat failures, unsupported languages, and summarizer errors.
```

## Out of Scope

- A new Oracle-only search daemon or MCP server.
- Interactive semantic-query tools after launch.
- Changes to index storage, embedding models, or reranking.
- Sending private repository content to external search providers.
- Changing structural summarization algorithms or adding a general shell
  parser; v1 only recognizes the narrow Codex command forms specified above.
- Using search output as final evidence without an exact targeted read.
