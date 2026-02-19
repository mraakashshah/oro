package codesearch

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Language identifies a programming language for summarization.
type Language string

// Supported languages.
const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangTypeScript Language = "typescript"
	LangJavaScript Language = "javascript"
	LangRust       Language = "rust"
	LangJava       Language = "java"
)

var extToLang = map[string]Language{ //nolint:gochecknoglobals // static config
	".go":   LangGo,
	".py":   LangPython,
	".ts":   LangTypeScript,
	".tsx":  LangTypeScript,
	".js":   LangJavaScript,
	".jsx":  LangJavaScript,
	".mjs":  LangJavaScript,
	".rs":   LangRust,
	".java": LangJava,
}

// LangFromPath returns the language for a file path based on its extension.
func LangFromPath(filePath string) (Language, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	lang, ok := extToLang[ext]
	return lang, ok
}

// SummarizeFile dispatches to the appropriate summarizer based on extension.
func SummarizeFile(filePath string) (string, error) {
	lang, ok := LangFromPath(filePath)
	if !ok {
		return "", fmt.Errorf("codesearch: unsupported file type: %s", filepath.Ext(filePath))
	}
	if lang == LangGo {
		return Summarize(filePath)
	}
	return summarizeWithAstGrep(filePath, lang)
}
