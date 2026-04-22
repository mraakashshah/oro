# oro dolt setup — launchd race condition fix

**Date:** 2026-04-22
**Status:** Design
**Owner:** as21
**Supersedes:** partial refinement of D6.3 in `2026-04-20-oro-dolt-shared-lifecycle-coordination-design.md`

---

## Problem

`runDoltSetup` (`cmd/oro/cmd_dolt.go:228`) creates a race condition between two
dolt startup paths:

1. `installDoltPlist` → `launchctl bootstrap` → launchd starts dolt immediately
   (plist has `RunAtLoad=true` AND `KeepAlive=true`)
2. `cfg.startFn` → `startSharedDoltServer` → also tries to start dolt

Race outcome:
- If launchd wins: `startSharedDoltServer` adopts the running process (correct
  since D0 fixed the kickstart label). PID/port files written. ✓
- If `startSharedDoltServer` wins: PID/port files written by oro. Launchd's dolt
  starts later, fails to bind port 13307, exits code 1. `KeepAlive=true` causes
  launchd to retry in a throttle loop — forever failing, never winning. ✗

The losing outcome leaves the shared dolt as an **oro-owned orphan**, not managed
by launchd. On crash, launchd's retry is always blocked by the orphan — until the
orphan dies, at which point launchd finally succeeds. The result: launchd
`exit code 1` in `launchctl list`, dolt running but not supervised.

**Root cause**: D6.3 in the prior design doc said "keep `startFn` as the ONLY
legal direct spawn in setup." It did not account for RunAtLoad+KeepAlive together
making launchd also spawn on bootstrap. D0 (kickstart label fix) has since landed,
making launchd reliable — the fallback direct-spawn is now unnecessary and harmful.

---

## Goal

Give launchd exclusive ownership of the dolt process started by `oro dolt setup`.
No direct-spawn race. `launchctl list` shows the dolt PID managed by launchd, not
an orphan.

---

## Design

### Single decision: kill-before-plist, launchd-owns-startup

Replace the `installDoltPlist → startFn` sequence with:

```
1. Drain port 13307: kill any running server so launchd gets clean port access
2. installDoltPlist → bootstrap → launchd starts dolt (gets port cleanly, no race)
3. Wait for launchd's dolt (up to 8s, inject-able for tests)
4. Discover PID via discoverPIDByPort, write dolt-server.pid and dolt-server.port
5. No startFn call
```

`startFn` field stays in `doltSetupConfig` for test backward compat but is not
called in the production path when `installPlistFn` is non-nil.

**Why 8s timeout**: launchd typically starts processes within 200-500ms on
macOS 14. 8s is 16× that, accommodating cold-start disk pressure. On timeout,
return a hard error (not a warning) directing the user to `oro dolt repair`.

**Premortem (risks accepted):**
- *Tiger*: `waitForPort` timeout in tests where no real dolt runs — mitigated by
  injecting `waitForPortFn func(int, time.Duration) bool` into `doltSetupConfig`.
- *Tiger*: `discoverPIDByPort` fails (lsof absent on minimal macOS) — write no
  PID file; `readSharedServerState` already falls back to `isPortUp` (fix from
  dfbe3d3e). Dolt runs fine; `oro dolt status` shows correct state.
- *Elephant*: killing existing shared server during re-setup disrupts active `bd`
  commands — acceptable; setup already requires dispatcher to be stopped
  (`dispatcherPIDFn` guard). Active beads-related bd calls should not be present.
- *Paper tiger*: KeepAlive+RunAtLoad removed race by pure luck sometimes —
  removing startFn eliminates the non-determinism entirely.

---

## Files changed

| File | Change |
|------|--------|
| `cmd/oro/cmd_dolt.go` | Add `waitForPortFn`, `discoverPIDFn` to `doltSetupConfig`. Refactor `runDoltSetup`: drain port before plist install, wait+adopt after, remove startFn call. |
| `cmd/oro/cmd_dolt_test.go` | Update `TestDoltSetup` happy-path mock to inject `waitForPortFn`/`discoverPIDFn`. Add `TestDoltSetup_LaunchdOwnsStartup` asserting `startFn` is NOT called and PID file written from discover. Add `TestDoltSetup_WaitTimeout` asserting hard error on timeout. |

No other files change. `dolt.go`, `launchd.go`, `identity_probe.go` are untouched.

---

## New `doltSetupConfig` fields

