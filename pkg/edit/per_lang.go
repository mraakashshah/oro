package edit

import "strings"

// Language identifies the programming language of a symbol body.
type Language string

// Language constants for each supported language.
const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangTypeScript Language = "typescript"
	LangJavaScript Language = "javascript"
)

// contMarkerFor returns the continuation-marker string for a language.
func contMarkerFor(lang Language) string {
	if lang == LangPython {
		return "# ..."
	}
	return "// ..."
}

// detectBaseIndent returns the leading-space string of the first non-empty,
// non-marker line in lines.
func detectBaseIndent(lines []string, marker string) string {
	for _, l := range lines {
		if l == "" || l == marker {
			continue
		}
		i := 0
		for i < len(l) && l[i] == ' ' {
			i++
		}
		return l[:i]
	}
	return ""
}

// normalizeIndent adjusts snippet line indentation so that its base indent
// matches orig's base indent. Continuation markers and empty lines are
// left unchanged. Nested indentation levels are scaled by the same offset.
func normalizeIndent(orig, snippet []string, marker string) []string {
	origBase := detectBaseIndent(orig, marker)
	snippetBase := detectBaseIndent(snippet, marker)
	if origBase == snippetBase {
		return snippet
	}
	diff := len(origBase) - len(snippetBase)
	out := make([]string, len(snippet))
	for i, l := range snippet {
		if l == "" || l == marker {
			out[i] = l
			continue
		}
		lead := countLeadingSpaces(l)
		newLead := lead + diff
		if newLead < 0 {
			newLead = 0
		}
		out[i] = strings.Repeat(" ", newLead) + l[lead:]
	}
	return out
}

// countLeadingSpaces returns the number of leading space characters in s.
func countLeadingSpaces(s string) int {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return i
}

// SplicePerLang applies anchor-splice with per-language normalizations (§7.4).
//
//   - Go / TypeScript / JavaScript: uses the appropriate continuation marker.
//     Decorator lines in orig are preserved automatically via the core splice
//     algorithm (they appear in the pre-anchor region when the snippet begins
//     at a later anchor).
//   - Python: snippet indentation is normalized to match orig before splicing.
//
//oro:testonly — production wiring deferred to CLI surface bead (Phase C.2)
func SplicePerLang(lang Language, orig, snippet []string) ([]string, error) {
	marker := contMarkerFor(lang)
	if lang == LangPython {
		snippet = normalizeIndent(orig, snippet, marker)
	}
	return Splice(orig, snippet, marker)
}
