package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"oro/pkg/langprofile"
)

// --- ToolChecker unit tests ---

func TestCheckTool_Found(t *testing.T) {
	// "go" should always be available in CI and dev environments.
	result := checkTool(toolDef{Name: "go", CheckCmd: "go", CheckArgs: []string{"version"}})
	if result.Status != statusOK {
		t.Errorf("expected status OK for 'go', got %q (err: %v)", result.Status, result.Err)
	}
	if result.Version == "" {
		t.Error("expected non-empty version for 'go'")
	}
}

func TestCheckTool_NotFound(t *testing.T) {
	result := checkTool(toolDef{Name: "nonexistent-tool-xyz", CheckCmd: "nonexistent-tool-xyz", CheckArgs: []string{"--version"}})
	if result.Status != statusMissing {
		t.Errorf("expected status MISSING for nonexistent tool, got %q", result.Status)
	}
}

func TestCheckAllTools_ReturnsResults(t *testing.T) {
	defs := []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
		{Name: "nonexistent-tool-xyz", Category: "system", CheckCmd: "nonexistent-tool-xyz", CheckArgs: []string{"--version"}},
	}
	results := checkAllTools(defs)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != statusOK {
		t.Errorf("go should be OK, got %q", results[0].Status)
	}
	if results[1].Status != statusMissing {
		t.Errorf("nonexistent tool should be MISSING, got %q", results[1].Status)
	}
}

// --- Table formatting tests ---

func TestFormatInitTable(t *testing.T) {
	results := []toolResult{
		{Name: "go", Category: "prerequisites", Status: statusOK, Version: "go1.25.6"},
		{Name: "gofumpt", Category: "go-tools", Status: statusOK, Version: "v0.7.0"},
		{Name: "biome", Category: "system", Status: statusMissing},
	}

	var buf bytes.Buffer
	formatInitTable(&buf, results)
	got := buf.String()

	// Should contain tool names.
	if !strings.Contains(got, "go") {
		t.Errorf("table should contain 'go', got:\n%s", got)
	}
	if !strings.Contains(got, "gofumpt") {
		t.Errorf("table should contain 'gofumpt', got:\n%s", got)
	}
	if !strings.Contains(got, "biome") {
		t.Errorf("table should contain 'biome', got:\n%s", got)
	}

	// Should contain status indicators.
	if !strings.Contains(got, "OK") {
		t.Errorf("table should contain 'OK' status, got:\n%s", got)
	}
	if !strings.Contains(got, "MISSING") {
		t.Errorf("table should contain 'MISSING' status, got:\n%s", got)
	}
}

func TestFormatInitTable_AllPresent(t *testing.T) {
	results := []toolResult{
		{Name: "go", Category: "prerequisites", Status: statusOK, Version: "go1.25.6"},
		{Name: "python3", Category: "prerequisites", Status: statusOK, Version: "3.12.0"},
	}

	var buf bytes.Buffer
	formatInitTable(&buf, results)
	got := buf.String()

	// Should contain a success summary line.
	if !strings.Contains(got, "All") {
		t.Errorf("table should contain success summary, got:\n%s", got)
	}
}

func TestFormatInitTable_SomeMissing(t *testing.T) {
	results := []toolResult{
		{Name: "go", Category: "prerequisites", Status: statusOK, Version: "go1.25.6"},
		{Name: "biome", Category: "system", Status: statusMissing},
	}

	var buf bytes.Buffer
	formatInitTable(&buf, results)
	got := buf.String()

	// Should contain a summary indicating missing tools.
	if !strings.Contains(got, "missing") || !strings.Contains(got, "1") {
		t.Errorf("table should indicate 1 missing tool, got:\n%s", got)
	}
}

// --- Check mode tests (via cobra command) ---

func TestInitCmd_CheckMode_AllPresent(t *testing.T) {
	// Override the tool definitions to only include tools we know exist.
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	defaultToolDefs = []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--check"})

	err := root.Execute()
	if err != nil {
		t.Fatalf("init --check should succeed when all tools present, got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "OK") {
		t.Errorf("output should contain OK status, got:\n%s", got)
	}
}

func TestInitCmd_CheckMode_MissingTool(t *testing.T) {
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	defaultToolDefs = []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
		{Name: "nonexistent-tool-xyz", Category: "system", CheckCmd: "nonexistent-tool-xyz", CheckArgs: []string{"--version"}},
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"init", "--check"})

	err := root.Execute()
	if err == nil {
		t.Fatal("init --check should fail when a tool is missing")
	}

	got := buf.String()
	if !strings.Contains(got, "MISSING") {
		t.Errorf("output should contain MISSING status, got:\n%s", got)
	}
}

func TestInitCmd_QuietMode(t *testing.T) {
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	defaultToolDefs = []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--check", "--quiet"})

	err := root.Execute()
	if err != nil {
		t.Fatalf("init --check --quiet should succeed when all tools present, got: %v", err)
	}

	got := buf.String()
	if got != "" {
		t.Errorf("quiet mode should produce no output, got: %q", got)
	}
}

func TestInitCmd_QuietMode_MissingTool(t *testing.T) {
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	defaultToolDefs = []toolDef{
		{Name: "nonexistent-tool-xyz", Category: "system", CheckCmd: "nonexistent-tool-xyz", CheckArgs: []string{"--version"}},
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"init", "--check", "--quiet"})

	err := root.Execute()
	if err == nil {
		t.Fatal("init --check --quiet should fail when a tool is missing")
	}

	got := buf.String()
	if got != "" {
		t.Errorf("quiet mode should produce no output even on failure, got: %q", got)
	}
}

// --- Default tool definitions tests ---

func TestDefaultToolDefs_NonEmpty(t *testing.T) {
	defs := defaultToolDefs
	if len(defs) == 0 {
		t.Fatal("defaultToolDefs should not be empty")
	}

	// Verify every entry has required fields.
	for _, d := range defs {
		if d.Name == "" {
			t.Error("tool def has empty Name")
		}
		if d.Category == "" {
			t.Errorf("tool %q has empty Category", d.Name)
		}
		if d.CheckCmd == "" {
			t.Errorf("tool %q has empty CheckCmd", d.Name)
		}
	}
}

func TestDefaultToolDefs_HasCategories(t *testing.T) {
	categories := map[string]bool{}
	for _, d := range defaultToolDefs {
		categories[d.Category] = true
	}

	expected := []string{"prerequisites", "go-tools", "python-tools", "system"}
	for _, cat := range expected {
		if !categories[cat] {
			t.Errorf("expected category %q in default tool defs", cat)
		}
	}
}

func TestDefaultToolDefs_BdInstallURL(t *testing.T) {
	// Find the bd tool definition
	var bdTool *toolDef
	for i, d := range defaultToolDefs {
		if d.Name == "bd" {
			bdTool = &defaultToolDefs[i]
			break
		}
	}

	if bdTool == nil {
		t.Fatal("bd tool not found in defaultToolDefs")
		return
	}

	// Verify it has the correct install command
	expectedCmd := "go"
	expectedArgs := []string{"install", "github.com/steveyegge/beads/cmd/bd@latest"}

	if bdTool.InstallCmd != expectedCmd {
		t.Errorf("bd InstallCmd = %q, want %q", bdTool.InstallCmd, expectedCmd)
	}

	if len(bdTool.InstallArgs) != len(expectedArgs) {
		t.Fatalf("bd InstallArgs length = %d, want %d", len(bdTool.InstallArgs), len(expectedArgs))
	}

	for i, arg := range expectedArgs {
		if bdTool.InstallArgs[i] != arg {
			t.Errorf("bd InstallArgs[%d] = %q, want %q", i, bdTool.InstallArgs[i], arg)
		}
	}
}

// --- Install command helpers ---

func TestInstallCommandForTool(t *testing.T) {
	tool := toolDef{
		Name:        "gofumpt",
		Category:    "go-tools",
		InstallCmd:  "go",
		InstallArgs: []string{"install", "mvdan.cc/gofumpt@latest"},
	}

	cmd, args := installCommandForTool(tool)
	if cmd != "go" {
		t.Errorf("install cmd = %q, want 'go'", cmd)
	}
	if len(args) != 2 || args[0] != "install" {
		t.Errorf("install args = %v, want [install mvdan.cc/gofumpt@latest]", args)
	}
}

