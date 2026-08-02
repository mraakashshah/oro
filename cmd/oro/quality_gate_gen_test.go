package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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

// TestQualityGatePylintRunsInProjectEnv verifies the generated gate invokes
// pylint inside the project's dependency environment (via uv) rather than a
// global install, so source files that import project dependencies (e.g. pytest)
// do not raise a false import-error (E0401), and that the python tool resolver
// does not fall back to a global ~/.local/bin install.
func TestQualityGatePylintRunsInProjectEnv(t *testing.T) {
	cfg := &langprofile.Config{
		Languages: map[string]langprofile.LanguageConfig{
			"python": {
				TestCmd:    "uv run pytest",
				Formatters: []string{"ruff"},
				Linters:    []string{"ruff", "pylint"},
				TypeCheck:  "pyright",
			},
		},
	}
	script, err := generateQualityGateScript(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fix: the python tool resolver must not fall back to a global ~/.local/bin
	// install, which runs outside the project dependency environment.
	if strings.Contains(script, `$HOME/.local/bin/$tool`) {
		t.Error("qg_python_tool_path should not include the global $HOME/.local/bin candidate")
	}
	// Fix: pylint must run via uv in the project env, not a bare global binary.
	if !strings.Contains(script, `uv run --with pylint pylint`) {
		t.Error("qg_run_python_tool should run pylint via `uv run --with pylint`")
	}
	// Fix: the pylint lint helper must route through qg_run_python_tool.
	if !strings.Contains(script, `qg_run_python_tool pylint --disable=all --enable=E`) {
		t.Error("qg_run_pylint_source should route pylint through qg_run_python_tool")
	}

	checkBashSyntax(t, script)
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

		if !strings.HasPrefix(script, "#!/bin/sh\n# shellcheck shell=bash") {
			t.Error("script should start with /bin/sh Bash bootstrap")
		}
		if !strings.Contains(script, `BASH_VERSINFO[0]`) || !strings.Contains(script, `exec env -u BASH_ENV "$qg_bash" "$0" "$@"`) {
			t.Error("script should verify and exec Bash 4+ after bootstrap")
		}
		if !strings.Contains(script, "lane_go") {
			t.Error("go-only config should include lane_go function")
		}
		if strings.Contains(script, "lane_python") {
			t.Error("go-only config should not include lane_python function")
		}
		for _, want := range []string{
			`ORO_QG_ACTIVE_PID`,
			`should_run_mutation_tests()`,
			`--mutation-testing`,
			`QG_MUTATION_TESTING=true`,
			`mutation disabled by default; use --mutation-testing`,
			`QG_STAGE_ASSETS_LOCK=""`,
			`QG_EXIT_STATUS=0`,
			`trap cleanup_qg EXIT`,
			`trap 'exit 130' INT`,
			`STAGE_ASSETS_READY=true`,
			`if ! STAGE_ASSETS_ERROR=$(ensure_stage_assets 2>&1); then`,
			`if ! $STAGE_ASSETS_READY; then`,
			`ensure_stage_assets()`,
			`GO TIER 4: MUTATION TESTING (incremental)`,
			`restore_go_mutation_worktree()`,
			`snapshot_go_mutation_side_effects()`,
			`restore_go_mutation_side_effects()`,
			`git/hooks/pre-push`,
			`*.go.tmp`,
			`pre_mutation_patch`,
			`trap 'QG_EXIT_STATUS=$?; restore_go_mutation_worktree`,
			`restore_go_mutation_side_effects "$GO_MUTATION_SIDE_EFFECT_SNAPSHOT"`,
			`go tool -n go-mutesting`,
			`go tool go-mutesting`,
			`The mutation score is`,
			`mutation score $score for changed files is below 0.75 threshold`,
			`PASS: mutation score $score meets 0.75 threshold`,
			`cmd/oro embeds _assets but Makefile stage-assets target is unavailable`,
			`expected_rc_files=(`,
			`FAIL: missing lane result`,
			`export GOMAXPROCS="${ORO_QG_GOMAXPROCS:-2}"`,
		} {
			if !strings.Contains(script, want) {
				t.Errorf("generated Go script missing %q", want)
			}
		}
		for _, forbidden := range []string{
			`export GOCACHE=`,
			`export GOMODCACHE=`,
			`export GOLANGCI_LINT_CACHE=`,
			`export UV_CACHE_DIR=`,
			`GOCACHE=$QG_DIR/`,
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("generated Go script overrides shared cache via %q", forbidden)
			}
		}
		if strings.Contains(script, "ORO_RUN_MUTATION") {
			t.Error("generated Go script must not enable mutation testing from ambient ORO_RUN_MUTATION")
		}
		for _, forbidden := range []string{
			`push | pre-push`,
			`GITHUB_EVENT_NAME`,
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("generated Go script should not enable mutation by default via %q", forbidden)
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

		if !strings.HasPrefix(script, "#!/bin/sh\n# shellcheck shell=bash") {
			t.Error("script should start with /bin/sh Bash bootstrap")
		}
		if !strings.Contains(script, `BASH_VERSINFO[0]`) || !strings.Contains(script, `exec env -u BASH_ENV "$qg_bash" "$0" "$@"`) {
			t.Error("script should verify and exec Bash 4+ after bootstrap")
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
			`check "pytest" "qg_run_python_tool pytest"`,
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

		if !strings.HasPrefix(script, "#!/bin/sh\n# shellcheck shell=bash") {
			t.Error("script should start with /bin/sh Bash bootstrap")
		}
		if !strings.Contains(script, `BASH_VERSINFO[0]`) || !strings.Contains(script, `exec env -u BASH_ENV "$qg_bash" "$0" "$@"`) {
			t.Error("script should verify and exec Bash 4+ after bootstrap")
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

func TestQualityGateGolangciLintTimeoutAllowsLoadedRuns(t *testing.T) {
	cfg := &langprofile.Config{
		Languages: map[string]langprofile.LanguageConfig{
			"go": {Linters: []string{"golangci-lint"}},
		},
	}
	generated, err := generateQualityGateScript(cfg)
	if err != nil {
		t.Fatalf("generate quality gate: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for name, script := range map[string]string{
		"generated":  generated,
		"checked-in": string(checkedIn),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(script, "golangci-lint run --timeout 10m --allow-parallel-runners") {
				t.Fatal("quality gate does not allow ten minutes for golangci-lint under load")
			}
			if strings.Contains(script, "golangci-lint run --timeout 5m --allow-parallel-runners") {
				t.Fatal("quality gate retains the five-minute golangci-lint timeout")
			}
		})
	}
}

func TestQualityGatesUseTrackedShellSources(t *testing.T) {
	cfg := &langprofile.Config{
		Languages: map[string]langprofile.LanguageConfig{
			"go": {TestCmd: "go test ./..."},
		},
	}
	generated, err := generateQualityGateScript(cfg)
	if err != nil {
		t.Fatalf("generate quality gate: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for name, script := range map[string]string{
		"generated":  generated,
		"checked-in": string(checkedIn),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(script, `find . -name '*.sh'`) {
				t.Fatal("shell checks walk mutable untracked worktrees")
			}
			for _, want := range []string{"qg_shell_source_files", "qg_run_shellcheck_source"} {
				if !strings.Contains(script, want) {
					t.Fatalf("quality gate missing %s", want)
				}
			}
		})
	}
}

// TestQualityGateScript_StealthPaths verifies that writeQualityGateScript uses
// the configured docs path instead of a hardcoded default.
func TestQualityGateScript_StealthPaths(t *testing.T) {
	stealthBase := "/home/testuser/.oro/projects/s-abcdef0123456789"
	paths := ProjectPaths{
		Mode:         "stealth",
		RepoRoot:     "/home/testuser/myproject",
		WorktreesDir: filepath.Join(stealthBase, "worktrees"),
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
	for _, want := range []string{paths.OroDocsDir} {
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

	// Script must NOT use legacy hardcoded paths when stealth paths are set.
	if strings.Contains(script, ".beads") {
		t.Error("script should not contain legacy .beads paths")
	}

	// Empty paths must produce the standard defaults.
	var defBuf bytes.Buffer
	if err := writeQualityGateScript(&defBuf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript with empty paths: %v", err)
	}
	defScript := defBuf.String()
	if strings.Contains(defScript, ".beads") {
		t.Error("empty paths should not reintroduce legacy .beads paths")
	}
	if !strings.Contains(defScript, "for p in docs/") {
		t.Error("empty OroDocsDir should default to docs/ in biome loop")
	}

	checkBashSyntax(t, script)
	checkBashSyntax(t, defScript)

	// Standard mode: absolute docs paths should become repository-relative.
	t.Run("standard mode uses relative docs path", func(t *testing.T) {
		stdPaths := ProjectPaths{
			Mode:         "standard",
			RepoRoot:     "/home/testuser/myproject",
			WorktreesDir: "/home/testuser/myproject/.worktrees",
			OroDocsDir:   "/home/testuser/myproject/docs",
		}

		var stdBuf bytes.Buffer
		if err := writeQualityGateScript(&stdBuf, stdPaths); err != nil {
			t.Fatalf("writeQualityGateScript (standard): %v", err)
		}
		stdScript := stdBuf.String()

		// Biome loop should use relative paths.
		if strings.Contains(stdScript, ".beads") {
			t.Error("standard mode: script should not include legacy .beads path")
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
	if !strings.HasPrefix(script, "#!/bin/sh\n# shellcheck shell=bash") {
		t.Errorf("expected sh Bash bootstrap, got: %q", script[:min(len(script), 40)])
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

func qualityGateSerialLaneHarness(t *testing.T, script, body string) string {
	t.Helper()
	serialLaneStart := strings.Index(script, "run_serial_lane() {")
	if serialLaneStart < 0 {
		t.Fatal("quality gate missing run_serial_lane")
	}
	serialLaneEndRel := strings.Index(script[serialLaneStart:], "\n}\n")
	if serialLaneEndRel < 0 {
		t.Fatal("quality gate run_serial_lane missing closing brace")
	}
	serialLaneEnd := serialLaneStart + serialLaneEndRel + len("\n}\n")
	serialLane := script[serialLaneStart:serialLaneEnd]
	if !strings.Contains(serialLane, "acquire_quality_gate_lock") {
		t.Fatal("quality gate run_serial_lane does not acquire the quality-gate lock")
	}
	if strings.Contains(script[:serialLaneStart], "\nacquire_quality_gate_lock\n") {
		t.Fatal("quality gate acquires the lock before run_serial_lane")
	}
	return script[:serialLaneEnd] + "\nheader() { :; }\n" +
		"if [ -n \"${ORO_QG_TEST_BASH_ENV:-}\" ]; then source \"$ORO_QG_TEST_BASH_ENV\"; fi\n" +
		"run_serial_lane\n" + body
}

func qualityGateArtifactSweepHarness(t *testing.T, script string) string {
	t.Helper()
	start := strings.Index(script, "sweep_repo_root_escape_artifacts() {")
	if start < 0 {
		t.Fatal("quality gate missing OSC-8 artifact sweep")
	}
	endRel := strings.Index(script[start:], "\n}\n")
	if endRel < 0 {
		t.Fatal("quality gate OSC-8 artifact sweep missing closing brace")
	}
	invocation := strings.Index(script, "sweep_repo_root_escape_artifacts \"$REPO_ROOT\"")
	lock := strings.Index(script, "acquire_quality_gate_lock() {")
	if invocation < 0 || lock < 0 || invocation > lock {
		t.Fatal("quality gate does not sweep OSC-8 artifacts before lock acquisition")
	}
	end := start + endRel + len("\n}\n")
	return "#!/usr/bin/env bash\nset -euo pipefail\n" + script[start:end] + "\nsweep_repo_root_escape_artifacts \"$REPO_ROOT\"\n"
}

func TestQualityGateRepoRootEscapeArtifactSweep(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("write generated quality gate: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			artifact := filepath.Join(repoRoot, "\x1b]8;;file:artifact")
			normalDir := filepath.Join(repoRoot, "normal")
			liveLock := filepath.Join(repoRoot, ".oro-quality-gate.lock")
			ordinaryFile := filepath.Join(repoRoot, "ordinary-file")
			for _, dir := range []string{artifact, normalDir, liveLock} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatalf("create %q: %v", dir, err)
				}
			}
			if err := os.WriteFile(ordinaryFile, []byte("keep"), 0o644); err != nil {
				t.Fatalf("write ordinary file: %v", err)
			}

			harnessPath := filepath.Join(repoRoot, "sweep.sh")
			if err := os.WriteFile(harnessPath, []byte(qualityGateArtifactSweepHarness(t, tc.script)), 0o755); err != nil {
				t.Fatalf("write artifact sweep harness: %v", err)
			}
			cmd := exec.Command(harnessPath) //nolint:gosec // harnessPath is a test-owned temp file
			cmd.Env = append(os.Environ(), "REPO_ROOT="+repoRoot)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("run artifact sweep: %v\n%s", err, out)
			}

			if _, err := os.Stat(artifact); !os.IsNotExist(err) {
				t.Fatalf("OSC-8 artifact directory should be removed, stat err=%v", err)
			}
			for _, preserved := range []string{normalDir, liveLock, ordinaryFile} {
				if _, err := os.Lstat(preserved); err != nil {
					t.Fatalf("preserved entry %q missing: %v", preserved, err)
				}
			}
		})
	}
}

func TestQualityGateRunLockArchivesDeadOwnerAndStartsWithoutTimeout(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "quality_gate.sh")

	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()
	harness := qualityGateSerialLaneHarness(t, script, "")
	if err := os.WriteFile(scriptPath, []byte(harness), 0o755); err != nil {
		t.Fatalf("write quality gate script: %v", err)
	}

	lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatalf("create stale lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte("pid=999999\n"), 0o644); err != nil {
		t.Fatalf("write stale lock owner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath) //nolint:gosec // scriptPath is a test-owned temp file
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ORO_QG_LOCK_TIMEOUT_SECONDS=2")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quality gate should archive stale dead-owner lock and start, got %v\n%s", err, out)
	}

	output := string(out)
	if strings.Contains(output, "timed out waiting for quality gate lock") {
		t.Fatalf("quality gate timed out instead of archiving stale lock:\n%s", output)
	}
	if !strings.Contains(output, "archived stale quality gate lock") {
		t.Fatalf("quality gate did not report stale lock archival:\n%s", output)
	}

	matches, err := filepath.Glob(lockDir + ".stale.*")
	if err != nil {
		t.Fatalf("glob stale lock archives: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one stale lock archive, got %d: %v", len(matches), matches)
	}
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Fatalf("active quality gate lock should be cleaned up after run, stat err=%v", err)
	}
}

func TestQualityGateRunLockDoesNotReportWaitingWhenUncontended(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "quality_gate.sh")

	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()
	harness := qualityGateSerialLaneHarness(t, script, "")
	if err := os.WriteFile(scriptPath, []byte(harness), 0o755); err != nil {
		t.Fatalf("write quality gate script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath) //nolint:gosec // scriptPath is a test-owned temp file
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quality gate should acquire an uncontended lock, got %v\n%s", err, out)
	}
	if strings.Contains(string(out), "Waiting for another quality gate to finish") {
		t.Fatalf("uncontended quality gate reported lock waiting:\n%s", out)
	}
}

func TestQualityGateScriptsRecursiveInvocationReturnsWithoutQueueingBehindParent(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}

	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			harness := qualityGateSerialLaneHarness(t, tc.script, `
if [ "${ORO_QG_RECURSIVE_TEST:-}" = "1" ]; then
    ORO_QG_RECURSIVE_TEST=0 "$0"
fi
if [ "${ORO_QG_RECURSIVE_TEST:-}" = "0" ]; then
    echo "recursive child reached quality gate body"
fi
`)
			scriptPath := filepath.Join(dir, "quality_gate.sh")
			if err := os.WriteFile(scriptPath, []byte(harness), 0o755); err != nil {
				t.Fatalf("write quality gate harness: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, scriptPath) //nolint:gosec // scriptPath is a test-owned temp file
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"ORO_QG_RECURSIVE_TEST=1",
				"ORO_QG_LOCK_POLL_SECONDS=1",
				"ORO_QG_LOCK_TIMEOUT_SECONDS=1",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("recursive quality gate should return without waiting behind its parent, got %v\n%s", err, out)
			}
			if strings.Contains(string(out), "Waiting for another quality gate to finish") {
				t.Fatalf("recursive quality gate queued behind its parent:\n%s", out)
			}
			if strings.Contains(string(out), "recursive child reached quality gate body") {
				t.Fatalf("recursive quality gate continued into its body:\n%s", out)
			}
		})
	}
}

