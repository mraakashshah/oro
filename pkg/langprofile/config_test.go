package langprofile_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/langprofile"
)

func TestGenerateConfig(t *testing.T) {
	t.Run("detects go project when go.mod present", func(t *testing.T) {
		tmpDir := t.TempDir()
		//nolint:gosec // Test file permissions are acceptable
		if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example\n\ngo 1.21\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := langprofile.GenerateConfig(tmpDir, []langprofile.LangProfile{langprofile.GoProfile()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		goCfg, ok := cfg.Languages["go"]
		if !ok {
			t.Fatal("expected 'go' language in config, not found")
		}
		if goCfg.TestCmd != "go test ./..." {
			t.Errorf("expected TestCmd 'go test ./...', got %q", goCfg.TestCmd)
		}
		if len(goCfg.Formatters) == 0 {
			t.Fatal("expected at least one formatter for go")
		}
		if goCfg.Formatters[0] != "gofumpt" {
			t.Errorf("expected formatter 'gofumpt', got %q", goCfg.Formatters[0])
		}
		if len(goCfg.Linters) == 0 {
			t.Fatal("expected at least one linter for go")
		}
		if goCfg.Linters[0] != "golangci-lint" {
			t.Errorf("expected linter 'golangci-lint', got %q", goCfg.Linters[0])
		}
		if goCfg.Security != "govulncheck" {
			t.Errorf("expected security 'govulncheck', got %q", goCfg.Security)
		}
		if len(goCfg.CodingRules) == 0 {
			t.Error("expected coding rules for go")
		}
	})

	t.Run("returns empty config when no languages detected", func(t *testing.T) {
		tmpDir := t.TempDir() // empty directory — no language markers

		cfg, err := langprofile.GenerateConfig(tmpDir, langprofile.AllProfiles())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(cfg.Languages) != 0 {
			t.Errorf("expected empty Languages map, got %d entries", len(cfg.Languages))
		}
	})

	t.Run("detects python project when pyproject.toml present", func(t *testing.T) {
		tmpDir := t.TempDir()
		//nolint:gosec // Test file permissions are acceptable
		if err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte("[tool.poetry]\nname = \"test\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := langprofile.GenerateConfig(tmpDir, []langprofile.LangProfile{langprofile.PythonProfile()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		pythonCfg, ok := cfg.Languages["python"]
		if !ok {
			t.Fatal("expected 'python' language in config, not found")
		}
		if pythonCfg.TestCmd != "pytest" {
			t.Errorf("expected TestCmd 'pytest', got %q", pythonCfg.TestCmd)
		}
		if pythonCfg.TypeCheck != "pyright" {
			t.Errorf("expected TypeCheck 'pyright', got %q", pythonCfg.TypeCheck)
		}
	})

	t.Run("profile without optional fields produces no TypeCheck or Security", func(t *testing.T) {
		tmpDir := t.TempDir()
		//nolint:gosec // Test file permissions are acceptable
		if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := langprofile.GenerateConfig(tmpDir, []langprofile.LangProfile{langprofile.JavaScriptProfile()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		jsCfg, ok := cfg.Languages["javascript"]
		if !ok {
			t.Fatal("expected 'javascript' language in config, not found")
		}
		if jsCfg.TypeCheck != "" {
			t.Errorf("expected empty TypeCheck, got %q", jsCfg.TypeCheck)
		}
		if jsCfg.Security != "" {
			t.Errorf("expected empty Security, got %q", jsCfg.Security)
		}
	})
}

func TestBuildYAML(t *testing.T) {
	t.Run("empty config produces comment block with languages: {}", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{},
		}

		got := langprofile.BuildYAML(cfg)

		if !strings.Contains(got, "languages: {}") {
			t.Errorf("expected 'languages: {}' in output, got:\n%s", got)
		}
		if !strings.Contains(got, "# no languages detected") {
			t.Errorf("expected comment about no languages detected, got:\n%s", got)
		}
		if !strings.Contains(got, "oro init") {
			t.Errorf("expected hint to run 'oro init', got:\n%s", got)
		}
	})

	t.Run("populated config produces correct YAML for formatters and linters", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"go": {
					Formatters: []string{"gofumpt"},
					Linters:    []string{"golangci-lint"},
					TestCmd:    "go test ./...",
					Security:   "govulncheck",
					CodingRules: []string{
						"Use gofumpt for consistent formatting",
					},
				},
			},
		}

		got := langprofile.BuildYAML(cfg)

		expectations := []struct {
			label string
			want  string
		}{
			{"languages header", "languages:"},
			{"go entry", "  go:"},
			{"formatters header", "    formatters:"},
			{"gofumpt entry", "      - gofumpt"},
			{"linters header", "    linters:"},
			{"golangci-lint entry", "      - golangci-lint"},
			{"test_cmd", "    test_cmd: go test ./..."},
			{"security", "    security: govulncheck"},
			{"coding_rules header", "    coding_rules:"},
			{"coding rule entry", "      - Use gofumpt for consistent formatting"},
		}

		for _, e := range expectations {
			if !strings.Contains(got, e.want) {
				t.Errorf("expected %s %q in output, got:\n%s", e.label, e.want, got)
			}
		}
	})

	t.Run("config with TypeCheck produces type_check field", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"python": {
					Formatters: []string{"ruff"},
					TestCmd:    "pytest",
					TypeCheck:  "pyright",
				},
			},
		}

		got := langprofile.BuildYAML(cfg)

		if !strings.Contains(got, "    type_check: pyright") {
			t.Errorf("expected 'type_check: pyright' in output, got:\n%s", got)
		}
	})

	t.Run("config omits optional fields when empty", func(t *testing.T) {
		cfg := &langprofile.Config{
			Languages: map[string]langprofile.LanguageConfig{
				"go": {
					TestCmd: "go test ./...",
				},
			},
		}

		got := langprofile.BuildYAML(cfg)

		for _, absent := range []string{"formatters:", "linters:", "type_check:", "security:", "coding_rules:"} {
			if strings.Contains(got, absent) {
				t.Errorf("expected %q to be absent when empty, but found it in:\n%s", absent, got)
			}
		}
	})
}

