# Unattended factory task graph

Design: [unattended factory design](2026-10-23-unattended-factory-design.md), Gate 1 PASS. Gate 2 PASS after independent review and two contract repairs. Native parent: `oro-factory-managed`, under `oro-factory-autonomy`. There are now 68 tracked nodes below the managed epic, including host capacity's seven children, cleanup runtime's seven children and two wiring grandchildren, container transport/readiness, managed file identity, CLI start's immutable admission view, and the stopped-keeper and guard-image implementation leaves. Later integration findings remain qualification blockers until their protocols and tests are accepted.

Every task inherits the design's authority, budget, platform, and operational requirements. Root owns Git integration; implementers use test-first RED/GREEN and report frozen files. Keep full-gate blockers visible until resolved. Component success does not close the parent.

Final acceptance: `./scripts/qualify_factory.sh --docker-context colima-oro-factory --iterations 10`, rerun on integrated main.

## Shared implementation contracts

The design pins ManagedSpec, RuntimePolicy, RecoveryPolicy, Check, ScopeHandle,
HostInvocation, HostResult, HostRuntime, and HostReservation. Add the following exact seams;
private helpers may be chosen by implementers.

```go
type ScopeAuthority interface {
    RecordHello(context.Context, ScopeHandle, int, int) error // verified peer PID, PGID
    Granted(context.Context, ScopeHandle) (bool, error)
    Observe(context.Context, ScopeHandle) (ScopeObservation, error)
    CleanupPermit(context.Context, ScopeHandle) (DockerCleanupPermit, error)
    MarkQuiescent(context.Context, ScopeHandle) error
    Close() error
}
type ScopeObservation struct {
    Phase string
    PID, PGID int
    Deadline time.Time
}
func OpenScopeAuthority(ctx context.Context, stateDir, id string) (ScopeAuthority, error)
type SupervisorBackend interface {
    HostRuntime
    ReleaseHostReservation(context.Context, string) error
    StartController(context.Context, ScopeHandle) error
    Retire(context.Context, ScopeHandle) error
    Quiescent(context.Context, ScopeHandle) (bool, error)
}
type sourceLimits struct {
    MaxArchiveBytes, MaxBytes, MaxFileBytes int64
    MaxFiles, MaxPathBytes int
}
```

Authority instances are bound to one frozen managed objective. Every mutation
compares current generation, nonce, and lifecycle phase in a transaction. The
Darwin backend verifies actual peer credentials before RecordHello; the peer's
JSON claim is not authentication. Journal/controller/supervisor storage APIs use
separate ownership transactions from the existing objective revision.
Observe reads an exact full handle, including revoked old epochs, for recovery;
unknown or mismatched handles fail without creating state or granting execution.

Host runtime configuration is supplied by trusted CLI wiring, not by ownership
queries. `cli-start` freezes/verifies the Oro executable and bounded runtime
artifact before service registration, including across resume; `host-budget`
includes that additional executable (up to 512 MiB). The objective label hash is
the first eight SHA-256 bytes of canonical state root + NUL + objective ID.
`host-scope` enforces the design's exact read-only observer argv, environment,
repository, frozen Git identity, and 256-byte output limit. The backend consumes
already reserved handles; core journal code owns budget/scope allocation.

Routine live-controller Docker retirement uses ordinary H within the work
deadline. Recovery C runs from the token-owned supervisor only after the
controller and other non-Docker scopes positively retire. New ordinary work
cannot overlap a nonquiescent cleanup scope. Storage must preserve this
distinction; neither a controller exemption nor a supervisor scope that blocks
its own recovery is allowed.

Integration commands below deliberately enable live backends. Ordinary package
runs may omit expensive opt-in integration scenarios, but qualification requires
all of them and treats unavailable dependencies or skipped scenarios as failure.
Use the dedicated Docker context only. Never launch the old Oro swarm or mutate
unrelated tasks/services.

## oro-mf-contract: Validate managed factory contracts

- Read: pkg/factory/managed_contract.go, pkg/factory/contract.go:Spec/Status.
- Signature: ManagedSpec, RuntimePolicy, RecoveryPolicy, Check; validateManagedSpec(ManagedSpec) error.
- Test: pkg/factory/managed_contract_test.go:TestManagedSpecContract.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedSpecContract$' -count=1 -timeout=180s`.
- Assert / edges: Reject unknown/unbounded/overflow budgets, mutable image tags, malformed costs/check IDs, unsafe runtime identity; stable complete policy hash; legacy Spec unchanged.
- Depends on: none.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-journal: Journal managed execution ownership

- Read: pkg/factory/managed_store.go, pkg/factory/store.go:openStore/save, pkg/factory/lock.go:lockObjective.
- Signature: ScopeHandle; ScopeAuthority.RecordHello/Granted/Observe/MarkQuiescent; managed journal transactions.
- Test: pkg/factory/managed_store_test.go:TestManagedScopeJournal.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedScopeJournal$' -count=1 -timeout=180s`.
- Assert / edges: Reserve nonce before external effects; atomically compare generation/nonce/phase, reject revoked grants, count starts/calls once, immutable deadline, FULL synchronous persistence. Observe reopens authenticated PID/PGID and deadline for an exact revoked handle; unknown/mismatched handles fail and reads never grant.
- Depends on: `oro-mf-contract`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-admission: Freeze managed objective admission

- Read: pkg/factory/managed_admission.go, pkg/factory/admission.go:validateRepository, pkg/factory/contract.go:freezePolicies, pkg/factory/engine.go:run/admit.
- Signature: AdmitManaged(ctx context.Context, stateDir string, spec ManagedSpec, reservation HostReservation) (Status,error).
- Test: pkg/factory/managed_admission_test.go:TestManagedAdmission.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedAdmission$' -count=1 -timeout=180s`.
- Assert / edges: Strict canonical repository/state containment; marker precedes state; atomic frozen artifacts plus admission identity; repeated identical start no reset; source input removal permits resume; ordinary Run cannot adopt managed ID; bind persisted reservation ID/root/objective/byte tuple immutably.
- Depends on: `oro-mf-contract`, `oro-mf-journal`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-budget: Reserve managed host capacity

This epic retains its original integration acceptance and depends explicitly on
`oro-mf-host-limits`, `oro-mf-host-release`, `oro-mf-host-ledger`,
`oro-mf-ledger-bootstrap`, and `oro-mf-host-transfer64`.

- Read: cmd/oro/cmd_factory_budget.go, cmd/oro/cmd_factory.go:newFactoryCmd, pkg/storage/runtime_budget.go:CheckRuntimeBudget.
- Signature: admitFactoryHostBudget(ctx context.Context,stateDir string,spec factory.ManagedSpec,probe storage.DiskProbe) (factory.HostReservation,error); releaseFactoryHostReservation(ctx context.Context,stateDir,id string) error.
- Test: cmd/oro/cmd_factory_budget_test.go:TestManagedAdmissionBudget.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestManagedAdmissionBudget$' -count=1 -timeout=180s`.
- Assert / edges: Admission boundary denies insufficient/probe-failed budget before scope/root/attempt; equality permits; concurrent admission cannot double-reserve; preserve reservations on uncertain cleanup; require same filesystem and conservative bounded-writer aggregate. Public CLI wiring/denial is verified by cli-start; reservation survives admission-process exit; release reopens ledger by ID, CAS/idempotent, never a closure deferred by start. Include the additional frozen Oro executable (up to 512 MiB), bounded CLI runtime artifact, all encoded request/result overhead and ManagedStateStorageBytes in retained byte accounting.
- Depends on: `oro-mf-contract`, `oro-mf-admission`, `oro-mf-state-budget`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-job: Describe isolated launchd jobs

- Read: cmd/oro/cmd_factory_launchd.go, cmd/oro/launchd.go:existing unrelated label.
- Signature: factoryJobPlist(invocation factory.HostInvocation) ([]byte,error).
- Test: cmd/oro/cmd_factory_launchd_test.go:TestFactoryLaunchdJob.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryLaunchdJob$' -count=1 -timeout=180s`.
- Assert / edges: Unique exact owned labels; immutable argv; no shell interpolation; KeepAlive false for invocations, AbandonProcessGroup false, finite exit timeout; reject invalid identities/limits.
- Depends on: `oro-mf-contract`, `oro-mf-journal`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-exec: Run bounded host invocation wrappers

- Read: cmd/oro/cmd_factory_host_exec.go, cmd/oro/cmd_factory.go:newFactoryCmd, pkg/factory/runner.go:runProcess.
- Signature: factoryHostExecRequest{Invocation factory.HostInvocation; ResultPath string}; runFactoryHostExec(ctx context.Context,request factoryHostExecRequest,authority factory.ScopeAuthority) error.
- Test: cmd/oro/cmd_factory_host_exec_test.go:TestFactoryHostExec.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryHostExec$' -count=1 -timeout=180s`.
- Assert / edges: No child or mutable work before authenticated GO; children inherit job PGID; bounded time/output/result serialization; atomic pending result; no host result accepted before scope retirement. Digest-bound ResultPath must equal request-directory/result.pending.json; payload cwd cannot select receipt destination; deadline is rechecked before spawn. Normal child exit7 publishes a valid HostResult with ExitCode7; publication rechecks deadline after directory sync; exclusive inflight survives successful publication; hidden CLI sends mandatory bounded PUBLISHED only after successful timely result publication. Combined ordinary exit7 and retained descendant pipes must remain a transport failure with no PUBLISHED; successful nonzero exits require independent complete output capture.
- Depends on: `oro-mf-host-job`, `oro-mf-host-peer`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-scope: Retire launchd invocation scopes

- Read: cmd/oro/cmd_factory_launchd_runtime.go, cmd/oro/cmd_factory_host_exec.go, pkg/factory/managed_store.go.
- Signature: factory.HostRuntime.Invoke/Retire and factory.ScopeAuthority adapter.
- Test: cmd/oro/cmd_factory_launchd_runtime_test.go:TestFactoryHostScope.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryHostScope$' -count=1 -timeout=180s`.
- Assert / edges: Real temporary launchd jobs prove normal-exit descendant cleanup, timeout, lost bootstrap ack, revoked nonce rejection, real peer PID/UID validation and PGID ESRCH before result consumption; no print parsing or signalling stored PID. Require complete authenticated PUBLISHED frame validated before deadline and a post-decode time/context check; bind request/scope/PID/PGID/deadline/pending digest, then retire and verify exact pending bytes/fields; reject missing/partial/late/mismatched/replayed publication and never accept file existence alone. Trusted config binds canonical root/objective, frozen Oro/Git identities, repository/ref and cleanup/hard stop; exact objective hash includes canonical root. Enforce the design's exact read-only observer argv/environment, no stdin and output limit 256; never allocate scope authority.
- Depends on: `oro-mf-host-exec`, `oro-mf-journal`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-archive: Validate candidate source archives

- Read: pkg/factory/source_archive.go, pkg/factory/contract.go:containedPath.
- Signature: sourceEntry{Path string; Mode uint32; Data []byte}; decodeSourceArchive(io.Reader,sourceLimits) ([]sourceEntry,error).
- Test: pkg/factory/source_archive_test.go:TestCandidateImportRejectsEscapes.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestCandidateImportRejectsEscapes$' -count=1 -timeout=180s`.
- Assert / edges: Reject traversal, absolute/.git/case-collision/duplicate paths, symlinks/hardlinks/devices/FIFOs/submodules, oversize bytes/file/inode/path counts; nothing extracted outside owned import; preserve regular bytes and executable mode.
- Depends on: none.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-git-tree: Preserve exact candidate Git trees

