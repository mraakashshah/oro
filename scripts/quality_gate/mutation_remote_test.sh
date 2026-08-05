#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
readonly repo_root
readonly runner="$repo_root/scripts/quality_gate/mutation.sh"
tmp=""

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

dispatcher_supplements_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^dispatcher_test_supplement\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	bash -c "$function_source"$'\n''dispatcher_test_supplement "$1" "$2"' _ "$file" "$match"
}

assert_dispatcher_supplements() {
	local file="$1"
	local match="$2"
	shift 2
	local supplements expected
	supplements=$(dispatcher_supplements_for "$file" "$match")
	for expected in "$@"; do
		grep -Fxq "$expected" <<<"$supplements" ||
			fail "$file $match omitted mutation owner $expected"
	done
}

TestDispatcherMutationContractSupplements() {
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(executableAfterEpicSideEffects)$' \
		TestExecutableAfterEpicSideEffectsClassifiesNonEpicAndChildlessEpic \
		TestExecutableAfterEpicSideEffectsFailsClosedAndAuditsChildLookupError \
		TestExecutableAfterEpicSideEffectsProcessesAndReleasesDecomposedEpic \
		TestExecutableAfterEpicSideEffectsDoesNotProcessBlockedOrUnknownAdmission
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(filterAssignable)$' \
		TestFilterAssignableAppliesEveryDurableEligibilityStage
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(filterExecutableBeads)$' \
		TestFilterExecutableBeadsReturnsOnlyExecutableInputs
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(filterReviewCheckpointBlockedBeads)$' \
		TestFilterReviewCheckpointBlockedBeadsShortCircuitsEmptyAndNilDatabase \
		TestFilterReviewCheckpointBlockedBeadsFiltersAndAuditsExactRows \
		TestFilterReviewCheckpointBlockedBeadsFailsClosedAndRecordsObservation
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(reviewCheckpointBlockedBeads)$' \
		TestReviewCheckpointBlockedBeadsReturnsExactSetAndScanErrors
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(reviewCheckpointBlocksAssignment)$' \
		TestReviewCheckpointBlocksAssignmentHandlesNilDatabaseAndExactState \
		TestReviewCheckpointBlocksAssignmentReportsObservationFailure
	assert_dispatcher_supplements pkg/dispatcher/assignment_reconcile.go '^(tryRecoverExternalCloseWork)$' \
		TestTryRecoverExternalCloseWorkAuditsSuccessProof \
		TestTryRecoverExternalCloseWorkAuditsAndEscalatesFailureCause
	assert_dispatcher_supplements pkg/dispatcher/assignment_side_effect_admission.go '^(acquireAssignmentSideEffectAdmission)$' \
		TestAcquireAssignmentSideEffectAdmissionRejectsInvalidInputs \
		TestAcquireAssignmentSideEffectAdmissionPersistsOwnedToken \
		TestAcquireAssignmentSideEffectAdmissionBlocksAndAuditsReservedBead \
		TestAcquireAssignmentSideEffectAdmissionReportsStorageFailureAndObservation
	assert_dispatcher_supplements pkg/dispatcher/assignment_side_effect_admission.go '^(releaseAssignmentSideEffectAdmission)$' \
		TestReleaseAssignmentSideEffectAdmissionHandlesNilInputs \
		TestReleaseAssignmentSideEffectAdmissionDeletesOnlyOwnedToken \
		TestReleaseAssignmentSideEffectAdmissionAuditsStorageFailure
	assert_dispatcher_supplements pkg/dispatcher/assignment_side_effect_admission.go '^(clearStaleAssignmentSideEffectAdmissions)$' \
		TestClearStaleAssignmentSideEffectAdmissionsHandlesNilInputs \
		TestClearStaleAssignmentSideEffectAdmissionsRemovesAllRows \
		TestClearStaleAssignmentSideEffectAdmissionsReportsStorageFailure
	assert_dispatcher_supplements pkg/dispatcher/assignment_state.go '^(createAssignment)$' \
		TestCreateAssignmentReportsAdmissionFailure \
		TestCreateAssignmentPersistsExactIdentity \
		TestCreateAssignmentRejectsDurableCheckpointWithoutRow \
		TestCreateAssignmentFailsClosedWhenCheckpointObservationFails \
		TestCreateAssignmentRollsBackCommitFailure
	assert_dispatcher_supplements pkg/dispatcher/assignment_state.go '^(createAssignmentWithEvidence)$' \
		TestCreateAssignmentWithEvidenceReportsTargetResolutionFailure \
		TestCreateAssignmentWithEvidenceRejectsBlankTargetSHA \
		TestCreateAssignmentWithEvidenceReportsAdmissionFailure \
		TestCreateAssignmentWithEvidencePersistsTrimmedProof \
		TestCreateAssignmentWithEvidenceFailsClosedWhenCheckpointObservationFails \
		TestCreateAssignmentWithEvidenceRollsBackCommitFailure
	assert_dispatcher_supplements pkg/dispatcher/epic_branch_admission.go '^(renewEpicBranchAdmission)$' \
		TestEpicBranchAdmissionMutationRenewalOutcomes
	assert_dispatcher_supplements pkg/dispatcher/epic_branch_admission.go '^(isOwnedBlockedEpicBranchAdmission)$' \
		TestEpicBranchAdmissionMutationRenewalOutcomes
	assert_dispatcher_supplements pkg/dispatcher/epic_branch_admission.go '^(blockEpicBranchAdmission)$' \
		TestEpicBranchAdmissionMutationBlockOutcomes
	assert_dispatcher_supplements pkg/dispatcher/epic_branch_admission.go '^(withEpicBranchAdmission)$' \
		TestEpicBranchAdmissionMutationBypassAndClaimPreservation
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(createOpsRun)$' \
		TestOpsRunMutationLowLevelFailures
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(completeOpsRunFromStatus)$' \
		TestOpsRunMutationLowLevelFailures \
		TestOpsRunMutationExactReplayRequiresEveryField
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(terminalOpsRunResult)$' \
		TestOpsRunMutationTerminalResultMapping
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(supersedeOpsRunForRetry)$' \
		TestOpsRunMutationRetryNormalizesReplacement
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(reviewContextFromWorkerLocked)$' \
		TestOpsRunMutationReviewContextIdentity
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(reviewContextFromAnyWorkerLocked)$' \
		TestOpsRunMutationReviewContextIdentity
	assert_dispatcher_supplements pkg/dispatcher/ops_runs.go '^(watchReroutedOpsRunResult)$' \
		TestOpsRunMutationWatcherRejectsZeroIdentity
}

