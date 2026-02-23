package main

import (
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