func TestQualityGateRunLockHasNoDefaultTimeoutForLiveContention(t *testing.T) {
	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()

	if regexp.MustCompile(`ORO_QG_LOCK_TIMEOUT_SECONDS:-[0-9]+`).MatchString(script) {
		t.Fatalf("quality gate lock should not have a default live-contention timeout")
	}
	if !strings.Contains(script, "ORO_QG_LOCK_TIMEOUT_SECONDS") {
		t.Fatalf("quality gate lock should still honor explicit ORO_QG_LOCK_TIMEOUT_SECONDS")
	}
}

func TestQualityGateScriptsPublishQueueTicketsAtomically(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			eventsPath := filepath.Join(dir, "events")
			readyPath := filepath.Join(dir, "ticket-ready")
			releasePath := filepath.Join(dir, "ticket-release")
			emergencyReleasePath := filepath.Join(dir, "ticket-emergency-release")
			harnessPath := filepath.Join(dir, "quality-gate.sh")
			harness := qualityGateSerialLaneHarness(t, tc.script, `
printf '%s\n' "$ORO_QG_TEST_NAME" >>"$ORO_QG_TEST_EVENTS"
`)
			if err := os.WriteFile(harnessPath, []byte(harness), 0o755); err != nil {
				t.Fatalf("write quality gate harness: %v", err)
			}

			// Pause the early waiter at the publication boundary. The old code
			// publishes with mkdir and initializes owner afterward; the fixed code
			// initializes a sibling staging directory and publishes with atomic mv.
			barrierEnv := qualityGateTicketPublicationBarrierEnv(t)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			t.Cleanup(func() {
				_ = os.WriteFile(emergencyReleasePath, []byte("emergency release\n"), 0o644)
			})

			command := func(name string, output *bytes.Buffer) *exec.Cmd {
				cmd := exec.CommandContext(ctx, harnessPath) //nolint:gosec // test-owned temp script
				cmd.Dir = dir
				cmd.Env = append(os.Environ(),
					"ORO_QG_TEST_NAME="+name,
					"ORO_QG_TEST_EVENTS="+eventsPath,
					"ORO_QG_TEST_BASH_ENV="+barrierEnv,
					"ORO_QG_TEST_TICKET_READY="+readyPath,
					"ORO_QG_TEST_TICKET_RELEASE="+releasePath,
					"ORO_QG_TEST_TICKET_EMERGENCY_RELEASE="+emergencyReleasePath,
					"ORO_QG_LOCK_POLL_SECONDS=1",
					"ORO_QG_LOCK_TIMEOUT_SECONDS=5",
				)
				cmd.Stdout = output
				cmd.Stderr = output
				return cmd
			}

			var earlyOutput bytes.Buffer
			early := command("early", &earlyOutput)
			if err := early.Start(); err != nil {
				t.Fatalf("start early waiter: %v", err)
			}
			if !waitForQualityGatePath(readyPath, 2*time.Second) {
				t.Fatalf("early waiter did not reach ticket publication barrier:\n%s", earlyOutput.String())
			}

			var lateOutput bytes.Buffer
			late := command("late", &lateOutput)
			lateErr := late.Run()
			releaseErr := os.WriteFile(releasePath, []byte("release\n"), 0o644)
			if lateErr != nil {
				t.Fatalf("late waiter failed while early publication was paused: %v\n%s", lateErr, lateOutput.String())
			}
			if releaseErr != nil {
				t.Fatalf("release early ticket publication: %v", releaseErr)
			}
			if err := early.Wait(); err != nil {
				t.Fatalf("early waiter lost its ticket during publication: %v\n%s", err, earlyOutput.String())
			}

			events, err := os.ReadFile(eventsPath)
			if err != nil {
				t.Fatalf("read acquisition events: %v", err)
			}
			if got := strings.Fields(string(events)); !reflect.DeepEqual(got, []string{"late", "early"}) {
				t.Fatalf("quality gate acquisition order = %v, want late then early", got)
			}
			for _, path := range []string{
				filepath.Join(dir, ".oro-quality-gate.lock"),
				filepath.Join(dir, ".oro-quality-gate.queue"),
			} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("quality gate artifact should be cleaned up: %s: %v", path, err)
				}
			}
		})
	}
}