new_fixture() {
	local fixture="$1"
	mkdir -p "$fixture/bin" "$fixture/pkg/example"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	git -C "$fixture" add pkg/example/value.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$fixture/pkg/example/value.go"
	git -C "$fixture" add pkg/example/value.go
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

new_multi_fixture() {
	local fixture="$1"
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/pkg/other"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package other\n\nfunc Other() int { return 1 }\n' >"$fixture/pkg/other/other.go"
	git -C "$fixture" add pkg/example/value.go pkg/other/other.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$fixture/pkg/example/value.go"
	printf 'package other\n\nfunc Other() int { return 2 }\n' >"$fixture/pkg/other/other.go"
	git -C "$fixture" add pkg/example/value.go pkg/other/other.go
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

new_targeted_fixture() {
	local fixture="$1"
	local expanded="${2:-false}"
	local target="${3:-hooks}"
	mkdir -p "$fixture/bin" "$fixture/cmd/oro" "$fixture/pkg/dispatcher"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'module mutation.test/targeted\n\ngo 1.26\n' >"$fixture/go.mod"
	local base_parameters head_parameters head_test_name package_name source_file source_prefix test_file function_name test_name
	local -a test_names
	base_parameters=""
	head_parameters=""
	head_test_name=""
	source_prefix=""
	case "$target" in
	hooks)
		package_name=main
		source_file=cmd/oro/hooks.go
		test_file=cmd/oro/hooks_test.go
		function_name=isOroDistributedHook
		test_names=(TestIsOroDistributedHookRecognizesFastPrePush)
		;;
	init)
		package_name=main
		source_file=cmd/oro/cmd_init.go
		test_file=cmd/oro/cmd_init_test.go
		function_name=installAgentBranchGuard
		test_names=(TestInstallAgentBranchGuard)
		;;
	start)
		package_name=main
		source_file=cmd/oro/cmd_start.go
		test_file=cmd/oro/cmd_start_test.go
		function_name=hookPathsWouldLeak
		test_names=(
			TestHookPathsWouldLeak
			TestHookPathsWouldLeak_NonTmpdirSandboxRoot
			TestHookPathsWouldLeak_NonstandardGoTempRoot
			TestInstallCodexHookConfigRefusesLeakyHooks
		)
		;;
	scheduling)
		package_name=dispatcher
		source_file=pkg/dispatcher/scheduling.go
		test_file=pkg/dispatcher/scheduling_cursor_test.go
		function_name=advanceAssignedGeneralIdle
		base_parameters='idle []int'
		head_parameters='_ []int'
		source_prefix='func nextGeneralIdleIndex() int { return 0 }\n\n'
		test_names=(TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease)
		;;
	scheduling-launch)
		package_name=dispatcher
		source_file=pkg/dispatcher/scheduling.go
		test_file=pkg/dispatcher/scheduling_mutation_test.go
		function_name=launchAssignmentWithResult
		test_names=(TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound)
		head_test_name=TestTimedOutSetupCannotClobberReplacement
		;;
	sqlite-retry)
		package_name=dispatcher
		source_file=pkg/dispatcher/sqlite_busy_retry.go
		test_file=pkg/dispatcher/sqlite_busy_retry_test.go
		function_name=retrySQLiteBusyOperation
		test_names=(TestRetrySQLiteBusyOperation)
		head_test_name=TestTryAssign_ConcentratesWorkersOnTopEpic
		;;
	history)
		package_name=dispatcher
		source_file=pkg/dispatcher/startup_recovery.go
		test_file=pkg/dispatcher/review_lifecycle_test.go
		function_name=startupRecovery
		test_names=(TestExistingDispatcherBehavior)
		head_test_name=TestReviewCheckpointStartupOrdering
		;;
	assignment-claim)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_mutation_test.go
		function_name=assignBeadWithClaim
		test_names=(TestAssignBeadWithClaimReportsUnclaimedValidationFailure)
		head_test_name=TestEpicSchedulerDoesNotRefillAfterClaimedSetupCleansUp
		;;
	assignment-release)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_mutation_test.go
		function_name=releaseAssignmentReservation
		test_names=(TestReleaseAssignmentReservationResetsStateAndUnlocks)
		head_test_name=TestEpicSchedulerDoesNotRefillAfterClaimedSetupCleansUp
		;;
	escalation-one-shot)
		package_name=dispatcher
		source_file=pkg/dispatcher/escalation.go
		test_file=pkg/dispatcher/bounded_mutation_test.go
		function_name=spawnEscalationOneShot
		test_names=(TestSpawnEscalationOneShotReturnsAfterReadingWorktree)
		head_test_name=TestOneShotFailureCreatesOpsRunFailureWithoutManagerFallback
		;;
	health-apply)
		package_name=dispatcher
		source_file=pkg/dispatcher/health.go
		test_file=pkg/dispatcher/bounded_mutation_test.go
		function_name=applyHealth
		test_names=(TestApplyHealthReturnsAndReleasesDispatcherMutex)
		head_test_name=TestReadyObservationFailureBlocksAssignmentAndDegradesHealthAndStatus
		;;
	ops-review-context)
		package_name=dispatcher
		source_file=pkg/dispatcher/ops_runs.go
		test_file=pkg/dispatcher/bounded_mutation_test.go
		function_name=reviewContextForOpsRun
		test_names=(TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex)
		head_test_name=TestRouteOpsRunRestoresCheckpointLinkedToExactOpsRun
		;;
	*) fail "unknown targeted fixture: $target" ;;
	esac
	printf 'package %s\n\n%bfunc %s(%s) bool { return false }\n' \
		"$package_name" "$source_prefix" "$function_name" "$base_parameters" >"$fixture/$source_file"
	if [[ "$target" = history ]]; then
		printf '\nfunc removedStartupPath() bool { return false }\n' >>"$fixture/$source_file"
	fi
	if [[ "$expanded" = true ]]; then
		printf '\nfunc anotherHookDecision() bool { return false }\n' >>"$fixture/$source_file"
	fi
	printf 'package %s\n' "$package_name" >"$fixture/$test_file"
	for test_name in "${test_names[@]}"; do
		printf '\nfunc %s() {}\n' "$test_name" >>"$fixture/$test_file"
	done
	git -C "$fixture" add go.mod "$source_file" "$test_file"
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package %s\n\n%bfunc %s(%s) bool { return true }\n' \
		"$package_name" "$source_prefix" "$function_name" "$head_parameters" >"$fixture/$source_file"
	if [[ "$expanded" = true ]]; then
		printf '\nfunc anotherHookDecision() bool { return true }\n' >>"$fixture/$source_file"
	fi
	git -C "$fixture" add "$source_file"
	if [[ -n "$head_test_name" ]]; then
		printf '\nfunc %s() {}\n' "$head_test_name" >>"$fixture/$test_file"
		git -C "$fixture" add "$test_file"
	fi
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

