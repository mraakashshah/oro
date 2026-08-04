package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"oro/pkg/config"
	"oro/pkg/langprofile"
	"oro/pkg/protocol"
	"oro/pkg/worker"

	"github.com/spf13/cobra"
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

func TestInitCmd_ForceFlagOverwritesGeneratedQualityGate(t *testing.T) {
	origDefs := defaultToolDefs
	defer func() { defaultToolDefs = origDefs }()
	defaultToolDefs = []toolDef{
		{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
	}

	projectDir := t.TempDir()
	homeDir := t.TempDir()
	oroHome := filepath.Join(homeDir, ".oro")
	t.Setenv("HOME", homeDir)
	t.Setenv("ORO_HOME", oroHome)

	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatalf("write go.mod: %v", err)
	}

	scriptsDir := filepath.Join(projectDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o750); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	qgPath := filepath.Join(scriptsDir, "quality_gate.sh")
	const custom = "#!/bin/bash\n# custom user script\n"
	if err := os.WriteFile(qgPath, []byte(custom), 0o755); err != nil { //nolint:gosec // test file
		t.Fatalf("write existing quality_gate.sh: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"init", "--local", "--force", "--skip-wizard", "--project-root", projectDir})

	if err := root.Execute(); err != nil {
		t.Fatalf("init --force should succeed, got: %v\n%s", err, buf.String())
	}

	got, err := os.ReadFile(qgPath) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("read quality_gate.sh: %v", err)
	}
	if string(got) == custom {
		t.Fatal("quality_gate.sh should be overwritten when init is run with --force")
	}
	if !strings.Contains(string(got), "Oro Quality Gate") {
		t.Fatalf("quality_gate.sh should contain generated content, got:\n%s", string(got))
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

func TestDefaultToolDefs_NoBdRequirement(t *testing.T) {
	bdInstallModule := strings.Join([]string{"github.com/steveyegge/beads/cmd", "bd"}, "/")

	for _, d := range defaultToolDefs {
		if d.Name == "bd" {
			t.Fatal("defaultToolDefs should not require bd")
		}
		if d.CheckCmd == "bd" {
			t.Fatalf("tool %q should not check bd", d.Name)
		}
		for _, arg := range d.InstallArgs {
			if strings.Contains(arg, bdInstallModule) {
				t.Fatalf("tool %q should not install bd via %q", d.Name, arg)
			}
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

func TestInstallAgentBranchGuard(t *testing.T) {
	t.Run("missing git directory is left untouched", func(t *testing.T) {
		projectRoot := t.TempDir()

		installAgentBranchGuard(projectRoot)

		if _, err := os.Stat(filepath.Join(projectRoot, ".git")); !os.IsNotExist(err) {
			t.Fatalf("missing .git directory changed: %v", err)
		}
	})

	t.Run("existing git directory gets the fast guard", func(t *testing.T) {
		projectRoot := t.TempDir()
		if err := os.Mkdir(filepath.Join(projectRoot, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}

		stderr := captureInitStderr(t, func() {
			installAgentBranchGuard(projectRoot)
		})
		if stderr != "" {
			t.Fatalf("successful guard install wrote stderr: %q", stderr)
		}

		hookPath := filepath.Join(projectRoot, ".git", "hooks", "pre-push")
		data, err := os.ReadFile(hookPath) //nolint:gosec // test-created hook path
		if err != nil {
			t.Fatalf("read installed pre-push hook: %v", err)
		}
		for _, want := range []string{"managed by oro", "refs/heads/agent/*", "refs/heads/epic/*"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("installed pre-push hook missing %q:\n%s", want, data)
			}
		}
		for _, forbidden := range []string{"quality_gate.sh", "ORO_QG_CONTEXT", "ORO_PRE_PUSH_QG"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("installed pre-push hook contains full-gate token %q", forbidden)
			}
		}
	})

	t.Run("install failure warns and remains fail open", func(t *testing.T) {
		projectRoot := t.TempDir()
		gitDir := filepath.Join(projectRoot, ".git")
		if err := os.Mkdir(gitDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "hooks"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}

		stderr := captureInitStderr(t, func() {
			installAgentBranchGuard(projectRoot)
		})
		if !strings.Contains(stderr, "warning: install pre-push hook:") {
			t.Fatalf("install failure did not emit warning: %q", stderr)
		}
	})
}

func captureInitStderr(t *testing.T, fn func()) string {
	t.Helper()
	stderrPath := filepath.Join(t.TempDir(), "stderr")
	stderrFile, err := os.Create(stderrPath) //nolint:gosec // test-created capture path
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = stderrFile
	defer func() {
		os.Stderr = originalStderr
	}()

	fn()
	os.Stderr = originalStderr
	if err := stderrFile.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stderrPath) //nolint:gosec // test-created capture path
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
		"ORO_AGENT.md":                            &fstest.MapFile{Data: []byte("# Shared Oro Instructions\n")},
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

func TestBootstrapProject_DoesNotCreateLegacyBeadsSymlink(t *testing.T) {
	assets := testAssets()

	t.Run("bootstrapProject leaves project .beads absent", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject failed: %v", err)
		}

		if _, err := os.Lstat(filepath.Join(projectDir, ".beads")); !os.IsNotExist(err) {
			t.Fatalf("bootstrapProject created legacy .beads artifact; stat err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(oroHome, "projects", "myproject", "beads")); !os.IsNotExist(err) {
			t.Fatalf("bootstrapProject created legacy beads store under ORO_HOME; stat err=%v", err)
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
		for _, entry := range []string{".oro/"} {
			if !strings.Contains(content, entry) {
				t.Errorf("global gitignore should contain %q, got:\n%s", entry, content)
			}
		}
		for _, entry := range []string{".beads/", ".beads", ".dolt/"} {
			if strings.Contains(content, entry) {
				t.Errorf("global gitignore should not contain legacy entry %q, got:\n%s", entry, content)
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
		for _, entry := range []string{".oro/"} {
			if !strings.Contains(content, entry) {
				t.Errorf("global gitignore should contain %q, got:\n%s", entry, content)
			}
		}
		for _, entry := range []string{".beads/", ".beads", ".dolt/"} {
			if strings.Contains(content, entry) {
				t.Errorf("global gitignore should not contain legacy entry %q, got:\n%s", entry, content)
			}
		}
	})

	t.Run("does not duplicate entries already present", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore_global")

		existing := ".oro/\n"
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
		if strings.Count(content, ".oro/") != 1 {
			t.Errorf(".oro/ should appear exactly once, got:\n%s", content)
		}
		for _, entry := range []string{".beads/", ".beads", ".dolt/"} {
			if strings.Contains(content, entry) {
				t.Errorf("legacy entry %q should not be added, got:\n%s", entry, content)
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

func TestBootstrapDoesNotStartDolt(t *testing.T) {
	assets := testAssets()

	t.Run("dolt binary missing does not break init", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		// Override PATH so dolt is not found. Init should not invoke it.
		t.Setenv("PATH", t.TempDir())

		_, err := bootstrapProject(projectDir, "testproj", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject should succeed even when dolt is missing: %v", err)
		}

		assertNoDoltInitState(t, filepath.Join(projectDir, ".beads"))
	})

	t.Run("dolt state is not created", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()

		_, err := bootstrapProject(projectDir, "testproj", oroHome, assets, false)
		if err != nil {
			t.Fatalf("bootstrapProject should succeed: %v", err)
		}

		assertNoDoltInitState(t, filepath.Join(projectDir, ".beads"))
	})
}

func assertNoDoltInitState(t *testing.T, beadsPath string) {
	t.Helper()
	for _, name := range []string{"metadata.json", "dolt-server.port", "dolt-server.pid"} {
		if _, statErr := os.Stat(filepath.Join(beadsPath, name)); statErr == nil {
			t.Fatalf("init must not create Dolt state file %s", name)
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("check Dolt state file %s: %v", name, statErr)
		}
	}
}

func TestInitLocalNoDoltState(t *testing.T) {
	assets := testAssets()
	projectDir := t.TempDir()
	oroHome := t.TempDir()

	_, err := bootstrapProject(projectDir, "testproj", oroHome, assets, false)
	if err != nil {
		t.Fatalf("bootstrapProject failed: %v", err)
	}

	assertNoDoltInitState(t, filepath.Join(projectDir, ".beads"))
}

func TestInitLocalNoBdState(t *testing.T) {
	assets := testAssets()
	projectDir := t.TempDir()
	oroHome := t.TempDir()

	_, err := bootstrapProject(projectDir, "testproj", oroHome, assets, false)
	if err != nil {
		t.Fatalf("bootstrapProject failed: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(projectDir, ".beads", "beads.db")); statErr == nil {
		t.Fatal("local init must not create bd state file beads.db")
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("check bd state file: %v", statErr)
	}
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
		if !strings.HasPrefix(script, "#!/bin/sh\n# shellcheck shell=bash") {
			t.Error("generated script should start with sh Bash bootstrap")
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

func TestBootstrapPublishesOracleSettings(t *testing.T) {
	assets := testAssets()

	tests := []struct {
		name      string
		bootstrap func(t *testing.T, projectDir, oroHome string) string
	}{
		{
			name: "normal project",
			bootstrap: func(t *testing.T, projectDir, oroHome string) string {
				t.Helper()
				if _, err := bootstrapProject(projectDir, "oracle-project", oroHome, assets, false); err != nil {
					t.Fatalf("bootstrapProject: %v", err)
				}
				return filepath.Join(oroHome, "projects", "oracle-project")
			},
		},
		{
			name: "stealth project",
			bootstrap: func(t *testing.T, projectDir, oroHome string) string {
				t.Helper()
				if err := bootstrapStealthProject(projectDir, oroHome, assets, false); err != nil {
					t.Fatalf("bootstrapStealthProject: %v", err)
				}
				hash, err := projectHash(projectDir)
				if err != nil {
					t.Fatalf("projectHash: %v", err)
				}
				return filepath.Join(oroHome, "projects", "s-"+hash)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := t.TempDir()
			oroHome := t.TempDir()
			hookPath := filepath.Join(oroHome, "hooks", "oro-search-hook")
			if err := os.MkdirAll(filepath.Dir(hookPath), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			canonicalHookPath, err := worker.ValidateManagedOracleHook(hookPath)
			if err != nil {
				t.Fatalf("ValidateManagedOracleHook: %v", err)
			}

			projectSettingsDir := tt.bootstrap(t, projectDir, oroHome)
			oracleSettingsPath := filepath.Join(projectSettingsDir, "oracle-settings.json")
			data, err := os.ReadFile(oracleSettingsPath)
			if err != nil {
				t.Fatalf("read oracle settings: %v", err)
			}
			if got := tt.bootstrap(t, projectDir, oroHome); got != projectSettingsDir {
				t.Fatalf("second bootstrap settings dir = %q, want %q", got, projectSettingsDir)
			}
			secondData, err := os.ReadFile(oracleSettingsPath)
			if err != nil {
				t.Fatalf("read idempotent oracle settings: %v", err)
			}
			if !bytes.Equal(secondData, data) {
				t.Fatalf("oracle settings changed across identical bootstrap runs:\nfirst: %s\nsecond: %s", data, secondData)
			}

			var settings struct {
				Hooks map[string][]hookGroup `json:"hooks"`
			}
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatalf("unmarshal oracle settings: %v", err)
			}
			if len(settings.Hooks) != 2 {
				t.Fatalf("oracle hooks = %#v, want only SessionStart and PreToolUse", settings.Hooks)
			}
			assertOracleHookGroup(t, settings.Hooks["SessionStart"], "", canonicalHookPath)
			assertOracleHookGroup(t, settings.Hooks["PreToolUse"], "Read", canonicalHookPath)
		})
	}
}

func assertOracleHookGroup(t *testing.T, groups []hookGroup, matcher, hookPath string) {
	t.Helper()
	if len(groups) != 1 {
		t.Fatalf("groups = %#v, want one group", groups)
	}
	if groups[0].Matcher != matcher {
		t.Fatalf("matcher = %q, want %q", groups[0].Matcher, matcher)
	}
	if len(groups[0].Hooks) != 1 {
		t.Fatalf("hooks = %#v, want one hook", groups[0].Hooks)
	}
	if groups[0].Hooks[0] != (hookEntry{Type: "command", Command: hookPath}) {
		t.Fatalf("hook = %#v, want command %q", groups[0].Hooks[0], hookPath)
	}
}

func TestWriteOracleSettingsRejectsUntrustedHookWithoutReplacingProfile(t *testing.T) {
	projectDir := t.TempDir()
	profilePath := filepath.Join(projectDir, "oracle-settings.json")
	priorProfile := []byte("{\"prior\":true}\n")
	if err := os.WriteFile(profilePath, priorProfile, 0o644); err != nil {
		t.Fatal(err)
	}

	untrustedHook := filepath.Join(projectDir, "untrusted-hook")
	if err := os.WriteFile(untrustedHook, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeOracleSettings(projectDir, untrustedHook); err == nil {
		t.Fatal("writeOracleSettings succeeded with a non-executable hook")
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read prior profile: %v", err)
	}
	if !bytes.Equal(data, priorProfile) {
		t.Fatalf("profile = %q, want preserved %q", data, priorProfile)
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

func TestGenerateSettings_NoTaskCreateNotifier(t *testing.T) {
	data, err := generateSettings("$HOME/.oro")
	if err != nil {
		t.Fatalf("generateSettings failed: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "notify_manager_on_bead_create") {
		t.Errorf("settings.json should NOT contain notify_manager_on_bead_create hook, got:\n%s", content)
	}
}

func TestGenerateSettingsNoArchitectRouter(t *testing.T) {
	data, err := generateSettings("$HOME/.oro")
	if err != nil {
		t.Fatalf("generateSettings failed: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "architect_router") {
		t.Errorf("settings.json should NOT contain architect_router hook, got:\n%s", content)
	}
}

func TestDefaultHookEntries_NoGhostHooks(t *testing.T) {
	// Test that buildHookConfig contains no removed PostToolUse hooks
	// such as memory_capture or learning_reminder (oro-pw0d), and no
	// notify_manager_on_bead_create after the architect/notify teardown.
	hooks := buildHookConfig("$HOME/.oro/hooks")

	postToolUseHooks, ok := hooks["PostToolUse"]
	if !ok {
		t.Fatal("PostToolUse key missing from hook config")
	}

	for _, group := range postToolUseHooks {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "memory_capture") {
				t.Errorf("memory_capture.py hook should not exist, found in command: %s", hook.Command)
			}
			if strings.Contains(hook.Command, "learning_reminder") {
				t.Errorf("learning_reminder.py hook should not exist, found in command: %s", hook.Command)
			}
			if strings.Contains(hook.Command, "notify_manager_on_bead_create") {
				t.Errorf("notify_manager_on_bead_create hook should not exist, found in command: %s", hook.Command)
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

func TestInitInstallsTaskPrimaryBeacons(t *testing.T) {
	assertInstalledBeaconsAreTaskPrimary(t)
}

func TestInitInstallsBeaconTaskPrimaryAssets(t *testing.T) {
	assertInstalledBeaconsAreTaskPrimary(t)
}

func assertInstalledBeaconsAreTaskPrimary(t *testing.T) {
	t.Helper()

	oroHome := t.TempDir()
	embeddedAssets, err := fs.Sub(EmbeddedAssets, "_assets")
	if err != nil {
		t.Fatalf("sub embedded assets: %v", err)
	}

	if err := extractAssets(oroHome, embeddedAssets, true); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	primaryBeadCommands := []string{
		"oro bead ready",
		"oro bead create",
		"oro bead show",
		"oro bead close",
		"oro bead dep",
		"oro bead status",
		"oro bead blocked",
		"oro bead list",
	}

	for _, name := range []string{"manager.md"} {
		path := filepath.Join(oroHome, "beacons", name)
		data, err := os.ReadFile(path) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read installed beacon %s: %v", name, err)
		}
		text := string(data)
		if !strings.Contains(text, "oro task") {
			t.Fatalf("%s should teach task-primary commands, got:\n%s", name, text)
		}
		for _, command := range primaryBeadCommands {
			if strings.Contains(text, command) {
				t.Fatalf("%s should not teach primary command %q", name, command)
			}
		}
	}
}

func TestExtractAgentAssetsSharedSource(t *testing.T) {
	assets := fstest.MapFS{
		"ORO_AGENT.md":                   &fstest.MapFile{Data: []byte("# Shared Oro Instructions\nUse portable skills.\n")},
		"skills/brainstorming/SKILL.md":  &fstest.MapFile{Data: []byte("# Brainstorming\n")},
		"hooks/session_start_extras.py":  &fstest.MapFile{Data: []byte("# session start\n")},
		"beacons/architect.md":           &fstest.MapFile{Data: []byte("# Architect\n")},
		"commands/restart-oro/prompt.md": &fstest.MapFile{Data: []byte("restart\n")},
	}
	dest := t.TempDir()

	if err := extractAssets(dest, assets, true); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	sharedPath := filepath.Join(dest, "ORO_AGENT.md")
	sharedData, err := os.ReadFile(sharedPath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("ORO_AGENT.md not extracted: %v", err)
	}
	if !strings.Contains(string(sharedData), "Shared Oro Instructions") {
		t.Fatalf("ORO_AGENT.md content mismatch: %q", string(sharedData))
	}

	claudePath := filepath.Join(dest, ".claude", "CLAUDE.md")
	claudeData, err := os.ReadFile(claudePath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("CLAUDE.md compatibility wrapper not generated: %v", err)
	}
	claude := string(claudeData)
	if !strings.Contains(claude, "# Shared Oro Instructions") || !strings.Contains(claude, "../ORO_AGENT.md") {
		t.Fatalf("CLAUDE.md should be a wrapper generated from shared source, got %q", claude)
	}

	if _, err := os.Stat(filepath.Join(dest, ".claude", "skills", "brainstorming", "SKILL.md")); err != nil {
		t.Fatalf("skills should still extract through shared assets: %v", err)
	}
}

func TestExtractAssetsClaudeRules(t *testing.T) {
	assets := fstest.MapFS{
		"skills/.keep":               &fstest.MapFile{Data: []byte("")},
		"hooks/.keep":                &fstest.MapFile{Data: []byte("")},
		"beacons/.keep":              &fstest.MapFile{Data: []byte("")},
		"commands/.keep":             &fstest.MapFile{Data: []byte("")},
		"rules/claude/oro-worker.md": &fstest.MapFile{Data: []byte("# Worker\n")},
	}
	dest := t.TempDir()
	rulesDir := filepath.Join(dest, ".claude", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatalf("setup rules dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "standards.md"), []byte("user standards\n"), 0o644); err != nil {
		t.Fatalf("setup user rule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "oro-worker.md"), []byte("old worker\n"), 0o644); err != nil {
		t.Fatalf("setup stale oro rule: %v", err)
	}

	if err := extractAssets(dest, assets, false); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	rulePath := filepath.Join(dest, ".claude", "rules", "oro-worker.md")
	data, err := os.ReadFile(rulePath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("claude rule not extracted: %v", err)
	}
	if string(data) != "# Worker\n" {
		t.Fatalf("claude rule content = %q, want %q", data, "# Worker\n")
	}
	userData, err := os.ReadFile(filepath.Join(rulesDir, "standards.md")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("user rule not preserved: %v", err)
	}
	if string(userData) != "user standards\n" {
		t.Fatalf("user rule content = %q, want %q", userData, "user standards\n")
	}
}

func TestOroInitGeneratesSharedAndClaudeViews(t *testing.T) {
	assets := fstest.MapFS{
		"ORO_AGENT.md":                            &fstest.MapFile{Data: []byte("# Shared Oro Instructions\nUse portable skills.\n")},
		"skills/brainstorming/SKILL.md":           &fstest.MapFile{Data: []byte("# Brainstorming\n")},
		"skills/test-driven-development/SKILL.md": &fstest.MapFile{Data: []byte("# TDD\n")},
		"hooks/session_start_extras.py":           &fstest.MapFile{Data: []byte("# session start\n")},
		"beacons/architect.md":                    &fstest.MapFile{Data: []byte("# Architect\n")},
		"commands/restart-oro/prompt.md":          &fstest.MapFile{Data: []byte("restart\n")},
	}
	projectDir := t.TempDir()
	oroHome := t.TempDir()

	if _, err := bootstrapProject(projectDir, "myproject", oroHome, assets, false); err != nil {
		t.Fatalf("bootstrapProject failed: %v", err)
	}

	sharedPath := filepath.Join(oroHome, "ORO_AGENT.md")
	sharedData, err := os.ReadFile(sharedPath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("shared ORO_AGENT.md not created: %v", err)
	}
	if !strings.Contains(string(sharedData), "Use portable skills.") {
		t.Fatalf("shared ORO_AGENT.md content mismatch: %q", string(sharedData))
	}

	claudePath := filepath.Join(oroHome, ".claude", "CLAUDE.md")
	claudeData, err := os.ReadFile(claudePath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("Claude compatibility view not created: %v", err)
	}
	claude := string(claudeData)
	if !strings.Contains(claude, "# Shared Oro Instructions") || !strings.Contains(claude, "../ORO_AGENT.md") {
		t.Fatalf("Claude compatibility view should be a wrapper generated from shared instructions, got %q", claude)
	}

	if _, err := os.Stat(filepath.Join(oroHome, ".claude", "skills", "brainstorming", "SKILL.md")); err != nil {
		t.Fatalf("Claude compatibility skill view missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oroHome, "hooks", "session_start_extras.py")); err != nil {
		t.Fatalf("shared hooks should still be materialized: %v", err)
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

func TestBuildHookConfigCaptureHookOptIn(t *testing.T) {
	defaultCfg := buildHookConfig("/hooks")
	for _, group := range defaultCfg["PostToolUse"] {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "oro-capture-hook") {
				t.Fatalf("oro-capture-hook must be absent by default, got %#v", defaultCfg["PostToolUse"])
			}
		}
	}

	enabledCfg := buildHookConfigWithCapture("/hooks", true)
	var found bool
	for _, group := range enabledCfg["PostToolUse"] {
		for _, hook := range group.Hooks {
			if hook.Command == "/hooks/oro-capture-hook" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("oro-capture-hook missing when continuous capture enabled: %#v", enabledCfg["PostToolUse"])
	}
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

// TestBuildHookConfig_EnforceWorktreeWritesWired verifies that file-mutating
// tools (Write/Edit/NotebookEdit) route through the enforce_worktree_writes
// PreToolUse guard, so writes to the primary checkout are blocked per the
// all-code-in-worktrees policy.
func TestBuildHookConfig_EnforceWorktreeWritesWired(t *testing.T) {
	cfg := buildHookConfig("/hooks")
	found := false
	for _, group := range cfg["PreToolUse"] {
		if !strings.Contains(group.Matcher, "Write") || !strings.Contains(group.Matcher, "Edit") {
			continue
		}
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "enforce_worktree_writes.py") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("PreToolUse must run enforce_worktree_writes.py on a Write|Edit matcher")
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
	for _, forbidden := range []string{"ORO_QG_CONTEXT", "ORO_PRE_PUSH_QG", "quality_gate.sh"} {
		if strings.Contains(string(prePushData), forbidden) {
			t.Errorf("pre-push hook must leave authoritative full QG to GitHub; found %q", forbidden)
		}
	}
	stealthQG := filepath.Join(stealthDir, "quality_gate.sh")
	if strings.Contains(string(prePushData), stealthQG) {
		t.Errorf("pre-push hook must not run stealth quality gate %q, got:\n%s", stealthQG, string(prePushData))
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

func TestInitStealthNoDoltState(t *testing.T) {
	overrideToolDefs(t)

	projectDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--project-root", projectDir})

	if err := root.Execute(); err != nil {
		t.Fatalf("oro init (stealth default) failed: %v\noutput: %s", err, buf.String())
	}

	hash, err := projectHash(projectDir)
	if err != nil {
		t.Fatalf("projectHash: %v", err)
	}
	assertNoDoltInitState(t, filepath.Join(oroHome, "projects", "s-"+hash, "beads"))
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

// TestReviewPatternCandidateFilesIgnored verifies that review-pattern inbox
// files are git-ignored at the repo level, and that oroGitignoreEntries still
// carries .oro/ so target-project inits remain correct.
func TestReviewPatternCandidateFilesIgnored(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	data, err := os.ReadFile(gitignorePath) //nolint:gosec // test reads a known repo file
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	content := string(data)

	for _, entry := range []string{
		"/.oro/review-pattern-candidates.md",
		"/.oro/review-pattern-candidates.promoted.md",
	} {
		if !strings.Contains(content, entry) {
			t.Errorf("repo .gitignore must contain %q", entry)
		}
	}

	entries := oroGitignoreEntries()
	found := false
	for _, e := range entries {
		if e == ".oro/" {
			found = true
			break
		}
	}
	if !found {
		t.Error("oroGitignoreEntries() must still include \".oro/\" for target-project inits")
	}
}

// TestQualityGateRuntimeLockIgnored verifies that the repo-level ignore covers
// local quality-gate lock directories and stale lock archives.
func TestQualityGateRuntimeLockIgnored(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	data, err := os.ReadFile(gitignorePath) //nolint:gosec // test reads a known repo file
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, entry := range []string{
		"/.oro-quality-gate.lock*",
		"/.qg-local/",
		"/.qg-cache/",
	} {
		if !strings.Contains(string(data), entry) {
			t.Errorf("repo .gitignore must ignore quality-gate runtime artifact %q", entry)
		}
	}
}

// TestGateCacheDirectoriesIgnored asserts that every Go/lint cache directory
// name observed in worker worktrees is ignored. An unignored cache directory
// makes `git status --porcelain` non-empty, which the dispatcher reads as
// unpreserved work and quarantines the assignment as stale — that froze the
// factory on 2026-07-28. Substring checks against .gitignore cannot catch a
// new variant, so this asks git itself whether each path is ignored.
func TestGateCacheDirectoriesIgnored(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	// Names observed in real worktrees plus the per-bead suffixed forms.
	for _, dir := range []string{
		".gocache",
		".gocache-somebead",
		".golangci-cache",
		".golangci-cache-somebead",
		".golangci-lint-cache",
		".task-gocache",
		".qg-local",
		".qg-cache",
		".qg-go-cache",
		".qg-lint-cache",
		".qg-golangci-cache",
		// "affected" variants: the gate names caches after the lane that owns
		// them, so new lanes introduce new names. Observed in .worktrees/oro-3eax.
		".qg-affected-golangci-cache",
		".qg-affected-go-cache",
		// Randomized per-run scratch dirs with NO "cache" in the name. The gate
		// mints these per lane+invocation, so the family is unbounded and only a
		// prefix pattern can cover it. Observed dirtying .worktrees/oro-3eax and
		// .worktrees/oro-qg-incident-375, which raised
		// progress_timeout_recovery_blocked quarantine 449.
		".qg-lint-1QyLYx",
		".qg-verify-lint-main-IZ8Rzy",
		".qg-incident375-lint-NV2cq3",
		".qg-recheck-go-UQiLDM",
		".qg-final-lint-cache-W8VY44",
		// Nested, undotted forms: the directory name carries no leading dot,
		// so leading-dot patterns miss it entirely.
		".oro/golangci-cache",
		".oro/gocache",
	} {
		cmd := exec.Command("git", "check-ignore", "-q", filepath.Join(dir, "probe"))
		cmd.Dir = repoRoot
		if err := cmd.Run(); err != nil {
			t.Errorf("gate cache directory %q is NOT gitignored: an unignored cache dir dirties the worktree and triggers stale_active_assignment quarantines", dir)
		}
	}

	// FILE artifacts, checked as paths in their own right. The directory loop
	// above appends "probe", so it would pass these via a directory-only
	// pattern and never exercise the file case. .qg-incident-588-coverage.out
	// leaked exactly this way and dirtied .worktrees/oro-qg-incident-588.
	for _, file := range []string{
		".qg-incident-588-coverage.out",
		".qg-coverage.out",
	} {
		cmd := exec.Command("git", "check-ignore", "-q", file)
		cmd.Dir = repoRoot
		if err := cmd.Run(); err != nil {
			t.Errorf("gate cache file %q is NOT gitignored: an unignored artifact dirties the worktree and triggers stale_active_assignment quarantines", file)
		}
	}
}

// TestInitEmitsBothClaudeAndAgentsMD verifies that extractAssets materialises
// both .claude/CLAUDE.md and AGENTS.md from the ORO_AGENT.md shared source.
func TestInitEmitsBothClaudeAndAgentsMD(t *testing.T) {
	assets := fstest.MapFS{
		"ORO_AGENT.md":                 &fstest.MapFile{Data: []byte("# Oro Agent Instructions\nportable content\n")},
		"skills/using-skills/SKILL.md": &fstest.MapFile{Data: []byte("# skill\n")},
		"hooks/placeholder.sh":         &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"beacons/placeholder.md":       &fstest.MapFile{Data: []byte("# beacon\n")},
		"commands/placeholder/cmd.md":  &fstest.MapFile{Data: []byte("# cmd\n")},
	}
	dest := t.TempDir()

	if err := extractAssets(dest, assets, false); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	sharedPath := filepath.Join(dest, "ORO_AGENT.md")
	sharedData, err := os.ReadFile(sharedPath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("ORO_AGENT.md not extracted: %v", err)
	}

	claudePath := filepath.Join(dest, ".claude", "CLAUDE.md")
	claudeData, err := os.ReadFile(claudePath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf(".claude/CLAUDE.md not extracted: %v", err)
	}
	claude := string(claudeData)
	if !strings.Contains(claude, "# Oro Agent Instructions") || !strings.Contains(claude, "../ORO_AGENT.md") {
		t.Errorf(".claude/CLAUDE.md should be a wrapper generated from ORO_AGENT.md, got %q", claude)
	}

	agentsPath := filepath.Join(dest, "AGENTS.md")
	agentsData, err := os.ReadFile(agentsPath) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("AGENTS.md not extracted: %v", err)
	}
	if string(agentsData) != string(sharedData) {
		t.Errorf("AGENTS.md should mirror ORO_AGENT.md\ngot  %q\nwant %q", string(agentsData), string(sharedData))
	}
}

func TestInitGeneratesClaudeWrapperFromShared(t *testing.T) {
	const shared = "# Oro Agent Instructions\n\nportable content\n"
	assets := fstest.MapFS{
		"ORO_AGENT.md":                 &fstest.MapFile{Data: []byte(shared)},
		"skills/using-skills/SKILL.md": &fstest.MapFile{Data: []byte("# skill\n")},
		"hooks/placeholder.sh":         &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"beacons/placeholder.md":       &fstest.MapFile{Data: []byte("# beacon\n")},
		"commands/placeholder/cmd.md":  &fstest.MapFile{Data: []byte("# cmd\n")},
	}
	dest := t.TempDir()

	if err := extractAssets(dest, assets, false); err != nil {
		t.Fatalf("extractAssets failed: %v", err)
	}

	agentsData, err := os.ReadFile(filepath.Join(dest, "AGENTS.md")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf("AGENTS.md not extracted: %v", err)
	}
	if string(agentsData) != shared {
		t.Fatalf("AGENTS.md should mirror ORO_AGENT.md\ngot  %q\nwant %q", string(agentsData), shared)
	}

	claudeData, err := os.ReadFile(filepath.Join(dest, ".claude", "CLAUDE.md")) //nolint:gosec // test-created file
	if err != nil {
		t.Fatalf(".claude/CLAUDE.md not extracted: %v", err)
	}
	claude := string(claudeData)
	if claude == shared {
		t.Fatalf(".claude/CLAUDE.md should be a wrapper, not a duplicate of ORO_AGENT.md")
	}
	if !strings.Contains(claude, "../ORO_AGENT.md") {
		t.Fatalf(".claude/CLAUDE.md should reference ../ORO_AGENT.md, got:\n%s", claude)
	}
	if !strings.Contains(claude, "# Oro Agent Instructions") {
		t.Fatalf(".claude/CLAUDE.md should derive its title from ORO_AGENT.md, got:\n%s", claude)
	}
}

// TestRegenerationPolicyOnDivergence verifies the content-aware regeneration
// policy for ORO_AGENT.md, .claude/CLAUDE.md and AGENTS.md:
//   - matching content → silent skip
//   - diverged content + force=false → warn but do NOT overwrite
//   - diverged content + force=true  → overwrite silently
func TestRegenerationPolicyOnDivergence(t *testing.T) {
	const original = "# ORO_AGENT original content\n"
	const userEdited = "# user edited this file — do not overwrite\n"
	const updated = "# updated by oro upgrade\n"

	makeAssets := func(content string) fstest.MapFS {
		return fstest.MapFS{
			"ORO_AGENT.md":                 &fstest.MapFile{Data: []byte(content)},
			"skills/using-skills/SKILL.md": &fstest.MapFile{Data: []byte("# skill\n")},
			"hooks/placeholder.sh":         &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
			"beacons/placeholder.md":       &fstest.MapFile{Data: []byte("# beacon\n")},
			"commands/placeholder/cmd.md":  &fstest.MapFile{Data: []byte("# cmd\n")},
		}
	}

	t.Run("matching content skip silently", func(t *testing.T) {
		dest := t.TempDir()

		var w bytes.Buffer
		if err := extractAssetsW(dest, makeAssets(original), false, &w); err != nil {
			t.Fatalf("first extraction failed: %v", err)
		}
		if w.Len() > 0 {
			t.Errorf("expected no warnings on first install, got: %q", w.String())
		}

		w.Reset()
		if err := extractAssetsW(dest, makeAssets(original), false, &w); err != nil {
			t.Fatalf("second extraction failed: %v", err)
		}
		if w.Len() > 0 {
			t.Errorf("expected no warnings when content matches, got: %q", w.String())
		}
	})

	t.Run("diverged content warns but does not overwrite with force=false", func(t *testing.T) {
		dest := t.TempDir()
		claudeDir := filepath.Join(dest, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // test dir
			t.Fatalf("mkdir: %v", err)
		}
		writeFile := func(path, content string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test file
				t.Fatalf("write %s: %v", path, err)
			}
		}
		writeFile(filepath.Join(dest, "ORO_AGENT.md"), userEdited)
		writeFile(filepath.Join(claudeDir, "CLAUDE.md"), userEdited)
		writeFile(filepath.Join(dest, "AGENTS.md"), userEdited)

		var w bytes.Buffer
		if err := extractAssetsW(dest, makeAssets(updated), false, &w); err != nil {
			t.Fatalf("extraction failed: %v", err)
		}

		checkNotOverwritten := func(path string) {
			t.Helper()
			data, err := os.ReadFile(path) //nolint:gosec // test-created file
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if string(data) != userEdited {
				t.Errorf("%s should not be overwritten\ngot  %q\nwant %q", path, string(data), userEdited)
			}
		}
		checkNotOverwritten(filepath.Join(dest, "ORO_AGENT.md"))
		checkNotOverwritten(filepath.Join(claudeDir, "CLAUDE.md"))
		checkNotOverwritten(filepath.Join(dest, "AGENTS.md"))

		warning := w.String()
		if warning == "" {
			t.Error("expected divergence warnings, got none")
		}
		if !strings.Contains(warning, "AGENTS.md") {
			t.Errorf("warning should mention AGENTS.md, got: %q", warning)
		}
		if !strings.Contains(warning, "CLAUDE.md") {
			t.Errorf("warning should mention CLAUDE.md, got: %q", warning)
		}
	})

	t.Run("diverged content overwritten when force=true", func(t *testing.T) {
		dest := t.TempDir()
		claudeDir := filepath.Join(dest, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // test dir
			t.Fatalf("mkdir: %v", err)
		}
		writeFile := func(path, content string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test file
				t.Fatalf("write %s: %v", path, err)
			}
		}
		writeFile(filepath.Join(dest, "ORO_AGENT.md"), userEdited)
		writeFile(filepath.Join(claudeDir, "CLAUDE.md"), userEdited)
		writeFile(filepath.Join(dest, "AGENTS.md"), userEdited)

		var w bytes.Buffer
		if err := extractAssetsW(dest, makeAssets(updated), true, &w); err != nil {
			t.Fatalf("extraction with force failed: %v", err)
		}

		checkOverwritten := func(path string) {
			t.Helper()
			data, err := os.ReadFile(path) //nolint:gosec // test-created file
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if string(data) != updated {
				t.Errorf("%s should be overwritten\ngot  %q\nwant %q", path, string(data), updated)
			}
		}
		checkOverwritten(filepath.Join(dest, "ORO_AGENT.md"))
		checkOverwritten(filepath.Join(dest, "AGENTS.md"))

		claudeData, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md")) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("read CLAUDE.md: %v", err)
		}
		claude := string(claudeData)
		if !strings.Contains(claude, "# updated by oro upgrade") || !strings.Contains(claude, "../ORO_AGENT.md") {
			t.Errorf("CLAUDE.md should be overwritten with regenerated wrapper, got %q", claude)
		}
	})
}

// --- Init wizard tests (oro-vezg) ---

// newTestRootForInitDeps builds a minimal cobra root with the init subcommand
// wired to the given deps. Used only in tests.
func newTestRootForInitDeps(deps *initDeps) *cobra.Command {
	root := &cobra.Command{Use: "oro", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newInitCmdWithDeps(deps))
	return root
}

// cannedPrompt returns a prompt function that serves answers from the slice in
// order, falling back to def when the slice is exhausted.
func cannedPrompt(answers []string) func(io.Writer, string, string) (string, error) {
	idx := 0
	return func(_ io.Writer, _, def string) (string, error) {
		if idx >= len(answers) {
			return def, nil
		}
		ans := answers[idx]
		idx++
		return ans, nil
	}
}

// TestInitWizardWritesProviderMode verifies that a TTY-detected init session
// presents the provider-mode wizard and writes the selected agent.provider_mode
// in config.yaml.
func TestInitWizardWritesProviderMode(t *testing.T) {
	overrideToolDefs(t)
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", t.TempDir())

	deps := &initDeps{
		isTTY: func() bool { return true },
		prompt: cannedPrompt([]string{
			"claude-coding-codex-review",
		}),
	}

	root := newTestRootForInitDeps(deps)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"init", "--project-root", tmpDir, "--local"})

	if err := root.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
	data, err := os.ReadFile(configPath) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("config.yaml not found: %v", err)
	}
	content := string(data)

	for _, want := range []string{"agent:", "provider_mode:", "claude-coding-codex-review"} {
		if !strings.Contains(content, want) {
			t.Errorf("config.yaml should contain %q after wizard, got:\n%s", want, content)
		}
	}
}

func TestInitWizardWritesAgentTiers(t *testing.T) {
	overrideToolDefs(t)
	tmpDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", filepath.Join(home, "home"))
	t.Setenv("ORO_HOME", filepath.Join(home, "oro-home"))

	deps := &initDeps{
		isTTY: func() bool { return true },
		prompt: cannedPrompt([]string{
			"claude-coding-codex-review",
		}),
	}

	root := newTestRootForInitDeps(deps)
	root.SetArgs([]string{"init", "--project-root", tmpDir, "--local"})

	if err := root.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load agent config: %v", err)
	}
	if cfg.ProviderMode != config.ProviderModeClaudeCodingCodexReview {
		t.Fatalf("provider_mode = %q, want %q", cfg.ProviderMode, config.ProviderModeClaudeCodingCodexReview)
	}
	for _, tier := range []protocol.Tier{protocol.TierFast, protocol.TierBalanced, protocol.TierDeep, protocol.TierBackground} {
		tierCfg, ok := cfg.Tiers[tier]
		if !ok {
			t.Fatalf("missing tier %q in hydrated agent config", tier)
		}
		if tierCfg.Runtime != runtimeClaude {
			t.Fatalf("tier %q runtime = %q, want %q", tier, tierCfg.Runtime, runtimeClaude)
		}
		if tierCfg.Model == "" {
			t.Fatalf("tier %q model is empty", tier)
		}
	}
	if got := cfg.Roles["ops_review"].Runtime; got != runtimeCodex {
		t.Fatalf("ops_review runtime = %q, want %q", got, runtimeCodex)
	}
}

// TestInitNonTTYWritesDefaults verifies that a non-TTY session skips the
// wizard, writes the default agent.provider_mode, and emits a notice to stderr.
func TestInitNonTTYWritesDefaults(t *testing.T) {
	overrideToolDefs(t)
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", t.TempDir())

	deps := &initDeps{
		isTTY:  func() bool { return false },
		prompt: cannedPrompt(nil), // should never be called
	}

	root := newTestRootForInitDeps(deps)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"init", "--project-root", tmpDir, "--local"})

	if err := root.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
	data, err := os.ReadFile(configPath) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("config.yaml not found: %v", err)
	}
	content := string(data)

	for _, want := range []string{"agent:", "provider_mode:", "codex-coding-claude-review"} {
		if !strings.Contains(content, want) {
			t.Errorf("config.yaml should contain %q (defaults), got:\n%s", want, content)
		}
	}

	if !strings.Contains(stderr.String(), "non-interactive") {
		t.Errorf("stderr should contain non-interactive notice, got: %q", stderr.String())
	}
}

// TestInitSkipWizardFlag verifies that --skip-wizard writes silent defaults
// without emitting a stderr notice.
func TestInitSkipWizardFlag(t *testing.T) {
	overrideToolDefs(t)
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", t.TempDir())

	deps := &initDeps{
		isTTY:  func() bool { return true }, // would normally trigger wizard
		prompt: cannedPrompt(nil),           // must not be called
	}

	root := newTestRootForInitDeps(deps)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"init", "--project-root", tmpDir, "--local", "--skip-wizard"})

	if err := root.Execute(); err != nil {
		t.Fatalf("init --skip-wizard failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
	data, err := os.ReadFile(configPath) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("config.yaml not found: %v", err)
	}
	content := string(data)

	for _, want := range []string{"agent:", "provider_mode:", "codex-coding-claude-review"} {
		if !strings.Contains(content, want) {
			t.Errorf("config.yaml should contain %q (defaults), got:\n%s", want, content)
		}
	}

	if strings.Contains(stderr.String(), "non-interactive") {
		t.Errorf("--skip-wizard should produce no stderr notice, got: %q", stderr.String())
	}
}

// TestInitCheckQuietPreserved verifies that --check, --quiet, --local, and
// --project-root flags still behave identically after the wizard changes.
func TestInitCheckQuietPreserved(t *testing.T) {
	t.Run("check flag still exits non-zero on missing tool", func(t *testing.T) {
		orig := defaultToolDefs
		defaultToolDefs = []toolDef{
			{Name: "nonexistent-tool-xyz", Category: "system", CheckCmd: "nonexistent-tool-xyz", CheckArgs: []string{"--version"}},
		}
		t.Cleanup(func() { defaultToolDefs = orig })

		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"init", "--check"})

		if err := root.Execute(); err == nil {
			t.Fatal("init --check should fail with missing tools")
		}
	})

	t.Run("quiet flag produces no output on success", func(t *testing.T) {
		overrideToolDefs(t)

		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"init", "--check", "--quiet"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init --check --quiet should pass: %v", err)
		}
		if buf.String() != "" {
			t.Errorf("quiet mode should produce no output, got: %q", buf.String())
		}
	})

	t.Run("local flag creates .oro in project-root", func(t *testing.T) {
		overrideToolDefs(t)
		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", t.TempDir())

		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "--project-root", tmpDir, "--local", "--skip-wizard"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init --local failed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(tmpDir, ".oro", "config.yaml")); err != nil {
			t.Errorf("--local should create .oro/config.yaml in project-root: %v", err)
		}
	})

	t.Run("skip-wizard composes with check flag (no bootstrap)", func(t *testing.T) {
		overrideToolDefs(t)

		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "--check", "--skip-wizard"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init --check --skip-wizard should pass: %v", err)
		}
		if !strings.Contains(buf.String(), "OK") {
			t.Errorf("--check output should contain OK, got: %q", buf.String())
		}
	})
}
