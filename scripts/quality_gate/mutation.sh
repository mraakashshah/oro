#!/usr/bin/env bash
set -euo pipefail

readonly policy_score=0.75

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
		'{base: $base, head: $head, conclusion: $conclusion, mutation_exit_code: $exit_code, score: $score, total: $total, changed_files: $ARGS.positional}' \
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

	local output=""
	local mutation_exit=0
	output=$(timeout 480 go tool go-mutesting --exec-timeout=60 "${changed_files[@]}" 2>&1) || mutation_exit=$?
	printf '%s\n' "$output"
	if ((mutation_exit != 0)); then
		infrastructure_failure "$evidence" "$base" "$head" 'mutation tool failed or timed out' "$mutation_exit" "${changed_files[@]}"
		return
	fi

	local summary
	local score
	local total
	summary=$(sed -nE '/^The mutation score is [0-9]+(\.[0-9]+)?([[:space:]]+\([0-9]+ passed, [0-9]+ failed, [0-9]+ duplicated, [0-9]+ skipped, total is [0-9]+\))?[[:space:]]*$/p' <<<"$output" | tail -1)
	score=$(sed -nE 's/^The mutation score is ([0-9]+(\.[0-9]+)?).*$/\1/p' <<<"$summary")
	total=$(sed -nE 's/^The mutation score is [0-9]+(\.[0-9]+)?[[:space:]]+\([0-9]+ passed, [0-9]+ failed, [0-9]+ duplicated, [0-9]+ skipped, total is ([0-9]+)\)[[:space:]]*$/\2/p' <<<"$summary")
	if [[ -z "$total" ]]; then
		total=$(sed -nE 's/^total is ([0-9]+)[[:space:]]*$/\1/p' <<<"$output" | tail -1)
	fi
	if [[ -z "$score" || -z "$total" || ! "$score" =~ ^[0-9]+(\.[0-9]+)?$ || ! "$total" =~ ^[0-9]+$ ]]; then
		infrastructure_failure "$evidence" "$base" "$head" 'mutation output was malformed or absent' 0 "${changed_files[@]}"
		return
	fi
	if ((total == 0)); then
		infrastructure_failure "$evidence" "$base" "$head" 'mutation tool generated zero mutants' 0 "${changed_files[@]}"
		return
	fi
	if awk "BEGIN { exit !($score < $policy_score) }"; then
		write_evidence "$evidence" "$base" "$head" deterministic_failure 0 "$score" "$total" "${changed_files[@]}"
		printf 'deterministic failure: mutation score %s is below policy %s\n' "$score" "$policy_score" >&2
		return 1
	fi
	write_evidence "$evidence" "$base" "$head" pass 0 "$score" "$total" "${changed_files[@]}"
	printf 'pass: mutation score %s meets policy %s\n' "$score" "$policy_score"
}

main "$@"