func TestQualityGateScriptsCleanStagedTicketOnInterrupt(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			readyPath := filepath.Join(dir, "ticket-ready")
			releasePath := filepath.Join(dir, "ticket-release")
			emergencyReleasePath := filepath.Join(dir, "ticket-emergency-release")
			harnessPath := filepath.Join(dir, "quality-gate.sh")
			harness := qualityGateSerialLaneHarness(t, tc.script, "")
			if err := os.WriteFile(harnessPath, []byte(harness), 0o755); err != nil {
				t.Fatalf("write quality gate harness: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			t.Cleanup(func() {
				_ = os.WriteFile(emergencyReleasePath, []byte("emergency release\n"), 0o644)
			})
			var output bytes.Buffer
			cmd := exec.CommandContext(ctx, harnessPath) //nolint:gosec // test-owned temp script
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"ORO_QG_TEST_NAME=early",
				"ORO_QG_TEST_BASH_ENV="+qualityGateTicketPublicationBarrierEnv(t),
				"ORO_QG_TEST_TICKET_READY="+readyPath,
				"ORO_QG_TEST_TICKET_RELEASE="+releasePath,
				"ORO_QG_TEST_TICKET_EMERGENCY_RELEASE="+emergencyReleasePath,
			)
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatalf("start interrupted waiter: %v", err)
			}
			if !waitForQualityGatePath(readyPath, 2*time.Second) {
				t.Fatalf("waiter did not reach ticket publication barrier:\n%s", output.String())
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatalf("interrupt staged waiter: %v", err)
			}
			err := cmd.Wait()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
				t.Fatalf("interrupted waiter exit = %v, want status 130\n%s", err, output.String())
			}

			staging, err := filepath.Glob(filepath.Join(dir, ".oro-quality-gate.queue.staging.*"))
			if err != nil {
				t.Fatalf("glob staged tickets: %v", err)
			}
			if len(staging) != 0 {
				t.Fatalf("interrupted waiter left staged tickets: %v", staging)
			}
			if _, err := os.Stat(filepath.Join(dir, ".oro-quality-gate.queue")); !os.IsNotExist(err) {
				t.Fatalf("interrupted waiter left queue directory: %v", err)
			}
		})
	}
}