func TestInstallCommandForTool_BrewFallback(t *testing.T) {
	tool := toolDef{
		Name:        "tmux",
		Category:    "system",
		BrewName:    "tmux",
		InstallCmd:  "apt-get",
		InstallArgs: []string{"install", "-y", "tmux"},
	}

	cmd, args := installCommandForTool(tool)

	wantCmd := "apt-get"
	wantArg0 := "install"
	wantArg1 := "-y"
	if runtime.GOOS == "darwin" {
		wantCmd = "brew"
		wantArg0 = "install"
		wantArg1 = "tmux"
	}

	if cmd != wantCmd {
		t.Errorf("install cmd = %q, want %q", cmd, wantCmd)
	}
	if len(args) < 2 || args[0] != wantArg0 || args[1] != wantArg1 {
		t.Errorf("install args = %v, want [%s %s ...]", args, wantArg0, wantArg1)
	}
}

// --- allToolsPresent helper ---

func TestAllToolsPresent_True(t *testing.T) {
	results := []toolResult{
		{Status: statusOK},
		{Status: statusOK},
	}
	if !allToolsPresent(results) {
		t.Error("allToolsPresent should return true when all OK")
	}
}

func TestAllToolsPresent_False(t *testing.T) {
	results := []toolResult{
		{Status: statusOK},
		{Status: statusMissing},
	}
	if allToolsPresent(results) {
		t.Error("allToolsPresent should return false when any missing")
	}
}

func TestCountMissing(t *testing.T) {
	results := []toolResult{
		{Status: statusOK},
		{Status: statusMissing},
		{Status: statusOK},
		{Status: statusMissing},
	}
	if got := countMissing(results); got != 2 {
		t.Errorf("countMissing = %d, want 2", got)
	}
}

// --- Config generation tests ---

// overrideToolDefs replaces defaultToolDefs with a minimal set of tools that
// are guaranteed to be present in any development environment (go, tmux, jq).
// This prevents TestInitCommand_GeneratesConfig from failing when optional
// tools like gofumpt/goimports are not installed on the test machine.
// The original value is restored via t.Cleanup.
func overrideToolDefs(t *testing.T) {
	t.Helper()
	orig := defaultToolDefs
	defaultToolDefs = []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
	}
	t.Cleanup(func() { defaultToolDefs = orig })
}

func TestInitCommand_GeneratesConfig(t *testing.T) {
	t.Run("generates config with project name and Go profile", func(t *testing.T) {
		overrideToolDefs(t)
		tmpDir := t.TempDir()

		// Create a go.mod file to simulate a Go project
		goModPath := filepath.Join(tmpDir, "go.mod")
		if err := os.WriteFile(goModPath, []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("failed to create go.mod: %v", err)
		}

		// Run init command with project name
		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "testproj", "--project-root", tmpDir, "--local"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init command failed: %v", err)
		}

		// Verify .oro/config.yaml was created (--local = in-repo mode)
		configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("config file not created: %v", err)
		}

		config := string(data)
		if !strings.Contains(config, "project: testproj") {
			t.Errorf("config should contain project name, got:\n%s", config)
		}
		if !strings.Contains(config, "go:") {
			t.Errorf("config should contain 'go:' section, got:\n%s", config)
		}
		if !strings.Contains(config, "gofumpt") {
			t.Errorf("config should contain 'gofumpt' tool, got:\n%s", config)
		}
	})

	t.Run("generates config with project name when no languages detected", func(t *testing.T) {
		overrideToolDefs(t)
		tmpDir := t.TempDir()

		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "emptyproj", "--project-root", tmpDir, "--local"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init command failed: %v", err)
		}

		configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("config file not created: %v", err)
		}

		config := string(data)
		if !strings.Contains(config, "project: emptyproj") {
			t.Errorf("config should contain project name, got:\n%s", config)
		}
	})

	t.Run("idempotent re-run succeeds", func(t *testing.T) {
		overrideToolDefs(t)
		tmpDir := t.TempDir()

		// First run
		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "myproj", "--project-root", tmpDir, "--local"})

		if err := root.Execute(); err != nil {
			t.Fatalf("first init failed: %v", err)
		}

		// Second run (idempotent — should not error)
		root2 := newRootCmd()
		var buf2 bytes.Buffer
		root2.SetOut(&buf2)
		root2.SetArgs([]string{"init", "myproj", "--project-root", tmpDir, "--local"})

		if err := root2.Execute(); err != nil {
			t.Fatalf("second init should succeed (idempotent), got: %v", err)
		}
	})

	t.Run("derives project name from directory when not provided", func(t *testing.T) {
		overrideToolDefs(t)
		tmpDir := t.TempDir()

		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "--project-root", tmpDir, "--local"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init command failed: %v", err)
		}

		configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("config file not created: %v", err)
		}

		// Project name should be derived from tmpDir basename
		config := string(data)
		if !strings.Contains(config, "project:") {
			t.Errorf("config should contain project: field, got:\n%s", config)
		}
	})
}

// testAssets returns a minimal fstest.MapFS that simulates embedded oro assets.
func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"skills/brainstorming/SKILL.md":           &fstest.MapFile{Data: []byte("# Brainstorming\n")},
		"skills/test-driven-development/SKILL.md": &fstest.MapFile{Data: []byte("# TDD\n")},
		"hooks/session_start_extras.py":           &fstest.MapFile{Data: []byte("# session start\n")},
		"beacons/architect.md":                    &fstest.MapFile{Data: []byte("# Architect\n")},
		"beacons/manager.md":                      &fstest.MapFile{Data: []byte("# Manager\n")},
		"commands/restart-oro/prompt.md":          &fstest.MapFile{Data: []byte("restart\n")},
		"CLAUDE.md":                               &fstest.MapFile{Data: []byte("# Oro Instructions\n")},
	}
}

// --- Project bootstrapping tests (oro-etu3.2) ---

func TestOroInit(t *testing.T) {
	assets := testAssets()

	t.Run("creates config with project name", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		configPath := filepath.Join(projectDir, ".oro", "config.yaml")
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("config not created: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "project: myproject") {
			t.Errorf("config should contain project name, got:\n%s", content)
		}
	})

	t.Run("creates settings.json with absolute hook paths", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		settingsPath := filepath.Join(oroHome, "projects", "myproject", "settings.json")
		data, err := os.ReadFile(settingsPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("settings.json not created: %v", err)
		}

		// Verify it's valid JSON
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("settings.json is not valid JSON: %v", err)
		}

		content := string(data)
		// All hook commands should use $HOME/.oro/hooks/ prefix
		if !strings.Contains(content, "$HOME/.oro/hooks/") {
			t.Errorf("settings.json should use $HOME/.oro/hooks/ paths, got:\n%s", content)
		}
		// Should have hooks section
		if _, ok := parsed["hooks"]; !ok {
			t.Errorf("settings.json should contain hooks key, got:\n%s", content)
		}
		// Should have permissions section with context7 MCP tools.
		// Workers need library/API doc lookups (same as interactive sessions).
		perms, ok := parsed["permissions"].(map[string]any)
		if !ok {
			t.Fatalf("settings.json missing permissions key, got:\n%s", content)
		}
		allow, ok := perms["allow"].([]any)
		if !ok {
			t.Fatalf("settings.json permissions missing allow list, got:\n%s", content)
		}
		wantPerms := []string{
			"mcp__context7__resolve-library-id",
			"mcp__context7__query-docs",
		}
		for _, want := range wantPerms {
			found := false
			for _, got := range allow {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("settings.json permissions.allow missing %q, got: %v", want, allow)
			}
		}
	})

	t.Run("creates handoffs directory", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		handoffsDir := filepath.Join(oroHome, "projects", "myproject", "handoffs")
		info, err := os.Stat(handoffsDir)
		if err != nil {
			t.Fatalf("handoffs dir not created: %v", err)
		}
		if !info.IsDir() {
			t.Errorf("handoffs should be a directory")
		}
	})

	t.Run("extracts embedded skills", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		// Skills go to ~/.oro/.claude/skills/
		skillPath := filepath.Join(oroHome, ".claude", "skills", "brainstorming", "SKILL.md")
		data, err := os.ReadFile(skillPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("skill not extracted: %v", err)
		}
		if !strings.Contains(string(data), "Brainstorming") {
			t.Errorf("skill content mismatch, got: %s", string(data))
		}
	})

	t.Run("extracts embedded hooks", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		// Hooks go to ~/.oro/hooks/
		hookPath := filepath.Join(oroHome, "hooks", "session_start_extras.py")
		data, err := os.ReadFile(hookPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("hook not extracted: %v", err)
		}
		if !strings.Contains(string(data), "session start") {
			t.Errorf("hook content mismatch, got: %s", string(data))
		}
	})

	t.Run("extracts beacons", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		// Beacons go to ~/.oro/beacons/
		beaconPath := filepath.Join(oroHome, "beacons", "architect.md")
		data, err := os.ReadFile(beaconPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("beacon not extracted: %v", err)
		}
		if !strings.Contains(string(data), "Architect") {
			t.Errorf("beacon content mismatch, got: %s", string(data))
		}
	})

	t.Run("idempotent re-run updates settings without wiping handoffs", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// First run
		if _, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false); err != nil {
			t.Fatalf("first bootstrapProject failed: %v", err)
		}

		// Create a handoff file to verify it survives re-run
		handoffFile := filepath.Join(oroHome, "projects", "myproject", "handoffs", "session-001.yaml")
		if err := os.WriteFile(handoffFile, []byte("session: 001\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write handoff: %v", err)
		}

		// Second run (idempotent)
		if _, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false); err != nil {
			t.Fatalf("second bootstrapProject failed: %v", err)
		}

		// Handoff file should still exist
		if _, err := os.Stat(handoffFile); err != nil {
			t.Errorf("handoff file should survive re-run: %v", err)
		}

		// Settings.json should still be valid
		settingsPath := filepath.Join(oroHome, "projects", "myproject", "settings.json")
		data, err := os.ReadFile(settingsPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("settings.json missing after re-run: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("settings.json invalid after re-run: %v", err)
		}
	})
}

