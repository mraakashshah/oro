# Codex Plugin Discovery

> Historical research note: Oro no longer distributes its worker skills through
> a marketplace plugin. The supported implementation links skills directly into
> `$CODEX_HOME/skills` and writes managed hooks to `$CODEX_HOME/config.toml`.
> Marketplace observations below remain useful as Codex platform research, but
> the former Oro plugin proposal is rejected for non-interactive workers.

Research date: 2026-05-06  
Codex version: 0.128.0

## user-plugins-dir

**Verdict: `~/.codex/plugins/<name>/` is NOT an auto-discovery root.**

### What was tested

A stub plugin was placed at `~/.codex/plugins/oro-test-plugin/.codex-plugin/plugin.json`:

```json
{
  "name": "oro-test-plugin",
  "version": "0.0.1",
  "description": "Stub plugin to test ~/.codex/plugins/<name>/ discovery path.",
  "author": {
    "name": "oro-research",
    "url": "https://github.com/aakashshah/oro"
  },
  "skills": "./skills/"
}
```

After placing the stub, codex was queried in two ways:

1. `codex exec "List all loaded plugins"` — output: `{"plugins":["github"]}` (stub NOT present)
2. `codex debug prompt-input` — no plugin context for `oro-test-plugin`

```
plugin_loaded: false
```

### How codex actually discovers plugins

Codex uses a **marketplace-based** plugin system with two tiers:

**Tier 1 — Curated plugins (remote, auto-synced)**
- Path: `~/.codex/.tmp/plugins/plugins/<name>/`
- Synced from the official `openai/plugins` GitHub repo
- Updated automatically on codex startup
- Contains ~100+ official plugins (figma, github, linear, notion, etc.)

**Tier 2 — User/local plugins (manual registration)**
- Require a marketplace root directory registered via CLI
- The marketplace root must contain `.agents/plugins/marketplace.json`
- Each plugin entry in `marketplace.json` uses a relative source path

### The correct marketplace.json schema

From the curated marketplace at `~/.codex/.tmp/plugins/.agents/plugins/marketplace.json`:

```json
{
  "name": "openai-curated",
  "interface": {
    "displayName": "Codex official"
  },
  "plugins": [
    {
      "name": "my-plugin",
      "source": {
        "source": "local",
        "path": "./plugins/my-plugin"
      },
      "policy": {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL"
      }
    }
  ]
}
```

Key schema notes:
- Plugin entry key is `"name"` (not `"id"`)
- `source.source` is `"local"` (not `"type"`)
- Paths are relative to the `marketplace.json` location
- `policy.installation` and `policy.authentication` are optional for user plugins

### Template plugin.json (from figma plugin)

Full schema reference from `~/.codex/.tmp/plugins/plugins/figma/.codex-plugin/plugin.json`:

```json
{
  "name": "figma",
  "version": "2.0.7",
  "description": "Figma workflows for design implementation...",
  "author": {
    "name": "Figma",
    "url": "https://www.figma.com"
  },
  "homepage": "https://www.figma.com",
  "repository": "https://github.com/openai/plugins",
  "license": "LicenseRef-Figma-Developer-Terms",
  "keywords": ["figma", "design-to-code"],
  "skills": "./skills/",
  "apps": "./.app.json",
  "interface": {
    "displayName": "Figma",
    "shortDescription": "...",
    "longDescription": "...",
    "developerName": "Figma",
    "category": "Design",
    "capabilities": ["Interactive", "Read", "Write"],
    "websiteURL": "https://www.figma.com",
    "privacyPolicyURL": "...",
    "termsOfServiceURL": "...",
    "defaultPrompt": ["Inspect a Figma design and implement it in code"],
    "brandColor": "#1ABCFE",
    "composerIcon": "./assets/figma.png",
    "logo": "./assets/figma.png",
    "screenshots": []
  }
}
```

### ~/.codex/config.toml

```toml
model = "gpt-5.5"
model_reasoning_effort = "medium"

[projects."/path/to/project"]
trust_level = "trusted"

[marketplaces.home-local]
last_updated = "2026-05-06T22:41:45Z"
source_type = "local"
source = "/Users/as21/local-plugins"
```

The `[marketplaces.<name>]` section is appended by `codex plugin marketplace add`.

### How to register a user plugin (correct procedure)

