#!/usr/bin/env bash
set -euo pipefail

require_literal() {
	local file="$1"
	local text="$2"

	if ! grep -Fq "$text" "$file"; then
		printf 'parity docs: %s missing required text: %s\n' "$file" "$text" >&2
		return 1
	fi
}

require_regex() {
	local file="$1"
	local pattern="$2"

	if ! grep -Eq "$pattern" "$file"; then
		printf 'parity docs: %s missing required pattern: %s\n' "$file" "$pattern" >&2
		return 1
	fi
}

readme="README.md"
runbook="docs/runbooks/codex-setup.md"
spec="docs/plans/2026-05-06-codex-harness-parity-design.md"
r1="docs/learnings/codex-plugin-discovery.md"

for file in "$readme" "$runbook" "$spec" "$r1"; do
	if [ ! -f "$file" ]; then
		printf 'parity docs: required file missing: %s\n' "$file" >&2
		exit 1
	fi
done

require_literal "$readme" "codex --version"
require_literal "$readme" "docs/runbooks/codex-setup.md"
require_literal "$readme" "Case B (interactive Codex sessions) is deferred to a future spec"

require_literal "$runbook" "codex --version"
require_literal "$runbook" "Codex CLI installed and authenticated"
require_literal "$runbook" 'CODEX_HOME'
require_literal "$runbook" 'AGENTS.md'
require_literal "$runbook" 'prefix_rule(pattern=["oro"], decision="allow")'
require_literal "$runbook" "prefix_rule entries are Codex command-permission rules"
require_literal "$runbook" "Case B (interactive Codex sessions) is deferred to a future spec"
require_literal "$runbook" "../plans/2026-05-06-codex-harness-parity-design.md"
require_literal "$runbook" "../learnings/codex-plugin-discovery.md"
require_literal "$runbook" 'codex plugin marketplace add "$CODEX_HOME/oro-marketplace"'
require_literal "$runbook" ".agents/plugins/marketplace.json"
require_literal "$runbook" "plugins/oro/.codex-plugin/plugin.json"
require_literal "$runbook" "plugins/oro/hooks.json"
require_literal "$runbook" "plugins/oro/skills/"
require_literal "$runbook" '"source": {"source": "local", "path": "./plugins/oro"}'
require_literal "$runbook" '"skills": "./skills/"'
require_literal "$runbook" "Oro does not install plugins into ~/.codex/plugins/"
require_literal "$runbook" "Oro does not install plugins into ~/.codex/.tmp/plugins/"

require_regex "$runbook" 'oro (agent-assets|setup|start)'

echo "parity docs: ok"
