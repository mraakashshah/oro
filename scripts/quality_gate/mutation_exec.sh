#!/usr/bin/env bash
set -euo pipefail

: "${MUTATE_CHANGED:?}"
: "${MUTATE_ORIGINAL:?}"
: "${MUTATE_PACKAGE:?}"
: "${MUTATE_TIMEOUT:?}"
: "${MUTATION_TEST_PATTERN?}"
: "${MUTATION_TEST_FILE:=}"

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT trap
cleanup_mutation() {
	if [[ -f "$MUTATE_ORIGINAL.tmp" ]]; then
		mv -- "$MUTATE_ORIGINAL.tmp" "$MUTATE_ORIGINAL"
	fi
}

trap cleanup_mutation EXIT
trap 'exit 124' HUP INT TERM

mutation_diff=$(diff -u "$MUTATE_ORIGINAL" "$MUTATE_CHANGED" || true)
mv -- "$MUTATE_ORIGINAL" "$MUTATE_ORIGINAL.tmp"
cp -- "$MUTATE_CHANGED" "$MUTATE_ORIGINAL"

test_targets=("$MUTATE_PACKAGE")
if [[ -n "$MUTATION_TEST_FILE" ]]; then
	package_dir=$(dirname -- "$MUTATE_ORIGINAL")
	package_dir_abs=$(cd "$package_dir" && pwd -P)
	test_file_dir=$(dirname -- "$MUTATION_TEST_FILE")
	test_file_dir_abs=$(cd "$test_file_dir" 2>/dev/null && pwd -P || true)
	if [[ -z "$test_file_dir_abs" || "$test_file_dir_abs" != "$package_dir_abs" ||
		"$(basename -- "$MUTATION_TEST_FILE")" != *_test.go || ! -f "$MUTATION_TEST_FILE" ]]; then
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		exit 2
	fi
	mapfile -t test_targets < <(find "$package_dir" -maxdepth 1 -type f -name '*.go' ! -name '*_test.go' | sort)
	test_targets+=("$MUTATION_TEST_FILE")
fi

test_exit=0
set +e
test_output=$(timeout "$MUTATE_TIMEOUT" go test -vet=off -count=1 -timeout "$((MUTATE_TIMEOUT + 5))s" \
	-run "$MUTATION_TEST_PATTERN" "${test_targets[@]}" 2>&1)
test_exit=$?
set -e

if [[ "${MUTATE_DEBUG:-false}" == true ]]; then
	printf '%s\n' "$test_output"
fi

case "$test_exit" in
0)
	printf '%s\n' "$mutation_diff"
	exit 1
	;;
1)
	if [[ "${MUTATE_DEBUG:-false}" == true ]]; then
		printf '%s\n' "$mutation_diff"
	fi
	exit 0
	;;
124)
	printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
	exit 124
	;;
*)
	printf '%s\n' "$test_output"
	printf 'ORO_MUTATION_EXEC_FAILURE:%d\n' "$test_exit"
	exit "$test_exit"
	;;
esac