- Read: pkg/factory/source_git.go, pkg/factory/git.go:run/commit/promote, pkg/factory/source_archive.go.
- Signature: managed Git operations consume HostRuntime and exact sourceEntry vectors.
- Test: pkg/factory/source_git_test.go:TestCandidateExportExactTree.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestCandidateExportExactTree$' -count=1 -timeout=180s`.
- Assert / edges: Raw ls-tree/cat-file export preserves every path/mode/blob despite export-ignore/subst; filter-free import creates same tree and expected parent; candidate hooks/filters never execute on host; bounded bulk output; CAS preserved.
- Depends on: `oro-mf-archive`, `oro-mf-host-scope`, `oro-mf-host-limits`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-watchdog: Enforce independent namespace deadlines

- Read: cmd/oro-factory-guard/main.go (new cmd/oro-factory-guard), cmd/oro-factory-guard/managed contract runtime policy.
- Signature: runGuard(ctx context.Context,absoluteDeadline time.Time,ready io.Writer) error.
- Test: cmd/oro-factory-guard/main_test.go:TestNamespaceDeadline.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro-factory-guard -run '^TestNamespaceDeadline$' -count=1 -timeout=180s`.
- Assert / edges: Immutable fixed deadline and monotonic timer; readiness only after installation; no extension channel; Linux PID1 exits at deadline; real namespace test shows UID1000 cannot signal/stop/replace UID65534 guard and detached descendants die.
- Depends on: none.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-docker-storage: Bound container source storage

- Read: pkg/factory/execution_docker_storage.go, pkg/factory/managed_store.go, pkg/factory/source_archive.go.
- Signature: managed source/keeper/scratch resource operations through HostRuntime.
- Test: pkg/factory/execution_docker_storage_test.go:TestExecutionStorageLimits.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestExecutionStorageLimits$' -count=1 -timeout=180s`.
- Assert / edges: Journal local-driver tmpfs/keeper before creation; enforce byte+inode quotas; keeper maintains source after worker stop until trusted export; no host mount; private checker sources; image/build/guard identity pinned; subsequent objective survives quota attack.
- Depends on: `oro-mf-journal`, `oro-mf-host-scope`, `oro-mf-archive`, `oro-mf-watchdog`, `oro-mf-git-tree` (shared host invocation allocator), `oro-mf-docker-ready`, `oro-mf-docker-archive`, `oro-mf-docker-time`, `oro-mf-cleanup-runtime`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-docker-ready: Authenticate container guard readiness

- Read: cmd/oro/cmd_factory.go:newFactoryCmd, cmd/oro/cmd_factory_launchd_runtime.go:verifyFactoryRuntimeProgram, cmd/oro/cmd_factory_host_exec.go, cmd/oro-factory-guard/main.go.
- Signature: newFactoryDockerReadyCmd() *cobra.Command; runFactoryDockerReady(context.Context,factoryDockerReadyRequest,io.Writer) error.
- Test: cmd/oro/cmd_factory_docker_ready_test.go:TestFactoryDockerReady.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryDockerReady$' -count=1 -timeout=180s`.
- Assert / edges: Frozen Oro helper verifies canonical Docker executable SHA256 and explicit private config/context; inspect exact stopped container ID/name/labels/image/guard UID/entrypoint/deadline before fixed start/attach. Accept exactly timely READY newline, kill and reap only its owned attaching client, return bounded identity/deadline while keeper remains alive. Invalid ownership, missing/partial/late/extra readiness, early exit, overflow, or incomplete client retirement fail closed. No ambient executable/context or stdin. All effects belong to one authenticated pre-journaled HostRuntime invocation; core rechecks the running container identity and StartedAt before granting work.
- Depends on: `oro-mf-host-scope`, `oro-mf-watchdog`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-docker-scope: Reconcile container execution scopes

- Read: pkg/factory/execution_docker.go, pkg/factory/execution_docker_storage.go, pkg/factory/managed_store.go.
- Signature: managed Docker backend scope create/start/retire/export operations.
- Test: pkg/factory/execution_docker_test.go:TestExecutionIndependentDeadline.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestExecutionIndependentDeadline$' -count=1 -timeout=180s`.
- Assert / edges: Create stopped with durable name/labels then record ID/start; lost create ack reconciles only own scopes; inspect isolation/resource fields; every exec fixed1000; stop+inspect before export; journal/control-death independent expiry and keeper cleanup.
- Depends on: `oro-mf-docker-storage`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-backend-seam: Separate controller delivery operations

- Read: pkg/factory/engine.go:run/attempt/check/observeTarget, pkg/factory/git.go, pkg/factory/runner.go.
- Signature: private deliveryBackend used by controller; legacy backend preserves API.
- Test: pkg/factory/backend_test.go:TestControllerBackendPreservesLegacy.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestControllerBackendPreservesLegacy$' -count=1 -timeout=180s`.
- Assert / edges: Existing trusted lifecycle/recovery/CAS tests remain green; backend is used for all managed effects including fresh observeTarget/completed-artifact Git instances; no legacy runProcess fallback for managed execution.
- Depends on: `oro-mf-admission`, `oro-mf-git-tree`, `oro-mf-docker-scope`, `oro-mf-state-budget`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-managed-delivery: Deliver isolated managed candidates

- Read: pkg/factory/managed_engine.go, pkg/factory/engine.go:attempt/check, pkg/factory/execution_docker.go, pkg/factory/source_git.go.
- Signature: RunManagedController(ctx context.Context,stateDir,id string,generation uint64,token string,host HostRuntime) (Status,error).
- Test: pkg/factory/managed_engine_test.go:TestManagedControllerBackendWiring.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedControllerBackendWiring$' -count=1 -timeout=180s`.
- Assert / edges: Production managed controller exports base, executes bounded worker, stops namespace, validates/imports exact candidate, preserves attempt/deadline/feedback; no stale scope reused, no origin metadata insidecandidate.
- Depends on: `oro-mf-backend-seam`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-acceptance: Create protected acceptance receipts

- Read: pkg/factory/managed_acceptance.go, pkg/factory/engine.go:check/authorizePromotion, pkg/factory/receipts.go:acceptEvidence/reviewEvidence.
- Signature: managed acceptance uses frozen []Check and host-produced evidence.
- Test: pkg/factory/managed_acceptance_test.go:TestReceiptAuthorityIsolation.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestReceiptAuthorityIsolation$' -count=1 -timeout=180s`.
- Assert / edges: Read-only exact candidate in separate scope; compare bounded stdin/stdout/exit against frozen expected outcomes; every required check passes; receipt-looking candidate stdout cannot approve; failure overrides prose; source mutation fails.
- Depends on: `oro-mf-managed-delivery`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-mcp: Restrict model tools to one container

- Read: pkg/factory/mcp_bridge.go, pkg/factory/execution_docker.go, pkg/factory/managed_store.go.
- Signature: ServeFactoryMCP(ctx context.Context,input io.Reader,output io.Writer,executor scopedExecutor) error.
- Test: pkg/factory/mcp_bridge_test.go:TestMCPAuthorityBoundary.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestMCPAuthorityBoundary$' -count=1 -timeout=180s`.
- Assert / edges: JSON-RPC stdio initialize/tools list/call; only exec(command,timeout_seconds), reject unknown/oversize args; fixed container/cwd/UID/daemon env from startup; deadline/call/output caps; no host authority or peer access.
- Depends on: `oro-mf-docker-scope`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-claude: Execute restricted real model stages

- Read: pkg/factory/model_claude.go, pkg/factory/mcp_bridge.go, pkg/factory/managed_acceptance.go, pkg/factory/managed_engine.go.
- Signature: managed worker/reviewer model adapters via HostRuntime.
- Test: pkg/factory/model_claude_test.go:TestModelToolSurface.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestModelToolSurface$' -count=1 -timeout=180s`.
- Assert / edges: Real authenticated Claude only sole admitted MCP tool; host read/shell prompt cannot cause host effect; frozen executable/model/flags, reserve bounded calls/cost before launch; separate readonlyreview, strict verdict, real repair from acceptance feedback.
- Depends on: `oro-mf-mcp`, `oro-mf-acceptance`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-supervisor: Recover managed controller generations

- Read: pkg/factory/supervisor.go, pkg/factory/managed_store.go, pkg/factory/engine.go:resume/reconcilePromotion, pkg/factory/managed_engine.go.
- Signature: Supervise(ctx context.Context,stateDir,id string,backend SupervisorBackend) (Status,error); SupervisorBackend embeds HostRuntime and ReleaseHostReservation(context.Context,string) error.
- Test: pkg/factory/supervisor_test.go:TestSupervisionRecovery.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestSupervisionRecovery$' -count=1 -timeout=180s`.
- Assert / edges: Reserve counted incarnation/start beforeeffects; revoke+retire everyoldscope before successor; frozenresume, consumedattempts, unchangeddeadline; duplicateowner denial; budget/backoff bounded; no neweffect during uncertaincleanup; bounded postCAS observation evenexhausted; exhausted-start reconciliation uses bounded HostRuntime read-only Git scope from separate immutable MaxReconciliationInvocations allowance, zero new StartController calls and unchanged counters; terminal+quiescent cleanup releases persisted host reservation, recovery finishes terminal-before-release death.
- Depends on: `oro-mf-managed-delivery`, `oro-mf-journal`, `oro-mf-host-scope`, `oro-mf-docker-scope`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-reservation-failure: Stop uncounted supervisor startups

- Read: cmd/oro/cmd_factory_supervise.go, cmd/oro/supervisor.go, cmd/oro/cmd_factory_launchd.go.
- Signature: internal _supervise exit mapping and immutable hard-stop args.
- Test: cmd/oro/cmd_factory_supervise_test.go:TestSupervisorReservationUnavailable.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestSupervisorReservationUnavailable$' -count=1 -timeout=180s`.
- Assert / edges: Unavailable/corrupt reservation DB: boundeddiagnostics, zero backendcalls, serviceexit0 no launchdhotloop; durablestopped failures also serviceexit0 but publicwaitnonzero; hardstop outsideDB; countedvsuncounted evidence.
- Depends on: `oro-mf-supervisor`, `oro-mf-host-job`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cli-start: Start managed factory services

This epic retains its public integration acceptance and explicitly depends on
its `oro-mf-admission-view` child. Remaining publication/activation work must
also be decomposed before implementation.

- Read: cmd/oro/cmd_factory.go:newFactoryCmd, cmd/oro/root.go:rootSubcommands, cmd/oro/cmd_factory_start.go, cmd/oro/cmd_factory_supervise.go.
- Signature: factory start strictspec + internal _controller/_supervise/_mcp commands.
- Test: cmd/oro/cmd_factory_start_test.go:TestFactoryManagedStart.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryManagedStart$' -count=1 -timeout=180s`.
- Assert / edges: ActualCLI wires budget->frozenadmission->ownedservice registration; controllerauthenticatedgate beforework; existingrun unchanged/rejectsmanaged; repeatedstartidempotent noreset; no legacy fixedlaunchdlabel/swarm; low disk or failed probe through actual public CLI creates no scope/controller/attempt; successful start return never releases active persistent host reservation. Durably freeze Oro executable identity and bounded runtime configuration before registration; resume verifies original frozen Oro/Git artifacts and supplies trusted host-runtime config. Missing or changed content fails without substituting current installation.
- Depends on: `oro-mf-host-budget`, `oro-mf-host-scope`, `oro-mf-claude`, `oro-mf-supervisor`, `oro-mf-reservation-failure`, `oro-mf-ledger-bootstrap`, `oro-mf-runtime-config`, `oro-mf-file-identity`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cli-control: Observe or stop managed objectives

