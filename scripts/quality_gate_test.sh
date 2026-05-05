#!/usr/bin/env bash
# Unit test for scripts/quality_gate.sh parallel_checks output capture.
# Asserts that on a failing parallel check, the FULL command output is emitted
# (not truncated by head -N), so downstream consumers (workers, dispatcher
# events) see the actual failing test name even if it appears past line 20.

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
qg_script="${script_dir}/quality_gate.sh"

if [ ! -f "$qg_script" ]; then
	echo "FAIL: $qg_script not found" >&2
	exit 1
fi

# Stub the QG_DIR / color vars expected by parallel_checks, then source the
# function. The script entry point runs only when invoked directly; sourcing
# is safe because the bottom of quality_gate.sh checks BASH_SOURCE.
QG_DIR="$(mktemp -d -t qg_test.XXXXXX)"
trap 'rm -rf "$QG_DIR"' EXIT
export QG_DIR

# Source the script in a way that defines the function but does NOT run main.
# quality_gate.sh runs main directly when invoked, so we extract just the
# parallel_checks definition by setting a sentinel that short-circuits main.
# Simplest approach: source in a subshell that sets BASH_SOURCE != $0.
# We do this by sourcing inside a wrapper that exposes the function only.
# shellcheck disable=SC1090
source <(sed -n '/^parallel_checks() {/,/^}/p' "$qg_script")

# Provide minimal color/decoration vars.
BLUE="" NC="" GREEN="" YELLOW="" RED=""
export BLUE NC GREEN YELLOW RED

# Build a failing command that emits 60 lines, with a unique sentinel on line 50.
# Uses 'false' (not 'exit 1') because eval runs in the current shell — exit
# would terminate the wrapping subshell before the failure-display branch runs.
SENTINEL="line50_sentinel_$$"
fail_cmd="for i in \$(seq 1 60); do
	if [ \"\$i\" = 50 ]; then echo \"${SENTINEL}\"; else echo \"line\$i\"; fi
done; false"

# Capture parallel_checks output.
out_file="$(mktemp -t qg_test_out.XXXXXX)"
parallel_checks "test_step" "$fail_cmd" >"$out_file" 2>&1

if ! grep -q "$SENTINEL" "$out_file"; then
	echo "FAIL: sentinel '$SENTINEL' not found in parallel_checks output" >&2
	echo "--- captured ---" >&2
	cat "$out_file" >&2
	echo "--- end ---" >&2
	rm -f "$out_file"
	exit 1
fi

rm -f "$out_file"
echo "PASS: parallel_checks emitted full output including line 50"