new_function_history_fixture() {
	local fixture="$1"
	mkdir -p "$fixture/bin" "$fixture/pkg/dispatcher"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'package dispatcher\n\nfunc First() bool { return false }\n\nfunc Second() bool { return false }\n' >"$fixture/pkg/dispatcher/function_history.go"
	printf 'package dispatcher\n\nfunc TestExistingBehavior() {}\n' >"$fixture/pkg/dispatcher/function_history_test.go"
	git -C "$fixture" add pkg/dispatcher/function_history.go pkg/dispatcher/function_history_test.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package dispatcher\n\nfunc First() bool { return true }\n\nfunc Second() bool { return false }\n' >"$fixture/pkg/dispatcher/function_history.go"
	printf '\nfunc TestFirstOwner() {}\n' >>"$fixture/pkg/dispatcher/function_history_test.go"
	git -C "$fixture" add pkg/dispatcher/function_history.go pkg/dispatcher/function_history_test.go
	git -C "$fixture" commit -qm first
	printf 'package dispatcher\n\nfunc First() bool { return true }\n\nfunc Second() bool { return true }\n' >"$fixture/pkg/dispatcher/function_history.go"
	printf '\nfunc TestSecondOwner() {}\n' >>"$fixture/pkg/dispatcher/function_history_test.go"
	git -C "$fixture" add pkg/dispatcher/function_history.go pkg/dispatcher/function_history_test.go
	git -C "$fixture" commit -qm second
	printf 'package dispatcher\n\nfunc First() bool { return !false }\n\nfunc Second() bool { return true }\n' >"$fixture/pkg/dispatcher/function_history.go"
	printf '\nfunc TestFirstNewestOwner() {}\n' >>"$fixture/pkg/dispatcher/function_history_test.go"
	git -C "$fixture" add pkg/dispatcher/function_history.go pkg/dispatcher/function_history_test.go
	git -C "$fixture" commit -qm first-newest
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

write_fake_go() {
	local path="$1"
	cat >"$path" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" = tool && "$2" = go-mutesting && " $* " == *" --no-exec "* ]]; then
	source_file=${*: -1}
	generation=$(mktemp -d "${TMPDIR:-/tmp}/fake-mutants.XXXXXX")
	mkdir -p "$generation/$(dirname "$source_file")"
	cp "$source_file" "$generation/$source_file.original"
	sed '0,/return true/s//return false/' "$source_file" >"$generation/$source_file.0"
	sed '0,/return true/s//return !false/' "$source_file" >"$generation/$source_file.1"
	printf 'Save mutations into "%s"\n' "$generation"
	printf 'Save mutation into "%s" with checksum 0\n' "$generation/$source_file.0"
	printf 'Save mutation into "%s" with checksum 1\n' "$generation/$source_file.1"
	printf 'PARALLEL_WORKERS=%s\n' "${MUTATION_PARALLEL_WORKERS:-}" >>"${MUTATION_ARGS_TRACE:?}"
	if [[ -n "${MUTATION_TEST_FILE:-}" ]]; then
		printf 'MUTATION_TEST_FILE=%s\n' "$MUTATION_TEST_FILE" >>"${MUTATION_ARGS_TRACE:?}"
	fi
	exit 0
fi

if [[ "$1" = test ]]; then
	printf '%s\n' "$*" >>"${MUTATION_LIST_TRACE:?}"
	for arg in "$@"; do
		case "$arg" in
		-coverprofile=*) printf 'mode: set\n' >"${arg#-coverprofile=}" ;;
		esac
	done
	if [[ "$MUTATION_FIXTURE" != targeted-list-miss ]]; then
		case "$*" in
		*TestInstallAgentBranchGuard*) printf 'TestInstallAgentBranchGuard\n' ;;
		*TestHookPathsWouldLeak*) printf 'TestHookPathsWouldLeak\nTestHookPathsWouldLeak_NonTmpdirSandboxRoot\nTestHookPathsWouldLeak_NonstandardGoTempRoot\nTestInstallCodexHookConfigRefusesLeakyHooks\n' ;;
		*TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease*) printf 'TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease\n' ;;
		*TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound*) printf 'TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound\n' ;;
		*TestRetrySQLiteBusyOperation*) printf 'TestRetrySQLiteBusyOperation\n' ;;
		*TestReviewCheckpointStartupOrdering*) printf 'TestReviewCheckpointStartupOrdering\n' ;;
		*TestAssignBeadWithClaimReportsUnclaimedValidationFailure*) printf 'TestAssignBeadWithClaimReportsUnclaimedValidationFailure\n' ;;
		*TestReleaseAssignmentReservationResetsStateAndUnlocks*) printf 'TestReleaseAssignmentReservationResetsStateAndUnlocks\n' ;;
		*TestSpawnEscalationOneShotReturnsAfterReadingWorktree*) printf 'TestSpawnEscalationOneShotReturnsAfterReadingWorktree\n' ;;
		*TestApplyHealthReturnsAndReleasesDispatcherMutex*) printf 'TestApplyHealthReturnsAndReleasesDispatcherMutex\n' ;;
		*TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex*) printf 'TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex\n' ;;
		*TestFirstOwner*) printf 'TestFirstOwner\n' ;;
		*TestFirstNewestOwner*) printf 'TestFirstNewestOwner\n' ;;
		*TestSecondOwner*) printf 'TestSecondOwner\n' ;;
		*) printf 'TestIsOroDistributedHookRecognizesFastPrePush\n' ;;
		esac
	fi
	if [[ -n "${MUTATE_ORIGINAL:-}" ]]; then
		exit 1
	fi
	exit 0
fi

if [[ "$1" = tool && "$2" = cover ]]; then
	if [[ "$MUTATION_FIXTURE" != targeted-uncovered ]]; then
		printf 'pkg/dispatcher/startup_recovery.go:1: startupRecovery 100.0%%\n'
	fi
	printf 'pkg/dispatcher/scheduling.go:1: advanceAssignedGeneralIdle 100.0%%\n'
	printf 'pkg/dispatcher/scheduling.go:2: launchAssignmentWithResult 100.0%%\n'
	printf 'pkg/dispatcher/sqlite_busy_retry.go:1: retrySQLiteBusyOperation 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:1: assignBeadWithClaim 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:2: releaseAssignmentReservation 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:1: spawnEscalationOneShot 100.0%%\n'
	printf 'pkg/dispatcher/health.go:1: applyHealth 100.0%%\n'
	printf 'pkg/dispatcher/ops_runs.go:1: reviewContextForOpsRun 100.0%%\n'
	printf 'pkg/dispatcher/function_history.go:1: First 100.0%%\n'
	printf 'pkg/dispatcher/function_history.go:2: Second 100.0%%\n'
	printf 'pkg/example/value.go:1: Value 100.0%%\n'
	exit 0
fi

if [[ "$1" != tool || "$2" != go-mutesting ]]; then
	echo "unexpected go invocation: $*" >&2
	exit 64
fi

case "${MUTATION_FIXTURE:?}" in
pass)
	printf 'The mutation score is 0.90\ntotal is 10\n'
	;;
reversed)
	printf 'total is 10\nThe mutation score is 0.90\n'
	;;
intervening)
	printf 'The mutation score is 0.90\nmutation detail\ntotal is 10\n'
	;;
annotated)
	printf 'The mutation score is 1.000000 (38 passed, 0 failed, 2 duplicated, 0 skipped, total is 38)\n'
	;;
below)
	printf 'The mutation score is 0.40\ntotal is 10\n'
	;;
crash)
	printf 'go-mutesting crashed\n' >&2
	exit 23
	;;
zero)
	printf 'The mutation score is 0.00\ntotal is 0\n'
	exit 23
	;;
zero-clean)
	printf 'The mutation score is 0.00\ntotal is 0\n'
	;;
timeout)
	printf 'mutation timed out\n' >&2
	exit 124
	;;
unknown-exit)
	printf 'UNKOWN exit code for mutation test infrastructure failure\n'
	printf 'The mutation score is 1.000000 (1 passed, 0 failed, 0 duplicated, 0 skipped, total is 1)\n'
	;;
malformed)
	printf 'not a mutation report\n'
	;;
malformed-annotated)
	printf 'The mutation score is 1.000000 (38 passed, total is nope)\n'
	;;
aggregate | aggregate-below | aggregate-zero | shard-timeout)
	target=${*: -1}
	exec_arg=""
	match=""
	for arg in "$@"; do
		case "$arg" in
		--exec=*) exec_arg=${arg#--exec=} ;;
		--match=*) match=${arg#--match=} ;;
		esac
	done
	printf '%s\t%s\t%s\t%s\t%s\n' "$target" "$PWD" "${GOCACHE:-}" "$match" "$exec_arg" >>"${MUTATION_TRACE:?}"
	case "$target" in
	*pkg/example/value.go)
		sleep 0.2
		if [[ "$MUTATION_FIXTURE" = aggregate-zero ]]; then
			printf 'The mutation score is 0.000000 (0 passed, 0 failed, 0 duplicated, 0 skipped, total is 0)\n'
		elif [[ "$MUTATION_FIXTURE" = aggregate-below ]]; then
			printf 'The mutation score is 0.500000 (5 passed, 5 failed, 1 duplicated, 0 skipped, total is 10)\n'
		else
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 1 duplicated, 0 skipped, total is 10)\n'
		fi
		;;
	*pkg/other/other.go)
		if [[ "$MUTATION_FIXTURE" = shard-timeout ]]; then
			printf 'mutation timed out\n' >&2
			exit 124
		elif [[ "$MUTATION_FIXTURE" = aggregate-zero ]]; then
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 2 duplicated, 0 skipped, total is 10)\n'
		elif [[ "$MUTATION_FIXTURE" = aggregate-below ]]; then
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 2 duplicated, 0 skipped, total is 10)\n'
		else
			printf 'The mutation score is 0.600000 (6 passed, 4 failed, 2 duplicated, 0 skipped, total is 10)\n'
		fi
		;;
	*)
		echo "unexpected mutation target: $target" >&2
		exit 65
		;;
	esac
	;;
