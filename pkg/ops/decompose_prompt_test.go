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
	if strings.Contains(prompt, "create --parent") {
		t.Error("prompt must not use bd create --parent (creates circular dependency deadlock)")
	}
}

func TestBuildDecomposePromptNoCreateParent(t *testing.T) {
	opts := DecomposeOpts{
		BeadID:   "oro-test",
		QGOutput: "lint failed",
	}
	prompt := BuildDecomposePrompt(opts)
	if strings.Contains(prompt, "create --parent") {
		t.Error("prompt must not use bd create --parent (creates circular dependency deadlock)")
	}
}
