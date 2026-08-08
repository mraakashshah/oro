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

review_checkpoint_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^review_checkpoint_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted reviewed checkpoint owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''review_checkpoint_mutation_test_pattern "$1" "$2"' _ "$file" "$match"
}

review_integration_recovery_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^review_integration_recovery_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted reviewed integration-recovery owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''review_integration_recovery_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

review_integration_recovery_test_file_for() {
	local file="$1"
	local function_source
	function_source=$(awk '
		/^review_integration_recovery_mutation_test_file\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted reviewed integration-recovery standalone file mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''review_integration_recovery_mutation_test_file "$1"' _ "$file" || true
}

assignment_bc_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^assignment_bc_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted assignment B+C owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''assignment_bc_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

assignment_admission_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^assignment_admission_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted assignment admission owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''assignment_admission_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

assignment_admission_test_file_for() {
	local file="$1"
	local function_source
	function_source=$(awk '
		/^assignment_admission_mutation_test_file\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted assignment admission standalone file mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''assignment_admission_mutation_test_file "$1"' _ "$file" || true
}

escalation_survivor_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^escalation_survivor_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted escalation survivor owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''escalation_survivor_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

escalation_mutation_test_file_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^escalation_mutation_test_file\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted escalation standalone file mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''escalation_mutation_test_file "$1" "$2"' _ "$file" "$match" || true
}

authoritative_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^authoritative_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted authoritative owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''authoritative_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

authoritative_test_file_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^authoritative_mutation_test_file\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted authoritative standalone file mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''authoritative_mutation_test_file "$1" "$2"' _ "$file" "$match" || true
}

startup_maintenance_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^startup_maintenance_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted startup-maintenance owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''startup_maintenance_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

cmd_mutation_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^cmd_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted cmd owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''cmd_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

p0_durability_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^p0_durability_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted P0 durability owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''p0_durability_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