```bash
# 1. Create the marketplace root structure
mkdir -p ~/my-marketplace/.agents/plugins
mkdir -p ~/my-marketplace/plugins/my-plugin/.codex-plugin

# 2. Create marketplace.json
cat > ~/my-marketplace/.agents/plugins/marketplace.json << 'EOF'
{
  "name": "my-local",
  "interface": {"displayName": "My Local Plugins"},
  "plugins": [
    {
      "name": "my-plugin",
      "source": {"source": "local", "path": "./plugins/my-plugin"}
    }
  ]
}
EOF

# 3. Create plugin.json
cat > ~/my-marketplace/plugins/my-plugin/.codex-plugin/plugin.json << 'EOF'
{
  "name": "my-plugin",
  "version": "0.1.0",
  "description": "...",
  "skills": "./skills/"
}
EOF

# 4. Register the marketplace
codex plugin marketplace add ~/my-marketplace
# Output: Added marketplace `my-local` from /Users/.../my-marketplace
```

### codex hooks list equivalent

There is no `codex hooks list` subcommand in v0.128.0. Available plugin-related commands:

- `codex plugin marketplace add <path>` — register a local marketplace
- `codex plugin marketplace upgrade` — upgrade registered marketplace
- `codex plugin marketplace remove` — remove a marketplace
- `codex debug prompt-input` — dump the full model-visible prompt (includes skills, no raw plugin list)
- `codex exec "..."` — non-interactive agent run; responds with loaded plugin names when asked

### Observation: plugins require UI installation

Even after `codex plugin marketplace add` succeeds, the plugin status is "AVAILABLE" (not active).
Active installation requires the codex TUI (interactive mode) to navigate the plugin marketplace UI.
The `codex exec` command operates with currently-installed plugins only; it cannot install from CLI.

### Edge cases

- `~/.codex/plugins/` directory was created manually during testing but codex does NOT scan it for auto-discovery
- `codex plugin marketplace add ~/.codex/plugins` errors: "marketplace root does not contain a supported manifest" — confirming `~/.codex/plugins/` has no special meaning
- Adding a marketplace with wrong `source.type` instead of `source.source` causes schema parse failure with "missing field `name`" error
- `~/.agents/` directory exists on this machine (with skills), but `~/.agents/plugins/` requires manual creation; codex does not auto-create it

---

## marketplace-path

**Verdict: `~/.codex/.tmp/plugins/plugins/<name>/` is the marketplace DOWNLOAD CACHE — ephemeral, wiped on each startup sync.**

### What was tested

A stub plugin was placed at `~/.codex/.tmp/plugins/plugins/oro-test-plugin/.codex-plugin/plugin.json` at `2026-05-06 18:44:33`:

```json
{
  "name": "oro-test-plugin",
  "version": "0.0.1",
  "description": "Stub plugin to verify .tmp marketplace path discovery.",
  "author": {
    "name": "oro-research",
    "url": "https://github.com/aakashshah/oro"
  },
  "skills": "./skills/"
}
```

### Filesystem evidence (before stub placement)

All 116 marketplace plugins in `~/.codex/.tmp/plugins/plugins/` share the **identical** timestamp `2026-05-06 16:44:13` — this means Codex performed a single atomic marketplace sync at startup and wrote all plugins simultaneously.

```
~/.codex/.tmp/
├── app-server-remote-plugin-sync-v1   (content: "ok", mtime: 2026-04-22)
├── marketplaces/                       (empty dir, mtime: 2026-05-06 18:40:09)
├── plugins/                            (mtime: 2026-05-06 16:44:13)
│   └── plugins/
│       ├── figma/                      (mtime: 2026-05-06 16:44:13)
│       ├── github/                     (mtime: 2026-05-06 16:44:13)
│       ├── linear/                     (mtime: 2026-05-06 16:44:13)
│       └── ... (113 more, all same timestamp)
└── plugins.sha                         (cc8b22955285a060a50d33b594c66db1e61c24c0)
```

### (a) Does the stub load on codex startup?

**NO.** Empirical test: after stub placement at `18:44:33`, a fresh `codex exec` invocation (session id `019dffbb-19f0-75f0-9347-301e3a8da260`) was queried with `"List all loaded plugins by name"`. Response: `{"plugins":["github"]}` — the stub is **not** loaded. This is consistent with the `user-plugins-dir` finding: Codex requires explicit installation through the marketplace TUI, regardless of the source path on disk.

The `.tmp/plugins/plugins/<name>/` directory is the **AVAILABLE pool**, not the **INSTALLED set**. Of the 116 official plugins present in `.tmp/plugins/plugins/`, only `github` is reported as loaded — confirming this directory is a discovery-source-of-truth for the marketplace UI, not a load list.

### (b) Does the stub survive a codex restart?

**YES (empirically confirmed) — but only under `codex exec`.** Two restart-loop tests were run:

