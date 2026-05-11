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
		for _, want := range []string{
			`should_run_mutation_tests()`,
			`ORO_QG_CONTEXT:-local`,
			`ORO_RUN_MUTATION`,
			`QG_STAGE_ASSETS_LOCK=""`,
			`QG_EXIT_STATUS=0`,
			`trap cleanup_qg EXIT`,
			`trap 'exit 130' INT`,
			`STAGE_ASSETS_READY=true`,
			`if ! STAGE_ASSETS_ERROR=$(ensure_stage_assets 2>&1); then`,
			`if ! $STAGE_ASSETS_READY; then`,
			`export GOLANGCI_LINT_CACHE="$QG_DIR/golangci-lint-cache"`,
			`GOCACHE=$QG_DIR/golangci-go-cache GOFLAGS=-buildvcs=false golangci-lint run`,
			`ensure_stage_assets()`,
			`GO TIER 4: MUTATION TESTING (incremental)`,
			`restore_go_mutation_worktree()`,
			`pre_mutation_patch`,
			`trap 'QG_EXIT_STATUS=$?; restore_go_mutation_worktree`,
			`go tool -n go-mutesting`,
			`go tool go-mutesting`,
			`The mutation score is`,
			`mutation score $score for changed files is below 0.75 threshold`,
			`PASS: mutation score $score meets 0.75 threshold`,
			`cmd/oro embeds _assets but Makefile stage-assets target is unavailable`,
			`expected_rc_files=(`,
			`FAIL: missing lane result`,
		} {
			if !strings.Contains(script, want) {
				t.Errorf("generated Go script missing %q", want)
			}
		}
		for _, forbidden := range []string{
			"make clean-assets",
			`make stage-assets 2>/dev/null || true`,
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("generated Go script should not contain %q", forbidden)
			}
		}
		if !strings.Contains(script, `[ ! -f "cmd/oro/embed.go" ] || ! grep -q "_assets" "cmd/oro/embed.go"`) {
			t.Error("generated Go script should keep stage-assets optional for non-Oro Go projects")
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
		for _, want := range []string{
			`should_run_mutation_tests()`,
			`PYTHON TIER 5: MUTATION TESTING (incremental)`,
			`uv run cosmic-ray exec`,
			`uv run cr-rate`,
		} {
			if !strings.Contains(script, want) {
				t.Errorf("generated Python script missing %q", want)
			}
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

	// Script must reference the provided stealth paths that can appear inside
	// repository-relative command arguments. WorktreesDir may be outside the
	// repo in stealth mode; git pathspecs must not include absolute external
	// paths because git rejects them before producing any source file list.
	for _, want := range []string{paths.BeadsDir, paths.OroDocsDir} {
		if !strings.Contains(script, want) {
			t.Errorf("expected %q in script", want)
		}
	}
	if strings.Contains(script, paths.WorktreesDir) {
		t.Errorf("script should not use stealth WorktreesDir %q as a git/find path", paths.WorktreesDir)
	}

	// Biome loop must use the stealth docs path.
	if !strings.Contains(script, "for p in "+paths.OroDocsDir) {
		t.Errorf("biome loop should start with OroDocsDir %q", paths.OroDocsDir)
	}

	// Script must NOT use hardcoded defaults when stealth paths are set, except
	// repo-relative .worktrees exclusions. Those are safe because find/git only
	// traverse the current repository, not external stealth worktree roots.
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

	// Standard mode: absolute WorktreesDir should become ./relative in find exclusions.
	t.Run("standard mode uses relative find exclusions", func(t *testing.T) {
		stdPaths := ProjectPaths{
			Mode:         "standard",
			RepoRoot:     "/home/testuser/myproject",
			WorktreesDir: "/home/testuser/myproject/.worktrees",
			BeadsDir:     "/home/testuser/myproject/.beads",
			OroDocsDir:   "/home/testuser/myproject/docs",
		}

		var stdBuf bytes.Buffer
		if err := writeQualityGateScript(&stdBuf, stdPaths); err != nil {
			t.Fatalf("writeQualityGateScript (standard): %v", err)
		}
		stdScript := stdBuf.String()

		// find exclusions must use ./-relative paths, not absolute.
		if strings.Contains(stdScript, "-not -path '/home/testuser/myproject/.worktrees") {
			t.Error("standard mode: find exclusion must not use absolute WorktreesDir path")
		}
		if !strings.Contains(stdScript, "-not -path './.worktrees/*'") {
			t.Error("standard mode: find exclusion should use ./.worktrees/*")
		}

		// Biome loop should use relative paths.
		if !strings.Contains(stdScript, "./.beads") {
			t.Error("standard mode: biome loop should use relative ./.beads path")
		}
		if !strings.Contains(stdScript, "./docs") {
			t.Error("standard mode: biome loop should use relative ./docs path")
		}

		checkBashSyntax(t, stdScript)
	})
}

// TestWriteQualityGateScriptFile_ZeroLanguages verifies that writeQualityGateScriptFile
// generates a quality_gate.sh even when config.yaml has languages: {} (zero languages).
func TestWriteQualityGateScriptFile_ZeroLanguages(t *testing.T) {
	dir := t.TempDir()

	// Write a config.yaml with an empty languages map.
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("languages: {}\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	qgPath := filepath.Join(dir, "scripts", "quality_gate.sh")
	paths := ProjectPaths{
		ConfigYAML:  configPath,
		QualityGate: qgPath,
	}

	if err := writeQualityGateScriptFile(paths, false); err != nil {
		t.Fatalf("writeQualityGateScriptFile: %v", err)
	}

	data, err := os.ReadFile(qgPath)
	if err != nil {
		t.Fatalf("quality_gate.sh was not created: %v", err)
	}

	script := string(data)
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf("expected shebang, got: %q", script[:min(len(script), 40)])
	}

	// Shell-only: no language lanes.
	if strings.Contains(script, "lane_go") {
		t.Error("shell-only script should not contain lane_go")
	}
	if strings.Contains(script, "lane_python") {
		t.Error("shell-only script should not contain lane_python")
	}

	checkBashSyntax(t, script)
}

// TestWriteQualityGateScriptFile_AbsentConfig verifies that writeQualityGateScriptFile
// silently skips (no error, no file) when config.yaml does not exist.
func TestWriteQualityGateScriptFile_AbsentConfig(t *testing.T) {
	dir := t.TempDir()
	qgPath := filepath.Join(dir, "scripts", "quality_gate.sh")
	paths := ProjectPaths{
		ConfigYAML:  filepath.Join(dir, "does-not-exist.yaml"),
		QualityGate: qgPath,
	}

	if err := writeQualityGateScriptFile(paths, false); err != nil {
		t.Fatalf("expected no error for absent config, got: %v", err)
	}

	if _, err := os.Stat(qgPath); !os.IsNotExist(err) {
		t.Error("quality_gate.sh should not be created when config.yaml is absent")
	}
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