targeted | targeted-fallback | targeted-list-miss | targeted-timeout | targeted-uncovered)
	printf '%s\n' "$*" >>"${MUTATION_ARGS_TRACE:?}"
	if [[ -n "${MUTATION_TEST_FILE:-}" ]]; then
		printf 'MUTATION_TEST_FILE=%s\n' "$MUTATION_TEST_FILE" >>"${MUTATION_ARGS_TRACE:?}"
	fi
	if [[ "$MUTATION_FIXTURE" = targeted-timeout ]]; then
		printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
		printf 'UNKOWN exit code for targeted mutation test timeout\n'
	fi
	printf 'The mutation score is 1.000000 (1 passed, 0 failed, 0 duplicated, 0 skipped, total is 1)\n'
	;;
*)
	echo "unknown mutation fixture: $MUTATION_FIXTURE" >&2
	exit 64
	;;
esac
EOF
	chmod +x "$path"
}

run_function_history_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local base head evidence status args_trace list_trace
	mapfile -t refs < <(new_function_history_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	args_trace="$fixture/mutation-args.txt"
	list_trace="$fixture/mutation-list.txt"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" \
			MUTATION_ARGS_TRACE="$args_trace" MUTATION_LIST_TRACE="$list_trace" \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	if [[ "$status" != "$expected_exit" ]]; then
		cat "$fixture/runner.log" >&2
		fail "$outcome exit = $status, want $expected_exit"
	fi
	jq -e --arg status "$expected_status" '.conclusion == $status' "$evidence" >/dev/null ||
		fail "$outcome did not preserve its expected conclusion"
	printf '%s\n' "$evidence"
}

run_targeted_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local expanded="${5:-false}"
	local target="${6:-hooks}"
	local base head evidence status args_trace list_trace
	mapfile -t refs < <(new_targeted_fixture "$fixture" "$expanded" "$target")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	args_trace="$fixture/mutation-args.txt"
	list_trace="$fixture/mutation-list.txt"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" \
			MUTATION_ARGS_TRACE="$args_trace" MUTATION_LIST_TRACE="$list_trace" \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	if [[ "$status" != "$expected_exit" ]]; then
		cat "$fixture/runner.log" >&2
		fail "$outcome exit = $status, want $expected_exit"
	fi
	jq -e --arg status "$expected_status" '.conclusion == $status' "$evidence" >/dev/null ||
		fail "$outcome did not preserve its expected conclusion"
	printf '%s\n' "$evidence"
}

run_multi_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local base head evidence status trace
	mapfile -t refs < <(new_multi_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	trace="$fixture/mutation-trace.tsv"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" MUTATION_TRACE="$trace" \
			MUTATION_MAX_WORKERS=2 MUTATION_FILE_TIMEOUT_SECONDS=5 \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = "$expected_exit" ]] || fail "$outcome exit = $status, want $expected_exit"
	[[ -s "$evidence" ]] || fail "$outcome did not write evidence"
	jq -e \
		--arg base "$base" \
		--arg head "$head" \
		--arg status "$expected_status" \
		'.base == $base and .head == $head and .conclusion == $status and
		 .changed_files == ["pkg/example/value.go", "pkg/other/other.go"] and
		 [.shards[].file] == .changed_files' \
		"$evidence" >/dev/null || fail "$outcome evidence is missing deterministic shard identity"
	printf '%s\n' "$evidence"
}

TestStrictIncrementalMutationShards() {
	local evidence trace fixture

	fixture="$tmp/aggregate"
	evidence=$(run_multi_fixture "$fixture" aggregate pass 0)
	jq -e \
		'.mutation_exit_code == 0 and .score == 0.75 and .total == 20 and
		 [.shards[] | {conclusion, exit_code, passed, failed, duplicated, skipped, total}] ==
		 [{conclusion:"completed", exit_code:0, passed:9, failed:1, duplicated:1, skipped:0, total:10},
		  {conclusion:"completed", exit_code:0, passed:6, failed:4, duplicated:2, skipped:0, total:10}]' \
		"$evidence" >/dev/null || fail 'weighted shard aggregation did not preserve strict mutation counts'
	trace="$fixture/mutation-trace.tsv"
	[[ "$(wc -l <"$trace" | tr -d ' ')" = 2 ]] || fail 'each changed file must run exactly once'
	[[ "$(cut -f2 "$trace" | sort -u | wc -l | tr -d ' ')" = 2 ]] || fail 'mutation shards must use isolated worktrees'
	[[ "$(cut -f3 "$trace" | sort -u | wc -l | tr -d ' ')" = 2 ]] || fail 'mutation shards must use isolated Go build caches'
	! grep -q $'\t\t' "$trace" || fail 'mutation shard GOCACHE must be non-empty'
	[[ "$(cut -f4 "$trace" | sort | tr '\n' ' ')" = "^(Other)$ ^(Value)$ " ]] ||
		fail 'mutation shards must target only functions touched in each changed file'
	[[ "$(cut -f5 "$trace" | sort -u)" = "bash scripts/quality_gate/mutation_exec.sh" ]] ||
		fail 'every mutation shard must use the timeout-aware exec boundary'
	jq -e '[.shards[].match] == ["^(Value)$", "^(Other)$"]' "$evidence" >/dev/null ||
		fail 'mutation evidence must preserve each deterministic touched-function match'

	evidence=$(run_multi_fixture "$tmp/below" aggregate-below deterministic_failure 1)
	jq -e '.mutation_exit_code == 0 and .score == 0.7 and .total == 20' "$evidence" >/dev/null ||
		fail 'below-threshold aggregate was not kept distinct from infrastructure failure'

	evidence=$(run_multi_fixture "$tmp/timeout" shard-timeout infrastructure_failure 2)
	jq -e \
		'.mutation_exit_code == 124 and .score == null and .total == 0 and
		 [.shards[].conclusion] == ["completed", "infrastructure_failure"] and
		 .shards[1].exit_code == 124' \
		"$evidence" >/dev/null || fail 'per-file timeout did not preserve completed and infrastructure shard evidence'

	evidence=$(run_multi_fixture "$tmp/zero-shard" aggregate-zero pass 0)
	jq -e \
		'.mutation_exit_code == 0 and .score == 0.9 and .total == 10 and
		 .shards[0].conclusion == "no_mutation_sites" and
		 .shards[0].reason == "validated function target has no mutation sites" and
		 .shards[0].score == null and .shards[0].total == 0 and
		 .shards[1].conclusion == "completed" and .shards[1].total == 10' \
		"$evidence" >/dev/null || fail 'a validated zero-site function must remain visible without altering the scored denominator'
}

