package main

import (
	"strings"
	"testing"
)

func TestManagerBeacon(t *testing.T) {
	beacon := ManagerBeacon()

	t.Run("returns non-empty string", func(t *testing.T) {
		if beacon == "" {
			t.Fatal("expected ManagerBeacon() to return a non-empty string")
		}
	})

	t.Run("contains all 12 section headers", func(t *testing.T) {
		sections := []string{
			"## Role",
			"## System Map",
			"## Startup",
			"## Oro CLI",
			"## Beads CLI",
			"## Decomposition",
			"## Scale Policy",
			"## Escalations",
			"## Human Interaction",
			"## Dispatcher Messages",
			"## Anti-patterns",
			"## Shutdown",
		}
		for _, section := range sections {
			if !strings.Contains(beacon, section) {
				t.Errorf("expected beacon to contain section header %q", section)
			}
		}
	})

	t.Run("contains key terms", func(t *testing.T) {
		keyTerms := []string{
			"oro start",
			"oro directive scale",
			"bd ready",
			"[ORO-DISPATCH]",
			"quality gate",
		}
		lower := strings.ToLower(beacon)
		for _, term := range keyTerms {
			if !strings.Contains(lower, strings.ToLower(term)) {
				t.Errorf("expected beacon to contain key term %q", term)
			}
		}
	})

	t.Run("is substantial in size", func(t *testing.T) {
		if len(beacon) < 2000 {
			t.Errorf("expected beacon to be substantial (>2000 chars for 12 sections), got %d chars", len(beacon))
		}
	})

	t.Run("role section establishes coordinator identity", func(t *testing.T) {
		if !strings.Contains(beacon, "You are the oro manager") {
			t.Error("expected Role section to contain 'You are the oro manager'")
		}
		if !strings.Contains(beacon, "do not write code") {
			t.Error("expected Role section to state manager does not write code")
		}
	})

	t.Run("startup section includes initialization steps", func(t *testing.T) {
		startupTerms := []string{"bd stats", "bd ready", "bd blocked", "oro directive status", "oro directive scale"}
		for _, term := range startupTerms {
			if !strings.Contains(beacon, term) {
				t.Errorf("expected Startup section to contain %q", term)
			}
		}
	})

	t.Run("startup section uses directive status not oro start", func(t *testing.T) {
		// Extract just the Startup section to verify step 5 does NOT tell the
		// manager to run "oro start" (the dispatcher is already running by the
		// time the manager receives the beacon).
		startIdx := strings.Index(beacon, "## Startup")
		if startIdx == -1 {
			t.Fatal("could not find ## Startup section")
		}
		// Find the next section header after Startup
		rest := beacon[startIdx+len("## Startup"):]
		nextSection := strings.Index(rest, "\n## ")
		var startupSection string
		if nextSection == -1 {
			startupSection = rest
		} else {
			startupSection = rest[:nextSection]
		}

		if strings.Contains(startupSection, "oro start") {
			t.Error("Startup section should NOT contain 'oro start' — the dispatcher is already running; use 'oro directive status' instead")
		}
		if !strings.Contains(startupSection, "oro directive status") {
			t.Error("Startup section should contain 'oro directive status' to confirm the dispatcher is running")
		}
	})

	t.Run("escalations section covers failure playbook", func(t *testing.T) {
		escalationTypes := []string{"MERGE_CONFLICT", "STUCK_WORKER", "PRIORITY_CONTENTION", "WORKER_CRASH"}
		for _, etype := range escalationTypes {
			if !strings.Contains(beacon, etype) {
				t.Errorf("expected Escalations section to contain %q", etype)
			}
		}
	})

	t.Run("dispatcher messages section describes ORO-DISPATCH protocol", func(t *testing.T) {
		if !strings.Contains(beacon, "[ORO-DISPATCH]") {
			t.Error("expected Dispatcher Messages section to contain '[ORO-DISPATCH]'")
		}
		dispatchTypes := []string{"MERGE_CONFLICT", "STUCK", "PRIORITY_CONTENTION", "STATUS"}
		for _, dtype := range dispatchTypes {
			if !strings.Contains(beacon, dtype) {
				t.Errorf("expected Dispatcher Messages section to contain message type %q", dtype)
			}
		}
	})

	t.Run("CLI section notes oro start is human-only", func(t *testing.T) {
		// The "oro start" entry in the CLI section should clarify that it is
		// used by the human to launch the swarm, not by the manager.
		cliIdx := strings.Index(beacon, "## Oro CLI")
		if cliIdx == -1 {
			t.Fatal("could not find ## Oro CLI section")
		}
		rest := beacon[cliIdx:]
		nextSection := strings.Index(rest[len("## Oro CLI"):], "\n## ")
		var cliSection string
		if nextSection == -1 {
			cliSection = rest
		} else {
			cliSection = rest[:len("## Oro CLI")+nextSection]
		}

		if !strings.Contains(strings.ToLower(cliSection), "human") {
			t.Error("expected CLI section's 'oro start' entry to note it is used by the human")
		}
	})

	t.Run("CLI commands use oro directive prefix for runtime operations", func(t *testing.T) {
		// These operations are subcommands of "oro directive", not top-level.
		// The beacon must instruct the manager to use the correct CLI form.
		directiveOps := []string{
			"oro directive scale",
			"oro directive pause",
			"oro directive resume",
			"oro directive focus",
			"oro directive status",
		}
		for _, op := range directiveOps {
			if !strings.Contains(beacon, op) {
				t.Errorf("expected beacon to contain %q (not bare form without 'directive')", op)
			}
		}

		// "oro start" and "oro stop" are legitimate top-level commands,
		// so they should NOT require the "directive" prefix.
		topLevel := []string{"oro start", "oro stop"}
		for _, cmd := range topLevel {
			if !strings.Contains(beacon, cmd) {
				t.Errorf("expected beacon to contain top-level command %q", cmd)
			}
		}
	})

	t.Run("shutdown section includes ordered steps", func(t *testing.T) {
		shutdownTerms := []string{"oro directive scale 0", "oro stop", "bd sync"}
		for _, term := range shutdownTerms {
			if !strings.Contains(beacon, term) {
				t.Errorf("expected Shutdown section to contain %q", term)
			}
		}
	})
}
