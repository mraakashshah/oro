#!/usr/bin/env bash
set -euo pipefail

readonly policy_score=0.75
readonly assignment_claim_shard_timeout=1800
mutation_script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly mutation_script_dir
mutation_shard_root=""
mutation_failure_evidence_root=""

cleanup_mutation_shards() {
	if [[ -n "$mutation_shard_root" ]]; then
		rm -rf -- "$mutation_shard_root"
	fi
}

usage() {
	printf 'Usage: %s --base <git-ref> --head <git-ref> --evidence <path>\n' "$0" >&2
	return 2
}

write_evidence() {
	local evidence="$1"
	local base="$2"
	local head="$3"
	local conclusion="$4"
	local exit_code="$5"
	local score="$6"
	local total="$7"
	shift 7
	local -a changed_files=("$@")

	mkdir -p "$(dirname "$evidence")"
	jq -n \
		--arg base "$base" \
		--arg head "$head" \
		--arg conclusion "$conclusion" \
		--argjson exit_code "$exit_code" \
		--argjson score "$score" \
		--argjson total "$total" \
		'{base: $base, head: $head, conclusion: $conclusion, mutation_exit_code: $exit_code, score: $score, total: $total, changed_files: $ARGS.positional, shards: []}' \
		--args "${changed_files[@]}" \
		>"$evidence"
}

write_sharded_evidence() {
	local evidence="$1"
	local base="$2"
	local head="$3"
	local conclusion="$4"
	local exit_code="$5"
	local score="$6"
	local total="$7"
	local shards="$8"
	shift 8
	local -a changed_files=("$@")

	mkdir -p "$(dirname "$evidence")"
	jq -n \
		--arg base "$base" \
		--arg head "$head" \
		--arg conclusion "$conclusion" \
		--argjson exit_code "$exit_code" \
		--argjson score "$score" \
		--argjson total "$total" \
		--argjson shards "$shards" \
		'{base: $base, head: $head, conclusion: $conclusion, mutation_exit_code: $exit_code, score: $score, total: $total, changed_files: $ARGS.positional, shards: $shards}' \
		--args "${changed_files[@]}" \
		>"$evidence"
}

infrastructure_failure() {
	local evidence="$1"
	local base="$2"
	local head="$3"
	local reason="$4"
	local exit_code="$5"
	shift 5
	local -a changed_files=("$@")

	printf 'infrastructure failure: %s\n' "$reason" >&2
	write_evidence "$evidence" "$base" "$head" infrastructure_failure "$exit_code" null 0 "${changed_files[@]}"
	return 2
}

write_shard_infrastructure() {
	local result="$1"
	local index="$2"
	local file="$3"
	local match="$4"
	local test_pattern="$5"
	local exit_code="$6"
	local reason="$7"

	jq -n \
		--argjson index "$index" \
		--arg file "$file" \
		--arg match "$match" \
		--arg test_pattern "$test_pattern" \
		--arg reason "$reason" \
		--argjson exit_code "$exit_code" \
		'{index: $index, file: $file, match: $match, test_pattern: $test_pattern, conclusion: "infrastructure_failure", exit_code: $exit_code, reason: $reason, score: null, passed: 0, failed: 0, duplicated: 0, skipped: 0, total: 0}' \
		>"$result"
}

write_shard_no_mutants() {
	local result="$1"
	local index="$2"
	local file="$3"
	local match="$4"
	local test_pattern="$5"

	jq -n \
		--argjson index "$index" \
		--arg file "$file" \
		--arg match "$match" \
		--arg test_pattern "$test_pattern" \
		'{index: $index, file: $file, match: $match, test_pattern: $test_pattern, conclusion: "completed", exit_code: 0, reason: "no mutants generated", score: null, passed: 0, failed: 0, duplicated: 0, skipped: 0, total: 0}' \
		>"$result"
}

write_shard_no_mutation_sites() {
	local result="$1"
	local index="$2"
	local file="$3"
	local match="$4"
	local test_pattern="$5"

	jq -n \
		--argjson index "$index" \
		--arg file "$file" \
		--arg match "$match" \
		--arg test_pattern "$test_pattern" \
		'{index: $index, file: $file, match: $match, test_pattern: $test_pattern, conclusion: "no_mutation_sites", exit_code: 0, reason: "validated function target has no mutation sites", score: null, passed: 0, failed: 0, duplicated: 0, skipped: 0, total: 0}' \
		>"$result"
}

write_shard_result() {
	local result="$1"
	local index="$2"
	local file="$3"
	local match="$4"
	local test_pattern="$5"
	local mutation_exit="$6"
	local output_file="$7"
	local output summary bare_record score total passed failed duplicated skipped
	output=$(<"$output_file")

	if ((mutation_exit != 0)); then
		local reason='mutation tool failed'
		if ((mutation_exit == 124)); then
			reason='mutation file deadline exceeded'
		fi
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" "$mutation_exit" "$reason"
		return
	fi
	if grep -q '^ORO_MUTATION_EXEC_TIMEOUT$' <<<"$output"; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 124 'targeted mutation test deadline exceeded'
		return
	fi
	if grep -Eq '^(ORO_MUTATION_EXEC_FAILURE:[0-9]+|UNKOWN exit code for )' <<<"$output"; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 \
			'mutation test execution returned an unexpected status'
		return
	fi

	summary=$(sed -nE '/^The mutation score is [0-9]+(\.[0-9]+)?[[:space:]]+\([0-9]+ passed, [0-9]+ failed, [0-9]+ duplicated, [0-9]+ skipped, total is [0-9]+\)[[:space:]]*$/p' <<<"$output" | tail -1)
	if [[ -n "$summary" ]]; then
		score=$(sed -nE 's/^The mutation score is ([0-9]+(\.[0-9]+)?).*$/\1/p' <<<"$summary")
		passed=$(sed -nE 's/^The mutation score is [^(]+\(([0-9]+) passed,.*/\1/p' <<<"$summary")
		failed=$(sed -nE 's/^The mutation score is [^(]+\([0-9]+ passed, ([0-9]+) failed,.*/\1/p' <<<"$summary")
		duplicated=$(sed -nE 's/^The mutation score is [^(]+\([0-9]+ passed, [0-9]+ failed, ([0-9]+) duplicated,.*/\1/p' <<<"$summary")
		skipped=$(sed -nE 's/^The mutation score is [^(]+\([0-9]+ passed, [0-9]+ failed, [0-9]+ duplicated, ([0-9]+) skipped,.*/\1/p' <<<"$summary")
		total=$(sed -nE 's/^The mutation score is [^(]+\(.*total is ([0-9]+)\)[[:space:]]*$/\1/p' <<<"$summary")
	else
		bare_record=$(awk '
			previous ~ /^The mutation score is [0-9]+(\.[0-9]+)?[[:space:]]*$/ &&
				$0 ~ /^total is [0-9]+[[:space:]]*$/ {
				print previous
				print
			}
			{ previous = $0 }
		' <<<"$output" | tail -2)
		score=$(sed -nE 's/^The mutation score is ([0-9]+(\.[0-9]+)?)[[:space:]]*$/\1/p' <<<"$bare_record")
		total=$(sed -nE 's/^total is ([0-9]+)[[:space:]]*$/\1/p' <<<"$bare_record")
		if [[ -n "$score" && -n "$total" ]]; then
			passed=$(awk "BEGIN { printf \"%.0f\", $score * $total }")
			failed=$((total - passed))
			duplicated=0
			skipped=0
		fi
	fi

	if [[ -z "${score:-}" || -z "${total:-}" || -z "${passed:-}" || -z "${failed:-}" ||
		-z "${duplicated:-}" || -z "${skipped:-}" || ! "$score" =~ ^[0-9]+(\.[0-9]+)?$ ||
		! "$total" =~ ^[0-9]+$ || ! "$passed" =~ ^[0-9]+$ || ! "$failed" =~ ^[0-9]+$ ||
		! "$duplicated" =~ ^[0-9]+$ || ! "$skipped" =~ ^[0-9]+$ ]]; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 0 'mutation output was malformed or absent'
		return
	fi
	if ((total == 0)); then
		write_shard_no_mutation_sites "$result" "$index" "$file" "$match" "$test_pattern"
		return
	fi

	jq -n \
		--argjson index "$index" \
		--arg file "$file" \
		--arg match "$match" \
		--arg test_pattern "$test_pattern" \
		--argjson score "$score" \
		--argjson passed "$passed" \
		--argjson failed "$failed" \
		--argjson duplicated "$duplicated" \
		--argjson skipped "$skipped" \
		--argjson total "$total" \
		'{index: $index, file: $file, match: $match, test_pattern: $test_pattern, conclusion: "completed", exit_code: 0, reason: "", score: $score, passed: $passed, failed: $failed, duplicated: $duplicated, skipped: $skipped, total: $total}' \
		>"$result"
}

touched_function_match() {
	local base="$1"
	local head="$2"
	local file="$3"
	local candidates head_functions touched_functions

	candidates=$(git diff --unified=0 "$base" "$head" -- "$file" 2>/dev/null |
		awk '
			function emit_function(line, name) {
				sub(/^.*func[[:space:]]+/, "", line)
				if (line ~ /^\(/) {
					sub(/^\([^)]*\)[[:space:]]+/, "", line)
				}
				name = line
				sub(/[^A-Za-z0-9_].*$/, "", name)
				if (name != "") {
					print name
				}
			}
			function flush_hunk(i) {
				if (declaration_count > 0) {
					for (i = 1; i <= declaration_count; i++) {
						emit_function(declarations[i])
					}
				} else if (hunk_label != "") {
					emit_function(hunk_label)
				}
				for (i in declarations) {
					delete declarations[i]
				}
				declaration_count = 0
				hunk_label = ""
			}
			/^@@/ {
				flush_hunk()
				hunk_label = $0
				next
			}
			/^[+-]func[[:space:]]/ {
				declarations[++declaration_count] = $0
			}
			END {
				flush_hunk()
			}
		' |
		sort -u)
	head_functions=$(git show "$head:$file" 2>/dev/null |
		awk '
			/^[[:space:]]*func[[:space:]]/ {
				line = $0
				sub(/^.*func[[:space:]]+/, "", line)
				if (line ~ /^\(/) {
					sub(/^\([^)]*\)[[:space:]]+/, "", line)
				}
				sub(/[^A-Za-z0-9_].*$/, "", line)
				if (line != "") print line
			}
		' |
		sort -u)
	touched_functions=$(
		while IFS= read -r candidate; do
			[[ -n "$candidate" ]] || continue
			if grep -Fxq "$candidate" <<<"$head_functions"; then
				printf '%s\n' "$candidate"
			fi
		done <<<"$candidates" |
			paste -sd'|' -
	)
	if [[ -n "$touched_functions" ]]; then
		printf '^(%s)$' "$touched_functions"
	fi
}

changed_dispatcher_test_names() {
	local commit="$1"
	local package_dir="$2"
	git diff --unified=0 "${commit}^" "$commit" -- "$package_dir/*_test.go" |
		awk '
				function emit_test(line, name) {
					sub(/^.*func[[:space:]]+/, "", line)
					name = line
					sub(/[^A-Za-z0-9_].*$/, "", name)
					if (name ~ /^Test/) {
						print name
					}
				}
				function flush_hunk(i) {
					if (declaration_count > 0) {
						for (i = 1; i <= declaration_count; i++) {
							emit_test(declarations[i])
						}
					} else if (hunk_label != "") {
						emit_test(hunk_label)
					}
					for (i in declarations) {
						delete declarations[i]
					}
					declaration_count = 0
					hunk_label = ""
				}
				/^@@/ {
					flush_hunk()
					hunk_label = $0
					next
				}
				/^\+func[[:space:]]+Test/ {
					declarations[++declaration_count] = $0
				}
				END {
					flush_hunk()
				}
			' |
		sort -u
}

cochanged_dispatcher_test_match() {
	local base="$1"
	local head="$2"
	local file="$3"
	local function="$4"
	local package_dir
	package_dir=$(dirname "$file")

	local test_names
	test_names=$(
		while read -r commit; do
			local commit_match commit_functions names
			commit_match=$(touched_function_match "${commit}^" "$commit" "$file")
			commit_functions=${commit_match#^(}
			commit_functions=${commit_functions%)$}
			if ! grep -Fxq "$function" < <(tr '|' '\n' <<<"$commit_functions"); then
				continue
			fi
			names=$(changed_dispatcher_test_names "$commit" "$package_dir")
			if [[ -n "$names" ]]; then
				printf '%s\n' "$names"
				break
			fi
		done < <(git log --no-merges --format=%H "$base..$head" -- "$file") |
			sort -u |
			paste -sd'|' -
	)
	if [[ -n "$test_names" ]]; then
		printf '^(%s)$' "$test_names"
	fi
}

review_checkpoint_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	[[ "$file" == pkg/dispatcher/review_checkpoint_store.go ]] || return
	case "$match" in
	'^(LoadOwningForBead)$' | '^(LoadForOpsRun)$')
		printf '^TestReviewCheckpointMutationOwnershipLoads$'
		;;
	'^(LoadForOpsRunOrBindLegacy)$' | '^(beginSerializedOwnershipBind)$' | '^(loadCheckpointForOpsRunTx)$' | \
		'^(bindLegacyCheckpointOwnership)$' | '^(legacyUnlinkedCheckpointIDs)$' | \
		'^(bindSingleLegacyCheckpoint)$' | '^(commitAbsentLegacyCheckpointOwnership)$')
		printf '^TestReviewCheckpointMutationLegacyBinding$'
		;;
	'^(ListPendingIntegrations)$' | '^(BeginIntegration)$' | '^(BlockIntegration)$')
		printf '^TestReviewCheckpointMutationIntegrationDurability$'
		;;
	esac
}

