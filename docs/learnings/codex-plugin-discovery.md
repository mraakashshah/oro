# Codex Plugin Discovery

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

**NO.** The `.tmp/plugins/plugins/` directory is populated only at startup sync time (`16:44:13`). A file placed after startup (`18:44:33`) is not re-scanned during the session — Codex does not hot-reload plugins at runtime.

### (b) Does the stub survive a codex restart?

**Almost certainly NO.** Evidence:

1. **`.tmp` prefix** — The path `/Users/as21/.codex/.tmp/` is named with a `.tmp` prefix by design. This is a universally understood signal for ephemeral/cache storage.

2. **SHA-based bundle check** — `~/.codex/.tmp/plugins.sha` contains `cc8b22955285a060a50d33b594c66db1e61c24c0`. On startup Codex downloads the marketplace bundle, computes its SHA, and compares to this file:
   - **SHA matches** (same marketplace version): sync may be skipped. Whether the stub survives in this case is undefined — the directory is treated as a bundle extraction target, not a user-writable namespace.
   - **SHA differs** (new marketplace version): Codex wipes `~/.codex/.tmp/plugins/` and re-extracts the new bundle. Stub is destroyed unconditionally.

3. **Single-timestamp sync pattern** — All 116 official plugins have the same timestamp. This is consistent with `rm -rf ~/.codex/.tmp/plugins/ && untar bundle/` semantics, not an incremental diff. A hand-placed file in the directory has no protection.

4. **No manifest entry** — The stub has no entry in the marketplace bundle's manifest. Even if Codex does a diff-based sync (add new, remove deleted from manifest), the stub would be removed as an "unlisted" entry.

### Restart-loop evidence

The stub was placed at `18:44:33`. The session continued and Codex was **not** restarted during this research task (restarting would end the agent session). However, the cumulative evidence above strongly predicts wipe-on-restart:

- The `.tmp/` directory received its current state at `16:44:13` — EARLIER than when `~/.codex/plugins/oro-test-plugin/` was placed (`18:39:55`) by the sibling `user-plugins-dir` research task.
- The sibling task confirmed that Codex does not load `~/.codex/plugins/oro-test-plugin/` — yet that plugin WAS placed before this research session began. This tells us the "current session's codex" already did its startup sync at `16:44:13` and nothing placed after that gets picked up until restart.
- Therefore: the stub placed at `18:44:33` will first be visible to Codex only after a restart. But at that restart, the SHA-based sync runs and wipes `.tmp/plugins/plugins/`.

### Correct vs. incorrect paths

| Path | Type | Persistent? | Loads? |
|------|------|-------------|--------|
| `~/.codex/.tmp/plugins/plugins/<name>/` | Marketplace cache | **No** (wiped on sync) | **No** (startup only) |
| `~/.codex/plugins/<name>/` | Installed path | Yes (fs-stable) | **No** (not auto-discovered) |
| `~/local-plugins/plugins/<name>/` | Local marketplace source | Yes | **Yes** (after marketplace add + TUI install) |

### Implications for oro plugin installation

To ship an oro plugin that loads in Codex:
1. The plugin files must live in a stable directory (e.g., `~/local-plugins/plugins/oro/`)
2. A `marketplace.json` must register the plugin under `.agents/plugins/`
3. `codex plugin marketplace add ~/local-plugins` must be run once (writes to `config.toml`)
4. User must install via Codex TUI — the `codex exec` path cannot trigger installation
5. **Do not use `~/.codex/.tmp/` for anything.** It is a download cache.
