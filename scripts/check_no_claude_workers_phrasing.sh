#!/usr/bin/env bash
# Verify agent-agnostic tier vocabulary in README.
# Usage: ./scripts/check_no_claude_workers_phrasing.sh [FILE]
# Default FILE: README.md
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

file="${1:-README.md}"
bad=0

# 1. Ensure a Claude-specific compatibility section exists (for isolation).
check_claude_section_exists() {
	if ! rg -q '^#{1,3}\s+Claude' "$file"; then
		printf 'claude-isolated: %s must contain a dedicated Claude section (heading matching "## Claude ...")\n' "$file" >&2
		return 1
	fi
	return 0
}

# 2. Ensure "Claude workers" phrase does not appear outside the Claude section.
check_no_claude_workers_outside_section() {
	local in_claude_section=0
	local line_num=0
	local found=0
	while IFS= read -r line; do
		((line_num++)) || true
		# Section boundary: markdown heading (1-3 hashes)
		if printf '%s' "$line" | rg -q '^#{1,3}\s+'; then
			if printf '%s' "$line" | rg -qi '^#{1,3}\s+Claude'; then
				in_claude_section=1
			else
				in_claude_section=0
			fi
		fi
		# Outside the Claude section — flag "Claude worker(s)" phrasing
		if [[ $in_claude_section -eq 0 ]]; then
			if printf '%s' "$line" | rg -qi 'claude\s+worker'; then
				printf '%s:%d: "%s"\n' "$file" "$line_num" "$line" >&2
				found=1
			fi
		fi
	done < "$file"
	if [[ $found -eq 1 ]]; then
		printf 'claude-workers: "%s" must not use "Claude workers" phrasing outside Claude-specific section\n' "$file" >&2
		return 1
	fi
	return 0
}

# 3. Ensure tier vocabulary is used (at least one tier name paired with "tier").
check_tier_vocabulary_present() {
	if ! rg -qi '\b(fast|balanced|deep|background)\s+tier\b' "$file"; then
		printf 'tier-vocab: %s must use tier vocabulary (e.g. "fast tier", "balanced tier", "deep tier", "background tier")\n' "$file" >&2
		return 1
	fi
	return 0
}

if ! check_claude_section_exists; then
	bad=1
fi
if ! check_no_claude_workers_outside_section; then
	bad=1
fi
if ! check_tier_vocabulary_present; then
	bad=1
fi

if [[ $bad -eq 0 ]]; then
	printf 'OK: %s passes agent-agnostic tier vocabulary checks\n' "$file"
fi

exit "$bad"
