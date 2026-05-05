//go:build cgo

package worker_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"oro/pkg/codestruct"
	"oro/pkg/worker"
)

// generateSyntheticGoFile creates a valid Go source file with approximately
// the given number of lines, containing multiple function declarations.
// Each function body is padded with comment lines so the file is large enough
// to demonstrate token savings in nav-map format.
func generateSyntheticGoFile(lines int) string {
	var b strings.Builder
	b.WriteString("package synthetic\n\n")
	b.WriteString("import \"fmt\"\n\n")

	funcsNeeded := lines / 20 // each function ~20 lines
	if funcsNeeded < 1 {
		funcsNeeded = 1
	}
	for i := range funcsNeeded {
		fmt.Fprintf(&b, "// Function%d performs synthetic operation %d.\nfunc Function%d(x int) int {\n", i, i, i)
		for j := range 15 {
			fmt.Fprintf(&b, "\t// padding line %d of function %d\n", j, i)
		}
		fmt.Fprintf(&b, "\t_ = fmt.Sprintf(\"%%d\", x)\n")
		fmt.Fprintf(&b, "\treturn x + %d\n}\n\n", i)
	}
	return b.String()
}

// TestPromptCodeStructureTokenSavings verifies the nav-map saves >= 80% tokens vs raw file content.
// Requires CGO for tree-sitter symbol extraction.
func TestPromptCodeStructureTokenSavings(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	tmpFile := tmpDir + "/synthetic.go"
	content := generateSyntheticGoFile(200)
	if err := os.WriteFile(tmpFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	symbols, err := codestruct.ExtractGoSymbols(tmpFile)
	if err != nil {
		t.Fatalf("extract symbols: %v", err)
	}

	totalLines := len(strings.Split(content, "\n"))
	navMap := worker.FormatNavMap(tmpFile, totalLines, symbols)

	rawChars := len(content)
	navChars := len(navMap)

	if rawChars == 0 {
		t.Fatal("raw file must have content")
	}

	savings := float64(rawChars-navChars) / float64(rawChars)
	if savings < 0.80 {
		t.Errorf("expected nav-map to save >= 80%% chars vs raw file, got %.1f%% savings (raw: %d chars, nav: %d chars)",
			savings*100, rawChars, navChars)
	}
}
