package codesearch_test

import (
	"testing"

	"oro/pkg/codesearch"
)

func TestLangFromPath(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		wantLang codesearch.Language
		wantOK   bool
	}{
		// --- Go files ---
		{
			name:     "Go file",
			filePath: "/project/pkg/handler/handler.go",
			wantLang: codesearch.LangGo,
			wantOK:   true,
		},
		{
			name:     "Go file uppercase extension",
			filePath: "/project/main.GO",
			wantLang: codesearch.LangGo,
			wantOK:   true,
		},
		{
			name:     "Go file mixed case extension",
			filePath: "/project/main.Go",
			wantLang: codesearch.LangGo,
			wantOK:   true,
		},

		// --- Python files ---
		{
			name:     "Python file",
			filePath: "/project/src/handler.py",
			wantLang: codesearch.LangPython,
			wantOK:   true,
		},
		{
			name:     "Python file uppercase extension",
			filePath: "/project/script.PY",
			wantLang: codesearch.LangPython,
			wantOK:   true,
		},

		// --- TypeScript files ---
		{
			name:     "TypeScript file",
			filePath: "/project/src/handler.ts",
			wantLang: codesearch.LangTypeScript,
			wantOK:   true,
		},
		{
			name:     "TypeScript React file",
			filePath: "/project/src/Component.tsx",
			wantLang: codesearch.LangTypeScript,
			wantOK:   true,
		},
		{
			name:     "TypeScript uppercase extension",
			filePath: "/project/main.TS",
			wantLang: codesearch.LangTypeScript,
			wantOK:   true,
		},

		// --- JavaScript files ---
		{
			name:     "JavaScript file",
			filePath: "/project/src/handler.js",
			wantLang: codesearch.LangJavaScript,
			wantOK:   true,
		},
		{
			name:     "JavaScript React file",
			filePath: "/project/src/Component.jsx",
			wantLang: codesearch.LangJavaScript,
			wantOK:   true,
		},
		{
			name:     "JavaScript module file",
			filePath: "/project/src/module.mjs",
			wantLang: codesearch.LangJavaScript,
			wantOK:   true,
		},
		{
			name:     "JavaScript uppercase extension",
			filePath: "/project/main.JS",
			wantLang: codesearch.LangJavaScript,
			wantOK:   true,
		},

		// --- Rust files ---
		{
			name:     "Rust file",
			filePath: "/project/src/main.rs",
			wantLang: codesearch.LangRust,
			wantOK:   true,
		},
		{
			name:     "Rust file uppercase extension",
			filePath: "/project/lib.RS",
			wantLang: codesearch.LangRust,
			wantOK:   true,
		},

		// --- Java files ---
		{
			name:     "Java file",
			filePath: "/project/src/main/java/Server.java",
			wantLang: codesearch.LangJava,
			wantOK:   true,
		},
		{
			name:     "Java file uppercase extension",
			filePath: "/project/Main.JAVA",
			wantLang: codesearch.LangJava,
			wantOK:   true,
		},

		// --- Unsupported / edge cases ---
		{
			name:     "empty path",
			filePath: "",
			wantLang: "",
			wantOK:   false,
		},
		{
			name:     "no extension",
			filePath: "/project/Makefile",
			wantLang: "",
			wantOK:   false,
		},
		{
			name:     "unsupported extension",
			filePath: "/project/README.md",
			wantLang: "",
			wantOK:   false,
		},
		{
			name:     "unsupported code extension (Swift)",
			filePath: "/project/main.swift",
			wantLang: "",
			wantOK:   false,
		},
		{
			name:     "unsupported code extension (C++)",
			filePath: "/project/main.cpp",
			wantLang: "",
			wantOK:   false,
		},
		{
			name:     "multiple dots in filename",
			filePath: "/project/handler.test.go",
			wantLang: codesearch.LangGo,
			wantOK:   true,
		},
		{
			name:     "hidden file with extension",
			filePath: "/project/.config.py",
			wantLang: codesearch.LangPython,
			wantOK:   true,
		},
		{
			name:     "file with no name just extension",
			filePath: ".go",
			wantLang: codesearch.LangGo,
			wantOK:   true,
		},
		{
			name:     "complex path with extension",
			filePath: "/a/b/c/d/e/f/handler.tsx",
			wantLang: codesearch.LangTypeScript,
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLang, gotOK := codesearch.LangFromPath(tt.filePath)
			if gotOK != tt.wantOK {
				t.Errorf("LangFromPath(%q) ok = %v, want %v", tt.filePath, gotOK, tt.wantOK)
			}
			if gotLang != tt.wantLang {
				t.Errorf("LangFromPath(%q) lang = %q, want %q", tt.filePath, gotLang, tt.wantLang)
			}
		})
	}
}