TestTargetedMutationScope() {
	local apply_health_pattern claim_pattern escalation_pattern evidence fixture args_trace history_pattern list_trace release_pattern review_context_pattern scheduling_pattern start_pattern
	fixture="$tmp/targeted"
	evidence=$(run_targeted_fixture "$fixture" targeted pass 0)
	args_trace="$fixture/mutation-args.txt"
	list_trace="$fixture/mutation-list.txt"
	grep -q -- '--exec=bash scripts/quality_gate/mutation_exec.sh' "$args_trace" ||
		fail 'bounded hook mutations must use the checked-in targeted exec boundary'
	grep -q -- '-list \^TestIsOroDistributedHook ./cmd/oro' "$list_trace" ||
		fail 'targeted mutation scope must preflight the exact package test pattern'
	jq -e '.shards[0].match == "^(isOroDistributedHook)$" and .shards[0].test_pattern == "^TestIsOroDistributedHook"' \
		"$evidence" >/dev/null || fail 'targeted mutation evidence must preserve function and test scope'

	evidence=$(run_targeted_fixture "$tmp/targeted-init" targeted pass 0 false init)
	grep -q -- '-list \^TestInstallAgentBranchGuard ./cmd/oro' "$tmp/targeted-init/mutation-list.txt" ||
		fail 'init guard mutations must preflight their exact direct test pattern'
	jq -e '.shards[0].match == "^(installAgentBranchGuard)$" and .shards[0].test_pattern == "^TestInstallAgentBranchGuard"' \
		"$evidence" >/dev/null || fail 'init guard mutation evidence must preserve function and test scope'

	start_pattern='^(TestHookPathsWouldLeak|TestHookPathsWouldLeak_NonTmpdirSandboxRoot|TestHookPathsWouldLeak_NonstandardGoTempRoot|TestInstallCodexHookConfigRefusesLeakyHooks)$'
	evidence=$(run_targeted_fixture "$tmp/targeted-start" targeted pass 0 false start)
	grep -Fq -- "-list $start_pattern ./cmd/oro" "$tmp/targeted-start/mutation-list.txt" ||
		fail 'start hook leak mutations must preflight the exact focused safety tests'
	jq -e --arg pattern "$start_pattern" \
		'.shards[0].match == "^(hookPathsWouldLeak)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'start hook leak mutation evidence must preserve function and focused test scope'

	scheduling_pattern='^TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease$'
	evidence=$(run_targeted_fixture "$tmp/targeted-scheduling" targeted pass 0 false scheduling)
	grep -Fq -- "-list $scheduling_pattern ./pkg/dispatcher" "$tmp/targeted-scheduling/mutation-list.txt" ||
		fail 'dispatcher scheduling mutations must preflight the exact idle-cursor regression test'
	jq -e --arg pattern "$scheduling_pattern" \
		'.shards[0].match == "^(advanceAssignedGeneralIdle)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'dispatcher scheduling mutation evidence must preserve function and exact regression test scope'

	launch_pattern='^TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound$'
	evidence=$(run_targeted_fixture "$tmp/targeted-scheduling-launch" targeted pass 0 false scheduling-launch)
	grep -Fq -- "-list $launch_pattern ./pkg/dispatcher" "$tmp/targeted-scheduling-launch/mutation-list.txt" ||
		fail 'launchAssignmentWithResult mutations must preflight the bounded claim-report contract'
	jq -e --arg pattern "$launch_pattern" \
		'.shards[0].match == "^(launchAssignmentWithResult)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'launchAssignmentWithResult mutations must select only the bounded claim-report contract'
	grep -Fxq 'MUTATION_TEST_FILE=pkg/dispatcher/scheduling_mutation_test.go' \
		"$tmp/targeted-scheduling-launch/mutation-args.txt" ||
		fail 'launchAssignmentWithResult mutations must compile only their standalone focused test file'

	sqlite_retry_pattern='^TestRetrySQLiteBusyOperation$'
	evidence=$(run_targeted_fixture "$tmp/targeted-sqlite-retry" targeted pass 0 false sqlite-retry)
	grep -Fq -- "-list $sqlite_retry_pattern ./pkg/dispatcher" "$tmp/targeted-sqlite-retry/mutation-list.txt" ||
		fail 'retrySQLiteBusyOperation mutations must preflight the direct retry contract'
	jq -e --arg pattern "$sqlite_retry_pattern" \
		'.shards[0].match == "^(retrySQLiteBusyOperation)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'retrySQLiteBusyOperation mutations must select only the direct retry contract'
	grep -Fxq 'MUTATION_TEST_FILE=pkg/dispatcher/sqlite_busy_retry_test.go' \
		"$tmp/targeted-sqlite-retry/mutation-args.txt" ||
		fail 'retrySQLiteBusyOperation mutations must compile only their standalone focused test file'

	history_pattern='^(TestReviewCheckpointStartupOrdering)$'
	evidence=$(run_targeted_fixture "$tmp/targeted-history" targeted pass 0 false history)
	grep -Fq -- "-list $history_pattern ./pkg/dispatcher" "$tmp/targeted-history/mutation-list.txt" ||
		fail 'dispatcher mutations must preflight tests co-changed with their production file'
	jq -e --arg pattern "$history_pattern" \
		'.shards[0].match == "^(startupRecovery)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'dispatcher mutation evidence must preserve its deterministic co-changed test scope'

	claim_pattern='^TestAssignBeadWithClaimReportsUnclaimedValidationFailure$'
	evidence=$(run_targeted_fixture "$tmp/targeted-assignment-claim" targeted pass 0 false assignment-claim)
	grep -Fq -- "-list $claim_pattern ./pkg/dispatcher" "$tmp/targeted-assignment-claim/mutation-list.txt" ||
		fail 'assignBeadWithClaim mutations must preflight the bounded callback contract'
	jq -e --arg pattern "$claim_pattern" \
		'.shards[0].match == "^(assignBeadWithClaim)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'assignBeadWithClaim mutations must select only the bounded callback contract'
	grep -Fxq 'MUTATION_TEST_FILE=pkg/dispatcher/assignment_mutation_test.go' \
		"$tmp/targeted-assignment-claim/mutation-args.txt" ||
		fail 'assignBeadWithClaim mutations must compile only their standalone focused test file'
	grep -Fxq 'PARALLEL_WORKERS=2' "$tmp/targeted-assignment-claim/mutation-args.txt" ||
		fail 'assignBeadWithClaim mutations must reserve exactly two mutant workers'

	release_pattern='^TestReleaseAssignmentReservationResetsStateAndUnlocks$'
	evidence=$(run_targeted_fixture "$tmp/targeted-assignment-release" targeted pass 0 false assignment-release)
	grep -Fq -- "-list $release_pattern ./pkg/dispatcher" "$tmp/targeted-assignment-release/mutation-list.txt" ||
		fail 'releaseAssignmentReservation mutations must preflight the bounded unlock contract'
	jq -e --arg pattern "$release_pattern" \
		'.shards[0].match == "^(releaseAssignmentReservation)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'releaseAssignmentReservation mutations must select only the bounded unlock contract'
	grep -Fxq 'MUTATION_TEST_FILE=pkg/dispatcher/assignment_mutation_test.go' \
		"$tmp/targeted-assignment-release/mutation-args.txt" ||
		fail 'releaseAssignmentReservation mutations must compile only their standalone focused test file'

	escalation_pattern='^TestSpawnEscalationOneShotReturnsAfterReadingWorktree$'
	evidence=$(run_targeted_fixture "$tmp/targeted-escalation-one-shot" targeted pass 0 false escalation-one-shot)
	grep -Fq -- "-list $escalation_pattern ./pkg/dispatcher" "$tmp/targeted-escalation-one-shot/mutation-list.txt" ||
		fail 'spawnEscalationOneShot mutations must preflight the bounded worktree-lock contract'
	jq -e --arg pattern "$escalation_pattern" \
		'.shards[0].match == "^(spawnEscalationOneShot)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'spawnEscalationOneShot mutations must select only the bounded worktree-lock contract'

	apply_health_pattern='^TestApplyHealthReturnsAndReleasesDispatcherMutex$'
	evidence=$(run_targeted_fixture "$tmp/targeted-health-apply" targeted pass 0 false health-apply)
	grep -Fq -- "-list $apply_health_pattern ./pkg/dispatcher" "$tmp/targeted-health-apply/mutation-list.txt" ||
		fail 'applyHealth mutations must preflight the bounded mutex-release contract'
	jq -e --arg pattern "$apply_health_pattern" \
		'.shards[0].match == "^(applyHealth)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'applyHealth mutations must select only the bounded mutex-release contract'

	review_context_pattern='^TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex$'
	evidence=$(run_targeted_fixture "$tmp/targeted-ops-review-context" targeted pass 0 false ops-review-context)
	grep -Fq -- "-list $review_context_pattern ./pkg/dispatcher" "$tmp/targeted-ops-review-context/mutation-list.txt" ||
		fail 'reviewContextForOpsRun mutations must preflight the bounded mutex-release contract'
	jq -e --arg pattern "$review_context_pattern" \
		'.shards[0].match == "^(reviewContextForOpsRun)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'reviewContextForOpsRun mutations must select only the bounded mutex-release contract'

	evidence=$(run_targeted_fixture "$tmp/targeted-history-uncovered" targeted-uncovered infrastructure_failure 2 false history)
	jq -e '.shards[0].reason == "targeted mutation tests do not cover every touched function"' \
		"$evidence" >/dev/null || fail 'an uncovered dispatcher mutation target must fail as infrastructure'

	evidence=$(run_targeted_fixture "$tmp/targeted-expanded" targeted-fallback pass 0 true)
	grep -q -- '--exec=bash scripts/quality_gate/mutation_exec.sh' "$tmp/targeted-expanded/mutation-args.txt" ||
		fail 'a full-package fallback must keep the timeout-aware exec boundary'
	jq -e '.shards[0].match == "^(anotherHookDecision|isOroDistributedHook)$" and .shards[0].test_pattern == ""' \
		"$evidence" >/dev/null || fail 'full-package fallback evidence must preserve the expanded function surface'

	evidence=$(run_targeted_fixture "$tmp/targeted-list-miss" targeted-list-miss infrastructure_failure 2)
	jq -e '.shards[0].reason == "targeted mutation test pattern matched no tests"' "$evidence" >/dev/null ||
		fail 'an empty targeted test scope must be an infrastructure failure'

	evidence=$(run_targeted_fixture "$tmp/targeted-timeout" targeted-timeout infrastructure_failure 2)
	jq -e '.mutation_exit_code == 124 and .shards[0].exit_code == 124' "$evidence" >/dev/null ||
		fail 'a targeted mutation test timeout must remain an infrastructure failure'

	evidence=$(run_function_history_fixture "$tmp/function-history" targeted pass 0)
	jq -e '
		.changed_files == ["pkg/dispatcher/function_history.go"] and
		[.shards[].file] == ["pkg/dispatcher/function_history.go", "pkg/dispatcher/function_history.go"] and
		[.shards[].match] == ["^(First)$", "^(Second)$"] and
		[.shards[].test_pattern] == ["^(TestFirstNewestOwner)$", "^(TestSecondOwner)$"] and
		([.shards[].match] | unique | length) == 2 and
		.score == 1 and .total == 2' \
		"$evidence" >/dev/null ||
		fail 'dispatcher files must split into complete, unique per-function shards with deterministic owners'
	[[ "$(wc -l <"$tmp/function-history/mutation-args.txt" | tr -d ' ')" = 2 ]] ||
		fail 'each touched dispatcher function must run exactly once'

	evidence=$(run_function_history_fixture "$tmp/function-history-timeout" targeted-timeout infrastructure_failure 2)
	jq -e '
		.mutation_exit_code == 124 and .score == null and .total == 0 and
		[.shards[].file] == ["pkg/dispatcher/function_history.go", "pkg/dispatcher/function_history.go"] and
		[.shards[].match] == ["^(First)$", "^(Second)$"] and
		[.shards[].conclusion] == ["infrastructure_failure", "infrastructure_failure"] and
		[.shards[].exit_code] == [124, 124]' \
		"$evidence" >/dev/null ||
		fail 'per-function dispatcher timeout must remain honest infrastructure with repeated-file identity'
}

