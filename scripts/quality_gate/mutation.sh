#!/usr/bin/env bash
set -euo pipefail

readonly policy_score=0.75
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
	local exit_code="$4"
	local reason="$5"

	jq -n \
		--argjson index "$index" \
		--arg file "$file" \
		--arg reason "$reason" \
		--argjson exit_code "$exit_code" \
		'{index: $index, file: $file, conclusion: "infrastructure_failure", exit_code: $exit_code, reason: $reason, score: null, passed: 0, failed: 0, duplicated: 0, skipped: 0, total: 0}' \
		>"$result"
}

write_shard_result() {
	local result="$1"
	local index="$2"
	local file="$3"
	local mutation_exit="$4"
	local output_file="$5"
	local output summary bare_record score total passed failed duplicated skipped
	output=$(<"$output_file")

	if ((mutation_exit != 0)); then
		local reason='mutation tool failed'
		if ((mutation_exit == 124)); then
			reason='mutation file deadline exceeded'
		fi
		write_shard_infrastructure "$result" "$index" "$file" "$mutation_exit" "$reason"
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
		write_shard_infrastructure "$result" "$index" "$file" 0 'mutation output was malformed or absent'
		return
	fi
	if ((total == 0)); then
		write_shard_infrastructure "$result" "$index" "$file" 0 'mutation tool generated zero mutants'
		return
	fi

	jq -n \
		--argjson index "$index" \
		--arg file "$file" \
		--argjson score "$score" \
		--argjson passed "$passed" \
		--argjson failed "$failed" \
		--argjson duplicated "$duplicated" \
		--argjson skipped "$skipped" \
		--argjson total "$total" \
		'{index: $index, file: $file, conclusion: "completed", exit_code: 0, reason: "", score: $score, passed: $passed, failed: $failed, duplicated: $duplicated, skipped: $skipped, total: $total}' \
		>"$result"
}

run_mutation_shard() {
	local index="$1"
	local file="$2"
	local cache_slot="$3"
	local head="$4"
	local shard_root="$5"
	local result_dir="$6"
	local file_timeout="$7"
	local exec_timeout="$8"
	local checkout="$shard_root/checkouts/$index"
	local output_file="$shard_root/logs/$index.log"
	local result="$result_dir/$index.json"
	local mutation_exit=0

	mkdir -p "$checkout" "$shard_root/logs" "$shard_root/caches/$cache_slot" "$shard_root/tmp/$index"
	if ! git archive "$head" | tar -x -C "$checkout"; then
		write_shard_infrastructure "$result" "$index" "$file" 2 'create isolated mutation checkout'
		return
	fi
	if [[ -f "$checkout/Makefile" && -f "$checkout/cmd/oro/embed.go" ]]; then
		if ! (cd "$checkout" && make stage-assets) >"$output_file" 2>&1; then
			write_shard_infrastructure "$result" "$index" "$file" 2 'stage embedded assets in mutation checkout'
			return
		fi
	fi
	(
		cd "$checkout"
		GOCACHE="$shard_root/caches/$cache_slot" \
			GOTMPDIR="$shard_root/tmp/$index" \
			timeout "$file_timeout" go tool go-mutesting --exec-timeout="$exec_timeout" "$file"
	) >"$output_file" 2>&1 || mutation_exit=$?
	write_shard_result "$result" "$index" "$file" "$mutation_exit" "$output_file"
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
	local pending_shards
	pending_shards=$(printf '%s\n' "${changed_files[@]}" |
		jq -Rn '[inputs | {file: ., conclusion: "pending", exit_code: 0, reason: "", score: null, passed: 0, failed: 0, duplicated: 0, skipped: 0, total: 0}]')
	write_sharded_evidence "$evidence" "$base" "$head" infrastructure_failure 2 null 0 "$pending_shards" "${changed_files[@]}"

	local worker_count=$max_workers
	if ((worker_count > ${#changed_files[@]})); then
		worker_count=${#changed_files[@]}
	fi
	printf 'mutation shards: files=%d workers=%d file_timeout=%ss exec_timeout=%ss\n' \
		"${#changed_files[@]}" "$worker_count" "$file_timeout" "$exec_timeout"

	local -a pids=()
	local index file key cache_slot pid
	for index in "${!changed_files[@]}"; do
		file=${changed_files[$index]}
		printf -v key '%06d' "$index"
		cache_slot=$((index % worker_count))
		run_mutation_shard "$key" "$file" "$cache_slot" "$head" "$shard_root" "$result_dir" "$file_timeout" "$exec_timeout" &
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

	for index in "${!changed_files[@]}"; do
		file=${changed_files[$index]}
		printf -v key '%06d' "$index"
		if [[ ! -s "$result_dir/$key.json" ]]; then
			write_shard_infrastructure "$result_dir/$key.json" "$key" "$file" 2 'mutation shard produced no evidence'
		fi
		if [[ -s "$shard_root/logs/$key.log" ]]; then
			printf '\n--- mutation shard %s: %s ---\n' "$key" "$file"
			sed -n '1,240p' "$shard_root/logs/$key.log"
		fi
	done

	local shards
	shards=$(jq -s 'sort_by(.index) | map(del(.index))' "$result_dir"/*.json)
	local infrastructure_count
	infrastructure_count=$(jq '[.[] | select(.conclusion != "completed")] | length' <<<"$shards")
	if ((infrastructure_count > 0)); then
		local infrastructure_exit
		infrastructure_exit=$(jq '[.[] | select(.conclusion != "completed") | .exit_code] |
			if index(124) != null then 124 elif length > 0 then .[0] else 2 end' <<<"$shards")
		write_sharded_evidence "$evidence" "$base" "$head" infrastructure_failure "$infrastructure_exit" null 0 "$shards" "${changed_files[@]}"
		printf 'infrastructure failure: %d of %d mutation shards did not complete\n' \
			"$infrastructure_count" "${#changed_files[@]}" >&2
		return 2
	fi

	local passed failed duplicated skipped total score
	passed=$(jq '[.[].passed] | add // 0' <<<"$shards")
	failed=$(jq '[.[].failed] | add // 0' <<<"$shards")
	duplicated=$(jq '[.[].duplicated] | add // 0' <<<"$shards")
	skipped=$(jq '[.[].skipped] | add // 0' <<<"$shards")
	total=$((passed + failed + skipped))
	if ((total == 0)); then
		write_sharded_evidence "$evidence" "$base" "$head" infrastructure_failure 0 null 0 "$shards" "${changed_files[@]}"
		printf 'infrastructure failure: mutation shards generated zero aggregate mutants\n' >&2
		return 2
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
