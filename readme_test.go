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

func TestNoBdInstallInstructions(t *testing.T) {
	docs := []string{
		"docs/INSTALL.md",
		"README.md",
		"docs/dev-setup.md",
	}

	for _, path := range docs {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Failed to read %s: %v", path, err)
		}

		text := string(content)
		forbidden := []string{
			"bd CLI",
			"`bd ",
			"bd version",
			"brew install beads",
			"Beads issue tracker",
			"pinned bd",
		}
		for _, phrase := range forbidden {
			if strings.Contains(text, phrase) {
				t.Errorf("%s contains operator-facing bd instruction/reference %q", path, phrase)
			}
		}

		if path != "docs/dev-setup.md" && !strings.Contains(text, "oro task") {
			t.Errorf("%s must reference native oro task operator workflows", path)
		}
	}
}
