package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

func TestUpdateGlobalSettingsTreatsJSONNullAsEmptyObject(t *testing.T) {
	tmp := t.TempDir()
	cfg := agentAssetsConfig{
		settingsPath: filepath.Join(tmp, "settings.json"),
	}
	if err := os.WriteFile(cfg.settingsPath, []byte("null"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := updateGlobalSettings(cfg, os.Stdout); err != nil {
		t.Fatalf("updateGlobalSettings: %v", err)
	}

	data, err := os.ReadFile(cfg.settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings JSON invalid: %v", err)
	}
	if _, ok := settings["hooks"]; !ok {
		t.Fatalf("settings missing hooks after null input: %s", string(data))
	}
}

func TestRunGlobalOroApproach_CopiesPortableHooks(t *testing.T) {
	tmp := t.TempDir()

	srcHooks := filepath.Join(tmp, "src", "hooks")
	dstHooks := filepath.Join(tmp, "dst", "hooks")

	makeHooksDir(t, srcHooks, map[string]string{
		"auto-format.sh":               "#!/bin/bash\n",
		"prompt_injection_guard.py":    "# guard\n",
		"pre_compact.py":               "# compact\n",
		"context_pruner.py":            "# pruner\n",
		"stop-checklist.sh":            "#!/bin/bash\necho '{}'\n",
		"enforce_skills.py":            "# marker\n",
		"destructive_command_guard.py": "# destructive guard\n",
		"bd_create_notifier.py":        "# oro-specific - should not copy\n",
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
		"context_pruner.py", "stop-checklist.sh", "enforce_skills.py", "destructive_command_guard.py",
	} {
		if _, err := os.Stat(filepath.Join(dstHooks, want)); err != nil {
			t.Errorf("expected hook %q to be copied, got: %v", want, err)
		}
	}

	// Oro-specific hooks should NOT be present
	if _, err := os.Stat(filepath.Join(dstHooks, "bd_create_notifier.py")); err == nil {
		t.Error("bd_create_notifier.py is oro-specific and should not have been copied")
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
		"enforce_skills.py":         "# marker\n",
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
	for _, event := range []string{"SessionStart", "PreCompact", "PreToolUse", "PostToolUse", "Stop"} {
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
		"enforce_worktree.py": "# oro-specific\n",
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
	for _, name := range []string{"enforce_worktree.py", "compact_trigger.py", "no_cd_guard.py"} {
		if _, err := os.Stat(filepath.Join(dstHooks, name)); err == nil {
			t.Errorf("oro-specific hook %q should not have been copied", name)
		}
	}
}

func TestRunGlobalOroApproach_RemovesStaleHooksFromDest(t *testing.T) {
	tmp := t.TempDir()

	srcHooks := filepath.Join(tmp, "src", "hooks")
	dstHooks := filepath.Join(tmp, "dst", "hooks")

	// Source has only auto-format.sh as the portable hook
	makeHooksDir(t, srcHooks, map[string]string{
		"auto-format.sh": "#!/bin/bash\n",
	})

	// Destination already has a stale hook from a previous run
	if err := os.MkdirAll(dstHooks, 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dstHooks, "old-hook.sh")
	if err := os.WriteFile(stale, []byte("#!/bin/bash\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	cfg := globalOroApproachConfig{
		oroSkillsDir:    filepath.Join(tmp, "src", "skills"),
		claudeSkillsDir: filepath.Join(tmp, "dst", "skills"),
		oroHooksDir:     srcHooks,
		claudeHooksDir:  dstHooks,
		settingsPath:    filepath.Join(tmp, "settings.json"),
		portableHooks:   []string{"auto-format.sh"},
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

	// old-hook.sh should have been removed from dst
	if _, err := os.Stat(stale); err == nil {
		t.Error("old-hook.sh should have been removed from dst hooks dir")
	}

	// auto-format.sh should still be present
	if _, err := os.Stat(filepath.Join(dstHooks, "auto-format.sh")); err != nil {
		t.Errorf("auto-format.sh should still be present: %v", err)
	}
}

func TestSkillsAreSymlinks(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	dstSkills := filepath.Join(tmp, "dst", "skills")

	makeSkillsDir(t, srcSkills, []string{"using-skills", "brainstorming"})

	cfg := globalOroApproachConfig{
		oroSkillsDir:    srcSkills,
		claudeSkillsDir: dstSkills,
		oroHooksDir:     filepath.Join(tmp, "src", "hooks"),
		claudeHooksDir:  filepath.Join(tmp, "dst", "hooks"),
		settingsPath:    filepath.Join(tmp, "settings.json"),
	}
	if err := os.MkdirAll(cfg.oroHooksDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, skill := range []string{"using-skills", "brainstorming"} {
		dst := filepath.Join(dstSkills, skill)
		info, err := os.Lstat(dst)
		if err != nil {
			t.Fatalf("lstat %s: %v", dst, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("skill %s should be a symlink, got mode %v", skill, info.Mode())
		}
		target, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("readlink %s: %v", dst, err)
		}
		want := filepath.Join(srcSkills, skill)
		if target != want {
			t.Errorf("skill %s symlink target = %q, want %q", skill, target, want)
		}
	}
}

func TestSkillsSymlink_ReplacesExistingDirOnRerun(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	dstSkills := filepath.Join(tmp, "dst", "skills")

	makeSkillsDir(t, srcSkills, []string{"using-skills"})

	// Pre-create dst as a plain directory (simulating an old copy-based run)
	if err := os.MkdirAll(filepath.Join(dstSkills, "using-skills"), 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := globalOroApproachConfig{
		oroSkillsDir:    srcSkills,
		claudeSkillsDir: dstSkills,
		oroHooksDir:     filepath.Join(tmp, "src", "hooks"),
		claudeHooksDir:  filepath.Join(tmp, "dst", "hooks"),
		settingsPath:    filepath.Join(tmp, "settings.json"),
	}
	if err := os.MkdirAll(cfg.oroHooksDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runGlobalOroApproach(cfg, os.Stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dst := filepath.Join(dstSkills, "using-skills")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat after rerun: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("using-skills should be a symlink after rerun, got mode %v", info.Mode())
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

func TestGlobalHooks_HasSessionStart(t *testing.T) {
	hooks := globalHooks("~/.claude/hooks")
	if _, ok := hooks["SessionStart"]; !ok {
		t.Error("globalHooks() must include a SessionStart event")
	}
	// Verify it references session_start_global.py
	sessionStart := hooks["SessionStart"]
	found := false
	for _, grp := range sessionStart {
		for _, entry := range grp.Hooks {
			if jsonContains(entry.Command, "session_start_global.py") {
				found = true
			}
		}
	}
	if !found {
		t.Error("SessionStart hook must reference session_start_global.py")
	}
}

func TestPortableHooks_IncludesSessionStartGlobal(t *testing.T) {
	found := false
	for _, name := range portableHooks {
		if name == "session_start_global.py" {
			found = true
			break
		}
	}
	if !found {
		t.Error("portableHooks must include session_start_global.py")
	}
}

func TestRunGlobalOroApproach_CopiesSessionStartGlobalHook(t *testing.T) {
	tmp := t.TempDir()
	srcHooks := filepath.Join(tmp, "src", "hooks")
	dstHooks := filepath.Join(tmp, "dst", "hooks")

	allHooks := map[string]string{
		"auto-format.sh":               "#!/bin/bash\n",
		"prompt_injection_guard.py":    "# guard\n",
		"pre_compact.py":               "# compact\n",
		"context_pruner.py":            "# pruner\n",
		"stop-checklist.sh":            "#!/bin/bash\n",
		"enforce_skills.py":            "# marker\n",
		"destructive_command_guard.py": "# destructive guard\n",
		"session_start_global.py":      "# global session start\n",
	}
	makeHooksDir(t, srcHooks, allHooks)

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

	if _, err := os.Stat(filepath.Join(dstHooks, "session_start_global.py")); err != nil {
		t.Errorf("session_start_global.py should be copied to dst hooks: %v", err)
	}
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

func TestAgentAssetsSyncSupportsClaudeAndCodex(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	srcHooks := filepath.Join(tmp, "src", "hooks")
	makeSkillsDir(t, srcSkills, []string{"using-skills", "brainstorming", "restart-oro"})
	makeHooksDir(t, srcHooks, map[string]string{
		"auto-format.sh":               "#!/bin/bash\n",
		"prompt_injection_guard.py":    "# guard\n",
		"pre_compact.py":               "# compact\n",
		"context_pruner.py":            "# pruner\n",
		"stop-checklist.sh":            "#!/bin/bash\n",
		"enforce_skills.py":            "# marker\n",
		"destructive_command_guard.py": "# destructive guard\n",
		"session_start_global.py":      "# global session start\n",
	})

	claudeSettings := filepath.Join(tmp, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettings, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	claudeCfg := agentAssetsConfig{
		runtime:       agentRuntimeClaude,
		oroSkillsDir:  srcSkills,
		oroHooksDir:   srcHooks,
		destSkillsDir: filepath.Join(tmp, "claude", "skills"),
		destHooksDir:  filepath.Join(tmp, "claude", "hooks"),
		settingsPath:  claudeSettings,
	}
	if err := runAgentAssetsSync(claudeCfg, os.Stdout); err != nil {
		t.Fatalf("Claude sync failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(claudeCfg.destSkillsDir, "using-skills", "SKILL.md")); err != nil {
		t.Fatalf("Claude runtime should receive synced skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeCfg.destHooksDir, "session_start_global.py")); err != nil {
		t.Fatalf("Claude runtime should receive hooks: %v", err)
	}

	codexCfg := agentAssetsConfig{
		runtime:         agentRuntimeCodex,
		oroSkillsDir:    srcSkills,
		oroHooksDir:     srcHooks,
		destSkillsDir:   filepath.Join(tmp, "codex", "skills"),
		codexPluginRoot: filepath.Join(tmp, "codex", "oro-marketplace"),
	}
	if err := runAgentAssetsSync(codexCfg, os.Stdout); err != nil {
		t.Fatalf("Codex sync failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(codexCfg.destSkillsDir, "using-skills", "SKILL.md")); err != nil {
		t.Fatalf("Codex runtime should receive synced skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(codexCfg.destSkillsDir, "restart-oro")); err == nil {
		t.Fatal("blocked skill restart-oro should not be synced to Codex")
	}
	if _, err := os.Stat(filepath.Join(tmp, "codex", "hooks")); err == nil {
		t.Fatal("Codex runtime should not require hooks to be installed")
	}
	assertFileContent(t, filepath.Join(codexCfg.codexPluginRoot, ".agents", "plugins", "marketplace.json"), `{
	"name": "oro-local",
	"interface": {
		"displayName": "Oro Local"
	},
	"plugins": [
		{
			"name": "oro",
			"source": {
				"source": "local",
				"path": "./plugins/oro"
			},
			"policy": {
				"installation": "AVAILABLE",
				"authentication": "ON_INSTALL"
			},
			"category": "Productivity"
		}
	]
}
`)
	assertFileContent(t, filepath.Join(codexCfg.codexPluginRoot, "plugins", "oro", "hooks.json"), `{
	"hooks": {
		"PostToolUse": [
			{
				"matcher": "Bash",
				"hooks": [
					{
						"type": "command",
						"command": "python3 `+filepath.ToSlash(srcHooks)+`/prompt_injection_guard.py"
					},
					{
						"type": "command",
						"command": "python3 `+filepath.ToSlash(srcHooks)+`/context_pruner.py"
					}
				]
			},
			{
				"matcher": "apply_patch",
				"hooks": [
					{
						"type": "command",
						"command": "`+filepath.ToSlash(srcHooks)+`/auto-format.sh"
					}
				]
			}
		],
		"PreToolUse": [
			{
				"matcher": "Bash",
				"hooks": [
					{
						"type": "command",
						"command": "python3 `+filepath.ToSlash(srcHooks)+`/enforce_skills.py"
					},
					{
						"type": "command",
						"command": "python3 `+filepath.ToSlash(srcHooks)+`/destructive_command_guard.py"
					}
				]
			},
			{
				"matcher": "str_replace_based_edit_tool",
				"hooks": [
					{
						"type": "command",
						"command": "`+filepath.ToSlash(srcHooks)+`/oro-search-hook"
					}
				]
			}
		],
		"SessionStart": [
			{
				"matcher": "",
				"hooks": [
					{
						"type": "command",
						"command": "python3 `+filepath.ToSlash(srcHooks)+`/session_start_global.py"
					}
				]
			}
		],
		"Stop": [
			{
				"matcher": "",
				"hooks": [
					{
						"type": "command",
						"command": "`+filepath.ToSlash(srcHooks)+`/stop-checklist.sh"
					}
				]
			}
		],
		"UserPromptSubmit": [
			{
				"matcher": "",
				"hooks": [
					{
						"type": "command",
						"command": "python3 `+filepath.ToSlash(srcHooks)+`/enforce_skills.py"
					}
				]
			}
		]
	}
}
`)

	globalAliasCfg := agentAssetsConfig{
		runtime:       agentRuntimeClaude,
		oroSkillsDir:  srcSkills,
		oroHooksDir:   srcHooks,
		destSkillsDir: filepath.Join(tmp, "alias", "skills"),
		destHooksDir:  filepath.Join(tmp, "alias", "hooks"),
		settingsPath:  filepath.Join(tmp, "alias", "settings.json"),
	}
	if err := os.MkdirAll(filepath.Dir(globalAliasCfg.settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalAliasCfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := runGlobalOroApproach(globalAliasCfg, os.Stdout); err != nil {
		t.Fatalf("global-skills compatibility alias failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalAliasCfg.destSkillsDir, "brainstorming", "SKILL.md")); err != nil {
		t.Fatalf("global-skills alias should still sync Claude-compatible skills: %v", err)
	}
}

func TestCodexSkillSyncInstallsPortableSkills(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	makeSkillsDir(t, srcSkills, []string{"using-skills", "brainstorming", "restart-oro"})

	cfg := agentAssetsConfig{
		runtime:       agentRuntimeCodex,
		oroSkillsDir:  srcSkills,
		destSkillsDir: filepath.Join(tmp, "codex", "skills"),
	}
	if err := runAgentAssetsSync(cfg, os.Stdout); err != nil {
		t.Fatalf("Codex sync failed: %v", err)
	}

	for _, want := range []string{"using-skills", "brainstorming"} {
		if _, err := os.Stat(filepath.Join(cfg.destSkillsDir, want, "SKILL.md")); err != nil {
			t.Fatalf("expected portable skill %q in Codex-visible location: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.destSkillsDir, "restart-oro")); err == nil {
		t.Fatal("restart-oro should not be installed as a portable Codex skill")
	}
}

func TestCodexPluginInstallIdempotent(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	srcHooks := filepath.Join(tmp, "src", "hooks")
	makeSkillsDir(t, srcSkills, []string{"using-skills"})
	makeHooksDir(t, srcHooks, map[string]string{
		"auto-format.sh":               "#!/bin/bash\n",
		"prompt_injection_guard.py":    "# guard\n",
		"context_pruner.py":            "# pruner\n",
		"stop-checklist.sh":            "#!/bin/bash\n",
		"enforce_skills.py":            "# marker\n",
		"destructive_command_guard.py": "# destructive guard\n",
		"session_start_global.py":      "# global session start\n",
	})

	cfg := agentAssetsConfig{
		runtime:         agentRuntimeCodex,
		oroSkillsDir:    srcSkills,
		oroHooksDir:     srcHooks,
		destSkillsDir:   filepath.Join(tmp, "codex", "skills"),
		codexPluginRoot: filepath.Join(tmp, "codex", "oro-marketplace"),
	}
	if err := runAgentAssetsSync(cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Codex sync failed: %v", err)
	}

	manifestPath := filepath.Join(cfg.codexPluginRoot, "plugins", "oro", ".codex-plugin", "plugin.json")
	var manifest map[string]any
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read plugin manifest: %v", err)
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("plugin manifest JSON invalid: %v", err)
	}
	for _, field := range []string{"name", "version", "description", "author", "homepage", "repository", "license", "interface"} {
		if _, ok := manifest[field]; !ok {
			t.Fatalf("plugin manifest missing required field %q: %s", field, manifestData)
		}
	}

	before := codexPackageSnapshot(t, cfg.codexPluginRoot)
	if err := runAgentAssetsSync(cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Codex sync failed: %v", err)
	}
	after := codexPackageSnapshot(t, cfg.codexPluginRoot)
	if before != after {
		t.Fatalf("Codex plugin install should be idempotent; before=%s after=%s", before, after)
	}
}

func TestCodexPluginInstallPreservesUserFiles(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	srcHooks := filepath.Join(tmp, "src", "hooks")
	makeSkillsDir(t, srcSkills, []string{"using-skills"})
	makeHooksDir(t, srcHooks, map[string]string{
		"auto-format.sh":               "#!/bin/bash\n",
		"prompt_injection_guard.py":    "# guard\n",
		"context_pruner.py":            "# pruner\n",
		"stop-checklist.sh":            "#!/bin/bash\n",
		"enforce_skills.py":            "# marker\n",
		"destructive_command_guard.py": "# destructive guard\n",
		"session_start_global.py":      "# global session start\n",
	})

	cfg := agentAssetsConfig{
		runtime:         agentRuntimeCodex,
		oroSkillsDir:    srcSkills,
		oroHooksDir:     srcHooks,
		destSkillsDir:   filepath.Join(tmp, "codex", "skills"),
		codexPluginRoot: filepath.Join(tmp, "codex", "oro-marketplace"),
	}

	staleManagedFile := filepath.Join(cfg.codexPluginRoot, "plugins", "oro", "old-hooks.json")
	userFile := filepath.Join(cfg.codexPluginRoot, "plugins", "oro", "README.local.md")
	if err := os.MkdirAll(filepath.Dir(staleManagedFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleManagedFile, []byte("stale generated file\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte("user notes\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := runAgentAssetsSync(cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("Codex sync failed: %v", err)
	}

	if _, err := os.Stat(staleManagedFile); !os.IsNotExist(err) {
		t.Fatalf("stale generated plugin file should be removed, stat err = %v", err)
	}
	assertFileContent(t, userFile, "user notes\n")
	if _, err := os.Stat(filepath.Join(cfg.codexPluginRoot, "plugins", "oro", ".codex-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin manifest should be installed at discovered local marketplace path: %v", err)
	}
}

func TestAgentAssetsSyncAllRuntimes(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "home", ".oro", ".claude", "skills")
	srcHooks := filepath.Join(tmp, "home", ".oro", "hooks")
	makeSkillsDir(t, srcSkills, []string{"using-skills", "brainstorming", "restart-oro"})
	makeHooksDir(t, srcHooks, map[string]string{
		"session_start_global.py": "# global session start\n",
	})
	if err := os.MkdirAll(filepath.Join(tmp, "home", ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "home", ".claude", "settings.json"), []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex-home"))

	root := newRootCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"agent-assets", "--runtime", "all"})

	if err := root.Execute(); err != nil {
		t.Fatalf("agent-assets --runtime all failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmp, "home", ".claude", "skills", "using-skills", "SKILL.md")); err != nil {
		t.Fatalf("runtime=all should install Claude skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "home", ".claude", "hooks", "session_start_global.py")); err != nil {
		t.Fatalf("runtime=all should install Claude hooks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "codex-home", "skills", "brainstorming", "SKILL.md")); err != nil {
		t.Fatalf("runtime=all should install Codex skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "codex-home", "skills", "restart-oro")); err == nil {
		t.Fatal("runtime=all should not install blocked skills for Codex")
	}
	if got := stderr.String(); jsonContains(got, "deprecated") {
		t.Fatalf("canonical agent-assets command should not print alias deprecation notice to stderr: %q", got)
	}
	if got := stdout.String(); jsonContains(got, "deprecated") {
		t.Fatalf("canonical agent-assets command should not print alias deprecation notice to stdout: %q", got)
	}
}

func TestGlobalSkillsRemainsAlias(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "home", ".oro", ".claude", "skills")
	srcHooks := filepath.Join(tmp, "home", ".oro", "hooks")
	makeSkillsDir(t, srcSkills, []string{"using-skills"})
	makeHooksDir(t, srcHooks, map[string]string{
		"session_start_global.py": "# global session start\n",
	})
	if err := os.MkdirAll(filepath.Join(tmp, "home", ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "home", ".claude", "settings.json"), []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex-home"))

	root := newRootCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"global-skills", "--runtime", "codex"})

	if err := root.Execute(); err != nil {
		t.Fatalf("global-skills alias failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmp, "home", ".claude", "skills", "using-skills", "SKILL.md")); err != nil {
		t.Fatalf("global-skills alias should sync Claude skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "home", ".claude", "hooks", "session_start_global.py")); err != nil {
		t.Fatalf("global-skills alias should sync Claude hooks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "codex-home", "skills", "using-skills")); err == nil {
		t.Fatal("global-skills alias should ignore --runtime codex and remain Claude-targeted")
	}
	if got := stderr.String(); !jsonContains(got, "deprecated") || !jsonContains(got, "oro agent-assets --runtime claude") {
		t.Fatalf("global-skills alias should print deprecation notice to stderr, got %q", got)
	}
	if got := stdout.String(); jsonContains(got, "deprecated") {
		t.Fatalf("global-skills alias should not print deprecation notice to stdout: %q", got)
	}
}

func TestAgentAssetsSyncInstallsClaudeRulesOnlyForClaudeRuntime(t *testing.T) {
	tmp := t.TempDir()
	srcSkills := filepath.Join(tmp, "src", "skills")
	srcHooks := filepath.Join(tmp, "src", "hooks")
	srcRules := filepath.Join(tmp, "src", "rules", "claude")
	makeSkillsDir(t, srcSkills, []string{"using-skills"})
	makeHooksDir(t, srcHooks, map[string]string{
		"session_start_global.py": "# global session start\n",
	})
	if err := os.MkdirAll(srcRules, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRules, "oro-worker.md"), []byte("# Worker\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	claudeSettings := filepath.Join(tmp, "claude-home", ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0o750); err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(filepath.Dir(claudeSettings), "rules")
	if err := os.MkdirAll(rulesDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "standards.md"), []byte("user rule\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettings, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}

	claudeCfg := agentAssetsConfig{
		runtime:         agentRuntimeClaude,
		oroSkillsDir:    srcSkills,
		oroHooksDir:     srcHooks,
		oroAssetsDir:    filepath.Join(tmp, "src"),
		destSkillsDir:   filepath.Join(tmp, "claude-home", ".claude", "skills"),
		destHooksDir:    filepath.Join(tmp, "claude-home", ".claude", "hooks"),
		claudeRulesRoot: filepath.Join(tmp, "claude-home"),
		settingsPath:    claudeSettings,
		portableHooks:   []string{"session_start_global.py"},
	}
	if err := runAgentAssetsSync(claudeCfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("Claude sync failed: %v", err)
	}
	if err := runAgentAssetsSync(claudeCfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("repeated Claude sync failed: %v", err)
	}
	assertFileContent(t, filepath.Join(tmp, "claude-home", ".claude", "rules", "oro-worker.md"), "# Worker\n")
	assertFileContent(t, filepath.Join(tmp, "claude-home", ".claude", "rules", "standards.md"), "user rule\n")

	codexCfg := agentAssetsConfig{
		runtime:       agentRuntimeCodex,
		oroSkillsDir:  srcSkills,
		oroHooksDir:   srcHooks,
		oroAssetsDir:  filepath.Join(tmp, "src"),
		destSkillsDir: filepath.Join(tmp, "codex-home", "skills"),
	}
	if err := runAgentAssetsSync(codexCfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("Codex sync failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "codex-home", ".claude", "rules", "oro-worker.md")); !os.IsNotExist(err) {
		t.Fatalf("Codex runtime should not install Claude rules, stat err = %v", err)
	}

	allCfg := claudeCfg
	allCfg.runtime = agentRuntimeAll
	allCfg.destSkillsDir = filepath.Join(tmp, "all-claude-home", ".claude", "skills")
	allCfg.destHooksDir = filepath.Join(tmp, "all-claude-home", ".claude", "hooks")
	allCfg.claudeRulesRoot = filepath.Join(tmp, "all-claude-home")
	allCfg.settingsPath = filepath.Join(tmp, "all-claude-home", ".claude", "settings.json")
	allCfg.codexSkillsDir = filepath.Join(tmp, "all-codex-home", "skills")
	if err := os.MkdirAll(filepath.Dir(allCfg.settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allCfg.settingsPath, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := runAgentAssetsSync(allCfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("all-runtime sync failed: %v", err)
	}
	assertFileContent(t, filepath.Join(tmp, "all-claude-home", ".claude", "rules", "oro-worker.md"), "# Worker\n")
	if _, err := os.Stat(filepath.Join(allCfg.codexSkillsDir, "using-skills", "SKILL.md")); err != nil {
		t.Fatalf("runtime=all should install Codex skills too: %v", err)
	}
}

func TestDefaultAgentAssetsConfigAllRuntime(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join("custom", "codex"))

	cfg := defaultAgentAssetsConfig(filepath.Join("home", "user"), agentRuntimeAll)

	if cfg.runtime != agentRuntimeAll {
		t.Fatalf("runtime = %q, want %q", cfg.runtime, agentRuntimeAll)
	}
	if cfg.oroAssetsDir != filepath.Join("home", "user", ".oro") {
		t.Fatalf("oroAssetsDir = %q", cfg.oroAssetsDir)
	}
	if cfg.destSkillsDir != filepath.Join("home", "user", ".claude", "skills") {
		t.Fatalf("Claude skills dir = %q", cfg.destSkillsDir)
	}
	if cfg.codexSkillsDir != filepath.Join("custom", "codex", "skills") {
		t.Fatalf("Codex skills dir = %q", cfg.codexSkillsDir)
	}
	if cfg.codexPluginRoot != filepath.Join("custom", "codex", "oro-marketplace") {
		t.Fatalf("Codex plugin root = %q", cfg.codexPluginRoot)
	}
	if cfg.claudeRulesRoot != filepath.Join("home", "user") {
		t.Fatalf("Claude rules root = %q", cfg.claudeRulesRoot)
	}
}

func TestInstallClaudeRuleAssetsHandlesNoopAndDiscoveryErrors(t *testing.T) {
	t.Run("empty config is no-op", func(t *testing.T) {
		if err := installClaudeRuleAssets(agentAssetsConfig{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("empty config should be a no-op: %v", err)
		}
	})

	t.Run("invalid bundled filename is wrapped", func(t *testing.T) {
		tmp := t.TempDir()
		rulesDir := filepath.Join(tmp, "rules", "claude")
		if err := os.MkdirAll(rulesDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rulesDir, "standards.md"), []byte("bad\n"), 0o640); err != nil {
			t.Fatal(err)
		}

		err := installClaudeRuleAssets(agentAssetsConfig{
			oroAssetsDir:    tmp,
			claudeRulesRoot: filepath.Join(tmp, "home"),
		}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("expected invalid rule asset to fail")
		}
		if !jsonContains(err.Error(), "generate Claude rule assets") {
			t.Fatalf("expected wrapped generator error, got %v", err)
		}
	})
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func codexPackageSnapshot(t *testing.T, root string) string {
	t.Helper()

	type fileSnapshot struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    uint32 `json:"mode"`
		ModTime int64  `json:"modTime"`
	}
	var files []fileSnapshot
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, fileSnapshot{
			Path:    filepath.ToSlash(rel),
			Content: string(mustReadFile(t, path)),
			Mode:    uint32(info.Mode().Perm()),
			ModTime: info.ModTime().UnixNano(),
		})
		return nil
	}); err != nil {
		t.Fatalf("snapshot codex package: %v", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	data, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return string(data)
}