run_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local base head evidence status
	mapfile -t refs < <(new_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence"
	)
	status=$?
	set -e
	[[ "$status" = "$expected_exit" ]] || fail "$outcome exit = $status, want $expected_exit"
	[[ -s "$evidence" ]] || fail "$outcome did not write evidence"
	jq -e \
		--arg base "$base" \
		--arg head "$head" \
		--arg status "$expected_status" \
		'.base == $base and .head == $head and .conclusion == $status and .changed_files == ["pkg/example/value.go"]' \
		"$evidence" >/dev/null || fail "$outcome evidence is missing exact refs, conclusion, or changed scope"
}

run_missing_base_fixture() {
	local fixture="$1"
	local head evidence status
	mapfile -t refs < <(new_fixture "$fixture")
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"

	set +e
	(
		cd "$fixture"
		bash "$runner" --base missing-base --head "$head" --evidence "$evidence"
	)
	status=$?
	set -e
	[[ "$status" = 2 ]] || fail "missing base exit = $status, want 2"
	jq -e --arg head "$head" \
		'.base == "missing-base" and .head == $head and .conclusion == "infrastructure_failure"' \
		"$evidence" >/dev/null || fail 'missing base did not emit infrastructure evidence'
}

test_mutation_exec_unexpected_exit() {
	local fixture="$1"
	local original="$fixture/original.go"
	local changed="$fixture/changed.go"
	local output="$fixture/exec.log"
	local status
	mkdir -p "$fixture/bin"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
printf 'synthetic go test infrastructure failure\n' >&2
exit 2
EOF
	chmod +x "$fixture/bin/go"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$original"
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$changed"

	set +e
	PATH="$fixture/bin:$PATH" \
		MUTATE_CHANGED="$changed" MUTATE_ORIGINAL="$original" MUTATE_PACKAGE=./pkg/example \
		MUTATE_TIMEOUT=5 MUTATION_TEST_PATTERN=TestValue \
		bash "$repo_root/scripts/quality_gate/mutation_exec.sh" >"$output" 2>&1
	status=$?
	set -e
	[[ "$status" = 2 ]] || fail "unexpected mutation test exit = $status, want 2"
	grep -q '^ORO_MUTATION_EXEC_FAILURE:2$' "$output" ||
		fail 'unexpected mutation test exit did not emit a durable infrastructure marker'
	grep -q '^synthetic go test infrastructure failure$' "$output" ||
		fail 'unexpected mutation test exit lost its diagnostic output'
	grep -q 'Value() int { return 1 }' "$original" ||
		fail 'mutation exec did not restore the original source after unexpected test exit'
}

test_mutation_exec_focused_file() {
	local fixture="$1"
	local original="$fixture/pkg/example/value.go"
	local changed="$fixture/changed.go"
	local output="$fixture/exec.log"
	local status
	mkdir -p "$fixture/bin" "$fixture/pkg/example"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"${MUTATION_FOCUSED_TRACE:?}"
exit 0
EOF
	chmod +x "$fixture/bin/go"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$original"
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$changed"
	printf 'package example\n\nfunc TestFocused() {}\n' >"$fixture/pkg/example/focused_test.go"
	printf 'package example\n\nfunc TestUnselected() {}\n' >"$fixture/pkg/example/unselected_test.go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FOCUSED_TRACE="$fixture/focused-args.txt" \
			MUTATE_CHANGED="$changed" MUTATE_ORIGINAL="$original" MUTATE_PACKAGE=./pkg/example \
			MUTATE_TIMEOUT=5 MUTATION_TEST_PATTERN=TestFocused \
			MUTATION_TEST_FILE=pkg/example/focused_test.go \
			bash "$repo_root/scripts/quality_gate/mutation_exec.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 1 ]] || fail "focused surviving mutant exit = $status, want 1"
	grep -q 'pkg/example/value.go' "$fixture/focused-args.txt" ||
		fail 'focused mutation compile omitted production source files'
	grep -q 'pkg/example/focused_test.go' "$fixture/focused-args.txt" ||
		fail 'focused mutation compile omitted its selected behavior test'
	! grep -q 'pkg/example/unselected_test.go' "$fixture/focused-args.txt" ||
		fail 'focused mutation compile included an unselected test file'
}

