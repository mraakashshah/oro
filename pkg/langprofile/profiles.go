package langprofile

import (
	"os"
	"path/filepath"
)

// GoProfile returns the language profile for Go projects.
//
//oro:testonly
func GoProfile() LangProfile {
	return LangProfile{
		Language: "go",
		Detect:   detectGo,
		Formatters: []Tool{
			{
				Name:        "gofumpt",
				Cmd:         "gofumpt -w .",
				DetectCmd:   "gofumpt --version",
				InstallHint: "go install mvdan.cc/gofumpt@latest",
			},
		},
		Linters: []Tool{
			{
				Name:        "golangci-lint",
				Cmd:         "golangci-lint run",
				DetectCmd:   "golangci-lint --version",
				InstallHint: "go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest",
			},
		},
		TestCmd: "go test ./...",
		Security: &Tool{
			Name:        "govulncheck",
			Cmd:         "govulncheck ./...",
			DetectCmd:   "govulncheck -version",
			InstallHint: "go install golang.org/x/vuln/cmd/govulncheck@latest",
		},
		CodingRules: []string{
			"Use gofumpt for consistent formatting",
			"Run golangci-lint with project config",
			"Pure core (business logic), impure edges (I/O)",
			"Prefer early returns over nested conditionals",
		},
	}
}

// detectGo returns true if the given directory contains a go.mod file.
func detectGo(projectRoot string) bool {
	goModPath := filepath.Join(projectRoot, "go.mod")
	_, err := os.Stat(goModPath)
	return err == nil
}

// PythonProfile returns the language profile for Python projects.
//
//oro:testonly
func PythonProfile() LangProfile {
	return LangProfile{
		Language: "python",
		Detect:   detectPython,
		Formatters: []Tool{
			{
				Name:        "ruff",
				Cmd:         "ruff format .",
				DetectCmd:   "ruff --version",
				InstallHint: "pip install ruff",
			},
		},
		Linters: []Tool{
			{
				Name:        "ruff",
				Cmd:         "ruff check .",
				DetectCmd:   "ruff --version",
				InstallHint: "pip install ruff",
			},
		},
		TestCmd: "pytest",
		TypeCheck: &Tool{
			Name:        "pyright",
			Cmd:         "pyright .",
			DetectCmd:   "pyright --version",
			InstallHint: "pip install pyright",
		},
		CodingRules: []string{
			"Follow PEP 8 style guide",
			"Use f-strings for string formatting",
			"Prefer pytest fixtures over test classes",
			"Pure core (business logic), impure edges (I/O)",
		},
	}
}

// detectPython returns true if the given directory contains any Python project marker:
// pyproject.toml, setup.py, or requirements.txt.
func detectPython(projectRoot string) bool {
	markers := []string{"pyproject.toml", "setup.py", "requirements.txt"}
	for _, marker := range markers {
		markerPath := filepath.Join(projectRoot, marker)
		if _, err := os.Stat(markerPath); err == nil {
			return true
		}
	}
	return false
}
