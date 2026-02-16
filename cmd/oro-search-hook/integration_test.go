package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookEndToEnd is an integration test that compiles the oro-search-hook
// binary and exercises it end-to-end by piping realistic PreToolUse JSON via
// stdin, then asserts correct stdout output.
//
// It also validates that .claude/settings.json has the hook registered.
func TestHookEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// --- Phase 1: Compile the binary ---
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "oro-search-hook")

	build := exec.Command("go", "build", "-o", binPath, "./cmd/oro-search-hook") //nolint:gosec // test-only, args are constant
	build.Dir = projectRoot(t)
	buildOut, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, buildOut)
	}

	// --- Phase 2: Create temp Go files ---
	// Large Go file (>3KB) should be denied with a summary.
	largeGoFile := writeLargeGoFile(t, 200)
	// Small Go file (<3KB) should be allowed.
	smallGoFile := writeSmallGoFile(t, 3)

	t.Run("deny_large_go_file", func(t *testing.T) {
		input := map[string]any{
			"hook_type": "PreToolUse",
			"tool_name": "Read",
			"tool_input": map[string]any{
				"file_path": largeGoFile,
			},
		}
		stdout := runHookBinary(t, binPath, input)

		var resp struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		}
		if err := json.Unmarshal(stdout, &resp); err != nil {
			t.Fatalf("failed to parse stdout JSON %q: %v", string(stdout), err)
		}

		if resp.PermissionDecision != "deny" {
			t.Errorf("expected permissionDecision=deny, got %q", resp.PermissionDecision)
		}
		if !strings.Contains(resp.PermissionDecisionReason, "package testpkg") {
			t.Errorf("expected reason to contain 'package testpkg', got %q", resp.PermissionDecisionReason)
		}
		if !strings.Contains(resp.PermissionDecisionReason, "ExportedFunc") {
			t.Errorf("expected reason to contain function signatures, got %q", resp.PermissionDecisionReason)
		}
	})

	t.Run("allow_small_go_file", func(t *testing.T) {
		input := map[string]any{
			"hook_type": "PreToolUse",
			"tool_name": "Read",
			"tool_input": map[string]any{
				"file_path": smallGoFile,
			},
		}
		stdout := runHookBinary(t, binPath, input)

		// Allow response is empty JSON object.
		trimmed := strings.TrimSpace(string(stdout))
		if trimmed != "{}" {
			t.Errorf("expected allow response {}, got %q", trimmed)
		}
	})

	t.Run("allow_non_read_tool", func(t *testing.T) {
		input := map[string]any{
			"hook_type": "PreToolUse",
			"tool_name": "Bash",
			"tool_input": map[string]any{
				"command": "ls -la",
			},
		}
		stdout := runHookBinary(t, binPath, input)

		trimmed := strings.TrimSpace(string(stdout))
		if trimmed != "{}" {
			t.Errorf("expected allow response {}, got %q", trimmed)
		}
	})

	t.Run("allow_read_with_offset", func(t *testing.T) {
		input := map[string]any{
			"hook_type": "PreToolUse",
			"tool_name": "Read",
			"tool_input": map[string]any{
				"file_path": largeGoFile,
				"offset":    50,
			},
		}
		stdout := runHookBinary(t, binPath, input)

		trimmed := strings.TrimSpace(string(stdout))
		if trimmed != "{}" {
			t.Errorf("expected allow response {}, got %q", trimmed)
		}
	})

	t.Run("settings_json_registration", func(t *testing.T) {
		// This test requires ORO_PROJECT to be set (via oro start or test setup).
		// Skip if not present — settings.json lives in ~/.oro/projects/<name>/.
		oroProject := os.Getenv("ORO_PROJECT")
		if oroProject == "" {
			t.Skip("ORO_PROJECT not set, skipping settings.json registration check")
		}

		// Resolve ORO_HOME (defaults to ~/.oro if not set).
		oroHome := os.Getenv("ORO_HOME")
		if oroHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatalf("failed to get home dir: %v", err)
			}
			oroHome = filepath.Join(home, ".oro")
		}

		// Construct path to settings.json.
		settingsPath := filepath.Join(oroHome, "projects", oroProject, "settings.json")
		data, err := os.ReadFile(settingsPath) //nolint:gosec // test reads project-specific settings
		if err != nil {
			t.Fatalf("failed to read settings.json at %s: %v", settingsPath, err)
		}

		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("failed to parse settings.json: %v", err)
		}

		hooks, ok := settings["hooks"].(map[string]any)
		if !ok {
			t.Fatal("settings.json missing 'hooks' key")
		}

		preToolUse, ok := hooks["PreToolUse"].([]any)
		if !ok {
			t.Fatal("settings.json missing 'PreToolUse' in hooks")
		}

		hookEntry := findHookEntry(preToolUse, "Read", "oro-search-hook")
		if hookEntry == nil {
			t.Fatal("settings.json does not contain oro-search-hook registration under PreToolUse with Read matcher")
		}

		// Verify timeout is set to 5000ms.
		timeoutNum, ok := hookEntry["timeout"].(float64)
		if !ok || timeoutNum != 5000 {
			t.Errorf("expected timeout=5000, got %v", hookEntry["timeout"])
		}

		// Verify type is command.
		hookType, _ := hookEntry["type"].(string)
		if hookType != "command" {
			t.Errorf("expected type=command, got %q", hookType)
		}
	})
}