split_branch_pattern_for() {
	local file="$1"
	local match="$2"
	local function_source
	function_source=$(awk '
		/^split_branch_mutation_test_pattern\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted split-branch owner mapping'
		return 1
	fi
	bash -c "$function_source"$'\n''split_branch_mutation_test_pattern "$1" "$2"' _ "$file" "$match" || true
}

function_sharded_for() {
	local file="$1"
	local function_source
	function_source=$(awk '
		/^function_sharded_mutation_target\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	if [[ -z "$function_source" ]]; then
		fail 'mutation runner omitted function-sharded target classifier'
		return 1
	fi
	bash -c "$function_source"$'\n''function_sharded_mutation_target "$1"' _ "$file"
}

TestP0DurabilityMutationMapping() {
	local cardinality coverage coverage_root file function got listed package pattern report
	local dispatcher_pattern='^TestTryAssignBatchP0MutationOwner$'
	local beadstore_pattern='^(TestParityDependencyAndStatusAPIs|TestSQLiteRemoveDependencyNoOpDoesNotEmitEvent|TestSQLiteRemoveDependencyPropagatesTransactionFailures|TestSQLiteStoreDependencyRoundTrip)$'

	coverage_root=$(mktemp -d)
	# shellcheck disable=SC2064 # expand the function-local path before the RETURN trap runs.
	trap "rm -rf '$coverage_root'" RETURN
	while IFS=$'\t' read -r file function package pattern cardinality; do
		got=$(p0_durability_pattern_for "$file" "^(${function})$")
		[[ "$got" == "$pattern" ]] ||
			fail "$file $function P0 mutation owner = $got, want $pattern"
		function_sharded_for "$file" || fail "$file must use per-function mutation sharding"
		listed=$(go test -list "$pattern" "$package")
		[[ "$(grep -Ec '^Test' <<<"$listed")" == "$cardinality" ]] ||
			fail "$file $function owner must select exactly $cardinality real tests"
		coverage="$coverage_root/${function}.out"
		timeout 60 go test -vet=off -count=1 -timeout 55s -coverprofile="$coverage" -run "$pattern" "$package" >/dev/null
		report=$(go tool cover -func="$coverage")
		awk -v target="$function" '
			$2 == target || $2 ~ ("[.]" target "$") {
				value = $3
				gsub(/%/, "", value)
				if (value + 0 > 0) found = 1
			}
			END { exit !found }
		' <<<"$report" || fail "$file $function owner has zero production coverage"
	done <<EOF
pkg/dispatcher/scheduling.go	tryAssignBatch	./pkg/dispatcher	$dispatcher_pattern	1
pkg/dispatcher/scheduling.go	scopeRecoveryQuarantineAssignments	./pkg/dispatcher	$dispatcher_pattern	1
pkg/beadstore/sqlite.go	RemoveDependency	./pkg/beadstore	$beadstore_pattern	4
EOF

	got=$(p0_durability_pattern_for pkg/dispatcher/other.go '^(tryAssignBatch)$')
	[[ -z "$got" ]] || fail "P0 dispatcher owner accepted wrong source: $got"
	got=$(p0_durability_pattern_for pkg/dispatcher/scheduling.go '^(unmappedSchedulingFunction)$')
	[[ -z "$got" ]] || fail "P0 dispatcher owner accepted wrong function: $got"
	got=$(p0_durability_pattern_for pkg/beadstore/other.go '^(RemoveDependency)$')
	[[ -z "$got" ]] || fail "P0 beadstore owner accepted wrong source: $got"
	got=$(p0_durability_pattern_for pkg/beadstore/sqlite.go '^(unmappedSQLiteFunction)$')
	[[ -z "$got" ]] || fail "P0 beadstore owner accepted wrong function: $got"
	[[ "$dispatcher_pattern" != *TestRedeployableQuarantineWithoutReadyBeadReportsAssignmentFreeze* &&
		"$dispatcher_pattern" != *TestTryAssignAllowsFreshWorkWhenRecoveryQuarantineIsHumanOwned* &&
		"$dispatcher_pattern" != *TestTryAssignBlocksFreshWorkWhenRecoveryQuarantineOpen* &&
		"$dispatcher_pattern" != *TestTryAssignNotFrozenByEmptySafeQuarantine* ]] ||
		fail 'P0 scheduling owner must not run mutex-sensitive behavioral tests directly'

	got=$(startup_maintenance_pattern_for pkg/storage/dev_schedule.go '^(reconcileInterruptedWeeklyDevCacheSweep)$')
	[[ "$got" == '^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$' ]] ||
		fail "P0 mapping changed dev_schedule owner: $got"
	got=$(cmd_mutation_pattern_for cmd/oro/cmd_start.go '^(startFreshSwarm)$')
	[[ "$got" == '^TestDetachedStartForwardsBaseBranchToDaemon$' ]] ||
		fail "P0 mapping changed cmd owner: $got"
}

TestSplitBranchMutationOwners() {
	local cardinality coverage coverage_root file function got listed package pattern report
	local cmd_pattern='^TestSplitBranchCmdMutationOwner$'
	local config_pattern='^TestSplitBranchConfigMutationOwner$'

	coverage_root=$(mktemp -d)
	# shellcheck disable=SC2064 # expand the function-local path before the RETURN trap runs.
	trap "rm -rf '$coverage_root'" RETURN
	while IFS=$'\t' read -r file function package pattern cardinality; do
		got=$(split_branch_pattern_for "$file" "^(${function})$")
		[[ "$got" == "$pattern" ]] ||
			fail "$file $function split-branch owner = $got, want $pattern"
		function_sharded_for "$file" || fail "$file must use per-function mutation sharding"
		listed=$(go test -list "$pattern" "$package")
		[[ "$(grep -Ec '^Test' <<<"$listed")" == "$cardinality" ]] ||
			fail "$file $function owner must select exactly $cardinality real test"
		coverage="$coverage_root/${function}.out"
		timeout 60 go test -vet=off -count=1 -timeout 55s -coverprofile="$coverage" -run "$pattern" "$package" >/dev/null
		report=$(go tool cover -func="$coverage")
		awk -v target="$function" '
			$2 == target || $2 ~ ("[.]" target "$") {
				value = $3
				gsub(/%/, "", value)
				if (value + 0 > 0) found = 1
			}
			END { exit !found }
		' <<<"$report" || fail "$file $function owner has zero production coverage"
	done <<EOF
cmd/oro/cmd_start.go	buildDispatcherWithReviewTimeoutsAndCleanliness	./cmd/oro	$cmd_pattern	1
cmd/oro/cmd_start.go	buildDispatcherWithReviewTimeoutsAndCleanlinessForBranches	./cmd/oro	$cmd_pattern	1
cmd/oro/cmd_start.go	registerStartCommandFlags	./cmd/oro	$cmd_pattern	1
cmd/oro/cmd_start.go	resolveStartBranchConfig	./cmd/oro	$cmd_pattern	1
cmd/oro/cmd_start.go	runDaemonOnly	./cmd/oro	$cmd_pattern	1
cmd/oro/cmd_start.go	startFreshSwarmWithSpawner	./cmd/oro	$cmd_pattern	1
cmd/oro/cmd_start.go	startTargetIsRemoteTrackingRef	./cmd/oro	$cmd_pattern	1
pkg/dispatcher/config.go	validateBranchConfig	./pkg/dispatcher	$config_pattern	1
pkg/dispatcher/config.go	validateOperationalConfig	./pkg/dispatcher	$config_pattern	1
pkg/dispatcher/config.go	withDefaults	./pkg/dispatcher	$config_pattern	1
EOF

	while IFS=$'\t' read -r file function; do
		got=$(split_branch_pattern_for "$file" "^(${function})$")
		[[ -z "$got" ]] || fail "$file $function must remain an explicit no-site unit: $got"
	done <<'EOF'
cmd/oro/cmd_start.go	buildDispatcherWithBranches
cmd/oro/cmd_start.go	startFreshSwarm
cmd/oro/cmd_start.go	startFreshSwarmWithBranches
EOF

	got=$(split_branch_pattern_for cmd/oro/other.go '^(resolveStartBranchConfig)$')
	[[ -z "$got" ]] || fail "split-branch owner accepted wrong command source: $got"
	got=$(split_branch_pattern_for pkg/dispatcher/other.go '^(validateBranchConfig)$')
	[[ -z "$got" ]] || fail "split-branch owner accepted wrong dispatcher source: $got"
	got=$(split_branch_pattern_for cmd/oro/cmd_start.go '^(unmappedStartFunction)$')
	[[ -z "$got" ]] || fail "split-branch owner accepted wrong function: $got"
	got=$(split_branch_pattern_for cmd/oro/cmd_start.go '^(resolveStartBranchConfig|runDaemonOnly)$')
	[[ -z "$got" ]] || fail "split-branch owner accepted grouped function union: $got"

	got=$(p0_durability_pattern_for pkg/dispatcher/scheduling.go '^(tryAssignBatch)$')
	[[ "$got" == '^TestTryAssignBatchP0MutationOwner$' ]] || fail "split-branch mapping changed P0 scheduling owner: $got"
	got=$(p0_durability_pattern_for pkg/dispatcher/scheduling.go '^(scopeRecoveryQuarantineAssignments)$')
	[[ "$got" == '^TestTryAssignBatchP0MutationOwner$' ]] || fail "split-branch mapping changed P0 quarantine owner: $got"
	got=$(p0_durability_pattern_for pkg/beadstore/sqlite.go '^(RemoveDependency)$')
	[[ "$got" == '^(TestParityDependencyAndStatusAPIs|TestSQLiteRemoveDependencyNoOpDoesNotEmitEvent|TestSQLiteRemoveDependencyPropagatesTransactionFailures|TestSQLiteStoreDependencyRoundTrip)$' ]] ||
		fail "split-branch mapping changed P0 dependency owner: $got"
	got=$(startup_maintenance_pattern_for cmd/oro/cmd_start.go '^(withEnvValue)$')
	[[ "$got" == '^(TestDaemonChildEnvMarksTmuxManagedDaemon|TestStartModesPropagateOracleRuntimeIdentity|TestStartupReadinessCoversDevCacheSweep)$' ]] ||
		fail "split-branch mapping changed startup owner: $got"

	while IFS=$'\t' read -r function pattern; do
		got=$(startup_maintenance_pattern_for pkg/storage/dev_schedule.go "^(${function})$")
		[[ "$got" == "$pattern" ]] || fail "split-branch mapping changed $function owner: $got"
	done <<'EOF'
RunWeeklyDevCacheSweep	^(TestDevCacheSweepTriggersOnSizeThreshold|TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationRejectsInvalidRequest|TestWeeklyDevCacheSweepMutationRunBoundaries)$
failInterruptedWeeklyDevCacheSweeps	^(TestWeeklyDevCacheSweepMutationRejectsMissingSweepCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision)$
interruptedSweepHasLiveController	^(TestWeeklyDevCacheSweepMutationReportsControllerQueryFailure|TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership)$
interruptedWeeklyDevCacheSweeps	^(TestWeeklyDevCacheSweepMutationReportsSweepQueryFailure|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation)$
openInterruptedWeeklyDevCachePauses	^(TestWeeklyDevCacheSweepMutationRejectsMissingPauseCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged)$
reconcileInterruptedWeeklyDevCacheSweep	^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$
runWeeklyDevCacheProviders	^(TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationSkipsIneligibleProviders|TestWeeklyDevCacheSweepMutationUsesDefaultProviderRunner)$
EOF

	while IFS=$'\t' read -r file function pattern; do
		got=$(cmd_mutation_pattern_for "$file" "^(${function})$")
		[[ "$got" == "$pattern" ]] || fail "split-branch mapping changed $file $function owner: $got"
	done <<'EOF'
cmd/oro/cmd_start.go	buildArgs	^(TestDetachedStartForwardsBaseBranchToDaemon|TestJanitorStartPlumbing|TestStartProgressTimeoutFlag|TestStartReviewTimeoutFlagsAreDistinct)$
cmd/oro/cmd_start.go	newStartCmd	^(TestNewStartCmdMutationBoundaries|TestStartRejectsGitHubPolicyBeforeDispatcherMutation)$
cmd/oro/cmd_start.go	startFreshSwarm	^TestDetachedStartForwardsBaseBranchToDaemon$
cmd/oro/cmd_monitor.go	RestartDaemon	^(TestCLIMonitorRestartErrorBoundaries|TestCLIMonitorRestartUsesDetachedStartHandoff)$
EOF

	got=$(split_branch_pattern_for pkg/dispatcher/config.go '^(unrelatedDispatcherFunction)$')
	[[ -z "$got" ]] || fail "split-branch resolver intercepted generic dispatcher fallback: $got"
	got=$(split_branch_pattern_for cmd/oro/cmd_status.go '^(unrelatedCommandFunction)$')
	[[ -z "$got" ]] || fail "split-branch resolver intercepted unrelated command fallback: $got"
	if function_sharded_for cmd/oro/cmd_status.go; then
		fail 'unrelated command source must retain whole-package fallback'
	fi
	grep -Fq "pattern=\$(cochanged_dispatcher_test_match" "$runner" ||
		fail 'generic dispatcher cochanged-test fallback was removed'
	grep -Fq "supplements=\$(dispatcher_test_supplement" "$runner" ||
		fail 'generic dispatcher supplement fallback was removed'
	grep -q 'dispatcher mutation target has no deterministic test owner' "$runner" ||
		fail 'generic dispatcher deterministic-owner failure was removed'
}

TestStartupMaintenanceMutationMapping() {
	local cmd_pattern got listed coverage_root report function names pattern cardinality
	cmd_pattern='^(TestDaemonChildEnvMarksTmuxManagedDaemon|TestStartModesPropagateOracleRuntimeIdentity|TestStartupReadinessCoversDevCacheSweep)$'

	got=$(startup_maintenance_pattern_for cmd/oro/cmd_start.go '^(withEnvValue)$')
	[[ "$got" == "$cmd_pattern" ]] ||
		fail "startup readiness mutation owner = $got, want $cmd_pattern"

	while IFS=$'\t' read -r function pattern cardinality; do
		got=$(startup_maintenance_pattern_for pkg/storage/dev_schedule.go "^(${function})$")
		[[ "$got" == "$pattern" ]] ||
			fail "$function weekly sweep mutation owner = $got, want $pattern"
		names=$(sed -e 's/^\^(//' -e 's/)\$$//' <<<"$pattern" | tr '|' '\n')
		[[ "$(wc -l <<<"$names" | tr -d ' ')" == "$cardinality" &&
		"$(sort -u <<<"$names" | wc -l | tr -d ' ')" == "$cardinality" ]] ||
			fail "$function owner must contain exactly $cardinality unique named tests"
		listed=$(go test -list "$pattern" ./pkg/storage)
		[[ "$(grep -Ec '^Test' <<<"$listed")" == "$cardinality" ]] ||
			fail "$function owner must select exactly $cardinality real tests"
	done <<'EOF'
RunWeeklyDevCacheSweep	^(TestDevCacheSweepTriggersOnSizeThreshold|TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationRejectsInvalidRequest|TestWeeklyDevCacheSweepMutationRunBoundaries)$	4
failInterruptedWeeklyDevCacheSweeps	^(TestWeeklyDevCacheSweepMutationRejectsMissingSweepCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision)$	3
interruptedSweepHasLiveController	^(TestWeeklyDevCacheSweepMutationReportsControllerQueryFailure|TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership)$	2
interruptedWeeklyDevCacheSweeps	^(TestWeeklyDevCacheSweepMutationReportsSweepQueryFailure|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation)$	3
openInterruptedWeeklyDevCachePauses	^(TestWeeklyDevCacheSweepMutationRejectsMissingPauseCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged)$	3
reconcileInterruptedWeeklyDevCacheSweep	^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$	1
runWeeklyDevCacheProviders	^(TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationSkipsIneligibleProviders|TestWeeklyDevCacheSweepMutationUsesDefaultProviderRunner)$	3
EOF

	got=$(startup_maintenance_pattern_for cmd/oro/other.go '^(withEnvValue)$')
	[[ -z "$got" ]] || fail "startup owner accepted wrong source: $got"
	got=$(startup_maintenance_pattern_for cmd/oro/cmd_start.go '^(unmappedStartupFunction)$')
	[[ -z "$got" ]] || fail "startup owner accepted unmapped function: $got"
	got=$(startup_maintenance_pattern_for pkg/storage/other.go '^(RunWeeklyDevCacheSweep)$')
	[[ -z "$got" ]] || fail "weekly sweep owner accepted wrong source: $got"
	got=$(startup_maintenance_pattern_for pkg/storage/dev_schedule.go '^(unmappedWeeklySweepFunction)$')
	[[ -z "$got" ]] || fail "weekly sweep owner accepted unmapped function: $got"

	names=$(sed -e 's/^\^(//' -e 's/)\$$//' <<<"$cmd_pattern" | tr '|' '\n')
	[[ "$(wc -l <<<"$names" | tr -d ' ')" == 3 && "$(sort -u <<<"$names" | wc -l | tr -d ' ')" == 3 ]] ||
		fail 'startup owner must contain exactly three unique named tests'
	listed=$(go test -list "$cmd_pattern" ./cmd/oro)
	[[ "$(grep -Ec '^Test' <<<"$listed")" == 3 ]] ||
		fail 'startup owner must select exactly three real tests'

	coverage_root=$(mktemp -d)
	# shellcheck disable=SC2064 # expand the function-local path before the RETURN trap runs.
	trap "rm -rf '$coverage_root'" RETURN
	go test -vet=off -count=1 -coverprofile="$coverage_root/cmd.out" -run "$cmd_pattern" ./cmd/oro >/dev/null
	report=$(go tool cover -func="$coverage_root/cmd.out")
	awk '$2 == "withEnvValue" && $3 + 0 > 0 { found = 1 } END { exit !found }' <<<"$report" ||
		fail 'startup owner has zero withEnvValue production coverage'

	while IFS=$'\t' read -r function pattern cardinality; do
		go test -vet=off -count=1 -coverprofile="$coverage_root/storage.out" -run "$pattern" ./pkg/storage >/dev/null
		report=$(go tool cover -func="$coverage_root/storage.out")
		awk -v target="$function" '
			$2 == target || $2 ~ ("[.]" target "$") {
				value = $3
				gsub(/%/, "", value)
				if (value + 0 > 0) found = 1
			}
			END { exit !found }
		' <<<"$report" || fail "$function weekly sweep owner has zero production coverage"
	done <<'EOF'
RunWeeklyDevCacheSweep	^(TestDevCacheSweepTriggersOnSizeThreshold|TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationRejectsInvalidRequest|TestWeeklyDevCacheSweepMutationRunBoundaries)$	4
failInterruptedWeeklyDevCacheSweeps	^(TestWeeklyDevCacheSweepMutationRejectsMissingSweepCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision)$	3
interruptedSweepHasLiveController	^(TestWeeklyDevCacheSweepMutationReportsControllerQueryFailure|TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership)$	2
interruptedWeeklyDevCacheSweeps	^(TestWeeklyDevCacheSweepMutationReportsSweepQueryFailure|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation)$	3
openInterruptedWeeklyDevCachePauses	^(TestWeeklyDevCacheSweepMutationRejectsMissingPauseCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged)$	3
reconcileInterruptedWeeklyDevCacheSweep	^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$	1
runWeeklyDevCacheProviders	^(TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationSkipsIneligibleProviders|TestWeeklyDevCacheSweepMutationUsesDefaultProviderRunner)$	3
EOF
}

TestStartupMaintenanceMutationSharding() {
	local evidence fixture
	fixture=$(mktemp -d)
	# shellcheck disable=SC2064 # expand the function-local path before the RETURN trap runs.
	trap "rm -rf '$fixture'" RETURN
	evidence=$(run_startup_maintenance_function_fixture "$fixture/startup-maintenance")
	jq -e '
		[.shards[].file] == [
			"pkg/storage/dev_schedule.go", "pkg/storage/dev_schedule.go",
			"pkg/storage/dev_schedule.go", "pkg/storage/dev_schedule.go",
			"pkg/storage/dev_schedule.go", "pkg/storage/dev_schedule.go",
			"pkg/storage/dev_schedule.go"
		] and
		[.shards[].match] == [
			"^(RunWeeklyDevCacheSweep)$",
			"^(failInterruptedWeeklyDevCacheSweeps)$",
			"^(interruptedSweepHasLiveController)$",
			"^(interruptedWeeklyDevCacheSweeps)$",
			"^(openInterruptedWeeklyDevCachePauses)$",
			"^(reconcileInterruptedWeeklyDevCacheSweep)$",
			"^(runWeeklyDevCacheProviders)$"
		] and
		[.shards[].test_pattern] == [
			"^(TestDevCacheSweepTriggersOnSizeThreshold|TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationRejectsInvalidRequest|TestWeeklyDevCacheSweepMutationRunBoundaries)$",
			"^(TestWeeklyDevCacheSweepMutationRejectsMissingSweepCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision)$",
			"^(TestWeeklyDevCacheSweepMutationReportsControllerQueryFailure|TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership)$",
			"^(TestWeeklyDevCacheSweepMutationReportsSweepQueryFailure|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation)$",
			"^(TestWeeklyDevCacheSweepMutationRejectsMissingPauseCAS|TestWeeklyDevCacheSweepReconcilesInterruptedRun|TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged)$",
			"^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$",
			"^(TestWeeklyDevCacheDueAndCatchup|TestWeeklyDevCacheSweepMutationSkipsIneligibleProviders|TestWeeklyDevCacheSweepMutationUsesDefaultProviderRunner)$"
		] and
		([.shards[].match] | unique | length) == 7 and .score == 1 and .total == 7' \
		"$evidence" >/dev/null ||
		fail 'weekly sweep functions must split into seven exact owner shards'
	[[ "$(wc -l <"$fixture/startup-maintenance/mutation-args.txt" | tr -d ' ')" == 7 ]] ||
		fail 'each weekly sweep function must run exactly once'
}

TestCmdMutationSharding() {
	local cardinality coverage_root function got listed monitor_pattern pattern report
	monitor_pattern='^(TestCLIMonitorRestartErrorBoundaries|TestCLIMonitorRestartUsesDetachedStartHandoff)$'

	coverage_root=$(mktemp -d)
	# shellcheck disable=SC2064 # expand the function-local path before the RETURN trap runs.
	trap "rm -rf '$coverage_root'" RETURN
	while IFS=$'\t' read -r function pattern cardinality; do
		got=$(cmd_mutation_pattern_for cmd/oro/cmd_start.go "^(${function})$")
		[[ "$got" == "$pattern" ]] || fail "$function cmd mutation owner = $got, want $pattern"
		listed=$(go test -list "$pattern" ./cmd/oro)
		[[ "$(grep -Ec '^Test' <<<"$listed")" == "$cardinality" ]] ||
			fail "$function owner must select exactly $cardinality real tests"
		go test -vet=off -count=1 -coverprofile="$coverage_root/$function.out" -run "$pattern" ./cmd/oro >/dev/null
		report=$(go tool cover -func="$coverage_root/$function.out")
		awk -v target="$function" '$2 == target && $3 + 0 > 0 { found = 1 } END { exit !found }' <<<"$report" ||
			fail "$function cmd mutation owner has zero production coverage"
	done <<'EOF'
buildArgs	^(TestDetachedStartForwardsBaseBranchToDaemon|TestJanitorStartPlumbing|TestStartProgressTimeoutFlag|TestStartReviewTimeoutFlagsAreDistinct)$	4
newStartCmd	^(TestNewStartCmdMutationBoundaries|TestStartRejectsGitHubPolicyBeforeDispatcherMutation)$	2
startFreshSwarm	^TestDetachedStartForwardsBaseBranchToDaemon$	1
EOF
	timeout 30 go test -vet=off -count=1 -run '^TestNewStartCmdMutationBoundaries$' ./cmd/oro >/dev/null ||
		fail 'newStartCmd direct owner must finish within 30 seconds'
	got=$(cmd_mutation_pattern_for cmd/oro/cmd_monitor.go '^(RestartDaemon)$')
	[[ "$got" == "$monitor_pattern" ]] ||
		fail "RestartDaemon cmd mutation owner = $got, want $monitor_pattern"

	for function in cmd/oro/cmd_start.go cmd/oro/cmd_monitor.go pkg/dispatcher/assignment.go pkg/storage/dev_schedule.go; do
		function_sharded_for "$function" || fail "$function must retain per-function mutation sharding"
	done
	if function_sharded_for cmd/oro/cmd_status.go; then
		fail 'unrelated cmd source must preserve whole-package fallback'
	fi
	got=$(cmd_mutation_pattern_for cmd/oro/cmd_start.go '^(unmappedStartFunction)$')
	[[ -z "$got" ]] || fail "unmapped start function unexpectedly selected $got"
	got=$(cmd_mutation_pattern_for cmd/oro/cmd_monitor.go '^(unmappedMonitorFunction)$')
	[[ -z "$got" ]] || fail "unmapped monitor function unexpectedly selected $got"

	listed=$(go test -list "$monitor_pattern" ./cmd/oro)
	[[ "$(grep -Ec '^Test' <<<"$listed")" == 2 ]] ||
		fail 'monitor owner must select exactly two real tests'

	go test -vet=off -count=1 -coverprofile="$coverage_root/monitor.out" -run "$monitor_pattern" ./cmd/oro >/dev/null
	report=$(go tool cover -func="$coverage_root/monitor.out")
	awk '$2 == "RestartDaemon" && $3 + 0 > 0 { found = 1 } END { exit !found }' <<<"$report" ||
		fail 'RestartDaemon cmd mutation owner has zero production coverage'
}

TestAuthoritativeMutationMapping() {
	local expected_file expected_pattern file function got
	while IFS=$'\t' read -r file function expected_pattern expected_file; do
		got=$(authoritative_pattern_for "$file" "^($function)$")
		[[ "$got" == "$expected_pattern" ]] ||
			fail "$file $function authoritative owner = $got, want $expected_pattern"
		got=$(authoritative_test_file_for "$file" "^($function)$")
		[[ "$got" == "$expected_file" ]] ||
			fail "$file $function authoritative file = $got, want $expected_file"
	done <<'EOF'
pkg/dispatcher/assignment.go	assignmentInsertFailureAllowsReopen	^TestAssignmentAuthoritativeSurvivorMutation	pkg/dispatcher/assignment_authoritative_survivor_mutation_test.go
pkg/dispatcher/assignment.go	checkpointAssignmentAdmissionAllowed	^TestAssignmentAuthoritativeSurvivorMutation	pkg/dispatcher/assignment_authoritative_survivor_mutation_test.go
pkg/dispatcher/assignment.go	assignBeadWithClaim	^(TestAssignmentClaimAuthoritativeSurvivorMutation|TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$
pkg/dispatcher/ops_runs.go	CompleteOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	CreateOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	applyOpsResolve	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	completeOpsRunFromStatus	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	createOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	findBlockingOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	isSQLiteUniqueConstraint	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	loadOpsRunByID	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	replaceOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	reviewContextForOpsRun	^(TestOpsAuthoritativeSurvivorMutationReviewContexts|TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex)$
pkg/dispatcher/ops_runs.go	reviewContextFromAnyWorkerLocked	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	reviewContextFromWorkerLocked	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	routeOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	routeReviewOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	supersedeAndRerouteOpsRun	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	supersedeOpsRunForRetry	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	terminalOpsRunResult	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/ops_runs.go	watchReroutedOpsRunResult	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
pkg/dispatcher/health.go	applyHealth	^(TestHealthAuthoritativeSurvivorMutation|TestApplyHealthReturnsAndReleasesDispatcherMutex$)
pkg/dispatcher/health.go	evaluateFactoryHealth	^TestHealthAuthoritativeSurvivorMutation	pkg/dispatcher/health_authoritative_survivor_mutation_test.go
pkg/dispatcher/health.go	recordAssignmentObservation	^TestHealthAuthoritativeSurvivorMutation	pkg/dispatcher/health_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	AdvanceIntegrationStep	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	BlockIntegration	^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationIntegrationDurability$)
pkg/dispatcher/review_checkpoint_store.go	CompleteIntegration	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	CreateOrReuse	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	ObserveIntegration	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	PromoteManualIntegration	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	createOrReuseReviewCheckpoint	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	createOrReuseReviewCheckpointAttempt	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	legacyUnlinkedCheckpointIDs	^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationLegacyBinding$)
pkg/dispatcher/review_checkpoint_store.go	requireOneCheckpointRow	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
pkg/dispatcher/review_checkpoint_store.go	validateOpsRunCheckpointIdentity	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
EOF

	got=$(authoritative_pattern_for pkg/dispatcher/other.go '^(applyHealth)$')
	[[ -z "$got" ]] || fail "wrong authoritative source unexpectedly selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/health.go '^(assignmentObservationErrorsLocked)$')
	[[ -z "$got" ]] || fail "zero-survivor health function unexpectedly selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/assignment.go '^(assignBead)$')
	[[ -z "$got" ]] || fail "unmapped assignment function unexpectedly selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/review_checkpoint_store.go '^(LoadOwningForBead)$')
	[[ -z "$got" ]] || fail "non-survivor checkpoint function unexpectedly selected $got"

	local listed review_context_pattern
	review_context_pattern=$(authoritative_pattern_for pkg/dispatcher/ops_runs.go '^(reviewContextForOpsRun)$')
	listed=$(go test -list "$review_context_pattern" ./pkg/dispatcher)
	[[ "$(grep -Ec '^Test' <<<"$listed")" = 2 ]] ||
		fail 'reviewContextForOpsRun mutation owner must select exactly two real tests'
	grep -Fxq TestOpsAuthoritativeSurvivorMutationReviewContexts <<<"$listed" ||
		fail 'reviewContextForOpsRun mutation owner omitted its exact authoritative contract'
	grep -Fxq TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex <<<"$listed" ||
		fail 'reviewContextForOpsRun mutation owner omitted its bounded mutex contract'
}

TestAuthoritativeMutationTargetedScope() {
	local evidence fixture focused_line focused_lines function pattern source target test_file
	while IFS=$'\t' read -r target source function pattern test_file; do
		fixture="$tmp/targeted-$target"
		evidence=$(run_targeted_fixture "$fixture" targeted pass 0 false "$target")
		grep -Fq -- "-list $pattern ./pkg/dispatcher" "$fixture/mutation-list.txt" ||
			fail "$function authoritative mutations omitted exact list preflight"
		grep -F -- "-run $pattern ./pkg/dispatcher" "$fixture/mutation-list.txt" |
			grep -q -- '-coverprofile=' ||
			fail "$function authoritative baseline omitted production coverage"
		jq -e --arg function "$function" --arg pattern "$pattern" \
			'.shards[0].match == "^(" + $function + ")$" and .shards[0].test_pattern == $pattern' \
			"$evidence" >/dev/null || fail "$function authoritative evidence lost exact scope"
		for expected_limit in \
			'PARALLEL_WORKERS=2' \
			'EXEC_TIMEOUT=60' \
			'TIMEOUT_MARGIN=5' \
			'BASE_SHARD_TIMEOUT=240' \
			'MAX_SHARD_TIMEOUT=240'; do
			grep -Fxq "$expected_limit" "$fixture/mutation-args.txt" ||
				fail "$function authoritative boundary omitted $expected_limit"
		done
		if [[ "$test_file" == - ]]; then
			! grep -q '^MUTATION_TEST_FILE=' "$fixture/mutation-args.txt" ||
				fail "$function additive owner conflict silently selected one focused file"
			focused_lines=$(grep -F -- "-run $pattern ./pkg/dispatcher" "$fixture/mutation-list.txt")
			[[ -n "$focused_lines" ]] ||
				fail "$function additive owner conflict omitted full-package focused fallback"
			continue
		fi
		grep -Fxq "MUTATION_TEST_FILE=$test_file" "$fixture/mutation-args.txt" ||
			fail "$function authoritative mutation omitted standalone owner file"
		focused_lines=$(grep -F "$test_file" "$fixture/mutation-list.txt")
		[[ -n "$focused_lines" ]] || fail "$function emitted no focused authoritative argv"
		while IFS= read -r focused_line; do
			[[ "$(grep -oF "$source" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
				fail "$function focused argv must include source exactly once"
			[[ "$(grep -oF "$test_file" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
				fail "$function focused argv must include owner exactly once"
			grep -Fq -- '-timeout 55s' <<<"$focused_line" ||
				fail "$function focused argv omitted internal Go deadline"
			! grep -Fq authoritative_unselected_test.go <<<"$focused_line" ||
				fail "$function focused argv included an unselected test file"
		done <<<"$focused_lines"
	done <<'EOF'
authoritative-assignment	pkg/dispatcher/assignment.go	assignmentInsertFailureAllowsReopen	^TestAssignmentAuthoritativeSurvivorMutation	pkg/dispatcher/assignment_authoritative_survivor_mutation_test.go
authoritative-ops	pkg/dispatcher/ops_runs.go	applyOpsResolve	^TestOpsAuthoritativeSurvivorMutation	pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
authoritative-ops-conflict	pkg/dispatcher/ops_runs.go	reviewContextForOpsRun	^(TestOpsAuthoritativeSurvivorMutationReviewContexts|TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex)$	-
authoritative-health	pkg/dispatcher/health.go	evaluateFactoryHealth	^TestHealthAuthoritativeSurvivorMutation	pkg/dispatcher/health_authoritative_survivor_mutation_test.go
authoritative-health-conflict	pkg/dispatcher/health.go	applyHealth	^(TestHealthAuthoritativeSurvivorMutation|TestApplyHealthReturnsAndReleasesDispatcherMutex$)	-
authoritative-review	pkg/dispatcher/review_checkpoint_store.go	AdvanceIntegrationStep	^TestReviewCheckpointAuthoritativeSurvivorMutation	pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
authoritative-review-block	pkg/dispatcher/review_checkpoint_store.go	BlockIntegration	^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationIntegrationDurability$)	-
authoritative-review-legacy	pkg/dispatcher/review_checkpoint_store.go	legacyUnlinkedCheckpointIDs	^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationLegacyBinding$)	-
EOF
}

TestAuthoritativeMutationCoverage() {
	local coverage coverage_root function functions group listed pattern report
	coverage_root=$(mktemp -d)
	while IFS=$'\t' read -r group pattern; do
		coverage="$coverage_root/$group.out"
		listed=$(go test -list "$pattern" ./pkg/dispatcher)
		grep -Eq "$pattern" <<<"$listed" || fail "$group authoritative owner matched no real tests"
		timeout 60 go test -vet=off -count=1 -timeout 55s -coverprofile="$coverage" \
			-run "$pattern" ./pkg/dispatcher >/dev/null
		report=$(go tool cover -func="$coverage")
		case "$group" in
		assignment)
			functions=$'assignmentInsertFailureAllowsReopen\ncheckpointAssignmentAdmissionAllowed'
			;;
		claim)
			functions=assignBeadWithClaim
			;;
		ops)
			functions=$'CompleteOpsRun\nCreateOpsRun\napplyOpsResolve\ncompleteOpsRunFromStatus\ncreateOpsRun\nfindBlockingOpsRun\nisSQLiteUniqueConstraint\nloadOpsRunByID\nreplaceOpsRun\nreviewContextForOpsRun\nreviewContextFromAnyWorkerLocked\nreviewContextFromWorkerLocked\nrouteOpsRun\nrouteReviewOpsRun\nsupersedeAndRerouteOpsRun\nsupersedeOpsRunForRetry\nterminalOpsRunResult\nwatchReroutedOpsRunResult'
			;;
		health)
			functions=$'applyHealth\nevaluateFactoryHealth\nrecordAssignmentObservation'
			;;
		review)
			functions=$'AdvanceIntegrationStep\nBlockIntegration\nCompleteIntegration\nCreateOrReuse\nObserveIntegration\nPromoteManualIntegration\ncreateOrReuseReviewCheckpoint\ncreateOrReuseReviewCheckpointAttempt\nlegacyUnlinkedCheckpointIDs\nrequireOneCheckpointRow\nvalidateOpsRunCheckpointIdentity'
			;;
		esac
		while IFS= read -r function; do
			awk -v target="$function" '
				$2 == target || $2 ~ ("[.]" target "$") {
					value = $3
					gsub(/%/, "", value)
					if (value + 0 > 0) covered = 1
				}
				END { exit !covered }
			' <<<"$report" || fail "$group authoritative owner has zero $function production coverage"
		done <<<"$functions"
	done <<'EOF'
