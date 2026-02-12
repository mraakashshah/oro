// Package langprofile defines language-specific quality tool profiles.
// Each profile describes the formatters, linters, test commands, and
// optional type-checking/security tools for a programming language.
package langprofile

import "fmt"

// Tool describes a single quality tool (formatter, linter, etc.).
type Tool struct {
	Name        string // display name (e.g. "gofumpt")
	Cmd         string // command to run (e.g. "gofumpt -w .")
	DetectCmd   string // command to check if tool is installed (e.g. "gofumpt --version")
	InstallHint string // how to install (e.g. "go install mvdan.cc/gofumpt@latest")
}

// LangProfile describes the quality toolchain for a single language.
type LangProfile struct {
	Language    string            // canonical name (e.g. "go", "python", "typescript")
	Detect      func(string) bool // returns true if the given project root uses this language
	Formatters  []Tool            // ordered list of formatters to run
	Linters     []Tool            // ordered list of linters to run
	TestCmd     string            // command to run tests (e.g. "go test ./...")
	TypeCheck   *Tool             // optional type checker
	Security    *Tool             // optional security scanner
	CodingRules []string          // language-specific coding rules/conventions
	CoverageCmd string            // optional coverage command
	CoverageMin int               // minimum coverage percentage (0 = no enforcement)
}

// Validate checks that required fields are set. Returns an error describing
// the first missing field.
func (lp LangProfile) Validate() error {
	if lp.Language == "" {
		return fmt.Errorf("langprofile: Language is required")
	}
	if lp.Detect == nil {
		return fmt.Errorf("langprofile: Detect function is required")
	}
	if lp.TestCmd == "" {
		return fmt.Errorf("langprofile: TestCmd is required")
	}
	return nil
}
