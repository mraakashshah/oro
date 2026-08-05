#!/usr/bin/env bash
set -euo pipefail

readonly policy_score=0.75
mutation_script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly mutation_script_dir
mutation_shard_root=""

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
	[[ "$file" == pkg/dispatcher/epic_branch_admission.go && "$match" == *blockEpicBranchAdmission* ]] &&
		tests+=(TestEpicBranchAdmissionBlocksUnsafeFreshInspection)
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
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *terminalOpsRunResult* ]] &&
		tests+=(TestWatchReroutedOpsRunResultRunsSideEffectsOnlyForAcquiredCompletion)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *watchReroutedOpsRunResult* ]] &&
		tests+=(TestWatchReroutedOpsRunResultRunsSideEffectsOnlyForAcquiredCompletion)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *supersedeOpsRunForRetry* ]] &&
		tests+=(TestSupersedeOpsReviewRetryPreservesContext)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *reviewContextFromWorkerLocked* ]] &&
		tests+=(TestRouteOpsRunRoutesReviewOpsRun)
	[[ "$file" == pkg/dispatcher/ops_runs.go && "$match" == *reviewContextFromAnyWorkerLocked* ]] &&
		tests+=(TestRouteOpsRunRoutesReviewOpsRun)
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
	elif [[ "$file" == pkg/dispatcher/assignment.go && "$match" == '^(assignBeadWithClaim)$' ]]; then
		printf '^TestAssignBeadWithClaimReportsUnclaimedValidationFailure$'
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
	local checkout="$shard_root/checkouts/$index"
	local output_file="$shard_root/logs/$index.log"
	local result="$result_dir/$index.json"
	local mutation_exit=0
	local mutation_test_file=""
	case "$test_pattern" in
	'^TestAssignBeadWithClaimReportsUnclaimedValidationFailure$' | '^TestReleaseAssignmentReservationResetsStateAndUnlocks$')
		mutation_test_file=pkg/dispatcher/assignment_mutation_test.go
		;;
	'^TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound$')
		mutation_test_file=pkg/dispatcher/scheduling_mutation_test.go
		;;
	'^TestRetrySQLiteBusyOperation$')
		mutation_test_file=pkg/dispatcher/sqlite_busy_retry_test.go
		;;
	esac
	if [[ -z "$match" ]]; then
		write_shard_no_mutants "$result" "$index" "$file" "$match" "$test_pattern"
		return
	fi
	mkdir -p "$checkout" "$shard_root/logs" "$shard_root/caches/$cache_slot" "$shard_root/tmp/$index"
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
		local listed_tests
		if ! listed_tests=$(
			cd "$checkout"
			GOCACHE="$shard_root/caches/$cache_slot" GOTMPDIR="$shard_root/tmp/$index" \
				go test -list "$test_pattern" "$package"
		); then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'list targeted mutation tests'
			return
		fi
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
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" "$baseline_exit" "$reason"
			return
		fi
		if [[ "$file" == pkg/dispatcher/*.go ]] && ! touched_functions_covered "$match" "$coverage_file"; then
			write_shard_infrastructure "$result" "$index" "$file" "$match" "$test_pattern" 2 'targeted mutation tests do not cover every touched function'
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
				MUTATION_PARALLEL_WORKERS="${MUTATION_PARALLEL_WORKERS:-2}" \
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
	if [[ ! "$max_workers" =~ ^[1-9][0-9]*$ || ! "$file_timeout" =~ ^[1-9][0-9]*$ ||
		! "$exec_timeout" =~ ^[1-9][0-9]*$ ]]; then
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
		if [[ "$file" == pkg/dispatcher/*.go && -n "$match" ]]; then
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

	local worker_count=$max_workers
	if ((worker_count > ${#shard_files[@]})); then
		worker_count=${#shard_files[@]}
	fi
	printf 'mutation shards: shards=%d files=%d workers=%d file_timeout=%ss exec_timeout=%ss\n' \
		"${#shard_files[@]}" "${#changed_files[@]}" "$worker_count" "$file_timeout" "$exec_timeout"

	local -a pids=()
	local index key cache_slot pid
	for index in "${!shard_files[@]}"; do
		file=${shard_files[$index]}
		printf -v key '%06d' "$index"
		cache_slot=$((index % worker_count))
		if [[ "$file" == pkg/dispatcher/assignment.go && "${match_patterns[$index]}" == '^(assignBeadWithClaim)$' ]]; then
			for pid in "${pids[@]}"; do
				wait "$pid" || true
			done
			pids=()
			MUTATION_PARALLEL_WORKERS=2 \
				run_mutation_shard "$key" "$file" "${match_patterns[$index]}" "${test_patterns[$index]}" "$cache_slot" "$head" "$shard_root" "$result_dir" "$file_timeout" "$exec_timeout"
			continue
		fi
		run_mutation_shard "$key" "$file" "${match_patterns[$index]}" "${test_patterns[$index]}" "$cache_slot" "$head" "$shard_root" "$result_dir" "$file_timeout" "$exec_timeout" &
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