- Read: cmd/oro/cmd_factory.go:newFactoryStatusCmd, cmd/oro/cmd_factory_control.go, pkg/factory/contract.go:Status, cmd/oro/supervisor.go.
- Signature: factory wait/stop/status and optional supervision status fields.
- Test: cmd/oro/cmd_factory_control_test.go:TestFactoryManagedControl.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryManagedControl$' -count=1 -timeout=180s`.
- Assert / edges: Statusreadonly truthful objective vsstopped supervision; waitzero onlycompleted; stopdurablerevocation and exactscope retirement/unregister; no unrelatedjob/processkill; unknownpromotion preserved; stop and terminal recovery reopen persisted reservation ledger and release once only after all scopes quiescent; cross-process start-exit/success/crash-before-release/uncertain-cleanup test proves capacity recovered or retained correctly.
- Depends on: `oro-mf-cli-start`, `oro-mf-cleanup-runtime`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-fault-controller: Prove automatic controller recovery

- Read: tests/factory/controller_recovery_test.go, cmd/oro/cmd_factory_start.go, pkg/factory/managed_engine.go, pkg/factory/execution_docker.go.
- Signature: external CLI/process fault harness, no production fault env switches.
- Test: tests/factory/controller_recovery_test.go:TestControllerDeathReapsExecutionScope.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./tests/factory -run '^TestControllerDeathReapsExecutionScope$' -count=1 -timeout=180s`.
- Assert / edges: RealcontrollerSIGKILL whilehostmodel/MCPfixture anddetachedcandidatewriteractive; onlyobserve afterward; no oldscopebefore successor, no latewrites, attemptspentonce, same deadline, acceptedfreshartifact; injectcreateackloss and cleanupdeath windows.
- Depends on: `oro-mf-cli-control`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-fault-supervisor: Prove automatic supervisor recovery

- Read: tests/factory/supervisor_recovery_test.go, cmd/oro/cmd_factory_supervise.go, pkg/factory/supervisor.go, pkg/factory/managed_store.go.
- Signature: external launchd service death harness.
- Test: tests/factory/supervisor_recovery_test.go:TestSupervisorDeathRecovery.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./tests/factory -run '^TestSupervisorDeathRecovery$' -count=1 -timeout=180s`.
- Assert / edges: Real launchd supplies supervisorafterSIGKILL; oldjobs/containersretired beforework, countersfixed; repeat crashes/budgetexhaustion/DBunavailable combination bounded; watchdogkillsnamespacewithoutjournal; unrelatedjobs survive.
- Depends on: `oro-mf-cli-control`, `oro-mf-fault-controller`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-fault-promotion: Reconcile death after exact promotion

- Read: tests/factory/promotion_recovery_test.go, pkg/factory/engine.go:promote/observeTarget/completePromotion, pkg/factory/git.go:promote.
- Signature: trusted admitted Git qualification wrapper/barrier.
- Test: tests/factory/promotion_recovery_test.go:TestDeathAfterPromotion.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./tests/factory -run '^TestDeathAfterPromotion$' -count=1 -timeout=180s`.
- Assert / edges: Realupdate-ref CAS thencontrollerSIGKILL beforecompletion; onlyobserve; completesexactcandidate withoutworker/check/review repeats evenfinalstart+deadlineexpired; unreadabletarget preservespromoting stoppedunknown; concurrenttargetadvance preserved.
- Depends on: `oro-mf-cli-control`, `oro-mf-fault-controller`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-qualification: Qualify sustained unattended delivery

- Read: scripts/qualify_factory.sh, tests/factory, docs/runbooks/factory-core.md, docs/proofs/unattended-factory.md.
- Signature: ./scripts/qualify_factory.sh --docker-context colima-oro-factory --iterations 10.
- Test: tests/factory/qualification_test.go:TestFactoryQualificationEvidence.
- Cmd: `./scripts/qualify_factory.sh --docker-context colima-oro-factory --iterations 10`.
- Assert / edges: BuildactualCLI withrevision+binarydigest, run10realobjectives/3fixtures+allfaultandauthorityscenarios, independentlyexecutepromotedtree, no manualrestart/cleanup/approval; missingprerequisitesfail; fullgates/review and integratedmainrerun beforecutover; no broaderautonomycompletionclaim.
- Depends on: `oro-mf-fault-supervisor`, `oro-mf-fault-promotion`, `oro-mf-claude`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-peer: Authenticate factory Unix socket peers

- Read: cmd/oro/cmd_factory_host_peer_darwin.go, cmd/oro/cmd_factory_host_peer_other.go, cmd/oro/cmd_factory_host_exec.go.
- Signature: factoryHostPeer(conn *net.UnixConn)(pid int,uid int,err error).
- Test: cmd/oro/cmd_factory_host_peer_test.go:TestFactoryHostPeer.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryHostPeer$' -count=1 -timeout=180s`.
- Assert / edges: Read real Darwin LOCAL_PEERCRED and LOCAL_PEERPID through SyscallConn; an owned subprocess Unix socket yields its actual PID and UID; no claimed JSON identity or ps parsing; nil/closed socket and unsupported OS fail explicitly.
- Depends on: `oro-mf-contract`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-limits: Bound retained host artifacts

- Parent: `oro-mf-host-budget`.
- Read: pkg/factory/managed_contract.go:validateManagedSpec; pkg/factory/managed_storage.go:ManagedStateStorageBytes; cmd/oro/cmd_factory_host_exec.go:factoryHostExecRequestLimit.
- Signature: ManagedHostRetention(ManagedSpec) (HostRetentionLimits,error); exported ManagedHostRequestBytes=64MiB and ManagedHostResultMetadataBytes=16384.
- Test: pkg/factory/managed_host_limits_test.go:TestManagedHostRetention.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedHostRetention$' -count=1 -timeout=180s`.
- Assert / edges: Use checked integer arithmetic for archive A=S+1024F+1536, output O=max(A,1MiB), pending P=4O+16384, invocation records Q=H+M+T+reconciliation+cleanup, scope records E=2*(supervisor+controller+reconciliation+cleanup)+H+M+T, source versions X=2+attempts*(checks+3). Minimum B=3*512MiB+4*2MiB+2*1MiB+ManagedStateStorageBytes+32MiB+1MiB+X*(2A+2S+128KiB*F+1MiB)+Q*(2*64MiB+2P+2O)+E*1MiB. F bounds files plus implicit dirs, MaxPathBytes255. Reject overflow and4*ceil(A/3)+1MiB>64MiB; exact known fixture and each budget family tested; return caps all future writers consume. CLI rejects HostRequiredBytes below MinimumBytes. The Docker root consumes512 transport bytes; each cleanup permit fits within its counted scope metadata. Cleanup accounting is implemented by the separate cleanup-policy task.
- Depends on: `oro-mf-contract`, `oro-mf-state-budget`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-release: Expose terminal host reservation eligibility

- Parent: `oro-mf-host-budget`.
- Read: pkg/factory/managed_store.go:mutate/setDesired/scopes; pkg/factory/managed_admission.go:loadManagedManifest; pkg/factory/managed_contract.go:HostReservation.
- Signature: ReleasableHostReservation(context.Context,string,string) (HostReservation,bool,error).
- Test: pkg/factory/managed_reservation_test.go:TestReleasableHostReservation.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestReleasableHostReservation$' -count=1 -timeout=180s`.
- Assert / edges: One ownership transaction returns the exact immutable reservation only for monotonic stopped supervision with every exact scope quiescent and cleanup evidence. Running or live/revoked scopes deny release; missing/corrupt authority errors. Eligibility cannot reopen execution; CLI compares the exact tuple before a durable idempotent release.
- Depends on: `oro-mf-journal`, `oro-mf-state-budget`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-ledger: Persist host capacity reservations

- Parent: `oro-mf-host-budget`.
- Read: cmd/oro/cmd_factory_budget.go; pkg/storage/runtime_budget.go:CheckRuntimeBudget; pkg/factory/managed_host_limits.go:ManagedHostRetention; pkg/factory/managed_reservation.go:ReleasableHostReservation.
- Signature: admitFactoryHostBudget(context.Context,string,factory.ManagedSpec,storage.DiskProbe) (factory.HostReservation,error); releaseFactoryHostReservation(context.Context,string,string) error.
- Test: cmd/oro/cmd_factory_budget_test.go:TestManagedAdmissionBudget.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestManagedAdmissionBudget$' -count=1 -timeout=180s`.
- Assert / edges: Canonical parent lock serializes probe and durable reservation, same-filesystem repository/state required. Explicit BudgetFresh needed; denial/probe failure creates no factory/objective root or attempt. Subtract active reservations and preserve largest reserve margin; equality succeeds. Canonical versioned full-image JSON<=8MiB and4096 lifetime entries<=1KiB each, within32MiB retention; root-bound identity/revision/checksum, released IDs retained. Exclusive deterministic staging+fsync+atomic rename+directory sync leaves valid old or new authority after SIGKILL; discard uncommitted staging only under lock after validating committed snapshot. External initialization marker prevents reset when root/ledger disappears; missing/corrupt authority errors. Cross-process tests prove simultaneous admission cannot double-reserve, reservation survives process exit, pre-rename kill recovers old capacity, post-rename kill retains new capacity. Release exact stored tuple only after core eligibility, durably idempotent; uncertain cleanup retains reservation; actual public CLI wiring remains cli-start.
- Depends on: `oro-mf-host-limits`, `oro-mf-host-release`, `oro-mf-admission`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-state-budget: Bound managed SQLite storage

- Read: pkg/factory/managed_storage.go, pkg/factory/managed_store.go:openManagedJournal/managedDurability, pkg/factory/managed_admission.go:admitManaged, pkg/factory/store.go:factoryStore/initialize/save.
- Signature: openManagedFactoryStore(ctx context.Context,dir string)(*factoryStore,error); shared managed SQLite connection configuration.
- Test: pkg/factory/managed_storage_test.go:TestManagedStateByteBudget.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedStateByteBudget$' -count=1 -timeout=180s`.
- Assert / edges: Every managed admission, journal and delivery snapshot opener uses verified FULL durability and bounded rollback journaling with64MiB data-page cap; no WAL growth under a pinned reader; combined managed database and transient journal bytes stay below256MiB; blocked writes fail within context/busy limit; actual interrupted transaction rolls back on reopen; legacy WAL behavior remains unchanged.
- Depends on: `oro-mf-journal`, `oro-mf-admission`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-docker-archive: Validate Docker source archive transport

- Read: pkg/factory/source_archive.go:decodeSourceArchive/rejectSourceSpecialHeader; pkg/factory/managed_host_limits.go:ManagedHostRetention; pkg/factory/source_git.go; cmd/oro-factory-guard/main.go.
- Signature: decodeDockerSourceArchive(io.Reader,sourceLimits)([]sourceEntry,error); trusted USTAR seed encoder with matching sourceLimits.
- Test: pkg/factory/source_docker_archive_test.go:TestDockerSourceArchive.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run 'TestDockerSourceArchive|TestCandidateImportRejectsEscapes|TestManagedHostRetention|TestCandidateExportExactTree' -count=1 -timeout=180s`.
- Assert / edges: trusted seed USTAR preserves UID/GID1000 without docker cp -a; preflight USTAR representability before resource allocation. Export adapter accepts exactly one first ./ directory, strips exactly one ./ from following paths, then performs unchanged raw-header, metadata, path, duplicate, size/count and trailer validation; original decoder semantics unchanged. Reject PAX/GNU/link/special/ambiguous or malformed roots and paths. Shared archive allowance adds exactly512B for Docker root: A=S+1024F+1536; one F=1,S=1048577 real-format stream needs1051136bytes and stays bounded; updated base64 guard rejects overflow. all bytes bounded before parse; never normalize hidden PAX metadata, widen source/file/path quotas, or extract on host; captured real Docker fixture and malformed variants prove transport behavior.
- Depends on: `oro-mf-archive`, `oro-mf-host-limits`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-policy: Bound cleanup invocation policy

- Read: pkg/factory/managed_contract.go:RecoveryPolicy/validateManagedSpec; pkg/factory/managed_host_limits.go:ManagedHostRetention; pkg/factory/managed_store.go:journalRecordLimit.
- Signature: RecoveryPolicy.MaxCleanupInvocations int; updated checked bound helpers.
- Test: pkg/factory/managed_contract_test.go:TestManagedCleanupPolicy.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run 'TestManagedCleanupPolicy|TestManagedSpecContract|TestManagedHostRetention' -count=1 -timeout=180s`.
- Assert / edges: immutable required Recovery.MaxCleanupInvocations C is positive, bounded and part of canonical identity; checked retained-byte/record/epoch bounds include every cleanup helper and permit without spending host or reconciliation counts. Q=H+M+T+R+C; include cleanup scope/epoch rows and <=16KiB permit per C within declared retention. Known small fixture, overflow, zero/negative/excessC, unchanged original deadline and bootstrap storage limits tested. no default unlimited cleanup, no refunded budget, no public CLI dummy wiring; preserve Docker archive +512 root allowance.
- Depends on: `oro-mf-contract`, `oro-mf-host-limits`, `oro-mf-docker-archive`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-authority: Journal restricted cleanup authority

