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
		if !strings.Contains(script, `exec /usr/bin/env bash "$0" "$@"`) {
			t.Error("script should exec Bash after bootstrap")
		}
		if !strings.Contains(script, "lane_go") {
			t.Error("go-only config should include lane_go function")
		}
		if strings.Contains(script, "lane_python") {
			t.Error("go-only config should not include lane_python function")
		}
		for _, want := range []string{
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
			`export GOLANGCI_LINT_CACHE="$QG_DIR/golangci-lint-cache"`,
			`GOCACHE=$QG_DIR/golangci-go-cache GOFLAGS=-buildvcs=false golangci-lint run`,
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
			`export GOCACHE="$QG_DIR/go-build-cache"`,
			`export UV_CACHE_DIR="${UV_CACHE_DIR:-$QG_DIR/uv-cache}"`,
			`export GOMAXPROCS="${ORO_QG_GOMAXPROCS:-2}"`,
		} {
			if !strings.Contains(script, want) {
				t.Errorf("generated Go script missing %q", want)
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
		if !strings.Contains(script, `exec /usr/bin/env bash "$0" "$@"`) {
			t.Error("script should exec Bash after bootstrap")
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
		if !strings.Contains(script, `exec /usr/bin/env bash "$0" "$@"`) {
			t.Error("script should exec Bash after bootstrap")
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

func TestQualityGateRunLockArchivesDeadOwnerAndStartsWithoutTimeout(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "quality_gate.sh")

	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()
	acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
	acquireIdx := strings.Index(script, acquireCall)
	if acquireIdx < 0 {
		t.Fatalf("generated quality gate missing acquire call marker")
	}
	harness := script[:acquireIdx+len("acquire_quality_gate_lock")] + "\n"
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
	acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
	acquireIdx := strings.Index(script, acquireCall)
	if acquireIdx < 0 {
		t.Fatalf("generated quality gate missing acquire call marker")
	}
	harness := script[:acquireIdx+len("acquire_quality_gate_lock")] + "\n"
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
			acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
			acquireIdx := strings.Index(tc.script, acquireCall)
			if acquireIdx < 0 {
				t.Fatalf("quality gate missing acquire call marker")
			}

			harness := tc.script[:acquireIdx+len("acquire_quality_gate_lock")] + `
if [ "${ORO_QG_RECURSIVE_TEST:-}" = "1" ]; then
    ORO_QG_RECURSIVE_TEST=0 "$0"
fi
if [ "${ORO_QG_RECURSIVE_TEST:-}" = "0" ]; then
    echo "recursive child reached quality gate body"
fi
`
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

func TestQualityGateRunLockRecoveryPreservesLiveOwnerThenClearsRecursiveQueue(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events")

	var buf bytes.Buffer
	if err := writeQualityGateScript(&buf, ProjectPaths{}); err != nil {
		t.Fatalf("writeQualityGateScript: %v", err)
	}
	script := buf.String()
	acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
	acquireIdx := strings.Index(script, acquireCall)
	if acquireIdx < 0 {
		t.Fatalf("generated quality gate missing acquire call marker")
	}
	harness := script[:acquireIdx+len("acquire_quality_gate_lock")] + `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`

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

func TestCheckedInQualityGateStartsAfterLiveFIFOQueueDrains(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events")

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("read checked-in quality gate: %v", err)
	}
	acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
	acquireIdx := strings.Index(string(script), acquireCall)
	if acquireIdx < 0 {
		t.Fatal("checked-in quality gate missing acquire call marker")
	}
	harness := string(script[:acquireIdx+len("acquire_quality_gate_lock")]) + `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`

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
	acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
	acquireIdx := strings.Index(string(script), acquireCall)
	if acquireIdx < 0 {
		t.Fatal("checked-in quality gate missing acquire call marker")
	}
	harness := string(script[:acquireIdx+len("acquire_quality_gate_lock")]) + `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`

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
			acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
			acquireIdx := strings.Index(tc.script, acquireCall)
			if acquireIdx < 0 {
				t.Fatal("quality gate missing acquire call marker")
			}
			harness := tc.script[:acquireIdx+len("acquire_quality_gate_lock")] + `
echo "$ORO_QG_TEST_NAME" >> "$ORO_QG_TEST_EVENTS"
`

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
			acquireCall := "acquire_quality_gate_lock\n\n# =============================================================================\n# PRIMITIVES"
			acquireIdx := strings.Index(tc.script, acquireCall)
			if acquireIdx < 0 {
				t.Fatal("quality gate missing acquire call marker")
			}
			harnessPath := filepath.Join(dir, "acquire.sh")
			harness := tc.script[:acquireIdx+len("acquire_quality_gate_lock")] + "\necho acquired\n"
			if err := os.WriteFile(harnessPath, []byte(harness), 0o755); err != nil {
				t.Fatalf("write quality gate harness: %v", err)
			}

			lockDir := filepath.Join(dir, ".oro-quality-gate.lock")
			orphanPID := startOrphanedQualityGateTestProcess(t)
			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create orphan-held lock: %v", err)
			}
			if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(fmt.Sprintf("pid=%d\n", orphanPID)), 0o644); err != nil {
				t.Fatalf("write orphan lock owner: %v", err)
			}
			orphan := exec.Command(harnessPath) //nolint:gosec // test-owned temp script
			orphan.Dir = dir
			orphan.Env = append(os.Environ(), "ORO_QG_LOCK_POLL_SECONDS=1", "ORO_QG_LOCK_TIMEOUT_SECONDS=2")
			orphanOutput, err := orphan.CombinedOutput()
			if err != nil {
				t.Fatalf("orphaned live owner should be recovered: %v\n%s", err, orphanOutput)
			}
			if !strings.Contains(string(orphanOutput), "acquired") {
				t.Fatalf("orphaned live owner did not allow FIFO head to acquire:\n%s", orphanOutput)
			}

			if err := os.Mkdir(lockDir, 0o755); err != nil {
				t.Fatalf("create healthy-held lock: %v", err)
			}
			ownerPath := filepath.Join(lockDir, "owner")
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
		})
	}
}

func startOrphanedQualityGateTestProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & printf '%s\\n' \"$!\"") //nolint:gosec // fixed test helper command
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("start orphaned process: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("parse orphaned process PID %q: %v", output, err)
	}
	t.Cleanup(func() {
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = process.Kill()
		}
	})
	return pid
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