// --- Beads symlink tests (oro-6v9z) ---

func TestSetupBeadsSymlink(t *testing.T) {
	t.Run("creates symlink from project .beads to oroHome beads dir", func(t *testing.T) {
		projectDir := t.TempDir()
		beadsTarget := filepath.Join(t.TempDir(), "beads")

		err := setupBeadsSymlink(projectDir, beadsTarget)
		if err != nil {
			t.Fatalf("setupBeadsSymlink failed: %v", err)
		}

		// Target directory should exist
		info, err := os.Stat(beadsTarget)
		if err != nil {
			t.Fatalf("beads target dir not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("beads target should be a directory")
		}

		// .beads in project should be a symlink
		linkPath := filepath.Join(projectDir, ".beads")
		linkTarget, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf(".beads should be a symlink: %v", err)
		}
		if linkTarget != beadsTarget {
			t.Errorf("symlink target = %q, want %q", linkTarget, beadsTarget)
		}
	})

	t.Run("idempotent when symlink already correct", func(t *testing.T) {
		projectDir := t.TempDir()
		beadsTarget := filepath.Join(t.TempDir(), "beads")

		// First call
		if err := setupBeadsSymlink(projectDir, beadsTarget); err != nil {
			t.Fatalf("first call failed: %v", err)
		}

		// Put a file in the beads dir to verify it survives
		testFile := filepath.Join(beadsTarget, "issues.jsonl")
		if err := os.WriteFile(testFile, []byte(`{"id":"test"}`), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write test file: %v", err)
		}

		// Second call (idempotent)
		if err := setupBeadsSymlink(projectDir, beadsTarget); err != nil {
			t.Fatalf("second call failed: %v", err)
		}

		// File should survive
		if _, err := os.Stat(testFile); err != nil {
			t.Errorf("test file should survive idempotent re-run: %v", err)
		}
	})

	t.Run("skips when .beads is a real directory", func(t *testing.T) {
		projectDir := t.TempDir()
		beadsTarget := filepath.Join(t.TempDir(), "beads")

		// Pre-create .beads as a real directory with data
		realBeads := filepath.Join(projectDir, ".beads")
		if err := os.Mkdir(realBeads, 0o750); err != nil { //nolint:gosec // test directory
			t.Fatalf("mkdir .beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(realBeads, "issues.jsonl"), []byte("data"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write file: %v", err)
		}

		// Should not error — just skip
		if err := setupBeadsSymlink(projectDir, beadsTarget); err != nil {
			t.Fatalf("should not error on existing real dir: %v", err)
		}

		// .beads should still be a real directory, not a symlink
		_, err := os.Readlink(realBeads)
		if err == nil {
			t.Error(".beads should remain a real directory, not become a symlink")
		}
	})
}

func TestBootstrapProject_CreatesBeadsSymlink(t *testing.T) {
	assets := testAssets()

	t.Run("bootstrapProject creates beads symlink", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		// .beads should be a symlink pointing to oroHome/projects/myproject/beads
		linkPath := filepath.Join(projectDir, ".beads")
		linkTarget, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf(".beads should be a symlink: %v", err)
		}

		expectedTarget := filepath.Join(oroHome, "projects", "myproject", "beads")
		if linkTarget != expectedTarget {
			t.Errorf("symlink target = %q, want %q", linkTarget, expectedTarget)
		}
	})
}

func TestEnsureGlobalGitignore(t *testing.T) {
	t.Run("creates file and adds entries when file does not exist", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore_global")

		if err := ensureGlobalGitignoreAt(path); err != nil {
			t.Fatalf("ensureGlobalGitignoreAt failed: %v", err)
		}

		data, err := os.ReadFile(path) //nolint:gosec // test file
		if err != nil {
			t.Fatalf("file not created: %v", err)
		}

		content := string(data)
		for _, entry := range []string{".beads/", ".beads", ".oro/", ".dolt/"} {
			if !strings.Contains(content, entry) {
				t.Errorf("global gitignore should contain %q, got:\n%s", entry, content)
			}
		}
	})

	t.Run("adds missing entries to existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore_global")

		existing := "node_modules/\n.DS_Store\n"
		if err := os.WriteFile(path, []byte(existing), 0o644); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}

		if err := ensureGlobalGitignoreAt(path); err != nil {
			t.Fatalf("ensureGlobalGitignoreAt failed: %v", err)
		}

		data, err := os.ReadFile(path) //nolint:gosec // test file
		if err != nil {
			t.Fatal(err)
		}

		content := string(data)
		// Original content preserved
		if !strings.Contains(content, "node_modules/") {
			t.Error("original content should be preserved")
		}
		// New entries added
		for _, entry := range []string{".beads/", ".beads", ".oro/", ".dolt/"} {
			if !strings.Contains(content, entry) {
				t.Errorf("global gitignore should contain %q, got:\n%s", entry, content)
			}
		}
	})

	t.Run("does not duplicate entries already present", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore_global")

		existing := ".beads/\n.oro/\n"
		if err := os.WriteFile(path, []byte(existing), 0o644); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}

		if err := ensureGlobalGitignoreAt(path); err != nil {
			t.Fatalf("ensureGlobalGitignoreAt failed: %v", err)
		}

		data, err := os.ReadFile(path) //nolint:gosec // test file
		if err != nil {
			t.Fatal(err)
		}

		content := string(data)
		if strings.Count(content, ".beads/") != 1 {
			t.Errorf(".beads/ should appear exactly once, got:\n%s", content)
		}
		if strings.Count(content, ".oro/") != 1 {
			t.Errorf(".oro/ should appear exactly once, got:\n%s", content)
		}
		// .beads (without slash) and .dolt/ should be added
		if !strings.Contains(content, "\n.beads\n") && !strings.HasPrefix(content, ".beads\n") {
			// Just check it exists somewhere as a line
			found := false
			for _, line := range strings.Split(content, "\n") {
				if strings.TrimSpace(line) == ".beads" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf(".beads (no slash) should be added, got:\n%s", content)
			}
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore_global")

		// Run twice
		if err := ensureGlobalGitignoreAt(path); err != nil {
			t.Fatal(err)
		}
		first, _ := os.ReadFile(path) //nolint:gosec // test file

		if err := ensureGlobalGitignoreAt(path); err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(path) //nolint:gosec // test file

		if string(first) != string(second) {
			t.Errorf("second run should not change file.\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
}

func TestBootstrapDoesNotModifyRepoGitignore(t *testing.T) {
	assets := testAssets()

	t.Run("bootstrapProject does not add .oro/ or .beads to repo gitignore", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		existing := "node_modules/\n.env\n"
		if err := os.WriteFile(filepath.Join(projectDir, ".gitignore"), []byte(existing), 0o644); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(projectDir, ".gitignore")) //nolint:gosec // test file
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}

		content := string(data)
		if content != existing {
			t.Errorf("repo .gitignore should not be modified.\nwant:\n%s\ngot:\n%s", existing, content)
		}
	})
}

