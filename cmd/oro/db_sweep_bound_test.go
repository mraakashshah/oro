package main

import (
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
