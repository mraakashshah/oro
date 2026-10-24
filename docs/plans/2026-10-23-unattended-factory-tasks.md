# Unattended factory task graph

Design: [unattended factory design](2026-10-23-unattended-factory-design.md), Gate 1 PASS. Gate 2 PASS after independent review and two contract repairs. Native parent: `oro-factory-managed`, under `oro-factory-autonomy`. All 25 children and explicit dependency edges are recorded.

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
    MarkQuiescent(context.Context, ScopeHandle) error
    Close() error
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
- Signature: ScopeHandle; ScopeAuthority.RecordHello/Granted/MarkQuiescent; managed journal transactions.
- Test: pkg/factory/managed_store_test.go:TestManagedScopeJournal.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./pkg/factory -run '^TestManagedScopeJournal$' -count=1 -timeout=180s`.
- Assert / edges: Reserve nonce before external effects; atomically compare generation/nonce/phase, reject revoked grants, count starts/calls once, immutable deadline, FULL synchronous persistence.
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

- Read: cmd/oro/cmd_factory_budget.go, cmd/oro/cmd_factory.go:newFactoryCmd, pkg/storage/runtime_budget.go:CheckRuntimeBudget.
- Signature: admitFactoryHostBudget(ctx context.Context,stateDir string,spec factory.ManagedSpec,probe storage.DiskProbe) (factory.HostReservation,error); releaseFactoryHostReservation(ctx context.Context,stateDir,id string) error.
- Test: cmd/oro/cmd_factory_budget_test.go:TestManagedAdmissionBudget.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestManagedAdmissionBudget$' -count=1 -timeout=180s`.
- Assert / edges: Admission boundary denies insufficient/probe-failed budget before scope/root/attempt; equality permits; concurrent admission cannot double-reserve; preserve reservations on uncertain cleanup; require same filesystem and conservative bounded-writer aggregate. Public CLI wiring/denial is verified by cli-start.; reservation survives admission-process exit; release reopens ledger by ID, CAS/idempotent, never a closure deferred by start.
- Depends on: `oro-mf-contract`, `oro-mf-admission`.

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
- Signature: runFactoryHostExec(ctx context.Context,request factory.HostInvocation,authority factory.ScopeAuthority) error.
- Test: cmd/oro/cmd_factory_host_exec_test.go:TestFactoryHostExec.
- Cmd: `GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryHostExec$' -count=1 -timeout=180s`.
- Assert / edges: No child or mutable work before authenticated GO; children inherit job PGID; bounded time/output/result serialization; atomic pending result; no host result accepted before scope retirement.
- Depends on: `oro-mf-host-job`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-host-scope: Retire launchd invocation scopes

- Read: cmd/oro/cmd_factory_launchd_runtime.go, cmd/oro/cmd_factory_host_exec.go, pkg/factory/managed_store.go.
- Signature: factory.HostRuntime.Invoke/Retire and factory.ScopeAuthority adapter.
- Test: cmd/oro/cmd_factory_launchd_runtime_test.go:TestFactoryHostScope.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryHostScope$' -count=1 -timeout=180s`.
- Assert / edges: Real temporary launchd jobs prove normal-exit descendant cleanup, timeout, lost bootstrap ack, revoked nonce rejection, real peer PID/UID validation and PGID ESRCH before result consumption; no print parsing or signalling stored PID.
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
- Depends on: `oro-mf-archive`, `oro-mf-host-scope`.

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
- Depends on: `oro-mf-journal`, `oro-mf-host-scope`, `oro-mf-archive`, `oro-mf-watchdog`.

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
- Depends on: `oro-mf-admission`, `oro-mf-git-tree`, `oro-mf-docker-scope`.

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

- Read: cmd/oro/cmd_factory.go:newFactoryCmd, cmd/oro/root.go:rootSubcommands, cmd/oro/cmd_factory_start.go, cmd/oro/cmd_factory_supervise.go.
- Signature: factory start strictspec + internal _controller/_supervise/_mcp commands.
- Test: cmd/oro/cmd_factory_start_test.go:TestFactoryManagedStart.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryManagedStart$' -count=1 -timeout=180s`.
- Assert / edges: ActualCLI wires budget->frozenadmission->ownedservice registration; controllerauthenticatedgate beforework; existingrun unchanged/rejectsmanaged; repeatedstartidempotent noreset; no legacy fixedlaunchdlabel/swarm; low disk or failed probe through actual public CLI creates no scope/controller/attempt; successful start return never releases active persistent host reservation.
- Depends on: `oro-mf-host-budget`, `oro-mf-host-scope`, `oro-mf-claude`, `oro-mf-supervisor`, `oro-mf-reservation-failure`.

Commit separately after targeted tests, lint, and review. No fake callers or check suppression.

## oro-mf-cli-control: Observe or stop managed objectives

- Read: cmd/oro/cmd_factory.go:newFactoryStatusCmd, cmd/oro/cmd_factory_control.go, pkg/factory/contract.go:Status, cmd/oro/supervisor.go.
- Signature: factory wait/stop/status and optional supervision status fields.
- Test: cmd/oro/cmd_factory_control_test.go:TestFactoryManagedControl.
- Cmd: `ORO_FACTORY_INTEGRATION=1 ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory GOTOOLCHAIN=go1.26.10 go test ./cmd/oro -run '^TestFactoryManagedControl$' -count=1 -timeout=180s`.
- Assert / edges: Statusreadonly truthful objective vsstopped supervision; waitzero onlycompleted; stopdurablerevocation and exactscope retirement/unregister; no unrelatedjob/processkill; unknownpromotion preserved; stop and terminal recovery reopen persisted reservation ledger and release once only after all scopes quiescent; cross-process start-exit/success/crash-before-release/uncertain-cleanup test proves capacity recovered or retained correctly.
- Depends on: `oro-mf-cli-start`.

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
