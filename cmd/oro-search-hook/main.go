// Binary oro-search-hook is a PreToolUse hook that intercepts file reads and
// returns AST-based summaries instead of raw file content for large source
// files. This saves tokens by replacing full file reads with compact structural
// summaries (function signatures, type declarations, etc.).
//
// It handles three read surfaces:
//   - Claude Code Read tool (tool_name="Read", tool_input.file_path).
//   - Codex Bash reads (tool_name="Bash", a bare `cat [--] <path>`).
//   - Legacy Codex view (tool_name="str_replace_based_edit_tool", command="view").
//
// Protocol: reads JSON from stdin, writes to stdout.
//   - Claude/legacy allow (pass through): {}
//   - Claude/legacy deny (with summary):  {"permissionDecision":"deny","permissionDecisionReason":"..."}
//   - Codex Bash allow (pass through):    EMPTY STDOUT (zero bytes) — matches the
//     sibling destructive_command_guard.py contract; {} is unverified on the
//     Bash surface and could be parsed as a malformed decision.
//   - Codex Bash intercept (with summary): allow + updatedInput rewriting the
//     `cat` into `printf '%s' '<summary>'`. Codex exec ignores a PreToolUse deny
//     for trusted read commands (cat/ls/sed run regardless), so the read is
//     suppressed by REWRITING the command, not denying it — verified live against
//     codex-cli 0.144.6.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"oro/pkg/codesearch"
)

// run reads a hook event from r, processes it, and writes the response to w.
// Extracted from main for testability.
func run(r io.Reader, w io.Writer) error {
	input, err := io.ReadAll(r)
	if err != nil {
		// On stdin read error, output allow to avoid blocking.
		writeOut(w, allowJSON)
		return fmt.Errorf("read stdin: %w", err)
	}

	var event struct {
		HookType      string `json:"hook_type"`
		HookEventName string `json:"hook_event_name"`
	}
	if json.Unmarshal(input, &event) == nil &&
		(event.HookType == "SessionStart" || event.HookEventName == "SessionStart") {
		return handleSessionStart(os.Getenv("ORO_HOOK_PROBE"))
	}

	writeOut(w, HandleHook(input))
	return nil
}

