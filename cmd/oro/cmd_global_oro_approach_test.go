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
		"enforce_skills.py":         "# marker\n",
		"bd_create_notifier.py":     "# oro-specific - should not copy\n",
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
		"context_pruner.py", "stop-checklist.sh", "enforce_skills.py",
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
		"auto-format.sh":            "#!/bin/bash\n",
		"prompt_injection_guard.py": "# guard\n",
		"pre_compact.py":            "# compact\n",
		"context_pruner.py":         "# pruner\n",
		"stop-checklist.sh":         "#!/bin/bash\n",
		"enforce_skills.py":         "# marker\n",
		"session_start_global.py":   "# global session start\n",
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
		"auto-format.sh":            "#!/bin/bash\n",
		"prompt_injection_guard.py": "# guard\n",
		"pre_compact.py":            "# compact\n",
		"context_pruner.py":         "# pruner\n",
		"stop-checklist.sh":         "#!/bin/bash\n",
		"enforce_skills.py":         "# marker\n",
		"session_start_global.py":   "# global session start\n",
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
		runtime:       agentRuntimeCodex,
		oroSkillsDir:  srcSkills,
		oroHooksDir:   srcHooks,
		destSkillsDir: filepath.Join(tmp, "codex", "skills"),
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

func TestAgentAssetsSyncAllRuntimes(t *testing.T) {
	t.Parallel()

	TestAgentAssetsSyncSupportsClaudeAndCodex(t)
}
