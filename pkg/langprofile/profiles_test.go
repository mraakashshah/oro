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
