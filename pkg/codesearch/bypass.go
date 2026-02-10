// Package codesearch implements code search hooks for Oro sessions, including
// AST-based file summarization and bypass rules for the Read tool interceptor.
package codesearch

import (
	"path/filepath"
	"strings"
)

// maxBypassSize is the file size threshold (3KB) below which files are
// bypassed (allowed through without summarization). ~100 lines of Go.
const maxBypassSize = 3072

// ToolInput represents the relevant fields from a Claude Code Read tool call.
type ToolInput struct {
	FilePath string
	FileSize int64
	Offset   int // non-zero means explicit line offset
	Limit    int // non-zero means explicit line limit
}

// ShouldBypass returns true if the Read call should be allowed through without
// interception (no summarization needed). Pure function, no side effects.
//
// Bypass conditions (any one is sufficient):
//   - File size <= 3KB (~100 lines)
//   - Non-code file extension (JSON, YAML, MD, TOML, etc.)
//   - Test file (*_test.go)
//   - Explicit offset or limit in the Read call
//   - File in .claude/ directory
func ShouldBypass(input ToolInput) bool {
	// Small files: not worth summarizing.
	if input.FileSize <= maxBypassSize {
		return true
	}

	// Explicit offset/limit: user wants a specific range.
	if input.Offset != 0 || input.Limit != 0 {
		return true
	}

	// .claude/ directory: hook/config files, always pass through.
	if isClaudeDir(input.FilePath) {
		return true
	}

	// Test files: developers need the full test source.
	if isTestFile(input.FilePath) {
		return true
	}

	// Non-code files: structured data, docs, configs.
	if isNonCodeFile(input.FilePath) {
		return true
	}

	return false
}

// isClaudeDir returns true if the file path is inside a .claude/ directory.
func isClaudeDir(path string) bool {
	return strings.Contains(path, "/.claude/") || strings.HasPrefix(path, ".claude/")
}

// isTestFile returns true if the file is a test file in any supported language.
func isTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.HasSuffix(base, ".py") {
		name := strings.TrimSuffix(base, ".py")
		if strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test") {
			return true
		}
	}
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)
	innerExt := filepath.Ext(nameWithoutExt)
	if innerExt == ".test" || innerExt == ".spec" {
		return true
	}
	return false
}

// isNonCodeExt returns true if the given extension is a non-code file type.
func isNonCodeExt(ext string) bool {
	switch ext {
	case ".json", ".yaml", ".yml", ".md", ".toml", ".txt",
		".csv", ".env", ".sum", ".lock", ".gitignore", ".mod":
		return true
	}
	return false
}

// isNonCodeFile returns true if the file extension indicates a non-code file.
func isNonCodeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if isNonCodeExt(ext) {
		return true
	}
	// Dotfiles without extension (e.g., .gitignore, .env).
	base := filepath.Base(path)
	return strings.HasPrefix(base, ".") && ext == ""
}