func handleSessionStart(probePath string) error {
	if probePath == "" {
		return fmt.Errorf("ORO_HOOK_PROBE is not set")
	}
	file, err := os.OpenFile(probePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: ORO_HOOK_PROBE intentionally selects the private probe path.
	if err != nil {
		return fmt.Errorf("create ORO_HOOK_PROBE: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close ORO_HOOK_PROBE: %w", err)
	}
	return nil
}

// hookInput represents the JSON payload sent by Claude Code on stdin.
type hookInput struct {
	HookType  string    `json:"hook_type"`
	ToolName  string    `json:"tool_name"`
	ToolInput toolInput `json:"tool_input"`
}

// toolInput represents the tool_input field from the Claude Code hook payload.
// Command carries the Codex Bash shell string (tool_name="Bash").
type toolInput struct {
	FilePath string  `json:"file_path"`
	Command  string  `json:"command,omitempty"`
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

// codexRewriteResponse is the JSON shape for intercepting a Codex Bash read.
//
// Codex exec does NOT honor a PreToolUse "deny" for trusted read commands
// (cat/ls/sed run regardless — verified live against codex-cli 0.144.6), so a
// deny cannot suppress a `cat`. Codex DOES honor `updatedInput`, which rewrites
// the command before execution. We therefore ALLOW the call but rewrite
// `cat <path>` into a command that emits the AST summary, so the model receives
// the summary and the raw file is never read.
type codexRewriteResponse struct {
	HookSpecificOutput struct {
		HookEventName      string `json:"hookEventName"`
		PermissionDecision string `json:"permissionDecision"`
		UpdatedInput       struct {
			Command string `json:"command"`
		} `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
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
//  2. Codex Bash shape: tool_name="Bash", shell string at tool_input.command.
//  3. Legacy Codex view: tool_name="str_replace_based_edit_tool", command="view",
//     file at tool_input.path. No hook_type field.
//  4. Unknown shape: fail-open allow (user-never-blocked invariant).
//
// Within each shape:
//   - Non-read tools / non-cat commands / non-view commands: allow.
//   - Bypass conditions (small file, test file, config, offset/limit/view_range): allow.
//   - Large file: summarize and deny with summary.
//   - Summarize error: allow (fail open).
//
// Allow contracts differ by surface: Claude/legacy allow with {}; the Codex Bash
// arm allows with EMPTY STDOUT (nil), matching destructive_command_guard.py.
func HandleHook(input []byte) []byte {
	var hook hookInput
	if err := json.Unmarshal(input, &hook); err != nil {
		return allowJSON
	}

	switch hook.ToolName {
	case "Read":
		// Claude Code shape: file path at tool_input.file_path.
		return handleClaudeRead(hook.ToolInput)

	case "Bash":
		// Codex Bash shape: recognize only a bare `cat [--] <path>` read.
		return handleCodexBash(hook.ToolInput.Command)

	case "str_replace_based_edit_tool":
		// Legacy Codex shape: file path at tool_input.path, view indicated by command="view".
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

// shellMetaChars are characters whose presence signals shell syntax we refuse
// to reason about: pipes, chains, redirects, command substitution, globs,
// quoting, tilde/home expansion, history expansion. Any of them makes the
// command ambiguous, so handleCodexBash fails open.
const shellMetaChars = "|&;<>$`(){}[]*?~!'\"\\\n\r"

// simpleCatPath returns the single file path of a bare `cat <path>` or
// `cat -- <path>` command and true. Any other form — extra flags, multiple
// files, or shell metacharacters — returns ("", false) so the caller fails open.
func simpleCatPath(command string) (string, bool) {
	if strings.ContainsAny(command, shellMetaChars) {
		return "", false
	}
	fields := strings.Fields(command)
	switch {
	case len(fields) == 2 && fields[0] == "cat" && !strings.HasPrefix(fields[1], "-"):
		return fields[1], true
	case len(fields) == 3 && fields[0] == "cat" && fields[1] == "--":
		return fields[2], true
	default:
		return "", false
	}
}

// handleCodexBash intercepts a Codex Bash file read. It recognizes ONLY a bare
// `cat [--] <path>` of a large code file; every other command shape fails open.
//
// Because codex exec ignores a PreToolUse deny for trusted read commands, the
// interception ALLOWS the call and rewrites the command via updatedInput to emit
// the AST summary instead of the raw file (see codexRewriteResponse). The Codex
// Bash allow / fail-open contract is EMPTY STDOUT (nil) — NOT {}, which is
// unverified on the Bash surface and could be parsed as a malformed decision.
func handleCodexBash(command string) []byte {
	filePath, ok := simpleCatPath(command)
	if !ok {
		return nil
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil
	}
	cti := codesearch.ToolInput{
		FilePath: filePath,
		FileSize: info.Size(),
	}
	if codesearch.ShouldBypass(cti) {
		return nil
	}
	return summarizeAndRewriteCodex(filePath)
}

// summaryHeader labels the rewritten output so the model knows it received a
// structural summary in place of the raw file.
const summaryHeader = "[oro-search-hook] structural summary — raw file read suppressed to save context:\n\n"

// summarizeAndRewriteCodex summarizes filePath and returns a Codex allow+rewrite
// response: the `cat` is replaced with a `printf` that prints the summary, so the
// raw file is never read. On summarization error, returns nil (empty stdout =
// fail-open allow), letting the original `cat` run rather than emitting nothing.
func summarizeAndRewriteCodex(filePath string) []byte {
	summary, err := codesearch.SummarizeFile(filePath)
	if err != nil {
		return nil
	}
	var resp codexRewriteResponse
	resp.HookSpecificOutput.HookEventName = "PreToolUse"
	resp.HookSpecificOutput.PermissionDecision = "allow"
	resp.HookSpecificOutput.UpdatedInput.Command = "printf '%s' " + shellSingleQuote(summaryHeader+summary)
	out, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return out
}

// shellSingleQuote wraps s in single quotes for safe POSIX-shell interpolation,
// escaping any embedded single quotes as '\”. printf '%s' <quoted> then prints
// s verbatim regardless of newlines or % characters in the summary.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "oro-search-hook: %v\n", err)
		os.Exit(1)
	}
}

// writeOut writes data to w, logging any write error to stderr.
func writeOut(w io.Writer, data []byte) {
	if _, err := w.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "oro-search-hook: stdout write error: %v\n", err)
	}
}