func qualityGateTicketPublicationBarrierEnv(t *testing.T) string {
	t.Helper()
	return writeQualityGateTestBashEnv(t, `
pause_early_ticket_publication() {
    [ "${ORO_QG_TEST_NAME:-}" = "early" ] || return 0
    [ "${ORO_QG_TEST_TICKET_BARRIER_USED:-}" != "1" ] || return 0
    ORO_QG_TEST_TICKET_BARRIER_USED=1
    touch "$ORO_QG_TEST_TICKET_READY"
    while [ ! -f "$ORO_QG_TEST_TICKET_RELEASE" ] && [ ! -f "$ORO_QG_TEST_TICKET_EMERGENCY_RELEASE" ]; do
        sleep 0.01
    done
}
mkdir() {
    command mkdir "$@"
    local status=$?
    [ "$status" -eq 0 ] || return "$status"
    local target="${!#}"
    case "$target" in
        "$PWD/.oro-quality-gate.queue/"*) pause_early_ticket_publication ;;
    esac
}
mv() {
    local target="${!#}"
    case "$target" in
        "$PWD/.oro-quality-gate.queue/"*) pause_early_ticket_publication ;;
    esac
    command mv "$@"
}
`)
}

func TestQualityGateRunLockRecoveryPreservesLiveOwnerThenClearsRecursiveQueue(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events")

	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()
	harness := qualityGateSerialLaneHarness(t, script, `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`)

	earlyScript := filepath.Join(dir, "early.sh")
	if err := os.WriteFile(earlyScript, []byte(harness), 0o755); err != nil {
		t.Fatalf("write early harness: %v", err)
	}
	lateScript := filepath.Join(dir, "late.sh")
	if err := os.WriteFile(lateScript, []byte(harness), 0o755); err != nil {
		t.Fatalf("write late harness: %v", err)
	}

	lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatalf("create held lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(fmt.Sprintf("pid=%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatalf("write held lock owner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	early := exec.CommandContext(ctx, earlyScript) //nolint:gosec // test-owned temp script
	early.Dir = dir
	early.Env = append(os.Environ(),
		"ORO_QG_TEST_NAME=early",
		"ORO_QG_TEST_EVENTS="+eventsPath,
		"ORO_QG_LOCK_POLL_SECONDS=1",
		"ORO_QG_LOCK_TIMEOUT_SECONDS=8",
	)
	if err := early.Start(); err != nil {
		t.Fatalf("start early waiter: %v", err)
	}

	queueDir := filepath.Join(dir, ".oro-quality-gate.queue")
	if !waitForQualityGateQueueEntries(queueDir, 1, 2*time.Second) {
		_ = os.Remove(filepath.Join(lockDir, "owner"))
		_ = os.Remove(lockDir)
		_ = early.Wait()
		t.Fatalf("early waiter did not create a quality gate FIFO queue ticket")
	}

	late := exec.CommandContext(ctx, lateScript) //nolint:gosec // test-owned temp script
	late.Dir = dir
	late.Env = append(os.Environ(),
		"ORO_QG_TEST_NAME=late",
		"ORO_QG_TEST_EVENTS="+eventsPath,
		"ORO_QG_LOCK_POLL_SECONDS=1",
		"ORO_QG_LOCK_TIMEOUT_SECONDS=8",
	)
	if err := late.Start(); err != nil {
		t.Fatalf("start late waiter: %v", err)
	}
	if !waitForQualityGateQueueEntries(queueDir, 2, 2*time.Second) {
		_ = os.Remove(filepath.Join(lockDir, "owner"))
		_ = os.Remove(lockDir)
		_ = early.Wait()
		_ = late.Wait()
		t.Fatalf("late waiter did not join quality gate FIFO queue")
	}
	owner, err := os.ReadFile(filepath.Join(lockDir, "owner"))
	if err != nil {
		t.Fatalf("read live lock owner while waiters are queued: %v", err)
	}
	if !strings.Contains(string(owner), fmt.Sprintf("pid=%d\n", os.Getpid())) {
		t.Fatalf("live lock owner changed while waiters were queued: %q", owner)
	}

	if err := os.Remove(filepath.Join(lockDir, "owner")); err != nil {
		t.Fatalf("remove held lock owner: %v", err)
	}
	if err := os.Remove(lockDir); err != nil {
		t.Fatalf("release held lock: %v", err)
	}

	if err := early.Wait(); err != nil {
		t.Fatalf("early waiter failed: %v", err)
	}
	if err := late.Wait(); err != nil {
		t.Fatalf("late waiter failed: %v", err)
	}

	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read acquisition events: %v", err)
	}
	got := strings.Fields(string(events))
	if !reflect.DeepEqual(got, []string{"early", "late"}) {
		t.Fatalf("quality gate acquisition order = %v, want FIFO early before late", got)
	}
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Fatalf("quality gate lock should be cleared after queued waiters finish, stat err=%v", err)
	}
	if _, err := os.Stat(queueDir); !os.IsNotExist(err) {
		t.Fatalf("quality gate queue should be cleared after queued waiters finish, stat err=%v", err)
	}
}