review_worker_lifecycle_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/directives.go:^(restartWorkerIfStillOnBead)$')
		printf '^TestRestartWorkerIfStillOnBeadAdmissionAndEffects$'
		;;
	'pkg/dispatcher/ops_runs.go:^(reviewContextFromAnyWorkerLocked)$' | \
		'pkg/dispatcher/ops_runs.go:^(reviewContextFromWorkerLocked)$')
		printf '^(TestOpsAuthoritativeSurvivorMutationReviewContexts|TestReviewContextWorkerIdentityMatrix|TestReviewReleaseTokenFencesDirectReviewTransitions)$'
		;;
	'pkg/dispatcher/qg_flow.go:^(withReservation)$')
		printf '^(TestReviewReleaseTokenFencesDirectReviewTransitions|TestWithReservationRevalidatesAfterUnlockedIO)$'
		;;
	'pkg/dispatcher/review.go:^(claimBlockedReviewAssignment)$' | \
		'pkg/dispatcher/review.go:^(claimReviewDependencyCheck)$' | \
		'pkg/dispatcher/review.go:^(reviewingWorkerMatches)$')
		printf '^(TestDirectReviewTransitionAdmissionMatrix|TestReviewReleaseTokenFencesDirectReviewTransitions)$'
		;;
	'pkg/dispatcher/review.go:^(reserveReviewRetryAttempt)$')
		printf '^(TestReserveReviewRetryAttemptOutcomes|TestReviewReleaseTokenFencesDirectReviewTransitions)$'
		;;
	'pkg/dispatcher/review.go:^(sendReviewApproved)$')
		printf '^TestReviewReleaseTokenFencesDirectReviewTransitions$'
		;;
	'pkg/dispatcher/review.go:^(sendPreReviewGitDirtyFeedback)$')
		printf '^TestSendPreReviewGitDirtyFeedbackRevalidatesAfterPayloadBuild$'
		;;
	'pkg/dispatcher/reconnect.go:^(replayReconnectEvents)$')
		printf '^TestReplayReconnectEventsSyntheticReadyMatrix$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(handleConn)$')
		printf '^TestHandleConnLifecycleMatrix$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(handleMessageUnchecked)$')
		printf '^TestHandleMessageUncheckedRoutingMatrix$'
		;;
	'pkg/dispatcher/review.go:^(beginReviewWorkerResult)$')
		printf '^(TestBeginReviewWorkerResultAdmission|TestCheckpointWorkerReleaseFenceRejectsConcurrentReviewResult)$'
		;;
	'pkg/dispatcher/review.go:^(handleReviewResultForAssignment)$')
		printf '^TestHandleReviewResultForAssignmentOutcomeMatrix$'
		;;
	'pkg/dispatcher/review_checkpoint_store.go:^(ReleaseWorker)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(releaseWorkerWithHook)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(runReviewCheckpointWorkerReleaseHook)$')
		printf '^TestReviewCheckpointStoreReleaseWorkerAtomicity$'
		;;
	'pkg/dispatcher/review_checkpoint_store.go:^(loadReviewCheckpointWorkerReleaseTarget)$')
		printf '^(TestReviewCheckpointStoreReleaseWorkerAtomicity|TestReviewCheckpointWorkerReleaseTargetFailuresAndZeroAssignment)$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(releaseCheckpointOwnedWorker)$')
		printf '^TestReleaseCheckpointOwnedWorkerProductionPath$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(releaseCheckpointOwnedWorkerUsing)$')
		printf '^TestReleaseCheckpointOwnedWorkerUsingUnlocksAllPaths$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(releaseCheckpointOwnedWorkerGeneration)$')
		printf '^(TestReviewWorkerDisconnectDurablyReleasesCurrentCheckpoint|TestReviewWorkerDisconnectReleaseFailurePreservesWorker)$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(releaseCheckpointOwnedWorkerGenerationUsing)$')
		printf '^TestReleaseCheckpointOwnedWorkerRejectsStaleConnectionGeneration$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(releaseCheckpointOwnedWorkerGenerationWithActionUsing)$')
		printf '^TestCheckpointWorkerReleasePanicCleanup$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(runCheckpointWorkerReleaseLease)$')
		printf '^(TestCheckpointWorkerReleaseContextCancelClearsOwnedDrain|TestCheckpointWorkerReleasePanicCleanup|TestCheckpointWorkerReleaseRunLeaseFailureAndStaleMatrix|TestCheckpointWorkerReleaseShutdownAbortsBeforeStore|TestReleaseCheckpointOwnedWorkerDurableBeforeMemory)$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(finalizeDurable)$')
		printf '^TestCheckpointWorkerReleaseFinalizeDurableMatrix$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(acquireCheckpointWorkerRelease)$')
		printf '^TestAcquireCheckpointWorkerReleaseAdmission$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(acquireCheckpointWorkerReleaseLocked)$')
		printf '^(TestCheckpointWorkerReleaseFenceTokenCannotBeClearedBySecondRelease|TestCheckpointWorkerReleaseLeaseAdmission)$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(current)$')
		printf '^TestCheckpointWorkerReleaseLeaseCurrentMatrix$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(waitForMessages)$')
		printf '^(TestCheckpointWorkerReleaseContextCancelClearsOwnedDrain|TestCheckpointWorkerReleaseShutdownAbortsBeforeStore|TestCheckpointWorkerReleaseWaitCurrentFence|TestCheckpointWorkerReleaseWaitsForAllInFlightMessages)$'
		;;
	'pkg/dispatcher/review_worker_release.go:^(abort)$')
		printf '^TestCheckpointWorkerReleaseAbortOwnershipMatrix$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(beginReviewWorkerMessage)$')
		printf '^TestCheckpointWorkerReleaseWaitsForAllInFlightMessages$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(finishReviewWorkerMessage)$')
		printf '^TestFinishReviewWorkerMessageDrainMatrix$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(connCloseCleanup)$')
		printf '^(TestConnCloseCleanupAdmissionAndEffects|TestReviewWorkerDisconnectDurablyReleasesCurrentCheckpoint|TestReviewWorkerDisconnectReleaseFailurePreservesWorker|TestReviewWorkerDisconnectStaleConnectionPreservesReplacement|TestReviewWorkerDisconnectStaleConnectionPreservesSamePointerReconnect)$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(checkpointOwnedWorkerForConnection)$')
		printf '^(TestReviewWorkerDisconnectDurablyReleasesCurrentCheckpoint|TestReviewWorkerDisconnectReleaseFailurePreservesWorker|TestReviewWorkerDisconnectStaleConnectionPreservesReplacement|TestReviewWorkerDisconnectStaleConnectionPreservesSamePointerReconnect)$'
		;;
	'pkg/dispatcher/startup_recovery.go:^(handleMessageFromConnection)$')
		printf '^(TestCheckpointWorkerReleaseFenceRejectsReconnectAndMessages|TestHandleMessageFromConnectionRoutesAcceptedMessage)$'
		;;
	'pkg/dispatcher/worker_pool.go:^(registerWorkerWithProtocol)$')
		printf '^(TestRegisterWorkerWithProtocolMutationCoverage|TestRegisterWorkerWithProtocolReleasesMutexBoundedMutation)$'
		;;
	'pkg/dispatcher/worker_pool.go:^(upsertWorker)$')
		printf '^TestCheckpointWorkerReleaseFenceRejectsReconnectAndMessages$'
		;;
	'pkg/dispatcher/worker_directives.go:^(applyKillWorker)$')
		printf '^TestApplyKillWorkerAdmissionAndEffects$'
		;;
	'pkg/dispatcher/worker_directives.go:^(applyRestartWorker)$')
		printf '^(TestApplyRestartWorkerAdmissionAndEffects|TestApplyRestartWorkerFailureAndRetryEffects)$'
		;;
	'pkg/dispatcher/worker_directives.go:^(killCheckpointOwnedWorker)$' | \
		'pkg/dispatcher/worker_directives.go:^(killCheckpointOwnedWorkerUsing)$')
		printf '^(TestReviewWorkerDirectiveReleaseFailureDoesNotFallBack|TestReviewWorkerDirectivesDurablyReleaseCheckpoint)$'
		;;
	'pkg/dispatcher/worker_directives.go:^(restartCheckpointOwnedWorker)$')
		printf '^(TestReviewWorkerDirectivesDurablyReleaseCheckpoint|TestReviewWorkerRestartActionErrorsStillFinalizeDurableRelease|TestReviewWorkerRestartFenceSpansStoreKillAndSpawn)$'
		;;
	'pkg/dispatcher/worker_directives.go:^(restartCheckpointOwnedWorkerUsing)$')
		printf '^(TestRestartCheckpointOwnedWorkerUsingBoundedMutation|TestRestartCheckpointOwnedWorkerUsingMutationCoverage)$'
		;;
	'pkg/dispatcher/worker_pool.go:^(registerWorker)$')
		printf '^TestSpawnFor_StopCleanupBeforeReconnectPreservesShutdownState$'
		;;
	'pkg/dispatcher/worker_pool.go:^(releaseReviewWorkerAfterSendFailure)$' | \
		'pkg/dispatcher/worker_pool.go:^(sendToWorker)$')
		printf '^(TestReviewSendFailureDefersReleaseUntilCurrentMessageExits|TestReviewWorkerSendFailureDurablyReleasesBeforeFallback|TestReviewWorkerSendFailurePreservesSamePointerReconnect|TestReviewWorkerSendFailureReleaseFailurePreservesMemory|TestReviewWorkerSendFailureStaleGenerationPreservesReplacement|TestReviewWorkerSynchronousSendReleasePanicRestoresCallerLock)$'
		;;
	'pkg/dispatcher/worker_pool.go:^(releaseReviewWorkerAfterSendFailureUsing)$')
		printf '^(TestReleaseReviewWorkerAfterSendFailureUsingBoundedMutation|TestReleaseReviewWorkerAfterSendFailureUsingMutationCoverage)$'
		;;
	'pkg/dispatcher/worker_pool.go:^(removeDeadWorkersLocked)$')
		printf '^TestCheckHeartbeats_RemovesDeadBusyWorker$'
		;;
	'pkg/dispatcher/worker_pool.go:^(removeStoppedSpawnForWorkersLocked)$')
		printf '^TestSpawnFor_StoppedWorkerHeartbeatTimeoutDoesNotEscalateCrash$'
		;;
	'pkg/dispatcher/worker_pool.go:^(removeStuckWorkersLocked)$')
		printf '^TestCheckHeartbeats_DetectsStuckWorker$'
		;;
	'pkg/dispatcher/worker_pool.go:^(sendShutdownToConnectionWithoutBuffering)$')
		printf '^TestReviewWorkerDirectivesDurablyReleaseCheckpoint$'
		;;
	esac
}

