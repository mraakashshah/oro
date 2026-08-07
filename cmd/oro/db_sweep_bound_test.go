package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStartupDevCacheSweepIsBounded pins the boot-path sweep budget. The sweep
// runs before the dispatcher opens its socket, so an unbounded sweep blocks
// startup: on 2026-07-29 a 52 GB Go build cache made `go clean -cache` outlast
// oro start's socket-readiness wait and the launch reported failure. The
// budget must stay well inside that wait so a large cache degrades into a
// partial sweep — which the size trigger simply resumes next start — rather
// than into a failed boot.
func TestStartupDevCacheSweepIsBounded(t *testing.T) {
	if startupDevCacheSweepBudget <= 0 {
		t.Fatal("startupDevCacheSweepBudget must be positive so the boot-path sweep cannot run unbounded")
	}
	if startupDevCacheSweepBudget > 30*time.Second {
		t.Errorf("startupDevCacheSweepBudget = %s, want <= 30s so cache maintenance never outlasts the socket-readiness wait", startupDevCacheSweepBudget)
	}
}

func TestStartupReadinessCoversDevCacheSweep(t *testing.T) {
	t.Run("budget includes a bounded post-sweep margin", func(t *testing.T) {
		margin := socketPollTimeout - startupDevCacheSweepBudget
		if margin <= 0 {
			t.Fatalf("socketPollTimeout = %s, want more than startupDevCacheSweepBudget = %s", socketPollTimeout, startupDevCacheSweepBudget)
		}
		if margin > 10*time.Second {
			t.Fatalf("startup readiness margin = %s, want at most 10s", margin)
		}
	})

	t.Run("live daemon past old boundary is not killed", func(t *testing.T) {
		const readinessTimeScale = 100
		oldBoundary := 15 * time.Second / readinessTimeScale
		socketDelay := (startupDevCacheSweepBudget - time.Second) / readinessTimeScale
		readinessBudget := socketPollTimeout / readinessTimeScale
		if socketDelay <= oldBoundary {
			t.Fatalf("test socket delay = %s, want beyond old boundary = %s", socketDelay, oldBoundary)
		}

		tmpDir := t.TempDir()
		sockPath := fmt.Sprintf("/tmp/oro-readiness-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

		socketDone := make(chan error, 1)
		spawner := &fakeSpawner{returnPID: 91943, socketPath: sockPath, socketDelay: socketDelay, socketDone: socketDone}
		fakeTmux := newFakeCmd()
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		killed := false
		err := runFullStart(&bytes.Buffer{}, 1, 1, "sonnet", "", spawner, fakeTmux, func(int) error {
			killed = true
			return nil
		}, readinessBudget, noopSleep, 50*time.Millisecond, true)
		if err != nil {
			t.Fatalf("runFullStart with delayed live socket: %v", err)
		}
		if killed {
			t.Fatal("runFullStart killed a live daemon whose socket appeared within the readiness budget")
		}
		if socketErr := <-socketDone; socketErr != nil {
			t.Fatalf("serve delayed dispatcher socket: %v", socketErr)
		}
	})

	t.Run("never-ready daemon is killed once at expanded deadline", func(t *testing.T) {
		const readinessTimeScale = 100
		readinessBudget := socketPollTimeout / readinessTimeScale
		oldBoundary := 15 * time.Second / readinessTimeScale
		if readinessBudget <= oldBoundary {
			t.Fatalf("scaled readiness budget = %s, want beyond old boundary = %s", readinessBudget, oldBoundary)
		}

		tmpDir := t.TempDir()
		t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "never-ready.sock"))
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

		kills := 0
		err := runFullStart(&bytes.Buffer{}, 1, 1, "sonnet", "", &fakeSpawner{returnPID: 91943}, newFakeCmd(), func(int) error {
			kills++
			return nil
		}, readinessBudget, noopSleep, 50*time.Millisecond, true)
		if err == nil {
			t.Fatal("runFullStart succeeded without a dispatcher socket")
		}
		if kills != 1 {
			t.Fatalf("runFullStart kill calls = %d, want exactly 1", kills)
		}
	})
}
