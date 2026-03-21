package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/langprofile"
)

// TestGenerateGolangciLint verifies .golangci.yml content generation from Config.
func TestGenerateGolangciLint(t *testing.T) {
	t.Run("Go in config returns valid YAML with version 2 and 15+ linters", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"go": {Linters: []string{"golangci-lint"}},
			},
		}

		got, err := generateGolangciLint(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == "" {
			t.Fatal("expected non-empty YAML, got empty string")
		}
		if !strings.Contains(got, `version: "2"`) {
			t.Errorf("expected version 2 format, got:\n%s", got)
		}

		// Verify the standard linter set is present.
		requiredLinters := []string{
			"staticcheck", "govet", "errcheck", "wrapcheck", "gosec",
			"gocritic", "revive", "misspell", "gocyclo", "gocognit",
			"funlen", "nestif", "errorlint", "nilerr", "bodyclose",
		}
		for _, l := range requiredLinters {
			if !strings.Contains(got, "- "+l) {
				t.Errorf("expected linter %q in output", l)
			}
		}
		// 15+ linters means 15+ entries: len(requiredLinters) == 15 already checked above.
	})

	t.Run("No Go in config returns empty string", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {Linters: []string{"ruff"}},
			},
		}

		got, err := generateGolangciLint(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for non-Go config, got:\n%s", got)
		}
	})

	t.Run("Nil config returns empty string", func(t *testing.T) {
		got, err := generateGolangciLint(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for nil config, got:\n%s", got)
		}
	})

	t.Run("Empty languages map returns empty string", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{},
		}

		got, err := generateGolangciLint(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for config with no languages, got:\n%s", got)
		}
	})
}

// TestGeneratePyprojectToolSections verifies pyproject.toml tool section generation from Config.
func TestGeneratePyprojectToolSections(t *testing.T) {
	t.Run("Python in config returns TOML with tool sections", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {Linters: []string{"ruff", "pylint"}, TypeCheck: "pyright"},
			},
		}

		got, err := generatePyprojectToolSections(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == "" {
			t.Fatal("expected non-empty TOML, got empty string")
		}

		// Verify all required tool sections are present.
		requiredSections := []string{
			"[tool.ruff]",
			"[tool.ruff.lint]",
			"[tool.pyright]",
			"[tool.pytest.ini_options]",
		}
		for _, section := range requiredSections {
			if !strings.Contains(got, section) {
				t.Errorf("expected section %q in output", section)
			}
		}

		// Verify ruff lint select includes the standard rule set.
		requiredRules := []string{"E", "F", "W", "I", "N", "UP", "B", "A", "SIM", "RUF"}
		for _, rule := range requiredRules {
			if !strings.Contains(got, `"`+rule+`"`) {
				t.Errorf("expected ruff rule %q in select list", rule)
			}
		}
	})

	t.Run("No Python in config returns empty string", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"go": {Linters: []string{"golangci-lint"}},
			},
		}

		got, err := generatePyprojectToolSections(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for non-Python config, got:\n%s", got)
		}
	})

	t.Run("Nil config returns empty string", func(t *testing.T) {
		got, err := generatePyprojectToolSections(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for nil config, got:\n%s", got)
		}
	})

	t.Run("Empty languages map returns empty string", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{},
		}

		got, err := generatePyprojectToolSections(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for config with no languages, got:\n%s", got)
		}
	})
}