- Read: pkg/factory/managed_store.go:beginGeneration/reserveBudget/checkScopeAdmission/RecordHello/Granted/setDesired; pkg/factory/managed_reservation.go:ReleasableHostReservation; pkg/factory/managed_contract.go:ScopeAuthority.
- Signature: DockerCleanupOp; DockerCleanupBinding; DockerCleanupPermit; ScopeAuthority.CleanupPermit(context.Context,ScopeHandle)(DockerCleanupPermit,error); reserveManagedCleanupInvocation(context.Context,*managedJournal,uint64,ScopeHandle,DockerCleanupOp,DockerCleanupBinding,time.Time)(ScopeHandle,error).
- Test: pkg/factory/managed_cleanup_test.go:TestManagedCleanupAuthority.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run 'TestManagedCleanupAuthority|TestManagedScopeJournal|TestReleasableHostReservation' -count=1 -timeout=180s`.
- Assert / edges: running->stopping->stopped is monotonic; stopping revokes work and admits only bounded cleanup plus exact restricted observer, final stopped seals epochs/scopes/grants and remains release eligibility. Count all supervisor recovery within original start allowance. reserveManagedCleanupInvocation atomically validates existing exact target fullhandle, persists new cleanup token/scope/canonical typed permit before effects, freshC per retry. ScopeAuthority.CleanupPermit reads exact current-generation reserved/granted permit before deadline without granting; no arbitrary argv/host paths/env. Unknown containerID allowed only inspect, stop/remove require full64hex ID, volume stop invalid; names/context/labels match admitted target. stale generation/nonce, wrong target/config/binding, exhaustedC, malformed/corrupt/oversize permit and expired hardstop fail without effects; original work cannot run while stopping, no new authority after release; cross-process reopen/races and exact deadline preservation.
- Depends on: `oro-mf-cleanup-policy`, `oro-mf-journal`, `oro-mf-host-release`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-runtime: Execute exact owned Docker cleanup

This epic retains the integration acceptance below and explicitly depends on
`oro-mf-runtime-config`, `oro-mf-cleanup-result`, `oro-mf-cleanup-plan`,
`oro-mf-cleanup-helper`, `oro-mf-cleanup-wiring`, `oro-mf-docker-time`, and
`oro-mf-docker-time-ingest`.

- Read: cmd/oro/cmd_factory_launchd_runtime.go:validateFactoryObserver; cmd/oro/cmd_factory_launchd_runtime_invoke_darwin.go:validateInvocation; cmd/oro/cmd_factory_docker_ready.go; pkg/factory/managed_contract.go:DockerCleanupPermit.
- Signature: newFactoryDockerCleanupCmd() *cobra.Command; runFactoryDockerCleanup(context.Context,factoryDockerCleanupConfig,factory.DockerCleanupPermit,io.Writer) error.
- Test: cmd/oro/cmd_factory_docker_cleanup_test.go:TestFactoryDockerCleanup.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryDockerCleanup$' -count=1 -timeout=180s`.
- Assert / edges: authenticated cleanup-kind HostRuntime permits only frozen-Oro _docker-cleanup with fixed trusted config path/digest, exact canonical core permit stdin<=16KiB, state-root cwd, fixed environment and1MiB output. Helper verifies frozen config/Docker/endpoint, exact current permit and grant before each child; fixedmax3 Docker commands inspect+ownership check, exact stop/remove, verify. Inspect one exactobject; stop SIGKILL timeout0 fullID; rm --force fullcontainerID without-v or volume rm exactname withoutforce afterconsumersretired. Results bounded typed present/stopped/observed_absent; absence is observation not pending-request completion. no create/start/exec/cp/build/pull/prune/list/arbitrary flags or ambient endpoint; bound every subprocess by originaldeadline and inheritedPGID, verify exactlabels/config/ID/CreatedAt; stale/mismatch/unknownretirement failsclosed and retainscapacity. Native exact cleanup proof includes foreignobjects unchanged, late cleanup succeeds, finalstopped prevents retry.
- Depends on: `oro-mf-cleanup-authority`, `oro-mf-docker-ready`, `oro-mf-host-scope`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-ledger-bootstrap: Recover initial host ledger publication

- Parent: `oro-mf-host-budget`.
- Read: cmd/oro/cmd_factory_budget.go:admitFactoryHostBudgetWithHooks, cmd/oro/cmd_factory_budget_ledger.go:load/initialize/publish, cmd/oro/cmd_factory_budget_test.go:factoryBudgetChildCommand/testFactoryBudgetNoReset.
- Signature: bounded parent bootstrap authority codec and recoverFactoryBudgetBootstrap under the existing factoryBudgetAccess lock.
- Test: cmd/oro/cmd_factory_budget_bootstrap_test.go:TestFactoryBudgetBootstrap.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run 'TestFactoryBudgetBootstrap|TestManagedAdmissionBudget' -count=1 -timeout=180s`.
- Assert / edges: One canonical parent record atomically transitions bootstrap to initialized. Embed and preserve exactly the first snapshot and reservation identity within2KiB; no return or further allocation before the initialized record is synced. Process kills across bootstrap, metadata, ledger stage/rename, parent phase stage/rename and directory sync preserve the same identity. Reconstruct only from valid bootstrap authority; initialized or legacy v1 missing/corrupt ledger remains closed. Reject conflicting first images, unexpected objective state, and ambiguous staging; no missing-state release or budget reset. Separate activation/abort proof remains necessary.
- Depends on: `oro-mf-host-ledger`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-runtime-config: Validate cleanup runtime artifacts

- Parent: `oro-mf-cleanup-runtime`.
- Read: cmd/oro/cmd_factory_launchd_runtime.go:factoryLaunchdConfig/verifyFactoryRuntimeProgram; cmd/oro/cmd_factory_docker_ready.go:factoryDockerInvocation; docs/plans/2026-10-23-unattended-factory-design.md.
- Signature: canonicalFactoryRuntimeConfig(factoryRuntimeConfig)([]byte,error); factoryDockerContextSnapshot(factoryRuntimeConfig)([]byte,[]byte,error); readFactoryRuntimeConfig(context.Context,string,string)(factoryDockerCleanupConfig,error).
- Test: cmd/oro/cmd_factory_runtime_config_test.go:TestFactoryRuntimeConfig.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryRuntimeConfig$' -count=1 -timeout=180s`.
- Assert / edges: canonical bounded runtime.json binds root/objective/policy, frozen Oro/Git paths/hashes, repository/ref, original deadlines and cleanup allowance, Docker binary/hash/context and dedicated owned unix endpoint. Exact derived private snapshot has only config.json bytes {} and contexts/meta/sha256(context)/meta.json with Name, empty Metadata and one Docker Endpoint Host/SkipTLSVerify=false. Reader verifies canonical bytes/digest and all program/context identities before returning trusted config; no publication or ambient fallback. Runtime JSON<=64KiB, context metadata<=8KiB, complete final bundle plus metadata within existing1MiB allowance, at most one equal staging bundle later. reject noncanonical/unknown/duplicate JSON, extra files/settings/auth/plugins/TLS, symlinks/hardlinks or wrong owners, non-unix/changed endpoint, wrong program hashes/paths, changed expected artifact digest or out-of-root runtime path; no fourth executable copy, initialization, grant or uncounted writes. Reader verifies only expected immutable bytes and artifact identities; it remains valid after work Deadline for cleanup. Later producer/consumer compares admitted policy/deadlines, because the reader cannot discover stale journal identity alone. CLI-start separately produces and freezes this bundle from verified immutable admission view.
- Depends on: `oro-mf-docker-ready`, `oro-mf-cleanup-policy`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-file-identity: Bind managed reads to regular inode identity

- Parent: `oro-factory-managed`.
- Read: pkg/factory/managed_storage.go:readManagedInitializationMarker/prepareManagedDatabase; pkg/factory/managed_admission.go:openManagedRegular; pkg/factory/managed_storage_test.go:TestManagedStateByteBudget; pkg/factory/managed_admission_test.go:TestManagedAdmissionRejectsFIFO.
- Signature: existing readManagedInitializationMarker(*os.Root,string) error and openManagedRegular(string,int64,bool)(*os.File,error); introduce only private rooted leaf validation if required.
- Test: pkg/factory/managed_file_identity_test.go:TestManagedFileIdentity.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run 'TestManagedFileIdentity|TestManagedAdmission|TestManagedStateByteBudget' -count=1 -timeout=180s`.
- Assert / edges: external and local initialization markers reject contained and dangling symlinks before interpreting absence or reading authority; managed artifact reads bind opened regular inode to pre-open and post-open leaf identity. Promote a failing reproduction of Go os.Root following contained symlinks despite O_NOFOLLOW. Legitimate ordinary marker/artifact paths continue to work; alias or replacement errors never initialize a fresh database or spend/reset budget. existing/dangling symlink, FIFO/device, leaf replaced with regular or symlink between Lstat/open, inode mismatch, ordinary missing path, cancellation unaffected. No reliance on O_NOFOLLOW inside os.Root, no broad retry/reset or changed executable hash semantics. Scope two production files and one new test file.
- Depends on: `oro-mf-admission`, `oro-mf-state-budget`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-result: Validate exact cleanup observations

- Parent: `oro-mf-cleanup-runtime`.
- Read: pkg/factory/managed_contract.go:DockerCleanupPermit/DockerCleanupBinding; pkg/factory/managed_cleanup.go:encodeCleanupPermit.
- Signature: type DockerCleanupResult; EncodeDockerCleanupResult(DockerCleanupResult,DockerCleanupPermit)([]byte,error); DecodeDockerCleanupResult([]byte,DockerCleanupPermit)(DockerCleanupResult,error).
- Test: pkg/factory/docker_cleanup_result_test.go:TestDockerCleanupResult.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run '^TestDockerCleanupResult$' -count=1 -timeout=180s`.
- Assert / edges: Pure canonical version1 result <=16KiB binds PermitSHA256 of canonical already-authenticated permit and exact Scope/Target/Operation. Observation is present, stopped, or observed_absent; includes observed Binding and MutationAcknowledged. Inspect never acknowledges mutation; pre-mutation absence has false ack. Unknown container ID inspect can fill only ContainerID/CreatedAt, immutable binding fields must remain exact; known lifetime identity never changes. Volume cannot be stopped. No quiescence field or inference. Shared encoder/decoder is used by later helper producer and storage consumer. Reject malformed/duplicate/unknown/noncanonical JSON, oversize, wrong permit digest/scope/target/operation, incompatible observation or acknowledgment, altered labels/image/context/name/volume config, missing known identity. Explicitly document authenticating permit is caller responsibility; codec creates no authority and does not independently read journal. Test all operation-observation-ack combinations and output at size boundary.
- Depends on: `oro-mf-cleanup-authority`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-plan: Constrain Docker cleanup commands

