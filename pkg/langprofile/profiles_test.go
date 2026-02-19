package langprofile_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/langprofile"
)

func TestGoProfile(t *testing.T) {
	profile := langprofile.GoProfile()

	// Validate required fields
	if err := profile.Validate(); err != nil {
		t.Fatalf("GoProfile validation failed: %v", err)
	}

	// Check language name
	if profile.Language != "go" {
		t.Errorf("Language = %q, want %q", profile.Language, "go")
	}

	// Check test command
	if profile.TestCmd == "" {
		t.Error("TestCmd should not be empty")
	}

	// Check formatters
	if len(profile.Formatters) == 0 {
		t.Error("Formatters should not be empty")
	}
	hasGofumpt := false
	for _, tool := range profile.Formatters {
		if tool.Name == "gofumpt" {
			hasGofumpt = true
			if tool.Cmd == "" {
				t.Error("gofumpt Cmd should not be empty")
			}
			if tool.DetectCmd == "" {
				t.Error("gofumpt DetectCmd should not be empty")
			}
			if tool.InstallHint == "" {
				t.Error("gofumpt InstallHint should not be empty")
			}
		}
	}
	if !hasGofumpt {
		t.Error("Formatters should include gofumpt")
	}

	// Check linters
	if len(profile.Linters) == 0 {
		t.Error("Linters should not be empty")
	}
	hasGolangciLint := false
	for _, tool := range profile.Linters {
		if tool.Name == "golangci-lint" {
			hasGolangciLint = true
		}
	}
	if !hasGolangciLint {
		t.Error("Linters should include golangci-lint")
	}

	// Check security tool
	if profile.Security == nil {
		t.Error("Security should be set")
	} else {
		if profile.Security.Name != "govulncheck" {
			t.Errorf("Security.Name = %q, want %q", profile.Security.Name, "govulncheck")
		}
	}

	// Test Detect function with go.mod
	t.Run("Detect_WithGoMod", func(t *testing.T) {
		tmpDir := t.TempDir()
		goModPath := filepath.Join(tmpDir, "go.mod")
		if err := os.WriteFile(goModPath, []byte("module test\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if !profile.Detect(tmpDir) {
			t.Error("Detect should return true when go.mod exists")
		}
	})

	// Test Detect function without go.mod
	t.Run("Detect_WithoutGoMod", func(t *testing.T) {
		tmpDir := t.TempDir()

		if profile.Detect(tmpDir) {
			t.Error("Detect should return false when go.mod does not exist")
		}
	})
}

func TestPythonProfile(t *testing.T) {
	profile := langprofile.PythonProfile()

	// Validate required fields
	if err := profile.Validate(); err != nil {
		t.Fatalf("PythonProfile validation failed: %v", err)
	}

	// Check language name
	if profile.Language != "python" {
		t.Errorf("Language = %q, want %q", profile.Language, "python")
	}

	// Check test command
	if profile.TestCmd == "" {
		t.Error("TestCmd should not be empty")
	}

	// Check formatters
	if len(profile.Formatters) == 0 {
		t.Error("Formatters should not be empty")
	}
	hasRuff := false
	for _, tool := range profile.Formatters {
		if tool.Name == "ruff" {
			hasRuff = true
			if tool.Cmd == "" {
				t.Error("ruff Cmd should not be empty")
			}
		}
	}
	if !hasRuff {
		t.Error("Formatters should include ruff")
	}

	// Check linters (ruff can be both formatter and linter)
	if len(profile.Linters) == 0 {
		t.Error("Linters should not be empty")
	}
	hasRuffLint := false
	for _, tool := range profile.Linters {
		if tool.Name == "ruff" {
			hasRuffLint = true
		}
	}
	if !hasRuffLint {
		t.Error("Linters should include ruff")
	}

	// Check type checker
	if profile.TypeCheck == nil {
		t.Error("TypeCheck should be set")
	} else {
		if profile.TypeCheck.Name != "pyright" {
			t.Errorf("TypeCheck.Name = %q, want %q", profile.TypeCheck.Name, "pyright")
		}
	}

	// Test Detect function with pyproject.toml
	t.Run("Detect_WithPyprojectToml", func(t *testing.T) {
		tmpDir := t.TempDir()
		pyprojectPath := filepath.Join(tmpDir, "pyproject.toml")
		if err := os.WriteFile(pyprojectPath, []byte("[tool.poetry]\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if !profile.Detect(tmpDir) {
			t.Error("Detect should return true when pyproject.toml exists")
		}
	})

	// Test Detect function with setup.py
	t.Run("Detect_WithSetupPy", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupPyPath := filepath.Join(tmpDir, "setup.py")
		if err := os.WriteFile(setupPyPath, []byte("from setuptools import setup\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if !profile.Detect(tmpDir) {
			t.Error("Detect should return true when setup.py exists")
		}
	})

	// Test Detect function with requirements.txt
	t.Run("Detect_WithRequirementsTxt", func(t *testing.T) {
		tmpDir := t.TempDir()
		reqPath := filepath.Join(tmpDir, "requirements.txt")
		if err := os.WriteFile(reqPath, []byte("pytest\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if !profile.Detect(tmpDir) {
			t.Error("Detect should return true when requirements.txt exists")
		}
	})

	// Test Detect function with no Python files
	t.Run("Detect_WithoutPythonMarkers", func(t *testing.T) {
		tmpDir := t.TempDir()

		if profile.Detect(tmpDir) {
			t.Error("Detect should return false when no Python markers exist")
		}
	})
}

func TestTypeScriptProfile(t *testing.T) {
	profile := langprofile.TypeScriptProfile()

	// Validate required fields
	if err := profile.Validate(); err != nil {
		t.Fatalf("TypeScriptProfile validation failed: %v", err)
	}

	// Check language name
	if profile.Language != "typescript" {
		t.Errorf("Language = %q, want %q", profile.Language, "typescript")
	}

	// Check test command contains vitest
	if profile.TestCmd == "" {
		t.Error("TestCmd should not be empty")
	}
	if profile.TestCmd != "vitest" {
		t.Errorf("TestCmd = %q, want %q", profile.TestCmd, "vitest")
	}

	// Check formatters include biome
	hasBiomeFormatter := false
	for _, tool := range profile.Formatters {
		if tool.Name == "biome" {
			hasBiomeFormatter = true
			if tool.Cmd == "" {
				t.Error("biome formatter Cmd should not be empty")
			}
			if tool.DetectCmd == "" {
				t.Error("biome formatter DetectCmd should not be empty")
			}
			if tool.InstallHint == "" {
				t.Error("biome formatter InstallHint should not be empty")
			}
		}
	}
	if !hasBiomeFormatter {
		t.Error("Formatters should include biome")
	}

	// Check linters include biome
	hasBiomeLinter := false
	for _, tool := range profile.Linters {
		if tool.Name == "biome" {
			hasBiomeLinter = true
		}
	}
	if !hasBiomeLinter {
		t.Error("Linters should include biome")
	}

	// Check type checker is tsc
	if profile.TypeCheck == nil {
		t.Error("TypeCheck should be set for TypeScript")
	} else {
		if profile.TypeCheck.Name != "tsc" {
			t.Errorf("TypeCheck.Name = %q, want %q", profile.TypeCheck.Name, "tsc")
		}
		if profile.TypeCheck.Cmd == "" {
			t.Error("tsc Cmd should not be empty")
		}
	}

	// Detect: tsconfig.json present → TypeScript
	t.Run("Detect_WithTsconfig", func(t *testing.T) {
		tmpDir := t.TempDir()
		tsconfigPath := filepath.Join(tmpDir, "tsconfig.json")
		if err := os.WriteFile(tsconfigPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		if !profile.Detect(tmpDir) {
			t.Error("Detect should return true when tsconfig.json exists")
		}
	})

	// Detect: no tsconfig.json → not TypeScript
	t.Run("Detect_WithoutTsconfig", func(t *testing.T) {
		tmpDir := t.TempDir()

		if profile.Detect(tmpDir) {
			t.Error("Detect should return false when tsconfig.json does not exist")
		}
	})

	// Detect: package.json alone is not TypeScript
	t.Run("Detect_PackageJsonOnly", func(t *testing.T) {
		tmpDir := t.TempDir()
		packageJSONPath := filepath.Join(tmpDir, "package.json")
		if err := os.WriteFile(packageJSONPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		if profile.Detect(tmpDir) {
			t.Error("Detect should return false when only package.json exists (no tsconfig.json)")
		}
	})
}

func TestJavaScriptProfile(t *testing.T) {
	profile := langprofile.JavaScriptProfile()

	// Validate required fields
	if err := profile.Validate(); err != nil {
		t.Fatalf("JavaScriptProfile validation failed: %v", err)
	}

	// Check language name
	if profile.Language != "javascript" {
		t.Errorf("Language = %q, want %q", profile.Language, "javascript")
	}

	// Check test command is vitest
	if profile.TestCmd == "" {
		t.Error("TestCmd should not be empty")
	}
	if profile.TestCmd != "vitest" {
		t.Errorf("TestCmd = %q, want %q", profile.TestCmd, "vitest")
	}

	// Check formatters include biome
	hasBiomeFormatter := false
	for _, tool := range profile.Formatters {
		if tool.Name == "biome" {
			hasBiomeFormatter = true
			if tool.Cmd == "" {
				t.Error("biome formatter Cmd should not be empty")
			}
		}
	}
	if !hasBiomeFormatter {
		t.Error("Formatters should include biome")
	}

	// Check linters include biome
	hasBiomeLinter := false
	for _, tool := range profile.Linters {
		if tool.Name == "biome" {
			hasBiomeLinter = true
		}
	}
	if !hasBiomeLinter {
		t.Error("Linters should include biome")
	}

	// JavaScript should NOT have a TypeScript-style type checker
	if profile.TypeCheck != nil {
		t.Error("TypeCheck should not be set for JavaScript (no tsc)")
	}

	// Detect: package.json without tsconfig → JavaScript
	t.Run("Detect_PackageJsonNoTsconfig", func(t *testing.T) {
		tmpDir := t.TempDir()
		packageJSONPath := filepath.Join(tmpDir, "package.json")
		if err := os.WriteFile(packageJSONPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		if !profile.Detect(tmpDir) {
			t.Error("Detect should return true when package.json exists without tsconfig.json")
		}
	})

	// Detect: package.json + tsconfig.json → not JavaScript (TypeScript takes precedence)
	t.Run("Detect_PackageJsonWithTsconfig", func(t *testing.T) {
		tmpDir := t.TempDir()
		packageJSONPath := filepath.Join(tmpDir, "package.json")
		tsconfigPath := filepath.Join(tmpDir, "tsconfig.json")
		if err := os.WriteFile(packageJSONPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tsconfigPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		if profile.Detect(tmpDir) {
			t.Error("Detect should return false when tsconfig.json is present (TypeScript takes precedence)")
		}
	})

	// Detect: no package.json → not JavaScript
	t.Run("Detect_NoPackageJson", func(t *testing.T) {
		tmpDir := t.TempDir()

		if profile.Detect(tmpDir) {
			t.Error("Detect should return false when package.json does not exist")
		}
	})
}
