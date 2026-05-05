package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/codestruct"
	"oro/pkg/worker"
)

// TestPromptCodeStructure verifies:
//  1. FormatNavMap produces structured nav-maps (OUTLINE: header + symbol entries with line ranges).
//  2. AssemblePrompt includes a "## Code Structure" section when CodeStructureContext is non-empty.
//  3. The section is omitted when CodeStructureContext is empty.
//  4. The section appears between ## Memory and ## Relevant Code.
//
// Note: the token-savings subtest (requires CGO) lives in prompt_code_structure_cgo_test.go.
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