assignment_bc_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	[[ "$file" == pkg/dispatcher/assignment.go ]] || return 0
	case "$match" in
	'^(prepareAssignmentWorktree)$')
		printf '^TestAssignmentBCPrepareWorktreeOutcomes$'
		;;
	'^(validateExistingWorktreeForReuse)$')
		printf '^(TestAssignmentBCValidateDivergedRecoveryOutcomes|TestAssignmentBCValidateCurrentBranchError)$'
		;;
	'^(releaseAssignmentReservationLocked)$')
		printf '^TestAssignmentBCReservationReleaseExactState$'
		;;
	'^(attachAssignmentToReservation)$')
		printf '^TestAssignmentBCAttachExactStateAndOwnership$'
		;;
	esac
}

assignment_admission_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	[[ "$file" == pkg/dispatcher/assignment_admission.go ]] || return 0
	case "$match" in
	'^(beginAssignmentAdmission)$')
		printf '^TestBufferAssignmentAdmissionBeginOutcomes$'
		;;
	'^(close)$')
		printf '^TestBufferAssignmentAdmissionCloseOutcomes$'
		;;
	'^(commit)$')
		printf '^TestBufferAssignmentAdmissionCommitOutcomes$'
		;;
	esac
}

assignment_admission_mutation_test_file() {
	local file="$1"
	[[ "$file" == pkg/dispatcher/assignment_admission.go ]] || return 0
	printf 'pkg/dispatcher/buffer_survivor_mutation_test.go'
}

escalation_survivor_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	[[ "$file" == pkg/dispatcher/escalation.go ]] || return 0
	case "$match" in
	'^(completeOneShotOpsRunFailureBestEffort)$' | \
		'^(completeOpsRunBestEffort)$' | \
		'^(escalateWithOneShot)$' | \
		'^(handleDecomposeResult)$' | \
		'^(handleDecomposeValidationError)$' | \
		'^(handleEscalationResult)$' | \
		'^(handleFailedEscalationResult)$' | \
		'^(logCompletedEscalationResult)$' | \
		'^(routeExistingRoutableEscalation)$' | \
		'^(routeNewRoutableEscalation)$')
		printf '^TestEscalationSurvivorMutation'
		;;
	esac
}

escalation_mutation_test_file() {
	local file="$1"
	local match="$2"
	[[ "$file" == pkg/dispatcher/escalation.go ]] || return 0
	case "$match" in
	'^(completeOneShotOpsRunFailureBestEffort)$' | \
		'^(completeOpsRunBestEffort)$' | \
		'^(escalateWithOneShot)$' | \
		'^(handleDecomposeResult)$' | \
		'^(handleDecomposeValidationError)$' | \
		'^(handleEscalationResult)$' | \
		'^(handleFailedEscalationResult)$' | \
		'^(logCompletedEscalationResult)$' | \
		'^(routeExistingRoutableEscalation)$' | \
		'^(routeNewRoutableEscalation)$')
		printf 'pkg/dispatcher/escalation_survivor_mutation_test.go'
		;;
	'^(spawnEscalationOneShot)$')
		printf 'pkg/dispatcher/bounded_mutation_test.go'
		;;
	esac
}

p0_durability_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/scheduling.go:^(tryAssignBatch)$' | \
		'pkg/dispatcher/scheduling.go:^(scopeRecoveryQuarantineAssignments)$')
		printf '^TestTryAssignBatchP0MutationOwner$'
		;;
	'pkg/beadstore/sqlite.go:^(RemoveDependency)$')
		printf '^(TestParityDependencyAndStatusAPIs|TestSQLiteRemoveDependencyNoOpDoesNotEmitEvent|TestSQLiteRemoveDependencyPropagatesTransactionFailures|TestSQLiteStoreDependencyRoundTrip)$'
		;;
	esac
}

split_branch_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'cmd/oro/cmd_start.go:^(buildDispatcherWithReviewTimeoutsAndCleanliness)$' | \
		'cmd/oro/cmd_start.go:^(buildDispatcherWithReviewTimeoutsAndCleanlinessForBranches)$' | \
		'cmd/oro/cmd_start.go:^(registerStartCommandFlags)$' | \
		'cmd/oro/cmd_start.go:^(resolveStartBranchConfig)$' | \
		'cmd/oro/cmd_start.go:^(runDaemonOnly)$' | \
		'cmd/oro/cmd_start.go:^(startFreshSwarmWithSpawner)$' | \
		'cmd/oro/cmd_start.go:^(startTargetIsRemoteTrackingRef)$')
		printf '^TestSplitBranchCmdMutationOwner$'
		;;
	'pkg/dispatcher/config.go:^(validateBranchConfig)$' | \
		'pkg/dispatcher/config.go:^(validateOperationalConfig)$' | \
		'pkg/dispatcher/config.go:^(withDefaults)$')
		printf '^TestSplitBranchConfigMutationOwner$'
		;;
	esac
}

qg_target_attribution_mutation_test_pattern() {
	return 0
}

qg_target_attribution_mutation_test_file() {
	local file="$1"
	local match="$2"
	if [[ -n "$(qg_target_attribution_mutation_test_pattern "$file" "$match")" ]]; then
		printf 'pkg/dispatcher/qg_target_attribution_mutation_test.go'
	fi
}

qg_classifier_decision_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/qg_failure_classifier.go:^(ClassifyQGFailure)$' | \
		'pkg/dispatcher/qg_failure_classifier.go:^(candidateOnlyDeterministicFailure)$' | \
		'pkg/dispatcher/qg_failure_classifier.go:^(isDeterministicQGFailure)$' | \
		'pkg/dispatcher/qg_failure_classifier.go:^(targetBaselineHasFailure)$' | \
		'pkg/dispatcher/qg_failure_store.go:^(acceptedQGTargetPassed)$')
		printf '^TestQGClassifierDecisionMutationOwner$'
		;;
	esac
}

qg_classifier_decision_mutation_test_file() {
	local file="$1"
	local match="$2"
	if [[ -n "$(qg_classifier_decision_mutation_test_pattern "$file" "$match")" ]]; then
		printf 'pkg/dispatcher/qg_classifier_decision_mutation_test.go'
	fi
}

qg_store_lifecycle_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/dispatcher.go:^(New)$' | \
		'pkg/dispatcher/qg_failure_store.go:^(classifyQGFailureWithAttribution)$' | \
		'pkg/dispatcher/qg_failure_store.go:^(qgFailureAttribution)$' | \
		'pkg/dispatcher/qg_failure_store.go:^(recordQGTargetFailure)$' | \
		'pkg/dispatcher/qg_failure_store.go:^(recordQGTargetPass)$')
		printf '^TestQGStoreLifecycleMutationOwner$'
		;;
	esac
}

qg_store_lifecycle_mutation_test_file() {
	local file="$1"
	local match="$2"
	if [[ -n "$(qg_store_lifecycle_mutation_test_pattern "$file" "$match")" ]]; then
		printf 'pkg/dispatcher/qg_store_lifecycle_mutation_test.go'
	fi
}

qg_flow_control_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/qg_flow.go:^(evaluateQGFailure)$' | \
		'pkg/dispatcher/qg_flow.go:^(handleQGFailure)$' | \
		'pkg/dispatcher/qg_flow.go:^(targetBaselineFailure)$')
		printf '^TestQGFlowControlMutationOwner$'
		;;
	esac
}

qg_flow_control_mutation_test_file() {
	local file="$1"
	local match="$2"
	if [[ -n "$(qg_flow_control_mutation_test_pattern "$file" "$match")" ]]; then
		printf 'pkg/dispatcher/qg_flow_control_mutation_test.go'
	fi
}

