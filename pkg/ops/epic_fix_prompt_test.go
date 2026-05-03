package ops //nolint:testpackage // internal test needs access to unexported buildEpicFixPrompt

import (
	"strings"
	"testing"
)

// TestBuildEpicFixPromptTaskTerminology verifies that the epic-fix prompt uses
// "oro task" as the primary command for create/update/dep-add operations.
func TestBuildEpicFixPromptTaskTerminology(t *testing.T) {
	got := buildEpicFixPrompt(EpicFixOpts{
		EpicID: "oro-fix-term",
		AC:     "Test: pkg/foo_test.go:TestFoo | Cmd: go test | Assert: PASS",
		Cmd:    "go test",
		Output: "FAIL",
	})
	for _, cmd := range []string{"oro task create", "oro task update", "oro task dep add"} {
		if !strings.Contains(got, cmd) {
			t.Errorf("epic fix prompt must contain %q as the primary task command; got:\n%s", cmd, got)
		}
	}
}

func TestBuildEpicFixPromptUsesOroBeadCreateWithAcceptance(t *testing.T) {
	prompt := buildEpicFixPrompt(EpicFixOpts{
		EpicID: "oro-epic",
		AC:     "Test: pkg/foo_test.go:TestFoo | Cmd: go test ./pkg/foo -run TestFoo | Assert: PASS",
		Cmd:    "go test ./pkg/foo -run TestFoo",
		Output: "FAIL",
	})

	if !strings.Contains(prompt, "oro task create") {
		t.Fatalf("prompt missing oro task create command:\n%s", prompt)
	}
	if !strings.Contains(prompt, "--acceptance=\"Test: <file>:<Fn> | Cmd: <cmd> | Assert: <expected>\"") {
		t.Fatalf("prompt create command must include machine-verifiable acceptance criteria:\n%s", prompt)
	}
	legacyCreate := string([]byte{98, 100}) + " create"
	if strings.Contains(prompt, legacyCreate) {
		t.Fatalf("prompt must not contain legacy bead create command:\n%s", prompt)
	}
}