assignment	^TestAssignmentAuthoritativeSurvivorMutation
claim	^(TestAssignmentClaimAuthoritativeSurvivorMutation|TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$
ops	^(TestOpsAuthoritativeSurvivorMutation|TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex$)
health	^(TestHealthAuthoritativeSurvivorMutation|TestApplyHealthReturnsAndReleasesDispatcherMutex$)
review	^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationIntegrationDurability$|TestReviewCheckpointMutationLegacyBinding$)
EOF
}

touched_function_pattern_for() {
	local base="$1"
	local head="$2"
	local file="$3"
	local function_source
	function_source=$(awk '
		/^touched_function_match\(\)/ { copying = 1 }
		copying { print }
		copying && /^}/ { exit }
	' "$runner")
	bash -c "$function_source"$'\n''touched_function_match "$1" "$2" "$3"' _ "$base" "$head" "$file"
}

TestAssignmentAdmissionTouchedFunctionRouting() {
	local fixture="$1"
	local actual base expected file function got head test_file
	local -a refs
	file=pkg/dispatcher/assignment_admission.go
	mapfile -t refs < <(new_assignment_admission_touched_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	actual=$(
		cd "$fixture"
		touched_function_pattern_for "$base" "$head" "$file"
	)
	expected='^(beginAssignmentAdmission|close|commit)$'
	[[ "$actual" == "$expected" ]] ||
		fail "canonical assignment admission touched functions = $actual, want $expected"

	while IFS=: read -r function expected; do
		got=$(assignment_admission_pattern_for "$file" "^($function)$")
		[[ "$got" == "$expected" ]] ||
			fail "canonical $function mutation owner = $got, want $expected"
		test_file=$(assignment_admission_test_file_for "$file")
		[[ "$test_file" == pkg/dispatcher/buffer_survivor_mutation_test.go ]] ||
			fail "canonical $function mutation file = $test_file, want buffer survivor file"
	done <<'EOF'
beginAssignmentAdmission:^TestBufferAssignmentAdmissionBeginOutcomes$
close:^TestBufferAssignmentAdmissionCloseOutcomes$
commit:^TestBufferAssignmentAdmissionCommitOutcomes$
EOF
}

TestEscalationTouchedFunctionRouting() {
	local fixture="$1"
	local actual base expected head
	local -a refs
	mapfile -t refs < <(new_escalation_touched_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	actual=$(
		cd "$fixture"
		touched_function_pattern_for "$base" "$head" pkg/dispatcher/escalation.go
	)
	expected='^(completeOneShotOpsRunFailureBestEffort|completeOpsRunBestEffort|escalateWithOneShot|handleDecomposeResult|handleDecomposeValidationError|handleEscalationResult|handleFailedEscalationResult|logCompletedEscalationResult|routeExistingRoutableEscalation|routeNewRoutableEscalation|spawnEscalationOneShot)$'
	[[ "$actual" == "$expected" ]] ||
		fail "canonical escalation touched functions = $actual, want $expected"
}

TestAssignmentAdmissionMutationMapping() {
	local function expected got
	while IFS=: read -r function expected; do
		got=$(assignment_admission_pattern_for pkg/dispatcher/assignment_admission.go "^($function)$")
		[[ "$got" == "$expected" ]] ||
			fail "$function mutation owner = $got, want $expected"
	done <<'EOF'
beginAssignmentAdmission:^TestBufferAssignmentAdmissionBeginOutcomes$
close:^TestBufferAssignmentAdmissionCloseOutcomes$
commit:^TestBufferAssignmentAdmissionCommitOutcomes$
EOF
	got=$(assignment_admission_pattern_for pkg/dispatcher/other.go '^(beginAssignmentAdmission)$')
	[[ -z "$got" ]] || fail "wrong assignment admission file unexpectedly selected $got"
	got=$(assignment_admission_pattern_for pkg/dispatcher/assignment_admission.go '^(unmappedAssignmentAdmissionFunction)$')
	[[ -z "$got" ]] || fail "unmapped assignment admission function unexpectedly selected $got"
}

TestAssignmentBCMutationMapping() {
	local function expected got
	while IFS=: read -r function expected; do
		got=$(assignment_bc_pattern_for pkg/dispatcher/assignment.go "^($function)$")
		[[ "$got" == "$expected" ]] ||
			fail "$function mutation owner = $got, want $expected"
	done <<'EOF'
prepareAssignmentWorktree:^TestAssignmentBCPrepareWorktreeOutcomes$
validateExistingWorktreeForReuse:^(TestAssignmentBCValidateDivergedRecoveryOutcomes|TestAssignmentBCValidateCurrentBranchError)$
releaseAssignmentReservationLocked:^TestAssignmentBCReservationReleaseExactState$
attachAssignmentToReservation:^TestAssignmentBCAttachExactStateAndOwnership$
EOF
	got=$(assignment_bc_pattern_for pkg/dispatcher/other.go '^(prepareAssignmentWorktree)$')
	[[ -z "$got" ]] || fail "wrong assignment file unexpectedly selected $got"
	got=$(assignment_bc_pattern_for pkg/dispatcher/assignment.go '^(unmappedAssignmentFunction)$')
	[[ -z "$got" ]] || fail "unmapped assignment function unexpectedly selected $got"
}

TestEscalationSurvivorMutationMapping() {
	local function got
	while IFS= read -r function; do
		got=$(escalation_survivor_pattern_for pkg/dispatcher/escalation.go "^($function)$")
		[[ "$got" == '^TestEscalationSurvivorMutation' ]] ||
			fail "$function escalation mutation owner = $got, want survivor owner"
		got=$(escalation_mutation_test_file_for pkg/dispatcher/escalation.go "^($function)$")
		[[ "$got" == pkg/dispatcher/escalation_survivor_mutation_test.go ]] ||
			fail "$function escalation mutation file = $got, want survivor file"
	done <<'EOF'
completeOneShotOpsRunFailureBestEffort
completeOpsRunBestEffort
escalateWithOneShot
handleDecomposeResult
handleDecomposeValidationError
handleEscalationResult
handleFailedEscalationResult
logCompletedEscalationResult
routeExistingRoutableEscalation
routeNewRoutableEscalation
EOF

	got=$(escalation_survivor_pattern_for pkg/dispatcher/escalation.go '^(spawnEscalationOneShot)$')
	[[ -z "$got" ]] || fail "spawn escalation unexpectedly selected survivor owner $got"
	got=$(escalation_mutation_test_file_for pkg/dispatcher/escalation.go '^(spawnEscalationOneShot)$')
	[[ "$got" == pkg/dispatcher/bounded_mutation_test.go ]] ||
		fail "spawn escalation mutation file = $got, want bounded file"
	got=$(escalation_survivor_pattern_for pkg/dispatcher/other.go '^(escalateWithOneShot)$')
	[[ -z "$got" ]] || fail "wrong escalation source unexpectedly selected $got"
	got=$(escalation_survivor_pattern_for pkg/dispatcher/escalation.go '^(unmappedEscalationFunction)$')
	[[ -z "$got" ]] || fail "unmapped escalation function unexpectedly selected $got"
}

TestMutationOwnerMappingsCoexist() {
	local got
	got=$(authoritative_pattern_for \
		pkg/dispatcher/assignment.go '^(assignmentInsertFailureAllowsReopen)$')
	[[ "$got" == '^TestAssignmentAuthoritativeSurvivorMutation' ]] ||
		fail "coexisting authoritative assignment resolver selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/assignment.go '^(assignBeadWithClaim)$')
	[[ "$got" == '^(TestAssignmentClaimAuthoritativeSurvivorMutation|TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$' ]] ||
		fail "coexisting authoritative assignment claim resolver selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/ops_runs.go '^(reviewContextForOpsRun)$')
	[[ "$got" == '^(TestOpsAuthoritativeSurvivorMutationReviewContexts|TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex)$' ]] ||
		fail "coexisting authoritative ops/bounded resolver selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/health.go '^(applyHealth)$')
	[[ "$got" == '^(TestHealthAuthoritativeSurvivorMutation|TestApplyHealthReturnsAndReleasesDispatcherMutex$)' ]] ||
		fail "coexisting authoritative health/bounded resolver selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/review_checkpoint_store.go '^(BlockIntegration)$')
	[[ "$got" == '^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationIntegrationDurability$)' ]] ||
		fail "coexisting authoritative checkpoint/integration resolver selected $got"
	got=$(authoritative_pattern_for pkg/dispatcher/review_checkpoint_store.go '^(legacyUnlinkedCheckpointIDs)$')
	[[ "$got" == '^(TestReviewCheckpointAuthoritativeSurvivorMutation|TestReviewCheckpointMutationLegacyBinding$)' ]] ||
		fail "coexisting authoritative checkpoint/legacy resolver selected $got"
	got=$(assignment_admission_pattern_for \
		pkg/dispatcher/assignment_admission.go '^(beginAssignmentAdmission)$')
	[[ "$got" == '^TestBufferAssignmentAdmissionBeginOutcomes$' ]] ||
		fail "coexisting assignment admission resolver selected $got"
	got=$(assignment_bc_pattern_for pkg/dispatcher/assignment.go '^(prepareAssignmentWorktree)$')
	[[ "$got" == '^TestAssignmentBCPrepareWorktreeOutcomes$' ]] ||
		fail "coexisting assignment resolver selected $got"
	got=$(review_integration_recovery_pattern_for \
		pkg/dispatcher/review_integration_recovery.go '^(finalizeReviewIntegration)$')
	[[ "$got" == '^TestReviewIntegrationRecoveryMutationFinalize$' ]] ||
		fail "coexisting integration-recovery resolver selected $got"
	got=$(review_checkpoint_pattern_for \
		pkg/dispatcher/review_checkpoint_store.go '^(LoadOwningForBead)$')
	[[ "$got" == '^TestReviewCheckpointMutationOwnershipLoads$' ]] ||
		fail "coexisting review checkpoint resolver selected $got"
	got=$(review_checkpoint_pattern_for \
		pkg/dispatcher/review_checkpoint_store.go '^(BlockIntegration)$')
	[[ "$got" == '^TestReviewCheckpointMutationIntegrationDurability$' ]] ||
		fail "coexisting checkpoint durability resolver selected $got"
	got=$(review_checkpoint_pattern_for \
		pkg/dispatcher/review_checkpoint_store.go '^(legacyUnlinkedCheckpointIDs)$')
	[[ "$got" == '^TestReviewCheckpointMutationLegacyBinding$' ]] ||
		fail "coexisting checkpoint legacy resolver selected $got"
	got=$(escalation_survivor_pattern_for \
		pkg/dispatcher/escalation.go '^(escalateWithOneShot)$')
	[[ "$got" == '^TestEscalationSurvivorMutation' ]] ||
		fail "coexisting escalation resolver selected $got"
	got=$(startup_maintenance_pattern_for \
		pkg/storage/dev_schedule.go '^(reconcileInterruptedWeeklyDevCacheSweep)$')
	[[ "$got" == '^TestWeeklyDevCacheSweepMutationReconciliationBoundaries$' ]] ||
		fail "coexisting weekly sweep resolver selected $got"

	got=$(assignment_admission_pattern_for pkg/dispatcher/assignment.go '^(beginAssignmentAdmission)$')
	[[ -z "$got" ]] || fail "assignment admission resolver accepted wrong source: $got"
	got=$(assignment_admission_pattern_for \
		pkg/dispatcher/assignment_admission.go '^(unmappedAssignmentAdmissionFunction)$')
	[[ -z "$got" ]] || fail "assignment admission resolver accepted unmapped function: $got"
	got=$(assignment_bc_pattern_for pkg/dispatcher/review_integration_recovery.go '^(prepareAssignmentWorktree)$')
	[[ -z "$got" ]] || fail "assignment resolver accepted wrong source: $got"
	got=$(assignment_bc_pattern_for pkg/dispatcher/assignment.go '^(unmappedAssignmentFunction)$')
	[[ -z "$got" ]] || fail "assignment resolver accepted unmapped function: $got"
	got=$(review_integration_recovery_pattern_for pkg/dispatcher/assignment.go '^(finalizeReviewIntegration)$')
	[[ -z "$got" ]] || fail "integration-recovery resolver accepted wrong source: $got"
	got=$(review_integration_recovery_pattern_for \
		pkg/dispatcher/review_integration_recovery.go '^(unmappedIntegrationRecoveryFunction)$')
	[[ -z "$got" ]] || fail "integration-recovery resolver accepted unmapped function: $got"
	got=$(startup_maintenance_pattern_for pkg/storage/dev_schedule.go \
		'^(reconcileInterruptedWeeklyDevCacheSweep|runWeeklyDevCacheProviders)$')
	[[ -z "$got" ]] || fail "weekly sweep resolver accepted a grouped function union: $got"
}

TestEscalationSurvivorMutationCoverage() {
	local coverage coverage_root function listed report
	coverage_root=$(mktemp -d)
	coverage="$coverage_root/escalation.out"
	listed=$(go test -list '^TestEscalationSurvivorMutation' ./pkg/dispatcher)
	grep -q '^TestEscalationSurvivorMutation' <<<"$listed" ||
		fail 'escalation survivor owner pattern matched no real tests'
	timeout 60 go test -vet=off -count=1 -timeout 55s -coverprofile="$coverage" \
		-run '^TestEscalationSurvivorMutation' ./pkg/dispatcher >/dev/null
	report=$(go tool cover -func="$coverage")
	while IFS= read -r function; do
		awk -v target="$function" '
			$2 == target || $2 ~ ("[.]" target "$") {
				value = $3
				gsub(/%/, "", value)
				if (value + 0 > 0) covered = 1
			}
			END { exit !covered }
		' <<<"$report" || fail "$function escalation survivor owner has zero production coverage"
	done <<'EOF'
completeOneShotOpsRunFailureBestEffort
completeOpsRunBestEffort
escalateWithOneShot
handleDecomposeResult
handleDecomposeValidationError
handleEscalationResult
handleFailedEscalationResult
logCompletedEscalationResult
routeExistingRoutableEscalation
routeNewRoutableEscalation
EOF
}

TestReviewCheckpointMutationMapping() {
	local function expected got
	while IFS=: read -r function expected; do
		got=$(review_checkpoint_pattern_for pkg/dispatcher/review_checkpoint_store.go "^($function)$")
		[[ "$got" == "^${expected}$" ]] ||
			fail "$function mutation owner = $got, want ^${expected}$"
	done <<'EOF'
LoadOwningForBead:TestReviewCheckpointMutationOwnershipLoads
LoadForOpsRun:TestReviewCheckpointMutationOwnershipLoads
LoadForOpsRunOrBindLegacy:TestReviewCheckpointMutationLegacyBinding
beginSerializedOwnershipBind:TestReviewCheckpointMutationLegacyBinding
loadCheckpointForOpsRunTx:TestReviewCheckpointMutationLegacyBinding
bindLegacyCheckpointOwnership:TestReviewCheckpointMutationLegacyBinding
legacyUnlinkedCheckpointIDs:TestReviewCheckpointMutationLegacyBinding
bindSingleLegacyCheckpoint:TestReviewCheckpointMutationLegacyBinding
commitAbsentLegacyCheckpointOwnership:TestReviewCheckpointMutationLegacyBinding
ListPendingIntegrations:TestReviewCheckpointMutationIntegrationDurability
BeginIntegration:TestReviewCheckpointMutationIntegrationDurability
BlockIntegration:TestReviewCheckpointMutationIntegrationDurability
EOF
	got=$(review_checkpoint_pattern_for pkg/dispatcher/review_checkpoint_store.go '^(unmappedCheckpointFunction)$')
	[[ -z "$got" ]] || fail "unmapped checkpoint function unexpectedly selected $got"
}

TestReviewIntegrationRecoveryMutationMapping() {
	local expected file function got match pattern
	file=pkg/dispatcher/review_integration_recovery.go
	while IFS=: read -r function expected; do
		match="^($function)$"
		if [[ "$expected" == *'|'* ]]; then
			pattern="^($expected)$"
		else
			pattern="^$expected$"
		fi
		got=$(review_integration_recovery_pattern_for "$file" "$match")
		[[ "$got" == "$pattern" ]] ||
			fail "$function integration-recovery mutation owner = $got, want $pattern"
		got=$(review_integration_recovery_test_file_for "$file")
		[[ "$got" == pkg/dispatcher/review_integration_recovery_mutation_test.go ]] ||
			fail "$function integration-recovery mutation file = $got, want reviewed standalone file"
	done <<'EOF'
completeCheckpointAssignment:TestReviewIntegrationRecoveryMutationCompleteCheckpointAssignment
reviewIntegrationRefSHA:TestReviewIntegrationRecoveryMutationReferenceResolution
reviewIntegrationTargetSHA:TestReviewIntegrationRecoveryMutationReferenceResolution
closeIntegratedBeadOnce:TestReviewIntegrationRecoveryMutationCloseIntegratedBeadOnce
reviewIntegrationAncestor:TestReviewIntegrationRecoveryMutationAncestryAndProof
reviewIntegrationProof:TestReviewIntegrationRecoveryMutationAncestryAndProof
verifyApprovedIntegrationSource:TestReviewIntegrationRecoveryMutationApprovedSourceAndRetry
retryReviewIntegrationMerge:TestReviewIntegrationRecoveryMutationApprovedSourceAndRetry
prepareApprovedReviewIntegration:TestReviewIntegrationRecoveryMutationPrepareAndReconcile
reconcileReviewIntegration:TestReviewIntegrationRecoveryMutationPrepareAndReconcile
finalizeReviewIntegration:TestReviewIntegrationRecoveryMutationFinalize
reconcileManualReviewIntegration:TestReviewIntegrationRecoveryMutationManualAndAutomatic
reconcileAutomaticReviewIntegration:TestReviewIntegrationRecoveryMutationManualAndAutomatic
reconcileReviewIntegrationsOnStartup:TestReviewIntegrationRecoveryMutationStartupListFailure|TestReviewIntegrationRecoveryMutationStartupWrapsCheckpointFailure
EOF
	got=$(review_integration_recovery_pattern_for "$file" '^(unmappedIntegrationRecoveryFunction)$')
	[[ -z "$got" ]] || fail "unmapped integration-recovery function unexpectedly selected $got"
	got=$(review_integration_recovery_pattern_for pkg/dispatcher/review_checkpoint_store.go '^(finalizeReviewIntegration)$')
	[[ -z "$got" ]] || fail "wrong integration-recovery source unexpectedly selected $got"
	got=$(review_integration_recovery_test_file_for pkg/dispatcher/review_checkpoint_store.go)
	[[ -z "$got" ]] || fail "wrong integration-recovery source unexpectedly selected standalone file $got"
}

TestReviewIntegrationRecoveryMutationCoverage() {
	local coverage coverage_root expected function listed match pattern report test_name
	coverage_root=$(mktemp -d)
	while IFS=: read -r function expected; do
		match="^($function)$"
		pattern=$(review_integration_recovery_pattern_for pkg/dispatcher/review_integration_recovery.go "$match")
		listed=$(go test -list "$pattern" ./pkg/dispatcher)
		while IFS= read -r test_name; do
			grep -Fxq "$test_name" <<<"$listed" ||
				fail "$function owner pattern omitted real test $test_name"
		done < <(tr '|' '\n' <<<"$expected")
		[[ "$(grep -Ec '^TestReviewIntegrationRecoveryMutation' <<<"$listed")" == "$(tr '|' '\n' <<<"$expected" | wc -l | tr -d ' ')" ]] ||
			fail "$function owner pattern selected unreviewed integration-recovery tests"
		coverage="$coverage_root/$function.out"
		timeout 60 go test -vet=off -count=1 -timeout 55s -coverprofile="$coverage" \
			-run "$pattern" ./pkg/dispatcher >/dev/null
		report=$(go tool cover -func="$coverage")
		awk -v target="$function" '
			$2 == target || $2 ~ ("[.]" target "$") {
				value = $3
				gsub(/%/, "", value)
				if (value + 0 > 0) covered = 1
			}
			END { exit !covered }
		' <<<"$report" || fail "$function reviewed owner has zero production coverage"
	done <<'EOF'
completeCheckpointAssignment:TestReviewIntegrationRecoveryMutationCompleteCheckpointAssignment
reviewIntegrationRefSHA:TestReviewIntegrationRecoveryMutationReferenceResolution
reviewIntegrationTargetSHA:TestReviewIntegrationRecoveryMutationReferenceResolution
closeIntegratedBeadOnce:TestReviewIntegrationRecoveryMutationCloseIntegratedBeadOnce
reviewIntegrationAncestor:TestReviewIntegrationRecoveryMutationAncestryAndProof
reviewIntegrationProof:TestReviewIntegrationRecoveryMutationAncestryAndProof
verifyApprovedIntegrationSource:TestReviewIntegrationRecoveryMutationApprovedSourceAndRetry
retryReviewIntegrationMerge:TestReviewIntegrationRecoveryMutationApprovedSourceAndRetry
prepareApprovedReviewIntegration:TestReviewIntegrationRecoveryMutationPrepareAndReconcile
reconcileReviewIntegration:TestReviewIntegrationRecoveryMutationPrepareAndReconcile
finalizeReviewIntegration:TestReviewIntegrationRecoveryMutationFinalize
reconcileManualReviewIntegration:TestReviewIntegrationRecoveryMutationManualAndAutomatic
reconcileAutomaticReviewIntegration:TestReviewIntegrationRecoveryMutationManualAndAutomatic
reconcileReviewIntegrationsOnStartup:TestReviewIntegrationRecoveryMutationStartupListFailure|TestReviewIntegrationRecoveryMutationStartupWrapsCheckpointFailure
EOF
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
	epic-branch-admission)
		package_name=dispatcher
		source_file=pkg/dispatcher/epic_branch_admission.go
		test_file=pkg/dispatcher/epic_branch_admission_mutation_test.go
		function_name=withEpicBranchAdmission
		test_names=(TestEpicBranchAdmissionMutationBypassAndClaimPreservation)
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
		test_file=pkg/dispatcher/assignment_behavior_mutation_test.go
		function_name=assignBeadWithClaim
		test_names=(
			TestAssignmentBehaviorMutation
			TestStandaloneAssignmentBehaviorHarnessCaseIsolation
		)
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
	assignment-bc-prepare)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go
		function_name=prepareAssignmentWorktree
		test_names=(TestAssignmentBCPrepareWorktreeOutcomes)
		;;
	assignment-bc-validate)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go
		function_name=validateExistingWorktreeForReuse
		test_names=(TestAssignmentBCValidateDivergedRecoveryOutcomes TestAssignmentBCValidateCurrentBranchError)
		;;
	assignment-bc-release)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go
		function_name=releaseAssignmentReservationLocked
		test_names=(TestAssignmentBCReservationReleaseExactState)
		;;
	assignment-bc-attach)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go
		function_name=attachAssignmentToReservation
		test_names=(TestAssignmentBCAttachExactStateAndOwnership)
		;;
	buffer-admission-begin)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment_admission.go
		test_file=pkg/dispatcher/buffer_survivor_mutation_test.go
		function_name=beginAssignmentAdmission
		test_names=(TestBufferAssignmentAdmissionBeginOutcomes)
		;;
	buffer-admission-close)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment_admission.go
		test_file=pkg/dispatcher/buffer_survivor_mutation_test.go
		function_name=close
		test_names=(TestBufferAssignmentAdmissionCloseOutcomes)
		;;
	buffer-admission-commit)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment_admission.go
		test_file=pkg/dispatcher/buffer_survivor_mutation_test.go
		function_name=commit
		test_names=(TestBufferAssignmentAdmissionCommitOutcomes)
		;;
	authoritative-assignment)
		package_name=dispatcher
		source_file=pkg/dispatcher/assignment.go
		test_file=pkg/dispatcher/assignment_authoritative_survivor_mutation_test.go
		function_name=assignmentInsertFailureAllowsReopen
		test_names=(TestAssignmentAuthoritativeSurvivorMutationInsertFailureDecision)
		;;
	authoritative-ops)
		package_name=dispatcher
		source_file=pkg/dispatcher/ops_runs.go
		test_file=pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
		function_name=applyOpsResolve
		test_names=(TestOpsAuthoritativeSurvivorMutationResolveContracts)
		;;
	authoritative-ops-conflict)
		package_name=dispatcher
		source_file=pkg/dispatcher/ops_runs.go
		test_file=pkg/dispatcher/ops_runs_authoritative_survivor_mutation_test.go
		function_name=reviewContextForOpsRun
		test_names=(TestOpsAuthoritativeSurvivorMutationReviewContexts)
		;;
	authoritative-health)
		package_name=dispatcher
		source_file=pkg/dispatcher/health.go
		test_file=pkg/dispatcher/health_authoritative_survivor_mutation_test.go
		function_name=evaluateFactoryHealth
		test_names=(TestHealthAuthoritativeSurvivorMutationApplyContracts)
		;;
	authoritative-health-conflict)
		package_name=dispatcher
		source_file=pkg/dispatcher/health.go
		test_file=pkg/dispatcher/health_authoritative_survivor_mutation_test.go
		function_name=applyHealth
		test_names=(TestHealthAuthoritativeSurvivorMutationApplyContracts)
		;;
	authoritative-review)
		package_name=dispatcher
		source_file=pkg/dispatcher/review_checkpoint_store.go
		test_file=pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
		function_name=AdvanceIntegrationStep
		test_names=(TestReviewCheckpointAuthoritativeSurvivorMutationTransitionFailureContracts)
		;;
	authoritative-review-block)
		package_name=dispatcher
		source_file=pkg/dispatcher/review_checkpoint_store.go
		test_file=pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
		function_name=BlockIntegration
		test_names=(TestReviewCheckpointAuthoritativeSurvivorMutationTransitionFailureContracts)
		;;
	authoritative-review-legacy)
		package_name=dispatcher
		source_file=pkg/dispatcher/review_checkpoint_store.go
		test_file=pkg/dispatcher/review_checkpoint_authoritative_survivor_mutation_test.go
		function_name=legacyUnlinkedCheckpointIDs
		test_names=(TestReviewCheckpointAuthoritativeSurvivorMutationIdentityValidation)
		;;
	review-integration-recovery)
		package_name=dispatcher
		source_file=pkg/dispatcher/review_integration_recovery.go
		test_file=pkg/dispatcher/review_integration_recovery_mutation_test.go
		function_name=finalizeReviewIntegration
		test_names=(TestReviewIntegrationRecoveryMutationFinalize)
		;;
	escalation-survivor)
		package_name=dispatcher
		source_file=pkg/dispatcher/escalation.go
		test_file=pkg/dispatcher/escalation_survivor_mutation_test.go
		function_name=escalateWithOneShot
		test_names=(TestEscalationSurvivorMutationRouting)
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
	review-checkpoint-owning)
		package_name=dispatcher
		source_file=pkg/dispatcher/review_checkpoint_store.go
		test_file=pkg/dispatcher/review_checkpoint_store_mutation_test.go
		function_name=LoadOwningForBead
		test_names=(TestReviewCheckpointMutationOwnershipLoads)
		head_test_name=TestUnrelatedReviewCheckpointHistory
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
	case "$target" in
	assignment-claim)
		printf 'package dispatcher\n\nfunc TestAssignmentClaimAuthoritativeSurvivorMutation() {}\n' \
			>"$fixture/pkg/dispatcher/assignment_claim_authoritative_survivor_mutation_test.go"
		printf 'package dispatcher\n\nfunc TestAssignmentClaimUnselected() {}\n' \
			>"$fixture/pkg/dispatcher/assignment_claim_unselected_test.go"
		;;
	assignment-bc-*)
		printf 'package dispatcher\n\nfunc TestAssignmentBCUnselected() {}\n' \
			>"$fixture/pkg/dispatcher/assignment_bc_unselected_test.go"
		;;
	buffer-admission-*)
		printf 'package dispatcher\n\nfunc TestBufferAdmissionUnselected() {}\n' \
			>"$fixture/pkg/dispatcher/buffer_unselected_test.go"
		;;
	review-integration-recovery)
		printf 'package dispatcher\n\nfunc TestReviewIntegrationRecoveryUnselected() {}\n' \
			>"$fixture/pkg/dispatcher/review_integration_recovery_unselected_test.go"
		;;
	escalation-survivor)
		printf 'package dispatcher\n\nfunc TestEscalationUnselected() {}\n' \
			>"$fixture/pkg/dispatcher/escalation_unselected_test.go"
		;;
	authoritative-*)
		printf 'package dispatcher\n\nfunc TestAuthoritativeUnselected() {}\n' \
			>"$fixture/pkg/dispatcher/authoritative_unselected_test.go"
		case "$target" in
		authoritative-ops-conflict)
			printf 'package dispatcher\n\nfunc TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex() {}\n' \
				>"$fixture/pkg/dispatcher/bounded_mutation_test.go"
			;;
		authoritative-health-conflict)
			printf 'package dispatcher\n\nfunc TestApplyHealthReturnsAndReleasesDispatcherMutex() {}\n' \
				>"$fixture/pkg/dispatcher/bounded_mutation_test.go"
			;;
		authoritative-review-block)
			printf 'package dispatcher\n\nfunc TestReviewCheckpointMutationIntegrationDurability() {}\n' \
				>"$fixture/pkg/dispatcher/review_checkpoint_store_mutation_test.go"
			;;
		authoritative-review-legacy)
			printf 'package dispatcher\n\nfunc TestReviewCheckpointMutationLegacyBinding() {}\n' \
				>"$fixture/pkg/dispatcher/review_checkpoint_store_mutation_test.go"
			;;
		esac
		;;
	esac
	git -C "$fixture" add go.mod "$source_file" "$test_file"
	case "$target" in
	assignment-claim)
		git -C "$fixture" add \
			pkg/dispatcher/assignment_claim_authoritative_survivor_mutation_test.go \
			pkg/dispatcher/assignment_claim_unselected_test.go
		;;
	assignment-bc-*)
		git -C "$fixture" add pkg/dispatcher/assignment_bc_unselected_test.go
		;;
	buffer-admission-*)
		git -C "$fixture" add pkg/dispatcher/buffer_unselected_test.go
		;;
	review-integration-recovery)
		git -C "$fixture" add pkg/dispatcher/review_integration_recovery_unselected_test.go
		;;
	escalation-survivor)
		git -C "$fixture" add pkg/dispatcher/escalation_unselected_test.go
		;;
	authoritative-*)
		git -C "$fixture" add pkg/dispatcher/authoritative_unselected_test.go
		case "$target" in
		authoritative-ops-conflict | authoritative-health-conflict)
			git -C "$fixture" add pkg/dispatcher/bounded_mutation_test.go
			;;
		authoritative-review-block | authoritative-review-legacy)
			git -C "$fixture" add pkg/dispatcher/review_checkpoint_store_mutation_test.go
			;;
		esac
		;;
	esac
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

