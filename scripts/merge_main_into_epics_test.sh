#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/merge_main_into_epics.sh"

PASS=0
FAIL=0

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

test_case() {
	local name="$1"
	local fn="$2"
	printf '> %-60s' "$name"
	if "$fn"; then
		printf 'PASS\n'
		PASS=$((PASS + 1))
	else
		printf 'FAIL\n'
		FAIL=$((FAIL + 1))
	fi
}

fixture=""

cleanup_fixture() {
	if [ -n "$fixture" ] && [ -d "$fixture" ]; then
		rm -rf "$fixture"
	fi
}

make_fixture() {
	fixture=$(mktemp -d)
	local repo="$fixture/repo"
	local bin="$fixture/bin"
	mkdir -p "$repo" "$bin"

	git -C "$repo" init --quiet --initial-branch=main
	git -C "$repo" config user.name fixture
	git -C "$repo" config user.email fixture@example.invalid
	printf 'base\n' >"$repo/shared.txt"
	git -C "$repo" add shared.txt
	git -C "$repo" commit --quiet -m base
	local base
	base=$(git -C "$repo" rev-parse HEAD)

	git -C "$repo" branch epic/oro-behind "$base"
	git -C "$repo" branch epic/oro-clean "$base"
	git -C "$repo" branch epic/oro-conflict "$base"
	git -C "$repo" branch epic/oro-dead "$base"

	printf 'main\n' >"$repo/shared.txt"
	git -C "$repo" add shared.txt
	git -C "$repo" commit --quiet -m main

	git -C "$repo" switch --quiet epic/oro-clean
	printf 'epic clean\n' >"$repo/epic-clean.txt"
	git -C "$repo" add epic-clean.txt
	git -C "$repo" commit --quiet -m clean

	git -C "$repo" switch --quiet epic/oro-conflict
	printf 'epic conflict\n' >"$repo/shared.txt"
	git -C "$repo" add shared.txt
	git -C "$repo" commit --quiet -m conflict
	git -C "$repo" switch --quiet main

	cat >"$bin/oro" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = status ]; then
	printf '%s\n' "${MOCK_ORO_STATUS:-dispatcher: stopped}"
	exit 0
fi
if [ "$1" = task ] && [ "$2" = show ]; then
	if [ "$3" = oro-dead ]; then
		printf '{"id":"oro-dead","status":"closed"}\n'
	else
		printf '{"id":"%s","status":"open"}\n' "$3"
	fi
	exit 0
fi
if [ "$1" = task ] && [ "$2" = list ]; then
	printf '[]\n'
	exit 0
fi
exit 1
EOF
	cat >"$bin/pgrep" <<'EOF'
#!/usr/bin/env bash
if [ "${MOCK_PGREP_MATCH:-0}" = 1 ]; then
	printf '99999\n'
	exit 0
fi
exit 1
EOF
	chmod +x "$bin/oro" "$bin/pgrep"
}

run_script() {
	local output_file="$fixture/output"
	local rc=0
	PATH="$fixture/bin:$PATH" ORO_EPIC_SNAPSHOT_DIR="$fixture/snapshots" \
		"$SCRIPT" "$fixture/repo" >"$output_file" 2>&1 || rc=$?
	printf '%s\n' "$rc"
}

test_merges_and_snapshots_epics() {
	make_fixture
	local repo="$fixture/repo"
	local behind_before clean_before conflict_before dead_before rc snapshot
	behind_before=$(git -C "$repo" rev-parse epic/oro-behind)
	clean_before=$(git -C "$repo" rev-parse epic/oro-clean)
	conflict_before=$(git -C "$repo" rev-parse epic/oro-conflict)
	dead_before=$(git -C "$repo" rev-parse epic/oro-dead)
	rc=$(run_script)
	[ "$rc" -eq 1 ] || fail "script exit status $rc, want 1 for one conflict" || return

	git -C "$repo" merge-base --is-ancestor main epic/oro-behind || fail "behind epic did not fast-forward" || return
	[ "$(git -C "$repo" rev-parse epic/oro-behind)" = "$(git -C "$repo" rev-parse main)" ] ||
		fail "behind epic is not at main" || return
	git -C "$repo" merge-base --is-ancestor "$clean_before" epic/oro-clean || fail "clean tip was lost" || return
	git -C "$repo" merge-base --is-ancestor main epic/oro-clean || fail "main was not merged into clean epic" || return
	[ "$(git -C "$repo" rev-list --parents -n 1 epic/oro-clean | wc -w | tr -d ' ')" -eq 3 ] ||
		fail "clean epic did not receive a merge commit" || return
	[ "$(git -C "$repo" rev-parse epic/oro-conflict)" = "$conflict_before" ] ||
		fail "conflicting epic ref changed" || return
	[ "$(git -C "$repo" rev-parse epic/oro-dead)" = "$dead_before" ] || fail "dead epic ref changed" || return
	grep -q 'CONFLICT: epic/oro-conflict' "$fixture/output" || fail "conflict was not reported" || return
	grep -q 'SKIP: epic/oro-dead (dead)' "$fixture/output" || fail "dead epic was not reported" || return

	snapshot=$(find "$fixture/snapshots" -type f -name '*.txt' -print -quit)
	[ -n "$snapshot" ] || fail "no epic-tip snapshot written" || return
	grep -q "epic/oro-behind $behind_before" "$snapshot" || fail "behind tip missing from snapshot" || return
	grep -q "epic/oro-clean $clean_before" "$snapshot" || fail "clean tip missing from snapshot" || return
	grep -q "epic/oro-conflict $conflict_before" "$snapshot" || fail "conflict tip missing from snapshot" || return
	grep -q "epic/oro-dead $dead_before" "$snapshot" || fail "dead tip missing from snapshot" || return
}

test_refuses_when_dispatcher_is_running() {
	make_fixture
	local rc
	rc=$(MOCK_ORO_STATUS='dispatcher: running' run_script)
	[ "$rc" -eq 2 ] || fail "script exit status $rc, want 2" || return
	grep -q 'dispatcher is running' "$fixture/output" || fail "running dispatcher was not reported" || return
	[ ! -d "$fixture/snapshots" ] || fail "script mutated snapshot state while dispatcher ran" || return
}

test_refuses_when_quality_gate_is_running() {
	make_fixture
	local rc
	rc=$(MOCK_PGREP_MATCH=1 run_script)
	[ "$rc" -eq 2 ] || fail "script exit status $rc, want 2" || return
	grep -q 'quality gate or worker process is still running' "$fixture/output" ||
		fail "live process was not reported" || return
}

trap cleanup_fixture EXIT

test_case "merges clean epics, aborts conflicts, and snapshots tips" test_merges_and_snapshots_epics
test_case "refuses while dispatcher is running" test_refuses_when_dispatcher_is_running
test_case "refuses while quality gate or worker is running" test_refuses_when_quality_gate_is_running

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
