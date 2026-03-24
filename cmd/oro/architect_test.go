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

func TestArchitectBeacon_GstackPatterns(t *testing.T) {
	beacon := ArchitectBeacon()

	t.Run("AskUserQuestion section exists after Research", func(t *testing.T) {
		if !strings.Contains(beacon, "## AskUserQuestion") {
			t.Error("expected ArchitectBeacon() to contain '## AskUserQuestion' section")
		}
		// Must appear after Research section
		researchIdx := strings.Index(beacon, "## Research")
		askIdx := strings.Index(beacon, "## AskUserQuestion")
		if researchIdx == -1 || askIdx == -1 || askIdx <= researchIdx {
			t.Error("expected '## AskUserQuestion' section to appear after '## Research' section")
		}
		// 4-part structure: Reground, Simplify, Recommend (with completeness score), Options (with effort estimates)
		for _, term := range []string{"Reground", "Simplify", "Recommend", "completeness", "Options", "effort"} {
			if !strings.Contains(beacon, term) {
				t.Errorf("expected AskUserQuestion section to contain %q", term)
			}
		}
	})

	t.Run("anti-sycophancy in Anti-patterns section", func(t *testing.T) {
		antiPatternsIdx := strings.Index(beacon, "## Anti-patterns")
		if antiPatternsIdx == -1 {
			t.Fatal("Anti-patterns section not found")
		}
		antiPatternsSection := beacon[antiPatternsIdx:]
		lower := strings.ToLower(antiPatternsSection)
		if !strings.Contains(lower, "sycophancy") && !strings.Contains(lower, "hedging") {
			t.Error("expected Anti-patterns section to contain anti-sycophancy guidance (mention 'sycophancy' or 'hedging')")
		}
		// Must include banned phrases or replacements
		if !strings.Contains(antiPatternsSection, "verification") && !strings.Contains(antiPatternsSection, "verify") {
			t.Error("expected anti-sycophancy guidance to mention verification (to prevent false decisiveness)")
		}
	})

	t.Run("Engineering Cognitive Patterns section exists after Core Skills", func(t *testing.T) {
		coreSkillsIdx := strings.Index(beacon, "## Core Skills")
		cogIdx := strings.Index(beacon, "## Engineering Cognitive Patterns")
		if cogIdx == -1 {
			t.Fatal("expected ArchitectBeacon() to contain '## Engineering Cognitive Patterns' section")
		}
		if coreSkillsIdx == -1 || cogIdx <= coreSkillsIdx {
			t.Error("expected '## Engineering Cognitive Patterns' to appear after '## Core Skills'")
		}
		// Actionable criteria (not abstract names)
		for _, term := range []string{"proven", "blast radius"} {
			if !strings.Contains(beacon, term) {
				t.Errorf("expected Engineering Cognitive Patterns to contain actionable criterion %q", term)
			}
		}
	})

	t.Run("pushback patterns in Core Skills with BAD/GOOD examples", func(t *testing.T) {
		coreSkillsIdx := strings.Index(beacon, "## Core Skills")
		if coreSkillsIdx == -1 {
			t.Fatal("Core Skills section not found")
		}
		// Find the end of Core Skills section (next ## heading)
		nextSection := strings.Index(beacon[coreSkillsIdx+len("## Core Skills"):], "\n## ")
		var coreSkillsSection string
		if nextSection == -1 {
			coreSkillsSection = beacon[coreSkillsIdx:]
		} else {
			coreSkillsSection = beacon[coreSkillsIdx : coreSkillsIdx+len("## Core Skills")+nextSection]
		}
		if !strings.Contains(coreSkillsSection, "BAD") || !strings.Contains(coreSkillsSection, "GOOD") {
			t.Error("expected Core Skills section to contain BAD/GOOD example pairs for pushback patterns")
		}
		if !strings.Contains(coreSkillsSection, "vague") {
			t.Error("expected Core Skills pushback patterns to mention vague requirements")
		}
		// Qualifier about when to push back vs proceed
		if !strings.Contains(coreSkillsSection, "precise") && !strings.Contains(coreSkillsSection, "AC") {
			t.Error("expected Core Skills to contain qualifier about when to push back (vague) vs proceed (precise/AC)")
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
