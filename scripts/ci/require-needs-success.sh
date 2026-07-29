#!/usr/bin/env bash
set -euo pipefail

needs_json=${1:?usage: require-needs-success.sh '<needs-json>'}
readonly required_jobs=(go cgo-free shell docs python)

if ! printf '%s\n' "$needs_json" | jq -e --argjson required "$(printf '%s\n' "${required_jobs[@]}" | jq -R . | jq -s .)" '
  . as $needs |
  type == "object" and
  all($required[]; ($needs[.]? | type == "object" and .result == "success"))
' >/dev/null; then
	printf 'portable gate dependency did not conclude success\n' >&2
	exit 1
fi