startup_maintenance_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'cmd/oro/cmd_start.go:^(withEnvValue)$')
		printf '^(TestDaemonChildEnvMarksTmuxManagedDaemon|TestStartModesPropagateOracleRuntimeIdentity|TestStartupReadinessCoversDevCacheSweep)$'
		;;
	'pkg/storage/dev_schedule.go:^(RunWeeklyDevCacheSweep)$')
		printf '^(TestDevCacheSweepTriggersOnSizeThreshold|TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationRejectsInvalidRequest|TestWeeklyDevCacheSweepMutationRunBoundaries)$'
		;;
	'pkg/storage/dev_schedule.go:^(failInterruptedWeeklyDevCacheSweeps)$')
		printf '^(TestWeeklyDevCacheSweepMutationRejectsMissingSweepCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision)$'
		;;
	'pkg/storage/dev_schedule.go:^(interruptedSweepHasLiveController)$')
		printf '^(TestWeeklyDevCacheSweepMutationReportsControllerQueryFailure|TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership)$'
		;;
	'pkg/storage/dev_schedule.go:^(interruptedWeeklyDevCacheSweeps)$')
		printf '^(TestWeeklyDevCacheSweepMutationReportsSweepQueryFailure|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation)$'
		;;
	'pkg/storage/dev_schedule.go:^(openInterruptedWeeklyDevCachePauses)$')
		printf '^(TestWeeklyDevCacheSweepMutationRejectsMissingPauseCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged)$'
		;;
	'pkg/storage/dev_schedule.go:^(reconcileInterruptedWeeklyDevCacheSweep)$')
		printf '^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$'
		;;
	'pkg/storage/dev_schedule.go:^(runWeeklyDevCacheProviders)$')
		printf '^(TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationSkipsIneligibleProviders|TestWeeklyDevCacheSweepMutationUsesDefaultProviderRunner)$'
		;;
	esac
}

cmd_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'cmd/oro/cmd_start.go:^(buildArgs)$')
		printf '^(TestDetachedStartForwardsBaseBranchToDaemon|TestJanitorStartPlumbing|TestStartProgressTimeoutFlag|TestStartReviewTimeoutFlagsAreDistinct)$'
		;;
	'cmd/oro/cmd_start.go:^(newStartCmd)$')
		printf '^(TestNewStartCmdMutationBoundaries|TestStartRejectsGitHubPolicyBeforeDispatcherMutation)$'
		;;
	'cmd/oro/cmd_start.go:^(startFreshSwarm)$')
		printf '^TestDetachedStartForwardsBaseBranchToDaemon$'
		;;
	'cmd/oro/cmd_monitor.go:^(RestartDaemon)$')
		printf '^(TestCLIMonitorRestartErrorBoundaries|TestCLIMonitorRestartUsesDetachedStartHandoff)$'
		;;
	esac
}

worker_ready_evidence_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/worker/ready_evidence.go:^(buildQGEvidence)$')
		printf '^TestWorkerReadyEvidenceMutationOwners$'
		;;
	'pkg/worker/ready_evidence.go:^(sha256Hex)$')
		printf '^(TestWorkerWritesCanonicalQGEvidenceAndSendsAssignedIdentity|TestWorkerWritesDurableReadyEvidenceIdentity|TestWorkerBuildsOrderedSubsecondEvidenceTiming)$'
		;;
	'pkg/worker/ready_evidence.go:^(writeQGEvidence)$')
		printf '^TestWorkerReadyEvidenceMutationOwners$'
		;;
	'pkg/worker/worker.go:^(SendReadyForReview)$')
		printf '^TestWorkerReadyEvidenceMutationOwners$'
		;;
	'pkg/worker/worker.go:^(gitHeadSHA)$')
		printf '^TestWorkerReadyEvidenceMutationOwners$'
		;;
	'pkg/worker/worker.go:^(loadQualityGateScript)$')
		printf '^TestWorkerReadyEvidenceMutationOwners$'
		;;
	'pkg/worker/worker.go:^(resetForNewAssignment)$')
		printf '^TestWorkerResetMutationOwners$'
		;;
	'pkg/worker/worker.go:^(runQGAndReport)$')
		printf '^TestWorkerQGLifecycleMutationOwners$'
		;;
	'pkg/worker/worker.go:^(runQualityGateWithProgress)$')
		printf '^TestWorkerQGLifecycleMutationOwners$'
		;;
	'pkg/worker/worker.go:^(closeLogFile)$' | \
	'pkg/worker/worker.go:^(openLogFile)$' | \
	'pkg/worker/worker.go:^(processOutput)$' | \
	'pkg/worker/worker.go:^(processOutputTextLine)$' | \
	'pkg/worker/worker.go:^(processStructuredStreamLine)$')
		printf '^TestWorkerLogOutputMutationOwners$'
		;;
	esac
}