test_parallel_mutant_executor() {
	local fixture="$1"
	local output="$fixture/parallel.log"
	local status
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/scripts/quality_gate" \
		"$fixture/cache" "$fixture/tmp" "$fixture/state"
	cp "$repo_root/scripts/quality_gate/mutation_exec.sh" "$fixture/scripts/quality_gate/mutation_exec.sh"
	printf 'module example.test/parallel\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" = tool && "$2" = go-mutesting ]]; then
	source_file=${*: -1}
	generation="$MUTATION_FAKE_STATE/generated"
	mkdir -p "$generation/$(dirname "$source_file")"
	cp "$source_file" "$generation/$source_file.original"
	for value in 2 3 4; do
		sed "s/return 1/return $value/" "$source_file" >"$generation/$source_file.$((value - 2))"
	done
	printf 'Save mutations into %q\n' "$generation"
	printf 'Save mutation into %q with checksum aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' "$generation/$source_file.0"
	printf 'Save mutation into %q with checksum bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n' "$generation/$source_file.1"
	printf '%q is a duplicate, we ignore it\n' "$generation/$source_file.duplicate"
	printf 'Save mutation into %q with checksum cccccccccccccccccccccccccccccccc\n' "$generation/$source_file.2"
	exit 0
fi
if [[ "$1" != test ]]; then
	printf 'unexpected fake go invocation: %s\n' "$*" >&2
	exit 64
fi
slot=""
while [[ -z "$slot" ]]; do
	for candidate in 1 2; do
		if mkdir "$MUTATION_FAKE_STATE/slot-$candidate" 2>/dev/null; then
			slot=$candidate
			break
		fi
	done
done
trap 'rmdir "$MUTATION_FAKE_STATE/slot-$slot"' EXIT
if [[ -d "$MUTATION_FAKE_STATE/slot-1" && -d "$MUTATION_FAKE_STATE/slot-2" ]]; then
	: >"$MUTATION_FAKE_STATE/reached-two-workers"
fi
printf '%s\t%s\n' "$MUTATE_ORIGINAL" "$slot" >>"$MUTATION_FAKE_STATE/executions.tsv"
sleep 0.1
if grep -q 'return 3' "$MUTATE_ORIGINAL"; then
	exit 1
fi
exit 0
EOF
	chmod +x "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
			GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
			MUTATION_EXEC_TIMEOUT=5 MUTATION_PARALLEL_WORKERS=2 \
			bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 0 ]] || fail "parallel mutant executor exit = $status, want 0"
	grep -q '^The mutation score is 0.333333 (1 passed, 2 failed, 1 duplicated, 0 skipped, total is 3)$' "$output" ||
		fail 'parallel mutant aggregation diverged from its sequential-equivalent counts'
	[[ "$(wc -l <"$fixture/state/executions.tsv" | tr -d ' ')" = 3 ]] ||
		fail 'parallel mutant executor omitted or duplicated a frozen unique mutant'
	[[ "$(cut -f1 "$fixture/state/executions.tsv" | sort -u | wc -l | tr -d ' ')" = 2 ]] ||
		fail 'parallel mutant executor did not isolate source mutation across two checkouts'
	[[ -f "$fixture/state/reached-two-workers" ]] ||
		fail 'parallel mutant executor did not use both reserved workers'
	grep -q 'return 1' "$fixture/pkg/example/value.go" ||
		fail 'parallel mutant executor leaked a mutation into the source checkout'
}

run_parallel_capacity_fixture() {
	local fixture="$1"
	local mutant_count="$2"
	local expected_timeout="$3"
	local output="$fixture/parallel.log"
	local status
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/cache" "$fixture/tmp" "$fixture/state"
	printf 'module example.test/capacity\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" != tool || "$2" != go-mutesting ]]; then
	printf 'unexpected fake go invocation: %s\n' "$*" >&2
	exit 64
fi
source_file=${*: -1}
generation="$MUTATION_FAKE_STATE/generated"
mkdir -p "$generation/$(dirname "$source_file")"
cp "$source_file" "$generation/$source_file.original"
for ((index = 0; index < MUTATION_FAKE_MUTANT_COUNT; index++)); do
	sed "s/return 1/return $((index + 2))/" "$source_file" >"$generation/$source_file.$index"
	printf 'Save mutation into %q with checksum %032d\n' "$generation/$source_file.$index" "$index"
done
printf 'Save mutations into %q\n' "$generation"
EOF
	cat >"$fixture/bin/mutation-exec" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
	chmod +x "$fixture/bin/go" "$fixture/bin/mutation-exec"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
			MUTATION_FAKE_MUTANT_COUNT="$mutant_count" \
			GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
			MUTATION_EXEC_TIMEOUT=60 MUTATION_PARALLEL_WORKERS=2 \
			MUTATION_BASE_SHARD_TIMEOUT_SECONDS=240 MUTATION_MAX_SHARD_TIMEOUT_SECONDS=900 \
			MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
			bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 0 ]] || fail "parallel capacity fixture exit = $status, want 0"
	grep -Fxq "mutation shard capacity: mutants=$mutant_count workers=2 effective_timeout=${expected_timeout}s emergency_cap=900s" "$output" ||
		fail "$mutant_count-mutant shard did not report its deterministic effective deadline"
	grep -Eq "^The mutation score is 1\.000000 \($mutant_count passed, 0 failed, 0 duplicated, 0 skipped, total is $mutant_count\)$" "$output" ||
		fail "$mutant_count-mutant capacity fixture changed the mutation denominator"
}

run_parallel_marker_fixture() {
	local fixture="$1"
	local mode="$2"
	local expected_marker="$3"
	local output="$fixture/parallel.log"
	local elapsed_seconds peer_sleep=4 status
	[[ "$mode" != ordinary ]] || peer_sleep=1
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/cache" "$fixture/tmp" "$fixture/state"
	printf 'module example.test/markers\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
source_file=${*: -1}
generation="$MUTATION_FAKE_STATE/generated"
mkdir -p "$generation/$(dirname "$source_file")"
cp "$source_file" "$generation/$source_file.original"
for ((index = 0; index < 4; index++)); do
	sed "s/return 1/return $((index + 2))/" "$source_file" >"$generation/$source_file.$index"
done
printf 'Save mutations into %q\n' "$generation"
EOF
	cat >"$fixture/bin/mutation-exec" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$MUTATION_FAKE_MODE:${MUTATE_CHANGED##*.}" in
timeout:0)
	printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
	exit 124
	;;
unknown:0)
	printf 'UNKOWN exit code for synthetic mutation executor\n'
	exit 2
	;;
infra:0)
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
	;;
*:1)
	sleep "${MUTATION_FAKE_PEER_SLEEP:?}"
	exit 1
	;;
*)
	exit 0
	;;
esac
EOF
	chmod +x "$fixture/bin/go" "$fixture/bin/mutation-exec"

	SECONDS=0
	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
			MUTATION_FAKE_MODE="$mode" MUTATION_FAKE_PEER_SLEEP="$peer_sleep" \
			GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
			MUTATION_EXEC_TIMEOUT=60 MUTATION_PARALLEL_WORKERS=2 \
			MUTATION_BASE_SHARD_TIMEOUT_SECONDS=240 MUTATION_MAX_SHARD_TIMEOUT_SECONDS=900 \
			MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
			timeout 8 bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	elapsed_seconds=$SECONDS
	if [[ "$mode" = ordinary ]]; then
		[[ "$status" = 0 ]] || fail 'ordinary killed/survived statuses triggered fail-fast termination'
		grep -Eq '^The mutation score is 0\.750000 \(3 passed, 1 failed, 0 duplicated, 0 skipped, total is 4\)$' "$output" ||
			fail 'ordinary killed/survived statuses did not preserve the complete denominator'
		return
	fi
	[[ "$status" != 0 ]] || fail "$mode marker was accepted as a completed mutation campaign"
	grep -Fxq "$expected_marker" "$output" || fail "$mode marker was not surfaced"
	((elapsed_seconds < 3)) || fail "$mode marker waited ${elapsed_seconds}s for a sleeping peer"
}

