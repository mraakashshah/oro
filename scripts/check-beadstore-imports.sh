#!/usr/bin/env bash
set -euo pipefail

if [ ! -d pkg/beadstore ]; then
	echo "pkg/beadstore not found; run from repo root" >&2
	exit 1
fi

matches=$(
	find pkg/beadstore -name '*.go' -type f -exec awk '
		/"oro\/pkg\/(dispatcher|worker|ops|mg)(\/[^"]*)?"/ {
			print FILENAME ":" FNR ":" $0
		}
	' {} + || true
)
if [ -z "$matches" ]; then
	exit 0
fi

printf '%s\n' "$matches" >&2
exit 1
