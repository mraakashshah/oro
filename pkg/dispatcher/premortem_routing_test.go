package dispatcher //nolint:testpackage // white-box: routes via BuildPrompt

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// TestPremortemBeadRoutedToPremortemAgent verifies §10.3:
// a bead with type=premortem and parent_id=<epic> routes to the premortem
// prompt assembler (not the worker prompt), and the assembled prompt is
// non-interactive (no human-in-loop hooks like Quality Gate or Worktree).
func TestPremortemBeadRoutedToPremortemAgent(t *testing.T) {
	ctx := context.Background()

	bead := protocol.Bead{
		ID:          "pm-99",
		Type:        "premortem",
		Title:       "Premortem for epic-42",
		Description: "Identify failure modes for the login epic.",
		Epic:        "epic-42",
	}

	got, err := BuildPrompt(ctx, bead)
	if err != nil {
		t.Fatalf("BuildPrompt(premortem): unexpected error: %v", err)
	}

	premortemSections := []string{
		"## Failure Modes",
		"## Hidden Assumptions",
		"## Dependencies",
		"## Blast Radius",
		"## Verification Plan",
		"## Verdict",
	}
	for _, label := range premortemSections {
		if !strings.Contains(got, label) {
			t.Errorf("premortem prompt missing section %q\nprompt:\n%s", label, got)
		}
	}

	// Routing identity: must include the bead ID and the target epic.
	if !strings.Contains(got, "pm-99") {
		t.Errorf("prompt missing premortem bead ID")
	}
	if !strings.Contains(got, "epic-42") {
		t.Errorf("prompt missing parent epic ID")
	}

	// Worker-prompt distinguishing markers must be absent — proves we did NOT
	// route through AssemblePrompt (the default worker prompt).
	workerMarkers := []string{
		"Quality Gate",
		"## Worktree",
		"## TDD",
		"## Edit Tools",
		"## Coding Rules",
		"## Merge Target",
	}
	for _, m := range workerMarkers {
		if strings.Contains(got, m) {
			t.Errorf("premortem prompt contains worker-prompt marker %q — routing fell through to AssemblePrompt\nprompt:\n%s", m, got)
		}
	}

	// Non-interactive: no human approval / ops review markers.
	for _, marker := range []string{"human approval", "ops review", "human-in-loop", "human-in-the-loop"} {
		if strings.Contains(strings.ToLower(got), marker) {
			t.Errorf("premortem prompt contains interactive marker %q — must be non-interactive", marker)
		}
	}
}
