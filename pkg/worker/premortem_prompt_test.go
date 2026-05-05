package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/worker"
)

// TestAssemblePremortemPromptHasSixSections verifies §11.4 / §10.3:
// the rendered premortem prompt contains exactly six labeled output
// sections in the order specified, and includes the target bead identity.
func TestAssemblePremortemPromptHasSixSections(t *testing.T) {
	params := worker.PremortemPromptParams{
		BeadID:            "pm-1",
		TargetBeadID:      "epic-99",
		TargetTitle:       "Build login flow",
		TargetDescription: "Design and implement OAuth2 + email/password sign-in.",
	}
	got := worker.AssemblePremortemPrompt(params)

	expected := []string{
		"## Failure Modes",
		"## Hidden Assumptions",
		"## Dependencies",
		"## Blast Radius",
		"## Verification Plan",
		"## Verdict",
	}

	for _, label := range expected {
		count := strings.Count(got, label)
		if count != 1 {
			t.Errorf("section label %q: want exactly 1 occurrence, got %d\nprompt:\n%s", label, count, got)
		}
	}

	// Total ## section markers must be exactly six (the output template labels).
	// Structural sections of the prompt itself use a single # to avoid colliding
	// with the agent's output template.
	totalSections := strings.Count(got, "\n## ")
	if strings.HasPrefix(got, "## ") {
		totalSections++
	}
	if totalSections != 6 {
		t.Errorf("total ## section headers: want 6, got %d\nprompt:\n%s", totalSections, got)
	}

	lastIdx := -1
	for _, label := range expected {
		idx := strings.Index(got, label)
		if idx == -1 {
			t.Errorf("label %q not found", label)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("label %q (idx=%d) out of order; previous label ended at idx=%d", label, idx, lastIdx)
		}
		lastIdx = idx
	}

	if !strings.Contains(got, "epic-99") {
		t.Errorf("prompt missing target bead ID %q\nprompt:\n%s", "epic-99", got)
	}
	if !strings.Contains(got, "Build login flow") {
		t.Errorf("prompt missing target title\nprompt:\n%s", got)
	}
	if !strings.Contains(got, "OAuth2") {
		t.Errorf("prompt missing target description\nprompt:\n%s", got)
	}

	for _, verdict := range []string{"proceed", "block", "replan"} {
		if !strings.Contains(got, verdict) {
			t.Errorf("Verdict section must enumerate %q as a valid value", verdict)
		}
	}
}
