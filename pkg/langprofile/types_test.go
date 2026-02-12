package langprofile_test

import (
	"strings"
	"testing"

	"oro/pkg/langprofile"
)

func TestLangProfile_Validate(t *testing.T) {
	validProfile := langprofile.LangProfile{
		Language: "go",
		Detect:   func(string) bool { return true },
		Formatters: []langprofile.Tool{
			{Name: "gofumpt", Cmd: "gofumpt -w ."},
		},
		Linters: []langprofile.Tool{
			{Name: "golangci-lint", Cmd: "golangci-lint run"},
		},
		TestCmd: "go test ./...",
	}

	t.Run("valid profile passes", func(t *testing.T) {
		if err := validProfile.Validate(); err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("empty Language is error", func(t *testing.T) {
		p := validProfile
		p.Language = ""
		err := p.Validate()
		if err == nil {
			t.Fatal("expected error for empty Language")
		}
		if !strings.Contains(err.Error(), "Language") {
			t.Errorf("error should mention Language, got: %v", err)
		}
	})

	t.Run("nil Detect is error", func(t *testing.T) {
		p := validProfile
		p.Detect = nil
		err := p.Validate()
		if err == nil {
			t.Fatal("expected error for nil Detect")
		}
		if !strings.Contains(err.Error(), "Detect") {
			t.Errorf("error should mention Detect, got: %v", err)
		}
	})

	t.Run("empty TestCmd is error", func(t *testing.T) {
		p := validProfile
		p.TestCmd = ""
		err := p.Validate()
		if err == nil {
			t.Fatal("expected error for empty TestCmd")
		}
		if !strings.Contains(err.Error(), "TestCmd") {
			t.Errorf("error should mention TestCmd, got: %v", err)
		}
	})

	t.Run("optional fields can be nil", func(t *testing.T) {
		p := validProfile
		p.TypeCheck = nil
		p.Security = nil
		p.CoverageCmd = ""
		p.CoverageMin = 0
		p.CodingRules = nil
		if err := p.Validate(); err != nil {
			t.Errorf("optional nil fields should not cause error: %v", err)
		}
	})
}

func TestTool_Fields(t *testing.T) {
	tool := langprofile.Tool{
		Name:        "ruff",
		Cmd:         "ruff check .",
		DetectCmd:   "ruff --version",
		InstallHint: "pip install ruff",
	}

	if tool.Name != "ruff" {
		t.Errorf("expected Name=ruff, got %s", tool.Name)
	}
	if tool.Cmd != "ruff check ." {
		t.Errorf("expected Cmd='ruff check .', got %s", tool.Cmd)
	}
	if tool.DetectCmd != "ruff --version" {
		t.Errorf("expected DetectCmd='ruff --version', got %s", tool.DetectCmd)
	}
	if tool.InstallHint != "pip install ruff" {
		t.Errorf("expected InstallHint='pip install ruff', got %s", tool.InstallHint)
	}
}
