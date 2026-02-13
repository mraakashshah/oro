# Detach Mode for `oro start`

**Date:** 2026-02-13
**Status:** Draft

## Problem

`oro start` fails with exit code 1 when run without a TTY (e.g., from Claude Code's
Bash tool, CI, scripts). The dispatcher daemon and tmux session create successfully,
but `AttachInteractive()` requires a real terminal. The error is misleading — everything
important already succeeded.

## Design

### Behavior Change

`runFullStart` auto-detects whether stdin is a terminal:

- **TTY present + no `--detach`:** Current behavior (create tmux, attach interactively)
- **No TTY OR `--detach` flag set:** Create tmux session, print status, exit 0

### Implementation

**File:** `cmd/oro/cmd_start.go`

1. Add `--detach` / `-D` boolean flag to the start command
2. Add `isDetached(detachFlag bool) bool` helper:
   - Returns `true` if `detachFlag` is set OR `!isatty(os.Stdin.Fd())`
3. Pass `detach bool` to `runFullStart`
4. In `runFullStart`, after step 4 (print status):
   - If `detach`: print attach instructions, return nil
   - If `!detach`: call `AttachInteractive()` as before

### TTY Detection

Use `github.com/mattn/go-isatty` (already an indirect dependency via go.mod):

```go
import "github.com/mattn/go-isatty"

func isDetached(flag bool) bool {
    return flag || !isatty.IsTerminal(os.Stdin.Fd())
}
```

### Output in Detach Mode

```
oro swarm started (PID 42463, workers=2, model=sonnet)
detached — attach with: tmux attach -t oro
```

### Flag

```
--detach, -D    Start in detached mode (don't attach to tmux session)
```

### Test Plan

1. `TestIsDetached_FlagOverride` — flag=true always returns true regardless of TTY
2. `TestIsDetached_NoFlag_NonTTY` — flag=false with non-TTY fd returns true
3. `TestRunFullStart_Detached_SkipsAttach` — detach=true: no AttachInteractive call, exits clean
4. `TestRunFullStart_Detached_PrintsInstructions` — output contains "tmux attach -t oro"
5. Update `TestStartSendsDirective` and `TestRunFullStartAttachesSession` — these currently
   assert attach errors; with detach=true they should succeed cleanly

### Risks

| Risk | Mitigation |
|------|------------|
| isatty false positive in exotic terminals | `--no-detach` flag could force attach (YAGNI for now) |
| User confusion about blocking change | Clear "detached" message with attach command |

### Changes

- `cmd/oro/cmd_start.go` — add flag, TTY check, conditional attach
- `cmd/oro/start_test.go` — new tests + update existing
- No changes to `tmux.go`, dispatcher, or daemon subprocess
