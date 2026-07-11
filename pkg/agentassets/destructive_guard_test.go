package agentassets_test

import (
	"slices"
	"testing"

	"oro/pkg/agentassets"
)

func TestAgentAssetsIncludeDestructiveCommandGuard(t *testing.T) {
	const hooksDir = "/Users/alice/.oro/hooks"

	assertPreToolUseBashGuard(t, "Claude", (agentassets.ClaudeGenerator{}).Hooks(hooksDir))
}

func assertPreToolUseBashGuard(t *testing.T, runtime string, hooks map[string][]agentassets.HookGroup) {
	t.Helper()

	preToolUse, ok := hooks["PreToolUse"]
	if !ok {
		t.Fatalf("%s hooks missing PreToolUse: %#v", runtime, hooks)
	}

	bashIndex := slices.IndexFunc(preToolUse, func(group agentassets.HookGroup) bool {
		return group.Matcher == "Bash"
	})
	if bashIndex < 0 {
		t.Fatalf("%s PreToolUse hooks missing Bash matcher: %#v", runtime, preToolUse)
	}

	bashCommands := hookCommands(preToolUse[bashIndex].Hooks)
	if !slices.Contains(bashCommands, "python3 /Users/alice/.oro/hooks/destructive_command_guard.py") {
		t.Fatalf("%s PreToolUse Bash commands = %#v, want destructive guard", runtime, bashCommands)
	}
	if !slices.Contains(allPreToolUseCommands(preToolUse), "python3 /Users/alice/.oro/hooks/enforce_skills.py") {
		t.Fatalf("%s PreToolUse hooks must preserve enforce_skills.py wiring: %#v", runtime, preToolUse)
	}

	for _, group := range preToolUse {
		if group.Matcher != "Bash" && slices.Contains(hookCommands(group.Hooks), "python3 /Users/alice/.oro/hooks/destructive_command_guard.py") {
			t.Fatalf("%s destructive guard must only run for PreToolUse Bash, got group %#v", runtime, group)
		}
	}
}

func hookCommands(hooks []agentassets.HookSpec) []string {
	commands := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		commands = append(commands, hook.Command)
	}
	return commands
}

func allPreToolUseCommands(groups []agentassets.HookGroup) []string {
	commands := []string{}
	for _, group := range groups {
		commands = append(commands, hookCommands(group.Hooks)...)
	}
	return commands
}
