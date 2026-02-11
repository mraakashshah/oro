package main

import (
	"strings"
	"testing"
)

func TestArchitectBeacon_NonEmpty(t *testing.T) {
	beacon := ArchitectBeacon()
	if beacon == "" {
		t.Fatal("expected ArchitectBeacon() to return non-empty string")
	}
	if len(beacon) < 500 {
		t.Errorf("expected ArchitectBeacon() to be substantial (>500 chars), got %d chars", len(beacon))
	}
}

func TestArchitectBeacon_AllNineSections(t *testing.T) {
	beacon := ArchitectBeacon()

	sections := []string{
		"## Role",
		"## System Map",
		"## Core Skills",
		"## Output Contract",
		"## Bead Craft",
		"## Strategic Decomposition",
		"## Research",
		"## Beads CLI",
		"## Anti-patterns",
	}

	for _, section := range sections {
		t.Run(section, func(t *testing.T) {
			if !strings.Contains(beacon, section) {
				t.Errorf("expected ArchitectBeacon() to contain section header %q", section)
			}
		})
	}
}

func TestArchitectBeacon_KeyTerms(t *testing.T) {
	beacon := ArchitectBeacon()

	terms := []struct {
		term   string
		reason string
	}{
		{"bd create", "architect creates beads"},
		{"bd show", "architect inspects beads"},
		{"bd dep add", "architect maps dependencies"},
		{"acceptance criteria", "beads must have acceptance criteria"},
		{"worktree", "system map references worktrees"},
		{"You do not write code", "core role constraint"},
	}

	for _, tt := range terms {
		t.Run(tt.term, func(t *testing.T) {
			if !strings.Contains(beacon, tt.term) {
				t.Errorf("expected ArchitectBeacon() to contain %q (%s)", tt.term, tt.reason)
			}
		})
	}
}

func TestArchitectNudge(t *testing.T) {
	nudge := ArchitectNudge()

	t.Run("returns non-empty string", func(t *testing.T) {
		if nudge == "" {
			t.Fatal("expected ArchitectNudge() to return non-empty string")
		}
	})

	t.Run("is short (under 500 chars)", func(t *testing.T) {
		if len(nudge) > 500 {
			t.Errorf("expected ArchitectNudge() to be short (<500 chars), got %d chars", len(nudge))
		}
	})

	t.Run("identifies the role", func(t *testing.T) {
		if !strings.Contains(nudge, "architect") {
			t.Error("expected ArchitectNudge() to mention 'architect'")
		}
	})

	t.Run("mentions SessionStart hook", func(t *testing.T) {
		if !strings.Contains(nudge, "SessionStart") {
			t.Error("expected ArchitectNudge() to mention 'SessionStart' hook")
		}
	})

	t.Run("suggests orientation commands", func(t *testing.T) {
		if !strings.Contains(nudge, "bd stats") {
			t.Error("expected ArchitectNudge() to suggest 'bd stats'")
		}
		if !strings.Contains(nudge, "bd ready") {
			t.Error("expected ArchitectNudge() to suggest 'bd ready'")
		}
	})

	t.Run("is much shorter than full beacon", func(t *testing.T) {
		beacon := ArchitectBeacon()
		if len(nudge) >= len(beacon)/2 {
			t.Errorf("nudge (%d chars) should be much shorter than beacon (%d chars)", len(nudge), len(beacon))
		}
	})
}

func TestArchitectBeacon_ArchitectConstraints(t *testing.T) {
	beacon := ArchitectBeacon()

	t.Run("no code writing", func(t *testing.T) {
		lower := strings.ToLower(beacon)
		hasNoCode := strings.Contains(lower, "no code writing") ||
			strings.Contains(lower, "do not write code") ||
			strings.Contains(lower, "you do not write code") ||
			strings.Contains(lower, "never write code")
		if !hasNoCode {
			t.Error("expected ArchitectBeacon() to contain a no-code-writing constraint")
		}
	})

	t.Run("no oro CLI usage", func(t *testing.T) {
		lower := strings.ToLower(beacon)
		hasNoOro := strings.Contains(lower, "no using `oro` cli") ||
			strings.Contains(lower, "no oro cli") ||
			strings.Contains(lower, "do not use oro") ||
			strings.Contains(lower, "never use oro") ||
			strings.Contains(lower, "oro` cli commands")
		if !hasNoOro {
			t.Error("expected ArchitectBeacon() to contain a no-oro-CLI constraint")
		}
	})
}
