package codesearch

import (
	"strings"
)

// QueryType classifies a grep search pattern.
type QueryType int

const (
	// QueryLiteral is a plain text search best handled by ripgrep.
	QueryLiteral QueryType = iota
	// QueryStructural is a pattern that searches for code structure
	// (function definitions, type declarations, etc.) better handled by AST search.
	QueryStructural
	// QuerySemantic is a natural-language question flagged for future handling.
	QuerySemantic
)

// String returns a human-readable label for the query type.
func (q QueryType) String() string {
	switch q {
	case QueryLiteral:
		return "literal"
	case QueryStructural:
		return "structural"
	case QuerySemantic:
		return "semantic"
	default:
		return "unknown"
	}
}

// structuralKeywordPrefixes are pairs of (keyword, followedBy) that identify
// structural grep patterns. A pattern is structural if it contains a keyword
// at a word boundary followed by regex-space (\s+, \s*, \\s+, spaces) and
// then either \w+ or a word-like identifier.
//
// Design: grep patterns like "func\s+\w+" contain literal backslash characters.
// We normalize the pattern to collapse regex metacharacters, then check for
// keyword + gap + identifier structure.
var structuralKeywordPrefixes = []string{ //nolint:gochecknoglobals // static config
	// Go
	"func ",
	"type ",

	// Python
	"def ",
	"async def ",
	"class ",

	// TypeScript/JavaScript
	"function ",
	"export function ",
	"export class ",
	"export interface ",
	"export type ",
	"interface ",

	// Rust
	"fn ",
	"struct ",
	"impl ",
	"trait ",
	"enum ",
}

// structuralSuffixes are keyword suffixes that indicate structural patterns
// when preceded by a type name. E.g., "type Server struct" or "type Handler interface".
var structuralSuffixes = []string{ //nolint:gochecknoglobals // static config
	" struct",
	" interface",
}

// semanticPrefixes are natural-language prefixes that indicate a semantic query.
var semanticPrefixes = []string{ //nolint:gochecknoglobals // static config
	"where is ",
	"where are ",
	"how does ",
	"how do ",
	"how is ",
	"what is ",
	"what are ",
	"what handles ",
	"why is ",
	"why does ",
	"why do ",
	"find the ",
	"show me ",
}

// bareKeywords are structural keywords that, when used alone as a grep
// pattern, are better served by ripgrep (too broad for AST search).
var bareKeywords = map[string]bool{ //nolint:gochecknoglobals // static config
	"func":      true,
	"def":       true,
	"class":     true,
	"struct":    true,
	"interface": true,
	"impl":      true,
	"trait":     true,
	"enum":      true,
	"fn":        true,
	"type":      true,
	"function":  true,
	"export":    true,
	"async":     true,
}

// ClassifyQuery classifies a grep search pattern as structural, literal, or
// semantic. Pure function with no side effects.
//
// Classification priority:
//  1. Empty/trivial patterns -> Literal (conservative)
//  2. Bare keywords (func, class, def, struct) -> Literal (too broad for AST)
//  3. Semantic (natural language) -> Semantic
//  4. Structural (code definition patterns) -> Structural
//  5. Everything else -> Literal (conservative default)
func ClassifyQuery(pattern string) QueryType {
	trimmed := strings.TrimSpace(pattern)

	// Empty or trivial patterns: literal.
	if len(trimmed) < 3 {
		return QueryLiteral
	}

	// Pure regex metacharacter patterns with no alphanumeric content: literal.
	if !hasAlphanumeric(trimmed) {
		return QueryLiteral
	}

	// Bare keywords: these are too broad for AST search.
	if bareKeywords[trimmed] {
		return QueryLiteral
	}

	// Semantic: natural language questions.
	lower := strings.ToLower(trimmed)
	for _, prefix := range semanticPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return QuerySemantic
		}
	}

	// Structural: normalize the pattern and check for keyword + identifier.
	normalized := normalizeRegexPattern(trimmed)
	if isStructuralPattern(normalized) {
		return QueryStructural
	}

	// Conservative default: literal.
	return QueryLiteral
}

// normalizeRegexPattern collapses common regex metacharacters in a grep
// pattern into spaces, so "func\s+\w+" becomes "func W" and
// "func.*error" becomes "func error". This lets us do simple prefix/suffix
// matching on the normalized form.
//
// Replacements (order matters):
//   - \s+, \s*, \\s+, \\s* -> " " (regex whitespace quantifiers)
//   - \w+, \w*, \\w+, \\w* -> "W" (regex word quantifiers)
//   - \S+, \S* -> "W" (non-whitespace quantifiers, often used as word match)
//   - .+, .*, \.\* -> " " (any-char quantifiers)
//   - [^/]+ and similar bracket classes -> "W"
//   - \( and \) -> "(" and ")" (escaped parens in Go method patterns)
//   - Collapse multiple spaces into one
func normalizeRegexPattern(pattern string) string {
	s := pattern

	// Replace regex word matchers with "W" (identifier placeholder).
	s = strings.ReplaceAll(s, `\w+`, "W")
	s = strings.ReplaceAll(s, `\w*`, "W")
	s = strings.ReplaceAll(s, `\\w+`, "W")
	s = strings.ReplaceAll(s, `\\w*`, "W")
	s = strings.ReplaceAll(s, `\S+`, "W")
	s = strings.ReplaceAll(s, `\S*`, "W")

	// Replace regex whitespace matchers with space.
	s = strings.ReplaceAll(s, `\s+`, " ")
	s = strings.ReplaceAll(s, `\s*`, " ")
	s = strings.ReplaceAll(s, `\\s+`, " ")
	s = strings.ReplaceAll(s, `\\s*`, " ")

	// Replace dot-quantifiers with space (used as flexible gap).
	s = strings.ReplaceAll(s, `.*`, " ")
	s = strings.ReplaceAll(s, `.+`, " ")

	// Replace escaped parens (Go method receiver: func\s*\(\w+\s+\*?\w+\)).
	s = strings.ReplaceAll(s, `\(`, "(")
	s = strings.ReplaceAll(s, `\)`, ")")

	// Replace \*? (optional pointer) with empty string.
	s = strings.ReplaceAll(s, `\*?`, "")

	// Collapse multiple spaces.
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}

	return strings.TrimSpace(s)
}

// isStructuralPattern checks if the normalized pattern matches any structural
// keyword prefix or suffix patterns.
func isStructuralPattern(normalized string) bool {
	// Check keyword + identifier prefixes.
	for _, prefix := range structuralKeywordPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			rest := normalized[len(prefix):]
			// After the keyword+space, there should be something meaningful
			// (an identifier, a word placeholder "W", or more structure).
			if rest != "" {
				return true
			}
		}
	}

	// Check for method receiver pattern: "func (..." or "func (W W)".
	if strings.HasPrefix(normalized, "func (") || strings.HasPrefix(normalized, "func(") {
		return true
	}

	// Check structural suffixes (e.g., "type Server struct").
	for _, suffix := range structuralSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			// Must have a keyword prefix before the suffix.
			before := normalized[:len(normalized)-len(suffix)]
			if strings.HasPrefix(before, "type ") && len(before) > len("type ") {
				return true
			}
		}
	}

	return false
}

// hasAlphanumeric returns true if the string contains at least one letter or digit.
func hasAlphanumeric(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return true
		}
	}
	return false
}
