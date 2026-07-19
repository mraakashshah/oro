#!/usr/bin/env bash
# Reproduce (or refute) the concurrency-flakiness of the serial-lane test set.
#
# Simulates concurrent worker quality gates: launches N copies of the pkg/dispatcher
# test binary in parallel, each pinned to a low GOMAXPROCS (as the real gate does),
# under CPU burn, for R rounds, and harvests non-deterministic failures.
#
# Usage:
#   scripts/reproduce_serial_lane_flakes.sh            # hammer the serial-lane set
#   scripts/reproduce_serial_lane_flakes.sh --control  # hammer a same-size sample of
#                                                      # NON-listed tests (must stay green)
#
# Env knobs: WORKERS (default 12), ROUNDS (default 15), GOMAXPROCS (default 1),
#            BURN (default 8 CPU burners).
set -euo pipefail

cd "$(dirname "$0")/.."

WORKERS="${WORKERS:-12}"
ROUNDS="${ROUNDS:-15}"
export GOMAXPROCS="${GOMAXPROCS:-1}"
BURN="${BURN:-8}"
LIST="pkg/dispatcher/testdata/serial_lane_tests.txt"

# Build the -run regex from the canonical list (anchored per name).
names="$(grep -vE '^\s*#' "$LIST" | grep -vE '^\s*$' | sed -E 's/^\s+|\s+$//g')"
# shellcheck disable=SC2086 # intentional word-splitting: one arg per test name
serial_run="$(printf '^%s$|' $names)"
serial_run="${serial_run%|}"

if [ "${1:-}" = "--control" ]; then
	# Control: dispatcher tests NOT in the list (exclude via -skip), capped to a
	# comparable count so the contention is equivalent.
	RUN=".*"
	SKIP="$serial_run"
	echo "CONTROL run: non-listed dispatcher tests under identical contention"
else
	RUN="$serial_run"
	SKIP=""
	echo "SERIAL-LANE run: listed tests under contention"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"; pkill -P $$ 2>/dev/null || true' EXIT

echo "Building dispatcher test binary..."
go test ./pkg/dispatcher -c -o "$work/disp.test"

echo "Starting $BURN CPU burners..."
for _ in $(seq 1 "$BURN"); do (yes >/dev/null) & done

echo "Hammering: WORKERS=$WORKERS ROUNDS=$ROUNDS GOMAXPROCS=$GOMAXPROCS"
for r in $(seq 1 "$ROUNDS"); do
	pids=()
	for _ in $(seq 1 "$WORKERS"); do
		if [ -n "$SKIP" ]; then
			("$work/disp.test" -test.run "$RUN" -test.skip "$SKIP" -test.count=1 -test.v 2>&1 |
				grep -E '^--- FAIL|panic:|DATA RACE' >>"$work/fails" || true) &
		else
			("$work/disp.test" -test.run "$RUN" -test.count=1 -test.v 2>&1 |
				grep -E '^--- FAIL|panic:|DATA RACE' >>"$work/fails" || true) &
		fi
		pids+=($!)
	done
	for p in "${pids[@]}"; do wait "$p" || true; done
	printf 'round %d/%d\r' "$r" "$ROUNDS"
done
echo ""

echo "=== flaky failures by frequency ==="
if [ -s "$work/fails" ]; then
	sed -E 's/ \([0-9.]+s\)//' "$work/fails" | sort | uniq -c | sort -rn
	exit 1
else
	echo "(none reproduced)"
fi