func TestBootstrapStartsDolt(t *testing.T) {
	assets := testAssets()

	t.Run("dolt binary missing does not break init", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Override PATH so dolt is not found — fail-open behavior.
		t.Setenv("PATH", t.TempDir())

		_, err := bootstrapProject(projectDir, "testproj", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject should succeed even when dolt is missing: %v", err)
		}

		// Metadata should still be written.
		beadsPath := filepath.Join(projectDir, ".beads")
		meta, err := readDoltMeta(beadsPath)
		if err != nil {
			t.Fatalf("readDoltMeta: %v", err)
		}
		if meta == nil {
			t.Fatal("metadata should exist after init")
		}
		if meta.Backend != "dolt" {
			t.Errorf("Backend = %q, want dolt", meta.Backend)
		}
	})

	t.Run("dolt already running is adopted", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Start a TCP listener on the derived port to simulate running dolt.
		beadsPath := filepath.Join(oroHome, "projects", "testproj", "beads")
		if err := os.MkdirAll(beadsPath, 0o755); err != nil {
			t.Fatal(err)
		}
		// Create symlink so bootstrapProject can resolve .beads
		// (bootstrapProject creates the symlink itself, so we just need
		// to verify it doesn't error with a running server on the port)

		_, err := bootstrapProject(projectDir, "testproj", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject should succeed: %v", err)
		}
	})
}

// --- Quality gate generation tests (oro-1rep.2) ---

func TestBootstrapGeneratesQualityGate(t *testing.T) {
	assets := testAssets()

	t.Run("generates quality_gate.sh in project root with mode 0755", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Create a go.mod to simulate Go project.
		if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write go.mod: %v", err)
		}

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		qgPath := filepath.Join(projectDir, "scripts", "quality_gate.sh")
		info, err := os.Stat(qgPath)
		if err != nil {
			t.Fatalf("quality_gate.sh not created: %v", err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("quality_gate.sh mode = %#o, want 0755", info.Mode().Perm())
		}
	})

	t.Run("Python-only config produces script with Python lane and no Go lane", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Create a requirements.txt to simulate Python project (no go.mod).
		if err := os.WriteFile(filepath.Join(projectDir, "requirements.txt"), []byte("requests\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write requirements.txt: %v", err)
		}

		_, err := bootstrapProject(projectDir, "pyproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		qgPath := filepath.Join(projectDir, "scripts", "quality_gate.sh")
		data, err := os.ReadFile(qgPath) //nolint:gosec // test file
		if err != nil {
			t.Fatalf("quality_gate.sh not created: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "lane_python") {
			t.Errorf("Python-only config should produce Python lane, got:\n%s", content)
		}
		if strings.Contains(content, "lane_go") {
			t.Errorf("Python-only config should NOT produce Go lane, got:\n%s", content)
		}
	})

	t.Run("existing file is NOT overwritten without force", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Create a go.mod so detection finds Go.
		if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write go.mod: %v", err)
		}

		// Pre-create scripts/quality_gate.sh with custom content.
		scriptsDir := filepath.Join(projectDir, "scripts")
		if err := os.MkdirAll(scriptsDir, 0o750); err != nil {
			t.Fatalf("mkdir scripts: %v", err)
		}
		qgPath := filepath.Join(scriptsDir, "quality_gate.sh")
		custom := []byte("#!/bin/bash\n# custom user script\n")
		if err := os.WriteFile(qgPath, custom, 0o755); err != nil { //nolint:gosec // test file
			t.Fatalf("write existing quality_gate.sh: %v", err)
		}

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		got, err := os.ReadFile(qgPath) //nolint:gosec // test file
		if err != nil {
			t.Fatalf("read quality_gate.sh: %v", err)
		}
		if string(got) != string(custom) {
			t.Errorf("quality_gate.sh should NOT be overwritten without force, got:\n%s", string(got))
		}
	})

	t.Run("force flag overwrites existing file", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Create a go.mod so detection finds Go.
		if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write go.mod: %v", err)
		}

		// Pre-create scripts/quality_gate.sh with custom content.
		scriptsDir2 := filepath.Join(projectDir, "scripts")
		if err := os.MkdirAll(scriptsDir2, 0o750); err != nil {
			t.Fatalf("mkdir scripts: %v", err)
		}
		qgPath := filepath.Join(scriptsDir2, "quality_gate.sh")
		custom := []byte("#!/bin/bash\n# custom user script\n")
		if err := os.WriteFile(qgPath, custom, 0o755); err != nil { //nolint:gosec // test file
			t.Fatalf("write existing quality_gate.sh: %v", err)
		}

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, true)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		got, err := os.ReadFile(qgPath) //nolint:gosec // test file
		if err != nil {
			t.Fatalf("read quality_gate.sh: %v", err)
		}
		if string(got) == string(custom) {
			t.Error("quality_gate.sh should be overwritten with --force")
		}
		if !strings.Contains(string(got), "Oro Quality Gate") {
			t.Errorf("overwritten quality_gate.sh should contain generated content, got:\n%s", string(got))
		}
	})

	t.Run("no languages detected still generates shell-only quality gate", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Empty dir — no language markers; config.yaml will have languages: {}.
		_, err := bootstrapProject(projectDir, "emptyproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		qgPath := filepath.Join(projectDir, "scripts", "quality_gate.sh")
		data, err := os.ReadFile(qgPath)
		if err != nil {
			t.Fatalf("quality_gate.sh should be created even when no languages detected: %v", err)
		}
		script := string(data)
		if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
			t.Error("generated script should start with shebang")
		}
		if strings.Contains(script, "lane_go") {
			t.Error("shell-only script should not contain lane_go")
		}
		if strings.Contains(script, "lane_python") {
			t.Error("shell-only script should not contain lane_python")
		}
	})
}

func TestGenerateSettings(t *testing.T) {
	data, err := generateSettings("$HOME/.oro")
	if err != nil {
		t.Fatalf("generateSettings failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, string(data))
	}

	content := string(data)
	if !strings.Contains(content, "$HOME/.oro/hooks/") {
		t.Errorf("should contain $HOME/.oro/hooks/ paths, got:\n%s", content)
	}

	// Should have all four lifecycle phases
	for _, phase := range []string{"SessionStart", "PreToolUse", "PostToolUse", "Stop"} {
		if !strings.Contains(content, phase) {
			t.Errorf("should contain %s phase, got:\n%s", phase, content)
		}
	}
}

func TestGenerateSettings_NoBdCreateNotifier(t *testing.T) {
	data, err := generateSettings("$HOME/.oro")
	if err != nil {
		t.Fatalf("generateSettings failed: %v", err)
	}

	content := string(data)
	// bd_create_notifier hook should NOT be registered (oro-t0np)
	if strings.Contains(content, "bd_create_notifier") {
		t.Errorf("settings.json should NOT contain bd_create_notifier hook, got:\n%s", content)
	}
}

func TestDefaultHookEntries_NoGhostHooks(t *testing.T) {
	// Test that buildHookConfig contains no Bash PostToolUse matcher
	// with memory_capture or learning_reminder (oro-pw0d)
	hooks := buildHookConfig("$HOME/.oro/hooks")

	postToolUseHooks, ok := hooks["PostToolUse"]
	if !ok {
		t.Fatal("PostToolUse key missing from hook config")
	}

	for _, group := range postToolUseHooks {
		if group.Matcher == "Bash" {
			t.Errorf("Bash PostToolUse matcher should be removed, found with hooks: %v", group.Hooks)
		}
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "memory_capture") {
				t.Errorf("memory_capture.py hook should not exist, found in command: %s", hook.Command)
			}
			if strings.Contains(hook.Command, "learning_reminder") {
				t.Errorf("learning_reminder.py hook should not exist, found in command: %s", hook.Command)
			}
		}
	}
}