new_assignment_admission_touched_fixture() {
	local fixture="$1"
	local base head
	mkdir -p "$fixture/pkg/dispatcher"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf '%s\n' \
		'package dispatcher' \
		'' \
		'type Dispatcher struct{}' \
		'type assignmentAdmission struct{}' \
		'' \
		'func (d *Dispatcher) beginAssignmentAdmission() bool { return false }' \
		'func (a *assignmentAdmission) close() bool { return false }' \
		'func (a *assignmentAdmission) commit() bool { return false }' \
		>"$fixture/pkg/dispatcher/assignment_admission.go"
	git -C "$fixture" add pkg/dispatcher/assignment_admission.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n' \
		'package dispatcher' \
		'' \
		'type Dispatcher struct{}' \
		'type assignmentAdmission struct{}' \
		'' \
		'func (d *Dispatcher) beginAssignmentAdmission() bool { return true }' \
		'func (a *assignmentAdmission) close() bool { return true }' \
		'func (a *assignmentAdmission) commit() bool { return true }' \
		>"$fixture/pkg/dispatcher/assignment_admission.go"
	git -C "$fixture" add pkg/dispatcher/assignment_admission.go
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

new_escalation_touched_fixture() {
	local fixture="$1"
	local base head function
	local -a functions=(
		completeOneShotOpsRunFailureBestEffort
		completeOpsRunBestEffort
		escalateWithOneShot
		handleDecomposeResult
		handleDecomposeValidationError
		handleEscalationResult
		handleFailedEscalationResult
		logCompletedEscalationResult
		routeExistingRoutableEscalation
		routeNewRoutableEscalation
		spawnEscalationOneShot
	)
	mkdir -p "$fixture/pkg/dispatcher"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	{
		printf 'package dispatcher\n\ntype Dispatcher struct{}\n'
		for function in "${functions[@]}"; do
			printf 'func (d *Dispatcher) %s() bool { return false }\n' "$function"
		done
	} >"$fixture/pkg/dispatcher/escalation.go"
	git -C "$fixture" add pkg/dispatcher/escalation.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	{
		printf 'package dispatcher\n\ntype Dispatcher struct{}\n'
		for function in "${functions[@]}"; do
			printf 'func (d *Dispatcher) %s() bool { return true }\n' "$function"
		done
	} >"$fixture/pkg/dispatcher/escalation.go"
	git -C "$fixture" add pkg/dispatcher/escalation.go
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

write_authoritative_touched_file() {
	local path="$1"
	local receiver="$2"
	local value="$3"
	shift 3
	{
		printf 'package dispatcher\n\ntype %s struct{}\n' "$receiver"
		local function
		for function in "$@"; do
			printf 'func (r *%s) %s() bool { return %s }\n' "$receiver" "$function" "$value"
		done
	} >"$path"
}

new_authoritative_touched_fixture() {
	local fixture="$1"
	local base head
	local -a assignment_functions=(assignBeadWithClaim assignmentInsertFailureAllowsReopen checkpointAssignmentAdmissionAllowed)
	local -a ops_functions=(
		CompleteOpsRun CreateOpsRun applyOpsResolve completeOpsRunFromStatus createOpsRun
		findBlockingOpsRun isSQLiteUniqueConstraint loadOpsRunByID replaceOpsRun reviewContextForOpsRun
		reviewContextFromAnyWorkerLocked reviewContextFromWorkerLocked routeOpsRun routeReviewOpsRun
		supersedeAndRerouteOpsRun supersedeOpsRunForRetry terminalOpsRunResult watchReroutedOpsRunResult
	)
	local -a health_functions=(applyHealth evaluateFactoryHealth recordAssignmentObservation)
	local -a review_functions=(
		AdvanceIntegrationStep BlockIntegration CompleteIntegration CreateOrReuse ObserveIntegration
		PromoteManualIntegration createOrReuseReviewCheckpoint createOrReuseReviewCheckpointAttempt
		legacyUnlinkedCheckpointIDs requireOneCheckpointRow validateOpsRunCheckpointIdentity
	)
	mkdir -p "$fixture/pkg/dispatcher"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	write_authoritative_touched_file "$fixture/pkg/dispatcher/assignment.go" AssignmentFixture false "${assignment_functions[@]}"
	write_authoritative_touched_file "$fixture/pkg/dispatcher/ops_runs.go" OpsFixture false "${ops_functions[@]}"
	write_authoritative_touched_file "$fixture/pkg/dispatcher/health.go" HealthFixture false "${health_functions[@]}"
	write_authoritative_touched_file "$fixture/pkg/dispatcher/review_checkpoint_store.go" ReviewFixture false "${review_functions[@]}"
	git -C "$fixture" add pkg/dispatcher
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	write_authoritative_touched_file "$fixture/pkg/dispatcher/assignment.go" AssignmentFixture true "${assignment_functions[@]}"
	write_authoritative_touched_file "$fixture/pkg/dispatcher/ops_runs.go" OpsFixture true "${ops_functions[@]}"
	write_authoritative_touched_file "$fixture/pkg/dispatcher/health.go" HealthFixture true "${health_functions[@]}"
	write_authoritative_touched_file "$fixture/pkg/dispatcher/review_checkpoint_store.go" ReviewFixture true "${review_functions[@]}"
	git -C "$fixture" add pkg/dispatcher
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

TestAuthoritativeTouchedFunctionRouting() {
	local actual base expected file head
	local -a refs
	mapfile -t refs < <(new_authoritative_touched_fixture "$1")
	base=${refs[0]}
	head=${refs[1]}
	while IFS=$'\t' read -r file expected; do
		actual=$(cd "$1" && touched_function_pattern_for "$base" "$head" "$file")
		[[ "$actual" == "$expected" ]] ||
			fail "$file authoritative touched functions = $actual, want $expected"
	done <<'EOF'
pkg/dispatcher/assignment.go	^(assignBeadWithClaim|assignmentInsertFailureAllowsReopen|checkpointAssignmentAdmissionAllowed)$
pkg/dispatcher/ops_runs.go	^(CompleteOpsRun|CreateOpsRun|applyOpsResolve|completeOpsRunFromStatus|createOpsRun|findBlockingOpsRun|isSQLiteUniqueConstraint|loadOpsRunByID|replaceOpsRun|reviewContextForOpsRun|reviewContextFromAnyWorkerLocked|reviewContextFromWorkerLocked|routeOpsRun|routeReviewOpsRun|supersedeAndRerouteOpsRun|supersedeOpsRunForRetry|terminalOpsRunResult|watchReroutedOpsRunResult)$
pkg/dispatcher/health.go	^(applyHealth|evaluateFactoryHealth|recordAssignmentObservation)$
pkg/dispatcher/review_checkpoint_store.go	^(AdvanceIntegrationStep|BlockIntegration|CompleteIntegration|CreateOrReuse|ObserveIntegration|PromoteManualIntegration|createOrReuseReviewCheckpoint|createOrReuseReviewCheckpointAttempt|legacyUnlinkedCheckpointIDs|requireOneCheckpointRow|validateOpsRunCheckpointIdentity)$
EOF
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

write_startup_maintenance_function_fixture() {
	local path="$1"
	local value="$2"
	shift 2
	{
		printf 'package storage\n\n'
		local function
		for function in "$@"; do
			printf 'func %s() bool { return %s }\n' "$function" "$value"
		done
	} >"$path"
}

new_startup_maintenance_function_fixture() {
	local fixture="$1"
	local base head test_name
	local -a functions=(
		RunWeeklyDevCacheSweep
		failInterruptedWeeklyDevCacheSweeps
		interruptedSweepHasLiveController
		interruptedWeeklyDevCacheSweeps
		openInterruptedWeeklyDevCachePauses
		reconcileInterruptedWeeklyDevCacheSweep
		runWeeklyDevCacheProviders
	)
	local -a tests=(
		TestDevCacheSweepTriggersOnSizeThreshold
		TestWeeklyDevCacheDueAndCatchup
		TestWeeklyDevCacheSweepMutationNoSweepReleasesTransaction
		TestWeeklyDevCacheSweepMutationReconciliationBoundaries
		TestWeeklyDevCacheSweepReconcilesInterruptedRun
		TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision
		TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership
		TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged
		TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation
	)
	mkdir -p "$fixture/bin" "$fixture/pkg/storage"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'module mutation.test/startup-maintenance\n\ngo 1.25\n' >"$fixture/go.mod"
	write_startup_maintenance_function_fixture "$fixture/pkg/storage/dev_schedule.go" false "${functions[@]}"
	printf 'package storage\n' >"$fixture/pkg/storage/dev_schedule_test.go"
	for test_name in "${tests[@]}"; do
		printf '\nfunc %s() {}\n' "$test_name" >>"$fixture/pkg/storage/dev_schedule_test.go"
	done
	git -C "$fixture" add go.mod pkg/storage
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	write_startup_maintenance_function_fixture "$fixture/pkg/storage/dev_schedule.go" true "${functions[@]}"
	git -C "$fixture" add pkg/storage/dev_schedule.go
	git -C "$fixture" commit -qm head
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
	printf 'EXEC_TIMEOUT=%s\n' "${MUTATION_EXEC_TIMEOUT:-}" >>"${MUTATION_ARGS_TRACE:?}"
	printf 'TIMEOUT_MARGIN=%s\n' "${MUTATION_TEST_TIMEOUT_MARGIN_SECONDS:-}" >>"${MUTATION_ARGS_TRACE:?}"
	printf 'BASE_SHARD_TIMEOUT=%s\n' "${MUTATION_BASE_SHARD_TIMEOUT_SECONDS:-}" >>"${MUTATION_ARGS_TRACE:?}"
	printf 'MAX_SHARD_TIMEOUT=%s\n' "${MUTATION_MAX_SHARD_TIMEOUT_SECONDS:-}" >>"${MUTATION_ARGS_TRACE:?}"
	if [[ -n "${MUTATION_WORKER_CACHE_WARM_TIMEOUT_SECONDS:-}" ]]; then
		printf 'WORKER_CACHE_WARM_TIMEOUT=%s\n' "$MUTATION_WORKER_CACHE_WARM_TIMEOUT_SECONDS" >>"${MUTATION_ARGS_TRACE:?}"
	fi
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
		*TestDevCacheSweepTriggersOnSizeThreshold*) printf 'TestDevCacheSweepTriggersOnSizeThreshold\nTestWeeklyDevCacheDueAndCatchup\n' ;;
		*TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision*) printf 'TestWeeklyDevCacheSweepReconcilesInterruptedRun\nTestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision\n' ;;
		*TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation*) printf 'TestWeeklyDevCacheSweepReconcilesInterruptedRun\nTestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation\n' ;;
		*TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged*) printf 'TestWeeklyDevCacheSweepReconcilesInterruptedRun\nTestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged\n' ;;
		*TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership*) printf 'TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership\n' ;;
		*TestWeeklyDevCacheSweepMutationNoSweepReleasesTransaction*) printf 'TestWeeklyDevCacheSweepMutationNoSweepReleasesTransaction\n' ;;
		*TestWeeklyDevCacheSweepMutationReconciliationBoundaries*) printf 'TestWeeklyDevCacheSweepMutationReconciliationBoundaries\n' ;;
		*TestWeeklyDevCacheDueAndCatchup*) printf 'TestWeeklyDevCacheDueAndCatchup\n' ;;
		*TestInstallAgentBranchGuard*) printf 'TestInstallAgentBranchGuard\n' ;;
		*TestHookPathsWouldLeak*) printf 'TestHookPathsWouldLeak\nTestHookPathsWouldLeak_NonTmpdirSandboxRoot\nTestHookPathsWouldLeak_NonstandardGoTempRoot\nTestInstallCodexHookConfigRefusesLeakyHooks\n' ;;
		*TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease*) printf 'TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease\n' ;;
		*TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound*) printf 'TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound\n' ;;
		*TestRetrySQLiteBusyOperation*) printf 'TestRetrySQLiteBusyOperation\n' ;;
		*TestEpicBranchAdmissionMutationBypassAndClaimPreservation*) printf 'TestEpicBranchAdmissionMutationBypassAndClaimPreservation\n' ;;
		*TestReviewCheckpointStartupOrdering*) printf 'TestReviewCheckpointStartupOrdering\n' ;;
		*TestAssignmentClaimAuthoritativeSurvivorMutation*) printf 'TestAssignmentClaimAuthoritativeSurvivorMutation\nTestAssignmentBehaviorMutation\nTestStandaloneAssignmentBehaviorHarnessCaseIsolation\n' ;;
		*TestAssignmentBehaviorMutation*) printf 'TestAssignmentBehaviorMutation\nTestStandaloneAssignmentBehaviorHarnessCaseIsolation\n' ;;
		*TestAssignBeadWithClaimReportsUnclaimedValidationFailure*) printf 'TestAssignBeadWithClaimReportsUnclaimedValidationFailure\n' ;;
		*TestReleaseAssignmentReservationResetsStateAndUnlocks*) printf 'TestReleaseAssignmentReservationResetsStateAndUnlocks\n' ;;
		*TestAssignmentBCPrepareWorktreeOutcomes*) printf 'TestAssignmentBCPrepareWorktreeOutcomes\n' ;;
		*TestAssignmentBCValidateDivergedRecoveryOutcomes*) printf 'TestAssignmentBCValidateDivergedRecoveryOutcomes\nTestAssignmentBCValidateCurrentBranchError\n' ;;
		*TestAssignmentBCReservationReleaseExactState*) printf 'TestAssignmentBCReservationReleaseExactState\n' ;;
		*TestAssignmentBCAttachExactStateAndOwnership*) printf 'TestAssignmentBCAttachExactStateAndOwnership\n' ;;
		*TestBufferAssignmentAdmissionBeginOutcomes*) printf 'TestBufferAssignmentAdmissionBeginOutcomes\n' ;;
		*TestBufferAssignmentAdmissionCloseOutcomes*) printf 'TestBufferAssignmentAdmissionCloseOutcomes\n' ;;
		*TestBufferAssignmentAdmissionCommitOutcomes*) printf 'TestBufferAssignmentAdmissionCommitOutcomes\n' ;;
		*TestReviewIntegrationRecoveryMutationFinalize*) printf 'TestReviewIntegrationRecoveryMutationFinalize\n' ;;
		*TestEscalationSurvivorMutation*) printf 'TestEscalationSurvivorMutationRouting\n' ;;
		*TestAssignmentAuthoritativeSurvivorMutation*) printf 'TestAssignmentAuthoritativeSurvivorMutationInsertFailureDecision\n' ;;
		*TestOpsAuthoritativeSurvivorMutation*) printf 'TestOpsAuthoritativeSurvivorMutationResolveContracts\nTestOpsAuthoritativeSurvivorMutationReviewContexts\nTestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex\n' ;;
		*TestHealthAuthoritativeSurvivorMutation*) printf 'TestHealthAuthoritativeSurvivorMutationApplyContracts\nTestApplyHealthReturnsAndReleasesDispatcherMutex\n' ;;
		*TestReviewCheckpointAuthoritativeSurvivorMutation*) printf 'TestReviewCheckpointAuthoritativeSurvivorMutationTransitionFailureContracts\nTestReviewCheckpointAuthoritativeSurvivorMutationIdentityValidation\nTestReviewCheckpointMutationIntegrationDurability\nTestReviewCheckpointMutationLegacyBinding\n' ;;
		*TestSpawnEscalationOneShotReturnsAfterReadingWorktree*) printf 'TestSpawnEscalationOneShotReturnsAfterReadingWorktree\n' ;;
		*TestApplyHealthReturnsAndReleasesDispatcherMutex*) printf 'TestApplyHealthReturnsAndReleasesDispatcherMutex\n' ;;
		*TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex*) printf 'TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex\n' ;;
		*TestReviewCheckpointMutationOwnershipLoads*) printf 'TestReviewCheckpointMutationOwnershipLoads\n' ;;
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
	printf 'pkg/dispatcher/epic_branch_admission.go:1: withEpicBranchAdmission 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:1: assignBeadWithClaim 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:2: releaseAssignmentReservation 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:7: assignmentInsertFailureAllowsReopen 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:8: checkpointAssignmentAdmissionAllowed 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:3: prepareAssignmentWorktree 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:4: validateExistingWorktreeForReuse 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:5: releaseAssignmentReservationLocked 100.0%%\n'
	printf 'pkg/dispatcher/assignment.go:6: attachAssignmentToReservation 100.0%%\n'
	printf 'pkg/dispatcher/assignment_admission.go:1: beginAssignmentAdmission 100.0%%\n'
	printf 'pkg/dispatcher/assignment_admission.go:2: close 100.0%%\n'
	printf 'pkg/dispatcher/assignment_admission.go:3: commit 100.0%%\n'
	printf 'pkg/dispatcher/review_integration_recovery.go:1: finalizeReviewIntegration 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:1: completeOneShotOpsRunFailureBestEffort 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:2: completeOpsRunBestEffort 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:3: escalateWithOneShot 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:4: handleDecomposeResult 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:5: handleDecomposeValidationError 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:6: handleEscalationResult 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:7: handleFailedEscalationResult 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:8: logCompletedEscalationResult 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:9: routeExistingRoutableEscalation 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:10: routeNewRoutableEscalation 100.0%%\n'
	printf 'pkg/dispatcher/escalation.go:1: spawnEscalationOneShot 100.0%%\n'
	printf 'pkg/dispatcher/health.go:1: applyHealth 100.0%%\n'
	printf 'pkg/dispatcher/health.go:2: evaluateFactoryHealth 100.0%%\n'
	printf 'pkg/dispatcher/health.go:3: recordAssignmentObservation 100.0%%\n'
	printf 'pkg/dispatcher/ops_runs.go:1: reviewContextForOpsRun 100.0%%\n'
	printf 'pkg/dispatcher/ops_runs.go:2: applyOpsResolve 100.0%%\n'
	printf 'pkg/dispatcher/review_checkpoint_store.go:1: LoadOwningForBead 100.0%%\n'
	printf 'pkg/dispatcher/review_checkpoint_store.go:2: AdvanceIntegrationStep 100.0%%\n'
	printf 'pkg/dispatcher/review_checkpoint_store.go:3: BlockIntegration 100.0%%\n'
	printf 'pkg/dispatcher/review_checkpoint_store.go:4: legacyUnlinkedCheckpointIDs 100.0%%\n'
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

