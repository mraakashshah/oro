package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errorWriter is an io.Writer that always returns an error.
type errorWriter struct{}

func (e *errorWriter) Write(p []byte) (int, error) {
	return 0, errors.New("simulated write error")
}

// errorReader is an io.Reader that always returns an error.
type errorReader struct{}

func (e *errorReader) Read(p []byte) (int, error) {
	return 0, errors.New("simulated read error")
}

// TestWriteOut verifies writeOut writes data to the provided writer.
func TestWriteOut(t *testing.T) {
	var buf bytes.Buffer
	writeOut(&buf, []byte(`{"ok":true}`))
	if got := buf.String(); got != `{"ok":true}` {
		t.Errorf("got %q, want %q", got, `{"ok":true}`)
	}
}

// TestWriteOut_writeError verifies writeOut does not panic when the writer fails.
func TestWriteOut_writeError(t *testing.T) {
	// Should not panic; error is logged to stderr.
	writeOut(&errorWriter{}, []byte(`{}`))
}

// TestRun verifies the happy path: valid JSON input produces the expected response.
func TestRun(t *testing.T) {
	input := `{"hook_type":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	var out bytes.Buffer
	run(strings.NewReader(input), &out)
	if got := strings.TrimSpace(out.String()); got != "{}" {
		t.Errorf("expected {} for non-Read tool, got %q", got)
	}
}

// TestRun_readError verifies that run() fails open when stdin cannot be read.
func TestRun_readError(t *testing.T) {
	var out bytes.Buffer
	run(&errorReader{}, &out)
	if got := strings.TrimSpace(out.String()); got != "{}" {
		t.Errorf("expected {} on read error (fail-open), got %q", got)
	}
}

// Ensure errorWriter satisfies io.Writer at compile time.
var _ io.Writer = (*errorWriter)(nil)

// Ensure errorReader satisfies io.Reader at compile time.
var _ io.Reader = (*errorReader)(nil)

// hookResponse is the decoded JSON response from HandleHook.
type hookResponse struct {
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func TestHookDispatch(t *testing.T) {
	// Create a large Go file (>3KB) for testing summarization.
	largeGoFile := writeTempGoFile(t, 200)
	// Create a small Go file (<3KB) for bypass testing.
	smallGoFile := writeTempGoFile(t, 5)
	// Create a file that will cause summarize to fail (not valid Go).
	badFile := writeTempFile(t, ".go", "this is not valid Go code {{{")
	largePyFile := writeTempFile(t, ".py", generatePythonFixture())
	largeTSFile := writeTempFile(t, ".ts", generateTypeScriptFixture())

	tests := []struct {
		name       string
		input      map[string]any
		wantAllow  bool   // expect empty JSON {} (allow)
		wantDeny   bool   // expect permissionDecision == "deny"
		wantReason string // substring expected in permissionDecisionReason
	}{
		{
			name: "deny and summarize large Go file Read",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": largeGoFile,
				},
			},
			wantDeny:   true,
			wantReason: "package testpkg",
		},
		{
			name: "allow small Go file Read (bypass)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": smallGoFile,
				},
			},
			wantAllow: true,
		},
		{
			name: "allow non-Read tool (Write)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Write",
				"tool_input": map[string]any{
					"file_path": "/some/file.go",
				},
			},
			wantAllow: true,
		},
		{
			name: "allow non-Read tool (Bash)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Bash",
				"tool_input": map[string]any{
					"command": "ls",
				},
			},
			wantAllow: true,
		},
		{
			name: "allow Read with explicit offset (bypass)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": largeGoFile,
					"offset":    50,
				},
			},
			wantAllow: true,
		},
		{
			name: "allow Read with explicit limit (bypass)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": largeGoFile,
					"limit":     100,
				},
			},
			wantAllow: true,
		},
		{
			name: "allow on summarize error (graceful fallthrough)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": badFile,
				},
			},
			wantAllow: true,
		},
		{
			name: "allow Grep tool (day-two passthrough)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Grep",
				"tool_input": map[string]any{
					"pattern": "func main",
				},
			},
			wantAllow: true,
		},
		{
			name: "allow Read of non-Go file (JSON)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": "/some/config.json",
				},
			},
			wantAllow: true,
		},
		{
			name: "allow Read of test file",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": "/some/file_test.go",
				},
			},
			wantAllow: true,
		},
		{
			name: "deny and summarize large Python file Read",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": largePyFile,
				},
			},
			wantDeny:   true,
			wantReason: "func",
		},
		{
			name: "deny and summarize large TypeScript file Read",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": largeTSFile,
				},
			},
			wantDeny:   true,
			wantReason: "func",
		},
		{
			name: "allow Read of nonexistent file (stat error = graceful allow)",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Read",
				"tool_input": map[string]any{
					"file_path": "/nonexistent/path/file.go",
				},
			},
			wantAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("failed to marshal input: %v", err)
			}

			output := HandleHook(inputJSON)

			var resp hookResponse
			if err := json.Unmarshal(output, &resp); err != nil {
				t.Fatalf("failed to unmarshal output %q: %v", string(output), err)
			}

			if tt.wantAllow && resp.PermissionDecision != "" {
				t.Errorf("expected allow (empty JSON), got permissionDecision=%q", resp.PermissionDecision)
			}

			if !tt.wantDeny {
				return
			}

			if resp.PermissionDecision != "deny" {
				t.Errorf("expected deny, got permissionDecision=%q", resp.PermissionDecision)
			}
			if tt.wantReason != "" && !strings.Contains(resp.PermissionDecisionReason, tt.wantReason) {
				t.Errorf("expected reason to contain %q, got %q", tt.wantReason, resp.PermissionDecisionReason)
			}
		})
	}
}

// writeTempGoFile creates a temporary Go file with N exported functions.
// Returns the file path. File is cleaned up after test.
func writeTempGoFile(t *testing.T, numFuncs int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile.go")

	var content string
	content = "package testpkg\n\nimport \"fmt\"\n\n"
	for i := range numFuncs {
		content += "// ExportedFunc" + itoa(i) + " does something.\n"
		content += "func ExportedFunc" + itoa(i) + "(ctx string, n int) (string, error) {\n"
		content += "\treturn fmt.Sprintf(\"hello %s %d\", ctx, n), nil\n"
		content += "}\n\n"
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temp Go file: %v", err)
	}
	return path
}

// writeTempFile creates a temporary file with the given extension and content.
func writeTempFile(t *testing.T, ext, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile"+ext)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}

// itoa converts an int to a string without importing strconv.
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

// generatePythonFixture returns a Python source file >3KB with classes and functions.
func generatePythonFixture() string {
	var b strings.Builder
	b.WriteString("from typing import Any, Optional, Callable\nimport asyncio\n\n")
	for i := range 20 {
		b.WriteString("class Service" + itoa(i) + ":\n")
		b.WriteString("    def __init__(self) -> None:\n")
		b.WriteString("        self.name = \"service_" + itoa(i) + "\"\n\n")
		b.WriteString("    def process(self, data: dict) -> dict:\n")
		b.WriteString("        return {\"status\": \"ok\", \"service\": self.name}\n\n")
		b.WriteString("    async def handle(self, request: Any) -> dict:\n")
		b.WriteString("        result = await asyncio.sleep(0)\n")
		b.WriteString("        return {\"handled\": True}\n\n")
	}
	b.WriteString("def main() -> None:\n")
	b.WriteString("    pass\n")
	return b.String()
}

// generateTypeScriptFixture returns a TypeScript source file >3KB.
func generateTypeScriptFixture() string {
	var b strings.Builder
	b.WriteString("export interface Config {\n  port: number;\n  host: string;\n}\n\n")
	for i := range 20 {
		b.WriteString("interface Handler" + itoa(i) + " {\n")
		b.WriteString("  handle(req: Request): Promise<Response>;\n")
		b.WriteString("  name: string;\n}\n\n")
		b.WriteString("function processRequest" + itoa(i) + "(req: Request): Response {\n")
		b.WriteString("  return new Response(\"ok\");\n}\n\n")
	}
	b.WriteString("export function main(): void {\n  console.log(\"start\");\n}\n")
	return b.String()
}
