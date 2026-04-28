package ops //nolint:testpackage // internal test needs access to unexported buildEpicFixPrompt

import (
	"strings"
	"testing"
)

func TestBuildEpicFixPromptUsesOroBeadCreateWithAcceptance(t *testing.T) {
	prompt := buildEpicFixPrompt(EpicFixOpts{
		EpicID: "oro-epic",
		AC:     "Test: pkg/foo_test.go:TestFoo | Cmd: go test ./pkg/foo -run TestFoo | Assert: PASS",
		Cmd:    "go test ./pkg/foo -run TestFoo",
		Output: "FAIL",
	})

	if !strings.Contains(prompt, "oro bead create") {
		t.Fatalf("prompt missing oro bead create command:\n%s", prompt)
	}
	if !strings.Contains(prompt, "--acceptance=\"Test: <file>:<Fn> | Cmd: <cmd> | Assert: <expected>\"") {
		t.Fatalf("prompt create command must include machine-verifiable acceptance criteria:\n%s", prompt)
	}
	legacyCreate := string([]byte{98, 100}) + " create"
	if strings.Contains(prompt, legacyCreate) {
		t.Fatalf("prompt must not contain legacy bead create command:\n%s", prompt)
	}
}