run_startup_maintenance_function_fixture() {
	local fixture="$1"
	local base head evidence status args_trace list_trace
	mapfile -t refs < <(new_startup_maintenance_function_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	args_trace="$fixture/mutation-args.txt"
	list_trace="$fixture/mutation-list.txt"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE=targeted \
			MUTATION_ARGS_TRACE="$args_trace" MUTATION_LIST_TRACE="$list_trace" \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	if [[ "$status" != 0 ]]; then
		cat "$fixture/runner.log" >&2
		fail "startup maintenance function fixture exit = $status, want 0"
	fi
	jq -e '.conclusion == "pass"' "$evidence" >/dev/null ||
		fail 'startup maintenance function fixture did not pass'
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
	local apply_health_pattern checkpoint_pattern claim_focused_line claim_focused_lines claim_pattern escalation_pattern evidence fixture args_trace history_pattern list_trace release_pattern review_context_pattern scheduling_pattern start_pattern
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

	epic_admission_pattern='^TestEpicBranchAdmissionMutationBypassAndClaimPreservation$'
	evidence=$(run_targeted_fixture "$tmp/targeted-epic-branch-admission" targeted pass 0 false epic-branch-admission)
	grep -Fq -- "-list $epic_admission_pattern ./pkg/dispatcher" \
		"$tmp/targeted-epic-branch-admission/mutation-list.txt" ||
		fail 'withEpicBranchAdmission mutations must preflight the bounded admission owner'
	jq -e --arg pattern "$epic_admission_pattern" \
		'.shards[0].match == "^(withEpicBranchAdmission)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null ||
		fail 'withEpicBranchAdmission mutations must select only the bounded admission owner'
	! grep -Fq 'TestTryAssign_ConcentratesWorkersOnTopEpic' \
		"$tmp/targeted-epic-branch-admission/mutation-list.txt" ||
		fail 'withEpicBranchAdmission focused argv included a co-changed scheduling test'

	history_pattern='^(TestReviewCheckpointStartupOrdering)$'
	evidence=$(run_targeted_fixture "$tmp/targeted-history" targeted pass 0 false history)
	grep -Fq -- "-list $history_pattern ./pkg/dispatcher" "$tmp/targeted-history/mutation-list.txt" ||
		fail 'dispatcher mutations must preflight tests co-changed with their production file'
	jq -e --arg pattern "$history_pattern" \
		'.shards[0].match == "^(startupRecovery)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'dispatcher mutation evidence must preserve its deterministic co-changed test scope'

	checkpoint_pattern='^TestReviewCheckpointMutationOwnershipLoads$'
	evidence=$(run_targeted_fixture "$tmp/targeted-review-checkpoint" targeted pass 0 false review-checkpoint-owning)
	grep -Fq -- "-list $checkpoint_pattern ./pkg/dispatcher" "$tmp/targeted-review-checkpoint/mutation-list.txt" ||
		fail 'review checkpoint mutations must preflight the exact reviewed owner'
	grep -F -- "-run $checkpoint_pattern ./pkg/dispatcher" "$tmp/targeted-review-checkpoint/mutation-list.txt" |
		grep -q -- '-coverprofile=' || fail 'review checkpoint baseline must retain full-package coverage preflight'
	jq -e --arg pattern "$checkpoint_pattern" \
		'.shards[0].match == "^(LoadOwningForBead)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'review checkpoint mutation evidence must preserve exact function and owner scope'
	grep -Fxq 'MUTATION_TEST_FILE=pkg/dispatcher/review_checkpoint_store_mutation_test.go' \
		"$tmp/targeted-review-checkpoint/mutation-args.txt" ||
		fail 'review checkpoint mutations must compile only the reviewed focused test file'
	! grep -Fq 'review_checkpoint_store_mutation_test.go' "$tmp/targeted-review-checkpoint/mutation-list.txt" ||
		fail 'review checkpoint focused file leaked into list or coverage baseline'

	claim_pattern='^(TestAssignmentClaimAuthoritativeSurvivorMutation|TestAssignmentBehaviorMutation|TestStandaloneAssignmentBehaviorHarnessCaseIsolation)$'
	evidence=$(run_targeted_fixture "$tmp/targeted-assignment-claim" targeted pass 0 false assignment-claim)
	grep -Fq -- "-list $claim_pattern ./pkg/dispatcher" "$tmp/targeted-assignment-claim/mutation-list.txt" ||
		fail 'assignBeadWithClaim mutations must preflight authoritative and bounded callback contracts'
	grep -F -- "-run $claim_pattern ./pkg/dispatcher" "$tmp/targeted-assignment-claim/mutation-list.txt" |
		grep -q -- '-coverprofile=' ||
		fail 'assignBeadWithClaim baseline must retain full-package production coverage'
	jq -e --arg pattern "$claim_pattern" \
		'.shards[0].match == "^(assignBeadWithClaim)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'assignBeadWithClaim mutations lost their additive owner scope'
	! grep -q '^MUTATION_TEST_FILE=' "$tmp/targeted-assignment-claim/mutation-args.txt" ||
		fail 'assignBeadWithClaim additive owners silently selected only one focused file'
	for expected_limit in \
		'PARALLEL_WORKERS=2' \
		'EXEC_TIMEOUT=60' \
		'TIMEOUT_MARGIN=5' \
		'BASE_SHARD_TIMEOUT=1800' \
		'MAX_SHARD_TIMEOUT=1800' \
		'WORKER_CACHE_WARM_TIMEOUT=120'; do
		grep -Fxq "$expected_limit" "$tmp/targeted-assignment-claim/mutation-args.txt" ||
			fail "assignBeadWithClaim mutation boundary omitted $expected_limit"
	done
	grep -Fxq 'mutation shard capacity: mutants=2 workers=2 effective_timeout=1800s emergency_cap=1800s' \
		"$tmp/targeted-assignment-claim/runner.log" ||
		fail 'assignBeadWithClaim mutations did not reserve their claim-specific shard capacity'
	claim_focused_lines=$(grep -F -- "-timeout 55s -run $claim_pattern " \
		"$tmp/targeted-assignment-claim/mutation-list.txt" || true)
	[[ -n "$claim_focused_lines" ]] ||
		fail 'assignBeadWithClaim emitted no full-package focused mutation argv'
	while IFS= read -r claim_focused_line; do
		grep -Fq -- "-run $claim_pattern mutation.test/targeted/pkg/dispatcher" <<<"$claim_focused_line" ||
			fail 'assignBeadWithClaim focused argv omitted its full package import path'
		! grep -Eq 'assignment_(behavior|claim_authoritative|claim_unselected)_.*_test[.]go' <<<"$claim_focused_line" ||
			fail 'assignBeadWithClaim full-package fallback silently selected a single owner or unselected file'
	done <<<"$claim_focused_lines"

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
	! grep -q '^WORKER_CACHE_WARM_TIMEOUT=' "$tmp/targeted-assignment-release/mutation-args.txt" ||
		fail 'non-claim dispatcher mutations must not opt into worker cache prewarming'

	local assignment_bc_function assignment_bc_target assignment_bc_test_file focused_line focused_lines
	assignment_bc_test_file=pkg/dispatcher/assignment_reservation_worktree_survivor_mutation_test.go
	while IFS=$'\t' read -r assignment_bc_target assignment_bc_function assignment_bc_pattern; do
		fixture="$tmp/targeted-$assignment_bc_target"
		evidence=$(run_targeted_fixture "$fixture" targeted pass 0 false "$assignment_bc_target")
		list_trace="$fixture/mutation-list.txt"
		grep -Fq -- "-list $assignment_bc_pattern ./pkg/dispatcher" "$list_trace" ||
			fail "$assignment_bc_function mutations must preflight the exact reviewed test pattern"
		grep -F -- "-run $assignment_bc_pattern ./pkg/dispatcher" "$list_trace" |
			grep -q -- '-coverprofile=' ||
			fail "$assignment_bc_function baseline must retain full-package production coverage"
		jq -e --arg function "$assignment_bc_function" --arg pattern "$assignment_bc_pattern" \
			'.shards[0].match == "^(" + $function + ")$" and .shards[0].test_pattern == $pattern' \
			"$evidence" >/dev/null ||
			fail "$assignment_bc_function mutation evidence lost its exact function/test mapping"
		grep -Fxq "MUTATION_TEST_FILE=$assignment_bc_test_file" "$fixture/mutation-args.txt" ||
			fail "$assignment_bc_function mutations must compile only the reviewed B+C test file"
		for expected_limit in \
			'PARALLEL_WORKERS=2' \
			'EXEC_TIMEOUT=60' \
			'TIMEOUT_MARGIN=5' \
			'BASE_SHARD_TIMEOUT=240' \
			'MAX_SHARD_TIMEOUT=240'; do
			grep -Fxq "$expected_limit" "$fixture/mutation-args.txt" ||
				fail "$assignment_bc_function mutation boundary omitted $expected_limit"
		done
		focused_lines=$(grep -F "$assignment_bc_test_file" "$list_trace")
		[[ -n "$focused_lines" ]] || fail "$assignment_bc_function emitted no focused mutation argv"
		while IFS= read -r focused_line; do
			[[ "$(grep -oF 'pkg/dispatcher/assignment.go' <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
				fail "$assignment_bc_function focused argv must include the mutated source exactly once"
			[[ "$(grep -oF "$assignment_bc_test_file" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
				fail "$assignment_bc_function focused argv must include the reviewed test file exactly once"
			grep -Fq -- '-timeout 55s' <<<"$focused_line" ||
				fail "$assignment_bc_function focused argv must enforce the 55s internal Go deadline"
			! grep -Fq 'assignment_bc_unselected_test.go' <<<"$focused_line" ||
				fail "$assignment_bc_function focused argv included an unselected test file"
		done <<<"$focused_lines"
	done <<'EOF'
assignment-bc-prepare	prepareAssignmentWorktree	^TestAssignmentBCPrepareWorktreeOutcomes$
assignment-bc-validate	validateExistingWorktreeForReuse	^(TestAssignmentBCValidateDivergedRecoveryOutcomes|TestAssignmentBCValidateCurrentBranchError)$
assignment-bc-release	releaseAssignmentReservationLocked	^TestAssignmentBCReservationReleaseExactState$
assignment-bc-attach	attachAssignmentToReservation	^TestAssignmentBCAttachExactStateAndOwnership$
EOF

	local admission_function admission_pattern admission_target admission_test_file
	admission_test_file=pkg/dispatcher/buffer_survivor_mutation_test.go
	while IFS=$'\t' read -r admission_target admission_function admission_pattern; do
		fixture="$tmp/targeted-$admission_target"
		evidence=$(run_targeted_fixture "$fixture" targeted pass 0 false "$admission_target")
		list_trace="$fixture/mutation-list.txt"
		grep -Fq -- "-list $admission_pattern ./pkg/dispatcher" "$list_trace" ||
			fail "$admission_function mutations must preflight the exact buffer survivor test"
		grep -F -- "-run $admission_pattern ./pkg/dispatcher" "$list_trace" |
			grep -q -- '-coverprofile=' ||
			fail "$admission_function baseline must retain full-package production coverage"
		jq -e --arg function "$admission_function" --arg pattern "$admission_pattern" \
			'.shards[0].match == "^(" + $function + ")$" and .shards[0].test_pattern == $pattern' \
			"$evidence" >/dev/null ||
			fail "$admission_function mutation evidence lost its exact function/test mapping"
		grep -Fxq "MUTATION_TEST_FILE=$admission_test_file" "$fixture/mutation-args.txt" ||
			fail "$admission_function mutations must compile only the buffer survivor test file"
		for expected_limit in \
			'PARALLEL_WORKERS=2' \
			'EXEC_TIMEOUT=60' \
			'TIMEOUT_MARGIN=5' \
			'BASE_SHARD_TIMEOUT=240' \
			'MAX_SHARD_TIMEOUT=240'; do
			grep -Fxq "$expected_limit" "$fixture/mutation-args.txt" ||
				fail "$admission_function mutation boundary omitted $expected_limit"
		done
		focused_lines=$(grep -F "$admission_test_file" "$list_trace")
		[[ -n "$focused_lines" ]] || fail "$admission_function emitted no focused mutation argv"
		while IFS= read -r focused_line; do
			[[ "$(grep -oF 'pkg/dispatcher/assignment_admission.go' <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
				fail "$admission_function focused argv must include the mutated source exactly once"
			[[ "$(grep -oF "$admission_test_file" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
				fail "$admission_function focused argv must include the buffer test file exactly once"
			grep -Fq -- '-timeout 55s' <<<"$focused_line" ||
				fail "$admission_function focused argv must enforce the 55s internal Go deadline"
			! grep -Fq 'buffer_unselected_test.go' <<<"$focused_line" ||
				fail "$admission_function focused argv included an unselected test file"
		done <<<"$focused_lines"
	done <<'EOF'
buffer-admission-begin	beginAssignmentAdmission	^TestBufferAssignmentAdmissionBeginOutcomes$
buffer-admission-close	close	^TestBufferAssignmentAdmissionCloseOutcomes$
buffer-admission-commit	commit	^TestBufferAssignmentAdmissionCommitOutcomes$
EOF

	local integration_file integration_pattern integration_test_file
	integration_file=pkg/dispatcher/review_integration_recovery.go
	integration_pattern='^TestReviewIntegrationRecoveryMutationFinalize$'
	integration_test_file=pkg/dispatcher/review_integration_recovery_mutation_test.go
	fixture="$tmp/targeted-review-integration-recovery"
	evidence=$(run_targeted_fixture "$fixture" targeted pass 0 false review-integration-recovery)
	list_trace="$fixture/mutation-list.txt"
	grep -Fq -- "-list $integration_pattern ./pkg/dispatcher" "$list_trace" ||
		fail 'integration-recovery mutations must preflight their reviewed owner'
	jq -e --arg pattern "$integration_pattern" \
		'.shards[0].match == "^(finalizeReviewIntegration)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'integration-recovery evidence lost exact owner mapping'
	grep -Fxq "MUTATION_TEST_FILE=$integration_test_file" "$fixture/mutation-args.txt" ||
		fail 'integration-recovery mutations must compile only the reviewed standalone file'
	for expected_limit in \
		'PARALLEL_WORKERS=2' \
		'EXEC_TIMEOUT=60' \
		'TIMEOUT_MARGIN=5' \
		'BASE_SHARD_TIMEOUT=240' \
		'MAX_SHARD_TIMEOUT=240'; do
		grep -Fxq "$expected_limit" "$fixture/mutation-args.txt" ||
			fail "integration-recovery mutation boundary omitted $expected_limit"
	done
	focused_lines=$(grep -F "$integration_test_file" "$list_trace")
	[[ -n "$focused_lines" ]] || fail 'integration-recovery emitted no focused mutation argv'
	while IFS= read -r focused_line; do
		[[ "$(grep -oF "$integration_file" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
			fail 'integration-recovery focused argv must include source exactly once'
		[[ "$(grep -oF "$integration_test_file" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
			fail 'integration-recovery focused argv must include owner exactly once'
		grep -Fq -- '-timeout 55s' <<<"$focused_line" ||
			fail 'integration-recovery focused argv must enforce the 55s Go deadline'
		! grep -Fq 'review_integration_recovery_unselected_test.go' <<<"$focused_line" ||
			fail 'integration-recovery focused argv included an unselected test file'
	done <<<"$focused_lines"

	local escalation_survivor_pattern escalation_survivor_test_file
	escalation_survivor_pattern='^TestEscalationSurvivorMutation'
	escalation_survivor_test_file=pkg/dispatcher/escalation_survivor_mutation_test.go
	fixture="$tmp/targeted-escalation-survivor"
	evidence=$(run_targeted_fixture "$fixture" targeted pass 0 false escalation-survivor)
	list_trace="$fixture/mutation-list.txt"
	grep -Fq -- "-list $escalation_survivor_pattern ./pkg/dispatcher" "$list_trace" ||
		fail 'escalation survivor mutations must preflight their reviewed owner'
	grep -F -- "-run $escalation_survivor_pattern ./pkg/dispatcher" "$list_trace" |
		grep -q -- '-coverprofile=' ||
		fail 'escalation survivor baseline must retain full-package production coverage'
	jq -e --arg pattern "$escalation_survivor_pattern" \
		'.shards[0].match == "^(escalateWithOneShot)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'escalation survivor evidence lost exact owner mapping'
	grep -Fxq "MUTATION_TEST_FILE=$escalation_survivor_test_file" "$fixture/mutation-args.txt" ||
		fail 'escalation survivor mutations must compile only the reviewed standalone file'
	for expected_limit in \
		'PARALLEL_WORKERS=2' \
		'EXEC_TIMEOUT=60' \
		'TIMEOUT_MARGIN=5' \
		'BASE_SHARD_TIMEOUT=240' \
		'MAX_SHARD_TIMEOUT=240'; do
		grep -Fxq "$expected_limit" "$fixture/mutation-args.txt" ||
			fail "escalation survivor mutation boundary omitted $expected_limit"
	done
	focused_lines=$(grep -F "$escalation_survivor_test_file" "$list_trace")
	[[ -n "$focused_lines" ]] || fail 'escalation survivor emitted no focused mutation argv'
	while IFS= read -r focused_line; do
		[[ "$(grep -oF 'pkg/dispatcher/escalation.go' <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
			fail 'escalation survivor focused argv must include source exactly once'
		[[ "$(grep -oF "$escalation_survivor_test_file" <<<"$focused_line" | wc -l | tr -d ' ')" = 1 ]] ||
			fail 'escalation survivor focused argv must include owner exactly once'
		grep -Fq -- '-timeout 55s' <<<"$focused_line" ||
			fail 'escalation survivor focused argv must enforce the 55s Go deadline'
		! grep -Fq 'escalation_unselected_test.go' <<<"$focused_line" ||
			fail 'escalation survivor focused argv included an unselected test file'
	done <<<"$focused_lines"

	escalation_pattern='^TestSpawnEscalationOneShotReturnsAfterReadingWorktree$'
	evidence=$(run_targeted_fixture "$tmp/targeted-escalation-one-shot" targeted pass 0 false escalation-one-shot)
	grep -Fq -- "-list $escalation_pattern ./pkg/dispatcher" "$tmp/targeted-escalation-one-shot/mutation-list.txt" ||
		fail 'spawnEscalationOneShot mutations must preflight the bounded worktree-lock contract'
	jq -e --arg pattern "$escalation_pattern" \
		'.shards[0].match == "^(spawnEscalationOneShot)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'spawnEscalationOneShot mutations must select only the bounded worktree-lock contract'
	grep -Fxq 'MUTATION_TEST_FILE=pkg/dispatcher/bounded_mutation_test.go' \
		"$tmp/targeted-escalation-one-shot/mutation-args.txt" ||
		fail 'spawnEscalationOneShot mutations must preserve their bounded standalone file'

	apply_health_pattern='^(TestHealthAuthoritativeSurvivorMutation|TestApplyHealthReturnsAndReleasesDispatcherMutex$)'
	evidence=$(run_targeted_fixture "$tmp/targeted-health-apply" targeted pass 0 false health-apply)
	grep -Fq -- "-list $apply_health_pattern ./pkg/dispatcher" "$tmp/targeted-health-apply/mutation-list.txt" ||
		fail 'applyHealth mutations must preflight authoritative and bounded mutex-release contracts'
	jq -e --arg pattern "$apply_health_pattern" \
		'.shards[0].match == "^(applyHealth)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'applyHealth mutations must preserve authoritative and bounded mutex-release contracts'

	review_context_pattern='^(TestOpsAuthoritativeSurvivorMutationReviewContexts|TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex)$'
	evidence=$(run_targeted_fixture "$tmp/targeted-ops-review-context" targeted pass 0 false ops-review-context)
	grep -Fq -- "-list $review_context_pattern ./pkg/dispatcher" "$tmp/targeted-ops-review-context/mutation-list.txt" ||
		fail 'reviewContextForOpsRun mutations must preflight authoritative and bounded mutex-release contracts'
	jq -e --arg pattern "$review_context_pattern" \
		'.shards[0].match == "^(reviewContextForOpsRun)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'reviewContextForOpsRun mutations must preserve authoritative and bounded mutex-release contracts'

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
	local original="$fixture/pkg/dispatcher/review_checkpoint_store.go"
	local changed="$fixture/changed.go"
	local output="$fixture/exec.log"
	local status
	mkdir -p "$fixture/bin" "$fixture/pkg/dispatcher"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"${MUTATION_FOCUSED_TRACE:?}"
exit 0
EOF
	chmod +x "$fixture/bin/go"
	printf 'package dispatcher\n\nfunc LoadOwningForBead() int { return 1 }\n' >"$original"
	printf 'package dispatcher\n\nfunc LoadOwningForBead() int { return 2 }\n' >"$changed"
	printf 'package dispatcher\n\nfunc TestReviewCheckpointMutationOwnershipLoads() {}\n' \
		>"$fixture/pkg/dispatcher/review_checkpoint_store_mutation_test.go"
	printf 'package dispatcher\n\nfunc TestUnselected() {}\n' >"$fixture/pkg/dispatcher/unselected_test.go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FOCUSED_TRACE="$fixture/focused-args.txt" \
			MUTATE_CHANGED="$changed" MUTATE_ORIGINAL="$original" MUTATE_PACKAGE=./pkg/dispatcher \
			MUTATE_TIMEOUT=5 MUTATION_TEST_PATTERN=TestReviewCheckpointMutationOwnershipLoads \
			MUTATION_TEST_FILE=pkg/dispatcher/review_checkpoint_store_mutation_test.go \
			bash "$repo_root/scripts/quality_gate/mutation_exec.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 1 ]] || fail "focused surviving mutant exit = $status, want 1"
	[[ "$(grep -oF 'pkg/dispatcher/review_checkpoint_store.go' "$fixture/focused-args.txt" | wc -l | tr -d ' ')" = 1 ]] ||
		fail 'focused mutation compile must include the real mutated checkpoint source exactly once'
	[[ "$(grep -oF 'pkg/dispatcher/review_checkpoint_store_mutation_test.go' "$fixture/focused-args.txt" | wc -l | tr -d ' ')" = 1 ]] ||
		fail 'focused mutation compile must include the reviewed checkpoint owner exactly once'
	! grep -q 'pkg/dispatcher/unselected_test.go' "$fixture/focused-args.txt" ||
		fail 'focused mutation compile included an unselected test file'

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FOCUSED_TRACE="$fixture/unexpected-args.txt" \
			MUTATE_CHANGED="$changed" MUTATE_ORIGINAL="$original" MUTATE_PACKAGE=./pkg/dispatcher \
			MUTATE_TIMEOUT=5 MUTATION_TEST_PATTERN=TestReviewCheckpointMutationOwnershipLoads \
			MUTATION_TEST_FILE=pkg/missing/review_checkpoint_store_mutation_test.go \
			bash "$repo_root/scripts/quality_gate/mutation_exec.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 2 ]] || fail "missing focused test directory exit = $status, want 2"
	grep -q '^ORO_MUTATION_EXEC_FAILURE:2$' "$output" ||
		fail 'missing focused test directory did not emit a durable infrastructure marker'
	[[ ! -e "$fixture/unexpected-args.txt" ]] ||
		fail 'missing focused test directory invoked go test'
}

test_review_integration_recovery_mutation_exec_focused_file() {
	local fixture="$1"
	local original="$fixture/pkg/dispatcher/review_integration_recovery.go"
	local changed="$fixture/changed.go"
	local output="$fixture/exec.log"
	local status
	mkdir -p "$fixture/bin" "$fixture/pkg/dispatcher"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"${MUTATION_FOCUSED_TRACE:?}"
exit 0
EOF
	chmod +x "$fixture/bin/go"
	printf 'package dispatcher\n\nfunc finalizeReviewIntegration() int { return 1 }\n' >"$original"
	printf 'package dispatcher\n\nfunc finalizeReviewIntegration() int { return 2 }\n' >"$changed"
	printf 'package dispatcher\n\nfunc TestReviewIntegrationRecoveryMutationFinalize() {}\n' \
		>"$fixture/pkg/dispatcher/review_integration_recovery_mutation_test.go"
	printf 'package dispatcher\n\nfunc TestUnselectedReviewIntegrationRecovery() {}\n' \
		>"$fixture/pkg/dispatcher/review_integration_recovery_unselected_test.go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FOCUSED_TRACE="$fixture/focused-args.txt" \
			MUTATE_CHANGED="$changed" MUTATE_ORIGINAL="$original" MUTATE_PACKAGE=./pkg/dispatcher \
			MUTATE_TIMEOUT=60 MUTATION_TEST_TIMEOUT=55 \
			MUTATION_TEST_PATTERN='^TestReviewIntegrationRecoveryMutationFinalize$' \
			MUTATION_TEST_FILE=pkg/dispatcher/review_integration_recovery_mutation_test.go \
			bash "$repo_root/scripts/quality_gate/mutation_exec.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 1 ]] || fail "integration-recovery focused surviving mutant exit = $status, want 1"
	[[ "$(grep -oF 'pkg/dispatcher/review_integration_recovery.go' "$fixture/focused-args.txt" | wc -l | tr -d ' ')" = 1 ]] ||
		fail 'focused mutation compile must include mutated integration-recovery source exactly once'
	[[ "$(grep -oF 'pkg/dispatcher/review_integration_recovery_mutation_test.go' "$fixture/focused-args.txt" | wc -l | tr -d ' ')" = 1 ]] ||
		fail 'focused mutation compile must include reviewed integration-recovery owner exactly once'
	! grep -q 'review_integration_recovery_unselected_test.go' "$fixture/focused-args.txt" ||
		fail 'focused mutation compile included an unselected integration-recovery test file'
}

TestReviewIntegrationRecoveryMutationFocusedExec() {
	local focused_tmp
	focused_tmp=$(mktemp -d)
	test_review_integration_recovery_mutation_exec_focused_file "$focused_tmp/exec-review-integration-recovery-focused"
	rm -rf -- "$focused_tmp"
}

test_mutation_exec_internal_deadline_kills_hung_mutant() {
	local fixture="$1"
	local original="$fixture/pkg/example/value.go"
	local changed="$fixture/changed.go"
	local output="$fixture/exec.log"
	local status
	mkdir -p "$fixture/cache" "$fixture/pkg/example" "$fixture/tmp"
	printf 'module example.test/hung\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$original"
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$changed"
	cat >"$fixture/pkg/example/value_test.go" <<'EOF'
package example

import (
	"testing"
	"time"
)

func TestHungMutant(*testing.T) { time.Sleep(time.Hour) }
EOF

	SECONDS=0
	set +e
	(
		cd "$fixture"
		GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATE_CHANGED="$changed" MUTATE_ORIGINAL="$original" MUTATE_PACKAGE=./pkg/example \
			MUTATE_DEBUG=true MUTATE_TIMEOUT=5 MUTATION_TEST_TIMEOUT=1 MUTATION_TEST_PATTERN='^TestHungMutant$' \
			MUTATION_TEST_FILE="$fixture/pkg/example/value_test.go" \
			bash "$repo_root/scripts/quality_gate/mutation_exec.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 0 ]] || fail "hung mutant internal deadline exit = $status, want killed status 0"
	((SECONDS < 5)) || fail 'hung mutant reached the outer mutation execution timeout'
	grep -q '^panic: test timed out after 1s$' "$output" || {
		cat "$output" >&2
		fail 'hung mutant did not fail at its internal Go test deadline'
	}
	! grep -Eq '^ORO_MUTATION_EXEC_(TIMEOUT|FAILURE):?' "$output" ||
		fail 'hung mutant was classified as mutation infrastructure'
	grep -q 'Value() int { return 1 }' "$original" ||
		fail 'hung mutant internal deadline did not restore the original source'
}

TestMutationExecInternalDeadline() {
	local deadline_tmp
	deadline_tmp=$(mktemp -d)
	test_mutation_exec_internal_deadline_kills_hung_mutant "$deadline_tmp/exec-hung"
	rm -rf -- "$deadline_tmp"
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
			MUTATION_EXEC_TIMEOUT=6 MUTATION_PARALLEL_WORKERS=2 \
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
printf '%s\n' "${MUTATION_TEST_TIMEOUT:-unset}" >"$MUTATION_FAKE_STATE/internal-test-timeout"
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
	grep -Fxq 55 "$fixture/state/internal-test-timeout" ||
		fail "$mutant_count-mutant shard did not reserve a 5s margin below its 60s executor deadline"
}

run_parallel_worker_cache_prewarm_fixture() {
	local fixture="$1"
	local mode="$2"
	local output="$fixture/parallel.log"
	local evidence_path status warm_timeout=10
	[[ "$mode" != timeout ]] || warm_timeout=2
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/cache" "$fixture/tmp" "$fixture/state" "$fixture/evidence"
	printf 'module example.test/prewarm\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	git hash-object "$fixture/pkg/example/value.go" >"$fixture/state/original.hash"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" = tool && "$2" = go-mutesting ]]; then
	source_file=${*: -1}
	generation="$MUTATION_FAKE_STATE/generated"
	mkdir -p "$generation/$(dirname "$source_file")"
	cp "$source_file" "$generation/$source_file.original"
	for index in 0 1; do
		sed "s/return 1/return $((index + 2))/" "$source_file" >"$generation/$source_file.$index"
	done
	printf 'Save mutations into %q\n' "$generation"
	exit 0
fi
[[ "$1" = test ]] || exit 64
if compgen -G "$MUTATION_FAILURE_EVIDENCE_DIR/run.*/mutant-*.json" >/dev/null; then
	: >"$MUTATION_FAKE_STATE/prewarm-after-record"
fi
git hash-object "$MUTATION_SOURCE_FILE" >>"$MUTATION_FAKE_STATE/prewarm-source-hashes"
printf '%s\n' "$GOCACHE" >>"$MUTATION_FAKE_STATE/prewarm-caches"
slot=""
while [[ -z "$slot" ]]; do
	for candidate in 1 2; do
		if mkdir "$MUTATION_FAKE_STATE/prewarm-slot-$candidate" 2>/dev/null; then
			slot=$candidate
			break
		fi
	done
done
trap 'rmdir "$MUTATION_FAKE_STATE/prewarm-slot-$slot"' EXIT
if [[ -d "$MUTATION_FAKE_STATE/prewarm-slot-1" && -d "$MUTATION_FAKE_STATE/prewarm-slot-2" ]]; then
	: >"$MUTATION_FAKE_STATE/reached-two-prewarm-workers"
fi
case "$MUTATION_FAKE_MODE" in
success)
	sleep 0.1
	: >"$GOCACHE/prewarm-complete"
	exit 0
	;;
timeout)
	sleep 5
	;;
nonzero)
	exit 17
	;;
esac
EOF
	cat >"$fixture/bin/mutation-exec" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ -f "$GOCACHE/prewarm-complete" ]] || exit 66
printf '%s\n' "$MUTATE_CHANGED" >>"$MUTATION_FAKE_STATE/executions"
git hash-object "$MUTATE_ORIGINAL" >>"$MUTATION_FAKE_STATE/execution-source-before"
cp "$MUTATE_ORIGINAL" "$MUTATE_ORIGINAL.test-backup"
cp "$MUTATE_CHANGED" "$MUTATE_ORIGINAL"
mv "$MUTATE_ORIGINAL.test-backup" "$MUTATE_ORIGINAL"
git hash-object "$MUTATE_ORIGINAL" >>"$MUTATION_FAKE_STATE/execution-source-after"
exit 0
EOF
	chmod +x "$fixture/bin/go" "$fixture/bin/mutation-exec"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" MUTATION_FAKE_MODE="$mode" \
			GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_FAILURE_EVIDENCE_DIR="$fixture/evidence" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
			MUTATION_EXEC_TIMEOUT=60 MUTATION_TEST_TIMEOUT_MARGIN_SECONDS=1 MUTATION_PARALLEL_WORKERS=2 \
			MUTATION_WORKER_CACHE_WARM_TIMEOUT_SECONDS="$warm_timeout" \
			MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
			bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
	)
	status=$?
	set -e

	if [[ "$mode" = success ]]; then
		[[ "$status" = 0 ]] || fail "worker cache prewarm fixture exit = $status, want 0"
		[[ -f "$fixture/state/reached-two-prewarm-workers" ]] ||
			fail 'worker cache prewarm did not populate both isolated caches concurrently'
		[[ ! -e "$fixture/state/prewarm-after-record" ]] ||
			fail 'worker cache prewarm started after mutant evidence clocks'
		[[ "$(sort -u "$fixture/state/prewarm-caches" | wc -l | tr -d ' ')" = 2 ]] ||
			fail 'worker cache prewarm reused one cache across workers'
		[[ "$(wc -l <"$fixture/state/executions" | tr -d ' ')" = 2 ]] ||
			fail 'worker cache prewarm changed mutation execution cardinality'
		[[ "$(sort -u "$fixture/state/executions" | wc -l | tr -d ' ')" = 2 ]] ||
			fail 'worker cache prewarm duplicated a mutant execution'
		for hashes in prewarm-source-hashes execution-source-before execution-source-after; do
			[[ "$(sort -u "$fixture/state/$hashes")" = "$(<"$fixture/state/original.hash")" ]] ||
				fail "worker cache prewarm changed source identity in $hashes"
		done
		grep -Fxq 'The mutation score is 1.000000 (2 passed, 0 failed, 0 duplicated, 0 skipped, total is 2)' "$output" ||
			fail 'worker cache prewarm changed the mutation denominator'
		evidence_path=$(find "$fixture/evidence" -mindepth 1 -maxdepth 1 -type d -name 'run.*')
		jq -s -e 'length == 2 and map(.mutant_index) == [0, 1] and all(.exit_class == "killed")' \
			"$evidence_path"/mutant-*.json >/dev/null ||
			fail 'worker cache prewarm changed mutant evidence mapping or cardinality'
		return
	fi

	[[ "$status" = 2 ]] || fail "$mode worker cache prewarm exit = $status, want 2"
	evidence_path=$(sed -n 's/^ORO_MUTATION_FAILURE_EVIDENCE://p' "$output")
	[[ "$(wc -l <<<"$evidence_path" | tr -d ' ')" = 1 && -d "$evidence_path" ]] ||
		fail "$mode worker cache prewarm omitted its durable failure marker"
	[[ ! -e "$fixture/state/executions" ]] ||
		fail "$mode worker cache prewarm executed mutants after setup failure"
	! compgen -G "$evidence_path/mutant-*.json" >/dev/null ||
		fail "$mode worker cache prewarm published mutant records after setup failure"
	if [[ "$mode" = timeout ]]; then
		grep -Fxq 'ORO_MUTATION_EXEC_FAILURE:124' "$output" ||
			fail 'timed out worker cache prewarm lost its fail-closed status'
	else
		grep -Fxq 'ORO_MUTATION_EXEC_FAILURE:17' "$output" ||
			fail 'failed worker cache prewarm lost its fail-closed status'
	fi
}

test_parallel_worker_cache_prewarm() {
	local fixture="$1"
	run_parallel_worker_cache_prewarm_fixture "$fixture/success" success
	run_parallel_worker_cache_prewarm_fixture "$fixture/timeout" timeout
	run_parallel_worker_cache_prewarm_fixture "$fixture/nonzero" nonzero
}

run_parallel_completion_handshake_fixture() {
	local fixture="$1"
	local mode="$2"
	local output="$fixture/parallel.log"
	local evidence_path real_mv status
	real_mv=$(command -v mv)
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/cache" "$fixture/tmp" "$fixture/state"
	printf 'module example.test/completion\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
source_file=${*: -1}
generation="$MUTATION_FAKE_STATE/generated"
mkdir -p "$generation/$(dirname "$source_file")"
cp "$source_file" "$generation/$source_file.original"
sed 's/return 1/return 2/' "$source_file" >"$generation/$source_file.0"
printf 'Save mutations into %q\n' "$generation"
EOF
	cat >"$fixture/bin/mutation-exec" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
	cat >"$fixture/bin/mv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination=${*: -1}
case "$MUTATION_COMPLETION_MODE:$destination" in
missing_done:*.done)
	exit 0
	;;
invalid_done:*.done)
	printf 'not-the-worker\n' >"$destination"
	exit 0
	;;
missing_result:*/results/*.tsv)
	exit 0
	;;
esac
exec "$MUTATION_REAL_MV" "$@"
EOF
	chmod +x "$fixture/bin/go" "$fixture/bin/mutation-exec" "$fixture/bin/mv"

	SECONDS=0
	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
			MUTATION_COMPLETION_MODE="$mode" MUTATION_REAL_MV="$real_mv" \
			MUTATION_FAILURE_EVIDENCE_DIR="$fixture/evidence" \
			GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
			MUTATION_EXEC_TIMEOUT=60 MUTATION_PARALLEL_WORKERS=2 \
			MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
			timeout -k 1 5 bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = 2 ]] || fail "$mode completion handshake exit = $status, want 2"
	grep -Fxq 'ORO_MUTATION_EXEC_FAILURE:2' "$output" ||
		fail "$mode completion handshake did not emit an infrastructure marker"
	evidence_path=$(sed -n 's/^ORO_MUTATION_FAILURE_EVIDENCE://p' "$output")
	[[ "$(wc -l <<<"$evidence_path" | tr -d ' ')" = 1 && -d "$evidence_path" ]] ||
		fail "$mode completion handshake did not publish one absolute failure evidence path"
	((SECONDS < 5)) || fail "$mode completion handshake waited for the outer timeout"
}

test_parallel_completion_handshakes() {
	local fixture="$1"
	run_parallel_completion_handshake_fixture "$fixture/missing-done" missing_done
	run_parallel_completion_handshake_fixture "$fixture/invalid-done" invalid_done
	run_parallel_completion_handshake_fixture "$fixture/missing-result" missing_result
}

run_parallel_abnormal_teardown_fixture() {
	local fixture="$1"
	local mode="$2"
	local elapsed_seconds evidence_path output="$fixture/parallel.log" peer_group peer_pid status
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/cache" "$fixture/tmp" "$fixture/state"
	printf 'module example.test/teardown\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
source_file=${*: -1}
generation="$MUTATION_FAKE_STATE/generated"
mkdir -p "$generation/$(dirname "$source_file")"
cp "$source_file" "$generation/$source_file.original"
sed 's/return 1/return 2/' "$source_file" >"$generation/$source_file.0"
printf 'Save mutations into %q\n' "$generation"
EOF
	cat >"$fixture/bin/mutation-exec" <<'EOF'
#!/usr/bin/env bash
trap '' TERM
printf '%d\n' "$$" >"$MUTATION_FAKE_STATE/peer.pid"
sleep 30
EOF
	chmod +x "$fixture/bin/go" "$fixture/bin/mutation-exec"

	SECONDS=0
	set +e
	(
		cd "$fixture"
		if [[ "$mode" = signal ]]; then
			PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
				MUTATION_FAILURE_EVIDENCE_DIR="$fixture/evidence" \
				GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
				MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
				MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
				MUTATION_EXEC_TIMEOUT=60 MUTATION_PARALLEL_WORKERS=2 \
				MUTATION_BASE_SHARD_TIMEOUT_SECONDS=1 MUTATION_MAX_SHARD_TIMEOUT_SECONDS=6 \
				MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
				bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1 &
			parallel_pid=$!
			for ((attempt = 0; attempt < 200; attempt++)); do
				if compgen -G "$fixture/evidence/run.*/mutant-0.json" >/dev/null &&
					[[ -s "$fixture/state/peer.pid" ]]; then
					break
				fi
				sleep 0.01
			done
			peer_pid=$(<"$fixture/state/peer.pid")
			peer_group=$(ps -o pgid= -p "$peer_pid" | tr -d ' ')
			printf '%s\n' "$peer_group" >"$fixture/state/peer.pgid"
			kill -TERM "$parallel_pid"
			wait "$parallel_pid"
		else
			PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
				MUTATION_FAILURE_EVIDENCE_DIR="$fixture/evidence" \
				GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
				MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
				MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
				MUTATION_EXEC_TIMEOUT=60 MUTATION_PARALLEL_WORKERS=2 \
				MUTATION_BASE_SHARD_TIMEOUT_SECONDS=1 MUTATION_MAX_SHARD_TIMEOUT_SECONDS=6 \
				MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
				timeout -k 1 8 bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
		fi
	)
	status=$?
	set -e
	elapsed_seconds=$SECONDS
	[[ "$status" = 124 ]] || fail "$mode teardown exit = $status, want 124"
	evidence_path=$(sed -n 's/^ORO_MUTATION_FAILURE_EVIDENCE://p' "$output")
	[[ "$(wc -l <<<"$evidence_path" | tr -d ' ')" = 1 && -d "$evidence_path" ]] ||
		fail "$mode teardown did not publish one absolute failure evidence path"
	jq -e '.source_file == "pkg/example/value.go" and .function_match == "^(Value)$" and
		.mutant_index == 0 and .exit_class == "aborted" and .exit_status == null' \
		"$evidence_path/mutant-0.json" >/dev/null || fail "$mode teardown lost its aborted mutant identity"
	peer_pid=$(<"$fixture/state/peer.pid")
	! kill -0 "$peer_pid" 2>/dev/null || fail "$mode teardown orphaned mutant executor $peer_pid"
	if [[ -s "$fixture/state/peer.pgid" ]]; then
		peer_group=$(<"$fixture/state/peer.pgid")
		! kill -0 -- "-$peer_group" 2>/dev/null || fail "$mode teardown orphaned mutant worker group $peer_group"
	fi
	((elapsed_seconds < 8)) || fail "$mode teardown reached its outer diagnostic timeout"
}

test_parallel_abnormal_teardown_evidence() {
	local fixture="$1"
	run_parallel_abnormal_teardown_fixture "$fixture/signal" signal
	run_parallel_abnormal_teardown_fixture "$fixture/deadline" deadline
}

test_parallel_internal_timeout_margin_validation() {
	local fixture="$1"
	local margin output status
	mkdir -p "$fixture/cache" "$fixture/tmp"
	for margin in 0 5; do
		output="$fixture/margin-$margin.log"
		set +e
		GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_EXEC_TIMEOUT=5 \
			MUTATION_TEST_TIMEOUT_MARGIN_SECONDS="$margin" MUTATION_PARALLEL_WORKERS=2 \
			bash "$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
		status=$?
		set -e
		[[ "$status" = 2 ]] || fail "internal test timeout margin $margin exit = $status, want 2"
		grep -Fxq 'ORO_MUTATION_EXEC_FAILURE:2' "$output" ||
			fail "internal test timeout margin $margin did not emit an infrastructure marker"
	done
}

run_parallel_marker_fixture() {
	local fixture="$1"
	local mode="$2"
	local expected_marker="$3"
	local output="$fixture/parallel.log"
	local descendant_alive=0 diagnostic_line elapsed_seconds evidence_line evidence_path expected_exit_class expected_exit_status group_alive=0 group_id peer_pid peer_sleep=30 sentinel_alive=0 sentinel_pid status
	[[ "$mode" != ordinary ]] || peer_sleep=1
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/cache" "$fixture/tmp" "$fixture/state"
	printf 'module example.test/markers\n\ngo 1.26\n' >"$fixture/go.mod"
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package example\n\nfunc TestValue() {}\n' >"$fixture/pkg/example/value_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

trap '' TERM
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

wait_for_peer() {
	for ((attempt = 0; attempt < 100; attempt++)); do
		[[ -s "$MUTATION_FAKE_STATE/peer.pid" ]] && return
		sleep 0.01
	done
	exit 65
}

case "$MUTATION_FAKE_MODE:${MUTATE_CHANGED##*.}" in
ordinary:0)
	trap '' TERM
	sleep 30 &
	printf '%d\n' "$!" >"$MUTATION_FAKE_STATE/orphan.pid"
	exit 0
	;;
timeout:0)
	wait_for_peer
	printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
	exit 124
	;;
raw124:0)
	wait_for_peer
	for ((diagnostic_line = 0; diagnostic_line < 300; diagnostic_line++)); do
		printf 'raw124 diagnostic line %03d\n' "$diagnostic_line"
	done
	exit 124
	;;
unknown:0)
	wait_for_peer
	printf 'UNKOWN exit code for synthetic mutation executor\n'
	exit 2
	;;
