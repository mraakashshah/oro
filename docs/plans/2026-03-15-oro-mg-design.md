# oro mg — Mardi Gras TUI for Oro

**Date:** 2026-03-15
**Status:** Draft

## Goal

Port [Mardi Gras](https://github.com/quietpublish/mardi-gras) into oro as `oro mg` — a BubbleTea TUI for beads issues with the full parade metaphor, colors, confetti, and all visual features. Strip all Gas Town (`gt`) integration and the `agent` launch package, replacing agent dispatch with `oro work <bead-id>`.

## What We Keep

Everything that makes mg *feel* like mg:

| Package | Files | Purpose |
|---------|-------|---------|
| `ui/` | theme.go, symbols.go, styles.go, hop.go, gradient.go, sparkline.go | Full Mardi Gras color palette, unicode symbols, lipgloss styles |
| `data/` | issue.go, loader.go, filter.go, focus.go, watcher.go, mutate.go, source.go, metadata.go, hop.go, exec.go, crossrig.go | Issue types, JSONL/CLI loading, filtering, live updates, bd mutations, cross-rig deps |
| `views/` | parade.go, detail.go | Parade list (Rolling/Lined Up/Stalled/Past), detail pane with deps |
| `components/` | header.go, footer.go, help.go, palette.go, toast.go, create_form.go, float.go, confetti.go | Header with bead shimmer, command palette, toasts, confetti animation |
| `app/` | app.go (gutted), confetti.go, debug.go, deferred_keys.go, oscguard.go | Root BubbleTea model, lifecycle, key routing |
| `tmux/` | status.go | `oro mg --status` widget for tmux status bar |

## What We Strip

| Package/Feature | Lines | Reason |
|-----------------|-------|--------|
| `internal/gastown/` (17 files) | ~4,200 | No Gas Town in oro |
| `internal/agent/` (2 files) | ~230 | Replaced by `oro work` |
| `internal/views/gastown.go` | ~750 | Gas Town control panel |
| `internal/views/problems.go` | ~200 | Uses gastown problem detection |
| `internal/components/recovery_dialog.go` | ~150 | Gas Town rig recovery |
| 25 Model struct fields | — | townStatus, convoy, mail, molecule, sling, nudge, formula fields |
| 20 message types | — | All GT-specific BubbleTea messages |
| 35+ gastown function calls in app.go | — | Sling, convoy, mail, molecule, costs, vitals, etc. |

**Total removal:** ~5,500 lines + ~800 lines of surgery across 6 files

**Note:** `data/crossrig.go` is kept — it contains no gastown imports, only string literals. Cross-rig dependency format (`external:rig:id`) is a data model concept independent of Gas Town.

## What We Add

### 1. `cmd/oro/cmd_mg.go` — Cobra entry point

```go
func newMgCmd() *cobra.Command {
    var (
        path       string
        blockTypes string
        status     bool
    )
    cmd := &cobra.Command{
        Use:   "mg",
        Short: "Mardi Gras parade view for beads issues",
        RunE: func(cmd *cobra.Command, args []string) error {
            // Resolve source, load issues, run TUI
            // --status mode: print tmux status line and exit
        },
    }
    cmd.Flags().StringVar(&path, "path", "", "JSONL file path (default: bd CLI)")
    cmd.Flags().StringVar(&blockTypes, "block-types", "blocks,conditional-blocks", "dependency types that block")
    cmd.Flags().BoolVar(&status, "status", false, "print tmux status line and exit")
    return cmd
}
```

### 2. `pkg/mg/work.go` — Oro work dispatch (replaces agent/)

Replaces `agent.LaunchInTmux()` with `oro work <bead-id>`:

```go
package mg

// LaunchWork dispatches `oro work <beadID>` for the selected issue.
// In tmux: opens in a new split pane. Otherwise: tea.ExecProcess.
func LaunchWork(beadID, projectDir string) (*exec.Cmd, error) {
    return exec.Command("oro", "work", beadID), nil
}

// LaunchWorkInTmux splits a tmux pane running `oro work <beadID>`.
func LaunchWorkInTmux(beadID, projectDir string) (string, error) {
    // tmux split-window -h -l 60% -d -c <projectDir> -- oro work <beadID>
}
```

Key binding: `w` (not `a`) to launch `oro work` on selected bead.

### 3. Package layout in oro

```
oro/
├── cmd/oro/
│   └── cmd_mg.go          # Cobra entry point
├── pkg/mg/
│   ├── app/               # Root model (stripped of gastown)
│   │   ├── app.go
│   │   ├── confetti.go
│   │   ├── debug.go
│   │   ├── deferred_keys.go
│   │   └── oscguard.go
│   ├── views/             # Parade + Detail only
│   │   ├── parade.go
│   │   └── detail.go
│   ├── components/        # Header, footer, help, palette, toast, create, float
│   │   ├── header.go
│   │   ├── footer.go
│   │   ├── help.go
│   │   ├── palette.go
│   │   ├── toast.go
│   │   ├── create_form.go
│   │   └── float.go
│   ├── data/              # Issue types, loading, filtering, mutations
│   │   ├── issue.go
│   │   ├── loader.go
│   │   ├── filter.go
│   │   ├── focus.go
│   │   ├── watcher.go
│   │   ├── mutate.go
│   │   ├── source.go
│   │   ├── metadata.go
│   │   ├── hop.go
│   │   ├── exec.go
│   │   └── crossrig.go
│   ├── ui/                # Theme, symbols, styles (full palette)
│   │   ├── theme.go
│   │   ├── symbols.go
│   │   ├── styles.go
│   │   ├── hop.go
│   │   ├── gradient.go
│   │   └── sparkline.go
│   ├── tmux/              # Status line widget
│   │   └── status.go
│   └── work.go            # oro work dispatch
```

Import paths change from `github.com/matt-wright86/mardi-gras/internal/...` to `oro/pkg/mg/...`.

## Surgery Plan for app.go (2,782 lines)

This is the hardest file. The gastown integration is deeply woven in.

### Model struct — Remove these fields

```
gtEnv, townStatus, gasTown, showGasTown          # Gas Town core
gtPollInFlight, gasTownTicking                    # GT polling
formulaPicking, formulaTarget, formulaMulti       # Sling/formula
nudging, nudgeInput, nudgeTarget                  # Nudge flow
convoyCreating, convoyInput, convoyIssueIDs       # Convoy
mailReplying, mailReplyID, mailReplyInput         # Mail reply
mailComposing, mailComposeStep, mailComposeAddress # Mail compose
mailComposeSubject, mailComposeInput              # Mail compose
showProblems, doctorProblems                      # Problems view
```

### Model struct — Modify

```
agentAvail    → workAvail bool        # Whether oro binary is on PATH
agentRuntime  → (remove)              # No runtime detection needed
activeAgents  → activeWorkers map[string]string  # beadID -> pane/process
```

### Message types — Remove all 20 gastown messages. Keep

```
workLaunchedMsg   { beadID, paneID string }
workFinishedMsg   { err error }
workErrorMsg      { beadID string; err error }
workerStatusMsg   { active map[string]string }
```

### Key handlers — Strip all GT key handlers. Modify

```
"a" → "w"  :  Launch oro work (not agent/sling)
"A" → "W"  :  Kill active worker pane
ctrl+g      :  Remove (was Gas Town panel toggle)
```

### Update() — Remove ~460 lines of gastown message handling (lines 817-1278). Remove all fetch/poll functions for GT data

### View() — Remove Gas Town panel rendering, problems overlay. Keep parade + detail split

### propagateAgentState() → propagateWorkerState()
- Remove townStatus propagation
- Remove orphanedIDs calculation
- Keep activeWorkers sync to parade/detail/header

## New Dependencies (go.mod)

```
charm.land/bubbletea/v2 v2.0.2
charm.land/bubbles/v2 v2.0.0
charm.land/lipgloss/v2 v2.0.2
github.com/atotto/clipboard v0.1.4
github.com/charmbracelet/glamour v1.0.0
github.com/charmbracelet/ultraviolet        # used by app/oscguard.go
github.com/charmbracelet/x/ansi v0.11.6
github.com/lucasb-eyer/go-colorful          # used by ui/gradient.go, ui/sparkline.go
github.com/sahilm/fuzzy v0.1.1
```

## Surgery Plan for Kept Files (5 files beyond app.go)

### views/parade.go
- **Remove**: `gastown` import, `TownStatus *gastown.TownStatus` field, `OrphanedIDs map[string]bool` field
- **Strip**: Agent badge rendering block (~20 lines) that calls `TownStatus.AgentForIssue()`
- **Replace**: Simple worker badge using `ActiveWorkers map[string]string` — if beadID is in activeWorkers, show `⚡` badge (no named agent, just active/inactive)
- **Remove**: OrphanedIDs rendering (orphan detection was Gas Town concept)

### views/detail.go
- **Remove**: `gastown` import, `TownStatus`, `MoleculeDAG`, `MoleculeProgress`, `Comments` fields
- **Remove**: `SetMolecule()`, `SetComments()` methods
- **Strip**: ~250 lines of molecule DAG rendering (`renderMolecule()`, `renderDAGNode()`)
- **Strip**: Comments/timeline section, formula recommendation (`gastown.RecommendFormulas()`)
- **Strip**: Gate status rendering (`renderGateStatus()`)
- **Replace**: Agent info section → simple worker badge using `ActiveWorkers`

### components/header.go
- **Remove**: `gastown` import, `TownStatus *gastown.TownStatus`, `GasTownAvailable bool` fields
- **Strip**: ~30 lines of GT status rendering (working count, unread mail, convoys, MQ status)
- **Keep**: `AgentCount int` → rename to `WorkerCount int`, display as `⚡N`

### components/footer.go
- **Remove**: `hasGasTown` parameter from `NewFooter()` and `BulkFooter()`
- **Strip**: GT-conditional keybinding hints
- **Update**: Agent keybinding hints → worker hints (`w` work, `W` kill)

### components/palette.go
- **Remove**: 9 Gas Town action constants from PaletteAction enum: `ActionLaunchAgent`, `ActionKillAgent`, `ActionSlingFormula`, `ActionNudgeAgent`, `ActionFormulaSelect`, `ActionToggleGasTown`, `ActionCreateConvoy`, `ActionCascadeClose`, `ActionRecoverRigs`
- **Add**: `ActionLaunchWork` (replaces ActionLaunchAgent)
- **Note**: iota values will shift but PaletteAction is not serialized

### components/help.go
- **Strip**: Gas Town keybinding sections (nudge, handoff, decommission, convoy controls)
- **Update**: Agent keybindings → worker keybindings

### app/debug.go
- **Remove**: `gasTownTickMsg` reference

## Worker Polling Mechanism

After stripping Gas Town, `oro mg` tracks active workers via **tmux pane tags**:

1. **Launch**: `w` key runs `tmux split-window ... oro work <beadID>`, tags pane with `@oro_mg_work=<beadID>`
2. **Poll**: Background BubbleTea Cmd polls `tmux list-panes -a -F "#{@oro_mg_work}\t#{pane_id}"` every 3s
3. **State**: `activeWorkers map[string]string` maps beadID → paneID
4. **Display**: Parade shows `⚡` badge on active beads; header shows total count
5. **Kill**: `W` key runs `tmux kill-pane -t <paneID>`
6. **Non-tmux**: Falls back to `tea.ExecProcess` (takes over terminal, no tracking)

This is intentionally simple. Future integration with oro's dispatcher (reading worker state from Dolt/dispatcher API) is out of scope.

## Symbol Cleanup

Remove Gas Town-specific symbols from `symbols.go`:
```
SymAgent, SymConvoy, SymMail, SymSling, SymDog, SymTown
SymDAGFlow, SymDAGBranch, SymDAGFork, SymDAGJoin
```

Keep all parade, priority, dependency, bead, and HOP symbols.

Add:
```
SymWorker = "⚡"   # Reuse agent symbol for oro workers
```

## Header Changes

Strip Gas Town status line (working agents, unread mail, convoys, MQ status).
Keep: parade counts, bead string shimmer, progress bar.
Add: active worker count (simple `⚡2` display).

## Risk Assessment

| Risk | Mitigation |
|------|------------|
| app.go surgery breaks compilation | Port incrementally: strip GT, compile, fix, repeat |
| 5 kept files also need gastown surgery | Explicit surgery plan for each (parade, detail, header, footer, palette) |
| BubbleTea v2 conflicts with oro's deps | No overlapping deps; verified go.mod compatibility |
| Import path rewrite misses a reference | `grep -r mardi-gras` after port to catch stragglers |
| Missing transitive deps | go-colorful and ultraviolet explicitly listed; `go mod tidy` catches any others |
| Tests reference stripped gastown types | 17 test files removed, 5 surgically cleaned, ~30 survive unchanged |
| Worker polling too simple | Intentionally minimal; future integration with dispatcher is out of scope |

## Test Strategy

52 test files in the mardi-gras repo. Three categories:

### Remove (17 files) — gastown/agent packages stripped entirely
- `internal/gastown/*_test.go` (12 files)
- `internal/agent/*_test.go` (2 files)
- `internal/views/gastown_test.go`
- `internal/views/problems_test.go`
- `internal/components/recovery_dialog_test.go`

### Surgery (5 files) — reference gastown types in kept packages
- `internal/app/update_test.go` — strip GT palette action tests, rewrite for `w`/`W` keys
- `internal/app/palette_test.go` — strip tests referencing gastown.TownStatus, gastown.RigStatus
- `internal/app/keys_test.go` — strip nudge/unsling/formula tests, add `oro work` key tests
- `internal/views/detail_test.go` — strip molecule DAG, comment, agent badge tests (~25 gastown refs)
- `internal/components/footer_test.go` — strip `TestNewFooterGasTownAddsBindings`, `TestBulkFooterGasTownBindings`

### Keep unchanged (~30 files)
- `internal/ui/*_test.go` (5 files) — no gastown imports
- `internal/data/*_test.go` (10 files) — no gastown imports (hop_test, contract_test use "gastown" as string literal only)
- `internal/app/app_test.go`, `helpers_test.go`, `confetti_test.go`, `deferred_keys_test.go`, `oscguard_test.go`
- `internal/views/parade_test.go`, `render_test.go`
- `internal/tmux/status_test.go`
- `cmd/mg/main_test.go`

### New tests
- `pkg/mg/work_test.go` — test LaunchWork command construction, tmux pane tagging
- `cmd/oro/cmd_mg_test.go` — test flag parsing, source resolution

### Testdata
Port `testdata/sample.jsonl` for development and `testdata/screenshot.jsonl` for visual testing.

## Out of Scope

- Integrating with oro's dispatcher for worker status (future: read dispatcher state instead of tmux pane tracking)
- Replacing `bd` CLI calls with direct Dolt queries
- Adding oro-specific features (worker logs, cost dashboard)
- Separate `oro-mg` binary (it's a subcommand)
