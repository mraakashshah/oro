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

// ---------------------------------------------------------------------------
// Tests targeting surviving mutations
// ---------------------------------------------------------------------------

// TestClassifyQuery_NormalizeRegexPattern exercises normalizeRegexPattern
// indirectly via ClassifyQuery so that mutations in the normalization logic
// are detected.
func TestClassifyQuery_NormalizeRegexPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    codesearch.QueryType
	}{
		// double-backslash variants (Go raw-string literal \\w+, \\s+)
		// These come in when callers pass strings produced by fmt.Sprintf or
		// JSON encoding that double the backslash.
		{
			name:    "double-backslash \\w+ becomes W — structural",
			pattern: `func \\w+`,
			want:    codesearch.QueryStructural,
		},
		{
			name:    "double-backslash \\s+ becomes space — structural",
			pattern: `func \\s+\w+`,
			want:    codesearch.QueryStructural,
		},
		{
			name:    "double-backslash \\s* becomes space — structural",
			pattern: `func \\s*\w+`,
			want:    codesearch.QueryStructural,
		},
		{
			name:    "double-backslash \\w* becomes W — structural",
			pattern: `func \\w*`,
			want:    codesearch.QueryStructural,
		},
		// \S+ / \S* treated as word match (W)
		{
			name:    "\\S+ treated as word match — structural",
			pattern: `func\s+\S+`,
			want:    codesearch.QueryStructural,
		},
		{
			name:    "\\S* treated as word match — structural",
			pattern: `func\s+\S*`,
			want:    codesearch.QueryStructural,
		},
		// .* gap patterns
		{
			name:    ".* gap becomes space — struct suffix structural",
			pattern: `type.*struct`,
			want:    codesearch.QueryStructural,
		},
		{
			name:    ".+ gap becomes space — func structural",
			pattern: `func.+\w+`,
			want:    codesearch.QueryStructural,
		},
		// escaped parens \( \) for method receivers
		{
			name:    "escaped parens in method receiver — structural",
			pattern: `func \(\w+\s+\w+\) \w+`,
			want:    codesearch.QueryStructural,
		},
		// multiple consecutive spaces collapse to one
		{
			name:    "multiple spaces collapse — structural",
			pattern: "func   MyFunc",
			want:    codesearch.QueryStructural,
		},
		// pure \\w+ with no prefix is NOT structural (falls through to literal)
		{
			name:    "bare \\w+ alone is literal",
			pattern: `\\w+`,
			want:    codesearch.QueryLiteral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

// TestClassifyQuery_IsStructuralPattern covers isStructuralPattern branches
// that surviving mutations target.
func TestClassifyQuery_IsStructuralPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    codesearch.QueryType
	}{
		// Literal pattern with no regex — "type Server struct" normalizes as-is
		// and should still be structural via the suffix check.
		{
			name:    "literal type Server struct — structural",
			pattern: "type Server struct",
			want:    codesearch.QueryStructural,
		},
		// Method receiver with literal "func (" prefix.
		{
			name:    "func ( receiver prefix — structural",
			pattern: "func (r *Router) ServeHTTP",
			want:    codesearch.QueryStructural,
		},
		// Structural suffix without required "type " prefix must NOT classify as structural.
		{
			name:    "suffix struct without type prefix — literal",
			pattern: "foo struct",
			want:    codesearch.QueryLiteral,
		},
		{
			name:    "suffix interface without type prefix — literal",
			pattern: "foo interface",
			want:    codesearch.QueryLiteral,
		},
		// "type " alone (bare keyword "type" was already tested; "type " with
		// nothing after it — the keyword map contains "type" not "type ").
		{
			name:    "type keyword alone is literal",
			pattern: "type",
			want:    codesearch.QueryLiteral,
		},
		// Structural suffix check: "type " followed by only one char — still matches
		// "type " prefix but rest is one char, and that IS something non-empty.
		{
			name:    "type X struct — structural",
			pattern: "type X struct",
			want:    codesearch.QueryStructural,
		},
		// Prefix must match the full keyword token including trailing space.
		{
			name:    "func_nospace is literal",
			pattern: "funcFoo",
			want:    codesearch.QueryLiteral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

// TestClassifyQuery_HasAlphanumeric covers hasAlphanumeric boundary conditions.
func TestClassifyQuery_HasAlphanumeric(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    codesearch.QueryType
	}{
		// Only digits — hasAlphanumeric returns true (digits count), length >= 3 —
		// falls through bareKeywords (no match), no semantic prefix, no structural
		// match => literal.
		{
			name:    "digits only 123 — literal",
			pattern: "123",
			want:    codesearch.QueryLiteral,
		},
		// Exactly 3 chars (boundary: len < 3 is literal, len == 3 continues).
		{
			name:    "length exactly 3 alphanumeric — literal (no structural match)",
			pattern: "abc",
			want:    codesearch.QueryLiteral,
		},
		// Exactly 2 chars — below threshold, always literal.
		{
			name:    "length 2 — trivial literal",
			pattern: "ab",
			want:    codesearch.QueryLiteral,
		},
		// No alphanumeric at all (pure metacharacters, len >= 3) — literal.
		{
			name:    "pure metacharacters no alphanumeric — literal",
			pattern: `\s+`,
			want:    codesearch.QueryLiteral,
		},
		// Mixed metacharacters with digit — length >= 3, has alphanumeric.
		{
			name:    "metacharacter with digit suffix — literal",
			pattern: `\s1`,
			want:    codesearch.QueryLiteral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ClassifyQuery(tt.pattern)
			if got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

// TestClassifyQuery_AllSemanticPrefixes ensures every entry in semanticPrefixes
// is covered so mutations that alter individual prefix strings are detected.
func TestClassifyQuery_AllSemanticPrefixes(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"where is", "where is auth logic"},
		{"where are", "where are the handlers"},
		{"how does", "how does authentication work"},
		{"how do", "how do I configure TLS"},
		{"how is", "how is the context propagated"},
		{"what is", "what is the retry strategy"},
		{"what are", "what are the middlewares"},
		{"what handles", "what handles incoming requests"},
		{"why is", "why is this locked"},
		{"why does", "why does the test fail"},
		{"why do", "why do we need this wrapper"},
		{"find the", "find the error handler"},
		{"show me", "show me the config loader"},
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

// TestClassifyQuery_AllBareKeywordsLiteral covers every bareKeyword entry,
// including the ones not exercised by existing tests (type, function, export, async).
func TestClassifyQuery_AllBareKeywordsLiteral(t *testing.T) {
	keywords := []string{
		"func", "def", "class", "struct", "interface", "impl",
		"trait", "enum", "fn",
		// Previously uncovered bare keywords:
		"type", "function", "export", "async",
	}

	for _, kw := range keywords {
		kw := kw
		t.Run("bare keyword: "+kw, func(t *testing.T) {
			got := codesearch.ClassifyQuery(kw)
			if got != codesearch.QueryLiteral {
				t.Errorf("ClassifyQuery(%q) = %v, want QueryLiteral (bare keyword)", kw, got)
			}
		})
	}
}