- Parent: `oro-mf-cleanup-runtime`.
- Read: cmd/oro/cmd_factory_docker_ready_inspect.go:validateFactoryDockerInspection; pkg/factory/managed_contract.go:DockerCleanupPermit/DockerCleanupBinding.
- Signature: factoryDockerCleanupInspectArgs(factory.DockerCleanupPermit)([]string,error); factoryDockerCleanupMutationArgs(factory.DockerCleanupPermit,factory.DockerCleanupBinding)([]string,error); parseFactoryDockerCleanupInspection(factory.DockerCleanupPermit,factory.HostResult)(factory.DockerCleanupBinding,string,error).
- Test: cmd/oro/cmd_factory_docker_cleanup_plan_test.go:TestFactoryDockerCleanupPlan.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryDockerCleanupPlan$' -count=1 -timeout=180s`.
- Assert / edges: Pure fixed command planner plus bounded inspector authenticates one exact container or volume. Bind context/name/6labels/image/full64hexID/canonicalCreatedAt/volume driver and exact options. Unknown container ID allowed only inspect exact name; known ID always exact ID. Stop args kill with SIGKILL and timeout0 via Docker stop --signal SIGKILL --timeout 0 fullID; container rm --force fullID without -v; volume rm exactname without force. Missing only exact native-probed exit1/[]/stderr forms. No process execution, no list/prune or caller flags. Fixture normal running/stopped/missing; changed labels/ID/CreatedAt/image/options, multiple objects, oversized/truncated/invalid JSON, exit failures, empty output and foreign object all fail. Changed StartedAt alone is not loss of lifetime ownership; do not import readiness never-started constraint. Retain uncertain daemon request semantics. Max1MiB inspect output; native exact missing forms need evidence before encoding.
- Depends on: `oro-mf-cleanup-authority`, `oro-mf-docker-ready`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-helper: Execute bounded Docker cleanup helpers

- Parent: `oro-mf-cleanup-runtime`.
- Read: cmd/oro/cmd_factory_runtime_config_read.go:readFactoryRuntimeConfig; cmd/oro/cmd_factory_docker_cleanup_plan.go; pkg/factory/managed_contract.go:ScopeAuthority; cmd/oro/cmd_factory_host_exec.go:executeFactoryHostCommand.
- Signature: newFactoryDockerCleanupCmd() *cobra.Command; runFactoryDockerCleanup(context.Context,factoryDockerCleanupConfig,factory.DockerCleanupPermit,io.Writer) error.
- Test: cmd/oro/cmd_factory_docker_cleanup_test.go:TestFactoryDockerCleanup.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryDockerCleanup$' -count=1 -timeout=180s`.
- Assert / edges: Register hidden frozen-Oro factory _docker-cleanup accepting only runtime-config path/digest, canonical <=16KiB permit stdin. Reopen durable scope authority. Before each child verify frozen config, exact same permit, live Granted, Observe granted with matching deadline and PID=PGID>1, and helper actual Getpgid equals observed PGID. Never RecordHello. Execute at most inspect/mutate/verify fixed Docker children with original permit deadline and inherited outer PGID, bounded output. Use shared core result codec and set MutationAcknowledged only when exact mutation child exits0. Publish only complete bounded result before deadline. Stale/revoked/expired/mismatched authority, swapped config/endpoint, denied grant between commands, unexpected child result, unknown target, short output writer, missing pending request and cancellation produce error or observation without fabricated quiescence. Polling does not extend deadlines. No create/start/exec/cp/build/pull/prune/arbitrary argv. Runtime reader identity must match trusted permit objective/policy/context. Unit fake seam plus native owned-container/volume proof; foreign objects unchanged.
- Depends on: `oro-mf-runtime-config`, `oro-mf-cleanup-result`, `oro-mf-cleanup-plan`, `oro-mf-cleanup-authority`, `oro-mf-docker-time-ingest`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-wiring: Restrict launchd cleanup invocations

This epic retains its integration acceptance and depends explicitly on
`oro-mf-cleanup-restriction` and `oro-mf-cleanup-launchd`. Configuration and
invocation restriction land first; the second child enables the exact scope
kind in the shared renderer and consumes the restriction in the native runtime.

- Parent: `oro-mf-cleanup-runtime`.
- Read: cmd/oro/cmd_factory_launchd.go:validateFactoryJobScope; cmd/oro/cmd_factory_launchd_runtime.go:factoryLaunchdConfig; cmd/oro/cmd_factory_launchd_runtime_invoke_darwin.go:validateInvocation; cmd/oro/cmd_factory_docker_cleanup.go:newFactoryDockerCleanupCmd.
- Signature: validateFactoryCleanupInvocation(context.Context,factoryLaunchdConfig,factory.HostInvocation,factory.DockerCleanupPermit) error; cleanup-kind branch in factoryLaunchdRuntime.validateInvocation.
- Test: cmd/oro/cmd_factory_launchd_cleanup_darwin_test.go:TestFactoryLaunchdCleanup.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory ORO_FACTORY_DOCKER_ENDPOINT=unix:///Users/as21/.colima/oro-factory/docker.sock GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryLaunchdCleanup$' -count=1 -timeout=180s`.
- Assert / edges: Extend launchd scope validation only to exact cleanup kind and require trusted runtime-config path/digest in frozen configuration. Cleanup invocation is exact frozen Oro + factory _docker-cleanup --runtime-config canonicalpath --runtime-config-sha256 digest; canonical core permit stdin<=16KiB, Dir=StateRoot, Env PATH=/usr/bin:/bin LC_ALL=C LANG=C, OutputLimit=1MiB, exact permit Deadline. Read current reserved permit from authority before launch; current ordinary host/observer rules unchanged. Existing authenticated HELLO/GO/PUBLISHED and retirement must apply, including native late cleanup after work deadline within HardStop. Deny altered executable/args/env/cwd/output/deadline/stdin/config or target, no direct Docker or arbitrary late host invocation, no scope-kind fallback. Native proof late cleanup succeeds with exact current permit, final stopped forbids retry, revoked/foreign identities denied, outer process group positively retires. C helper only supervisor-owned after controller/host retirement; routine live-controller cleanup uses ordinary H before work deadline.
- Depends on: `oro-mf-cleanup-helper`, `oro-mf-host-scope`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-admission-view: Read verified immutable managed admission

- Parent: `oro-mf-cli-start`.
- Read: pkg/factory/managed_admission.go:loadManagedManifest/managedJournalIdentity/managedSnapshotSpec/existingManagedAdmission; pkg/factory/managed_store.go:openManagedJournal/mutate; pkg/factory/managed_reservation.go:ReleasableHostReservation; pkg/factory/store.go:factoryStore.load.
- Signature: type ManagedAdmissionView struct{StateRoot,ObjectiveID,PolicyHash,GitPath,GitSHA256,Repository,TargetRef string; Deadline,HardStop time.Time; CleanupTimeoutSeconds int; DockerContext,ImageDigest string}; ReadManagedAdmission(context.Context,string,string)(ManagedAdmissionView,error).
- Test: pkg/factory/managed_admission_view_test.go:TestManagedAdmissionView.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run 'TestManagedAdmissionView|TestManagedAdmission|TestManagedFileIdentity|TestManagedScopeJournal' -count=1 -timeout=180s`.
- Assert / edges: ReadManagedAdmission returns only scalar immutable producer inputs from positively committed existing admission: StateRoot, ObjectiveID, PolicyHash, GitPath, GitSHA256, Repository, TargetRef, Deadline, HardStop, CleanupTimeoutSeconds, DockerContext, ImageDigest. PolicyHash equals journal ManifestHash; GitPath is verified frozen objective/git, TargetRef exact refs/heads branch, ImageDigest preserved verbatim. Validate exact canonical root/id and bound whole verification to one30s context; load verified manifest/frozen artifacts without original paths, reopen existing managed journal only, compare exact managedJournalIdentity(manifest) and snapshot immutable spec/policy/CreatedAt/Deadline in one read-only ownership transaction. Success never authorizes service startup. marker-only/artifacts-published-before-rolled-back-admission must reject without creating state; absent/corrupt manifest, frozen programs, journal identity or snapshot identity reject. Removed original spec/programs still succeeds; repeated reads leave revisions/counters/grants/events/deadlines unchanged; expired/stopped committed admission stays readable; canceled context and alias root/artifact fail. No initialization, re-admission, Git execution, grants, epoch changes or invented Docker binary/endpoint/Oro identity; CLI pins those separately. Activation/abort fence remains separate and successful metadata verification never bypasses it. Only new managed_admission_view.go and _test.go.
- Depends on: `oro-mf-admission`, `oro-mf-file-identity`, `oro-mf-journal`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-docker-time: Canonicalize Docker creation timestamps

- Parent: `oro-mf-cleanup-runtime`.
- Read: pkg/factory/managed_cleanup.go:validateCleanupCreatedAt; pkg/factory/docker_cleanup_result.go:validateCleanupResultLifetime.
- Signature: CanonicalDockerCreatedAt(string)(string,error).
- Test: pkg/factory/docker_identity_test.go:TestCanonicalDockerCreatedAt.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run '^TestCanonicalDockerCreatedAt$' -count=1 -timeout=180s`.
- Assert / edges: Normalize daemon-created RFC3339 timestamps with valid numeric offsets into canonical UTC RFC3339Nano before binding storage identities. Exact observed native input 2026-10-24T01:49:04-04:00 becomes 2026-10-24T05:49:04Z; preserve nanosecond instant. Shared helper is available to CLI inspection and later storage producer. Persisted permits/results remain strict canonical UTC; no changes to their validators or authority. Bound raw input<=35 bytes, require strict RFC3339 lexical shape and at most9 fractional digits, valid offset hour/minute and calendar fields; reject whitespace/trailing data/loose hour syntax/comma fractions/excess precision/zero instant/out-of-range UTC year or malformed offset. Test Z, +00:00, positive/negative nonzero offsets, fractional zeros/precision, date rollover and limits. No clock/filesystem/daemon calls. Scope new `docker_identity.go` and `docker_identity_test.go` only.
- Depends on: `oro-mf-cleanup-authority`, `oro-mf-cleanup-result`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-docker-time-ingest: Normalize observed Docker lifetime identity

- Parent: `oro-mf-cleanup-runtime`.
- Read: cmd/oro/cmd_factory_docker_cleanup_inspect.go:parseFactoryDockerCleanupContainer/parseFactoryDockerCleanupVolume/matchFactoryDockerCleanupBinding; pkg/factory/docker_identity.go:CanonicalDockerCreatedAt.
- Signature: existing parseFactoryDockerCleanupInspection(factory.DockerCleanupPermit,factory.HostResult)(factory.DockerCleanupBinding,string,error).
- Test: cmd/oro/cmd_factory_docker_cleanup_plan_test.go:TestFactoryDockerCleanupCreationTime.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryDockerCleanupCreationTime$' -count=1 -timeout=180s`.
- Assert / edges: Normalize observed Docker container Created and volume CreatedAt with shared core CanonicalDockerCreatedAt before exact trusted-binding comparison and before returning discovery results. Stored permits still require canonical UTC; raw equivalent daemon offset must match the same immutable instant, different instants must fail. Native volume raw 2026-10-24T01:49:04-04:00 matches binding2026-10-24T05:49:04Z. Container/volume unknown inspection returns canonical discovered timestamp only; no relaxation of ID/name/labels/image/options/operation. Reproduce existing offset rejection RED. Test positive/negative offsets, fractional instants, date rollover, mismatched instant, malformed/overlong/overprecision values, and unknown-lifetime discovery for both kinds. No core permit-validator edits or execution/authority bypass. Helper's native fixture must use shared normalizer at producer ingestion and actual Docker volume cleanup must pass afterward as separate helper evidence. Scope existing inspect production and plan test only.
- Depends on: `oro-mf-docker-time`, `oro-mf-cleanup-plan`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-restriction: Bind cleanup invocations to frozen configuration

