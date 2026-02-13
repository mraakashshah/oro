package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestInitCommand_GeneratesConfig(t *testing.T) {
	t.Run("generates config for Go project", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a go.mod file to simulate a Go project
		goModPath := filepath.Join(tmpDir, "go.mod")
		if err := os.WriteFile(goModPath, []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("failed to create go.mod: %v", err)
		}

		// Run init command
		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "--project-root", tmpDir})

		if err := root.Execute(); err != nil {
			t.Fatalf("init command failed: %v", err)
		}

		// Verify .oro/config.yaml was created
		configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("config file not created: %v", err)
		}

		// Verify config contains Go language profile
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("failed to read config: %v", err)
		}

		config := string(data)
		if !strings.Contains(config, "go:") {
			t.Errorf("config should contain 'go:' section, got:\n%s", config)
		}
		if !strings.Contains(config, "gofumpt") {
			t.Errorf("config should contain 'gofumpt' tool, got:\n%s", config)
		}
		if !strings.Contains(config, "golangci-lint") {
			t.Errorf("config should contain 'golangci-lint' tool, got:\n%s", config)
		}
	})

	t.Run("generates empty config when no languages detected", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Run init command on empty directory
		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "--project-root", tmpDir})

		if err := root.Execute(); err != nil {
			t.Fatalf("init command failed: %v", err)
		}

		// Verify .oro/config.yaml was created with comment
		configPath := filepath.Join(tmpDir, ".oro", "config.yaml")
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("config file not created: %v", err)
		}

		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("failed to read config: %v", err)
		}

		config := string(data)
		if !strings.Contains(config, "#") || !strings.Contains(config, "no languages detected") {
			t.Errorf("config should contain comment about no languages, got:\n%s", config)
		}
	})

	t.Run("warns when config already exists", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create existing config
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // test directory
			t.Fatalf("failed to create .oro dir: %v", err)
		}
		configPath := filepath.Join(oroDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte("existing config\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("failed to create existing config: %v", err)
		}

		// Run init command without --force
		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"init", "--project-root", tmpDir})

		if err := root.Execute(); err == nil {
			t.Fatal("init should fail when config exists without --force")
		}

		// Verify original config is unchanged
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("failed to read config: %v", err)
		}
		if string(data) != "existing config\n" {
			t.Errorf("config should be unchanged, got:\n%s", string(data))
		}

		// Verify warning message
		output := buf.String()
		if !strings.Contains(output, "exists") || !strings.Contains(output, "--force") {
			t.Errorf("output should warn about existing config and mention --force, got:\n%s", output)
		}
	})

	t.Run("overwrites config with --force", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a go.mod file
		goModPath := filepath.Join(tmpDir, "go.mod")
		if err := os.WriteFile(goModPath, []byte("module example.com/test\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("failed to create go.mod: %v", err)
		}

		// Create existing config
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // test directory
			t.Fatalf("failed to create .oro dir: %v", err)
		}
		configPath := filepath.Join(oroDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte("existing config\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("failed to create existing config: %v", err)
		}

		// Run init command with --force
		root := newRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs([]string{"init", "--project-root", tmpDir, "--force"})

		if err := root.Execute(); err != nil {
			t.Fatalf("init --force command failed: %v", err)
		}

		// Verify config was overwritten
		data, err := os.ReadFile(configPath) //nolint:gosec // test-created file
		if err != nil {
			t.Fatalf("failed to read config: %v", err)
		}

		config := string(data)
		if strings.Contains(config, "existing config") {
			t.Errorf("config should be overwritten, still contains old content:\n%s", config)
		}
		if !strings.Contains(config, "go:") {
			t.Errorf("new config should contain 'go:' section, got:\n%s", config)
		}
	})
}