func TestReadConfig(t *testing.T) {
	t.Run("returns config with languages and coding_rules from .oro/config.yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatal(err)
		}
		yaml := `languages:
  go:
    formatters:
      - gofumpt
    linters:
      - golangci-lint
    test_cmd: go test ./...
    coding_rules:
      - Use gofumpt for formatting
`
		//nolint:gosec // Test file permissions are acceptable
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := langprofile.ReadConfig(tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		goCfg, ok := cfg.Languages["go"]
		if !ok {
			t.Fatal("expected 'go' language in config")
		}
		if goCfg.TestCmd != "go test ./..." {
			t.Errorf("expected TestCmd 'go test ./...', got %q", goCfg.TestCmd)
		}
		if len(goCfg.CodingRules) == 0 {
			t.Error("expected coding_rules to be populated")
		}
		if goCfg.CodingRules[0] != "Use gofumpt for formatting" {
			t.Errorf("expected coding rule 'Use gofumpt for formatting', got %q", goCfg.CodingRules[0])
		}
	})

	t.Run("missing config returns nil,nil", func(t *testing.T) {
		tmpDir := t.TempDir() // no .oro/config.yaml

		cfg, err := langprofile.ReadConfig(tmpDir)
		if err != nil {
			t.Fatalf("expected nil error for missing config, got: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for missing file, got: %+v", cfg)
		}
	})

	t.Run("malformed YAML returns nil,err", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Test file permissions are acceptable
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(":\t invalid: [yaml"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := langprofile.ReadConfig(tmpDir)
		if err == nil {
			t.Fatal("expected error for malformed YAML, got nil")
		}
		if cfg != nil {
			t.Errorf("expected nil config on error, got: %+v", cfg)
		}
	})

	t.Run("empty projectRoot returns nil,ErrNoProjectRoot", func(t *testing.T) {
		cfg, err := langprofile.ReadConfig("")
		if err == nil {
			t.Fatal("expected ErrNoProjectRoot, got nil error")
		}
		if !errors.Is(err, langprofile.ErrNoProjectRoot) {
			t.Errorf("expected ErrNoProjectRoot, got: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config, got: %+v", cfg)
		}
	})
}

func TestResolveProjectRoot(t *testing.T) {
	t.Run("from worktree path returns main repo root", func(t *testing.T) {
		// Create a temporary git repo with a worktree so the test is self-contained.
		repoDir := t.TempDir()
		// On macOS, TempDir may return a /var symlink; resolve it so path
		// comparisons match what git returns (git resolves symlinks).
		repoDir, err := filepath.EvalSymlinks(repoDir)
		if err != nil {
			t.Fatal(err)
		}

		run := func(dir, name string, args ...string) {
			t.Helper()
			cmd := exec.Command(name, args...) //nolint:gosec // Test helper with controlled inputs
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
			}
		}

		// Init repo, disable hooks (prevent parent project hooks from firing),
		// and make an initial commit (worktree add requires a commit).
		run(repoDir, "git", "init")
		run(repoDir, "git", "config", "core.hooksPath", "/dev/null")
		run(repoDir, "git", "commit", "--allow-empty", "-m", "init")

		worktreeDir := filepath.Join(t.TempDir(), "wt")
		run(repoDir, "git", "worktree", "add", worktreeDir, "-b", "test-wt")
		t.Cleanup(func() {
			_ = exec.Command("git", "-C", repoDir, "worktree", "remove", "--force", worktreeDir).Run() //nolint:gosec // Test cleanup with controlled inputs
		})

		got, err := langprofile.ResolveProjectRoot(worktreeDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != repoDir {
			t.Errorf("expected %q, got %q", repoDir, got)
		}
	})

	t.Run("not in git repo returns path as-is", func(t *testing.T) {
		// Use a temp dir outside any git repo
		tmpDir := t.TempDir()

		got, err := langprofile.ResolveProjectRoot(tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != tmpDir {
			t.Errorf("expected path as-is %q, got %q", tmpDir, got)
		}
	})
}