infra:0)
	wait_for_peer
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
	;;
*:1)
	printf '%d\n' "$$" >"$MUTATION_FAKE_STATE/peer.pid"
	if [[ "$MUTATION_FAKE_MODE" = ordinary ]]; then
		sleep "${MUTATION_FAKE_PEER_SLEEP:?}"
		exit 1
	fi
	exec sleep "${MUTATION_FAKE_PEER_SLEEP:?}"
	;;
*)
	exit 0
	;;
esac
EOF
	chmod +x "$fixture/bin/go" "$fixture/bin/mutation-exec"

	sleep 30 &
	sentinel_pid=$!
	SECONDS=0
	set +e
	# shellcheck disable=SC2016 # nested shell expands the TERM-resistant kill shim
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FAKE_STATE="$fixture/state" \
			MUTATION_FAKE_MODE="$mode" MUTATION_FAKE_PEER_SLEEP="$peer_sleep" \
			MUTATION_KILL_TRACE="$fixture/state/kills.log" \
			MUTATION_FAILURE_EVIDENCE_DIR="$fixture/evidence" \
			GOCACHE="$fixture/cache" GOTMPDIR="$fixture/tmp" \
			MUTATION_SOURCE_FILE=pkg/example/value.go MUTATION_FUNCTION_MATCH='^(Value)$' \
			MUTATION_TEST_PATTERN='^TestValue$' MUTATION_TEST_FILE=pkg/example/value_test.go \
			MUTATION_EXEC_TIMEOUT=60 MUTATION_PARALLEL_WORKERS=2 \
			MUTATION_BASE_SHARD_TIMEOUT_SECONDS=240 MUTATION_MAX_SHARD_TIMEOUT_SECONDS=900 \
			MUTATION_EXEC_SCRIPT="$fixture/bin/mutation-exec" \
			timeout -k 1 3 bash -c '
				kill() {
					case "$1" in
					-0) builtin kill "$@" ;;
					-KILL)
						printf "KILL %s\n" "$*" >>"$MUTATION_KILL_TRACE"
						builtin kill "$@"
						;;
					*)
						printf "TERM %s\n" "$*" >>"$MUTATION_KILL_TRACE"
						return 0
						;;
					esac
				}
				export -f kill
				exec bash "$1"
			' _ \
				"$repo_root/scripts/quality_gate/mutation_parallel.sh" >"$output" 2>&1
	)
	status=$?
	set -e
	elapsed_seconds=$SECONDS
	if [[ "$mode" = ordinary ]]; then
		peer_pid=$(<"$fixture/state/orphan.pid")
		kill -0 "$peer_pid" 2>/dev/null && descendant_alive=1
		kill -0 "$sentinel_pid" 2>/dev/null && sentinel_alive=1
		group_id=$(awk '$1 == "KILL" { group = $NF } END { sub(/^-/, "", group); print group }' \
			"$fixture/state/kills.log" 2>/dev/null || true)
		if [[ -n "$group_id" ]] && kill -0 -- "-$group_id" 2>/dev/null; then
			group_alive=1
		fi
		kill -KILL "$peer_pid" 2>/dev/null || true
		kill "$sentinel_pid" 2>/dev/null || true
		wait "$sentinel_pid" 2>/dev/null || true
		[[ "$status" = 0 ]] || fail 'ordinary killed/survived statuses triggered fail-fast termination'
		grep -Eq '^The mutation score is 0\.750000 \(3 passed, 1 failed, 0 duplicated, 0 skipped, total is 4\)$' "$output" ||
			fail 'ordinary killed/survived statuses did not preserve the complete denominator'
		grep -q '^KILL ' "$fixture/state/kills.log" || fail 'ordinary completion did not tear down its owned worker groups'
		((descendant_alive == 0)) || fail "ordinary completion orphaned descendant $peer_pid"
		((group_alive == 0)) || fail "ordinary completion left owned process group $group_id alive"
		((sentinel_alive == 1)) || fail "ordinary completion killed unrelated process $sentinel_pid"
		((elapsed_seconds < 3)) || fail "ordinary completion waited ${elapsed_seconds}s for an orphaned descendant"
		return
	fi
	peer_pid=$(<"$fixture/state/peer.pid")
	kill -0 "$peer_pid" 2>/dev/null && descendant_alive=1
	kill -0 "$sentinel_pid" 2>/dev/null && sentinel_alive=1
	group_id=$(awk '$1 == "KILL" { group = $NF } END { sub(/^-/, "", group); print group }' \
		"$fixture/state/kills.log" 2>/dev/null || true)
	if [[ -n "$group_id" ]] && kill -0 -- "-$group_id" 2>/dev/null; then
		group_alive=1
	fi
	kill -KILL "$peer_pid" 2>/dev/null || true
	kill "$sentinel_pid" 2>/dev/null || true
	wait "$sentinel_pid" 2>/dev/null || true
	[[ "$status" != 0 ]] || fail "$mode marker was accepted as a completed mutation campaign"
	grep -Fxq "$expected_marker" "$output" || fail "$mode marker was not surfaced"
	evidence_path=$(sed -n 's/^ORO_MUTATION_FAILURE_EVIDENCE://p' "$output")
	[[ "$(wc -l <<<"$evidence_path" | tr -d ' ')" = 1 && -d "$evidence_path" ]] ||
		fail "$mode marker did not publish one failure evidence directory"
	if [[ "$mode" = raw124 ]]; then
		evidence_line=$(grep -n '^ORO_MUTATION_FAILURE_EVIDENCE:' "$output" | cut -d: -f1)
		diagnostic_line=$(grep -n '^raw124 diagnostic line ' "$output" | head -1 | cut -d: -f1)
		((evidence_line < diagnostic_line)) || fail 'raw124 failure evidence path was hidden behind arbitrary mutant output'
	fi
	case "$mode" in
	timeout | raw124)
		expected_exit_class=timeout
		expected_exit_status=124
		;;
	unknown | infra)
		expected_exit_class=infrastructure
		expected_exit_status=2
		;;
	esac
	jq -e --arg source_file pkg/example/value.go --arg function_match '^(Value)$' \
		--arg exit_class "$expected_exit_class" --argjson exit_status "$expected_exit_status" \
		'.source_file == $source_file and .function_match == $function_match and
			.mutant_index == 0 and (.mutant_path | startswith("/")) and
			.content_hash_algorithm == "git-blob" and (.content_hash | test("^[0-9a-f]{40,64}$")) and
			.exit_class == $exit_class and
			.exit_status == $exit_status' "$evidence_path/mutant-0.json" >/dev/null ||
		fail "$mode marker did not retain exact mutant identity and exit evidence"
	jq -e '.exit_class == "aborted" and .exit_status == null' "$evidence_path/mutant-1.json" >/dev/null ||
		fail "$mode marker did not retain its aborted peer identity"
	grep -q '^TERM ' "$fixture/state/kills.log" || fail "$mode marker did not attempt bounded group termination"
	grep -q '^KILL ' "$fixture/state/kills.log" || fail "$mode marker did not escalate the TERM-resistant group"
	((descendant_alive == 0)) || fail "$mode marker orphaned TERM-resistant descendant $peer_pid"
	((group_alive == 0)) || fail "$mode marker left owned process group $group_id alive"
	((sentinel_alive == 1)) || fail "$mode marker killed unrelated process $sentinel_pid"
	((elapsed_seconds < 3)) || fail "$mode marker waited ${elapsed_seconds}s for a sleeping peer"
}

