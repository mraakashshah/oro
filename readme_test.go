package main

import (
	"os"
	"strings"
	"testing"
)

func TestREADMEContainsReferencesSection(t *testing.T) {
	content, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("Failed to read README.md: %v", err)
	}

	readmeText := string(content)

	// Check for References section header
	if !strings.Contains(readmeText, "## References") {
		t.Error("README.md missing ## References section")
	}

	// Check for required links
	requiredLinks := map[string]string{
		"Continuous Claude v3":         "github.com/parcadei/Continuous-Claude-v3",
		"Steve Yegge - Beads":          "github.com/steveyegge/beads",
		"Steve Yegge - Gastown":        "github.com/steveyegge/gastown",
		"Obra - Superpowers":           "github.com/obra/superpowers",
		"Teresa Torres - Context Rot":  "https://www.producttalk.org/context-rot/",
		"Every's Compound Engineering": "every.to/guides/compound-engineering",
	}

	for name, expectedURL := range requiredLinks {
		if !strings.Contains(readmeText, name) {
			t.Errorf("README.md missing reference to %s", name)
		}
		if !strings.Contains(readmeText, expectedURL) {
			t.Errorf("README.md missing URL for %s (expected to contain: %s)", name, expectedURL)
		}
	}
}