**Test 1 — SHA-match path (sync skipped):**
- Pre-state: `plugins.sha = cc8b22...`, stub at `18:44:33`, figma at `16:44:13`.
- Action: ran `codex exec` (fresh process, session id `019dffbb-19f0-75f0-9347-301e3a8da260`).
- Post-state: stub mtime UNCHANGED (`18:44:33`), figma mtime UNCHANGED (`16:44:13`), `plugins.sha` UNCHANGED (`cc8b22...`, mtime `16:44:13`).
- **Stub survived.**

**Test 2 — SHA-mismatch path (forced sync):**
- Pre-state: overwrote `plugins.sha` with `deadbeef0000...` to force a re-sync if Codex performs SHA validation.
- Action: ran `codex exec` (fresh process, session id `019dffbb-dddf-7bd3-8a1d-6985f7c5340d`). Output: `{"plugins":["github"]}`.
- Post-state: stub mtime UNCHANGED (`18:44:33`), figma mtime UNCHANGED (`16:44:13`), `plugins/` dir mtime UNCHANGED (`18:44:33`). `plugins.sha` mtime updated (`19:59:50`) but **content remained `deadbeef...`** — Codex did not overwrite the SHA, did not wipe `.tmp/plugins/plugins/`, and did not re-extract the bundle.
- **Stub survived even with intentionally-corrupted SHA.**

**Inference:** Marketplace sync is **not triggered by `codex exec`** — it is a TUI-only flow. The `.tmp/plugins/plugins/` directory is populated by interactive Codex sessions when the marketplace bundle changes. A non-interactive `exec` call neither validates nor refreshes the cache.

### Restart-loop evidence

```text
T0 = 16:44:13  initial marketplace sync (figma, github, ... 116 plugins)
T1 = 18:44:33  stub placed at .tmp/plugins/plugins/oro-test-plugin/
T2 = 19:59:01  codex exec #1 (SHA matches)        → stub survived, not loaded
T3 = 19:59:30  plugins.sha overwritten with deadbeef
T4 = 19:59:50  codex exec #2 (SHA mismatch)       → stub survived, not loaded, sha NOT regenerated
T5 = 19:59:51  plugins.sha restored
```

Two restart events (`T2`, `T4`) confirmed the stub persists across `codex exec` invocations. **Caveat:** the TUI restart path (`codex` interactive) was NOT exercised in this research session because launching the TUI inside an automated worker is not feasible. Based on the SHA-based design (`plugins.sha` as a bundle-version marker), a TUI restart with a SHA-mismatch is the most likely path that would wipe and re-extract the cache.

### Correct vs. incorrect paths

| Path | Type | Persistent under `codex exec`? | Loads? |
|------|------|--------------------------------|--------|
| `~/.codex/.tmp/plugins/plugins/<name>/` | Marketplace AVAILABLE cache | **Yes** (exec does not sync); TUI restart with new bundle SHA likely wipes | **No** (must be installed via TUI, regardless of presence on disk) |
| `~/.codex/plugins/<name>/` | Not used by Codex | Yes (fs-stable, Codex never reads it) | **No** (not auto-discovered) |
| `~/local-plugins/plugins/<name>/` | Local marketplace source | Yes | **Yes** (after marketplace add + TUI install) |

### Historical rejected Oro plugin proposal

The 2026-05 research concluded that an Oro marketplace plugin would have required
the following steps. Oro does not use this distribution path; these are retained
only to explain why direct personal-skill installation replaced it.

To load a generic local plugin in the researched Codex version:
1. The plugin files must live in a stable directory (e.g., `~/local-plugins/plugins/oro/`)
2. A `marketplace.json` must register the plugin under `.agents/plugins/`
3. `codex plugin marketplace add ~/local-plugins` must be run once (writes to `config.toml`)
4. User must install via Codex TUI — the `codex exec` path cannot trigger installation
5. **Do not use `~/.codex/.tmp/` for anything.** It is the marketplace download cache: stub files placed there persist across `codex exec` (sync skipped) but provide no functionality, and would be wiped by any TUI-driven marketplace re-sync that detects a SHA mismatch.

---

## config-toml-plugins

**Summary:** `~/.codex/config.toml` does NOT support a `[plugin]` or `[[plugins]]` key that points to an arbitrary plugin path. The recognized mechanism is `[marketplaces.NAME]` with `source_type = "local"`.

### config_toml_supports

**Finding:** config.toml supports the `[marketplaces.NAME]` section for local plugin discovery, not `[plugin]` or `[[plugins]]`.

---

### TOML Key Names Tested

#### `[plugin]` (table)

```toml
model = "gpt-4o"

[plugin]
path = "/tmp/oro-test-plugin/"
```

**Result:** Silently ignored.
- `codex --version`: returns `codex-cli 0.128.0` unchanged (--version does not load config at all)
- `codex app-server`: starts normally, no error on stdout or stderr, continues running