// runHookBinary executes the compiled hook binary with the given input as
// JSON on stdin and returns stdout bytes.
func runHookBinary(t *testing.T, binPath string, input map[string]any) []byte {
	t.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("failed to marshal input: %v", err)
	}

	cmd := exec.Command(binPath)
	cmd.Stdin = strings.NewReader(string(inputJSON))

	stdout, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("hook binary exited with error: %v\nstderr: %s", err, exitErr.Stderr)
		}
		t.Fatalf("hook binary failed: %v", err)
	}

	return stdout
}

// projectRoot returns the project root directory by walking up from the test
// file's location to find go.mod.
func projectRoot(t *testing.T) string {
	t.Helper()

	// Start from the current working directory (which go test sets to the
	// package directory).
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

// writeLargeGoFile creates a temporary Go file with N exported functions,
// ensuring it exceeds the 3KB bypass threshold.
func writeLargeGoFile(t *testing.T, numFuncs int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "large.go")

	var b strings.Builder
	b.WriteString("package testpkg\n\nimport \"fmt\"\n\n")
	for i := range numFuncs {
		b.WriteString("// ExportedFunc")
		b.WriteString(itoa(i))
		b.WriteString(" does something.\n")
		b.WriteString("func ExportedFunc")
		b.WriteString(itoa(i))
		b.WriteString("(ctx string, n int) (string, error) {\n")
		b.WriteString("\treturn fmt.Sprintf(\"hello %s %d\", ctx, n), nil\n")
		b.WriteString("}\n\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("failed to write large Go file: %v", err)
	}

	// Sanity check: must exceed 3KB.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat large Go file: %v", err)
	}
	if info.Size() <= 3072 {
		t.Fatalf("large Go file is only %d bytes, expected >3072", info.Size())
	}

	return path
}

// writeSmallGoFile creates a temporary Go file with N exported functions,
// ensuring it stays under the 3KB bypass threshold.
func writeSmallGoFile(t *testing.T, numFuncs int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "small.go")

	var b strings.Builder
	b.WriteString("package testpkg\n\n")
	for i := range numFuncs {
		b.WriteString("func F")
		b.WriteString(itoa(i))
		b.WriteString("() {}\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("failed to write small Go file: %v", err)
	}

	// Sanity check: must be under 3KB.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat small Go file: %v", err)
	}
	if info.Size() > 3072 {
		t.Fatalf("small Go file is %d bytes, expected <=3072", info.Size())
	}

	return path
}

// findHookEntry searches a PreToolUse array for a hook entry matching the
// given matcher and command substring. Returns the hook map if found, nil otherwise.
func findHookEntry(preToolUse []any, matcher, cmdSubstring string) map[string]any {
	for _, entry := range preToolUse {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		m, _ := entryMap["matcher"].(string)
		if m != matcher {
			continue
		}
		hookList, ok := entryMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hookList {
			hMap, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hMap["command"].(string)
			if strings.Contains(cmd, cmdSubstring) {
				return hMap
			}
		}
	}
	return nil
}

// itoa converts a non-negative int to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
