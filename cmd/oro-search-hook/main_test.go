package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
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

// TestRun verifies the happy path: a non-cat Bash command routes to the Codex
// Bash arm and allows with EMPTY STDOUT (zero bytes), not {}. The Codex Bash
// allow contract is "no bytes" — see handleCodexBash.
func TestRun(t *testing.T) {
	input := `{"hook_type":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	var out bytes.Buffer
	run(strings.NewReader(input), &out)
	if got := out.Bytes(); len(got) != 0 {
		t.Errorf("expected empty stdout for non-cat Bash (allow), got %q", string(got))
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

func TestRunSessionStartProbeExitStatus(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe")
	t.Setenv("ORO_HOOK_PROBE", probe)
	for _, input := range []string{
		`{"hook_type":"SessionStart"}`,
		`{"hook_event_name":"SessionStart"}`,
	} {
		var out bytes.Buffer
		if err := run(strings.NewReader(input), &out); err != nil {
			t.Fatalf("run() error: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("SessionStart wrote stdout %q", out.String())
		}
		if _, err := os.Stat(probe); err != nil {
			t.Fatalf("probe was not created: %v", err)
		}
		if err := os.Remove(probe); err != nil {
			t.Fatalf("remove probe: %v", err)
		}
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
	if _, err := exec.LookPath("ast-grep"); err != nil {
		t.Skip("ast-grep not installed, skipping")
	}

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
		wantEmpty  bool   // expect zero bytes (Codex Bash allow contract)
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
			name: "allow non-cat Bash with empty stdout",
			input: map[string]any{
				"hook_type": "PreToolUse",
				"tool_name": "Bash",
				"tool_input": map[string]any{
					"command": "ls",
				},
			},
			wantEmpty: true,
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

			if tt.wantEmpty {
				if len(output) != 0 {
					t.Errorf("expected empty stdout (Codex Bash allow), got %q", string(output))
				}
				return
			}

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

// TestHandleHookCodexShape verifies that HandleHook correctly processes Codex
// CLI hook input where file_path is at tool_input.path (not tool_input.file_path)
// and tool_name is str_replace_based_edit_tool with command:"view".
// Codex does not send hook_type; runtime is selected by inspecting stdin shape.
func TestHandleHookCodexShape(t *testing.T) {
	if _, err := exec.LookPath("ast-grep"); err != nil {
		t.Skip("ast-grep not installed, skipping")
	}

	largeGoFile := writeTempGoFile(t, 200)
	smallGoFile := writeTempGoFile(t, 3)

	tests := []struct {
		name      string
		input     map[string]any
		wantAllow bool
		wantDeny  bool
	}{
		{
			name: "deny large Go file via Codex view shape",
			input: map[string]any{
				"tool_name": "str_replace_based_edit_tool",
				"tool_input": map[string]any{
					"command": "view",
					"path":    largeGoFile,
				},
			},
			wantDeny: true,
		},
		{
			name: "allow small Go file via Codex view shape (bypass)",
			input: map[string]any{
				"tool_name": "str_replace_based_edit_tool",
				"tool_input": map[string]any{
					"command": "view",
					"path":    smallGoFile,
				},
			},
			wantAllow: true,
		},
		{
			name: "allow non-view Codex command (str_replace is not a read)",
			input: map[string]any{
				"tool_name": "str_replace_based_edit_tool",
				"tool_input": map[string]any{
					"command": "str_replace",
					"path":    largeGoFile,
				},
			},
			wantAllow: true,
		},
		{
			name: "allow Codex view with view_range set (partial read bypass)",
			input: map[string]any{
				"tool_name": "str_replace_based_edit_tool",
				"tool_input": map[string]any{
					"command":    "view",
					"path":       largeGoFile,
					"view_range": []int{1, 50},
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
			if tt.wantDeny && resp.PermissionDecision != "deny" {
				t.Errorf("expected deny, got permissionDecision=%q", resp.PermissionDecision)
			}
		})
	}
}

// TestHandleHookFailsOpenOnUnknownShape verifies that HandleHook returns the
// allow response ({}) for any input that doesn't match the Claude Code or Codex
// hook shapes. Fail-open preserves the user-never-blocked invariant.
func TestHandleHookFailsOpenOnUnknownShape(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "empty object",
			input: `{}`,
		},
		{
			name:  "completely different structure",
			input: `{"event":"tool_call","action":"read","target":"/some/file.go"}`,
		},
		{
			name:  "unknown tool_name without hook_type",
			input: `{"tool_name":"UnknownTool","tool_input":{"data":"something"}}`,
		},
		{
			name:  "malformed JSON",
			input: `not valid json`,
		},
		{
			name:  "JSON array instead of object",
			input: `[]`,
		},
		{
			name:  "str_replace_based_edit_tool with unknown command",
			input: `{"tool_name":"str_replace_based_edit_tool","tool_input":{"command":"create","path":"/tmp/file.go","file_text":"package main"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := HandleHook([]byte(tt.input))

			trimmed := strings.TrimSpace(string(output))
			if trimmed == "{}" {
				return // explicit allow
			}
			var resp hookResponse
			if err := json.Unmarshal(output, &resp); err != nil {
				t.Errorf("expected valid JSON allow response for unknown shape, got %q", trimmed)
				return
			}
			if resp.PermissionDecision != "" {
				t.Errorf("expected allow (no permissionDecision) for unknown shape, got permissionDecision=%q", resp.PermissionDecision)
			}
		})
	}
}

