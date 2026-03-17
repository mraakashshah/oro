package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// makeSkillsDir creates a fake ~/.oro/.claude/skills/ tree with the given skill names.
func makeSkillsDir(t *testing.T, base string, skills []string) {
	t.Helper()
	for _, s := range skills {
		dir := filepath.Join(base, s)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+s+"\n"), 0o640); err != nil {
			t.Fatalf("write SKILL.md: %v", err)
		}
	}
}

// makeHooksDir creates a fake ~/.oro/hooks/ dir with the given hook files.
func makeHooksDir(t *testing.T, base string, hooks map[string]string) {
	t.Helper()
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", base, err)
	}
	for name, content := range hooks {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o640); err != nil {
			t.Fatalf("write hook %s: %v", name, err)
		}
	}
}

func TestRunGlobalOroApproach_CopiesSkillsExcludingBlocked(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	dstSkills := filepath.Join(tmp, "dst", "skills")

	allSkills := []string{
		"using-skills", "test-driven-development", "restart-oro", "watching-oro",
		"brainstorming", "git-commits",
	}
	makeSkillsDir(t, srcSkills, allSkills)

	cfg := globalOroApproachConfig{
		oroSkillsDir:    srcSkills,
		claudeSkillsDir: dstSkills,
		oroHooksDir:     filepath.Join(tmp, "src", "hooks"),
		claudeHooksDir:  filepath.Join(tmp, "dst", "hooks"),
		settingsPath:    filepath.Join(tmp, "settings.json"),
	}

	// Create empty source hooks dir so it doesn't fail
	if err := os.MkdirAll(cfg.oroHooksDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Create a minimal settings.json
	if err := os.WriteFile(cfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should copy non-blocked skills
	for _, want := range []string{"using-skills", "test-driven-development", "brainstorming", "git-commits"} {
		if _, err := os.Stat(filepath.Join(dstSkills, want, "SKILL.md")); err != nil {
			t.Errorf("expected skill %q to be copied, got: %v", want, err)
		}
	}

	// Should NOT copy blocked skills
	for _, blocked := range []string{"restart-oro", "watching-oro"} {
		if _, err := os.Stat(filepath.Join(dstSkills, blocked)); err == nil {
			t.Errorf("blocked skill %q should not have been copied", blocked)
		}
	}
}

func TestRunGlobalOroApproach_CopiesPortableHooks(t *testing.T) {
	tmp := t.TempDir()

	srcHooks := filepath.Join(tmp, "src", "hooks")
	dstHooks := filepath.Join(tmp, "dst", "hooks")

	makeHooksDir(t, srcHooks, map[string]string{
		"auto-format.sh":            "#!/bin/bash\n",
		"prompt_injection_guard.py": "# guard\n",
		"pre_compact.py":            "# compact\n",
		"context_pruner.py":         "# pruner\n",
		"stop-checklist.sh":         "#!/bin/bash\necho '{}'\n",
		"enforce-skills.sh":         "# marker\n",
		"memory_capture.py":         "# oro-specific - should not copy\n",
	})

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     srcHooks,
		claudeHooksDir:  dstHooks,
		settingsPath:    filepath.Join(tmp, "settings.json"),
	}

	if err := os.MkdirAll(cfg.oroSkillsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Portable hooks should be present
	for _, want := range []string{
		"auto-format.sh", "prompt_injection_guard.py", "pre_compact.py",
		"context_pruner.py", "stop-checklist.sh", "enforce-skills.sh",
	} {
		if _, err := os.Stat(filepath.Join(dstHooks, want)); err != nil {
			t.Errorf("expected hook %q to be copied, got: %v", want, err)
		}
	}

	// Oro-specific hooks should NOT be present
	if _, err := os.Stat(filepath.Join(dstHooks, "memory_capture.py")); err == nil {
		t.Error("memory_capture.py is oro-specific and should not have been copied")
	}
}

func TestRunGlobalOroApproach_FixesHardcodedOroPaths(t *testing.T) {
	tmp := t.TempDir()

	srcHooks := filepath.Join(tmp, "src", "hooks")
	dstHooks := filepath.Join(tmp, "dst", "hooks")

	makeHooksDir(t, srcHooks, map[string]string{
		"pre_compact.py":            `state_dir = Path.home() / ".oro" / "compaction-state"` + "\n",
		"context_pruner.py":         `LOG_FILE = str(Path.home() / ".oro" / "hooks" / "context_pruner.log")` + "\n",
		"auto-format.sh":            "#!/bin/bash\n",
		"prompt_injection_guard.py": "# no .oro refs\n",
		"stop-checklist.sh":         "#!/bin/bash\n",
		"enforce-skills.sh":         "# marker\n",
	})

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     srcHooks,
		claudeHooksDir:  dstHooks,
		settingsPath:    filepath.Join(tmp, "settings.json"),
	}
	if err := os.MkdirAll(cfg.oroSkillsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// pre_compact.py: .oro replaced with .claude
	preCompact, err := os.ReadFile(filepath.Join(dstHooks, "pre_compact.py"))
	if err != nil {
		t.Fatalf("read pre_compact.py: %v", err)
	}
	if got := string(preCompact); got != `state_dir = Path.home() / ".claude" / "compaction-state"`+"\n" {
		t.Errorf("pre_compact.py path not fixed: %q", got)
	}

	// context_pruner.py: .oro replaced with .claude
	pruner, err := os.ReadFile(filepath.Join(dstHooks, "context_pruner.py"))
	if err != nil {
		t.Fatalf("read context_pruner.py: %v", err)
	}
	if got := string(pruner); got != `LOG_FILE = str(Path.home() / ".claude" / "hooks" / "context_pruner.log")`+"\n" {
		t.Errorf("context_pruner.py path not fixed: %q", got)
	}

	// auto-format.sh: no .oro refs, should be unchanged
	autoFmt, err := os.ReadFile(filepath.Join(dstHooks, "auto-format.sh"))
	if err != nil {
		t.Fatalf("read auto-format.sh: %v", err)
	}
	if string(autoFmt) != "#!/bin/bash\n" {
		t.Errorf("auto-format.sh unexpectedly modified: %q", string(autoFmt))
	}
}

func TestRunGlobalOroApproach_UpdatesSettingsJSON(t *testing.T) {
	tmp := t.TempDir()

	existing := `{
	"model": "opus[1m]",
	"effortLevel": "high",
	"hooks": {
		"PreToolUse": [{"matcher":"","hooks":[{"type":"command","command":"old-hook.sh"}]}]
	}
}`
	settingsPath := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(existing), 0o640); err != nil {
		t.Fatal(err)
	}

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     filepath.Join(tmp, "src", "hooks"),
		claudeHooksDir:  filepath.Join(tmp, "dst", "hooks"),
		settingsPath:    settingsPath,
	}
	for _, d := range []string{cfg.oroSkillsDir, cfg.oroHooksDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	// Must be valid JSON
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}

	// Must preserve non-hooks keys
	for _, key := range []string{"model", "effortLevel"} {
		if _, ok := out[key]; !ok {
			t.Errorf("settings.json lost key %q", key)
		}
	}

	// hooks key must exist
	if _, ok := out["hooks"]; !ok {
		t.Fatal("settings.json missing hooks key")
	}

	// hooks must contain the expected events
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(out["hooks"], &hooks); err != nil {
		t.Fatalf("hooks not a map: %v", err)
	}
	for _, event := range []string{"PreCompact", "PreToolUse", "PostToolUse", "Stop"} {
		if _, ok := hooks[event]; !ok {
			t.Errorf("hooks missing event %q", event)
		}
	}

	// Old hook must be gone
	raw := string(out["hooks"])
	if contains := "old-hook.sh"; contains != "" {
		for _, b := range []byte(raw) {
			_ = b
		}
		// check via json string search
		if jsonContains(raw, "old-hook.sh") {
			t.Error("old oro-specific hook should have been replaced")
		}
	}

	// New hooks must reference ~/.claude/hooks/
	if !jsonContains(raw, "~/.claude/hooks/") {
		t.Error("hooks should reference ~/.claude/hooks/")
	}
}

