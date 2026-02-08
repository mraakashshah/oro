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

// hookInput represents the JSON payload sent by Claude Code on stdin.
type hookInput struct {
	HookType  string    `json:"hook_type"`
	ToolName  string    `json:"tool_name"`
	ToolInput toolInput `json:"tool_input"`
}

// toolInput represents the tool_input field from the hook payload.
type toolInput struct {
	FilePath string  `json:"file_path"`
	Offset   float64 `json:"offset,omitempty"`
	Limit    float64 `json:"limit,omitempty"`
}

// denyResponse is the JSON shape for blocking a Read with a summary.
type denyResponse struct {
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// allowJSON is the pre-encoded allow response (empty JSON object).
var allowJSON = []byte("{}")

// HandleHook processes a Claude Code PreToolUse hook event and returns the
// appropriate JSON response. This is the core logic, extracted from main() for
// testability.
//
// Design: fail-open. Every error path returns allowJSON so the hook never
// blocks the user. This is intentional -- a broken hook should degrade to
// normal Claude behavior, not prevent file reads.
//
// Logic:
//  1. Non-Read tools: allow.
//  2. Read tool with bypass conditions (small file, test file, config, offset/limit): allow.
//  3. Read tool on large Go file: summarize and deny with summary.
//  4. Summarize error: allow (fail open, never block the user).
func HandleHook(input []byte) []byte {
	var hook hookInput
	if err := json.Unmarshal(input, &hook); err != nil {
		return allowJSON
	}

	// Only intercept Read tool calls.
	if hook.ToolName != "Read" {
		return allowJSON
	}

	filePath := hook.ToolInput.FilePath
	if filePath == "" {
		return allowJSON
	}

	// Stat the file to get size for bypass check.
	info, err := os.Stat(filePath)
	if err != nil {
		// File doesn't exist or inaccessible: let Claude handle the error.
		return allowJSON
	}

	ti := codesearch.ToolInput{
		FilePath: filePath,
		FileSize: info.Size(),
		Offset:   int(hook.ToolInput.Offset),
		Limit:    int(hook.ToolInput.Limit),
	}

	if codesearch.ShouldBypass(ti) {
		return allowJSON
	}

	// Attempt summarization.
	summary, err := codesearch.Summarize(filePath)
	if err != nil {
		// Fail open: if summarization fails, allow the read through.
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
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oro-search-hook: failed to read stdin: %v\n", err)
		// On stdin read error, output allow to avoid blocking.
		writeOut(allowJSON)
		return
	}

	writeOut(HandleHook(input))
}

// writeOut writes data to stdout, logging any write error to stderr.
func writeOut(data []byte) {
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "oro-search-hook: stdout write error: %v\n", err)
	}
}
