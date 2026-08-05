#!/usr/bin/env bash
set -euo pipefail

: "${MUTATION_SOURCE_FILE:?}"
: "${MUTATION_FUNCTION_MATCH:?}"
: "${MUTATION_TEST_PATTERN?}"
: "${MUTATION_TEST_FILE:=}"
: "${MUTATION_EXEC_TIMEOUT:?}"
: "${MUTATION_PARALLEL_WORKERS:?}"
: "${MUTATION_EXEC_SCRIPT:=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mutation_exec.sh}"
: "${MUTATION_BASE_SHARD_TIMEOUT_SECONDS:=240}"
: "${MUTATION_MAX_SHARD_TIMEOUT_SECONDS:=900}"
: "${GOCACHE:?}"
: "${GOTMPDIR:?}"

if [[ ! "$MUTATION_PARALLEL_WORKERS" =~ ^[1-9][0-9]*$ ||
	! "$MUTATION_BASE_SHARD_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ||
	! "$MUTATION_MAX_SHARD_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ||
	MUTATION_MAX_SHARD_TIMEOUT_SECONDS -lt MUTATION_BASE_SHARD_TIMEOUT_SECONDS ]]; then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

executor_root=$(mktemp -d "$GOTMPDIR/parallel-mutants.XXXXXX")
generation_log="$executor_root/generation.log"
generation_root=""
worker_pids=()
worker_group_ids=()
worker_stop_file="$executor_root/stop-workers"
active_jobs_file="$executor_root/active-worker-jobs"
shard_started_at=$SECONDS
module_path=$(awk '$1 == "module" { print $2; exit }' go.mod)
if [[ -z "$module_path" ]]; then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

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
	refresh_active_worker_groups
	if ((${#active_worker_groups[@]} != 0)); then
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		return 2
	fi
	worker_pids=()
	worker_group_ids=()
}

cleanup_parallel_mutation() {
	stop_mutant_workers
	if [[ -n "$generation_root" && -d "$generation_root" ]]; then
		rm -rf -- "$generation_root"
	fi
	rm -rf -- "$executor_root"
}
trap cleanup_parallel_mutation EXIT
trap 'exit 124' HUP INT TERM

if ! go tool go-mutesting --debug --no-exec --do-not-remove-tmp-folder \
	"--match=$MUTATION_FUNCTION_MATCH" "$MUTATION_SOURCE_FILE" >"$generation_log" 2>&1; then
	cat "$generation_log"
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

generation_line=$(grep '^Save mutations into ' "$generation_log" | tail -1 || true)
generation_root=${generation_line#Save mutations into }
generation_root=${generation_root#\"}
generation_root=${generation_root%\"}
if [[ -z "$generation_root" || ! -d "$generation_root" ]]; then
	cat "$generation_log"
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

mutation_prefix="$generation_root/$MUTATION_SOURCE_FILE"
original_source="$mutation_prefix.original"
if [[ ! -f "$original_source" ]]; then
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
		(
			cd "$worker_repo"
			GOCACHE="$GOCACHE/parallel-$worker" \
				GOTMPDIR="$GOTMPDIR/parallel-worker-$worker" \
				MUTATE_CHANGED="$mutant" \
				MUTATE_ORIGINAL="$worker_source" \
				MUTATE_PACKAGE="$module_path/$(dirname "$MUTATION_SOURCE_FILE")" \
				MUTATE_TIMEOUT="$MUTATION_EXEC_TIMEOUT" \
				MUTATION_TEST_PATTERN="$MUTATION_TEST_PATTERN" \
				MUTATION_TEST_FILE="$worker_test_file" \
				timeout --foreground "$MUTATION_EXEC_TIMEOUT" bash "$MUTATION_EXEC_SCRIPT"
		) >"$output" 2>&1 || status=$?
		printf '%d\t%d\t%s\n' "$position" "$status" "$mutant" >"$result_tmp"
		mv -- "$result_tmp" "$result"
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
while :; do
	for ((position = 0; position < ${#mutants[@]}; position++)); do
		[[ -z "${observed_results[$position]:-}" ]] || continue
		result="$executor_root/results/$position.tsv"
		[[ -s "$result" ]] || continue
		output="$executor_root/logs/$position.log"
		IFS=$'\t' read -r recorded_position status mutant <"$result"
		if [[ "$recorded_position" != "$position" || "$mutant" != "${mutants[$position]}" ]]; then
			printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
			stop_mutant_workers
			exit 2
		fi
		observed_results[$position]=1
		case "$status" in
		0 | 1) ;;
		*)
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

	workers_running=0
	for pid in "${worker_pids[@]}"; do
		if kill -0 "$pid" 2>/dev/null; then
			workers_running=1
			break
		fi
	done
	if ((workers_running == 0)); then
		break
	fi
	if ((SECONDS >= shard_deadline)); then
		printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
		stop_mutant_workers
		exit 124
	fi
	sleep 0.05
done

worker_failure=0
for pid in "${worker_pids[@]}"; do
	wait "$pid" || worker_failure=1
done
worker_pids=()
worker_group_ids=()

passed=0
failed=0
skipped=0
infrastructure_failure=0
if ((worker_failure != 0)); then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	infrastructure_failure=1
fi
for ((position = 0; position < ${#mutants[@]}; position++)); do
	result="$executor_root/results/$position.tsv"
	output="$executor_root/logs/$position.log"
	if [[ ! -s "$result" || ! -f "$output" ]]; then
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
		skipped=$((skipped + 1))
		continue
	fi
	IFS=$'\t' read -r recorded_position status mutant <"$result"
	if [[ "$recorded_position" != "$position" || "$mutant" != "${mutants[$position]}" ]]; then
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
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
	fi
done

total=$((passed + failed + skipped))
score=$(awk "BEGIN { if ($total == 0) print \"0.000000\"; else printf \"%.6f\", $passed / $total }")
printf 'The mutation score is %s (%d passed, %d failed, %d duplicated, %d skipped, total is %d)\n' \
	"$score" "$passed" "$failed" "$duplicate_count" "$skipped" "$total"
if ((infrastructure_failure != 0)); then
	exit 2
fi