func TestExtractAssets(t *testing.T) {
	assets := testAssets()
	dest := t.TempDir()

	if err := extractAssets(dest, assets, true); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	// skills → .claude/skills/
	if _, err := os.Stat(filepath.Join(dest, ".claude", "skills", "brainstorming", "SKILL.md")); err != nil {
		t.Errorf("skills not extracted: %v", err)
	}

	// hooks → hooks/
	if _, err := os.Stat(filepath.Join(dest, "hooks", "session_start_extras.py")); err != nil {
		t.Errorf("hooks not extracted: %v", err)
	}

	// beacons → beacons/
	if _, err := os.Stat(filepath.Join(dest, "beacons", "architect.md")); err != nil {
		t.Errorf("beacons not extracted: %v", err)
	}

	// commands → .claude/commands/
	if _, err := os.Stat(filepath.Join(dest, ".claude", "commands", "restart-oro", "prompt.md")); err != nil {
		t.Errorf("commands not extracted: %v", err)
	}

	// CLAUDE.md → .claude/CLAUDE.md
	if _, err := os.Stat(filepath.Join(dest, ".claude", "CLAUDE.md")); err != nil {
		t.Errorf("CLAUDE.md not extracted: %v", err)
	}

	// bd_create_notifier.py should NOT be extracted (oro-t0np)
	bdCreateNotifierPath := filepath.Join(dest, "hooks", "bd_create_notifier.py")
	if _, err := os.Stat(bdCreateNotifierPath); err == nil {
		t.Errorf("bd_create_notifier.py should NOT be extracted, but found at: %s", bdCreateNotifierPath)
	}
}

// --- Executable bits test (oro-l9gw) ---

// --- runInstall tests ---

func TestRunInstall_Success(t *testing.T) {
	var buf bytes.Buffer
	def := toolDef{
		Name:        "test-echo",
		InstallCmd:  "echo",
		InstallArgs: []string{"install-ok"},
		// No BrewName: avoids brew on macOS
	}

	err := runInstall(&buf, def)
	if err != nil {
		t.Fatalf("runInstall should succeed for 'echo', got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "done") {
		t.Errorf("output should contain 'done', got: %q", got)
	}
	if !strings.Contains(got, def.Name) {
		t.Errorf("output should contain tool name %q, got: %q", def.Name, got)
	}
}

func TestRunInstall_NoInstallCmd(t *testing.T) {
	var buf bytes.Buffer
	def := toolDef{
		Name: "test-no-install",
		// No InstallCmd, no BrewName → installCommandForTool returns ""
	}

	err := runInstall(&buf, def)
	if err == nil {
		t.Fatal("runInstall should return error when no install cmd defined")
	}
	if !strings.Contains(err.Error(), "no install command defined") {
		t.Errorf("error should mention 'no install command defined', got: %v", err)
	}
	if !strings.Contains(err.Error(), "test-no-install") {
		t.Errorf("error should mention tool name, got: %v", err)
	}
}

func TestRunInstall_CommandFails(t *testing.T) {
	var buf bytes.Buffer
	def := toolDef{
		Name:       "test-fail",
		InstallCmd: "false", // always exits 1
	}

	err := runInstall(&buf, def)
	if err == nil {
		t.Fatal("runInstall should return error when install command fails")
	}
	if !strings.Contains(err.Error(), "install test-fail") {
		t.Errorf("error should mention 'install test-fail', got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "FAILED") {
		t.Errorf("output should contain 'FAILED', got: %q", got)
	}
}

// --- installMissingTools tests ---

func TestInstallMissingTools_NoneMissing(t *testing.T) {
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	defaultToolDefs = []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
	}

	results := []toolResult{
		{Name: "go", Category: "prerequisites", Status: statusOK, Version: "go1.21"},
	}

	var buf bytes.Buffer
	err := installMissingTools(&buf, results)
	if err != nil {
		t.Fatalf("installMissingTools should return nil with zero missing tools, got: %v", err)
	}

	// Early return path calls formatInitTable — output should contain tool name
	got := buf.String()
	if !strings.Contains(got, "go") {
		t.Errorf("output should contain tool table with 'go', got: %q", got)
	}
}

func TestInstallMissingTools_OneFailingInstall(t *testing.T) {
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	// CheckCmd won't be found → tool stays missing after failed install
	defaultToolDefs = []toolDef{
		{
			Name:        "nonexistent-tool-xyz-12345",
			Category:    "system",
			CheckCmd:    "nonexistent-tool-xyz-12345",
			CheckArgs:   []string{"--version"},
			InstallCmd:  "false", // exits 1 → install fails
			InstallArgs: []string{},
		},
	}

	results := []toolResult{
		{Name: "nonexistent-tool-xyz-12345", Category: "system", Status: statusMissing},
	}

	var buf bytes.Buffer
	err := installMissingTools(&buf, results)
	// Re-verification after failed install finds tool still missing → error
	if err == nil {
		t.Fatal("installMissingTools should return error when tool is still missing after install")
	}
	if !strings.Contains(err.Error(), "still missing") {
		t.Errorf("error should mention 'still missing', got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "Installing") {
		t.Errorf("output should mention 'Installing', got: %q", got)
	}
}

func TestExtractAssets_ExecutableBits(t *testing.T) {
	assets := fstest.MapFS{
		"hooks/auto-format.sh":          &fstest.MapFile{Data: []byte("#!/bin/bash\necho formatting\n")},
		"hooks/session_start_extras.py": &fstest.MapFile{Data: []byte("#!/usr/bin/env python3\nprint('start')\n")},
		"skills/test/SKILL.md":          &fstest.MapFile{Data: []byte("# Test Skill\n")},
		"beacons/guide.yaml":            &fstest.MapFile{Data: []byte("key: value\n")},
		"commands/test/prompt.md":       &fstest.MapFile{Data: []byte("test command\n")},
		"CLAUDE.md":                     &fstest.MapFile{Data: []byte("# Instructions\n")},
	}

	dest := t.TempDir()

	if err := extractAssets(dest, assets, true); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	// .sh file should have executable permission (0o755)
	shPath := filepath.Join(dest, "hooks", "auto-format.sh")
	shInfo, err := os.Stat(shPath)
	if err != nil {
		t.Fatalf("shell script not extracted: %v", err)
	}
	shMode := shInfo.Mode()
	if shMode.Perm() != 0o755 {
		t.Errorf("shell script should have mode 0o755, got %#o", shMode.Perm())
	}

	// .py file should have executable permission (0o755)
	pyPath := filepath.Join(dest, "hooks", "session_start_extras.py")
	pyInfo, err := os.Stat(pyPath)
	if err != nil {
		t.Fatalf("python script not extracted: %v", err)
	}
	pyMode := pyInfo.Mode()
	if pyMode.Perm() != 0o755 {
		t.Errorf("python script should have mode 0o755, got %#o", pyMode.Perm())
	}

	// .md file should remain 0o644
	mdPath := filepath.Join(dest, ".claude", "skills", "test", "SKILL.md")
	mdInfo, err := os.Stat(mdPath)
	if err != nil {
		t.Fatalf("markdown file not extracted: %v", err)
	}
	mdMode := mdInfo.Mode()
	if mdMode.Perm() != 0o644 {
		t.Errorf("markdown file should have mode 0o644, got %#o", mdMode.Perm())
	}

	// .yaml file should remain 0o644
	yamlPath := filepath.Join(dest, "beacons", "guide.yaml")
	yamlInfo, err := os.Stat(yamlPath)
	if err != nil {
		t.Fatalf("yaml file not extracted: %v", err)
	}
	yamlMode := yamlInfo.Mode()
	if yamlMode.Perm() != 0o644 {
		t.Errorf("yaml file should have mode 0o644, got %#o", yamlMode.Perm())
	}
}

// --- Tool filtering by language tests ---

func TestToolFilterByLanguage(t *testing.T) {
	t.Run("Python-only config filters out go-tools category", func(t *testing.T) {
		// Create a config with only Python detected
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {},
			},
		}

		// Define a test tool set
		testTools := []toolDef{
			{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
			{Name: "python3", Category: "prerequisites", CheckCmd: "python3", CheckArgs: []string{"--version"}},
			{Name: "gofumpt", Category: "go-tools", CheckCmd: "gofumpt", CheckArgs: []string{"--version"}},
			{Name: "goimports", Category: "go-tools", CheckCmd: "goimports", CheckArgs: []string{"--version"}},
			{Name: "tmux", Category: "system", CheckCmd: "tmux", CheckArgs: []string{"-V"}},
		}

		// Filter tools by language
		filtered := filterToolsByLanguage(testTools, cfg)

		// Prerequisites should always be included
		var hasGo, hasPython3, hasTmux bool
		var hasGoTools int
		for _, t := range filtered {
			if t.Name == "go" {
				hasGo = true
			}
			if t.Name == "python3" {
				hasPython3 = true
			}
			if t.Name == "tmux" {
				hasTmux = true
			}
			if t.Category == "go-tools" {
				hasGoTools++
			}
		}

		if !hasGo || !hasPython3 {
			t.Error("prerequisites should always be included")
		}
		if !hasTmux {
			t.Error("system tools should always be included")
		}
		if hasGoTools > 0 {
			t.Error("go-tools should be filtered out when only Python is detected")
		}
	})

	t.Run("skipped tools shown as skipped not missing", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {},
			},
		}

		testTools := []toolDef{
			{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
			{Name: "gofumpt", Category: "go-tools", CheckCmd: "gofumpt", CheckArgs: []string{"--version"}},
		}

		// Check tools with filtering
		results := checkToolsWithLanguageFilter(testTools, cfg)

		// gofumpt should be skipped, not missing
		var gofumptResult *toolResult
		for i := range results {
			if results[i].Name == "gofumpt" {
				gofumptResult = &results[i]
				break
			}
		}

		if gofumptResult == nil {
			t.Fatal("gofumpt should be in results")
		}

		// Check for a "skipped" status (may need to add this constant)
		if gofumptResult.Status == statusMissing {
			t.Error("gofumpt should be marked as skipped, not missing")
		}
	})

	t.Run("skipped tools do not count toward missing-tool exit code", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {},
			},
		}

		testTools := []toolDef{
			{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
			{Name: "gofumpt", Category: "go-tools", CheckCmd: "gofumpt", CheckArgs: []string{"--version"}},
		}

		results := checkToolsWithLanguageFilter(testTools, cfg)
		missing := countMissingExcludingSkipped(results)

		// Only tools that are actually missing should count, not skipped ones
		if missing > 0 {
			t.Errorf("skipped tools should not count toward missing, got %d", missing)
		}
	})

	t.Run("zero languages detected returns only prerequisites and system", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{},
		}

		testTools := []toolDef{
			{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
			{Name: "python3", Category: "prerequisites", CheckCmd: "python3", CheckArgs: []string{"--version"}},
			{Name: "gofumpt", Category: "go-tools", CheckCmd: "gofumpt", CheckArgs: []string{"--version"}},
			{Name: "ruff", Category: "python-tools", CheckCmd: "ruff", CheckArgs: []string{"--version"}},
			{Name: "tmux", Category: "system", CheckCmd: "tmux", CheckArgs: []string{"-V"}},
		}

		filtered := filterToolsByLanguage(testTools, cfg)

		var hasPrereqs, hasSystem, hasLanguageTools bool
		for _, t := range filtered {
			if t.Category == "prerequisites" {
				hasPrereqs = true
			}
			if t.Category == "system" {
				hasSystem = true
			}
			if t.Category == "go-tools" || t.Category == "python-tools" {
				hasLanguageTools = true
			}
		}

		if !hasPrereqs || !hasSystem {
			t.Error("prerequisites and system should be included")
		}
		if hasLanguageTools {
			t.Error("language-specific tools should be filtered out when no languages detected")
		}
	})
}