func TestQualityGateScriptsQueuedWaiterRunsLanesAndEmitsFinalSummaryAfterLockRelease(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			harness := qualityGateSerialLaneHarness(t, tc.script, `
echo "lane: synthetic"
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " SUMMARY"
echo "═══════════════════════════════════════════════════════════════"
echo "Quality gate PASSED"
`)
			scriptPath := filepath.Join(dir, "quality_gate.sh")
			if err := os.WriteFile(scriptPath, []byte(harness), 0o755); err != nil {
				t.Fatalf("write quality gate harness: %v", err)
			}

			lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create held lock: %v", err)
			}
			if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(fmt.Sprintf("pid=%d\n", os.Getpid())), 0o644); err != nil {
				t.Fatalf("write live lock owner: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, scriptPath) //nolint:gosec // test-owned temp script
			outputPath := filepath.Join(dir, "quality-gate.out")
			outputFile, err := os.Create(outputPath)
			if err != nil {
				t.Fatalf("create quality gate output: %v", err)
			}
			defer outputFile.Close()
			cmd.Stdout = outputFile
			cmd.Stderr = outputFile
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"ORO_QG_LOCK_POLL_SECONDS=1",
				"ORO_QG_LOCK_TIMEOUT_SECONDS=6",
			)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start queued quality gate: %v", err)
			}

			queueDir := filepath.Join(dir, ".oro-quality-gate.queue")
			if !waitForQualityGateQueueEntries(queueDir, 1, 2*time.Second) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("queued quality gate did not create a FIFO ticket")
			}
			if !waitForQualityGateOutput(outputPath, "Waiting for another quality gate to finish...", 2*time.Second) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("queued quality gate did not report waiting for the live lock")
			}
			if err := os.RemoveAll(lockDir); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("release held lock: %v", err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("queued quality gate failed after lock release: %v", err)
			}
			if err := outputFile.Close(); err != nil {
				t.Fatalf("close quality gate output: %v", err)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read quality gate output: %v", err)
			}

			for _, want := range []string{
				"Waiting for another quality gate to finish...",
				"lane: synthetic",
				"SUMMARY",
				"Quality gate PASSED",
			} {
				if !strings.Contains(string(output), want) {
					t.Fatalf("queued quality gate output missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestCheckedInQualityGateStartsAfterLiveFIFOQueueDrains(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events")

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}
	harness := qualityGateSerialLaneHarness(t, string(script), `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`)

	earlyScript := filepath.Join(dir, "early.sh")
	if err := os.WriteFile(earlyScript, []byte(harness), 0o755); err != nil {
		t.Fatalf("write early harness: %v", err)
	}
	lateScript := filepath.Join(dir, "late.sh")
	if err := os.WriteFile(lateScript, []byte(harness), 0o755); err != nil {
		t.Fatalf("write late harness: %v", err)
	}

	lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatalf("create held lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(fmt.Sprintf("pid=%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatalf("write held lock owner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	startWaiter := func(name, scriptPath string) *exec.Cmd {
		t.Helper()
		cmd := exec.CommandContext(ctx, scriptPath) //nolint:gosec // scriptPath is a test-owned temp file
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"ORO_QG_TEST_NAME="+name,
			"ORO_QG_TEST_EVENTS="+eventsPath,
			"ORO_QG_LOCK_POLL_SECONDS=1",
			"ORO_QG_LOCK_TIMEOUT_SECONDS=8",
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s waiter: %v", name, err)
		}
		return cmd
	}

	early := startWaiter("early", earlyScript)
	queueDir := filepath.Join(dir, ".oro-quality-gate.queue")
	if !waitForQualityGateQueueEntries(queueDir, 1, 2*time.Second) {
		t.Fatalf("early waiter did not create a quality gate FIFO queue ticket")
	}

	late := startWaiter("late", lateScript)
	if !waitForQualityGateQueueEntries(queueDir, 2, 2*time.Second) {
		t.Fatalf("late waiter did not join quality gate FIFO queue")
	}

	if err := os.RemoveAll(lockDir); err != nil {
		t.Fatalf("release held lock: %v", err)
	}
	if err := early.Wait(); err != nil {
		t.Fatalf("early waiter failed: %v", err)
	}
	if err := late.Wait(); err != nil {
		t.Fatalf("late waiter failed: %v", err)
	}

	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read acquisition events: %v", err)
	}
	got := strings.Fields(string(events))
	if !reflect.DeepEqual(got, []string{"early", "late"}) {
		t.Fatalf("checked-in quality gate acquisition order = %v, want FIFO early before late", got)
	}
}

func TestCheckedInQualityGatePreservesLiveOwnerThenClearsFIFOQueue(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events")

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}
	harness := qualityGateSerialLaneHarness(t, string(script), `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`)

	writeHarness := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name+".sh")
		if err := os.WriteFile(path, []byte(harness), 0o755); err != nil {
			t.Fatalf("write %s harness: %v", name, err)
		}
		return path
	}

	lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatalf("create held lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(fmt.Sprintf("pid=%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatalf("write held lock owner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startWaiter := func(name string) *exec.Cmd {
		t.Helper()
		cmd := exec.CommandContext(ctx, writeHarness(name)) //nolint:gosec // test-owned temp script
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"ORO_QG_TEST_NAME="+name,
			"ORO_QG_TEST_EVENTS="+eventsPath,
			"ORO_QG_LOCK_POLL_SECONDS=1",
			"ORO_QG_LOCK_TIMEOUT_SECONDS=8",
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s waiter: %v", name, err)
		}
		return cmd
	}

	early := startWaiter("early")
	queueDir := filepath.Join(dir, ".oro-quality-gate.queue")
	if !waitForQualityGateQueueEntries(queueDir, 1, 2*time.Second) {
		t.Fatalf("early waiter did not create a quality gate FIFO queue ticket")
	}
	late := startWaiter("late")
	if !waitForQualityGateQueueEntries(queueDir, 2, 2*time.Second) {
		t.Fatalf("late waiter did not join quality gate FIFO queue")
	}

	owner, err := os.ReadFile(filepath.Join(lockDir, "owner"))
	if err != nil {
		t.Fatalf("read live lock owner while waiters are queued: %v", err)
	}
	if !strings.Contains(string(owner), fmt.Sprintf("pid=%d\n", os.Getpid())) {
		t.Fatalf("live lock owner changed while waiters were queued: %q", owner)
	}

	if err := os.Remove(filepath.Join(lockDir, "owner")); err != nil {
		t.Fatalf("remove held lock owner: %v", err)
	}
	if err := os.Remove(lockDir); err != nil {
		t.Fatalf("release held lock: %v", err)
	}
	if err := early.Wait(); err != nil {
		t.Fatalf("early waiter failed: %v", err)
	}
	if err := late.Wait(); err != nil {
		t.Fatalf("late waiter failed: %v", err)
	}

	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read acquisition events: %v", err)
	}
	if got := strings.Fields(string(events)); !reflect.DeepEqual(got, []string{"early", "late"}) {
		t.Fatalf("checked-in quality gate acquisition order = %v, want FIFO early before late", got)
	}
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Fatalf("quality gate lock should be cleared after queued waiters finish, stat err=%v", err)
	}
	if _, err := os.Stat(queueDir); !os.IsNotExist(err) {
		t.Fatalf("quality gate queue should be cleared after queued waiters finish, stat err=%v", err)
	}
}

