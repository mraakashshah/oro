#!/usr/bin/env bash
set -euo pipefail

: "${MUTATE_CHANGED:?}"
: "${MUTATE_ORIGINAL:?}"
: "${MUTATE_PACKAGE:?}"
: "${MUTATE_TIMEOUT:?}"
: "${MUTATION_TEST_PATTERN?}"
: "${MUTATION_TEST_FILE:=}"
: "${MUTATION_TEST_TIMEOUT:=$((MUTATE_TIMEOUT + 5))}"

mutation_backup_created=false
mutation_test_pid=''
mutation_test_pgid=''
mutation_test_output=''
mutation_timed_out=false

mutation_test_group_alive() {
	[[ "$mutation_test_pgid" =~ ^[0-9]+$ ]] || return 1
	kill -0 -- "-$mutation_test_pgid" 2>/dev/null
}

mutation_test_leader_exited() {
	local state
	[[ "$mutation_test_pid" =~ ^[0-9]+$ ]] || return 0
	state=$(ps -o state= -p "$mutation_test_pid" 2>/dev/null | tr -d ' ')
	[[ -z "$state" || "$state" = Z* ]]
}

mutation_cleanup_owned_processes() {
	if mutation_test_group_alive; then
		kill -TERM -- "-$mutation_test_pgid" 2>/dev/null || true
		for _ in $(seq 1 20); do
			mutation_test_leader_exited && break
			sleep 0.05
		done
		mutation_test_group_alive && kill -KILL -- "-$mutation_test_pgid" 2>/dev/null || true
	fi
	if [[ "$mutation_test_pid" =~ ^[0-9]+$ ]]; then
		wait "$mutation_test_pid" 2>/dev/null || true
	fi
	for _ in $(seq 1 20); do
		mutation_test_group_alive || break
		sleep 0.05
	done
	mutation_test_pid=''
	mutation_test_pgid=''
}

mutation_cleanup_test_tree() {
	mutation_cleanup_owned_processes
	mutation_test_pid=''
}

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT trap
cleanup_mutation() {
	trap - HUP INT TERM
	mutation_cleanup_owned_processes
	if [[ -n "$mutation_test_output" ]]; then
		rm -f -- "$mutation_test_output"
	fi
	if [[ "$mutation_backup_created" = true && -f "$MUTATE_ORIGINAL.tmp" && ! -L "$MUTATE_ORIGINAL.tmp" ]]; then
		mv -- "$MUTATE_ORIGINAL.tmp" "$MUTATE_ORIGINAL"
	fi
	if [[ "$mutation_timed_out" = true ]]; then
		printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
	fi
}

mutation_setup_failure() {
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
}

trap cleanup_mutation EXIT
trap 'exit 124' HUP INT TERM

package_dir=$(dirname -- "$MUTATE_ORIGINAL")
package_dir_abs=$(cd "$package_dir" 2>/dev/null && pwd -P) || mutation_setup_failure

[[ -f "$MUTATE_ORIGINAL" && ! -L "$MUTATE_ORIGINAL" ]] || mutation_setup_failure
[[ -f "$MUTATE_CHANGED" && ! -L "$MUTATE_CHANGED" ]] || mutation_setup_failure
[[ ! -e "$MUTATE_ORIGINAL.tmp" && ! -L "$MUTATE_ORIGINAL.tmp" ]] || mutation_setup_failure

if [[ -n "$MUTATION_TEST_FILE" ]]; then
	test_file_dir=$(dirname -- "$MUTATION_TEST_FILE")
	test_file_dir_abs=$(cd "$test_file_dir" 2>/dev/null && pwd -P) || mutation_setup_failure
	[[ "$test_file_dir_abs" = "$package_dir_abs" &&
		"$(basename -- "$MUTATION_TEST_FILE")" = *_test.go &&
		-f "$MUTATION_TEST_FILE" && ! -L "$MUTATION_TEST_FILE" ]] || mutation_setup_failure
fi

mutation_diff=$(diff -u "$MUTATE_ORIGINAL" "$MUTATE_CHANGED" || true)
mv -- "$MUTATE_ORIGINAL" "$MUTATE_ORIGINAL.tmp" || mutation_setup_failure
mutation_backup_created=true
cp -- "$MUTATE_CHANGED" "$MUTATE_ORIGINAL" || mutation_setup_failure
cmp -s "$MUTATE_CHANGED" "$MUTATE_ORIGINAL" || mutation_setup_failure

mutation_sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

changed_sha=$(mutation_sha256 "$MUTATE_CHANGED") || mutation_setup_failure
active_sha=$(mutation_sha256 "$MUTATE_ORIGINAL") || mutation_setup_failure
[[ "$changed_sha" = "$active_sha" ]] || mutation_setup_failure
if [[ "${MUTATE_DEBUG:-false}" == true ]]; then
	printf 'ORO_MUTATION_ACTIVE_SHA:%s\n' "$active_sha"
fi

test_targets=("$MUTATE_PACKAGE")

test_exit=0
mutation_test_output=$(mktemp "${TMPDIR:-/tmp}/oro-mutation-exec.XXXXXX") || mutation_setup_failure
set +e
set -m
timeout "$MUTATE_TIMEOUT" go test -vet=off -count=1 -timeout "${MUTATION_TEST_TIMEOUT}s" \
	-run "$MUTATION_TEST_PATTERN" "${test_targets[@]}" >"$mutation_test_output" 2>&1 &
mutation_test_pid=$!
set +m
mutation_test_pgid=$(ps -o pgid= -p "$mutation_test_pid" 2>/dev/null | tr -d ' ')
if [[ ! "$mutation_test_pgid" =~ ^[0-9]+$ || "$mutation_test_pgid" != "$mutation_test_pid" ]]; then
	kill -TERM "$mutation_test_pid" 2>/dev/null || true
	sleep 0.05
	kill -KILL "$mutation_test_pid" 2>/dev/null || true
	wait "$mutation_test_pid" 2>/dev/null || true
	mutation_test_pid=''
	mutation_test_pgid=''
	mutation_setup_failure
fi
wait "$mutation_test_pid"
test_exit=$?
set -e
mutation_cleanup_test_tree
test_output=$(<"$mutation_test_output")

if [[ "${MUTATE_DEBUG:-false}" == true ]]; then
	printf '%s\n' "$test_output"
fi

case "$test_exit" in
0)
	printf '%s\n' "$mutation_diff"
	exit 1
	;;
1)
	if grep -q '\[build failed\]' <<<"$test_output"; then
		printf '%s\n' "$test_output"
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		exit 2
	fi
	if [[ "${MUTATE_DEBUG:-false}" == true ]]; then
		printf '%s\n' "$mutation_diff"
	fi
	exit 0
	;;
124)
	mutation_timed_out=true
	exit 124
	;;
*)
	printf '%s\n' "$test_output"
	printf 'ORO_MUTATION_EXEC_FAILURE:%d\n' "$test_exit"
	exit "$test_exit"
	;;
esac