- Parent: `oro-mf-cleanup-wiring`.
- Read: cmd/oro/cmd_factory_launchd_runtime.go:factoryLaunchdConfig/validateFactoryLaunchdConfig; cmd/oro/cmd_factory_runtime_config_read.go:readFactoryRuntimeConfig; cmd/oro/cmd_factory_docker_cleanup_plan.go:validateFactoryDockerCleanupPlan; cmd/oro/cmd_factory_docker_cleanup.go:decodeFactoryDockerCleanupPermit.
- Signature: validateFactoryCleanupInvocation(context.Context,factoryLaunchdConfig,factory.HostInvocation,factory.DockerCleanupPermit) error; add RuntimeConfigPath,RuntimeConfigSHA256 string to factoryLaunchdConfig.
- Test: cmd/oro/cmd_factory_launchd_cleanup_test.go:TestFactoryCleanupInvocation.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryCleanupInvocation$' -count=1 -timeout=180s`.
- Assert / edges: Validate exactly the frozen Oro factory _docker-cleanup invocation against trusted runtime config and an already-authenticated canonical permit. Args are factory,_docker-cleanup,--runtime-config,<exactpath>,--runtime-config-sha256,<digest>; Dir=StateRoot; Env exactly PATH=/usr/bin:/bin,LC_ALL=C,LANG=C; OutputLimit=1048576; Stdin exactly canonical permit JSON <=16384bytes; Scope and Deadline equal permit. Read digest-bound artifact and compare StateRoot,ObjectiveID,OroPath/OroSHA256,GitPath/GitSHA256,Repository,TargetRef,HardStop,CleanupTimeoutSeconds to trusted launchd config; bind permit objective label/policy/context to artifact. This validator never grants authority, writes state, or executes. Both new config fields absent preserves ordinary host/observer constructors but cleanup validation denies; partial fields, noncanonical/outside objective runtime path, malformed digest reject. Exact path is StateRoot/ObjectiveID/runtime/runtime.json. Require current context/deadline and deadline<=HardStop; valid past work Deadline remains permitted within live cleanup deadline. Reject one-field mutations of every invocation/config/permit identity, duplicate/noncanonical/overlong stdin, wrong target/policy/context, changed artifact/binary/socket. Use existing strict permit validator; do not relax stored timestamps. Scope existing launchd_runtime.go plus new launchd_cleanup.go and one new test file. Shared renderer still rejects cleanup until follow-up wiring; no fake production caller or lint suppression.
- Depends on: `oro-mf-runtime-config`, `oro-mf-cleanup-helper`, `oro-mf-cleanup-authority`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cleanup-launchd: Execute authenticated cleanup through launchd

- Parent: `oro-mf-cleanup-wiring`.
- Read: cmd/oro/cmd_factory_launchd.go:validateFactoryJobScope/factoryJobPlist; cmd/oro/cmd_factory_launchd_runtime_invoke_darwin.go:validateInvocation/recordGrant; cmd/oro/cmd_factory_launchd_runtime_darwin.go:Invoke/Retire; cmd/oro/cmd_factory_launchd_cleanup.go:validateFactoryCleanupInvocation; cmd/oro/cmd_factory_launchd_runtime_darwin_test.go:factoryScopeSeedJournal; cmd/oro/cmd_factory_docker_cleanup_test.go:native fixture helpers.
- Signature: existing factoryLaunchdRuntime.validateInvocation(context.Context,factory.HostInvocation) error; existing validateFactoryJobScope(factory.ScopeHandle) error.
- Test: cmd/oro/cmd_factory_launchd_cleanup_darwin_test.go:TestFactoryLaunchdCleanup.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory ORO_FACTORY_DOCKER_ENDPOINT=unix:///Users/as21/.colima/oro-factory/docker.sock GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryLaunchdCleanup$' -count=1 -timeout=180s`.
- Assert / edges: Allow only exact cleanup kind/label through existing shared launchd scope validation, envelope/wrapper and retirement; no arbitrary kind fallback. Runtime validation explicitly switches host,observer,cleanup, reads current reserved CleanupPermit and calls exact cleanup restriction before any request/launch effects. Existing authenticated real HELLO/GO/PUBLISHED and positive PGID retirement govern cleanup. Native real frozen Oro + exact private runtime/Docker snapshot removes one owned unused tmpfs volume after work deadline and before HardStop; result ExitCode0 decodes with correct permit and MutationAcknowledged=true; foreign sentinel unchanged. Invoke returns only after cleanup scope quiescent and kill(-recordedPGID,0)==ESRCH. Unit validation denies absent/changed/stale/revoked/foreign permits and malformed kind/label before launchctl/request creation; ordinary host and observer behavior remains unchanged. Actual native test opt-in requires dedicated colima-oro-factory backend and fails if enabled but missing, no silently skipped required proof. Fixture may seed valid durable admitted journal/scope state as existing host tests, must disclose not public CLI/supervisor end-to-end. Assert stopped state and revoked/foreign replay denied before launch, no arbitrary late Docker/host commands, exact once-budgeted C only after controller/non-Docker retirement. Handshake adapter CleanupPermit remains denial; child reopens real durable authority. Scope only launchd.go, launchd_runtime_invoke_darwin.go and one new Darwin test file; request extra production edits before touching unrelated active files.
- Depends on: `oro-mf-cleanup-restriction`, `oro-mf-cleanup-helper`, `oro-mf-host-scope`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-transfer64: Bound full-tree host requests at 64 MiB

- Parent: `oro-mf-host-budget`.
- Read: pkg/factory/managed_host_limits.go:ManagedHostRetention,retainedHostBytes; pkg/factory/managed_host_limits_test.go:TestManagedHostRetention; cmd/oro/cmd_factory_host_exec.go:factoryHostExecRequestLimit.
- Signature: existing ManagedHostRequestBytes becomes `64<<20`; ManagedHostRetention retains its signature.
- Test: pkg/factory/managed_host_limits_test.go:TestManagedHostRetention.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run '^TestManagedHostRetention' -count=1`.
- Assert / edges: Tiny fixture minimum2,666,485,760 and Q5; archive49,545,216 passes and next byte rejects. Both request copies add exactly Q*64MiB over the previous cap. Source40MiB/files2200 fits; checked overflow and Docker-root1536 allowance remain. Ledger32MiB and database caps remain unchanged. Only the limits source and test files change in this slice. No old admission migration, reservation top-up or reset.
- Depends on: `oro-mf-host-limits`.

Old-admission core and ledger regressions, complete-envelope checks, native bulk transfer, failure retirement, symlink preservation and verified existing-blob reuse remain separate qualification requirements. This cap change alone does not establish full-tree autonomous delivery.

Component evidence at `e2ce78e9`: witnessed failures under the old32MiB cap, then author focused race1.320s, independent focused race1.304s, and existing host-wrapper race6.445s. All43 retention-helper statements covered; full factory-package lint reports zero issues. Independent source review passed. Native large-payload transfer and full-tree delivery remain unproven.

## oro-mf-source-links: Validate internal source symlinks

- Parent: `oro-factory-managed`.
- Read: pkg/factory/source_archive.go:sourceEntry,decodeSourceArchiveEntries,readSourceEntry,rejectSourceSpecialHeader,validateSourceMetadata,validateSourcePath,addSourcePath; pkg/factory/source_archive_test.go:candidateArchive,TestCandidateImportRejectsEscapes.
- Signature: func validateSourceLinks(entries []sourceEntry) error; existing archive decoder signatures unchanged.
- Test: pkg/factory/source_archive_test.go:TestSourceArchiveInternalLinks.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run '^Test(SourceArchiveInternalLinks|CandidateImport)' -count=1 -timeout=180s`.
- Assert / edges: Accept USTAR TypeSymlink only with Size0; return Mode0120000 and exact raw Linkname bytes as Data; both Oro ../../assets/hooks targets round-trip unchanged to existing regular targets; Validate the complete order-independent vector before returning it; raw target length counts toward total/per-file bytes and each link plus implicit parents counts toward MaxFiles; Allow only nonempty printable ASCII relative targets of at most100bytes; every raw non-final ordinary component must be an existing exact-case directory before any later .. pop; final target must be an existing100644/100755 entry; 100-byte valid target passes,101 fails; existing-dir/../file passes only with a real directory and regular endpoint; no host extraction or path-following occurs; Reject above-root, absolute/drive/backslash/NUL/empty/dot components, .git/reserved components, dangling/chained/cyclic/directory targets, symlink parents and case aliases; Reject nonexistent/../file, regular/../file, link/../file and wrong-case-dir/../file even when lexical cleaning yields a regular endpoint; Retain hardlink/PAX/GNU/sparse/device/FIFO/submodule rejection, nonzero link body refusal, malformed/trailing stream refusal and byte/node overflow checks.
- Depends on: `oro-mf-archive`.

This extends the original archive task's all-symlink rejection. Git and Docker codec integration remain separate tasks; this decoder alone does not establish full-tree delivery.

## oro-mf-host-old-core: Preserve old admission execution limits

- Parent: `oro-mf-host-budget`.
- Scope: pkg/factory/managed_host_compatibility_test.go.
- Test: pkg/factory/managed_host_compatibility_test.go:TestManagedHostOldReservation | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py transport-t1b go test -race ./pkg/factory -run '^TestManagedHostOldReservation$' -count=1 -timeout=180s | Assert: Create a valid admitted fixture with the same finite source/count policy under independently calculated B32 and B64, B32<B64; use source limits accepted by both caps and retain nonzero journal spend; ReadManagedAdmission succeeds without a backend invocation and leaves manifest/program hashes, exact reservation tuple, snapshots, revisions/events, spent counters and original deadlines unchanged; newManagedGitRepository rejects the B32 reservation before any H reservation, scope insertion or backend invocation; a matching B64 positive control succeeds; Supplying increased spec.HostRequiredBytes and tuple.Bytes to the existing objective cannot amend its marker or reset state
- Read: pkg/factory/managed_host_limits.go:ManagedHostRetention; pkg/factory/managed_admission_view.go:ReadManagedAdmission; pkg/factory/managed_admission.go:validateManagedManifest,validateHostReservation,ensureManagedMarker; pkg/factory/source_git.go:newManagedGitRepository; pkg/factory/managed_admission_test.go:managedAdmissionFixture; pkg/factory/managed_admission_view_test.go:testManagedAdmissionViewNoReset; pkg/factory/source_git_test.go:sourceGitRuntime
- Signature: func TestManagedHostOldReservation(t *testing.T); no production API change
- Edges: No migration, default activation, larger tuple or executable replacement; Construct old persisted data as a test fixture; do not bypass a production minimum check and claim a new admission succeeded; Metadata readability alone does not grant execution; overreserved old fixtures still do not prove mixed-binary protocol compatibility
- Depends on: `oro-mf-host-transfer64`, `oro-mf-admission-view`, `oro-mf-git-tree`.

## oro-mf-git-links: Preserve exact Git symlink objects

