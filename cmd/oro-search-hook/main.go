// Binary oro-search-hook is a Claude Code PreToolUse hook that intercepts Read
// tool calls and returns AST-based summaries instead of raw file content for
// large Go source files. This saves tokens by replacing full file reads with
// compact structural summaries (function signatures, type declarations, etc.).
//
// Protocol: reads JSON from stdin, writes JSON to stdout.
//   - Allow (pass through): {}
//   - Deny (with summary):  {"permissionDecision":"deny","permissionDecisionReason":"..."}
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"oro/pkg/codesearch"
)

// run reads a hook event from r, processes it, and writes the response to w.
// Extracted from main for testability.
func run(r io.Reader, w io.Writer) {
	input, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oro-search-hook: failed to read stdin: %v\n", err)
		// On stdin read error, output allow to avoid blocking.
		writeOut(w, allowJSON)
		return
	}

	writeOut(w, HandleHook(input))
}

// hookInput represents the JSON payload sent by Claude Code on stdin.
type hookInput struct {
	HookType  string    `json:"hook_type"`
	ToolName  string    `json:"tool_name"`
	ToolInput toolInput `json:"tool_input"`
}

// toolInput represents the tool_input field from the Claude Code hook payload.
type toolInput struct {
	FilePath string  `json:"file_path"`
	Offset   float64 `json:"offset,omitempty"`
	Limit    float64 `json:"limit,omitempty"`
}

// codexToolInput represents the tool_input field from the Codex CLI hook payload.
// Codex uses "path" instead of "file_path", and "view_range" instead of offset/limit.
type codexToolInput struct {
	Command   string    `json:"command"`
	Path      string    `json:"path"`
	ViewRange []float64 `json:"view_range,omitempty"`
}

// codexHookInput represents the JSON payload sent by Codex CLI hooks.
// Unlike Claude Code, Codex omits hook_type and uses str_replace_based_edit_tool
// with command:"view" for file reads.
type codexHookInput struct {
	ToolName  string         `json:"tool_name"`
	ToolInput codexToolInput `json:"tool_input"`
}

// denyResponse is the JSON shape for blocking a Read with a summary.
type denyResponse struct {
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// allowJSON is the pre-encoded allow response (empty JSON object).
var allowJSON = []byte("{}")

// HandleHook processes a hook event and returns the appropriate JSON response.
// This is the core logic, extracted from main() for testability.
//
// Design: fail-open. Every error path returns allowJSON so the hook never
// blocks the user. This is intentional -- a broken hook should degrade to
// normal agent behavior, not prevent file reads.
//
// Shape detection (runtime selected by inspecting stdin):
//  1. Claude Code shape: tool_name="Read", file at tool_input.file_path.
//  2. Codex shape: tool_name="str_replace_based_edit_tool", command="view",
//     file at tool_input.path. No hook_type field.
//  3. Unknown shape: fail-open allow (user-never-blocked invariant).
//
// Within each shape:
//   - Non-read tools / non-view commands: allow.
//   - Bypass conditions (small file, test file, config, offset/limit/view_range): allow.
//   - Large file: summarize and deny with summary.
//   - Summarize error: allow (fail open).
func HandleHook(input []byte) []byte {
	var hook hookInput
	if err := json.Unmarshal(input, &hook); err != nil {
		return allowJSON
	}

	switch hook.ToolName {
	case "Read":
		// Claude Code shape: file path at tool_input.file_path.
		return handleClaudeRead(hook.ToolInput)

	case "str_replace_based_edit_tool":
		// Codex shape: file path at tool_input.path, view indicated by command="view".
		var codexHook codexHookInput
		if err := json.Unmarshal(input, &codexHook); err != nil {
			return allowJSON
		}
		return handleCodexView(codexHook.ToolInput)

	default:
		// Unknown shape or non-intercepted tool: fail-open.
		return allowJSON
	}
}

// handleClaudeRead processes a Claude Code Read tool_input.
func handleClaudeRead(ti toolInput) []byte {
	filePath := ti.FilePath
	if filePath == "" {
		return allowJSON
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return allowJSON
	}
	cti := codesearch.ToolInput{
		FilePath: filePath,
		FileSize: info.Size(),
		Offset:   int(ti.Offset),
		Limit:    int(ti.Limit),
	}
	if codesearch.ShouldBypass(cti) {
		return allowJSON
	}
	return summarizeAndDeny(filePath)
}

// handleCodexView processes a Codex str_replace_based_edit_tool tool_input.
// Only command="view" without a view_range is intercepted; all others allow.
func handleCodexView(ti codexToolInput) []byte {
	if ti.Command != "view" || ti.Path == "" {
		return allowJSON
	}
	info, err := os.Stat(ti.Path)
	if err != nil {
		return allowJSON
	}
	// view_range indicates a partial read — treat like offset (bypass summarization).
	offset := 0
	if len(ti.ViewRange) > 0 {
		offset = 1
	}
	cti := codesearch.ToolInput{
		FilePath: ti.Path,
		FileSize: info.Size(),
		Offset:   offset,
	}
	if codesearch.ShouldBypass(cti) {
		return allowJSON
	}
	return summarizeAndDeny(ti.Path)
}

// summarizeAndDeny attempts to summarize filePath and returns a deny response.
// On summarization error, returns allowJSON (fail-open).
func summarizeAndDeny(filePath string) []byte {
	summary, err := codesearch.SummarizeFile(filePath)
	if err != nil {
		return allowJSON
	}
	resp := denyResponse{
		PermissionDecision:       "deny",
		PermissionDecisionReason: summary,
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return allowJSON
	}
	return out
}

func main() {
	run(os.Stdin, os.Stdout)
}

// writeOut writes data to w, logging any write error to stderr.
func writeOut(w io.Writer, data []byte) {
	if _, err := w.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "oro-search-hook: stdout write error: %v\n", err)
	}
}
