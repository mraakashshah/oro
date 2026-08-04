#!/usr/bin/env bash
set -euo pipefail

: "${MUTATE_CHANGED:?}"
: "${MUTATE_ORIGINAL:?}"
: "${MUTATE_PACKAGE:?}"
: "${MUTATE_TIMEOUT:?}"
: "${MUTATION_TEST_PATTERN:?}"

# shellcheck disable=SC2329 # invoked by the EXIT trap
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

test_exit=0
set +e
test_output=$(timeout "$MUTATE_TIMEOUT" go test -timeout "$((MUTATE_TIMEOUT + 5))s" \
	-run "$MUTATION_TEST_PATTERN" "$MUTATE_PACKAGE" 2>&1)
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
	exit "$test_exit"
	;;
esac