- Parent: `oro-factory-managed`.
- Scope: pkg/factory/source_git_transport.go, pkg/factory/source_git_objects.go, pkg/factory/source_git_test.go.
- Test: pkg/factory/source_git_test.go:TestCandidateInternalLinkExactTree | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py transport-t3 go test -race ./pkg/factory -run '^TestCandidateInternalLinkExactTree$' -count=1 -timeout=180s | Assert: On SHA1 and SHA256 private repositories, export both Oro-shaped links and their targets with exact paths/modes/raw bytes; rebuilt tree equals selected Git tree; Call the shared full-vector link validator during raw export reconstruction and candidate construction before object writes; supported safe link changes preserve their new raw bytes and exact tree; No-op commit returns exact base; hostile .gitattributes/export-ignore/export-subst and filters/hooks do not alter or execute during round-trip
- Read: pkg/factory/source_git_transport.go:decodeSourceListingEntry,export,commit,writeSourceTree; pkg/factory/source_git_objects.go:buildSourceTree,sourceObjectID,decodeSourceBatch; pkg/factory/source_symlink.go:validateSourceLinks; pkg/factory/source_git.go:invoke,managedGitEnvironment; pkg/factory/source_git_test.go:sourceGitFixture,testSourceGitExport,testSourceGitCommit
- Signature: Existing managedGitRepository.export/commit and buildSourceTree signatures unchanged; mode0120000 remains a leaf blob whose Data is raw target bytes
- Edges: Unsafe modified link, submodule160000, symlink chain/parent, wrong blob size/hash and malformed mode/listing fail; Unsafe candidate vector reaches no HostRuntime call or object write; do not drop unsupported paths; Preserve --no-replace-objects, no lazy fetch, original operation deadline and actual xcrun-resolved Git fixture on Darwin
- Depends on: `oro-mf-source-links`, `oro-mf-git-tree`.

## oro-mf-docker-links: Preserve Docker archive symlinks

- Parent: `oro-factory-managed`.
- Scope: pkg/factory/source_docker_archive.go, pkg/factory/source_docker_archive_test.go.
- Test: pkg/factory/source_docker_archive_test.go:TestDockerSourceArchive | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py transport-t4 go test -race ./pkg/factory -run '^TestDockerSourceArchive$' -count=1 -timeout=180s | Assert: Preflight complete vector using shared link rules before returning any seed or permitting caller resource allocation; Emit directories and regular files before links; links have USTAR TypeSymlink, Size0, exact Linkname and no body; decode returns120000/raw bytes; Raw target bytes count toward expanded/per-file quota despite Size0; check inode/path limits and raw plus Docker ./ USTAR representability; Round-trip actual captured Docker symlink-header shape without dereference/ownership rewrite; preserve one ./ root normalization
- Read: pkg/factory/source_docker_archive.go:encodeDockerSourceArchive,dockerSourceNodes,dockerSourceHeader,preflightDockerSourceHeader,decodeDockerSourceArchive; pkg/factory/source_symlink.go:validateSourceLinks; pkg/factory/source_archive.go:validateSourceHeader,decodeSourceArchiveEntries; pkg/factory/source_docker_archive_test.go:dockerArchiveLimits,dockerArchiveRoot
- Signature: Existing encodeDockerSourceArchive/decodeDockerSourceArchive signatures unchanged
- Edges: Reject link/data mismatch, nonzero body, PAX/long target, duplicate/colliding file-directory-link nodes, path/byte/inode overflow and unsafe target; Use no host extraction or Docker command in this codec leaf; storage integration owns fresh empty volume, cp without -L/-a, candidate quiescence and export; If a real Docker link capture is not yet available, root must schedule the bounded evidence capture separately; do not label a fabricated header as native evidence
- Depends on: `oro-mf-source-links`, `oro-mf-docker-archive`.

## oro-mf-git-presence: Reuse locally verified Git blob identities

- Parent: `oro-factory-managed`.
- Scope: pkg/factory/source_git_transport.go, pkg/factory/source_git_presence.go, pkg/factory/source_git_test.go.
- Test: pkg/factory/source_git_test.go:TestCandidateCommitExistingBlobs | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py transport-t5 go test -race ./pkg/factory -run '^TestCandidateCommitExistingBlobs$' -count=1 -timeout=180s | Assert: Compute deterministic unique candidate blob OIDs locally; one bounded cat-file --batch-check consumes the ordered full-width lowercase OID list before writes; Accept exactly one matching <oid> blob <canonical-decimal-size> newline or <oid> missing newline per input; skip writes only for exact validated present identity/type/size; For one missing blob over a many-blob base, commit spends exactly6 H tokens; k missing distinct blobs spends5+k; duplicates add no writes; no-op spends only existing base read; Missing objects use existing hash-object -w --stdin --no-filters and returned-OID comparison; mktree, commit parent/tree verification and later raw export remain exact
- Read: pkg/factory/source_git_transport.go:commit,writeSourceTree,export; pkg/factory/source_git_objects.go:buildSourceTree,sourceObjectID,decodeSourceBatch; pkg/factory/source_git.go:invoke,managedGitEnvironment; pkg/factory/managed_host.go:reserveManagedHostInvocation; pkg/factory/source_git_test.go:sourceGitRuntime,sourceGitFixture
- Signature: func decodeSourceBlobPresence(data []byte, oids []string, sizes []int64) ([]bool,error)
- Edges: Reject mismatched slice lengths, malformed/OID-width/type/size/order/duplicate/extra/truncated rows and invalid missing forms before writes; Replacement refs cannot redirect identities; correctly named but corrupted existing content must fail raw re-export hash verification and never gain an acceptance receipt or promotion; No fallback direct exec, no budget refund, no refreshed operation deadline; budget/deadline failure remains fail-closed; Presence metadata is not content verification; T7 must preserve fresh raw proof before acceptance/CAS
- Depends on: `oro-mf-git-links`.

## oro-mf-host-envelope64: Verify complete large request envelopes

- Parent: `oro-factory-managed`.
- Scope: cmd/oro/cmd_factory_host_envelope_test.go.
- Test: cmd/oro/cmd_factory_host_envelope_test.go:TestFactoryHostEnvelopeBoundaries | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py transport-t6a go test -race ./cmd/oro -run '^TestFactoryHostEnvelopeBoundaries$' -count=1 -timeout=180s | Assert: Build a canonical complete envelope of exactly64MiB and prove both encoder and bounded reader accept it; an otherwise valid64MiB+1 envelope is rejected; A raw byte payload above the former24,379,392-byte allowance round-trips canonically with exact bytes, including NUL/nonUTF8 in []byte stdin; Actual JSON/base64 and allowed string escaping count toward the cap; verify quotes/backslashes/control escaping and explicitly account for encoding/json behavior on invalid UTF8 string bytes
- Read: cmd/oro/cmd_factory_host_exec.go:encodeFactoryHostExecRequest,readFactoryHostExecRequest,readFactoryHostExecBytes,validateFactoryHostExecRequest; cmd/oro/cmd_factory_host_exec_test.go:factoryHostExecFixtureRequest,testFactoryHostExecSerialization; pkg/factory/managed_host_limits.go:ManagedHostRequestBytes
- Signature: func TestFactoryHostEnvelopeBoundaries(t *testing.T); no production API
- Edges: Wrong or truncated digest, duplicate/unknown/noncanonical JSON, metadata overflow and changed result.pending.json destination fail before execution; No launchd/native backend; valid/missing file ownership fixtures remain private and bounded; Do not expand production scope if a defect appears: report concrete RED to root for a separate repair leaf
- Depends on: `oro-mf-host-transfer64`, `oro-mf-host-exec`.

## oro-mf-host-bulk-native: Prove native large host transfer

- Parent: `oro-factory-managed`.
- Scope: cmd/oro/cmd_factory_host_bulk_darwin_test.go.
- Test: cmd/oro/cmd_factory_host_bulk_darwin_test.go:TestFactoryHostBulkNative | Cmd: GOTOOLCHAIN=go1.26.10 ORO_FACTORY_INTEGRATION=1 go test -race ./cmd/oro -run '^TestFactoryHostBulkNative$' -count=1 -timeout=180s | Assert: Darwin-only file and //go:build darwin; enabled integration fails if launchd, required Git/Oro artifacts or real capacity are unavailable; Use real serialized persistent allocation with a bounded Statfs-backed probe, Fresh/BudgetFresh, same filesystem and positive reserve margin; journal fixture binds the returned exact tuple and finite policy, not fake1TiB/1byte authority; Build/hash private Oro; resolve actual xcrun Git rather than copied /usr/bin/git launcher; real unmodified launchctl and wrapper transfer a32MiB deterministic binary blob inward via literal Git hash-object and outward via cat-file with exact SHA256 equality; Record actual request<=64MiB, streams/pending/transcript within derived caps, elapsed time/peak memory/retained bytes and original finite deadlines; authenticated PUBLISHED plus quiescent scope/PID=PGID>1/kill(-PGID,0)==ESRCH precedes success; Retire exact owned resources and release real tuple only through existing stopped+all-scopes-quiescent predicate; disclose that preallocated journal fixture proves transport, not public start
- Read: cmd/oro/cmd_factory_launchd_runtime_darwin.go:openFactoryLaunchdRuntime,Invoke,Retire; cmd/oro/cmd_factory_launchd_runtime_prepare_darwin.go:prepare; cmd/oro/cmd_factory_host_exec.go:executeFactoryHostCommand,publishFactoryHostResult; cmd/oro/cmd_factory_launchd_runtime_result.go:readFactoryRetiredResult; cmd/oro/cmd_factory_launchd_runtime_darwin_test.go:testFactoryHostScopeNative,factoryScopeSeedJournal,factoryScopeSeed; cmd/oro/cmd_factory_budget.go:admitFactoryHostBudget,releaseFactoryHostReservation; pkg/storage/runtime_budget.go:DiskProbe,CheckRuntimeBudget
- Signature: func TestFactoryHostBulkNative(t *testing.T); Provide test-only `newFactoryHostBulkFixture(t *testing.T) *factoryHostBulkFixture`; freeze its concrete runtime/config/tuple and scope-allocation/retirement helpers for T6c reuse; no production API
- Edges: No native sandbox workaround in production: run only approved direct scoped lane with private ORO_HOME/caches and unchanged HOME/CODEX_HOME; No new production payload/proxy or production fault environment; test fixture may seed valid admitted state as existing native scope tests; No scope deadline extension, unbounded binary reads, capacity bypass or release on uncertain retirement; enabled missing prerequisites are errors, not skips
- Depends on: `oro-mf-host-envelope64`, `oro-mf-host-scope`, `oro-mf-host-ledger`.

## oro-mf-host-bulk-faults: Retire failed large host transfers

- Parent: `oro-factory-managed`.
- Scope: cmd/oro/cmd_factory_host_bulk_failure_darwin_test.go.
- Test: cmd/oro/cmd_factory_host_bulk_failure_darwin_test.go:TestFactoryHostBulkFailureNative | Cmd: GOTOOLCHAIN=go1.26.10 ORO_FACTORY_INTEGRATION=1 go test -race ./cmd/oro -run '^TestFactoryHostBulkFailureNative$' -count=1 -timeout=180s | Assert: Darwin-only file and //go:build darwin; actual launchd cases cover cancellation while large bytes are transferring, raw output cap+1, and missing/late authenticated publication; Each case returns an error, no consumable successful result, and positive retirement of only its authenticated owned process group within original HardStop; Existing pending/request bytes remain bounded; file existence or partial output cannot substitute for PUBLISHED; retired resources preserve exactly spent H/tuple/deadline
- Read: cmd/oro/cmd_factory_host_bulk_darwin_test.go:newFactoryHostBulkFixture; cmd/oro/cmd_factory_launchd_runtime_darwin.go:Invoke,Retire,retirementObservation; cmd/oro/cmd_factory_launchd_runtime_invoke_darwin.go:perform,grant; cmd/oro/cmd_factory_launchd_runtime.go:receiveFactoryPublication,waitFactoryGroupEmpty; cmd/oro/cmd_factory_launchd_runtime_result.go:readFactoryRetiredResult; cmd/oro/cmd_factory_host_exec_test.go:testFactoryHostExecPendingHash,testFactoryHostExecOnePublished
- Signature: func TestFactoryHostBulkFailureNative(t *testing.T); consume T6b frozen test fixture without editing its file
- Edges: If positive retirement is unavailable, assert retained uncertainty/capacity rather than releasing it; Use owned fixture barriers/processes or existing test seams only; no production fault toggle, host-wide kill or changed launchctl implementation; If T6b fixture cannot express required fault without changing its file, stop and request a separate fixture seam; do not silently exceed one-test-file scope; Run approved direct scoped native lane; missing enabled dependencies fail
- Depends on: `oro-mf-host-bulk-native`.

