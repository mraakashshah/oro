package worker

import (
	"fmt"
	"strings"

	"oro/pkg/codestruct"
)

// FormatNavMap formats a single file's symbol list into a structured nav-map
// suitable for inclusion in the Code Structure prompt section (§6.7).
// The nav-map shows the file header with total line count, then an OUTLINE
// listing each symbol with its line range — saving ~80-90% of tokens vs
// including raw file content.
func FormatNavMap(filePath string, totalLines int, symbols []codestruct.Symbol) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s (%d lines) ===\n\nOUTLINE:\n", filePath, totalLines)
	for _, sym := range symbols {
		entry := navMapEntry(sym)
		fmt.Fprintf(&b, "  %-40s [%d-%d]\n", entry, sym.LineStart, sym.LineEnd)
	}
	return b.String()
}

// navMapEntry formats a single symbol as a display string for the nav-map outline.
func navMapEntry(sym codestruct.Symbol) string {
	switch sym.Kind {
	case codestruct.KindType, codestruct.KindInterface:
		return "type " + sym.Name
	case codestruct.KindConst:
		return "const " + sym.Name
	case codestruct.KindVar:
		return "var " + sym.Name
	case codestruct.KindMethod:
		return fmt.Sprintf("func (%s) %s", sym.Receiver, sym.Name)
	default: // KindFunc and any future kinds
		return "func " + sym.Name
	}
}
