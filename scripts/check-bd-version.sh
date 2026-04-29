#!/bin/bash

set -euo pipefail

# Phase 0 source of truth: docs/INSTALL.md records `bd version 1.0.2 (Homebrew)`.
PINNED_MAJOR_MINOR="1.0"
IGNORE_VERSION_DRIFT=0

usage() {
	echo "usage: scripts/check-bd-version.sh [--ignore-version-drift]" >&2
}

if [ "$#" -gt 1 ]; then
	usage
	exit 2
fi

if [ "$#" -eq 1 ]; then
	case "$1" in
	--ignore-version-drift)
		IGNORE_VERSION_DRIFT=1
		;;
	*)
		echo "unknown flag: $1" >&2
		usage
		exit 2
		;;
	esac
fi

if ! command -v bd >/dev/null 2>&1; then
	echo "bd not found on PATH; install pinned bd version ${PINNED_MAJOR_MINOR}.x before Phase 8 migration" >&2
	exit 1
fi

raw_output="$(bd --version 2>&1)"

if [[ ! "$raw_output" =~ (^|[^0-9A-Za-z])v?([0-9]+)\.([0-9]+)\.([0-9]+)([-+][0-9A-Za-z.-]+)? ]]; then
	echo "could not parse bd --version output: $raw_output" >&2
	exit 1
fi

current_major_minor="${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"

if [ "$current_major_minor" = "$PINNED_MAJOR_MINOR" ]; then
	exit 0
fi

if [ "$IGNORE_VERSION_DRIFT" -eq 1 ]; then
	echo "bd version drift override accepted: pinned $PINNED_MAJOR_MINOR, current $current_major_minor" >&2
	exit 0
fi

echo "bd version drifted from $PINNED_MAJOR_MINOR to $current_major_minor; reinstall pinned version or restart from Phase 0" >&2
exit 1
