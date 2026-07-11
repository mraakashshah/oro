# Codex Setup Runbook

This runbook documents the Codex side of the harness parity work from
[the Codex harness parity spec](../plans/2026-05-06-codex-harness-parity-design.md).
The direct skill discovery decision follows
[the Codex direct skill setup design](../plans/2026-07-11-codex-direct-skill-setup-design.md).

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

The asset-only Codex sync links portable skills into `$CODEX_HOME/skills`.
Skill links point to Oro's installed source under `~/.oro/.claude/skills`, so
skill edits and Oro upgrades are visible without copying or plugin installation.

When any configured CLI tier or role can use Codex, `oro start` also ensures the
Codex assets before launching the dispatcher: skill links, command rules under
`$CODEX_HOME/rules`, and managed hooks in `$CODEX_HOME/config.toml`. This covers the
`ORO_AGENT_RUNTIME=codex` override and mixed-provider modes where review or ops
roles use Codex even if the primary worker uses Claude. Claude-only routing does
not modify `$CODEX_HOME`.

Startup additionally writes `AGENTS.md` at the project root so Codex sessions
receive the same project instructions that Claude reads from `CLAUDE.md`.

## Direct Discovery Path

Oro uses Codex's personal skills directory directly:

```text
~/.oro/.claude/skills/using-skills/
└── SKILL.md

$CODEX_HOME/
├── skills/
│   ├── using-skills -> ~/.oro/.claude/skills/using-skills
│   └── ...          -> ~/.oro/.claude/skills/...
├── rules/
│   └── oro.rules
└── config.toml      # contains Oro's managed hooks block
```

No marketplace registration, plugin cache mutation, or interactive installation
step is part of Oro setup. Older Oro versions may have left a legacy marketplace
directory in a Codex home; current sync and startup paths ignore it and do not
delete user-home content automatically.

Codex-capable startup requires the canonical
`~/.oro/.claude/skills/using-skills/SKILL.md` source. If it is missing, startup
stops before launching the dispatcher rather than running an undisciplined Codex
worker. Reinstall or upgrade Oro to restore the source, then rerun `oro start`.

The SessionStart hook loads the same canonical file directly. The hook itself is
fail-open: if the file becomes unreadable during a running Codex session, it
still injects Oro's compact discipline block.

## Managed Hooks

Oro writes a marked hooks block directly into `$CODEX_HOME/config.toml`. Repeated
startup replaces only that block and preserves user configuration outside it.
The block wires SessionStart, PreToolUse, PostToolUse, and Stop events to the
shared scripts under `~/.oro/hooks`; those scripts are not duplicated into the
Codex home.

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