// codexRewriteDecoded decodes the Codex Bash allow+rewrite shape emitted by
// handleCodexBash (codex ignores deny for trusted reads, so the cat is rewritten).
type codexRewriteDecoded struct {
	HookSpecificOutput struct {
		HookEventName      string `json:"hookEventName"`
		PermissionDecision string `json:"permissionDecision"`
		UpdatedInput       struct {
			Command string `json:"command"`
		} `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
}

// bashEvent builds a Codex-shaped PreToolUse Bash hook event for command.
func bashEvent(t *testing.T, command string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_type":  "PreToolUse",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": command},
	})
	if err != nil {
		t.Fatalf("failed to marshal bash event: %v", err)
	}
	return b
}

// TestHandleCodexBashRead verifies the Codex Bash read-hook arm: a bare
// `cat [--] <largefile>` of a large code file is intercepted with a Codex
// allow+updatedInput response that rewrites the cat into `printf '%s' '<summary>'`
// (codex ignores deny for trusted reads, so the read is suppressed by rewriting);
// every other Bash command shape — and any bypassed cat — allows with EMPTY
// STDOUT (zero bytes), never {}.
func TestHandleCodexBashRead(t *testing.T) {
	if _, err := exec.LookPath("ast-grep"); err != nil {
		t.Skip("ast-grep not installed, skipping")
	}

	largeGoFile := writeTempGoFile(t, 200)             // >3KB code → intercept
	smallGoFile := writeTempGoFile(t, 3)               // <3KB → bypass on size
	badFile := writeTempFile(t, ".go", "not valid Go") // summarize error → allow
	testFile := writeTempFile(t, "_test.go", strings.Repeat("// filler line\n", 400))
	nonCodeFile := writeTempFile(t, ".json", strings.Repeat("{\"k\":\"v\"}\n", 400))

	// --- intercept cases: bare cat of a large code file → allow + rewrite ---
	interceptCmds := []struct {
		name string
		cmd  string
	}{
		{"cat path", "cat " + largeGoFile},
		{"cat -- path", "cat -- " + largeGoFile},
	}
	for _, tc := range interceptCmds {
		t.Run("intercept: "+tc.name, func(t *testing.T) {
			out := HandleHook(bashEvent(t, tc.cmd))
			var resp codexRewriteDecoded
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Fatalf("expected Codex allow+rewrite JSON, got %q: %v", string(out), err)
			}
			if resp.HookSpecificOutput.PermissionDecision != "allow" {
				t.Errorf("expected permissionDecision=allow, got %q", resp.HookSpecificOutput.PermissionDecision)
			}
			if resp.HookSpecificOutput.HookEventName != "PreToolUse" {
				t.Errorf("expected hookEventName=PreToolUse, got %q", resp.HookSpecificOutput.HookEventName)
			}
			rw := resp.HookSpecificOutput.UpdatedInput.Command
			if !strings.HasPrefix(rw, "printf '%s' ") {
				t.Errorf("rewritten command must print the summary via printf, got %q", rw)
			}
			// The rewrite must carry the summary (a signature), not the raw cat.
			if !strings.Contains(rw, "package testpkg") {
				t.Errorf("rewritten command must contain the AST summary, got %q", rw)
			}
			if strings.Contains(rw, "cat ") {
				t.Errorf("rewritten command must not re-invoke cat (raw read), got %q", rw)
			}
		})
	}

	// --- allow cases: EMPTY STDOUT (zero bytes), never {} ---
	allowCmds := []struct {
		name string
		cmd  string
	}{
		{"sed range", "sed -n 1,10p " + largeGoFile},
		{"head", "head " + largeGoFile},
		{"tail", "tail -20 " + largeGoFile},
		{"rg", "rg func " + largeGoFile},
		{"pipe", "cat " + largeGoFile + " | head"},
		{"chain and", "cat " + largeGoFile + " && echo done"},
		{"chain semicolon", "cat " + largeGoFile + " ; echo done"},
		{"chain or", "cat " + largeGoFile + " || true"},
		{"redirect out", "cat " + largeGoFile + " > /tmp/out"},
		{"redirect in", "cat < " + largeGoFile},
		{"command substitution", "cat $(echo " + largeGoFile + ")"},
		{"backtick substitution", "cat `echo x`"},
		{"glob", "cat pkg/*.go"},
		{"multiple files", "cat " + largeGoFile + " " + smallGoFile},
		{"extra flag", "cat -n " + largeGoFile},
		{"ls", "ls -la"},
		{"empty command", ""},
		{"blank command", "   "},
		{"bare cat", "cat"},
		{"bypass small file", "cat " + smallGoFile},
		{"bypass test file", "cat " + testFile},
		{"bypass non-code file", "cat " + nonCodeFile},
		{"summarize error fails open", "cat " + badFile},
		{"stat error fails open", "cat /nonexistent/path/file.go"},
	}
	for _, tc := range allowCmds {
		t.Run("allow empty stdout: "+tc.name, func(t *testing.T) {
			out := HandleHook(bashEvent(t, tc.cmd))
			if len(out) != 0 {
				t.Errorf("expected empty stdout (zero bytes), got %q", string(out))
			}
		})
	}
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
