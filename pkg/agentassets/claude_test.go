package agentassets_test

import (
	"encoding/json"
	"testing"

	"oro/pkg/agentassets"
)

func TestClaudeGeneratorMatchesGlobalHooks(t *testing.T) {
	got, err := json.Marshal((agentassets.ClaudeGenerator{}).Hooks("~/.claude/hooks"))
	if err != nil {
		t.Fatalf("marshal generated hooks: %v", err)
	}

	const want = `{"PostToolUse":[{"matcher":"Read|WebFetch|Bash","hooks":[{"type":"command","command":"python3 ~/.claude/hooks/prompt_injection_guard.py"}]},{"matcher":"Edit|Write","hooks":[{"type":"command","command":"~/.claude/hooks/auto-format.sh"}]},{"matcher":"","hooks":[{"type":"command","command":"python3 ~/.claude/hooks/context_pruner.py"}]}],"PreCompact":[{"matcher":"","hooks":[{"type":"command","command":"python3 ~/.claude/hooks/pre_compact.py"}]}],"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"python3 ~/.claude/hooks/enforce_skills.py"},{"type":"command","command":"python3 ~/.claude/hooks/destructive_command_guard.py"}]}],"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"python3 ~/.claude/hooks/session_start_global.py"}]}],"Stop":[{"matcher":"","hooks":[{"type":"command","command":"~/.claude/hooks/stop-checklist.sh"}]}]}`

	if string(got) != want {
		t.Fatalf("Claude hooks JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}
