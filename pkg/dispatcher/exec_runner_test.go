package dispatcher_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/dispatcher"
)

func TestExecCommandRunner_Run_Success(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Test simple command that should succeed
	out, err := runner.Run(ctx, "echo", "hello world")
	if err != nil {
		t.Fatalf("Run(echo) failed: %v", err)
	}

	got := strings.TrimSpace(string(out))
	want := "hello world"
	if got != want {
		t.Errorf("Run(echo) output = %q, want %q", got, want)
	}
}

func TestExecCommandRunner_Run_MultipleArgs(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Test command with multiple arguments
	out, err := runner.Run(ctx, "printf", "%s-%s-%s", "a", "b", "c")
	if err != nil {
		t.Fatalf("Run(printf) failed: %v", err)
	}

	got := string(out)
	want := "a-b-c"
	if got != want {
		t.Errorf("Run(printf) output = %q, want %q", got, want)
	}
}

func TestExecCommandRunner_Run_WithGitRepo(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Create a temporary git repository
	tmpDir := t.TempDir()

	// Initialize git repo
	_, err := runner.Run(ctx, "git", "-C", tmpDir, "init")
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// Configure git user (required for commits)
	_, err = runner.Run(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User")
	if err != nil {
		t.Fatalf("git config user.name failed: %v", err)
	}
	_, err = runner.Run(ctx, "git", "-C", tmpDir, "config", "user.email", "test@example.com")
	if err != nil {
		t.Fatalf("git config user.email failed: %v", err)
	}

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Add file to git
	_, err = runner.Run(ctx, "git", "-C", tmpDir, "add", "test.txt")
	if err != nil {
		t.Fatalf("git add failed: %v", err)
	}

	// Commit the file
	_, err = runner.Run(ctx, "git", "-C", tmpDir, "commit", "-m", "Initial commit")
	if err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Verify commit was created using git log
	out, err := runner.Run(ctx, "git", "-C", tmpDir, "log", "--oneline")
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}

	logOutput := string(out)
	if !strings.Contains(logOutput, "Initial commit") {
		t.Errorf("git log output should contain commit message, got: %q", logOutput)
	}

	// Verify file exists in git using git ls-files
	out, err = runner.Run(ctx, "git", "-C", tmpDir, "ls-files")
	if err != nil {
		t.Fatalf("git ls-files failed: %v", err)
	}

	lsOutput := strings.TrimSpace(string(out))
	if lsOutput != "test.txt" {
		t.Errorf("git ls-files output = %q, want %q", lsOutput, "test.txt")
	}
}

func TestExecCommandRunner_Run_CommandNotFound(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Test command that doesn't exist
	_, err := runner.Run(ctx, "nonexistent-command-12345")
	if err == nil {
		t.Fatal("Run(nonexistent-command) should fail")
	}

	// Error should mention the command name
	if !strings.Contains(err.Error(), "nonexistent-command-12345") {
		t.Errorf("error should mention command name, got: %v", err)
	}
}

func TestExecCommandRunner_Run_NonZeroExit(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Test command that exits with non-zero status
	_, err := runner.Run(ctx, "sh", "-c", "echo 'error message' >&2; exit 42")
	if err == nil {
		t.Fatal("Run(exit 42) should fail")
	}

	// Error should include stderr output
	errStr := err.Error()
	if !strings.Contains(errStr, "error message") {
		t.Errorf("error should include stderr, got: %v", errStr)
	}

	// Error should mention the command
	if !strings.Contains(errStr, "sh") {
		t.Errorf("error should mention command, got: %v", errStr)
	}
}

func TestExecCommandRunner_Run_ContextCancellation(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}

	// Create a context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	// Try to run a command with cancelled context
	_, err := runner.Run(ctx, "sleep", "10")
	if err == nil {
		t.Fatal("Run with cancelled context should fail")
	}

	// Error should be related to context cancellation
	if !strings.Contains(err.Error(), "sleep") {
		t.Errorf("error should mention command, got: %v", err)
	}
}

func TestExecCommandRunner_Run_ContextTimeout(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}

	// Create a context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Try to run a command that takes longer than timeout
	_, err := runner.Run(ctx, "sleep", "10")
	if err == nil {
		t.Fatal("Run with timeout should fail")
	}

	// Error should mention the command
	if !strings.Contains(err.Error(), "sleep") {
		t.Errorf("error should mention command, got: %v", err)
	}
}

func TestExecCommandRunner_Run_EmptyOutput(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Command that produces no output
	out, err := runner.Run(ctx, "true")
	if err != nil {
		t.Fatalf("Run(true) failed: %v", err)
	}

	if len(out) != 0 {
		t.Errorf("Run(true) output should be empty, got %d bytes", len(out))
	}
}

func TestExecCommandRunner_Run_BinaryOutput(t *testing.T) {
	runner := &dispatcher.ExecCommandRunner{}
	ctx := context.Background()

	// Create temp directory
	tmpDir := t.TempDir()

	// Create a file with known content
	testFile := filepath.Join(tmpDir, "test.bin")
	testData := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
	if err := os.WriteFile(testFile, testData, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Read file using cat
	out, err := runner.Run(ctx, "cat", testFile)
	if err != nil {
		t.Fatalf("Run(cat) failed: %v", err)
	}

	// Verify binary data is preserved
	if len(out) != len(testData) {
		t.Fatalf("output length = %d, want %d", len(out), len(testData))
	}
	for i, b := range testData {
		if out[i] != b {
			t.Errorf("output[%d] = 0x%02x, want 0x%02x", i, out[i], b)
		}
	}
}

func TestExecCommandRunner_ImplementsInterface(t *testing.T) {
	var _ dispatcher.CommandRunner = &dispatcher.ExecCommandRunner{}
}
