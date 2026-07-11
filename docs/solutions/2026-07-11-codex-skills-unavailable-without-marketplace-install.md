# Codex Skills Unavailable Without Marketplace Installation

**Date:** 2026-07-11
**Component:** Codex startup asset installation
**Severity:** high

## Symptom

Oro could generate a local Codex marketplace package, but Codex reported the
plugin as `AVAILABLE` rather than active. Non-interactive `codex exec` did not
install it, so workers could start without Oro's skills. Pointing Codex at an
ordinary directory also failed with `marketplace root does not contain a
supported manifest`.

## Investigation

The marketplace cache, a user plugin directory, and `config.toml` plugin paths
were tested. Files in the marketplace download cache were not loaded, and a
local marketplace still required a separate interactive TUI installation.
That makes marketplace registration unsuitable as an unattended startup
dependency. The detailed platform experiments remain in
[`docs/learnings/codex-plugin-discovery.md`](../learnings/codex-plugin-discovery.md).

## Root Cause

Oro treated marketplace discovery as though it implied plugin activation.
Codex separates those states: registration only makes a plugin available, while
activation is user-controlled. The generated plugin therefore could not
guarantee skill availability before a worker launched.

Startup also needs to account for effective routing, not only the top-level
agent runtime. A mixed configuration can route review or operations roles to
Codex while another runtime owns the primary tier.

## Solution

Codex startup now installs Oro skills through Codex's personal skill discovery
path:

- `cmd/oro/cmd_global_oro_approach.go:239` validates the canonical
  `~/.oro/.claude/skills/using-skills/SKILL.md` source and links each skill into
  `$CODEX_HOME/skills`.
- `cmd/oro/cmd_global_oro_approach.go:285` stages each link under a uniquely
  named temporary directory and renames it into place, allowing concurrent
  startup runs to converge and replacing legacy copied directories.
- `pkg/agentmodel/agentmodel.go:28` detects Codex in any effective CLI tier or
  role, and `cmd/oro/cmd_start.go:556` uses that result before launching workers.
- `assets/hooks/session_start_global.py:75` loads the bootstrap skill from the
  same canonical Oro source.

The obsolete marketplace generation and registration path was removed. The
explicit asset-only command remains fail-open when an optional source is absent,
but worker startup fails before launch if the required bootstrap skill is
missing.

## Prevention

Keep the aggregate Codex setup acceptance test in `cmd/oro/cmd_start_test.go`.
It covers direct discovery without marketplace state, missing-source failure,
concurrent convergence, legacy directory replacement, runtime overrides, mixed
providers, and Claude-only isolation. Keep the live discipline test asserting
both the skill link and the absence of generated marketplace state.

When adding a runtime-specific startup dependency, base installation on the
effective tier and role routes, and verify that a non-interactive worker can
consume the installed artifact before relying on a discovery mechanism.

## Related

- [Codex setup runbook](../runbooks/codex-setup.md)
- [Codex plugin discovery research](../learnings/codex-plugin-discovery.md)
- [Direct skill setup design](../plans/2026-07-11-codex-direct-skill-setup-design.md)
