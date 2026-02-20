package codesearch_test

import (
	"testing"

	"oro/pkg/codesearch"
)

func TestQueryType_String(t *testing.T) {
	tests := []struct {
		qt   codesearch.QueryType
		want string
	}{
		{codesearch.QueryLiteral, "literal"},
		{codesearch.QueryStructural, "structural"},
		{codesearch.QuerySemantic, "semantic"},
		{codesearch.QueryType(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.qt.String()
		if got != tt.want {
			t.Errorf("QueryType(%d).String() = %q, want %q", int(tt.qt), got, tt.want)
		}
	}
}

func TestClassifyQuery_Structural(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		// Go structural patterns
		{"Go func declaration", `func\s+\w+`},
		{"Go func with error return", `func.*error`},
		{"Go type struct", `type\s+Server\s+struct`},
		{"Go type interface", `type\s+Handler\s+interface`},
		{"Go method receiver", `func\s*\(\w+\s+\*?\w+\)\s+\w+`},
		{"Go struct definition generic", `type\s+\w+\s+struct`},
		{"Go interface definition generic", `type\s+\w+\s+interface`},

		// Python structural patterns
		{"Python class definition", `class\s+\w+`},
		{"Python def declaration", `def\s+\w+`},
		{"Python async def", `async\s+def\s+\w+`},

		// TypeScript/JavaScript structural patterns
		{"TS function declaration", `function\s+\w+`},
		{"TS interface declaration", `interface\s+\w+`},
		{"TS class declaration", `class\s+\w+\s+implements`},
		{"TS export function", `export\s+function\s+\w+`},
		{"TS export class", `export\s+class\s+\w+`},
		{"TS export interface", `export\s+interface\s+\w+`},
		{"TS export type", `export\s+type\s+\w+`},

		// Rust structural patterns
		{"Rust fn declaration", `fn\s+\w+`},
		{"Rust struct", `struct\s+\w+`},
		{"Rust impl block", `impl\s+\w+`},
		{"Rust trait", `trait\s+\w+`},
		{"Rust enum", `enum\s+\w+`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != codesearch.QueryStructural {
				t.Errorf("ClassifyQuery(%q) = %v, want QueryStructural", tt.pattern, got)
			}
		})
	}
}

func TestClassifyQuery_Literal(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		// Simple literal strings
		{"TODO marker", "TODO"},
		{"FIXME marker", "FIXME"},
		{"HACK marker", "HACK"},
		{"simple identifier", "handleRequest"},
		{"simple function name", "processEvent"},
		{"constant name", "MAX_RETRIES"},
		{"error string", "connection refused"},
		{"import path", "encoding/json"},
		{"package name", "codesearch"},
		{"file path pattern", "config.yaml"},
		{"log message", "server started"},
		{"variable name", "reqTimeout"},
		{"snake_case identifier", "user_id"},
		{"camelCase identifier", "getUserById"},

		// Patterns that look structural but are bypass patterns
		{"bare func keyword", "func"},
		{"bare class keyword", "class"},
		{"bare def keyword", "def"},
		{"bare struct keyword", "struct"},
		{"bare interface keyword", "interface"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != codesearch.QueryLiteral {
				t.Errorf("ClassifyQuery(%q) = %v, want QueryLiteral", tt.pattern, got)
			}
		})
	}
}

func TestClassifyQuery_Semantic(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"natural language question", "where is auth logic"},
		{"how question", "how does authentication work"},
		{"what question", "what handles the request"},
		{"why question", "why is this function called"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != codesearch.QuerySemantic {
				t.Errorf("ClassifyQuery(%q) = %v, want QuerySemantic", tt.pattern, got)
			}
		})
	}
}

func TestClassifyQuery_Ambiguous(t *testing.T) {
	// Ambiguous patterns should default to Literal (conservative).
	tests := []struct {
		name    string
		pattern string
	}{
		{"empty string", ""},
		{"single char", "x"},
		{"regex dot star", ".*"},
		{"just whitespace regex", `\s+`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != codesearch.QueryLiteral {
				t.Errorf("ClassifyQuery(%q) = %v, want QueryLiteral (conservative default)", tt.pattern, got)
			}
		})
	}
}

func TestClassifyQuery_BypassPatterns(t *testing.T) {
	// These patterns contain structural keywords but should be classified
	// as Literal because they are common grep bypass patterns.
	tests := []struct {
		name    string
		pattern string
	}{
		{"bare func", "func"},
		{"bare def", "def"},
		{"bare class", "class"},
		{"bare struct", "struct"},
		{"bare interface", "interface"},
		{"bare impl", "impl"},
		{"bare trait", "trait"},
		{"bare enum", "enum"},
		{"bare fn", "fn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != codesearch.QueryLiteral {
				t.Errorf("ClassifyQuery(%q) = %v, want QueryLiteral (bypass: bare keyword)", tt.pattern, got)
			}
		})
	}
}