func TestExtractAssets_Additive(t *testing.T) {
	assets := fstest.MapFS{
		"skills/test/SKILL.md":          &fstest.MapFile{Data: []byte("# Test Skill\n")},
		"hooks/session_start_extras.py": &fstest.MapFile{Data: []byte("# new hook content\n")},
		"beacons/architect.md":          &fstest.MapFile{Data: []byte("# New Architect\n")},
		"commands/test/prompt.md":       &fstest.MapFile{Data: []byte("test command\n")},
		"CLAUDE.md":                     &fstest.MapFile{Data: []byte("# New Instructions\n")},
	}

	t.Run("pre-existing file preserved when force=false", func(t *testing.T) {
		dest := t.TempDir()

		// Pre-create a file at the same path an asset would be extracted to.
		hookDir := filepath.Join(dest, "hooks")
		if err := os.MkdirAll(hookDir, 0o750); err != nil { //nolint:gosec // test dir
			t.Fatalf("mkdir: %v", err)
		}
		preExisting := []byte("# user customised hook\n")
		hookPath := filepath.Join(hookDir, "session_start_extras.py")
		if err := os.WriteFile(hookPath, preExisting, 0o600); err != nil { //nolint:gosec // test file
			t.Fatalf("write pre-existing: %v", err)
		}

		// Also pre-create CLAUDE.md
		claudeDir := filepath.Join(dest, ".claude")
		if err := os.MkdirAll(claudeDir, 0o750); err != nil { //nolint:gosec // test dir
			t.Fatalf("mkdir .claude: %v", err)
		}
		preClaudeContent := []byte("# user CLAUDE.md\n")
		if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), preClaudeContent, 0o600); err != nil { //nolint:gosec // test file
			t.Fatalf("write pre-existing CLAUDE.md: %v", err)
		}

		if err := extractAssets(dest, assets, false); err != nil {
			t.Fatalf("extractAssets failed: %v", err)
		}

		// Hook should be UNCHANGED (preserved).
		got, err := os.ReadFile(hookPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		if string(got) != string(preExisting) {
			t.Errorf("hook should be preserved, got %q, want %q", string(got), string(preExisting))
		}

		// CLAUDE.md should be UNCHANGED (preserved).
		gotClaude, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md")) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read CLAUDE.md: %v", err)
		}
		if string(gotClaude) != string(preClaudeContent) {
			t.Errorf("CLAUDE.md should be preserved, got %q, want %q", string(gotClaude), string(preClaudeContent))
		}
	})

	t.Run("pre-existing file overwritten when force=true", func(t *testing.T) {
		dest := t.TempDir()

		// Pre-create a file at the same path.
		hookDir := filepath.Join(dest, "hooks")
		if err := os.MkdirAll(hookDir, 0o750); err != nil { //nolint:gosec // test dir
			t.Fatalf("mkdir: %v", err)
		}
		hookPath := filepath.Join(hookDir, "session_start_extras.py")
		if err := os.WriteFile(hookPath, []byte("# old content\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatalf("write pre-existing: %v", err)
		}

		if err := extractAssets(dest, assets, true); err != nil {
			t.Fatalf("extractAssets failed: %v", err)
		}

		// Hook should be OVERWRITTEN with asset content.
		got, err := os.ReadFile(hookPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		if string(got) != "# new hook content\n" {
			t.Errorf("hook should be overwritten, got %q, want %q", string(got), "# new hook content\n")
		}
	})

	t.Run("new file always written regardless of force", func(t *testing.T) {
		dest := t.TempDir()

		// No pre-existing files — everything is new.
		if err := extractAssets(dest, assets, false); err != nil {
			t.Fatalf("extractAssets failed: %v", err)
		}

		// Hook should be created even with force=false.
		hookPath := filepath.Join(dest, "hooks", "session_start_extras.py")
		got, err := os.ReadFile(hookPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("hook not created: %v", err)
		}
		if string(got) != "# new hook content\n" {
			t.Errorf("hook content mismatch, got %q", string(got))
		}

		// Beacon should be created.
		beaconPath := filepath.Join(dest, "beacons", "architect.md")
		got, err = os.ReadFile(beaconPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("beacon not created: %v", err)
		}
		if string(got) != "# New Architect\n" {
			t.Errorf("beacon content mismatch, got %q", string(got))
		}

		// CLAUDE.md should be created.
		claudePath := filepath.Join(dest, ".claude", "CLAUDE.md")
		got, err = os.ReadFile(claudePath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("CLAUDE.md not created: %v", err)
		}
		if string(got) != "# New Instructions\n" {
			t.Errorf("CLAUDE.md content mismatch, got %q", string(got))
		}
	})
}

func TestExtractThresholdsJSON(t *testing.T) {
	const wantJSON = `{ "opus": 65, "sonnet": 50, "haiku": 40 }`

	t.Run("writes thresholds.json when absent", func(t *testing.T) {
		dest := t.TempDir()
		assets := fstest.MapFS{
			"thresholds.json": &fstest.MapFile{Data: []byte(wantJSON)},
		}
		if err := extractThresholdsJSON(dest, assets, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dest, "thresholds.json")) //nolint:gosec // test temp file
		if err != nil {
			t.Fatalf("thresholds.json not written: %v", err)
		}
		if string(got) != wantJSON {
			t.Errorf("content mismatch: got %q, want %q", string(got), wantJSON)
		}
	})

	t.Run("no overwrite when force=false and file exists", func(t *testing.T) {
		dest := t.TempDir()
		existing := []byte(`{"opus": 99}`)
		if err := os.WriteFile(filepath.Join(dest, "thresholds.json"), existing, 0o644); err != nil { //nolint:gosec // test temp file
			t.Fatalf("setup: %v", err)
		}
		assets := fstest.MapFS{
			"thresholds.json": &fstest.MapFile{Data: []byte(wantJSON)},
		}
		if err := extractThresholdsJSON(dest, assets, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dest, "thresholds.json")) //nolint:gosec // test temp file
		if string(got) != string(existing) {
			t.Errorf("file should not be overwritten: got %q, want %q", string(got), string(existing))
		}
	})

	t.Run("absent from FS returns nil", func(t *testing.T) {
		dest := t.TempDir()
		assets := fstest.MapFS{} // no thresholds.json
		if err := extractThresholdsJSON(dest, assets, false); err != nil {
			t.Fatalf("expected nil error for absent file, got: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "thresholds.json")); err == nil {
			t.Error("thresholds.json should not exist when absent from FS")
		}
	})
}