test_parallel_marker_fail_fast() {
	local fixture="$1"
	run_parallel_marker_fixture "$fixture/timeout" timeout ORO_MUTATION_EXEC_TIMEOUT
	run_parallel_marker_fixture "$fixture/unknown" unknown 'UNKOWN exit code for synthetic mutation executor'
	run_parallel_marker_fixture "$fixture/infra" infra ORO_MUTATION_EXEC_FAILURE:2
	run_parallel_marker_fixture "$fixture/ordinary" ordinary ''
}

test_parallel_emergency_ceiling() {
	local fixture="$1"
	local base evidence head real_timeout status
	mapfile -t refs < <(new_targeted_fixture "$fixture" false assignment-claim)
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	real_timeout=$(command -v timeout)
	write_fake_go "$fixture/bin/go"
	cat >"$fixture/bin/timeout" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >>"${MUTATION_TIMEOUT_TRACE:?}"
exec "${MUTATION_REAL_TIMEOUT:?}" "$@"
EOF
	chmod +x "$fixture/bin/timeout"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE=targeted \
			MUTATION_ARGS_TRACE="$fixture/mutation-args.txt" \
			MUTATION_LIST_TRACE="$fixture/mutation-list.txt" \
			MUTATION_TIMEOUT_TRACE="$fixture/mutation-timeouts.txt" \
			MUTATION_REAL_TIMEOUT="$real_timeout" \
			MUTATION_FILE_TIMEOUT_SECONDS=240 MUTATION_MAX_SHARD_TIMEOUT_SECONDS=900 \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	if [[ "$status" != 0 ]]; then
		cat "$fixture/runner.log" >&2
		fail "capacity ceiling fixture exit = $status, want 0"
	fi
	grep -Fxq 900 "$fixture/mutation-timeouts.txt" ||
		fail 'parallel shard outer boundary did not reserve its 900s emergency ceiling'
	grep -Fxq 'mutation shard capacity: mutants=2 workers=2 effective_timeout=240s emergency_cap=900s' \
		"$fixture/runner.log" ||
		fail 'parallel shard reported its emergency ceiling as the effective small-shard deadline'
}

TestMutationCapacity() {
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	run_parallel_capacity_fixture "$tmp/small" 3 240
	run_parallel_capacity_fixture "$tmp/cold-190" 190 570
	run_parallel_capacity_fixture "$tmp/capped" 302 900
	test_parallel_marker_fail_fast "$tmp/markers"
	test_parallel_emergency_ceiling "$tmp/ceiling"
}

TestStrictIncrementalMutation() {
	local file_timeout_seconds incident_file_count job_timeout_minutes minimum_timeout_minutes mutation_batches workers
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	run_fixture "$tmp/pass" pass pass 0
	run_fixture "$tmp/intervening" intervening infrastructure_failure 2
	run_fixture "$tmp/reversed" reversed infrastructure_failure 2
	run_fixture "$tmp/below" below deterministic_failure 1
	run_fixture "$tmp/crash" crash infrastructure_failure 2
	run_fixture "$tmp/zero" zero infrastructure_failure 2
	run_fixture "$tmp/zero-clean" zero-clean pass 0
	jq -e \
		'.score == null and .total == 0 and .shards[0].conclusion == "no_mutation_sites" and
		 .shards[0].reason == "validated function target has no mutation sites"' \
		"$tmp/zero-clean/mutation-evidence.json" >/dev/null ||
		fail 'a validated all-zero-site campaign did not preserve explicit null-score evidence'
	run_fixture "$tmp/timeout" timeout infrastructure_failure 2
	test_mutation_exec_unexpected_exit "$tmp/exec-unexpected"
	test_mutation_exec_focused_file "$tmp/exec-focused"
	test_parallel_mutant_executor "$tmp/parallel-mutants"
	run_fixture "$tmp/unknown-exit" unknown-exit infrastructure_failure 2
	jq -e \
		'.score == null and .total == 0 and .mutation_exit_code == 2 and
		 .shards[0].reason == "mutation test execution returned an unexpected status"' \
		"$tmp/unknown-exit/mutation-evidence.json" >/dev/null ||
		fail 'unexpected mutation test exit did not invalidate the entire shard denominator'
	run_fixture "$tmp/malformed" malformed infrastructure_failure 2
	run_fixture "$tmp/malformed-annotated" malformed-annotated infrastructure_failure 2
	jq -e '.score == null and .total == 0' "$tmp/malformed-annotated/mutation-evidence.json" >/dev/null ||
		fail 'malformed annotated output was accepted as mutation evidence'
	run_fixture "$tmp/annotated" annotated pass 0
	jq -e '.score == 1 and .total == 38' "$tmp/annotated/mutation-evidence.json" >/dev/null ||
		fail 'annotated output did not preserve its score and total'
	run_missing_base_fixture "$tmp/missing-base"
	TestStrictIncrementalMutationShards
	TestTargetedMutationScope
	TestDispatcherMutationContractSupplements

	awk '
		/^  incremental-mutation:$/ { in_job = 1; next }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ { exit }
		in_job { print }
	' "$repo_root/.github/workflows/ci.yml" >"$tmp/incremental-mutation.yml"
	grep -q 'scripts/quality_gate/mutation.sh' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must run the strict mutation runner'
	grep -q 'actions/upload-artifact' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must upload its JSON evidence artifact'
	grep -q 'if-no-files-found: error' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation artifact loss must fail the job'
	grep -q 'MUTATION_MAX_WORKERS: 2' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation shard concurrency must match hosted runner capacity'
	grep -q 'MUTATION_FILE_TIMEOUT_SECONDS: 240' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must declare a per-file deadline'
	job_timeout_minutes=$(awk '/^[[:space:]]+timeout-minutes:/ { print $2; exit }' "$tmp/incremental-mutation.yml")
	workers=$(awk '/MUTATION_MAX_WORKERS:/ { print $2; exit }' "$tmp/incremental-mutation.yml")
	file_timeout_seconds=$(awk '/MUTATION_FILE_TIMEOUT_SECONDS:/ { print $2; exit }' "$tmp/incremental-mutation.yml")
	incident_file_count=24
	mutation_batches=$(((incident_file_count + workers - 1) / workers))
	minimum_timeout_minutes=$(((\
		mutation_batches * file_timeout_seconds + 10 * 60 + 59) / \
		60))
	[[ "$job_timeout_minutes" =~ ^[1-9][0-9]*$ ]] ||
		fail 'incremental-mutation job must have a numeric bounded outer deadline'
	((job_timeout_minutes >= minimum_timeout_minutes)) ||
		fail "incremental-mutation outer deadline must cover 24 shards at declared capacity plus 10 minutes overhead"
}

main() {
	case "${1:-}" in
	'' | TestStrictIncrementalMutation)
		TestStrictIncrementalMutation
		;;
	TestDispatcherMutationContractSupplements)
		TestDispatcherMutationContractSupplements
		;;
	TestMutationCapacity)
		TestMutationCapacity
		;;
	*)
		fail "unknown test $1"
		;;
	esac
}

main "$@"
