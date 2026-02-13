package langprofile_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/langprofile"
)

func TestDetectExistingTools(t *testing.T) {
	t.Run("detects eslint in package.json devDeps and uses it over biome", func(t *testing.T) {
		// Setup: create temp project with package.json containing eslint
		tmpDir := t.TempDir()
		packageJSON := `{
  "name": "test-project",
  "devDependencies": {
    "eslint": "^8.0.0"
  }
}`
		//nolint:gosec // Test file permissions are acceptable
		err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		// Create a baseline TypeScript profile (which would use biome by default)
		baseProfile := langprofile.LangProfile{
			Language: "typescript",
			Detect:   func(string) bool { return true },
			Linters: []langprofile.Tool{
				{
					Name:        "biome",
					Cmd:         "biome check .",
					DetectCmd:   "biome --version",
					InstallHint: "npm install -g @biomejs/biome",
				},
			},
			TestCmd: "npm test",
		}

		// Act: detect existing tools
		adapted := langprofile.DetectExistingTools(tmpDir, baseProfile)

		// Assert: should use eslint instead of biome
		if len(adapted.Linters) == 0 {
			t.Fatal("expected at least one linter")
		}
		if adapted.Linters[0].Name != "eslint" {
			t.Errorf("expected eslint, got %s", adapted.Linters[0].Name)
		}
	})

	t.Run("detects black in pyproject.toml and uses it over ruff format", func(t *testing.T) {
		// Setup: create temp project with pyproject.toml containing black
		tmpDir := t.TempDir()
		pyprojectToml := `[tool.black]
line-length = 88

[tool.poetry]
name = "test-project"
`
		//nolint:gosec // Test file permissions are acceptable
		err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte(pyprojectToml), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		// Create a baseline Python profile (which would use ruff format by default)
		baseProfile := langprofile.LangProfile{
			Language: "python",
			Detect:   func(string) bool { return true },
			Formatters: []langprofile.Tool{
				{
					Name:        "ruff",
					Cmd:         "ruff format .",
					DetectCmd:   "ruff --version",
					InstallHint: "pip install ruff",
				},
			},
			TestCmd: "pytest",
		}

		// Act: detect existing tools
		adapted := langprofile.DetectExistingTools(tmpDir, baseProfile)

		// Assert: should use black instead of ruff format
		if len(adapted.Formatters) == 0 {
			t.Fatal("expected at least one formatter")
		}
		if adapted.Formatters[0].Name != "black" {
			t.Errorf("expected black, got %s", adapted.Formatters[0].Name)
		}
	})

	t.Run("respects .oro/config.yaml overrides", func(t *testing.T) {
		// Setup: create temp project with .oro/config.yaml
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		//nolint:gosec // Test directory permissions are acceptable
		err := os.Mkdir(oroDir, 0o755)
		if err != nil {
			t.Fatal(err)
		}

		configYaml := `language: python
formatters:
  - name: yapf
    cmd: yapf -i -r .
`
		//nolint:gosec // Test file permissions are acceptable
		err = os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(configYaml), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		// Create a baseline Python profile
		baseProfile := langprofile.LangProfile{
			Language: "python",
			Detect:   func(string) bool { return true },
			Formatters: []langprofile.Tool{
				{
					Name:        "ruff",
					Cmd:         "ruff format .",
					DetectCmd:   "ruff --version",
					InstallHint: "pip install ruff",
				},
			},
			TestCmd: "pytest",
		}

		// Act: detect existing tools (should respect config override)
		adapted := langprofile.DetectExistingTools(tmpDir, baseProfile)

		// Assert: should use yapf from config
		if len(adapted.Formatters) == 0 {
			t.Fatal("expected at least one formatter")
		}
		if adapted.Formatters[0].Name != "yapf" {
			t.Errorf("expected yapf from config, got %s", adapted.Formatters[0].Name)
		}
	})

	t.Run("handles package.json parse error gracefully", func(t *testing.T) {
		// Setup: create temp project with invalid package.json
		tmpDir := t.TempDir()
		invalidJSON := `{invalid json`
		//nolint:gosec // Test file permissions are acceptable
		err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(invalidJSON), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		baseProfile := langprofile.LangProfile{
			Language: "typescript",
			Detect:   func(string) bool { return true },
			Linters: []langprofile.Tool{
				{Name: "biome", Cmd: "biome check ."},
			},
			TestCmd: "npm test",
		}

		// Act: should not panic, should return original profile
		adapted := langprofile.DetectExistingTools(tmpDir, baseProfile)

		// Assert: should skip JS detection and keep original
		if adapted.Linters[0].Name != "biome" {
			t.Errorf("expected original biome linter after parse error, got %s", adapted.Linters[0].Name)
		}
	})

	t.Run("handles pyproject.toml parse error gracefully", func(t *testing.T) {
		// Setup: create temp project with invalid pyproject.toml
		tmpDir := t.TempDir()
		invalidToml := `[invalid toml`
		//nolint:gosec // Test file permissions are acceptable
		err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte(invalidToml), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		baseProfile := langprofile.LangProfile{
			Language: "python",
			Detect:   func(string) bool { return true },
			Formatters: []langprofile.Tool{
				{Name: "ruff", Cmd: "ruff format ."},
			},
			TestCmd: "pytest",
		}

		// Act: should not panic, should return original profile
		adapted := langprofile.DetectExistingTools(tmpDir, baseProfile)

		// Assert: should skip Python detection and keep original
		if adapted.Formatters[0].Name != "ruff" {
			t.Errorf("expected original ruff formatter after parse error, got %s", adapted.Formatters[0].Name)
		}
	})
}
