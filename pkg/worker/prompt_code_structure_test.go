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

// TestPromptCodeStructure verifies:
//  1. FormatNavMap produces structured nav-maps (OUTLINE: header + symbol entries with line ranges).
//  2. The nav-map saves >= 80% tokens vs raw file content.
//  3. AssemblePrompt includes a "## Code Structure" section when CodeStructureContext is non-empty.
//  4. The section is omitted when CodeStructureContext is empty.
//  5. The section appears between ## Memory and ## Relevant Code.
func TestPromptCodeStructure(t *testing.T) {
	t.Parallel()

	t.Run("format_nav_map_structure", func(t *testing.T) {
		t.Parallel()

		symbols := []codestruct.Symbol{
			{Name: "Config", Kind: codestruct.KindType, LineStart: 12, LineEnd: 78},
			{Name: "NewConfig", Kind: codestruct.KindFunc, Signature: "NewConfig() *Config", LineStart: 80, LineEnd: 95},
			{Name: "Run", Kind: codestruct.KindMethod, Receiver: "*Dispatcher", LineStart: 182, LineEnd: 340},
			{Name: "Stop", Kind: codestruct.KindMethod, Receiver: "*Dispatcher", LineStart: 342, LineEnd: 365},
		}

		navMap := worker.FormatNavMap("pkg/dispatcher/dispatcher.go", 1247, symbols)

		if !strings.Contains(navMap, "pkg/dispatcher/dispatcher.go") {
			t.Error("nav-map must contain file path")
		}
		if !strings.Contains(navMap, "1247") {
			t.Error("nav-map must contain total line count")
		}
		if !strings.Contains(navMap, "OUTLINE:") {
			t.Error("nav-map must contain OUTLINE: header")
		}
		if !strings.Contains(navMap, "Config") {
			t.Error("nav-map must contain symbol name 'Config'")
		}
		if !strings.Contains(navMap, "[12-78]") {
			t.Error("nav-map must contain line range [12-78] for Config")
		}
		if !strings.Contains(navMap, "[182-340]") {
			t.Error("nav-map must contain line range [182-340] for Run")
		}
	})

	t.Run("token_savings_vs_raw_file", func(t *testing.T) {
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
	})

	t.Run("section_present_in_prompt", func(t *testing.T) {
		t.Parallel()

		navMap := "=== pkg/foo/bar.go (100 lines) ===\n\nOUTLINE:\n  func Foo [1-50]\n"

		params := worker.PromptParams{
			BeadID:               "bead-code-struct",
			Title:                "Code structure test",
			Description:          "Test description",
			AcceptanceCriteria:   "Tests pass",
			WorktreePath:         "/tmp/wt-code-struct",
			Model:                "opus",
			CodeStructureContext: navMap,
		}

		prompt := worker.AssemblePrompt(params)

		if !strings.Contains(prompt, "## Code Structure") {
			t.Error("expected prompt to contain '## Code Structure' section when CodeStructureContext is non-empty")
		}
		if !strings.Contains(prompt, navMap) {
			t.Error("expected prompt to contain the nav-map content")
		}
	})

	t.Run("section_omitted_when_empty", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "bead-no-struct",
			Title:              "No code structure",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			WorktreePath:       "/tmp/wt-no-struct",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		if strings.Contains(prompt, "## Code Structure") {
			t.Error("expected ## Code Structure section to be omitted when CodeStructureContext is empty")
		}
	})

	t.Run("section_ordering", func(t *testing.T) {
		t.Parallel()

		navMap := "=== pkg/foo/bar.go (50 lines) ===\nOUTLINE:\n  func Foo [1-50]\n"

		params := worker.PromptParams{
			BeadID:               "bead-struct-order",
			Title:                "Order test",
			Description:          "Test description",
			AcceptanceCriteria:   "Tests pass",
			MemoryContext:        "Some memory",
			CodeStructureContext: navMap,
			CodeSearchContext:    "### pkg/foo/bar.go:1-10\n```go\nfunc Foo() {}\n```",
			WorktreePath:         "/tmp/wt-struct-order",
			Model:                "opus",
		}

		prompt := worker.AssemblePrompt(params)

		memIdx := strings.Index(prompt, "## Memory")
		structIdx := strings.Index(prompt, "## Code Structure")
		codeIdx := strings.Index(prompt, "## Relevant Code")

		if memIdx == -1 || structIdx == -1 || codeIdx == -1 {
			t.Fatalf("expected Memory (%d), Code Structure (%d), and Relevant Code (%d) sections in prompt",
				memIdx, structIdx, codeIdx)
		}

		if structIdx <= memIdx {
			t.Errorf("expected ## Code Structure (at %d) to appear after ## Memory (at %d)", structIdx, memIdx)
		}
		if structIdx >= codeIdx {
			t.Errorf("expected ## Code Structure (at %d) to appear before ## Relevant Code (at %d)", structIdx, codeIdx)
		}
	})
}