// TestGenerateQualityGateScript verifies the quality gate script generator
// produces scripts tailored to detected languages.
func TestGenerateQualityGateScript(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		_, err := generateQualityGateScript(nil)
		if err == nil {
			t.Fatal("expected error for nil config, got nil")
		}
	})

	t.Run("empty languages returns error", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{},
		}
		_, err := generateQualityGateScript(cfg)
		if err == nil {
			t.Fatal("expected error for empty languages, got nil")
		}
	})

	t.Run("go-only config produces Go lane and no Python lane", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"go": {
					TestCmd:    "go test ./...",
					Formatters: []string{"gofumpt"},
					Linters:    []string{"golangci-lint"},
				},
			},
		}
		script, err := generateQualityGateScript(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
			t.Error("script should start with #!/usr/bin/env bash")
		}
		if !strings.Contains(script, "lane_go") {
			t.Error("go-only config should include lane_go function")
		}
		if strings.Contains(script, "lane_python") {
			t.Error("go-only config should not include lane_python function")
		}

		checkBashSyntax(t, script)
	})

	t.Run("python-only config produces Python lane and no Go lane", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {
					TestCmd:    "uv run pytest",
					Formatters: []string{"ruff"},
					Linters:    []string{"ruff"},
				},
			},
		}
		script, err := generateQualityGateScript(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
			t.Error("script should start with #!/usr/bin/env bash")
		}
		if strings.Contains(script, "lane_go") {
			t.Error("python-only config should not include lane_go function")
		}
		if !strings.Contains(script, "lane_python") {
			t.Error("python-only config should include lane_python function")
		}

		checkBashSyntax(t, script)
	})

	t.Run("both go and python produces both lanes", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"go": {
					TestCmd:    "go test ./...",
					Formatters: []string{"gofumpt"},
				},
				"python": {
					TestCmd:    "uv run pytest",
					Formatters: []string{"ruff"},
				},
			},
		}
		script, err := generateQualityGateScript(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
			t.Error("script should start with #!/usr/bin/env bash")
		}
		if !strings.Contains(script, "lane_go") {
			t.Error("both-lang config should include lane_go function")
		}
		if !strings.Contains(script, "lane_python") {
			t.Error("both-lang config should include lane_python function")
		}

		checkBashSyntax(t, script)
	})
}

// TestQualityGateScript_StealthPaths verifies that writeQualityGateScript uses
// ProjectPaths fields for path substitution instead of hardcoded defaults.
func TestQualityGateScript_StealthPaths(t *testing.T) {
	stealthBase := "/home/testuser/.oro/projects/s-abcdef0123456789"
	paths := ProjectPaths{
		Mode:         "stealth",
		RepoRoot:     "/home/testuser/myproject",
		WorktreesDir: filepath.Join(stealthBase, "worktrees"),
		BeadsDir:     filepath.Join(stealthBase, "beads"),
		OroDocsDir:   filepath.Join(stealthBase, "docs"),
	}

	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, paths); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()

	// Script must reference the provided stealth paths.
	for _, want := range []string{paths.WorktreesDir, paths.BeadsDir, paths.OroDocsDir} {
		if !strings.Contains(script, want) {
			t.Errorf("expected %q in script", want)
		}
	}

	// Biome loop must use the stealth docs path.
	if !strings.Contains(script, "for p in "+paths.OroDocsDir) {
		t.Errorf("biome loop should start with OroDocsDir %q", paths.OroDocsDir)
	}

	// Script must NOT use hardcoded defaults when stealth paths are set.
	if strings.Contains(script, "./.worktrees") {
		t.Error("script should not contain hardcoded ./.worktrees when stealth WorktreesDir is set")
	}
	if strings.Contains(script, " .beads/") {
		t.Error("script should not contain hardcoded .beads/ when stealth BeadsDir is set")
	}

	// Empty paths must produce the standard defaults.
	var defBuf bytes.Buffer
	if err := writeQualityGateScript(&defBuf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript with empty paths: %v", err)
	}
	defScript := defBuf.String()
	if !strings.Contains(defScript, "./.worktrees") {
		t.Error("empty WorktreesDir should default to ./.worktrees")
	}
	if !strings.Contains(defScript, ".beads") {
		t.Error("empty BeadsDir should default to .beads")
	}
	if !strings.Contains(defScript, "for p in docs/") {
		t.Error("empty OroDocsDir should default to docs/ in biome loop")
	}

	checkBashSyntax(t, script)
	checkBashSyntax(t, defScript)
}

// checkBashSyntax writes the script to a temp file and runs bash -n to verify
// the script is syntactically valid shell.
func checkBashSyntax(t *testing.T, script string) {
	t.Helper()

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found in PATH, skipping syntax check")
		return
	}

	f, err := os.CreateTemp("", "quality_gate_*.sh")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(f.Name())

	if _, err := f.WriteString(script); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	out, err := exec.Command(bashPath, "-n", f.Name()).CombinedOutput() //nolint:gosec // bashPath from LookPath, f.Name() is our own temp file
	if err != nil {
		t.Errorf("bash -n syntax check failed: %v\n%s", err, string(out))
	}
}