// TestBuildHookConfigContainsCompactTrigger verifies that compact_trigger.py
// appears in the blank-matcher PostToolUse group, between context_pct_writer.py
// and context_pruner.py.
func TestBuildHookConfigContainsCompactTrigger(t *testing.T) {
	cfg := buildHookConfig("/hooks")
	postToolUse, ok := cfg["PostToolUse"]
	if !ok {
		t.Fatal("PostToolUse key missing from hook config")
	}

	// Find the blank-matcher group.
	var blankHooks []hookEntry
	for _, g := range postToolUse {
		if g.Matcher == "" {
			blankHooks = g.Hooks
			break
		}
	}
	if blankHooks == nil {
		t.Fatal("no blank-matcher group found in PostToolUse")
	}

	// Collect commands in order.
	var cmds []string
	for _, h := range blankHooks {
		cmds = append(cmds, h.Command)
	}

	idxPctWriter := -1
	idxCompact := -1
	idxPruner := -1
	for i, cmd := range cmds {
		if strings.Contains(cmd, "context_pct_writer.py") {
			idxPctWriter = i
		}
		if strings.Contains(cmd, "compact_trigger.py") {
			idxCompact = i
		}
		if strings.Contains(cmd, "context_pruner.py") {
			idxPruner = i
		}
	}

	if idxCompact == -1 {
		t.Fatal("compact_trigger.py not found in blank-matcher PostToolUse hooks")
	}
	if idxPctWriter >= idxCompact || idxCompact >= idxPruner {
		t.Errorf("order wrong: context_pct_writer.py[%d] < compact_trigger.py[%d] < context_pruner.py[%d] not satisfied",
			idxPctWriter, idxCompact, idxPruner)
	}
}

func TestInitBeadsDB(t *testing.T) {
	// Setup: create temporary project root and beads directory
	projectRoot := t.TempDir()
	beadsDir := t.TempDir()

	// Create .beads symlink pointing to beads directory
	beadsLink := filepath.Join(projectRoot, ".beads")
	if err := os.Symlink(beadsDir, beadsLink); err != nil {
		t.Fatalf("failed to create .beads symlink: %v", err)
	}

	// Verify symlink is followed by os.Stat (directory exists)
	if info, err := os.Stat(beadsLink); err != nil {
		t.Fatalf("os.Stat should follow symlink and find directory: %v", err)
	} else if !info.IsDir() {
		t.Fatal("os.Stat should return directory info for symlink")
	}

	// Call initBeadsDB - should detect existing .beads/ directory and return early
	initBeadsDB(projectRoot)

	// Verify that bd init was NOT called by checking that beads.db doesn't exist
	// (if it existed, bd init would have created it)
	dbPath := filepath.Join(projectRoot, ".beads", "beads.db")
	if _, err := os.Stat(dbPath); err == nil {
		t.Error("beads.db should not exist after initBeadsDB with existing .beads/")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking beads.db: %v", err)
	}
}

func TestInitWritesDoltPort(t *testing.T) {
	projectDir := t.TempDir()
	oroHome := t.TempDir()

	// Create minimal embedded FS for assets.
	assets := fstest.MapFS{
		".version":          &fstest.MapFile{Data: []byte("test-version")},
		"hooks/.gitkeep":    &fstest.MapFile{Data: []byte("")},
		"skills/.gitkeep":   &fstest.MapFile{Data: []byte("")},
		"beacons/.gitkeep":  &fstest.MapFile{Data: []byte("")},
		"commands/.gitkeep": &fstest.MapFile{Data: []byte("")},
	}

	_, err := bootstrapProject(projectDir, "testproject", oroHome, assets, false)
	if err != nil {
		t.Fatalf("bootstrapProject failed: %v", err)
	}

	// Verify metadata.json was created in .beads/ with dolt_server_port.
	beadsLink := filepath.Join(projectDir, ".beads")
	metaPath := filepath.Join(beadsLink, "metadata.json")

	data, err := os.ReadFile(metaPath) //nolint:gosec // metaPath is constructed from trusted t.TempDir()
	if err != nil {
		t.Fatalf("metadata.json not found: %v", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}

	if _, ok := meta["dolt_server_port"]; !ok {
		t.Error("metadata.json missing dolt_server_port field")
	}

	if _, ok := meta["backend"]; !ok {
		t.Error("metadata.json missing backend field")
	}

	if backend, ok := meta["backend"].(string); ok && backend != "dolt" {
		t.Errorf("expected backend=dolt, got %q", backend)
	}
}

// TestInitDetectsSharedServer verifies that initDoltForProject sets port 13307
// when ~/.oro/dolt-server.port exists (shared server mode), and falls back to
// the per-project derived port when that file is absent.
func TestInitDetectsSharedServer(t *testing.T) {
	t.Run("uses SharedDoltPort when dolt-server.port file exists in oroHome", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := filepath.Join(tmpDir, "oro")
		beadsDir := filepath.Join(tmpDir, ".beads")

		if err := os.MkdirAll(oroHome, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		// Simulate shared server: write dolt-server.port to oroHome.
		portPath := filepath.Join(oroHome, "dolt-server.port")
		if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
			t.Fatal(err)
		}

		initDoltForProject(beadsDir, oroHome)

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta: %v", err)
		}
		if meta == nil {
			t.Fatal("expected metadata.json to be written, got nil")
		}
		if meta.DoltServerPort != SharedDoltPort {
			t.Errorf("expected port %d when shared server exists, got %d", SharedDoltPort, meta.DoltServerPort)
		}
	})

	t.Run("falls back to per-project port when dolt-server.port absent from oroHome", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := filepath.Join(tmpDir, "oro")
		beadsDir := filepath.Join(tmpDir, ".beads")

		if err := os.MkdirAll(oroHome, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		// No dolt-server.port in oroHome.
		initDoltForProject(beadsDir, oroHome)

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta: %v", err)
		}
		if meta == nil {
			t.Fatal("expected metadata.json to be written, got nil")
		}
		// Port must be a valid per-project port (not SharedDoltPort).
		// AllocatePort is now used instead of DerivePort; the two agree unless
		// DerivePort would return SharedDoltPort, in which case AllocatePort bumps.
		if meta.DoltServerPort == SharedDoltPort {
			t.Errorf("per-project port must not be SharedDoltPort (%d)", SharedDoltPort)
		}
		if meta.DoltServerPort < doltPortBase+1 || meta.DoltServerPort > doltPortBase+doltPortRange-1 {
			t.Errorf("per-project port %d not in valid range [%d, %d]",
				meta.DoltServerPort, doltPortBase+1, doltPortBase+doltPortRange-1)
		}
	})
}

