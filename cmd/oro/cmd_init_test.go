package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
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
		root.SetArgs([]string{"init", "testproj", "--project-root", tmpDir})

		if err := root.Execute(); err != nil {
			t.Fatalf("init command failed: %v", err)
		}

		// Verify .oro/config.yaml was created
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
		root.SetArgs([]string{"init", "emptyproj", "--project-root", tmpDir})

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
		root.SetArgs([]string{"init", "myproj", "--project-root", tmpDir})

		if err := root.Execute(); err != nil {
			t.Fatalf("first init failed: %v", err)
		}

		// Second run (idempotent — should not error)
		root2 := newRootCmd()
		var buf2 bytes.Buffer
		root2.SetOut(&buf2)
		root2.SetArgs([]string{"init", "myproj", "--project-root", tmpDir})

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
		root.SetArgs([]string{"init", "--project-root", tmpDir})

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

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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

	t.Run("adds .oro/ to .gitignore if not present", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		gitignorePath := filepath.Join(projectDir, ".gitignore")
		data, err := os.ReadFile(gitignorePath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf(".gitignore not created: %v", err)
		}

		if !strings.Contains(string(data), ".oro/") {
			t.Errorf(".gitignore should contain .oro/, got:\n%s", string(data))
		}
	})

	t.Run("skips .gitignore if .oro/ already present", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Pre-create .gitignore with .oro/ already in it
		existing := "node_modules/\n.oro/\n.env\n"
		if err := os.WriteFile(filepath.Join(projectDir, ".gitignore"), []byte(existing), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write .gitignore: %v", err)
		}

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(projectDir, ".gitignore")) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}

		// Should not duplicate .oro/
		if strings.Count(string(data), ".oro/") != 1 {
			t.Errorf(".gitignore should contain .oro/ exactly once, got:\n%s", string(data))
		}
	})

	t.Run("creates settings.json with absolute hook paths", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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
		if err := bootstrapProject(projectDir, "myproject", oroHome, assets); err != nil {
			t.Fatalf("first bootstrapProject failed: %v", err)
		}

		// Create a handoff file to verify it survives re-run
		handoffFile := filepath.Join(oroHome, "projects", "myproject", "handoffs", "session-001.yaml")
		if err := os.WriteFile(handoffFile, []byte("session: 001\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write handoff: %v", err)
		}

		// Second run (idempotent)
		if err := bootstrapProject(projectDir, "myproject", oroHome, assets); err != nil {
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

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
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

	t.Run("bootstrapProject adds .beads to gitignore", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		err := bootstrapProject(projectDir, "myproject", oroHome, assets)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(projectDir, ".gitignore")) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}

		if !strings.Contains(string(data), ".beads") {
			t.Errorf(".gitignore should contain .beads, got:\n%s", string(data))
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

func TestExtractAssets(t *testing.T) {
	assets := testAssets()
	dest := t.TempDir()

	if err := extractAssets(dest, assets); err != nil {
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

	if err := extractAssets(dest, assets); err != nil {
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
