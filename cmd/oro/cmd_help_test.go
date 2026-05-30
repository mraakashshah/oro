package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpOutput(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()

	// Verify category headers are present.
	categories := []string{
		"Lifecycle:",
		"Monitoring:",
		"Knowledge:",
		"Control:",
		"Search:",
	}
	for _, cat := range categories {
		if !strings.Contains(out, cat) {
			t.Errorf("expected category header %q in output, got:\n%s", cat, out)
		}
	}

	// Verify all subcommands are listed (all commands except "help" itself).
	subcommands := []string{
		"init",
		"start",
		"stop",
		"cleanup",
		"status",
		"logs",
		"cards",
		"directive",
		"index",
		"worker",
		"work",
	}
	for _, cmd := range subcommands {
		if !strings.Contains(out, cmd) {
			t.Errorf("expected subcommand %q in output, got:\n%s", cmd, out)
		}
	}

	// Verify the banner line is present.
	if !strings.Contains(out, "Oro") {
		t.Errorf("expected banner containing 'Oro' in output, got:\n%s", out)
	}

	// Verify the footer hint is present.
	if !strings.Contains(out, "oro <command> --help") {
		t.Errorf("expected footer hint in output, got:\n%s", out)
	}
}

func TestHelpDoesNotAdvertiseLegacyMemoryCommands(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "\nMemory:\n") {
		t.Fatalf("help output must not present retired commands under a live Memory group:\n%s", out)
	}
	if strings.Contains(out, "\nRetired:\n") {
		t.Fatalf("help output must not advertise a retired memory command group:\n%s", out)
	}

	knowledgeSection := sectionFromHelp(t, out, "Knowledge:")
	for _, want := range []string{"cards", "models"} {
		if !strings.Contains(knowledgeSection, want) {
			t.Errorf("expected %q in Knowledge section:\n%s", want, knowledgeSection)
		}
	}
	assertHelpOmitsCommandLines(t, out, []string{"remember", "recall", "forget", "memories"})
}

func sectionFromHelp(t *testing.T, helpOutput, heading string) string {
	t.Helper()

	sectionIdx := strings.Index(helpOutput, heading)
	if sectionIdx < 0 {
		t.Fatalf("expected %q section in help output:\n%s", heading, helpOutput)
	}

	section := helpOutput[sectionIdx:]
	if nextSectionIdx := strings.Index(section[len(heading):], "\n\n"); nextSectionIdx >= 0 {
		section = section[:len(heading)+nextSectionIdx]
	}
	return section
}

func TestHelpFallthrough(t *testing.T) {
	// "oro help status" should fall through to cobra's per-command help,
	// which includes the Long description of the status command.
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help", "status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()

	// Cobra's per-command help should contain the status command's Long description.
	if !strings.Contains(out, "Displays dispatcher status") {
		t.Errorf("expected cobra per-command help for 'status', got:\n%s", out)
	}

	// Should NOT contain the categorized help headers (that's the custom help).
	if strings.Contains(out, "Lifecycle:") {
		t.Errorf("expected fallthrough to cobra help, not categorized help, got:\n%s", out)
	}
}

func TestHelpUnknownCommand(t *testing.T) {
	// "oro help foo" should mention "unknown" in output or error.
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help", "foo"})

	err := root.Execute()

	// Cobra may return an error or print "Unknown help topic" to output.
	out := buf.String()
	hasUnknown := strings.Contains(strings.ToLower(out), "unknown")
	if err != nil {
		hasUnknown = hasUnknown || strings.Contains(strings.ToLower(err.Error()), "unknown")
	}

	if !hasUnknown {
		t.Errorf("expected 'unknown' in output or error for unknown command, got output:\n%s\nerr: %v", out, err)
	}
}

// TestWorkflowHelpUsesTaskCanonicalCopy verifies that helpText is task-canonical:
// "task" is the Workflow command and normal user-facing copy does not route users
// through historical bead wording.
func TestWorkflowHelpUsesTaskCanonicalCopy(t *testing.T) {
	workflowIdx := strings.Index(helpText, "Workflow:")
	if workflowIdx < 0 {
		t.Fatal("expected Workflow section in helpText")
	}
	workflowSection := helpText[workflowIdx:]
	if nextSectionIdx := strings.Index(workflowSection[len("Workflow:"):], "\n\n"); nextSectionIdx >= 0 {
		workflowSection = workflowSection[:len("Workflow:")+nextSectionIdx]
	}

	if !strings.Contains(workflowSection, "  task") {
		t.Error("expected 'task' command in Workflow section of helpText")
	}
	if strings.Contains(workflowSection, "  bead") {
		t.Errorf("Workflow section must not list bead as a command:\n%s", workflowSection)
	}
	if strings.Contains(helpText, "oro bead") {
		t.Errorf("helpText must not direct normal users to oro bead:\n%s", helpText)
	}
	if strings.Contains(helpText, "bead") || strings.Contains(helpText, "Bead") {
		t.Errorf("helpText must use task terminology in user-facing copy:\n%s", helpText)
	}
}

// TestHelpIncludesAllRegisteredCommands ensures that if a new command is added
// to root.go, it must also be added to the help text. This prevents drift between
// registered commands and the help output.
func TestHelpIncludesAllRegisteredCommands(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	helpOutput := buf.String()

	// Collect all registered subcommands except "help" and auto-generated cobra commands.
	var missing []string
	for _, cmd := range root.Commands() {
		cmdName := cmd.Name()
		// Skip the help command itself - it shouldn't list itself.
		// Skip auto-generated cobra commands like "completion".
		// Skip hidden commands (e.g. colon-named edit:* worker aliases).
		if cmdName == "help" || cmdName == "completion" || cmd.Hidden {
			continue
		}
		if !strings.Contains(helpOutput, cmdName) {
			missing = append(missing, cmdName)
		}
	}

	if len(missing) > 0 {
		t.Errorf("help text is missing %d registered command(s): %v\nPlease update helpText in cmd_help.go", len(missing), missing)
	}
}
