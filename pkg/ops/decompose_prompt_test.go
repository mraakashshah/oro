package ops //nolint:testpackage // internal test needs access to unexported parseDecomposeOutput

import (
	"strings"
	"testing"
)

func TestParseDecomposeOutput(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantVerdict  Verdict
		wantFeedback string
	}{
		{
			name:         "resolved",
			input:        "VERDICT: resolved",
			wantVerdict:  VerdictResolved,
			wantFeedback: "",
		},
		{
			name:         "failed with reason",
			input:        "VERDICT: failed: no AC",
			wantVerdict:  VerdictFailed,
			wantFeedback: "no AC",
		},
		{
			name:         "empty string",
			input:        "",
			wantVerdict:  VerdictFailed,
			wantFeedback: "no verdict in output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVerdict, gotFeedback := parseDecomposeOutput(tc.input)
			if gotVerdict != tc.wantVerdict {
				t.Errorf("verdict: got %q, want %q", gotVerdict, tc.wantVerdict)
			}
			if gotFeedback != tc.wantFeedback {
				t.Errorf("feedback: got %q, want %q", gotFeedback, tc.wantFeedback)
			}
		})
	}
}

func TestBuildDecomposePromptContainsBeadID(t *testing.T) {
	opts := DecomposeOpts{
		BeadID:   "oro-test123",
		QGOutput: "some output",
	}
	prompt := buildDecomposePrompt(opts)
	if !strings.Contains(prompt, "oro-test123") {
		t.Errorf("prompt does not contain BeadID %q", opts.BeadID)
	}
	if !strings.Contains(prompt, "oro task create --title=\"...\" --type=task --parent=oro-test123") {
		t.Error("prompt must use native create --parent for child hierarchy")
	}
}

// TestBuildDecomposePromptTaskTerminology verifies that the decomposition prompt
// uses "oro task" as the primary command for show/create/dep/update operations.
func TestBuildDecomposePromptTaskTerminology(t *testing.T) {
	got := buildDecomposePrompt(DecomposeOpts{
		BeadID:   "oro-decomp-term",
		QGOutput: "lint failed",
	})
	for _, cmd := range []string{"oro task show", "oro task create", "oro task dep add", "oro task update"} {
		if !strings.Contains(got, cmd) {
			t.Errorf("decompose prompt must contain %q as the primary task command; got:\n%s", cmd, got)
		}
	}
}

func TestBuildDecomposePromptNoCreateParent(t *testing.T) {
	opts := DecomposeOpts{
		BeadID:   "oro-test",
		QGOutput: "lint failed",
	}
	prompt := buildDecomposePrompt(opts)
	if strings.Contains(prompt, "backwards dependency") || strings.Contains(prompt, "circular dependency deadlock") {
		t.Error("prompt must not describe native create --parent as dependency-creating")
	}
	if !strings.Contains(prompt, "--parent=oro-test") {
		t.Error("prompt must attach children with native create --parent")
	}
}

func TestDecomposePromptIncludesTier(t *testing.T) {
	t.Run("includes tier when parent has tier", func(t *testing.T) {
		opts := DecomposeOpts{
			BeadID:   "oro-parent",
			QGOutput: "qg failed",
			Tier:     "deep",
		}
		prompt := buildDecomposePrompt(opts)
		if !strings.Contains(prompt, "--tier=deep") {
			t.Errorf("prompt must include --tier=deep when parent tier is set; got:\n%s", prompt)
		}
	})

	t.Run("omits tier when parent has no tier", func(t *testing.T) {
		opts := DecomposeOpts{
			BeadID:   "oro-parent",
			QGOutput: "qg failed",
		}
		prompt := buildDecomposePrompt(opts)
		if strings.Contains(prompt, "--tier=") {
			t.Errorf("prompt must not include --tier= when parent tier is empty; got:\n%s", prompt)
		}
	})
}

func TestDecomposePromptSupportsOversizedReason(t *testing.T) {
	prompt := buildDecomposePrompt(DecomposeOpts{
		BeadID: "oro-big5",
		Reason: "OVERSIZED_BEAD: touches 3 modules — needs decomposition",
	})
	if !strings.Contains(prompt, "OVERSIZED_BEAD") {
		t.Fatalf("prompt must include oversized reason, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "exhausted all worker retry attempts") {
		t.Fatalf("oversized decomposition prompt should not claim retry exhaustion, got:\n%s", prompt)
	}
}

func TestDecomposePromptRequiresGoalSatisfactionGate(t *testing.T) {
	prompt := buildDecomposePrompt(DecomposeOpts{BeadID: "oro-big6"})
	required := []string{
		"Run the current Cmd: acceptance command before creating child tasks",
		"If the command passes, do not create child tasks",
		"oro task close oro-big6",
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q; got:\n%s", want, prompt)
		}
	}
}

func TestDecomposePromptRequiresParentEpicOrChildren(t *testing.T) {
	prompt := buildDecomposePrompt(DecomposeOpts{BeadID: "oro-big6"})
	required := []string{
		"Convert parent to epic",
		"Create 2-4 smaller child tasks",
		"Test:",
		"Cmd:",
		"Assert:",
		"oro task dep add oro-big6 <child-id>",
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q; got:\n%s", want, prompt)
		}
	}
}