test_parallel_marker_fail_fast() {
	local fixture="$1"
	run_parallel_marker_fixture "$fixture/timeout" timeout ORO_MUTATION_EXEC_TIMEOUT
	run_parallel_marker_fixture "$fixture/raw124" raw124 ORO_MUTATION_EXEC_FAILURE:124
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
	grep -Fxq 1800 "$fixture/mutation-timeouts.txt" ||
		fail 'parallel shard outer boundary did not reserve its 1800s claim-specific emergency ceiling'
	grep -Fxq 'mutation shard capacity: mutants=2 workers=2 effective_timeout=1800s emergency_cap=1800s' \
		"$fixture/runner.log" ||
		fail 'claim shard did not reserve its emergency ceiling as effective capacity'
}

TestMutationCapacity() {
	local tmp
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	run_parallel_capacity_fixture "$tmp/single" 1 240
	run_parallel_capacity_fixture "$tmp/small" 3 240
	run_parallel_capacity_fixture "$tmp/cold-190" 190 570
	run_parallel_capacity_fixture "$tmp/capped" 302 900
	test_parallel_marker_fail_fast "$tmp/markers"
	test_parallel_completion_handshakes "$tmp/completion"
	test_parallel_abnormal_teardown_evidence "$tmp/abnormal-teardown"
	test_parallel_internal_timeout_margin_validation "$tmp/internal-timeout-margin"
	test_parallel_worker_cache_prewarm "$tmp/worker-cache-prewarm"
	test_parallel_emergency_ceiling "$tmp/ceiling"
}