## oro-mf-host-old-ledger: Preserve old ledger capacity identities

- Parent: `oro-mf-host-budget`.
- Scope: cmd/oro/cmd_factory_budget_compatibility_test.go.
- Test: cmd/oro/cmd_factory_budget_compatibility_test.go:TestFactoryHostBudgetOldReservation | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py transport-t6d go test -race ./cmd/oro -run '^TestFactoryHostBudgetOldReservation$' -count=1 -timeout=180s | Assert: Persist a canonical test-owned old exact-B32 ledger with B32<B64 for the identical finite policy; old admission attempt under new code fails before probe/ledger mutation; Committed ledger bytes, tuple, SpecSHA256, revision, bootstrap First and initialized authority remain identical; no new entry or release occurs; Raised input HostRequiredBytes=B64 cannot amend/reuse the old objective; released objective stays terminal; an independent fresh B64 objective with sufficient controlled probe succeeds as a positive control
- Read: cmd/oro/cmd_factory_budget.go:prepareFactoryBudgetSpec,admitFactoryHostBudgetWithHooks; cmd/oro/cmd_factory_budget_ledger.go:decodeFactoryBudgetSnapshot,objective,publish; cmd/oro/cmd_factory_budget_bootstrap.go:persist,recoverParentAuthority; cmd/oro/cmd_factory_budget_test.go:factoryBudgetFixture,testFactoryBudgetNoReset; pkg/factory/managed_host_limits.go:ManagedHostRetention
- Signature: func TestFactoryHostBudgetOldReservation(t *testing.T); no production API
- Edges: The old ledger is deliberately seeded persisted data, not evidence new production admission can bypass minimums; No top-up, reset, migration, changed ID adoption or absent-DB release; keep immutable canonical/bootstrap behavior; Test controlled probe counts are allowed here; actual capacity proof belongs T6b/T7
- Depends on: `oro-mf-host-transfer64`, `oro-mf-host-ledger`, `oro-mf-ledger-bootstrap`.

### Full-tree qualification refinement

The existing `oro-mf-qualification` acceptance remains intact. It additionally requires `TestFactoryQualificationFullOroTree` through the same qualification command.

- Preserve10 real objectives/3fixtures/all original fault and authority scenarios, actual binary/revision digest, independent promoted-artifact execution and integrated-main rerun; full-tree scenario adds evidence rather than replacing original acceptance.
- Pin complete committed Oro revision, enumerate every path/mode/blob including both raw mode120000 links and large binary, and use production export/seed/import throughout worker/check/review/final proof; no filters or subtree substitution.
- Use fresh empty owned source volume, no cp -L/-a, positive candidate retirement before export; exact expected tree equivalence before receipt/CAS; unsafe/corrupted candidate leaves origin/receipt unchanged.
- Perform real one-file model repair, checks, review and final fresh proof; derive feasible H/M/T/R/C and real capacity/reserve margin/outstanding reservations, assert actual journal spend and retained bytes stay within frozen budget.
- Prove resume uses verified originally frozen Oro/config/Git or rejects before new work; old readable metadata and an overreserved tuple never authorize mixed32/64MiB execution or replacement installed binary.
- No manual restart/cleanup/approval, no fake1TiB reservation, no skipped prerequisites; native transport/ownership/deadline failure remains bounded and retains uncertain reservations; foreign sentinels unchanged.

Additional dependencies: `oro-mf-host-old-core`, `oro-mf-source-links`, `oro-mf-git-links`, `oro-mf-docker-links`, `oro-mf-git-presence`, `oro-mf-host-envelope64`, `oro-mf-host-bulk-native`, `oro-mf-host-bulk-faults`, `oro-mf-host-old-ledger`, `oro-mf-host-transfer64`, `oro-mf-docker-storage`, `oro-mf-managed-delivery`, `oro-mf-acceptance`, `oro-mf-cli-start`.

## oro-mf-docker-links-native: Qualify native Docker source-link roundtrip

- Parent: `oro-factory-managed`.
- Test: pkg/factory/source_docker_links_native_test.go:TestDockerSourceLinksNative | Cmd: uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py docker-links-native env ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory ORO_FACTORY_DOCKER_ENDPOINT=unix:///Users/as21/.colima/oro-factory/docker.sock ORO_FACTORY_NATIVE_ARTIFACT_DIR=/private/tmp/oro-factory-verify-ldhnhnvl go test -race ./pkg/factory -run '^TestDockerSourceLinksNative$' -count=1 -timeout=180s -v | Assert: production encoder -> actual Docker cp stdin -> actual cp stdout -> production decoder preserves complete five-entry T4 vector, exact regular modes/binary bytes and all3 raw120000 link targets including assets/../assets; raw captured USTAR has ./root/prefix/Size0/Linkname/0777/UIDGID1000/noPAX. Retain actual export/header inventory/receipt. Pinned original Docker29.9.2 hash5682ae92519b289f0340461c963af2031e6c876c8ffb2a8b2459bfde6316bda0, explicit dedicated context/endpoint/privateconfig, cached immutable golang image, no pull/build. Fresh target tmpfs/finite sleep holder/sentinel; complete lifetime identity before cleanup, positive stopped/PID0/containerabsence/volumeabsence, sentinel unchanged before separate cleanup.
- Read: pkg/factory/source_docker_archive.go:encodeDockerSourceArchive,decodeDockerSourceArchive; pkg/factory/source_docker_archive_test.go:testDockerArchiveLinks; cmd/oro/cmd_factory_runtime_config.go:factoryDockerContextSnapshot; cmd/oro/cmd_factory_docker_cleanup_test.go native fixture patterns; /private/tmp/oro-factory-verify-ldhnhnvl/docker-links-native-task-2026-10-24.md (sealed exact fixture and bounds)
- Signature: func TestDockerSourceLinksNative(t *testing.T), Darwin-qualified newfile only.
- Edges: disabled integration skips beforeeffects; enabled missingbackend/image/pin fails. No-a/-L/syntheticoutput/hostextraction/ambientcontext; live stdout1MiB/stderr4KiB caps; <=32commands/work60s/percall10s/cleanup30s, joined children. Lostack/identitymismatch retains uncertainty and fails, never broadcleanup/retrycreate/absence-as-drain. This is nativecodec proof, not factory/storage/recovery qualification.
- Depends on: `oro-mf-source-links`, `oro-mf-docker-links`; `oro-mf-qualification` also depends on this capture.

## Managed Docker volume implementation children

The [reviewed volume plan](2026-10-24-managed-docker-volume-v1.md) pins the full
shared lifecycle, exact signatures, commands, tests and failure contracts. These
children extend `oro-mf-docker-storage`; its native acceptance remains unchanged.

| Task | Component | Dependencies |
| --- | --- | --- |
| `oro-mf-volume-intent` | Atomic usable v1 intents; replay cannot recreate missing authority | `oro-mf-journal`, `oro-mf-cleanup-runtime`, `oro-mf-cleanup-scope-filters` |
| `oro-mf-volume-adapter` | Private exact create/inspect calls through HostRuntime | `oro-mf-volume-intent`, `oro-mf-host-scope`, `oro-mf-docker-time` |
| `oro-mf-volume-config` | Verified immutable CLI configuration reader | `oro-mf-volume-adapter`, `oro-mf-admission-view`, `oro-mf-runtime-config` |
| `oro-mf-source-keeper-create` | Consume volume creation in journaled stopped-keeper creation and inspection | `oro-mf-volume-adapter`, `oro-mf-volume-config`, `oro-mf-watchdog` |
| `oro-mf-guard-image-build` | Produce the derived immutable watchdog image and bounded provenance | `oro-mf-watchdog` |
| `oro-mf-ready-helper-config` | Carry verified Oro identity to the readiness adapter | `oro-mf-volume-config` |
| `oro-mf-source-prepare` | READY, one-shot stdin-tar seed and keeper continuity | `oro-mf-source-keeper-create`, `oro-mf-ready-helper-config`, `oro-mf-docker-ready`, `oro-mf-docker-archive` |
| `oro-mf-guard-image-native` | Qualify the production builder and produced guard image | `oro-mf-guard-image-build`, `oro-mf-watchdog` |

Each child has one test file, bounded production scope and explicit accounting.
No generic acknowledgment API or second publication receipt protocol is required.
Unknown post-dispatch daemon work still requires a qualified recovery mechanism;
these components do not establish unattended storage or factory readiness.

The stopped-keeper contract is in the
[implementation plan](2026-10-24-managed-source-keeper.md). The image builder is
trusted bootstrap tooling, outside objective execution. Its protected tests do
not establish a native image build. READY, one-shot seeding, stopped export,
managed controller construction and actual daemon recovery remain required.
The accepted [source preparation plan](2026-10-24-managed-source-prepare.md)
connects the keeper and READY helper; native image qualification follows the
[bounded test contract](2026-10-24-guard-image-native.md).

## oro-mf-scope-integrity: Reject lifecycle fields in scope identity JSON

- Read: `managed_store.go`: `loadManagedScope`, `scopes`, `insertScope`, `RecordHello`, `Granted`, `Observe`, `MarkQuiescent`; `managed_cleanup.go`: `loadCleanupPermit`; `managed_reservation.go`: `readReservationScope`.
- Signature: preserve the existing loader and scope-list APIs; decode canonical `ScopeHandle + Deadline` separately from authoritative lifecycle columns.
- Test: `pkg/factory/managed_scope_integrity_test.go:TestManagedScopeIntegrity`.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run '^TestManagedScopeIntegrity$|^TestManagedScopeJournal$|^TestManagedCleanupAuthority$|^TestManagedDockerVolumeIntent$' -count=1 -timeout=180s` through the protected verification harness.
- Assert: injected JSON `Phase`, `PID` or `PGID` cannot override actual columns, grant execution, forge retirement or reopen a cleanup permit. Unknown/duplicate/noncanonical bodies and identity mismatches reject without mutation. Valid canonical scope lifecycles remain compatible.
- Edges: reproduce the malformed persisted-authority bypass before fixing; no candidate journal-write capability is claimed. Final reservation release already has a separate canonical reader. No schema reset, refund, migration or native action.
- Depends on: `oro-mf-journal`. The managed epic and `oro-mf-qualification` explicitly depend on this repair.

The volume-intent path already validates referenced records locally. This task
closes the shared loader and duplicated listing path used outside that component.

## oro-mf-cleanup-scope-filters: Validate scope records before cleanup exclusion

- Read: `managed_cleanup.go`: `requireNoManagedCleanup`, `requireCleanupHostQuiescence`, `loadCleanupPermit`; `managed_scope_decode.go`: `decodeManagedScopeRecord`.
- Signature: retain both exclusion APIs; scan bounded complete scope rows and decode them before applying kind, backend, phase or self predicates.
- Test: `pkg/factory/managed_cleanup_integrity_test.go:TestManagedCleanupScopeIntegrity`.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test -race ./pkg/factory -run '^TestManagedCleanupScopeIntegrity$|^TestManagedScopeIntegrity$|^TestManagedCleanupAuthority$|^TestManagedDockerVolumeIntent$' -count=1 -timeout=180s` through the protected harness.
- Assert: noncanonical cleanup kind cannot hide a live helper; mismatched backend cannot hide an unretired host at cleanup admission or permit/grant recheck. Rejection preserves all rows, tokens, permits and events. Canonical Docker targets, retired peers and except-self behavior remain valid.
- Edges: no candidate journal-write access claimed; no new schema, budget, lifecycle or native action. Validate excluded rows too.
- Depends on: `oro-mf-scope-integrity`, `oro-mf-cleanup-authority`. The managed epic and qualification explicitly depend on this repair.