func TestQualityGateScriptsTimedOutWaiterPreservesLiveOwnerAndQueueProgress(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			canonicalDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatalf("resolve temp directory: %v", err)
			}
			eventsPath := filepath.Join(dir, "events")
			harness := qualityGateSerialLaneHarness(t, tc.script, `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`)

			writeHarness := func(name string) string {
				t.Helper()
				path := filepath.Join(dir, name+".sh")
				if err := os.WriteFile(path, []byte(harness), 0o755); err != nil {
					t.Fatalf("write %s harness: %v", name, err)
				}
				return path
			}
			startWaiter := func(ctx context.Context, name string, timeout int) (*exec.Cmd, *bytes.Buffer) {
				t.Helper()
				cmd := exec.CommandContext(ctx, writeHarness(name)) //nolint:gosec // test-owned temp script
				output := &bytes.Buffer{}
				cmd.Stdout = output
				cmd.Stderr = output
				cmd.Dir = dir
				cmd.Env = append(os.Environ(),
					"ORO_QG_TEST_NAME="+name,
					"ORO_QG_TEST_EVENTS="+eventsPath,
					"ORO_QG_LOCK_POLL_SECONDS=1",
					fmt.Sprintf("ORO_QG_LOCK_TIMEOUT_SECONDS=%d", timeout),
				)
				if err := cmd.Start(); err != nil {
					t.Fatalf("start %s waiter: %v", name, err)
				}
				t.Cleanup(func() {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				})
				return cmd, output
			}

			lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create held lock: %v", err)
			}
			ownerPath := filepath.Join(lockDir, "owner")
			liveOwner := fmt.Sprintf("pid=%d\n", os.Getpid())
			if err := os.WriteFile(ownerPath, []byte(liveOwner), 0o644); err != nil {
				t.Fatalf("write held lock owner: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			timedOut, timedOutOutput := startWaiter(ctx, "timed-out", 1)
			queueDir := filepath.Join(dir, ".oro-quality-gate.queue")
			if !waitForQualityGateQueueEntries(queueDir, 1, 2*time.Second) {
				t.Fatal("timed-out waiter did not create a quality gate FIFO queue ticket")
			}
			later, laterOutput := startWaiter(ctx, "later", 8)
			if !waitForQualityGateQueueEntries(queueDir, 2, 2*time.Second) {
				t.Fatal("later waiter did not join quality gate FIFO queue")
			}
			waitErr := timedOut.Wait()
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("timed-out waiter exit = %v, want status 1; output:\n%s", waitErr, timedOutOutput)
			}
			wantTimeout := "FAIL: timed out waiting for quality gate lock: " + filepath.Join(canonicalDir, ".oro-quality-gate.lock")
			timeoutLineFound := false
			for _, line := range strings.Split(strings.TrimSpace(timedOutOutput.String()), "\n") {
				if line == wantTimeout {
					timeoutLineFound = true
					break
				}
			}
			if !timeoutLineFound {
				t.Fatalf("timed-out waiter output missing %q:\n%s", wantTimeout, timedOutOutput)
			}

			owner, err := os.ReadFile(ownerPath)
			if err != nil {
				t.Fatalf("read live owner after timed-out waiter exits: %v", err)
			}
			if string(owner) != liveOwner {
				t.Fatalf("live lock owner after timed-out waiter = %q, want %q", owner, liveOwner)
			}
			if !waitForQualityGateQueueEntries(queueDir, 1, time.Second) {
				t.Fatal("later waiter queue ticket disappeared after an earlier waiter timed out")
			}

			if err := os.Remove(ownerPath); err != nil {
				t.Fatalf("remove held lock owner: %v", err)
			}
			if err := os.Remove(lockDir); err != nil {
				t.Fatalf("release held lock: %v", err)
			}
			if err := later.Wait(); err != nil {
				t.Fatalf("later waiter failed after owner released lock: %v; output:\n%s", err, laterOutput)
			}

			events, err := os.ReadFile(eventsPath)
			if err != nil {
				t.Fatalf("read acquisition events: %v", err)
			}
			if got := strings.Fields(string(events)); !reflect.DeepEqual(got, []string{"later"}) {
				t.Fatalf("quality gate acquisition events = %v, want later waiter to progress", got)
			}
			if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
				t.Fatalf("quality gate lock should be cleared after later waiter finishes, stat err=%v", err)
			}
			if _, err := os.Stat(queueDir); !os.IsNotExist(err) {
				t.Fatalf("quality gate queue should be cleared after later waiter finishes, stat err=%v", err)
			}
		})
	}
}

