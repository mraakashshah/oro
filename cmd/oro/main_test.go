package main

import (
	"bytes"
	"strings"
	"testing"
)

// executeCommand runs the root command with the given args and returns stdout, stderr, and error.
func executeCommand(args ...string) (stdout string, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestCLICommands(t *testing.T) {
	t.Run("root --help shows usage", func(t *testing.T) {
		out, _, err := executeCommand("--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !containsAll(out, "oro", "start", "stop", "status", "cards") {
			t.Errorf("expected root help to list all subcommands, got:\n%s", out)
		}
	})

	t.Run("root --version prints version", func(t *testing.T) {
		out, _, err := executeCommand("--version")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains(out, "oro") {
			t.Errorf("expected version output to contain 'oro', got: %s", out)
		}
	})

	t.Run("start --help shows flags", func(t *testing.T) {
		out, _, err := executeCommand("start", "--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !containsAll(out, "-w", "--workers", "-d", "--daemon-only", "--model") {
			t.Errorf("expected start help to show -w, --workers, -d, --daemon-only, --model flags, got:\n%s", out)
		}
	})

	t.Run("start parses -w flag", func(t *testing.T) {
		out, _, err := executeCommand("start", "--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !containsAll(out, "-w", "--workers") {
			t.Errorf("expected start help to show -w/--workers flag, got:\n%s", out)
		}
	})

	t.Run("start parses --daemon-only flag", func(t *testing.T) {
		out, _, err := executeCommand("start", "--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !containsAll(out, "-d", "--daemon-only") {
			t.Errorf("expected start help to show -d/--daemon-only flag, got:\n%s", out)
		}
	})

	t.Run("start parses --model flag", func(t *testing.T) {
		out, _, err := executeCommand("start", "--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains(out, "--model") {
			t.Errorf("expected start help to show --model flag, got:\n%s", out)
		}
	})

	t.Run("stop --help works", func(t *testing.T) {
		out, _, err := executeCommand("stop", "--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains(out, "stop") {
			t.Errorf("expected stop help to mention 'stop', got:\n%s", out)
		}
	})

	t.Run("stop executes without error", func(t *testing.T) {
		// Use isolated temp directory to avoid interfering with real dispatcher
		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", tmpDir)

		_, _, err := executeCommand("stop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("status --help works", func(t *testing.T) {
		out, _, err := executeCommand("status", "--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains(out, "status") {
			t.Errorf("expected status help to mention 'status', got:\n%s", out)
		}
	})

	t.Run("status executes without error", func(t *testing.T) {
		// Use isolated temp directory to avoid interfering with real dispatcher
		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", tmpDir)

		_, _, err := executeCommand("status")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("remember requires text argument", func(t *testing.T) {
		_, _, err := executeCommand("remember")
		if err == nil {
			t.Fatal("expected error when no text argument provided")
		}
	})

	t.Run("remember accepts text argument", func(t *testing.T) {
		_, _, err := executeCommand("remember", "always use TDD")
		if err == nil || !strings.Contains(err.Error(), "legacy memory has been retired") {
			t.Fatalf("remember error = %v, want retired memory error", err)
		}
	})

	t.Run("recall requires query argument", func(t *testing.T) {
		_, _, err := executeCommand("recall")
		if err == nil {
			t.Fatal("expected error when no query argument provided")
		}
	})

	t.Run("recall accepts query argument", func(t *testing.T) {
		_, _, err := executeCommand("recall", "TDD workflow")
		if err == nil || !strings.Contains(err.Error(), "legacy memory has been retired") {
			t.Fatalf("recall error = %v, want retired memory error", err)
		}
	})

	t.Run("unknown command returns error", func(t *testing.T) {
		_, _, err := executeCommand("nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown command")
		}
	})
}

func TestLegacyMemoryCommandsAreNotAdvertised(t *testing.T) {
	legacyCommands := []string{"remember", "recall", "forget", "memories"}

	t.Run("root help hides legacy memory commands", func(t *testing.T) {
		out, _, err := executeCommand("--help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains(out, "cards") {
			t.Fatalf("expected cards to remain advertised in root help:\n%s", out)
		}
		assertHelpOmitsCommandLines(t, out, legacyCommands)
	})

	t.Run("custom help hides legacy memory commands", func(t *testing.T) {
		out, _, err := executeCommand("help")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains(out, "cards") {
			t.Fatalf("expected cards to remain advertised in custom help:\n%s", out)
		}
		assertHelpOmitsCommandLines(t, out, legacyCommands)
		if contains(out, "Legacy memory") {
			t.Fatalf("custom help should not advertise retired legacy memory commands:\n%s", out)
		}
	})

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "remember", args: []string{"remember", "always use cards"}},
		{name: "recall", args: []string{"recall", "workflow"}},
		{name: "forget", args: []string{"forget", "1"}},
		{name: "memories", args: []string{"memories", "list"}},
	} {
		t.Run(tc.name+" direct invocation gives migration guidance", func(t *testing.T) {
			_, _, err := executeCommand(tc.args...)
			if err == nil {
				t.Fatalf("%s unexpectedly succeeded", tc.name)
			}
			if !strings.Contains(err.Error(), "legacy memory has been retired") ||
				!strings.Contains(err.Error(), "use cards instead") {
				t.Fatalf("%s error = %v, want explicit cards migration guidance", tc.name, err)
			}
		})
	}
}

func assertHelpOmitsCommandLines(t *testing.T, help string, commands []string) {
	t.Helper()

	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, command := range commands {
			if fields[0] == command {
				t.Fatalf("help advertises legacy memory command %q in line %q:\n%s", command, line, help)
			}
		}
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}

// containsAll checks if s contains all of the given substrings.
func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}
