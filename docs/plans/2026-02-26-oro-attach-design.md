# Design: `oro attach` Command

**Date**: 2026-02-26
**Status**: Draft
**Author**: architect

## Problem

When `oro start` detects the dispatcher is already running, it prints status and exits. The user must know to run `tmux attach -t oro` manually. This is a UX gap — the CLI should provide a first-class attach command.

## Decision

Add a new `oro attach` command that connects to a running oro tmux session. Separate from `start` to match the single-purpose verb CLI pattern.

**Premortemed risks accepted:**
- Zombie pane detection: refuse + suggest restart (user chose this over warn+attach or auto-heal)
- Non-TTY: detect and error early
- Multi-project: forward-compatible via `TmuxSession.Name` but not scoped yet

## Behavior Matrix

| Daemon | Tmux session | Healthy? | TTY? | Behavior |
|--------|-------------|----------|------|----------|
| Running | Exists | Yes | Yes | Attach (select architect window first) |
| Running | Exists | No | Yes | Error: "session unhealthy — run `oro stop && oro start`" |
| Running | Exists | Yes | No | Error: "cannot attach without a terminal" |
| Running | Missing | — | — | Error: "dispatcher running in daemon-only mode, no tmux session" |
| Stopped | — | — | — | Error: "no running session — use `oro start`" |
| Stale | — | — | — | Error: "stale PID found — run `oro cleanup` then `oro start`" |

## Implementation

### New file: `cmd/oro/cmd_attach.go`

```go
func newAttachCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "attach",
        Short: "Connect to a running swarm session",
        Long:  "Attaches your terminal to the running oro tmux session.\nRequires the swarm to be running (use 'oro start' first).",
        RunE: func(cmd *cobra.Command, args []string) error {
            // 1. Resolve paths (ResolvePaths)
            // 2. Check daemon status (DaemonStatus)
            // 3. Check TTY: isatty.IsTerminal(os.Stdin.Fd())
            //    — must be Stdin, not Stdout. AttachInteractive needs terminal input.
            //    — checking Stdout would break valid usage like `oro attach 2>&1 | tee`
            // 4. Check tmux session exists (TmuxSession.Exists)
            // 5. Check health (TmuxSession.isHealthy)
            // 6. Attach (TmuxSession.AttachInteractive)
        },
    }
    return cmd
}
```

### Wire into root command

Two files must be edited:

1. **`cmd/oro/root.go`**: Add `newAttachCmd()` to the `cmd.AddCommand(...)` list in `newRootCmd()`
2. **`cmd/oro/cmd_help.go`**: Add `attach     Connect to a running swarm session` line after `start` in the hardcoded `helpText` const (this is NOT auto-generated from registered commands)

### CLI help placement

```
Lifecycle:
  init       Bootstrap dependencies and generate config
  setup      User-friendly project setup
  start      Launch the swarm (tmux + dispatcher + workers)
  attach     Connect to a running swarm session
  stop       Graceful shutdown
  cleanup    Clean all stale state after a crash
```

### Key design points

1. **Reuse existing code**: `DaemonStatus()`, `TmuxSession.Exists()`, `isHealthy()`, `AttachInteractive()` — all exist.
2. **No new dependencies**: pure composition of existing building blocks.
3. **Forward-compatible**: uses `TmuxSession.Name` field, not hardcoded `"oro"`. When multi-project lands, the session name can be parameterized.
4. **Export `isHealthy`**: currently unexported method on `TmuxSession`. May need to export or keep `attach` in the same package (it's all `package main` in `cmd/oro/`, so unexported is fine).

### Test plan

- `TestAttachNoSession`: daemon stopped → returns error with "use `oro start`"
- `TestAttachStale`: stale PID → returns error with "oro cleanup"
- `TestAttachDaemonOnly`: daemon running, no tmux → returns error with "daemon-only mode"
- `TestAttachUnhealthy`: tmux exists but unhealthy → returns error with "oro stop && oro start"
- `TestAttachNoTTY`: tmux exists + healthy but no TTY → returns error
- `TestAttachSuccess`: tmux exists + healthy + TTY → calls `AttachInteractive`
- `TestHelpShowsAttach`: `oro help` output contains "attach" in Lifecycle section

## Multi-Project Design Notes (future)

When multi-project support is added:
- Session name becomes `oro-<project>` (derived from `.oro/config.yaml`)
- `~/.oro/` state dir scoped per project: `~/.oro/<project>/`
- `oro attach` gains optional `[project]` argument
- `oro attach` with no args: attach to the only running session, or list choices if multiple
- `ORO_HOME` env var already provides the manual escape hatch for this

## Acceptance Criteria

1. `oro attach` connects to a running healthy tmux session
2. `oro attach` errors clearly when no session / unhealthy / no TTY / daemon-only
3. Error messages include actionable next step (which command to run)
4. `oro help` shows `attach` in the Lifecycle section
5. No behavior change to `oro start`