func TestRunGlobalOroApproach_InvalidSettingsJSON(t *testing.T) {
	tmp := t.TempDir()

	settingsPath := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`not valid json`), 0o640); err != nil {
		t.Fatal(err)
	}

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     filepath.Join(tmp, "src", "hooks"),
		claudeHooksDir:  filepath.Join(tmp, "dst", "hooks"),
		settingsPath:    settingsPath,
	}
	for _, d := range []string{cfg.oroSkillsDir, cfg.oroHooksDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestRunGlobalOroApproach_SkipsOroSpecificHooks(t *testing.T) {
	tmp := t.TempDir()

	srcHooks := filepath.Join(tmp, "src", "hooks")
	dstHooks := filepath.Join(tmp, "dst", "hooks")

	// Only provide oro-specific hooks — none of the portable ones
	makeHooksDir(t, srcHooks, map[string]string{
		"architect_router.py": "# oro-specific\n",
		"compact_trigger.py":  "# oro-specific\n",
		"no_cd_guard.py":      "# oro-specific\n",
	})

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     srcHooks,
		claudeHooksDir:  dstHooks,
		settingsPath:    filepath.Join(tmp, "settings.json"),
	}
	if err := os.MkdirAll(cfg.oroSkillsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// None of the oro-specific hooks should appear in dst
	for _, name := range []string{"architect_router.py", "compact_trigger.py", "no_cd_guard.py"} {
		if _, err := os.Stat(filepath.Join(dstHooks, name)); err == nil {
			t.Errorf("oro-specific hook %q should not have been copied", name)
		}
	}
}

// jsonContains is a simple substring check on JSON text.
func jsonContains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

func TestRunGlobalOroApproach_SettingsJSON_MissingFile(t *testing.T) {
	tmp := t.TempDir()

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     filepath.Join(tmp, "src", "hooks"),
		claudeHooksDir:  filepath.Join(tmp, "dst", "hooks"),
		settingsPath:    filepath.Join(tmp, "nonexistent", "settings.json"),
	}
	for _, d := range []string{cfg.oroSkillsDir, cfg.oroHooksDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	err := runGlobalOroApproach(cfg, os.Stdout)
	if err == nil {
		t.Fatal("expected error for missing settings.json, got nil")
	}
}
