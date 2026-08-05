#!/usr/bin/env bash
set -euo pipefail

: "${MUTATION_SOURCE_FILE:?}"
: "${MUTATION_FUNCTION_MATCH:?}"
: "${MUTATION_TEST_PATTERN?}"
: "${MUTATION_TEST_FILE:=}"
: "${MUTATION_EXEC_TIMEOUT:?}"
: "${MUTATION_TEST_TIMEOUT_MARGIN_SECONDS:=5}"
: "${MUTATION_PARALLEL_WORKERS:?}"
: "${MUTATION_EXEC_SCRIPT:=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mutation_exec.sh}"
: "${MUTATION_BASE_SHARD_TIMEOUT_SECONDS:=240}"
: "${MUTATION_MAX_SHARD_TIMEOUT_SECONDS:=900}"
: "${GOCACHE:?}"
: "${GOTMPDIR:?}"

if [[ ! "$MUTATION_PARALLEL_WORKERS" =~ ^[1-9][0-9]*$ ||
	! "$MUTATION_EXEC_TIMEOUT" =~ ^[1-9][0-9]*$ ||
	! "$MUTATION_TEST_TIMEOUT_MARGIN_SECONDS" =~ ^[1-9][0-9]*$ ||
	! "$MUTATION_BASE_SHARD_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ||
	! "$MUTATION_MAX_SHARD_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]] ||
	((MUTATION_TEST_TIMEOUT_MARGIN_SECONDS >= MUTATION_EXEC_TIMEOUT ||
		MUTATION_MAX_SHARD_TIMEOUT_SECONDS < MUTATION_BASE_SHARD_TIMEOUT_SECONDS)); then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi
mutation_test_timeout_seconds=$((MUTATION_EXEC_TIMEOUT - MUTATION_TEST_TIMEOUT_MARGIN_SECONDS))

executor_root=$(mktemp -d "$GOTMPDIR/parallel-mutants.XXXXXX")
generation_log="$executor_root/generation.log"
generation_root=""
failure_evidence_root=${MUTATION_FAILURE_EVIDENCE_DIR:-$GOTMPDIR/mutation-failure-evidence}
mkdir -p "$failure_evidence_root"
failure_evidence_root=$(cd "$failure_evidence_root" && pwd -P)
failure_evidence_run=$(mktemp -d "$failure_evidence_root/run.XXXXXX")
failure_evidence_announced=0
worker_pids=()
worker_group_ids=()
worker_stop_file="$executor_root/stop-workers"
active_jobs_file="$executor_root/active-worker-jobs"
shard_started_at=$SECONDS

refresh_active_worker_groups() {
	local active group
	local -a running_jobs=()
	active_worker_groups=()
	jobs -pr >"$active_jobs_file" || true
	while IFS= read -r active; do
		[[ -n "$active" ]] || continue
		running_jobs+=("$active")
	done <"$active_jobs_file"
	for group in "${worker_group_ids[@]}"; do
		for active in "${running_jobs[@]}"; do
			if [[ "$group" == "$active" ]]; then
				active_worker_groups+=("$group")
				break
			fi
		done
	done
}

announce_failure_evidence() {
	if ((failure_evidence_announced == 0)); then
		printf 'ORO_MUTATION_FAILURE_EVIDENCE:%s\n' "$failure_evidence_run"
		failure_evidence_announced=1
	fi
}

write_mutant_evidence() {
	local worker="$1"
	local position="$2"
	local mutant="$3"
	local source_path="$4"
	local exit_class="$5"
	local exit_status="$6"
	local content_hash mutant_path record record_tmp
	content_hash=$(git hash-object -- "$mutant")
	mutant_path=$(cd "$(dirname "$mutant")" && pwd -P)/$(basename "$mutant")
	record="$failure_evidence_run/mutant-$position.json"
	record_tmp="$record.$BASHPID.tmp"
	jq -n \
		--argjson worker "$worker" \
		--arg source_file "$MUTATION_SOURCE_FILE" \
		--arg source_path "$source_path" \
		--arg function_match "$MUTATION_FUNCTION_MATCH" \
		--argjson mutant_index "$position" \
		--arg mutant_path "$mutant_path" \
		--arg content_hash "$content_hash" \
		--arg exit_class "$exit_class" \
		--argjson exit_status "$exit_status" \
		'{worker: $worker, source_file: $source_file, source_path: $source_path,
			function_match: $function_match, mutant_index: $mutant_index,
			mutant_path: $mutant_path, content_hash_algorithm: "git-blob", content_hash: $content_hash,
			exit_class: $exit_class, exit_status: $exit_status}' >"$record_tmp"
	mv -- "$record_tmp" "$record"
}

abort_running_mutant_evidence() {
	local record record_tmp
	for record in "$failure_evidence_run"/mutant-*.json; do
		[[ -f "$record" ]] || continue
		if jq -e '.exit_class == "running"' "$record" >/dev/null; then
			record_tmp="$record.$BASHPID.tmp"
			jq '.exit_class = "aborted" | .exit_status = null' "$record" >"$record_tmp"
			mv -- "$record_tmp" "$record"
		fi
	done
}

stop_mutant_workers() {
	local attempt group groups_running pid
	: >"$worker_stop_file"
	refresh_active_worker_groups
	for group in "${active_worker_groups[@]}"; do
		kill -TERM -- "-$group" 2>/dev/null || true
	done
	for ((attempt = 0; attempt < 25; attempt++)); do
		refresh_active_worker_groups
		groups_running=${#active_worker_groups[@]}
		((groups_running != 0)) || break
		sleep 0.01
	done
	refresh_active_worker_groups
	for group in "${active_worker_groups[@]}"; do
		kill -KILL -- "-$group" 2>/dev/null || true
	done
	for pid in "${worker_pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	abort_running_mutant_evidence
	refresh_active_worker_groups
	if ((${#active_worker_groups[@]} != 0)); then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		return 2
	fi
	worker_pids=()
	worker_group_ids=()
}

finish_mutant_workers() {
	local group pid
	refresh_active_worker_groups
	if ((${#active_worker_groups[@]} != ${#worker_group_ids[@]})); then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		return 2
	fi
	for group in "${active_worker_groups[@]}"; do
		kill -KILL -- "-$group" 2>/dev/null || true
	done
	for pid in "${worker_pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	refresh_active_worker_groups
	if ((${#active_worker_groups[@]} != 0)); then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		return 2
	fi
	worker_pids=()
	worker_group_ids=()
}

cleanup_parallel_mutation() {
	local exit_status=$?
	if ((exit_status != 0)); then
		announce_failure_evidence
	fi
	stop_mutant_workers
	if [[ -n "$generation_root" && -d "$generation_root" ]]; then
		rm -rf -- "$generation_root"
	fi
	rm -rf -- "$executor_root"
}
trap cleanup_parallel_mutation EXIT
trap 'announce_failure_evidence; exit 124' HUP INT TERM

module_path=$(awk '$1 == "module" { print $2; exit }' go.mod)
if [[ -z "$module_path" ]]; then
	announce_failure_evidence
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

if ! go tool go-mutesting --debug --no-exec --do-not-remove-tmp-folder \
	"--match=$MUTATION_FUNCTION_MATCH" "$MUTATION_SOURCE_FILE" >"$generation_log" 2>&1; then
	announce_failure_evidence
	cat "$generation_log"
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

generation_line=$(grep '^Save mutations into ' "$generation_log" | tail -1 || true)
generation_root=${generation_line#Save mutations into }
generation_root=${generation_root#\"}
generation_root=${generation_root%\"}
if [[ -z "$generation_root" || ! -d "$generation_root" ]]; then
	announce_failure_evidence
	cat "$generation_log"
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

mutation_prefix="$generation_root/$MUTATION_SOURCE_FILE"
original_source="$mutation_prefix.original"
if [[ ! -f "$original_source" ]]; then
	announce_failure_evidence
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

mapfile -t mutants < <(find "$(dirname "$mutation_prefix")" -maxdepth 1 -type f \
	-name "$(basename "$mutation_prefix").*" ! -name "$(basename "$mutation_prefix").original" | sort -V)
duplicate_count=$(grep -c ' is a duplicate, we ignore it$' "$generation_log" || true)
if ((${#mutants[@]} == 0)); then
	printf 'The mutation score is 0.000000 (0 passed, 0 failed, %d duplicated, 0 skipped, total is 0)\n' \
		"$duplicate_count"
	exit 0
fi

worker_batches=$(((${#mutants[@]} + MUTATION_PARALLEL_WORKERS - 1) / MUTATION_PARALLEL_WORKERS))
effective_timeout=$((worker_batches * 6))
if ((effective_timeout < MUTATION_BASE_SHARD_TIMEOUT_SECONDS)); then
	effective_timeout=$MUTATION_BASE_SHARD_TIMEOUT_SECONDS
elif ((effective_timeout > MUTATION_MAX_SHARD_TIMEOUT_SECONDS)); then
	effective_timeout=$MUTATION_MAX_SHARD_TIMEOUT_SECONDS
fi
printf 'mutation shard capacity: mutants=%d workers=%d effective_timeout=%ds emergency_cap=%ds\n' \
	"${#mutants[@]}" "$MUTATION_PARALLEL_WORKERS" "$effective_timeout" \
	"$MUTATION_MAX_SHARD_TIMEOUT_SECONDS"
shard_deadline=$((shard_started_at + effective_timeout))

mkdir -p "$executor_root/results" "$executor_root/logs" "$executor_root/workers"
for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	worker_parent="$executor_root/workers/$worker"
	mkdir -p "$worker_parent" "$GOCACHE/parallel-$worker" "$GOTMPDIR/parallel-worker-$worker"
	cp -R . "$worker_parent/repo"
done

run_mutant_worker() {
	local worker="$1"
	set +m
	trap ':' TERM
	local ready_file="$executor_root/workers/$worker.ready"
	printf '%d\n' "$BASHPID" >"$ready_file.tmp"
	mv -- "$ready_file.tmp" "$ready_file"
	local worker_repo="$executor_root/workers/$worker/repo"
	local worker_source="$worker_repo/$MUTATION_SOURCE_FILE"
	local worker_test_file=""
	if [[ -n "$MUTATION_TEST_FILE" ]]; then
		worker_test_file="$worker_repo/$MUTATION_TEST_FILE"
	fi
	local position mutant output result result_tmp status
	for ((position = worker; position < ${#mutants[@]}; position += MUTATION_PARALLEL_WORKERS)); do
		[[ ! -e "$worker_stop_file" ]] || break
		mutant=${mutants[$position]}
		output="$executor_root/logs/$position.log"
		result="$executor_root/results/$position.tsv"
		result_tmp="$result.$worker.tmp"
		status=0
		write_mutant_evidence "$worker" "$position" "$mutant" "$worker_source" running null
		(
			cd "$worker_repo"
			GOCACHE="$GOCACHE/parallel-$worker" \
				GOTMPDIR="$GOTMPDIR/parallel-worker-$worker" \
				MUTATE_CHANGED="$mutant" \
				MUTATE_ORIGINAL="$worker_source" \
				MUTATE_PACKAGE="$module_path/$(dirname "$MUTATION_SOURCE_FILE")" \
				MUTATE_TIMEOUT="$MUTATION_EXEC_TIMEOUT" \
				MUTATION_TEST_TIMEOUT="$mutation_test_timeout_seconds" \
				MUTATION_TEST_PATTERN="$MUTATION_TEST_PATTERN" \
				MUTATION_TEST_FILE="$worker_test_file" \
				timeout --foreground "$MUTATION_EXEC_TIMEOUT" bash "$MUTATION_EXEC_SCRIPT"
		) >"$output" 2>&1 || status=$?
		case "$status" in
		0) exit_class=killed ;;
		1) exit_class=survived ;;
		124) exit_class=timeout ;;
		*) exit_class=infrastructure ;;
		esac
		write_mutant_evidence "$worker" "$position" "$mutant" "$worker_source" "$exit_class" "$status"
		printf '%d\t%d\t%s\n' "$position" "$status" "$mutant" >"$result_tmp"
		mv -- "$result_tmp" "$result"
	done
	local done_file="$executor_root/workers/$worker.done"
	printf '%d\n' "$BASHPID" >"$done_file.tmp"
	mv -- "$done_file.tmp" "$done_file"
	while :; do
		sleep 1
	done
}

restore_monitor=0
if [[ "$-" != *m* ]]; then
	set -m
	restore_monitor=1
fi
for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	run_mutant_worker "$worker" &
	pid=$!
	worker_pids+=("$pid")
	worker_group_ids+=("$pid")
done
((restore_monitor == 0)) || set +m

worker_ready_failure=0
for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	ready_file="$executor_root/workers/$worker.ready"
	ready_pid=""
	for ((attempt = 0; attempt < 100; attempt++)); do
		if [[ -s "$ready_file" ]]; then
			IFS= read -r ready_pid <"$ready_file" || true
			break
		fi
		sleep 0.01
	done
	if [[ "$ready_pid" != "${worker_pids[$worker]}" ]]; then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		worker_ready_failure=1
		break
	fi
done
if ((worker_ready_failure != 0)); then
	stop_mutant_workers
	exit 2
fi

declare -A observed_results=()
result_complete_attempts=0
while :; do
	for ((position = 0; position < ${#mutants[@]}; position++)); do
		[[ -z "${observed_results[$position]:-}" ]] || continue
		result="$executor_root/results/$position.tsv"
		[[ -s "$result" ]] || continue
		output="$executor_root/logs/$position.log"
		IFS=$'\t' read -r recorded_position status mutant <"$result"
		if [[ "$recorded_position" != "$position" || "$mutant" != "${mutants[$position]}" ]]; then
			announce_failure_evidence
			printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
			stop_mutant_workers
			exit 2
		fi
		observed_results[$position]=1
		case "$status" in
		0 | 1) ;;
		*)
			announce_failure_evidence
			cat "$output"
			if grep -q '^ORO_MUTATION_EXEC_TIMEOUT$' "$output"; then
				stop_mutant_workers
				exit 124
			fi
			if ! grep -Eq '^(ORO_MUTATION_EXEC_FAILURE:[0-9]+|UNKOWN exit code for )' "$output"; then
				printf 'ORO_MUTATION_EXEC_FAILURE:%d\n' "$status"
			fi
			stop_mutant_workers
			exit 2
			;;
		esac
	done

	refresh_active_worker_groups
	workers_done=0
	worker_failure=0
	for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
		done_file="$executor_root/workers/$worker.done"
		done_pid=""
		if [[ -s "$done_file" ]]; then
			IFS= read -r done_pid <"$done_file" || true
			if [[ "$done_pid" != "${worker_pids[$worker]}" ]]; then
				worker_failure=1
				break
			fi
			workers_done=$((workers_done + 1))
			continue
		fi
		worker_active=0
		for group in "${active_worker_groups[@]}"; do
			if [[ "$group" == "${worker_group_ids[$worker]}" ]]; then
				worker_active=1
				break
			fi
		done
		if ((worker_active == 0)); then
			worker_failure=1
			break
		fi
	done
	if ((worker_failure != 0)); then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		stop_mutant_workers
		exit 2
	fi
	if ((workers_done == MUTATION_PARALLEL_WORKERS)); then
		if ((${#observed_results[@]} != ${#mutants[@]})); then
			announce_failure_evidence
			printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
			stop_mutant_workers
			exit 2
		fi
		break
	fi
	if ((${#observed_results[@]} == ${#mutants[@]})); then
		result_complete_attempts=$((result_complete_attempts + 1))
		if ((result_complete_attempts >= 20)); then
			announce_failure_evidence
			printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
			stop_mutant_workers
			exit 2
		fi
	else
		result_complete_attempts=0
	fi
	if ((SECONDS >= shard_deadline)); then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
		stop_mutant_workers
		exit 124
	fi
	sleep 0.05
done

if ! finish_mutant_workers; then
	announce_failure_evidence
	exit 2
fi

passed=0
failed=0
skipped=0
infrastructure_failure=0
for ((position = 0; position < ${#mutants[@]}; position++)); do
	result="$executor_root/results/$position.tsv"
	output="$executor_root/logs/$position.log"
	if [[ ! -s "$result" || ! -f "$output" ]]; then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
		skipped=$((skipped + 1))
		continue
	fi
	IFS=$'\t' read -r recorded_position status mutant <"$result"
	if [[ "$recorded_position" != "$position" || "$mutant" != "${mutants[$position]}" ]]; then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
		skipped=$((skipped + 1))
		continue
	fi
	cat "$output"
	case "$status" in
	0)
		printf 'PASS %q\n' "$mutant"
		passed=$((passed + 1))
		;;
	1)
		printf 'FAIL %q\n' "$mutant"
		failed=$((failed + 1))
		;;
	*)
		announce_failure_evidence
		if ! grep -Eq '^(ORO_MUTATION_EXEC_TIMEOUT|ORO_MUTATION_EXEC_FAILURE:)' "$output"; then
			printf 'ORO_MUTATION_EXEC_FAILURE:%d\n' "$status"
		fi
		printf 'SKIP %q\n' "$mutant"
		skipped=$((skipped + 1))
		infrastructure_failure=1
		;;
	esac
done

for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	if ! cmp -s "$original_source" "$executor_root/workers/$worker/repo/$MUTATION_SOURCE_FILE"; then
		announce_failure_evidence
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
	fi
done

total=$((passed + failed + skipped))
score=$(awk "BEGIN { if ($total == 0) print \"0.000000\"; else printf \"%.6f\", $passed / $total }")
printf 'The mutation score is %s (%d passed, %d failed, %d duplicated, %d skipped, total is %d)\n' \
	"$score" "$passed" "$failed" "$duplicate_count" "$skipped" "$total"
if ((infrastructure_failure != 0)); then
	announce_failure_evidence
	exit 2
fi