#### `[[plugins]]` (array of tables)

```toml
model = "gpt-4o"

[[plugins]]
path = "/tmp/oro-test-plugin/"
```

**Result:** Logs an error on stderr, falls back to defaults, continues running.
- `codex --version`: returns `codex-cli 0.128.0` (no error — --version skips config load)
- `codex app-server` stderr:
  ```
  ERROR codex_app_server: Invalid configuration; using defaults. /private/tmp/codex-test-home/config.toml:3:1: invalid type: sequence, expected a map
  ```
- The error "invalid type: sequence, expected a map" means codex reserves the key `plugins` internally (expects it to be a table/map), but TOML array-of-tables creates a sequence. App-server falls back to defaults and does not panic.
- Exit behavior: server starts and continues running (exit only on timeout/kill)

---

### Correct TOML Key: `[marketplaces.NAME]`

The only supported mechanism for adding local plugin paths is via the marketplace system:

```toml
[marketplaces.my-marketplace]
last_updated = "2026-05-09T09:13:14Z"
source_type = "local"
source = "/path/to/marketplace-directory"
```

This is what `codex plugin marketplace add /path/to/dir` writes into config.toml.

**Exact key name:** `marketplaces` (TOML table-of-tables, one entry per named marketplace)

---

### Marketplace Directory Structure Requirements

A local marketplace directory must contain `.agents/plugins/marketplace.json`:

```
/path/to/marketplace/
├── .agents/
│   └── plugins/
│       └── marketplace.json        ← required manifest
└── plugins/
    └── my-plugin/
        └── .codex-plugin/
            └── plugin.json         ← plugin manifest
```

**`marketplace.json` schema** (from test-marketplace):
```json
{
  "name": "marketplace-name",
  "interface": { "displayName": "Display Name" },
  "plugins": [
    {
      "name": "plugin-name",
      "source": { "source": "local", "path": "./plugins/plugin-name" },
      "policy": { "installation": "AVAILABLE", "authentication": "ON_INSTALL" },
      "category": "Productivity"
    }
  ]
}
```

**`plugin.json` schema** (from figma plugin):
```json
{
  "name": "plugin-name",
  "version": "1.0.0",
  "description": "...",
  "author": { "name": "...", "url": "..." },
  "skills": "./skills/",
  "apps": "./.app.json",
  "interface": {
    "displayName": "...",
    "shortDescription": "...",
    "longDescription": "...",
    "developerName": "...",
    "category": "...",
    "capabilities": ["Interactive", "Read", "Write"],
    "websiteURL": "...",
    "privacyPolicyURL": "...",
    "termsOfServiceURL": "...",
    "defaultPrompt": ["...", "...", "..."],
    "brandColor": "#RRGGBB",
    "composerIcon": "./assets/icon.png",
    "logo": "./assets/logo.png",
    "screenshots": []
  }
}
```

---

### Error Handling Summary

| Config Entry | `--version` | `app-server` stderr | App behavior |
|---|---|---|---|
| `[plugin]` + arbitrary keys | No error | No error | Silently ignored; server runs |
| `[[plugins]]` + arbitrary keys | No error | `ERROR: invalid type: sequence, expected a map` | Falls back to defaults; server runs |
| `[marketplaces.x]` → missing path | No error | No error | Silently ignored; server runs |
| `[marketplaces.x]` → dir without manifest | No error | No error | Silently ignored; server runs |
| Invalid TOML syntax | No error | — (depends on command) | `--version` skips config entirely |

**Key insight:** `codex --version` does NOT load config.toml. Errors only surface when starting the app-server or TUI session. This means config mistakes can go unnoticed until first interactive use.

---

### `codex --version` behavior

`--version` outputs `codex-cli 0.128.0` regardless of config.toml contents, including completely invalid TOML. It is not useful as a before/after comparison for config changes.

Use `codex app-server` (with `timeout`) or `codex debug models` to verify config is loaded correctly.

---

### Reference: config.toml snapshot at time of research

```toml
model = "gpt-5.5"
model_reasoning_effort = "medium"

[projects."/Users/as21/codehouse/oro"]
trust_level = "trusted"

[tui]
status_line = ["model-with-reasoning", "current-dir", "git-branch", "run-state", "context-used", "five-hour-limit", "weekly-limit"]

[marketplaces.home-local]
last_updated = "2026-05-06T22:41:45Z"
source_type = "local"
source = "/Users/as21/local-plugins"

[marketplaces.test-local]
last_updated = "2026-05-07T03:15:34Z"
source_type = "local"
source = "/private/tmp/test-marketplace"
```

codex version: `0.128.0`
