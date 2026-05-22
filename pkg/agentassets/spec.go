// Package agentassets generates runtime-specific agent assets from Oro's shared
// asset bundle.
package agentassets

import "io/fs"

// HookSpec describes one command hook in an agent settings file.
type HookSpec struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

// HookGroup describes a lifecycle matcher and the hooks that run for it.
type HookGroup struct {
	Matcher string     `json:"matcher"`
	Hooks   []HookSpec `json:"hooks"`
}

// RuleAsset describes one runtime rule file generated from Oro's asset bundle.
type RuleAsset struct {
	Source  string
	Target  string
	Content []byte
}

// ClaudeGenerator generates Claude-specific runtime assets.
type ClaudeGenerator struct {
	Source fs.FS
}

// RuleAssets returns the Oro-owned Claude rule files for the sync plan.
func (g ClaudeGenerator) RuleAssets() ([]RuleAsset, error) {
	return ClaudeRuleAssets(g.Source)
}

// Hooks returns the portable hooks wiring for ~/.claude/settings.json.
func (g ClaudeGenerator) Hooks(hooksDir string) map[string][]HookGroup {
	py := func(s string) string { return "python3 " + hooksDir + "/" + s }
	sh := func(s string) string { return hooksDir + "/" + s }

	return map[string][]HookGroup{
		"SessionStart": {{
			Matcher: "",
			Hooks:   []HookSpec{{Type: "command", Command: py("session_start_global.py")}},
		}},
		"PreCompact": {{
			Matcher: "",
			Hooks:   []HookSpec{{Type: "command", Command: py("pre_compact.py")}},
		}},
		"PreToolUse": {{
			Matcher: "",
			Hooks: []HookSpec{
				{Type: "command", Command: py("enforce_skills.py")},
				{Type: "command", Command: py("destructive_command_guard.py")},
			},
		}},
		"PostToolUse": {
			{Matcher: "Read|WebFetch|Bash", Hooks: []HookSpec{
				{Type: "command", Command: py("prompt_injection_guard.py")},
			}},
			{Matcher: "Edit|Write", Hooks: []HookSpec{
				{Type: "command", Command: sh("auto-format.sh")},
			}},
			{Matcher: "", Hooks: []HookSpec{
				{Type: "command", Command: py("context_pruner.py")},
			}},
		},
		"Stop": {{
			Matcher: "",
			Hooks:   []HookSpec{{Type: "command", Command: sh("stop-checklist.sh")}},
		}},
	}
}