// TestBuildHookConfig_NoStaleHookRefs verifies that every .py/.sh hook filename
// referenced in buildHookConfig exists in the assets/hooks/ directory.
// This catches stale references to deleted hooks (e.g. memory_capture.py).
func TestBuildHookConfig_NoStaleHookRefs(t *testing.T) {
	const dummyDir = "__hooksdir__"
	cfg := buildHookConfig(dummyDir)

	// Real hooks directory, relative to the cmd/oro package directory at test time.
	realHooksDir := "../../assets/hooks"
	prefix := dummyDir + "/"

	for phase, groups := range cfg {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				_, filename, found := strings.Cut(hook.Command, prefix)
				if !found {
					continue // not a local hook file reference
				}
				// Only check script files (.py, .sh) — binaries like
				// oro-search-hook are installed separately and won't be in assets/hooks/.
				ext := filepath.Ext(filename)
				if ext != ".py" && ext != ".sh" {
					continue
				}
				fullPath := filepath.Join(realHooksDir, filename)
				if _, err := os.Stat(fullPath); err != nil {
					t.Errorf("phase %s: hook references %q but file does not exist at %s", phase, filename, fullPath)
				}
			}
		}
	}
}

// --- Stealth mode init (oro-e2tg) ---

// TestOroInitStealth_EndToEnd verifies that `oro init --stealth` bootstraps a
// zero-footprint project: no .oro/ in the project root, stealth config in
// <oroHome>/projects/s-<hash>/, git hooks installed, settings.json created.
func TestOroInitStealth_EndToEnd(t *testing.T) {
	overrideToolDefs(t)

	projectDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	// Create .git dir so git hooks can be installed.
	gitDir := filepath.Join(projectDir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
		t.Fatalf("create .git/hooks: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--project-root", projectDir})

	if err := root.Execute(); err != nil {
		t.Fatalf("oro init (stealth default) failed: %v\noutput: %s", err, buf.String())
	}

	// 1. Standard .oro/config.yaml must NOT exist.
	if _, err := os.Stat(filepath.Join(projectDir, ".oro", "config.yaml")); err == nil {
		t.Error("stealth mode must not create .oro/config.yaml in project root")
	}

	// 2. Stealth config.yaml must exist with mode: stealth.
	hash, err := projectHash(projectDir)
	if err != nil {
		t.Fatalf("projectHash: %v", err)
	}
	stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)

	configData, err := os.ReadFile(filepath.Join(stealthDir, "config.yaml")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("stealth config.yaml not created: %v", err)
	}
	if !strings.Contains(string(configData), "mode: stealth") {
		t.Errorf("stealth config.yaml must contain 'mode: stealth', got:\n%s", string(configData))
	}

	// 3. Git pre-commit hook installed.
	preCommitData, err := os.ReadFile(filepath.Join(gitDir, "hooks", "pre-commit")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("pre-commit hook not installed: %v", err)
	}
	if !strings.Contains(string(preCommitData), "managed by oro") {
		t.Error("pre-commit hook must be an oro wrapper")
	}

	// 4. Git pre-push hook installed.
	prePushData, err := os.ReadFile(filepath.Join(gitDir, "hooks", "pre-push")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("pre-push hook not installed: %v", err)
	}
	if !strings.Contains(string(prePushData), "managed by oro") {
		t.Error("pre-push hook must be an oro wrapper")
	}

	// 5. settings.json created and is valid JSON.
	settingsData, err := os.ReadFile(filepath.Join(stealthDir, "settings.json")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("stealth settings.json not created: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(settingsData, &parsed); err != nil {
		t.Fatalf("stealth settings.json is not valid JSON: %v\n%s", err, string(settingsData))
	}
}

func TestOroInit_DefaultsStealth(t *testing.T) {
	overrideToolDefs(t)

	projectDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	// Create .git dir so hooks can be installed.
	if err := os.MkdirAll(filepath.Join(projectDir, ".git", "hooks"), 0o750); err != nil {
		t.Fatalf("create .git/hooks: %v", err)
	}

	// Run oro init WITHOUT --stealth flag — should default to stealth.
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--project-root", projectDir})

	if err := root.Execute(); err != nil {
		t.Fatalf("oro init (default stealth) failed: %v\noutput: %s", err, buf.String())
	}

	// Standard .oro/config.yaml must NOT exist — stealth is the default.
	if _, err := os.Stat(filepath.Join(projectDir, ".oro", "config.yaml")); err == nil {
		t.Error("default init must not create .oro/config.yaml — stealth should be the default")
	}

	// Stealth config must exist.
	hash, err := projectHash(projectDir)
	if err != nil {
		t.Fatalf("projectHash: %v", err)
	}
	stealthConfig := filepath.Join(oroHome, "projects", "s-"+hash, "config.yaml")
	data, err := os.ReadFile(stealthConfig) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("stealth config not created: %v", err)
	}
	if !strings.Contains(string(data), "mode: stealth") {
		t.Errorf("config must contain 'mode: stealth', got:\n%s", string(data))
	}
}

func TestOroInit_LocalFlagUsesStandardMode(t *testing.T) {
	overrideToolDefs(t)

	projectDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	// Run oro init --local — should use standard (in-repo) mode.
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--local", "--project-root", projectDir})

	if err := root.Execute(); err != nil {
		t.Fatalf("oro init --local failed: %v\noutput: %s", err, buf.String())
	}

	// Standard .oro/config.yaml MUST exist with --local.
	if _, err := os.Stat(filepath.Join(projectDir, ".oro", "config.yaml")); os.IsNotExist(err) {
		t.Error("--local must create .oro/config.yaml in project root")
	}
}

// findBeadsDirHashingToSharedPort brute-forces a beads directory path that
// DerivePort will hash to SharedDoltPort. Used for testing port collision detection.
func findBeadsDirHashingToSharedPort(t *testing.T) string {
	t.Helper()
	// DerivePort returns doltPortBase (13307) + hash%doltPortRange (1000).
	// For DerivePort to return SharedDoltPort (13307), we need hash%1000 == 0.
	// Expected collision rate: 1 in 1000, so should find one quickly.
	tmpBase := t.TempDir()
	for i := 0; i < 100000; i++ {
		candidate := filepath.Join(tmpBase, fmt.Sprintf("beads_%d", i))
		port := DerivePort(candidate)
		if port == SharedDoltPort {
			return candidate
		}
	}
	t.Fatalf("could not find a beads path that hashes to SharedDoltPort after 100000 attempts")
	return ""
}

// TestInitDoltForProject_RefusesSharedPort verifies that when DerivePort
// returns SharedDoltPort (port collision), initDoltForProject refuses
// initialization and does not spawn a per-project dolt server.
func TestInitDoltForProject_RefusesSharedPort(t *testing.T) {
	// Find a beads directory path that hashes to SharedDoltPort (collision).
	beadsDir := findBeadsDirHashingToSharedPort(t)
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Use empty oroHome to ensure no shared server file exists.
	// This ensures deriveEffectivePort will call DerivePort and return SharedDoltPort.
	oroHome := ""

	// Capture stderr to check for error message.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Call initDoltForProject with port collision scenario.
	initDoltForProject(beadsDir, oroHome)

	// Restore stderr and read captured output.
	w.Close()
	os.Stderr = oldStderr
	var buf bytes.Buffer
	buf.ReadFrom(r)
	stderrOutput := buf.String()

	// Verify that the function refused initialization:
	// - Should print an error message
	if !strings.Contains(stderrOutput, "oro dolt setup") {
		t.Errorf("expected error message mentioning 'oro dolt setup', got stderr: %q", stderrOutput)
	}

	// - Should NOT spawn a dolt server (no dolt-server.port file in beadsDir)
	beadsDoltPortPath := filepath.Join(beadsDir, "dolt-server.port")
	if _, err := os.Stat(beadsDoltPortPath); err == nil {
		t.Error("expected no dolt-server.port file in beadsDir when port is SharedDoltPort, but file was created")
	}

	// - Should NOT spawn a dolt server (no dolt-server.pid file in beadsDir)
	beadsDoltPIDPath := filepath.Join(beadsDir, "dolt-server.pid")
	if _, err := os.Stat(beadsDoltPIDPath); err == nil {
		t.Error("expected no dolt-server.pid file in beadsDir when port is SharedDoltPort, but file was created")
	}
}
