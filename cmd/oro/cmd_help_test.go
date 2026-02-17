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
		"Memory:",
		"Control:",
		"Search:",
	}
	for _, cat := range categories {
		if !strings.Contains(out, cat) {
			t.Errorf("expected category header %q in output, got:\n%s", cat, out)
		}
	}

	// Verify all 15 subcommands are listed (all commands except "help" itself).
	subcommands := []string{
		"init",
		"start",
		"stop",
		"cleanup",
		"status",
		"logs",
		"dash",
		"remember",
		"recall",
		"forget",
		"memories",
		"directive",
		"index",
		"worker",
		"ingest",
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
		if cmdName == "help" || cmdName == "completion" {
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
