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
			"oro scale",
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
		startupTerms := []string{"bd stats", "bd ready", "bd blocked", "oro start", "oro scale"}
		for _, term := range startupTerms {
			if !strings.Contains(beacon, term) {
				t.Errorf("expected Startup section to contain %q", term)
			}
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

	t.Run("shutdown section includes ordered steps", func(t *testing.T) {
		shutdownTerms := []string{"oro scale 0", "oro stop", "bd sync"}
		for _, term := range shutdownTerms {
			if !strings.Contains(beacon, term) {
				t.Errorf("expected Shutdown section to contain %q", term)
			}
		}
	})
}