func TestQualityGateScriptsOrphanedLiveOwnerDoesNotBlockFIFOAndHealthyLiveOwnerIsPreserved(t *testing.T) {
	var generated bytes.Buffer
	if err := writeQualityGateScript(&generated, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "generated", script: generated.String()},
		{name: "checked-in", script: string(checkedIn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			harnessPath := filepath.Join(dir, "acquire.sh")
			harness := qualityGateSerialLaneHarness(t, tc.script, "echo acquired\n")
			if err := os.WriteFile(harnessPath, []byte(harness), 0o755); err != nil {
				t.Fatalf("write quality gate harness: %v", err)
			}

			lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
			reusedPID, reusedStartTime := startOrphanedQualityGateTestProcess(t, false)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create orphan-held lock: %v", err)
			}
			if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(qualityGateLockOwner(reusedPID, "not-"+reusedStartTime)), 0o644); err != nil {
				t.Fatalf("write PID-reused lock owner: %v", err)
			}
			orphan := exec.Command(harnessPath) //nolint:gosec // test-owned temp script
			orphan.Dir = dir
			orphan.Env = append(os.Environ(), "ORO_QG_LOCK_POLL_SECONDS=1", "ORO_QG_LOCK_TIMEOUT_SECONDS=2")
			orphanOutput, err := orphan.CombinedOutput()
			if err != nil {
				t.Fatalf("PID-reused live owner should be recovered: %v\n%s", err, orphanOutput)
			}
			if !strings.Contains(string(orphanOutput), "acquired") {
				t.Fatalf("PID-reused live owner did not allow FIFO head to acquire:\n%s", orphanOutput)
			}

			youngPID, youngStartTime := startOrphanedQualityGateTestProcess(t, false)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create young detached lock: %v", err)
			}
			youngOwner := qualityGateLockOwner(youngPID, youngStartTime)
			ownerPath := filepath.Join(lockDir, "owner")
			if err := os.WriteFile(ownerPath, []byte(youngOwner), 0o644); err != nil {
				t.Fatalf("write young detached lock owner: %v", err)
			}
			assertQualityGateWaiterPreservesOwner(t, harnessPath, dir, ownerPath, youngOwner, 60, "young detached owner")
			if err := os.RemoveAll(lockDir); err != nil {
				t.Fatalf("remove young detached lock: %v", err)
			}

			workingPID, workingStartTime := startOrphanedQualityGateTestProcess(t, true)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create working detached lock: %v", err)
			}
			workingOwner := qualityGateLockOwner(workingPID, workingStartTime)
			if err := os.WriteFile(ownerPath, []byte(workingOwner), 0o644); err != nil {
				t.Fatalf("write working detached lock owner: %v", err)
			}
			old := time.Now().Add(-2 * time.Second)
			if err := os.Chtimes(lockDir, old, old); err != nil {
				t.Fatalf("age working detached lock: %v", err)
			}
			assertQualityGateWaiterPreservesOwner(t, harnessPath, dir, ownerPath, workingOwner, 1, "working detached owner")
			if err := os.RemoveAll(lockDir); err != nil {
				t.Fatalf("remove working detached lock: %v", err)
			}

			stalePID, staleStartTime := startOrphanedQualityGateTestProcess(t, false)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create stale detached lock: %v", err)
			}
			if err := os.WriteFile(ownerPath, []byte(qualityGateLockOwner(stalePID, staleStartTime)), 0o644); err != nil {
				t.Fatalf("write stale detached lock owner: %v", err)
			}
			if err := os.Chtimes(lockDir, old, old); err != nil {
				t.Fatalf("age stale detached lock: %v", err)
			}
			stale := exec.Command(harnessPath) //nolint:gosec // test-owned temp script
			stale.Dir = dir
			stale.Env = append(os.Environ(), "ORO_QG_LOCK_POLL_SECONDS=1", "ORO_QG_LOCK_TIMEOUT_SECONDS=2", "ORO_QG_STALE_LOCK_SECONDS=1")
			staleOutput, err := stale.CombinedOutput()
			if err != nil || !strings.Contains(string(staleOutput), "acquired") {
				t.Fatalf("old detached owner without descendants should be recovered: %v\n%s", err, staleOutput)
			}

			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create healthy-held lock: %v", err)
			}
			healthyOwner := fmt.Sprintf("pid=%d\n", os.Getpid())
			if err := os.WriteFile(ownerPath, []byte(healthyOwner), 0o644); err != nil {
				t.Fatalf("write healthy lock owner: %v", err)
			}
			healthy := exec.Command(harnessPath) //nolint:gosec // test-owned temp script
			healthy.Dir = dir
			healthy.Env = append(os.Environ(), "ORO_QG_LOCK_POLL_SECONDS=1", "ORO_QG_LOCK_TIMEOUT_SECONDS=1")
			healthyOutput, err := healthy.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("healthy owner waiter exit = %v, want status 1; output:\n%s", err, healthyOutput)
			}
			owner, err := os.ReadFile(ownerPath)
			if err != nil {
				t.Fatalf("read healthy owner after waiter exits: %v", err)
			}
			if string(owner) != healthyOwner {
				t.Fatalf("healthy live owner changed to %q, want %q", owner, healthyOwner)
			}
			if err := os.RemoveAll(lockDir); err != nil {
				t.Fatalf("remove healthy-held lock: %v", err)
			}

			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create timezone-shifted healthy lock: %v", err)
			}
			timezoneOwner := qualityGateLockOwner(os.Getpid(), qualityGateProcessStartTime(t, os.Getpid()))
			if err := os.WriteFile(ownerPath, []byte(timezoneOwner), 0o644); err != nil {
				t.Fatalf("write timezone-shifted healthy lock owner: %v", err)
			}
			assertQualityGateWaiterPreservesOwnerWithEnv(t, harnessPath, dir, ownerPath, timezoneOwner, 1, "timezone-shifted healthy owner", []string{
				"LC_ALL=POSIX",
				"TZ=America/New_York",
			})
			if err := os.RemoveAll(lockDir); err != nil {
				t.Fatalf("remove timezone-shifted healthy lock: %v", err)
			}

			failingBashEnv := writeQualityGateTestBashEnv(t, `
ps() {
    if [ "$1" = "-o" ] && [ "$2" = "lstart=" ]; then
        return 1
    fi
    command ps "$@"
}
`)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create identity-lookup failure lock: %v", err)
			}
			lookupFailureOwner := qualityGateLockOwner(os.Getpid(), "Fri Jul 17 01:54:21 2026")
			if err := os.WriteFile(ownerPath, []byte(lookupFailureOwner), 0o644); err != nil {
				t.Fatalf("write identity-lookup failure lock owner: %v", err)
			}
			assertQualityGateWaiterPreservesOwnerWithEnv(t, harnessPath, dir, ownerPath, lookupFailureOwner, 1, "identity lookup failure", []string{
				"ORO_QG_TEST_BASH_ENV=" + failingBashEnv,
			})
			if err := os.RemoveAll(lockDir); err != nil {
				t.Fatalf("remove identity-lookup failure lock: %v", err)
			}

			for _, probeExit := range []int{2, 127} {
				probeBashEnv := writeQualityGateTestBashEnv(t, fmt.Sprintf(`
pgrep() {
    if [ "$1" = "-P" ]; then
        return %d
    fi
    command pgrep "$@"
}
`, probeExit))
				probePID, probeStartTime := startOrphanedQualityGateTestProcess(t, false)
				if err := os.Mkdir(lockDir, 0o755); err != nil {
					t.Fatalf("create descendant-probe failure lock for exit %d: %v", probeExit, err)
				}
				probeOwner := qualityGateLockOwner(probePID, probeStartTime)
				if err := os.WriteFile(ownerPath, []byte(probeOwner), 0o644); err != nil {
					t.Fatalf("write descendant-probe failure owner for exit %d: %v", probeExit, err)
				}
				if err := os.Chtimes(lockDir, old, old); err != nil {
					t.Fatalf("age descendant-probe failure lock for exit %d: %v", probeExit, err)
				}
				assertQualityGateWaiterPreservesOwnerWithEnv(t, harnessPath, dir, ownerPath, probeOwner, 1, fmt.Sprintf("descendant probe exit %d", probeExit), []string{
					"ORO_QG_TEST_BASH_ENV=" + probeBashEnv,
				})
				if err := os.RemoveAll(lockDir); err != nil {
					t.Fatalf("remove descendant-probe failure lock for exit %d: %v", probeExit, err)
				}
			}

			confirmedPID, confirmedStartTime := startOrphanedQualityGateTestProcess(t, false)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create confirmed-no-descendants lock: %v", err)
			}
			if err := os.WriteFile(ownerPath, []byte(qualityGateLockOwner(confirmedPID, confirmedStartTime)), 0o644); err != nil {
				t.Fatalf("write confirmed-no-descendants owner: %v", err)
			}
			if err := os.Chtimes(lockDir, old, old); err != nil {
				t.Fatalf("age confirmed-no-descendants lock: %v", err)
			}
			confirmedBashEnv := writeQualityGateTestBashEnv(t, `
pgrep() {
    if [ "$1" = "-P" ]; then
        return 1
    fi
    command pgrep "$@"
}
`)
			confirmed := exec.Command(harnessPath) //nolint:gosec // test-owned temp script
			confirmed.Dir = dir
			confirmed.Env = append(os.Environ(), "ORO_QG_TEST_BASH_ENV="+confirmedBashEnv, "ORO_QG_LOCK_POLL_SECONDS=1", "ORO_QG_LOCK_TIMEOUT_SECONDS=2", "ORO_QG_STALE_LOCK_SECONDS=1")
			confirmedOutput, err := confirmed.CombinedOutput()
			if err != nil || !strings.Contains(string(confirmedOutput), "acquired") {
				t.Fatalf("confirmed absence of descendants should reclaim old detached owner: %v\n%s", err, confirmedOutput)
			}

			eventsPath := filepath.Join(dir, "fifo-events")
			fifoHarnessPath := filepath.Join(dir, "fifo-acquire.sh")
			fifoHarness := qualityGateSerialLaneHarness(t, tc.script, `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`)
			if err := os.WriteFile(fifoHarnessPath, []byte(fifoHarness), 0o755); err != nil {
				t.Fatalf("write FIFO quality gate harness: %v", err)
			}
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create healthy-held FIFO lock: %v", err)
			}
			healthyFIFOOwner := fmt.Sprintf("pid=%d\n", os.Getpid())
			if err := os.WriteFile(ownerPath, []byte(healthyFIFOOwner), 0o644); err != nil {
				t.Fatalf("write healthy-held FIFO owner: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			startWaiter := func(name string) *exec.Cmd {
				t.Helper()
				waiter := exec.CommandContext(ctx, fifoHarnessPath) //nolint:gosec // test-owned temp script
				waiter.Dir = dir
				waiter.Env = append(os.Environ(),
					"ORO_QG_TEST_NAME="+name,
					"ORO_QG_TEST_EVENTS="+eventsPath,
					"ORO_QG_LOCK_POLL_SECONDS=1",
					"ORO_QG_LOCK_TIMEOUT_SECONDS=8",
				)
				if err := waiter.Start(); err != nil {
					t.Fatalf("start %s FIFO waiter: %v", name, err)
				}
				return waiter
			}
			early := startWaiter("early")
			queueDir := filepath.Join(dir, ".oro-quality-gate.queue")
			if !waitForQualityGateQueueEntries(queueDir, 1, 2*time.Second) {
				t.Fatal("early FIFO waiter did not create a queue ticket")
			}
			late := startWaiter("late")
			if !waitForQualityGateQueueEntries(queueDir, 2, 2*time.Second) {
				t.Fatal("late FIFO waiter did not join the queue")
			}
			if err := os.RemoveAll(lockDir); err != nil {
				t.Fatalf("release healthy FIFO lock: %v", err)
			}
			if err := early.Wait(); err != nil {
				t.Fatalf("early FIFO waiter failed after healthy-owner release: %v", err)
			}
			if err := late.Wait(); err != nil {
				t.Fatalf("late FIFO waiter failed after early waiter release: %v", err)
			}
			events, err := os.ReadFile(eventsPath)
			if err != nil {
				t.Fatalf("read FIFO acquisition events: %v", err)
			}
			if got := strings.Fields(string(events)); !reflect.DeepEqual(got, []string{"early", "late"}) {
				t.Fatalf("FIFO acquisition order = %v, want early then late", got)
			}
		})
	}
}