function_sharded_mutation_target() {
	local file="$1"
	[[ "$file" == pkg/dispatcher/*.go || "$file" == pkg/storage/dev_schedule.go ||
		"$file" == cmd/oro/cmd_start.go || "$file" == cmd/oro/cmd_monitor.go ||
		"$file" == pkg/beadstore/sqlite.go || "$file" == pkg/worker/worker.go ||
		"$file" == pkg/worker/ready_evidence.go ]]
}

authoritative_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/assignment.go:^(assignBeadWithClaim)$')
		printf '^(TestAssignmentClaimAuthoritativeSurvivorMutation|TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$'
		;;
	'pkg/dispatcher/assignment.go:^(assignmentInsertFailureAllowsReopen)$' | \
		'pkg/dispatcher/assignment.go:^(checkpointAssignmentAdmissionAllowed)$')
		printf '^TestAssignmentAuthoritativeSurvivorMutation'
		;;
	'pkg/dispatcher/ops_runs.go:^(reviewContextForOpsRun)$')
		printf '^(TestOpsAuthoritativeSurvivorMutationReviewContexts|TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex)$'
		;;
	'pkg/dispatcher/ops_runs.go:^(CompleteOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(CreateOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(applyOpsResolve)$' | \
		'pkg/dispatcher/ops_runs.go:^(completeOpsRunFromStatus)$' | \
		'pkg/dispatcher/ops_runs.go:^(createOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(findBlockingOpsRun)$' | \
	'pkg/dispatcher/ops_runs.go:^(isSQLiteUniqueConstraint)$' | \
		'pkg/dispatcher/ops_runs.go:^(loadOpsRunByID)$' | \
		'pkg/dispatcher/ops_runs.go:^(replaceOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(routeOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(routeReviewOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(supersedeAndRerouteOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(supersedeOpsRunForRetry)$' | \
		'pkg/dispatcher/ops_runs.go:^(terminalOpsRunResult)$' | \
		'pkg/dispatcher/ops_runs.go:^(watchReroutedOpsRunResult)$')
		printf '^TestOpsAuthoritativeSurvivorMutation'
		;;
	'pkg/dispatcher/health.go:^(applyHealth)$')
		printf '^(TestHealthAuthoritativeSurvivorMutation|TestApplyHealthReturnsAndReleasesDispatcherMutex$)'
		;;
	'pkg/dispatcher/health.go:^(evaluateFactoryHealth)$' | \
		'pkg/dispatcher/health.go:^(recordAssignmentObservation)$')
		printf '^TestHealthAuthoritativeSurvivorMutation'
		;;
	'pkg/dispatcher/review_checkpoint_store.go:^(BlockIntegration)$')
		printf '^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationIntegrationDurability$)'
		;;
	'pkg/dispatcher/review_checkpoint_store.go:^(legacyUnlinkedCheckpointIDs)$')
		printf '^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationLegacyBinding$)'
		;;
	'pkg/dispatcher/review_checkpoint_store.go:^(AdvanceIntegrationStep)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(CompleteIntegration)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(CreateOrReuse)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(ObserveIntegration)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(PromoteManualIntegration)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(createOrReuseReviewCheckpoint)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(createOrReuseReviewCheckpointAttempt)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(requireOneCheckpointRow)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(validateOpsRunCheckpointIdentity)$')
		printf '^TestReviewCheckpointAuthoritativeSurvivorMutation'
		;;
	esac
}

authoritative_mutation_test_file() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/assignment.go:^(assignmentInsertFailureAllowsReopen)$' | \
		'pkg/dispatcher/assignment.go:^(checkpointAssignmentAdmissionAllowed)$')
		printf 'pkg/dispatcher/assignment_authoritative_survivor_mutation_test.go'
		;;
	'pkg/dispatcher/ops_runs.go:^(CompleteOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(CreateOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(applyOpsResolve)$' | \
		'pkg/dispatcher/ops_runs.go:^(completeOpsRunFromStatus)$' | \
		'pkg/dispatcher/ops_runs.go:^(createOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(findBlockingOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(isSQLiteUniqueConstraint)$' | \
		'pkg/dispatcher/ops_runs.go:^(loadOpsRunByID)$' | \
		'pkg/dispatcher/ops_runs.go:^(replaceOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(routeOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(routeReviewOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(supersedeAndRerouteOpsRun)$' | \
		'pkg/dispatcher/ops_runs.go:^(supersedeOpsRunForRetry)$' | \
		'pkg/dispatcher/ops_runs.go:^(terminalOpsRunResult)$' | \
		'pkg/dispatcher/ops_runs.go:^(watchReroutedOpsRunResult)$')
		printf 'pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go'
		;;
	'pkg/dispatcher/health.go:^(evaluateFactoryHealth)$' | \
		'pkg/dispatcher/health.go:^(recordAssignmentObservation)$')
		printf 'pkg/dispatcher/health_authoritative_survivor_mutation_test.go'
		;;
	'pkg/dispatcher/review_checkpoint_store.go:^(AdvanceIntegrationStep)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(CompleteIntegration)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(CreateOrReuse)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(ObserveIntegration)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(PromoteManualIntegration)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(createOrReuseReviewCheckpoint)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(createOrReuseReviewCheckpointAttempt)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(requireOneCheckpointRow)$' | \
		'pkg/dispatcher/review_checkpoint_store.go:^(validateOpsRunCheckpointIdentity)$')
		printf 'pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go'
		;;
	esac
}

review_integration_recovery_mutation_test_pattern() {
	local file="$1"
	local match="$2"
	[[ "$file" == pkg/dispatcher/review_integration_recovery.go ]] || return
	case "$match" in
	'^(completeCheckpointAssignment)$')
		printf '^TestReviewIntegrationRecoveryMutationCompleteCheckpointAssignment$'
		;;
	'^(reviewIntegrationRefSHA)$' | '^(reviewIntegrationTargetSHA)$')
		printf '^TestReviewIntegrationRecoveryMutationReferenceResolution$'
		;;
	'^(closeIntegratedBeadOnce)$')
		printf '^TestReviewIntegrationRecoveryMutationCloseIntegratedBeadOnce$'
		;;
	'^(reviewIntegrationAncestor)$' | '^(reviewIntegrationProof)$')
		printf '^TestReviewIntegrationRecoveryMutationAncestryAndProof$'
		;;
	'^(verifyApprovedIntegrationSource)$' | '^(retryReviewIntegrationMerge)$')
		printf '^TestReviewIntegrationRecoveryMutationApprovedSourceAndRetry$'
		;;
	'^(prepareApprovedReviewIntegration)$' | '^(reconcileReviewIntegration)$')
		printf '^TestReviewIntegrationRecoveryMutationPrepareAndReconcile$'
		;;
	'^(finalizeReviewIntegration)$')
		printf '^TestReviewIntegrationRecoveryMutationFinalize$'
		;;
	'^(reconcileManualReviewIntegration)$' | '^(reconcileAutomaticReviewIntegration)$')
		printf '^TestReviewIntegrationRecoveryMutationManualAndAutomatic$'
		;;
	'^(reconcileReviewIntegrationsOnStartup)$')
		printf '^(TestReviewIntegrationRecoveryMutationStartupListFailure|TestReviewIntegrationRecoveryMutationStartupWrapsCheckpointFailure)$'
		;;
	esac
}

review_integration_recovery_mutation_test_file() {
	local file="$1"
	if [[ "$file" == pkg/dispatcher/review_integration_recovery.go ]]; then
		printf 'pkg/dispatcher/review_integration_recovery_mutation_test.go'
	fi
}

dispatcher_test_supplement() {
	local file="$1"
	local match="$2"
	local -a tests=()
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *executableAfterEpicSideEffects* ]] &&
		tests+=(
			TestExecutableAfterEpicSideEffectsClassifiesNonEpicAndChildlessEpic
			TestExecutableAfterEpicSideEffectsFailsClosedAndAuditsChildLookupError
			TestExecutableAfterEpicSideEffectsProcessesAndReleasesDecomposedEpic
			TestExecutableAfterEpicSideEffectsDoesNotProcessBlockedOrUnknownAdmission
			TestFilterExecutableBeadsIgnoresPremortemGate
		)
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *filterAssignable* ]] &&
		tests+=(TestFilterAssignableAppliesEveryDurableEligibilityStage)
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *filterExecutableBeads* ]] &&
		tests+=(TestFilterExecutableBeadsReturnsOnlyExecutableInputs)
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *filterReviewCheckpointBlockedBeads* ]] &&
		tests+=(
			TestFilterReviewCheckpointBlockedBeadsShortCircuitsEmptyAndNilDatabase
			TestFilterReviewCheckpointBlockedBeadsFiltersAndAuditsExactRows
			TestFilterReviewCheckpointBlockedBeadsFailsClosedAndRecordsObservation
		)
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *reviewCheckpointBlockedBeads* ]] &&
		tests+=(TestReviewCheckpointBlockedBeadsReturnsExactSetAndScanErrors)
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *reviewCheckpointBlocksAssignment* ]] &&
		tests+=(
			TestReviewCheckpointBlocksAssignmentHandlesNilDatabaseAndExactState
			TestReviewCheckpointBlocksAssignmentReportsObservationFailure
		)
	[[ "$file" == pkg/dispatcher/assignment_reconcile.go && "$match" == *tryRecoverExternalCloseWork* ]] &&
		tests+=(
			TestTryRecoverExternalCloseWorkAuditsSuccessProof
			TestTryRecoverExternalCloseWorkAuditsAndEscalatesFailureCause
			TestExternalCloseCleansUpAssignmentAndTracking
		)
	[[ "$file" == pkg/dispatcher/assignment_side_effect_admission.go && "$match" == *acquireAssignmentSideEffectAdmission* ]] &&
		tests+=(
			TestAcquireAssignmentSideEffectAdmissionRejectsInvalidInputs
			TestAcquireAssignmentSideEffectAdmissionPersistsOwnedToken
			TestAcquireAssignmentSideEffectAdmissionBlocksAndAuditsReservedBead
			TestAcquireAssignmentSideEffectAdmissionReportsStorageFailureAndObservation
		)
	[[ "$file" == pkg/dispatcher/assignment_side_effect_admission.go && "$match" == *releaseAssignmentSideEffectAdmission* ]] &&
		tests+=(
			TestReleaseAssignmentSideEffectAdmissionHandlesNilInputs
			TestReleaseAssignmentSideEffectAdmissionDeletesOnlyOwnedToken
			TestReleaseAssignmentSideEffectAdmissionAuditsStorageFailure
		)
	[[ "$file" == pkg/dispatcher/assignment_side_effect_admission.go && "$match" == *clearStaleAssignmentSideEffectAdmissions* ]] &&
		tests+=(
			TestClearStaleAssignmentSideEffectAdmissionsHandlesNilInputs
			TestClearStaleAssignmentSideEffectAdmissionsRemovesAllRows
			TestClearStaleAssignmentSideEffectAdmissionsReportsStorageFailure
		)
	[[ "$file" == pkg/dispatcher/assignment_state.go && "$match" == '^(createAssignment)$' ]] &&
		tests+=(
			TestCreateAssignmentReportsAdmissionFailure
			TestCreateAssignmentPersistsExactIdentity
			TestCreateAssignmentRejectsDurableCheckpointWithoutRow
			TestCreateAssignmentFailsClosedWhenCheckpointObservationFails
			TestCreateAssignmentRollsBackCommitFailure
		)
	[[ "$file" == pkg/dispatcher/assignment_state.go && "$match" == *createAssignmentWithEvidence* ]] &&
		tests+=(
			TestCreateAssignmentWithEvidenceReportsTargetResolutionFailure
			TestCreateAssignmentWithEvidenceRejectsBlankTargetSHA
			TestCreateAssignmentWithEvidenceReportsAdmissionFailure
			TestCreateAssignmentWithEvidencePersistsTrimmedProof
			TestCreateAssignmentWithEvidenceFailsClosedWhenCheckpointObservationFails
			TestCreateAssignmentWithEvidenceRollsBackCommitFailure
			TestReanchorAssignmentWithEvidencePreservesAdmissionAndCheckpointGate
		)
	[[ "$file" == pkg/dispatcher/epic_branch_admission.go && "$match" == '^(withEpicBranchAdmission)$' ]] &&
		tests+=(TestEpicBranchAdmissionMutationBypassAndClaimPreservation)
	[[ "$file" == pkg/dispatcher/epic_branch_admission.go && "$match" == '^(renewEpicBranchAdmission)$' ]] &&
		tests+=(TestEpicBranchAdmissionMutationRenewalOutcomes)
	[[ "$file" == pkg/dispatcher/epic_branch_admission.go && "$match" == '^(isOwnedBlockedEpicBranchAdmission)$' ]] &&
		tests+=(TestEpicBranchAdmissionMutationRenewalOutcomes)
	[[ "$file" == pkg/dispatcher/epic_branch_admission.go && "$match" == '^(blockEpicBranchAdmission)$' ]] &&
		tests+=(TestEpicBranchAdmissionBlocksUnsafeFreshInspection TestEpicBranchAdmissionMutationBlockOutcomes)
	[[ "$file" == pkg/dispatcher/escalation.go && "$match" == *routeNewRoutableEscalation* ]] &&
		tests+=(TestEscalateOversizedRoutesToDecomposeProductionPath TestEscalateNoOpWhenBlockingOpsRunExists)
	[[ "$file" == pkg/dispatcher/escalation.go && "$match" == *handleDecomposeValidationError* ]] &&
		tests+=(TestOversizedDecomposeResultAcksOnlyAfterValidation)
	[[ "$file" == pkg/dispatcher/escalation.go && "$match" == *escalateWithOneShot* ]] &&
		tests+=(TestEscalateSpawnsOneShotForTargetTypes)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *applyOpsResolve* ]] &&
		tests+=(TestOpsRunDirectives)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *findBlockingOpsRun* ]] &&
		tests+=(TestFindBlockingOpsRun)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(createOpsRun)$' ]] &&
		tests+=(TestOpsRunMutationLowLevelFailures)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(completeOpsRunFromStatus)$' ]] &&
		tests+=(TestOpsRunMutationLowLevelFailures TestOpsRunMutationExactReplayRequiresEveryField)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(terminalOpsRunResult)$' ]] &&
		tests+=(TestOpsRunMutationTerminalResultMapping)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(watchReroutedOpsRunResult)$' ]] &&
		tests+=(TestOpsRunMutationWatcherRejectsZeroIdentity TestWatchReroutedOpsRunResultRunsSideEffectsOnlyForAcquiredCompletion)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(supersedeOpsRunForRetry)$' ]] &&
		tests+=(TestOpsRunMutationRetryNormalizesReplacement TestSupersedeOpsReviewRetryPreservesContext)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(reviewContextFromWorkerLocked)$' ]] &&
		tests+=(TestOpsRunMutationReviewContextIdentity)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(reviewContextFromAnyWorkerLocked)$' ]] &&
		tests+=(TestOpsRunMutationReviewContextIdentity)
	[[ "$file" == pkg/dispatcher/scheduling.go && "$match" == *launchAssignment* ]] &&
		tests+=(TestTimedOutSetupCannotClobberReplacement)
	if [[ "$file" == pkg/dispatcher/review_checkpoint_store.go ]]; then
		tests+=(
			TestReanchorTransactionalReadyCheckpointPreservesOpsRunIdentity
			TestRouteOpsRunRoutesReviewOpsRun
			TestDispatcherStartupReturnsUnroutableReplacementFailureWriteError
		)
	fi
	[[ "$file" == pkg/dispatcher/review_checkpoint_store.go && "$match" == *bindSingleLegacyCheckpoint* ]] &&
		tests+=(TestRouteOpsRunRestoresExactReviewCheckpointIdentityWithoutWorker)
	printf '%s\n' "${tests[@]}"
}

merge_test_patterns() {
	local pattern="$1"
	local supplements="$2"
	local names=""
	if [[ -n "$pattern" ]]; then
		names=${pattern#^(}
		names=${names%)$}
	fi
	names=$(
		{
			tr '|' '\n' <<<"$names"
			printf '%s\n' "$supplements"
		} |
			sed '/^$/d' |
			sort -u |
			paste -sd'|' -
	)
	if [[ -n "$names" ]]; then
		printf '^(%s)$' "$names"
	fi
}

touched_functions_covered() {
	local match="$1"
	local coverage="$2"
	local functions=${match#^(}
	functions=${functions%)$}
	local report
	report=$(go tool cover -func="$coverage") || return 1
	local function
	while IFS= read -r function; do
		awk -v target="$function" '
			$2 == target || $2 ~ ("[.]" target "$") {
				coverage = $3
				gsub(/%/, "", coverage)
				if (coverage + 0 > 0) found = 1
			}
			END { exit !found }
		' <<<"$report" || return 1
	done < <(tr '|' '\n' <<<"$functions")
}

targeted_test_pattern() {
	local base="$1"
	local head="$2"
	local file="$3"
	local match="$4"
	local assignment_admission_pattern assignment_bc_pattern authoritative_pattern cmd_pattern escalation_survivor_pattern p0_durability_pattern qg_classifier_decision_pattern qg_flow_control_pattern qg_store_lifecycle_pattern qg_target_attribution_pattern review_checkpoint_pattern review_integration_recovery_pattern review_worker_lifecycle_pattern split_branch_pattern startup_maintenance_pattern worker_ready_evidence_pattern
	worker_ready_evidence_pattern=$(worker_ready_evidence_mutation_test_pattern "$file" "$match")
	if [[ -n "$worker_ready_evidence_pattern" ]]; then
		printf '%s' "$worker_ready_evidence_pattern"
		return
	fi
	if [[ "$file" == pkg/worker/worker.go || "$file" == pkg/worker/ready_evidence.go ]]; then
		return
	fi
	p0_durability_pattern=$(p0_durability_mutation_test_pattern "$file" "$match")
	if [[ -n "$p0_durability_pattern" ]]; then
		printf '%s' "$p0_durability_pattern"
		return
	fi
	split_branch_pattern=$(split_branch_mutation_test_pattern "$file" "$match")
	if [[ -n "$split_branch_pattern" ]]; then
		printf '%s' "$split_branch_pattern"
		return
	fi
	qg_classifier_decision_pattern=$(qg_classifier_decision_mutation_test_pattern "$file" "$match")
	if [[ -n "$qg_classifier_decision_pattern" ]]; then
		printf '%s' "$qg_classifier_decision_pattern"
		return
	fi
	qg_store_lifecycle_pattern=$(qg_store_lifecycle_mutation_test_pattern "$file" "$match")
	if [[ -n "$qg_store_lifecycle_pattern" ]]; then
		printf '%s' "$qg_store_lifecycle_pattern"
		return
	fi
	qg_flow_control_pattern=$(qg_flow_control_mutation_test_pattern "$file" "$match")
	if [[ -n "$qg_flow_control_pattern" ]]; then
		printf '%s' "$qg_flow_control_pattern"
		return
	fi
	qg_target_attribution_pattern=$(qg_target_attribution_mutation_test_pattern "$file" "$match")
	if [[ -n "$qg_target_attribution_pattern" ]]; then
		printf '%s' "$qg_target_attribution_pattern"
		return
	fi
	startup_maintenance_pattern=$(startup_maintenance_mutation_test_pattern "$file" "$match")
	if [[ -n "$startup_maintenance_pattern" ]]; then
		printf '%s' "$startup_maintenance_pattern"
		return
	fi
	review_worker_lifecycle_pattern=$(review_worker_lifecycle_mutation_test_pattern "$file" "$match")
	if [[ -n "$review_worker_lifecycle_pattern" ]]; then
		printf '%s' "$review_worker_lifecycle_pattern"
		return
	fi
	cmd_pattern=$(cmd_mutation_test_pattern "$file" "$match")
	if [[ -n "$cmd_pattern" ]]; then
		printf '%s' "$cmd_pattern"
		return
	fi
	authoritative_pattern=$(authoritative_mutation_test_pattern "$file" "$match")
	if [[ -n "$authoritative_pattern" ]]; then
		printf '%s' "$authoritative_pattern"
		return
	fi
	assignment_admission_pattern=$(assignment_admission_mutation_test_pattern "$file" "$match")
	if [[ -n "$assignment_admission_pattern" ]]; then
		printf '%s' "$assignment_admission_pattern"
		return
	fi
	assignment_bc_pattern=$(assignment_bc_mutation_test_pattern "$file" "$match")
	if [[ -n "$assignment_bc_pattern" ]]; then
		printf '%s' "$assignment_bc_pattern"
		return
	fi
	escalation_survivor_pattern=$(escalation_survivor_mutation_test_pattern "$file" "$match")
	if [[ -n "$escalation_survivor_pattern" ]]; then
		printf '%s' "$escalation_survivor_pattern"
		return
	fi
	review_integration_recovery_pattern=$(review_integration_recovery_mutation_test_pattern "$file" "$match")
	if [[ -n "$review_integration_recovery_pattern" ]]; then
		printf '%s' "$review_integration_recovery_pattern"
		return
	fi
	review_checkpoint_pattern=$(review_checkpoint_mutation_test_pattern "$file" "$match")
	if [[ -n "$review_checkpoint_pattern" ]]; then
		printf '%s' "$review_checkpoint_pattern"
		return
	fi

	if [[ "$file" == cmd/oro/hooks.go && "$match" == '^(isOroDistributedHook)$' ]]; then
		printf '^TestIsOroDistributedHook'
	elif [[ "$file" == cmd/oro/cmd_init.go && "$match" == '^(installAgentBranchGuard)$' ]]; then
		printf '^TestInstallAgentBranchGuard'
	elif [[ "$file" == cmd/oro/cmd_start.go && "$match" == '^(hookPathsWouldLeak)$' ]]; then
		printf '^(TestHookPathsWouldLeak|TestHookPathsWouldLeak_NonTmpdirSandboxRoot|TestHookPathsWouldLeak_NonstandardGoTempRoot|TestInstallCodexHookConfigRefusesLeakyHooks)$'
	elif [[ "$file" == pkg/dispatcher/scheduling.go && "$match" == '^(advanceAssignedGeneralIdle)$' ]]; then
		printf '^TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease$'
	elif [[ "$file" == pkg/dispatcher/scheduling.go && "$match" == '^(launchAssignmentWithResult)$' ]]; then
		printf '^TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound$'
	elif [[ "$file" == pkg/dispatcher/sqlite_busy_retry.go && "$match" == '^(retrySQLiteBusyOperation)$' ]]; then
		printf '^TestRetrySQLiteBusyOperation$'
	elif [[ "$file" == pkg/dispatcher/epic_branch_admission.go && "$match" == '^(withEpicBranchAdmission)$' ]]; then
		printf '^TestEpicBranchAdmissionMutationBypassAndClaimPreservation$'
	elif [[ "$file" == pkg/dispatcher/assignment.go && "$match" == '^(assignBeadWithClaim)$' ]]; then
		printf '^(TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$'
	elif [[ "$file" == pkg/dispatcher/assignment.go && "$match" == '^(releaseAssignmentReservation)$' ]]; then
		printf '^TestReleaseAssignmentReservationResetsStateAndUnlocks$'
	elif [[ "$file" == pkg/dispatcher/escalation.go && "$match" == '^(spawnEscalationOneShot)$' ]]; then
		printf '^TestSpawnEscalationOneShotReturnsAfterReadingWorktree$'
	elif [[ "$file" == pkg/dispatcher/health.go && "$match" == '^(applyHealth)$' ]]; then
		printf '^TestApplyHealthReturnsAndReleasesDispatcherMutex$'
	elif [[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == '^(reviewContextForOpsRun)$' ]]; then
		printf '^TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex$'
	elif [[ "$file" == pkg/dispatcher/*.go ]]; then
		local function pattern supplements
		function=${match#^(}
		function=${function%)$}
		pattern=$(cochanged_dispatcher_test_match "$base" "$head" "$file" "$function")
		supplements=$(dispatcher_test_supplement "$file" "$match")
		merge_test_patterns "$pattern" "$supplements"
	fi
}

reset_mutation_cache_slot() {
	local shard_root="$1"
	local cache_slot="$2"
	local cache_root
	[[ -n "$shard_root" && -d "$shard_root" && "$cache_slot" =~ ^[0-9]+$ ]] || return 2
	cache_root="$shard_root/caches/$cache_slot"
	case "$cache_root" in
	"$shard_root"/caches/[0-9]*) ;;
	*) return 2 ;;
	esac
	rm -rf -- "$cache_root"
	mkdir -p "$cache_root"
}

prewarm_parallel_mutation_workers() {
	local checkout="$1"
	local source_file="$2"
	local test_file="$3"
	local cache_root="$4"
	local tmp_root="$5"
	local worker_count="$6"
	local timeout_seconds="$7"
	local module_path source_path source_dir source_dir_abs test_path test_dir_abs
	local worker warm_status warm_failure=0
	local -a test_targets warm_pids=()

	if [[ -z "$checkout" || ! -d "$checkout" || -z "$source_file" || "$source_file" == /* ||
		! -f "$checkout/$source_file" || -z "$cache_root" || -z "$tmp_root" ||
		! "$worker_count" =~ ^[1-9][0-9]*$ || ! "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] ||
		((timeout_seconds <= 5)); then
		return 2
	fi
	module_path=$(awk '$1 == "module" { print $2; exit }' "$checkout/go.mod")
	[[ -n "$module_path" ]] || return 2
	source_path="$checkout/$source_file"
	source_dir=$(dirname -- "$source_path")
	if ! source_dir_abs=$(cd "$source_dir" && pwd -P); then
		return 2
	fi
	if [[ -n "$test_file" ]]; then
		[[ "$test_file" != /* ]] || return 2
		test_path="$checkout/$test_file"
		if ! test_dir_abs=$(cd "$(dirname -- "$test_path")" 2>/dev/null && pwd -P); then
			return 2
		fi
		if [[ "$test_dir_abs" != "$source_dir_abs" || "$(basename -- "$test_path")" != *_test.go ||
			! -f "$test_path" ]]; then
			return 2
		fi
		mapfile -t test_targets < <(find "$source_dir_abs" -maxdepth 1 -type f -name '*.go' ! -name '*_test.go' | sort)
		test_targets+=("$test_path")
	else
		test_targets=("$module_path/$(dirname "$source_file")")
	fi

	mkdir -p "$cache_root" "$tmp_root/prewarm-logs"
	for ((worker = 0; worker < worker_count; worker++)); do
		mkdir -p "$cache_root/parallel-$worker" "$tmp_root/parallel-worker-$worker"
		(
			cd "$checkout"
			GOCACHE="$cache_root/parallel-$worker" \
				GOTMPDIR="$tmp_root/parallel-worker-$worker" \
				timeout --foreground "$timeout_seconds" \
				go test -vet=off -count=1 -timeout "$((timeout_seconds - 5))s" \
				-run '^$' "${test_targets[@]}"
		) >"$tmp_root/prewarm-logs/$worker.log" 2>&1 &
		warm_pids+=("$!")
	done
	for ((worker = 0; worker < worker_count; worker++)); do
		warm_status=0
		if wait "${warm_pids[$worker]}"; then
			continue
		else
			warm_status=$?
		fi
		warm_failure=1
		cat "$tmp_root/prewarm-logs/$worker.log" >&2
		printf 'mutation worker %d cache prewarm failed: status=%d\n' "$worker" "$warm_status" >&2
	done
	((warm_failure == 0))
}

heavy_parallel_mutation_shard() {
	local file="$1"
	local match="$2"
	case "$file:$match" in
	'pkg/dispatcher/review_checkpoint_store.go:^(releaseWorkerWithHook)$' | \
		'pkg/dispatcher/review_worker_release.go:^(acquireCheckpointWorkerReleaseLocked)$' | \
		'pkg/dispatcher/review_worker_release.go:^(runCheckpointWorkerReleaseLease)$' | \
		'pkg/dispatcher/worker_directives.go:^(applyKillWorker)$' | \
		'pkg/dispatcher/worker_directives.go:^(applyRestartWorker)$' | \
		'pkg/dispatcher/worker_directives.go:^(restartCheckpointOwnedWorkerUsing)$' | \
		'pkg/dispatcher/startup_recovery.go:^(handleConn)$' | \
		'pkg/dispatcher/worker_pool.go:^(registerWorkerWithProtocol)$' | \
		'pkg/dispatcher/worker_pool.go:^(releaseReviewWorkerAfterSendFailureUsing)$')
		return 0
		;;
	esac
	return 1
}

mutation_shard_timeout_seconds() {
	[[ "$#" = 3 && "$3" =~ ^[1-9][0-9]*$ ]] || return 2
	if [[ "$1:$2" = 'pkg/dispatcher/worker_pool.go:^(registerWorkerWithProtocol)$' ]]; then
		printf '360\n'
	else
		printf '%s\n' "$3"
	fi
}

run_mutation_shard() {
	local index="$1"
	local file="$2"
	local match="$3"
	local test_pattern="$4"
	local cache_slot="$5"
	local head="$6"
	local shard_root="$7"
	local result_dir="$8"
	local file_timeout="$9"
	local exec_timeout="${10}"
	local max_shard_timeout="${11}"
	local checkout="$shard_root/checkouts/$index"
	local output_file="$shard_root/logs/$index.log"
	local result="$result_dir/$index.json"
	local mutation_exit=0
	local mutation_test_file=""
	local authoritative_cache_warm_timeout=""
	local expected_source_hash=""
	local shard_timeout=""
	mutation_test_file=$(authoritative_mutation_test_file "$file" "$match")
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(escalation_mutation_test_file "$file" "$match")
	fi
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(review_integration_recovery_mutation_test_file "$file")
	fi
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(qg_classifier_decision_mutation_test_file "$file" "$match")
	fi
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(qg_store_lifecycle_mutation_test_file "$file" "$match")
	fi
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(qg_flow_control_mutation_test_file "$file" "$match")
	fi
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(qg_target_attribution_mutation_test_file "$file" "$match")
	fi
	if [[ -z "$mutation_test_file" ]]; then
		mutation_test_file=$(assignment_admission_mutation_test_file "$file")
	fi
	case "$test_pattern" in
	'^TestAssignBeadWithClaimReportsUnclaimedValidationFailure$' | '^TestReleaseAssignmentReservationResetsStateAndUnlocks$')
		mutation_test_file=pkg/dispatcher/assignment_mutation_test.go
		;;
	'^(TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$')
		mutation_test_file=pkg/dispatcher/assignment_behavior_mutation_test.go
		;;
	'^TestAssignmentBCPrepareWorktreeOutcomes$' | \
		'^(TestAssignmentBCValidateDivergedRecoveryOutcomes|TestAssignmentBCValidateCurrentBranchError)$' | \
		'^TestAssignmentBCReservationReleaseExactState$' | \
		'^TestAssignmentBCAttachExactStateAndOwnership$')
		mutation_test_file=pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go
		;;
	'^TestBufferAssignmentAdmissionBeginOutcomes$' | \
		'^TestBufferAssignmentAdmissionCloseOutcomes$' | \
		'^TestBufferAssignmentAdmissionCommitOutcomes$')
		mutation_test_file=pkg/dispatcher/buffer_survivor_mutation_test.go
		;;
	'^TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound$')
		mutation_test_file=pkg/dispatcher/scheduling_mutation_test.go
		;;
	'^TestRetrySQLiteBusyOperation$')
		mutation_test_file=pkg/dispatcher/sqlite_busy_retry_test.go
		;;
	'^TestReviewCheckpointMutationOwnershipLoads$' | '^TestReviewCheckpointMutationLegacyBinding$' | \
		'^TestReviewCheckpointMutationIntegrationDurability$')
		mutation_test_file=pkg/dispatcher/review_checkpoint_store_mutation_test.go
		;;
	esac
	if [[ -z "$match" ]]; then
		write_shard_no_mutants "$result" "$index" "$file" "$match" "$test_pattern"
		return
	fi
	if [[ "$file" == pkg/worker/worker.go || "$file" == pkg/worker/ready_evidence.go ]] &&
		[[ -z "$test_pattern" ]]; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 \
			'worker mutation target has no deterministic test owner'
		return
	fi
	if [[ "$test_pattern" == *AuthoritativeSurvivorMutation* ]]; then
		authoritative_cache_warm_timeout=120
	fi
	mkdir -p "$checkout" "$shard_root/logs" "$shard_root/tmp/$index"
	if ! reset_mutation_cache_slot "$shard_root" "$cache_slot"; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'reset isolated mutation cache slot'
		return
	fi
	if ! git archive "$head" | tar -x -C "$checkout"; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'create isolated mutation checkout'
		return
	fi
	if [[ -f "$checkout/Makefile" && -f "$checkout/cmd/oro/embed.go" ]]; then
		if ! (cd "$checkout" && make stage-assets) >"$output_file" 2>&1; then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'stage embedded assets in mutation checkout'
			return
		fi
	fi
	local -a mutation_exec=("--exec=bash scripts/quality_gate/mutation_exec.sh")
	if [[ "$file" == pkg/dispatcher/*.go && -z "$test_pattern" ]]; then
		write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'dispatcher mutation target has no deterministic test owner'
		return
	fi
	if [[ -n "$test_pattern" ]]; then
		local package
		package="./$(dirname "$file")"
		local listed_tests list_output_file list_reason
		list_output_file="$shard_root/logs/$index.list.log"
		if ! (
			cd "$checkout"
			GOCACHE="$shard_root/caches/$cache_slot" GOTMPDIR="$shard_root/tmp/$index" \
				go test -list "$test_pattern" "$package"
		) >"$list_output_file" 2>&1; then
			cat "$list_output_file" >>"$output_file"
			list_reason='list targeted mutation tests'
			if grep -Fqi 'no space left on device' "$list_output_file"; then
				list_reason='list targeted mutation tests: no space left on device'
			fi
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 "$list_reason"
			return
		fi
		listed_tests=$(<"$list_output_file")
		if ! grep -Eq "$test_pattern" <<<"$listed_tests"; then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'targeted mutation test pattern matched no tests'
			return
		fi
		local coverage_file="$shard_root/coverage/$index.out"
		mkdir -p "$shard_root/coverage"
		local baseline_exit=0
		(
			cd "$checkout"
			GOCACHE="$shard_root/caches/$cache_slot" GOTMPDIR="$shard_root/tmp/$index" \
				timeout "$exec_timeout" go test -vet=off -count=1 -timeout "$((exec_timeout + 5))s" \
				-coverprofile="$coverage_file" -run "$test_pattern" "$package"
		) >>"$output_file" 2>&1 || baseline_exit=$?
		if ((baseline_exit != 0)); then
			local reason='targeted mutation baseline failed'
			((baseline_exit == 124)) && reason='targeted mutation baseline deadline exceeded'
			if grep -Fqi 'no space left on device' "$output_file"; then
				reason='targeted mutation baseline failed: no space left on device'
			fi
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" "$baseline_exit" "$reason"
			return
		fi
		if [[ "$file" == pkg/dispatcher/*.go ]] && ! touched_functions_covered "$match" "$coverage_file"; then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'targeted mutation tests do not cover every touched function'
			return
		fi
	fi
	if heavy_parallel_mutation_shard "$file" "$match"; then
		if ! expected_source_hash=$(git rev-parse "$head:$file" 2>/dev/null); then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'record archived mutation source identity'
			return
		fi
		if ! prewarm_parallel_mutation_workers "$checkout" "$file" "$mutation_test_file" \
			"$shard_root/caches/$cache_slot" "$shard_root/tmp/$index" 2 120 >>"$output_file" 2>&1; then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'prewarm isolated mutation worker caches'
			return
		fi
		if [[ "$(git hash-object "$checkout/$file" 2>/dev/null || true)" != "$expected_source_hash" ]]; then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'mutation source changed during worker cache prewarm'
			return
		fi
		if ! shard_timeout=$(mutation_shard_timeout_seconds "$file" "$match" "$file_timeout"); then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'invalid heavy mutation shard timeout'
			return
		fi
	fi
	(
		cd "$checkout"
		if [[ "$file" == pkg/dispatcher/assignment.go && "$match" == '^(assignBeadWithClaim)$' ]]; then
			GOCACHE="$shard_root/caches/$cache_slot" \
				GOTMPDIR="$shard_root/tmp/$index" \
				MUTATION_SOURCE_FILE="$file" \
				MUTATION_FUNCTION_MATCH="$match" \
				MUTATION_TEST_PATTERN="$test_pattern" \
				MUTATION_TEST_FILE="$mutation_test_file" \
				MUTATION_EXEC_TIMEOUT="$exec_timeout" \
				MUTATION_TEST_TIMEOUT_MARGIN_SECONDS=5 \
				MUTATION_PARALLEL_WORKERS=2 \
				MUTATION_WORKER_CACHE_WARM_TIMEOUT_SECONDS=120 \
				MUTATION_BASE_SHARD_TIMEOUT_SECONDS="$assignment_claim_shard_timeout" \
				MUTATION_MAX_SHARD_TIMEOUT_SECONDS="$assignment_claim_shard_timeout" \
				MUTATION_FAILURE_EVIDENCE_DIR="$mutation_failure_evidence_root/$index" \
				MUTATION_EXEC_SCRIPT="$mutation_script_dir/mutation_exec.sh" \
				timeout "$assignment_claim_shard_timeout" bash "$mutation_script_dir/mutation_parallel.sh"
		elif heavy_parallel_mutation_shard "$file" "$match"; then
			GOCACHE="$shard_root/caches/$cache_slot" \
				GOTMPDIR="$shard_root/tmp/$index" \
				MUTATION_SOURCE_FILE="$file" \
				MUTATION_FUNCTION_MATCH="$match" \
				MUTATION_TEST_PATTERN="$test_pattern" \
				MUTATION_TEST_FILE="$mutation_test_file" \
				MUTATION_EXEC_TIMEOUT="$exec_timeout" \
				MUTATION_TEST_TIMEOUT_MARGIN_SECONDS=5 \
				MUTATION_PARALLEL_WORKERS=2 \
				MUTATION_WORKER_CACHE_WARM_TIMEOUT_SECONDS=120 \
				MUTATION_BASE_SHARD_TIMEOUT_SECONDS="$shard_timeout" \
				MUTATION_MAX_SHARD_TIMEOUT_SECONDS="$shard_timeout" \
				MUTATION_FAILURE_EVIDENCE_DIR="$mutation_failure_evidence_root/$index" \
				MUTATION_EXEC_SCRIPT="$mutation_script_dir/mutation_exec.sh" \
				timeout "$shard_timeout" bash "$mutation_script_dir/mutation_parallel.sh"
		elif [[ "$test_pattern" == *AuthoritativeSurvivorMutation* ||
			"$file" == pkg/dispatcher/review_integration_recovery.go ||
			"$mutation_test_file" == pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go ||
			"$mutation_test_file" == pkg/dispatcher/buffer_survivor_mutation_test.go ||
			"$mutation_test_file" == pkg/dispatcher/escalation_survivor_mutation_test.go ]]; then
			GOCACHE="$shard_root/caches/$cache_slot" \
				GOTMPDIR="$shard_root/tmp/$index" \
				MUTATION_SOURCE_FILE="$file" \
				MUTATION_FUNCTION_MATCH="$match" \
				MUTATION_TEST_PATTERN="$test_pattern" \
				MUTATION_TEST_FILE="$mutation_test_file" \
				MUTATION_EXEC_TIMEOUT="$exec_timeout" \
				MUTATION_TEST_TIMEOUT_MARGIN_SECONDS=5 \
				MUTATION_PARALLEL_WORKERS=2 \
				MUTATION_WORKER_CACHE_WARM_TIMEOUT_SECONDS="$authoritative_cache_warm_timeout" \
				MUTATION_BASE_SHARD_TIMEOUT_SECONDS="$file_timeout" \
				MUTATION_MAX_SHARD_TIMEOUT_SECONDS="$file_timeout" \
				MUTATION_FAILURE_EVIDENCE_DIR="$mutation_failure_evidence_root/$index" \
				MUTATION_EXEC_SCRIPT="$mutation_script_dir/mutation_exec.sh" \
				timeout "$file_timeout" bash "$mutation_script_dir/mutation_parallel.sh"
		else
			GOCACHE="$shard_root/caches/$cache_slot" \
				GOTMPDIR="$shard_root/tmp/$index" \
				MUTATION_TEST_PATTERN="$test_pattern" \
				MUTATION_TEST_FILE="$mutation_test_file" \
				timeout "$file_timeout" go tool go-mutesting --exec-timeout="$exec_timeout" "${mutation_exec[@]}" "--match=$match" "$file"
		fi
	) >"$output_file" 2>&1 || mutation_exit=$?
	write_shard_result "$result" "$index" "$file" "$match" "$test_pattern" "$mutation_exit" "$output_file"
}

main() {
	local base_ref=""
	local head_ref=""
	local evidence=""
	while (($# > 0)); do
		case "$1" in
		--base)
			base_ref=${2:-}
			shift 2
			;;
		--head)
			head_ref=${2:-}
			shift 2
			;;
		--evidence)
			evidence=${2:-}
			shift 2
			;;
		*)
			usage
			return
			;;
		esac
	done
	if [[ -z "$base_ref" || -z "$head_ref" || -z "$evidence" ]]; then
		usage
		return
	fi

	local base="$base_ref"
	local head="$head_ref"
	if ! base=$(git rev-parse --verify "$base_ref^{commit}" 2>/dev/null); then
		infrastructure_failure "$evidence" "$base_ref" "$head_ref" 'base ref is unavailable' 2
		return
	fi
	if ! head=$(git rev-parse --verify "$head_ref^{commit}" 2>/dev/null); then
		infrastructure_failure "$evidence" "$base" "$head_ref" 'head ref is unavailable' 2
		return
	fi

	local changed
	changed=$(git diff --name-only "$base" "$head" -- '*.go' 2>/dev/null |
		grep -Ev '(^|/)([^/]+_test|[^/]+_generated)\.go$|^cmd/oro/_assets/' || true)
	local -a changed_files=()
	if [[ -n "$changed" ]]; then
		mapfile -t changed_files <<<"$changed"
	fi
	if ((${#changed_files[@]} == 0)); then
		write_evidence "$evidence" "$base" "$head" pass 0 null 0
		printf 'pass: no production Go files changed\n'
		return
	fi

	local max_workers=${MUTATION_MAX_WORKERS:-4}
	local file_timeout=${MUTATION_FILE_TIMEOUT_SECONDS:-240}
	local exec_timeout=${MUTATION_EXEC_TIMEOUT_SECONDS:-60}
	local max_shard_timeout=${MUTATION_MAX_SHARD_TIMEOUT_SECONDS:-900}
	if [[ ! "$max_workers" =~ ^[1-9][0-9]*$ || ! "$file_timeout" =~ ^[1-9][0-9]*$ ||
		! "$exec_timeout" =~ ^[1-9][0-9]*$ || ! "$max_shard_timeout" =~ ^[1-9][0-9]*$ ||
		max_shard_timeout -lt file_timeout ]]; then
		infrastructure_failure "$evidence" "$base" "$head" 'mutation shard bounds must be positive integers' 2 "${changed_files[@]}"
		return
	fi
	if ! command -v timeout >/dev/null 2>&1; then
		infrastructure_failure "$evidence" "$base" "$head" 'timeout command is unavailable' 2 "${changed_files[@]}"
		return
	fi

	local shard_root
	shard_root=$(mktemp -d "${TMPDIR:-/tmp}/oro-mutation-shards.XXXXXX")
	mutation_shard_root=$shard_root
	trap cleanup_mutation_shards EXIT
	local result_dir="$shard_root/results"
	mkdir -p "$result_dir"
	local -a shard_files=()
	local -a match_patterns=()
	local -a test_patterns=()
	local file
	for file in "${changed_files[@]}"; do
		local match
		match=$(touched_function_match "$base" "$head" "$file")
		if function_sharded_mutation_target "$file" && [[ -n "$match" ]]; then
			local functions function function_match
			functions=${match#^(}
			functions=${functions%)$}
			while IFS= read -r function; do
				[[ -n "$function" ]] || continue
				function_match="^(${function})$"
				shard_files+=("$file")
				match_patterns+=("$function_match")
				test_patterns+=("$(targeted_test_pattern "$base" "$head" "$file" "$function_match")")
			done < <(tr '|' '\n' <<<"$functions")
		else
			shard_files+=("$file")
			match_patterns+=("$match")
			test_patterns+=("$(targeted_test_pattern "$base" "$head" "$file" "$match")")
		fi
	done
	local pending_shards
	pending_shards=$(
		for index in "${!shard_files[@]}"; do
			jq -nc --arg file "${shard_files[$index]}" --arg match "${match_patterns[$index]}" \
				--arg test_pattern "${test_patterns[$index]}" \
				'{file: $file, match: $match, test_pattern: $test_pattern, conclusion: "pending", exit_code: 0, reason: "", score: null, passed: 0, failed: 0, duplicated: 0, skipped: 0, total: 0}'
		done | jq -s '.'
	)
	write_sharded_evidence "$evidence" "$base" "$head" infrastructure_failure 2 null 0 "$pending_shards" "${changed_files[@]}"
	mutation_failure_evidence_root=$(cd "$(dirname "$evidence")" && pwd -P)/mutation-failures

	local worker_count=$max_workers
	if ((worker_count > ${#shard_files[@]})); then
		worker_count=${#shard_files[@]}
	fi
	printf 'mutation shards: shards=%d files=%d workers=%d file_timeout=%ss exec_timeout=%ss emergency_cap=%ss\n' \
		"${#shard_files[@]}" "${#changed_files[@]}" "$worker_count" "$file_timeout" "$exec_timeout" \
		"$max_shard_timeout"

	local -a pids=()
	local index key cache_slot pid
	for index in "${!shard_files[@]}"; do
		file=${shard_files[$index]}
		printf -v key '%06d' "$index"
		cache_slot=$((index % worker_count))
		if heavy_parallel_mutation_shard "$file" "${match_patterns[$index]}" ||
			[[ ("$file" == pkg/dispatcher/assignment.go &&
			("${match_patterns[$index]}" == '^(assignBeadWithClaim)$' ||
				"${test_patterns[$index]}" == *TestAssignmentBC*)) ||
			"${test_patterns[$index]}" == *AuthoritativeSurvivorMutation* ||
			"$file" == pkg/dispatcher/assignment_admission.go ||
			"$file" == pkg/dispatcher/review_integration_recovery.go ||
			("$file" == pkg/dispatcher/escalation.go &&
				"${test_patterns[$index]}" == '^TestEscalationSurvivorMutation') ]]; then
			for pid in "${pids[@]}"; do
				wait "$pid" || true
			done
			pids=()
			MUTATION_PARALLEL_WORKERS=2 \
				run_mutation_shard "$key" "$file" "${match_patterns[$index]}" "${test_patterns[$index]}" "$cache_slot" "$head" "$shard_root" "$result_dir" "$file_timeout" "$exec_timeout" "$max_shard_timeout"
			continue
		fi
		run_mutation_shard "$key" "$file" "${match_patterns[$index]}" "${test_patterns[$index]}" "$cache_slot" "$head" "$shard_root" "$result_dir" "$file_timeout" "$exec_timeout" "$max_shard_timeout" &
		pids+=("$!")
		if ((${#pids[@]} == worker_count)); then
			for pid in "${pids[@]}"; do
				wait "$pid" || true
			done
			pids=()
		fi
	done
	for pid in "${pids[@]}"; do
		wait "$pid" || true
	done

	for index in "${!shard_files[@]}"; do
		file=${shard_files[$index]}
		printf -v key '%06d' "$index"
		if [[ ! -s "$result_dir/$key.json" ]]; then
			write_shard_infrastructure "$result_dir/$key.json" "$key" "$file" "${match_patterns[$index]}" "${test_patterns[$index]}" 2 'mutation shard produced no evidence'
		fi
		if [[ -s "$shard_root/logs/$key.log" ]]; then
			printf '\n--- mutation shard %s: %s ---\n' "$key" "$file"
			sed -n '1,240p' "$shard_root/logs/$key.log"
		fi
	done

	local shards
	shards=$(jq -s 'sort_by(.index) | map(del(.index))' "$result_dir"/*.json)
	local infrastructure_count
	infrastructure_count=$(jq \
		'[.[] | select(.conclusion != "completed" and .conclusion != "no_mutation_sites")] | length' \
		<<<"$shards")
	if ((infrastructure_count > 0)); then
		local infrastructure_exit
		infrastructure_exit=$(jq \
			'[.[] | select(.conclusion != "completed" and .conclusion != "no_mutation_sites") | .exit_code] |
			if index(124) != null then 124 elif length > 0 then .[0] else 2 end' <<<"$shards")
		write_sharded_evidence "$evidence" "$base" "$head" infrastructure_failure "$infrastructure_exit" null 0 "$shards" "${changed_files[@]}"
		printf 'infrastructure failure: %d of %d mutation shards did not complete\n' \
			"$infrastructure_count" "${#shard_files[@]}" >&2
		return 2
	fi

	local passed failed duplicated skipped total score
	passed=$(jq '[.[].passed] | add // 0' <<<"$shards")
	failed=$(jq '[.[].failed] | add // 0' <<<"$shards")
	duplicated=$(jq '[.[].duplicated] | add // 0' <<<"$shards")
	skipped=$(jq '[.[].skipped] | add // 0' <<<"$shards")
	total=$((passed + failed + skipped))
	if ((total == 0)); then
		write_sharded_evidence "$evidence" "$base" "$head" pass 0 null 0 "$shards" "${changed_files[@]}"
		printf 'pass: validated changed functions contain no mutation sites\n'
		return
	fi
	score=$(awk "BEGIN { printf \"%.6f\", $passed / $total }")
	printf 'aggregate mutation score is %s (%d passed, %d failed, %d duplicated, %d skipped, total is %d)\n' \
		"$score" "$passed" "$failed" "$duplicated" "$skipped" "$total"
	if awk "BEGIN { exit !($score < $policy_score) }"; then
		write_sharded_evidence "$evidence" "$base" "$head" deterministic_failure 0 "$score" "$total" "$shards" "${changed_files[@]}"
		printf 'deterministic failure: mutation score %s is below policy %s\n' "$score" "$policy_score" >&2
		return 1
	fi
	write_sharded_evidence "$evidence" "$base" "$head" pass 0 "$score" "$total" "$shards" "${changed_files[@]}"
	printf 'pass: mutation score %s meets policy %s\n' "$score" "$policy_score"
}

main "$@"
