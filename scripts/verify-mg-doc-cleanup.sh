#!/usr/bin/env bash
set -euo pipefail

legacy_doc="docs/plans/2026-03-15-oro-mg-design.md"
solo_doc="docs/plans/2026-03-21-solo-mode-design.md"
web_doc="docs/plans/2026-03-31-web-dashboard-design.md"

if [ -e "$legacy_doc" ]; then
	echo "legacy oro mg design doc still exists: $legacy_doc" >&2
	exit 1
fi

if grep -nE "\`oro mg\`|^# oro mg" "$solo_doc"; then
	echo "solo-mode design still contains live oro mg references" >&2
	exit 1
fi

bad_web_refs=$(grep -ni 'oro mg' "$web_doc" | grep -viE 'historical|deprecated|removed|legacy' || true)
if [ -n "$bad_web_refs" ]; then
	echo "web dashboard doc has unqualified oro mg references:" >&2
	printf '%s\n' "$bad_web_refs" >&2
	exit 1
fi