func assertQualityGateWaiterPreservesOwner(t *testing.T, harnessPath, dir, ownerPath, wantOwner string, staleAfter int, name string) {
	t.Helper()
	assertQualityGateWaiterPreservesOwnerWithEnv(t, harnessPath, dir, ownerPath, wantOwner, staleAfter, name, nil)
}

func assertQualityGateWaiterPreservesOwnerWithEnv(t *testing.T, harnessPath, dir, ownerPath, wantOwner string, staleAfter int, name string, extraEnv []string) {
	t.Helper()
	waiter := exec.Command(harnessPath) //nolint:gosec // test-owned temp script
	waiter.Dir = dir
	waiter.Env = append(os.Environ(),
		"ORO_QG_LOCK_POLL_SECONDS=1",
		"ORO_QG_LOCK_TIMEOUT_SECONDS=1",
		fmt.Sprintf("ORO_QG_STALE_LOCK_SECONDS=%d", staleAfter),
	)
	waiter.Env = replaceQualityGateTestEnv(waiter.Env, extraEnv)
	output, err := waiter.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("%s waiter exit = %v, want status 1; output:\n%s", name, err, output)
	}
	owner, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatalf("read %s after waiter exits: %v", name, err)
	}
	if string(owner) != wantOwner {
		t.Fatalf("%s changed to %q, want %q", name, owner, wantOwner)
	}
}

func replaceQualityGateTestEnv(env, replacements []string) []string {
	keys := make(map[string]struct{}, len(replacements))
	for _, replacement := range replacements {
		key, _, ok := strings.Cut(replacement, "=")
		if ok {
			keys[key] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(env)+len(replacements))
	for _, value := range env {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if _, replaced := keys[key]; !replaced {
			filtered = append(filtered, value)
		}
	}
	return append(filtered, replacements...)
}

func writeQualityGateTestBashEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bash-env")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write test ps: %v", err)
	}
	return path
}

func qualityGateLockOwner(pid int, startTime string) string {
	return fmt.Sprintf("pid=%d\nstart_time=%s\n", pid, startTime)
}

func startOrphanedQualityGateTestProcess(t *testing.T, withDescendant bool) (int, string) {
	t.Helper()
	command := "sleep 30 >/dev/null 2>&1 & printf '%s\\n' \"$!\""
	if withDescendant {
		command = "sh -c 'sleep 30 >/dev/null 2>&1 & wait' >/dev/null 2>&1 & printf '%s\\n' \"$!\""
	}
	cmd := exec.Command("sh", "-c", command) //nolint:gosec // fixed test helper command
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("start orphaned process: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("parse orphaned process PID %q: %v", output, err)
	}
	startTime := qualityGateProcessStartTime(t, pid)
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-P", strconv.Itoa(pid)).Run() //nolint:gosec // pid belongs to the test-owned detached process.
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = process.Kill()
		}
	})
	return pid, startTime
}

func qualityGateProcessStartTime(t *testing.T, pid int) string {
	t.Helper()
	output, err := exec.Command("env", "LC_ALL=C", "TZ=UTC", "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid belongs to a test-owned process.
	if err != nil {
		t.Fatalf("read process start time for PID %d: %v", pid, err)
	}
	startTime := strings.TrimSpace(string(output))
	if startTime == "" {
		t.Fatalf("empty process start time for PID %d", pid)
	}
	return startTime
}

func waitForQualityGateQueueEntries(queueDir string, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(queueDir)
		if err == nil && len(entries) >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func waitForQualityGatePath(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func waitForQualityGateOutput(path, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(output), want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
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

// TestGeneratedQualityGateScopesLintCache proves the generated quality gate
// retains shared Go, uv, and module caches while isolating golangci-lint.
// The lint cache can retain absolute paths from sibling worktrees, unlike the
// other caches that should remain shared to avoid cold-compiling each gate.
func TestGeneratedQualityGateScopesLintCache(t *testing.T) {
	cfg := &langprofile.Config{
		Languages: map[string]langprofile.LanguageConfig{
			"go":     {TestCmd: "go test ./...", Linters: []string{"golangci-lint"}},
			"python": {TestCmd: "uv run pytest", Linters: []string{"ruff"}},
		},
	}
	generated, err := generateQualityGateScript(cfg)
	if err != nil {
		t.Fatalf("generate quality gate: %v", err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}

	// Only the lint cache may be redirected under QG_DIR. Fresh Go and uv
	// caches would cold-compile or reinstall dependencies for every gate.
	forbidden := regexp.MustCompile(`\$QG_DIR/(go-build-cache|golangci-go-cache|uv-cache)|QG_DIR.*(go-build-cache|golangci-go-cache|uv-cache)`)
	for name, script := range map[string]string{
		"generated":  generated,
		"checked-in": string(checkedIn),
	} {
		t.Run(name, func(t *testing.T) {
			if loc := forbidden.FindString(script); loc != "" {
				t.Errorf("quality gate redirects a shared tool cache under QG_DIR (%q)", loc)
			}
			if !strings.Contains(script, `GOLANGCI_LINT_CACHE="$lint_cache"`) {
				t.Error("quality gate does not scope golangci-lint cache")
			}
			if !strings.Contains(script, "Tool caches deliberately inherit their environment") {
				t.Error("quality gate missing the shared-cache inheritance contract")
			}
		})
	}
}

// TestGoLanesScopeOutUntrackedArchive guards against the Go build/vet/govulncheck
// lanes descending into archive/ — a gitignored, untracked tree of intentionally
// broken Go fixtures that fails the gate on cruft that can never be pushed.
//
// The fix lives in the checked-in script only: generated gates for other projects
// have no archive/ and must keep bare ./... , so the template is intentionally
// exempt from this assertion.
func TestGoLanesScopeOutUntrackedArchive(t *testing.T) {
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}
	script := string(checkedIn)

	// The Go build/vet/govulncheck lanes must not walk the whole tree with bare
	// ./... , which pulls in untracked archive/ fixtures.
	for _, forbidden := range []string{
		`go build -buildvcs=false ./...`,
		`go vet ./...`,
		`govulncheck ./...`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("quality gate still runs a bare-tree lane %q; scope it away from untracked archive/", forbidden)
		}
	}

	// The scoped lanes must cover every real tracked module subtree.
	for _, want := range []string{
		"./cmd/... ./internal/... ./pkg/... ./tests/...",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("quality gate missing scoped Go package set %q", want)
		}
	}
}
