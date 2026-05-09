package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestSearchHookOnBothRuntimes verifies that oro-search-hook correctly handles
// both Claude and Codex hook input shapes from R3, including the extra fields
// (tool_use_id, turn_id, transcript_path) present in R3 Codex events.
//
// Three assertions per the acceptance criteria:
//  1. Claude R3 shape  → non-empty AST summary for a large Go file.
//  2. Codex R3 shape   → non-empty AST summary for the same Go file.
//  3. Unknown shapes   → fail-open allow ({}).
func TestSearchHookOnBothRuntimes(t *testing.T) {
	if _, err := exec.LookPath("ast-grep"); err != nil {
		t.Skip("ast-grep not installed, skipping")
	}

	largeGoFile := writeTempGoFile(t, 200)

	t.Run("claude_r3_file_path_extraction_produces_ast_summary", func(t *testing.T) {
		// R3 Claude fixture: standard Read shape + R3 extra fields that must
		// not interfere with tool_input.file_path extraction.
		fixture := map[string]any{
			"hook_type": "PreToolUse",
			"tool_name": "Read",
			"tool_input": map[string]any{
				"file_path": largeGoFile,
			},
			"tool_use_id":     "tu_abc123",
			"turn_id":         "turn-001",
			"transcript_path": nil,
		}
		assertR3DenyWithSummary(t, fixture, "package testpkg")
	})

	t.Run("codex_r3_path_extraction_produces_ast_summary", func(t *testing.T) {
		// R3 Codex fixture: str_replace_based_edit_tool with command=view and
		// tool_input.path. Extra R3 fields (tool_use_id, turn_id, transcript_path)
		// must be ignored; file path extracted from tool_input.path.
		fixture := map[string]any{
			"tool_name": "str_replace_based_edit_tool",
			"tool_input": map[string]any{
				"command": "view",
				"path":    largeGoFile,
			},
			"tool_use_id":     "tu_abc123",
			"turn_id":         "turn-001",
			"transcript_path": nil,
		}
		assertR3DenyWithSummary(t, fixture, "")
	})

	t.Run("unknown_shape_fails_open", func(t *testing.T) {
		// Unknown shapes (not matching Claude Read or Codex view) must allow,
		// even when R3 extra fields are present.
		unknownShapes := []string{
			`{}`,
			`{"event":"tool_call","action":"read","target":"/some/file.go"}`,
			`{"tool_name":"UnknownTool","tool_input":{"data":"x"},"tool_use_id":"tu_x","turn_id":"t-1"}`,
			`{"tool_name":"str_replace_based_edit_tool","tool_input":{"command":"create","path":"/tmp/f.go"},"tool_use_id":"tu_x"}`,
		}
		for _, shape := range unknownShapes {
			out := HandleHook([]byte(shape))
			var resp hookResponse
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Errorf("shape %q: expected valid allow JSON, got %q", shape, string(out))
				continue
			}
			if resp.PermissionDecision != "" {
				t.Errorf("shape %q: expected fail-open allow, got permissionDecision=%q",
					shape, resp.PermissionDecision)
			}
		}
	})
}

// assertR3DenyWithSummary marshals fixture, calls HandleHook, and asserts a
// deny response with a non-empty AST summary. wantSubstr, if non-empty, must
// appear in permissionDecisionReason.
func assertR3DenyWithSummary(t *testing.T, fixture map[string]any, wantSubstr string) {
	t.Helper()
	inputJSON, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("failed to marshal fixture: %v", err)
	}
	out := HandleHook(inputJSON)
	var resp hookResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("failed to parse response %q: %v", string(out), err)
	}
	if resp.PermissionDecision != "deny" {
		t.Errorf("expected permissionDecision=deny, got %q (output: %s)",
			resp.PermissionDecision, string(out))
	}
	if resp.PermissionDecisionReason == "" {
		t.Error("expected non-empty AST summary in permissionDecisionReason")
	}
	if wantSubstr != "" && !strings.Contains(resp.PermissionDecisionReason, wantSubstr) {
		t.Errorf("expected reason to contain %q, got %q",
			wantSubstr, resp.PermissionDecisionReason)
	}
}
