# Codex Setup Runbook

This runbook documents the Codex side of the harness parity work from
[the Codex harness parity spec](../plans/2026-05-06-codex-harness-parity-design.md).
The plugin discovery rules come from
[the R1 Codex plugin discovery outcome](../learnings/codex-plugin-discovery.md).

## Scope

Oro supports Case A from the parity spec: dispatcher-spawned Codex workers
started by `oro start`, `oro dispatcher start`, or `oro work` through the Codex
runtime adapter.

Case B (interactive Codex sessions) is deferred to a future spec. A user running
`codex` directly is not registered as an Oro worker, has no dispatcher worker ID,
and cannot use the existing worker handoff path.

## Requirements

- macOS with `tmux` installed for swarm sessions.
- Oro installed from `scripts/install.sh`, `make install`, or a release binary.
- Codex CLI installed and authenticated:

```bash
codex --version
```

`CODEX_HOME` is optional. If unset, Oro uses `~/.codex`.

## Install Assets

For normal project setup, run:

```bash
oro setup
```

For asset-only sync, run:

```bash
oro agent-assets --runtime codex
```

The Codex sync installs portable skills into `$CODEX_HOME/skills`, installs
Codex command rules under `$CODEX_HOME/rules`, and writes the Oro local
marketplace package under `$CODEX_HOME/oro-marketplace`.

When Codex is the active worker runtime, `oro start` also ensures project
runtime assets are present. That includes writing `AGENTS.md` at the project
root so Codex sessions receive the same project instructions that Claude reads
from `CLAUDE.md`.

## Plugin Discovery Path

Codex does not auto-discover plugins from `~/.codex/plugins/<name>`. It also
does not load plugins just because files exist in `~/.codex/.tmp/plugins/`.
Oro does not install plugins into ~/.codex/plugins/. Oro does not install plugins into ~/.codex/.tmp/plugins/.

The R1-supported path is a local marketplace:

The required marketplace manifest path is `.agents/plugins/marketplace.json`.
The Oro plugin files live under `plugins/oro/.codex-plugin/plugin.json`,
`plugins/oro/hooks.json`, and `plugins/oro/skills/`.

```text
$CODEX_HOME/oro-marketplace/
├── .agents/
│   └── plugins/
│       └── marketplace.json
└── plugins/
    └── oro/
        ├── .codex-plugin/
        │   └── plugin.json
        ├── hooks.json
        └── skills/
```

The marketplace manifest registers the plugin by relative path:

```json
{
  "name": "oro-marketplace",
  "interface": {"displayName": "Oro local"},
  "plugins": [
    {
      "name": "oro",
      "source": {"source": "local", "path": "./plugins/oro"}
    }
  ]
}
```

The plugin manifest lives at `plugins/oro/.codex-plugin/plugin.json`:

```json
{
  "name": "oro",
  "version": "0.1.0",
  "description": "Oro workflow guidance, hooks, and task orchestration support for Codex.",
  "skills": "./skills/"
}
```

Register the marketplace once per Codex home:

```bash
codex plugin marketplace add "$CODEX_HOME/oro-marketplace"
```

After registration, Codex may still mark the plugin as available rather than
active. The R1 result found that active plugin installation is a Codex TUI step;
`codex exec` only uses already-installed plugins.

## Prefix Rules

Oro writes Codex `prefix_rule` entries to the Codex rules file. The
prefix_rule entries are Codex command-permission rules, not shell aliases and
not global bypasses. The
rules only allow the exact command prefixes Oro needs for routine worker tasks,
for example:

```text
prefix_rule(pattern=["oro"], decision="allow")
prefix_rule(pattern=["go", "test"], decision="allow")
prefix_rule(pattern=["make", "stage-assets"], decision="allow")
prefix_rule(pattern=["gofmt"], decision="allow")
prefix_rule(pattern=["goimports"], decision="allow")
prefix_rule(pattern=["golangci-lint"], decision="allow")
```