```go
// waitForPortFn polls until port is accepting connections or timeout elapses.
// Defaults to waitForPort. Injected in tests to avoid real network waits.
waitForPortFn func(port int, timeout time.Duration) bool

// discoverPIDFn finds the PID of the process listening on port.
// Defaults to discoverPIDByPort. Injected in tests.
discoverPIDFn func(port int) (int, error)

// killSharedFn kills any running process on SharedDoltPort before plist install.
// Defaults to inline drain logic. Injected in tests.
killSharedFn func(oroHome string)
```

---

## Revised `runDoltSetup` skeleton

```go
func runDoltSetup(cfg *doltSetupConfig, w io.Writer) error {
    // ... dispatcher check, findDoltProjects, checkDBCollisions, migrateProjects ...

    // Step 1: drain SharedDoltPort so launchd gets clean port access.
    if cfg.killSharedFn != nil {
        cfg.killSharedFn(cfg.oroHome)
    } else {
        drainSharedDoltServer(cfg.oroHome, w)
    }

    // Step 2: kill per-project orphans (unchanged).
    killOrphanDoltServers(projects, w)

    // Step 3: install plist — launchd bootstraps and starts dolt (RunAtLoad).
    if err := installDoltPlist(cfg, w); err != nil {
        return err
    }

    // Step 4: wait for launchd's dolt to bind the port.
    waitFn := cfg.waitForPortFn
    if waitFn == nil {
        waitFn = waitForPort
    }
    if !waitFn(SharedDoltPort, 8*time.Second) {
        return fmt.Errorf(
            "shared dolt server did not start within 8s after launchd bootstrap: "+
                "run 'oro dolt repair' if this persists",
        )
    }

    // Step 5: discover PID and write state files (best-effort; status cmd
    // falls back to isPortUp if PID file is absent).
    discoverFn := cfg.discoverPIDFn
    if discoverFn == nil {
        discoverFn = discoverPIDByPort
    }
    if pid, err := discoverFn(SharedDoltPort); err == nil && pid > 0 {
        pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
        portPath := filepath.Join(cfg.oroHome, "dolt-server.port")
        _ = os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600)
        _ = os.WriteFile(portPath, []byte(strconv.Itoa(SharedDoltPort)), 0o600)
    }

    clearPortRegistry(cfg.oroHome)
    fmt.Fprintln(w, "dolt setup complete")
    return nil
}
```

---

## `drainSharedDoltServer` helper

```go
// drainSharedDoltServer kills any process currently listening on SharedDoltPort
// so that the launchd agent can bind the port cleanly after plist install.
// Best-effort: logs a warning but does not fail if kill is unsuccessful.
func drainSharedDoltServer(oroHome string, w io.Writer) {
    if !isDoltServerRunning(SharedDoltPort) {
        return
    }
    pid, err := discoverPIDByPort(SharedDoltPort)
    if err != nil || pid <= 0 {
        fmt.Fprintf(w, "warning: server running on port %d but PID not discoverable; "+
            "launchd may race on startup\n", SharedDoltPort)
        return
    }
    if killErr := killAndWait(pid, oroHome); killErr != nil {
        fmt.Fprintf(w, "warning: failed to stop existing server (PID %d): %v\n", pid, killErr)
    }
}
```

---

## Test cases

| Test | What it asserts |
|------|----------------|
| `TestDoltSetup_LaunchdOwnsStartup` | `startFn` is NOT called; `waitForPortFn` is called once with `(13307, 8s)`; PID file written with value from `discoverPIDFn` |
| `TestDoltSetup_WaitTimeout` | When `waitForPortFn` returns false, `runDoltSetup` returns error containing "8s" and "oro dolt repair" |
| `TestDoltSetup_DrainKillsExisting` | When `killSharedFn` is injected, it is called before `installPlistFn` |
| `TestDoltSetup_DiscoverPIDFailsGracefully` | When `discoverPIDFn` returns error, setup succeeds (no PID file written, no error returned) |
| Existing `TestDoltSetup` happy path | Update mock: remove `startFn` write, add `waitForPortFn: func(...) bool { return true }`, `discoverPIDFn: func(...) (int, error) { return 42, nil }` |

---

## Out of scope

- Changing `KeepAlive`/`RunAtLoad` plist values (Option C was rejected above)
- Wiring `drainSharedDoltServer` into any path other than `runDoltSetup`
- Changing `startSharedDoltServer` (stays for `oro dolt repair` callers)
- `oro dolt start` command behavior (separate bead if needed)