TestIncrementalMutationArtifactRetention() {
	local tmp="$1"
	mkdir -p "$tmp"
	awk '
		/^  incremental-mutation:$/ { in_job = 1; next }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ { exit }
		in_job { print }
	' "$repo_root/.github/workflows/ci.yml" >"$tmp/incremental-mutation.yml"
	grep -q 'actions/upload-artifact' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must upload its JSON evidence artifact'
	grep -Fxq '          path: |' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation artifact upload must accept multiple evidence paths'
	grep -Fxq '            mutation-evidence.json' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must retain its aggregate JSON evidence'
	grep -Fxq '            mutation-failures/' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must retain durable per-mutant failure evidence'
	grep -q 'if-no-files-found: error' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation artifact loss must fail the job'
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
	test_review_integration_recovery_mutation_exec_focused_file "$tmp/exec-review-integration-recovery-focused"
	TestMutationExecInternalDeadline
	test_parallel_mutant_executor "$tmp/parallel-mutants"
	TestMutationCapacity
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
	TestReviewCheckpointMutationMapping
	TestReviewIntegrationRecoveryMutationMapping
	TestReviewIntegrationRecoveryMutationCoverage
	TestEscalationSurvivorMutationCoverage
	TestAssignmentBCMutationMapping
	TestAssignmentAdmissionMutationMapping
	TestAssignmentAdmissionTouchedFunctionRouting "$tmp/assignment-admission-touched"
	TestEscalationSurvivorMutationMapping
	TestAuthoritativeMutationMapping
	TestAuthoritativeMutationTargetedScope
	TestAuthoritativeMutationCoverage
	TestAuthoritativeTouchedFunctionRouting "$tmp/authoritative-touched"
	TestEscalationTouchedFunctionRouting "$tmp/escalation-touched"
	TestMutationOwnerMappingsCoexist
	TestP0DurabilityMutationMapping
	TestStartupMaintenanceMutationMapping
	TestStartupMaintenanceMutationSharding
	TestCmdMutationSharding

	TestIncrementalMutationArtifactRetention "$tmp/workflow-artifact"
	cp "$tmp/workflow-artifact/incremental-mutation.yml" "$tmp/incremental-mutation.yml"
	grep -q 'scripts/quality_gate/mutation.sh' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must run the strict mutation runner'
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
	TestReviewCheckpointMutationMapping)
		TestReviewCheckpointMutationMapping
		;;
	TestReviewIntegrationRecoveryMutationMapping)
		TestReviewIntegrationRecoveryMutationMapping
		;;
	TestAssignmentBCMutationMapping)
		TestAssignmentBCMutationMapping
		;;
	TestAssignmentAdmissionMutationMapping)
		TestAssignmentAdmissionMutationMapping
		;;
	TestAssignmentAdmissionTouchedFunctionRouting)
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' RETURN
		TestAssignmentAdmissionTouchedFunctionRouting "$tmp/assignment-admission-touched"
		;;
	TestEscalationTouchedFunctionRouting)
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' RETURN
		TestEscalationTouchedFunctionRouting "$tmp/escalation-touched"
		;;
	TestEscalationSurvivorMutationMapping)
		TestEscalationSurvivorMutationMapping
		;;
	TestAuthoritativeMutationMapping)
		TestAuthoritativeMutationMapping
		;;
	TestAuthoritativeMutationTargetedScope)
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' RETURN
		TestAuthoritativeMutationTargetedScope
		;;
	TestAuthoritativeMutationCoverage)
		TestAuthoritativeMutationCoverage
		;;
	TestAuthoritativeTouchedFunctionRouting)
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' RETURN
		TestAuthoritativeTouchedFunctionRouting "$tmp/authoritative-touched"
		;;
	TestEscalationSurvivorMutationCoverage)
		TestEscalationSurvivorMutationCoverage
		;;
	TestMutationOwnerMappingsCoexist)
		TestMutationOwnerMappingsCoexist
		;;
	TestP0DurabilityMutationMapping)
		TestP0DurabilityMutationMapping
		;;
	TestSplitBranchMutationOwners)
		TestSplitBranchMutationOwners
		;;
	TestStartupMaintenanceMutationMapping)
		TestStartupMaintenanceMutationMapping
		;;
	TestStartupMaintenanceMutationSharding)
		TestStartupMaintenanceMutationSharding
		;;
	TestCmdMutationSharding)
		TestCmdMutationSharding
		;;
	TestReviewIntegrationRecoveryMutationCoverage)
		TestReviewIntegrationRecoveryMutationCoverage
		;;
	TestReviewIntegrationRecoveryMutationFocusedExec)
		TestReviewIntegrationRecoveryMutationFocusedExec
		;;
	TestTargetedMutationScope)
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' RETURN
		TestTargetedMutationScope
		;;
	TestMutationCapacity)
		TestMutationCapacity
		;;
	TestMutationExecInternalDeadline)
		TestMutationExecInternalDeadline
		;;
	TestIncrementalMutationArtifactRetention)
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' EXIT
		TestIncrementalMutationArtifactRetention "$tmp"
		;;
	*)
		fail "unknown test $1"
		;;
	esac
}

main "$@"
