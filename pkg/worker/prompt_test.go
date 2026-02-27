package worker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// expectedSectionHeaders lists all 12 section headers in order.
var expectedSectionHeaders = []string{
	"## Role",
	"## Bead",
	"## Memory",
	"## Coding Rules",
	"## TDD",
	"## Quality Gate",
	"## Worktree",
	"## Git",
	"## Beads Tools",
	"## Constraints",
	"## Failure",
	"## Exit",
}

func TestAssemblePrompt_AllSectionHeadersPresent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-123",
		Title:              "Add prompt assembly",
		Description:        "Build the 12-section prompt template",
		AcceptanceCriteria: "All 12 sections present",
		MemoryContext:      "Prior session learned X",
		WorktreePath:       "/tmp/wt-123",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	for _, header := range expectedSectionHeaders {
		if !strings.Contains(prompt, header) {
			t.Errorf("expected prompt to contain section header %q", header)
		}
	}
}

func TestAssemblePrompt_BeadDetailsInjected(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-abc",
		Title:              "Implement foo feature",
		Description:        "Add the foo functionality to the bar module",
		AcceptanceCriteria: "foo returns correct output for all inputs",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-abc",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "bead-abc") {
		t.Error("expected prompt to contain bead ID")
	}
	if !strings.Contains(prompt, "Implement foo feature") {
		t.Error("expected prompt to contain bead title")
	}
	if !strings.Contains(prompt, "Add the foo functionality to the bar module") {
		t.Error("expected prompt to contain bead description")
	}
	if !strings.Contains(prompt, "foo returns correct output for all inputs") {
		t.Error("expected prompt to contain acceptance criteria")
	}
}

func TestAssemblePrompt_EmptyMemoryContext(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-nomem",
		Title:              "Test bead",
		Description:        "A test bead",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-nomem",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Memory section header should still be present
	if !strings.Contains(prompt, "## Memory") {
		t.Error("expected prompt to contain ## Memory header even when empty")
	}
	// Should contain a "no prior context" note
	if !strings.Contains(prompt, "No prior context") {
		t.Error("expected prompt to contain 'No prior context' note when memory is empty")
	}
}

func TestAssemblePrompt_NonEmptyMemoryContext(t *testing.T) {
	t.Parallel()

	memCtx := "- [lesson] always run go vet before committing\n- [gotcha] FTS5 needs triggers"

	params := worker.PromptParams{
		BeadID:             "bead-withmem",
		Title:              "Test bead with memory",
		Description:        "A test bead",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      memCtx,
		WorktreePath:       "/tmp/wt-withmem",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "always run go vet before committing") {
		t.Error("expected prompt to contain memory context content")
	}
	if !strings.Contains(prompt, "FTS5 needs triggers") {
		t.Error("expected prompt to contain all memory context entries")
	}
	if strings.Contains(prompt, "No prior context") {
		t.Error("prompt should NOT contain 'No prior context' when memory context is provided")
	}
}

func TestAssemblePrompt_WorktreeAndBeadIDInterpolated(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-wt-42",
		Title:              "Worktree test",
		Description:        "Test worktree interpolation",
		AcceptanceCriteria: "Paths correct",
		MemoryContext:      "",
		WorktreePath:       "/home/user/.worktrees/bead-wt-42",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Worktree section should reference the path
	if !strings.Contains(prompt, "/home/user/.worktrees/bead-wt-42") {
		t.Error("expected prompt to contain worktree path")
	}
	// Git section should reference the branch name
	if !strings.Contains(prompt, "agent/bead-wt-42") {
		t.Error("expected prompt to contain branch name agent/<bead-id>")
	}
}

func TestAssemblePrompt_ValidOutput(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-valid",
		Title:              "Validation test",
		Description:        "Ensure prompt is valid",
		AcceptanceCriteria: "Non-empty, reasonable length",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-valid",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	// The 12-section prompt should be at least a few hundred characters
	if len(prompt) < 500 {
		t.Errorf("expected prompt length > 500, got %d", len(prompt))
	}
	// Should not exceed a reasonable upper bound (no runaway generation)
	if len(prompt) > 10000 {
		t.Errorf("expected prompt length < 10000, got %d", len(prompt))
	}
}

func TestAssemblePrompt_SectionOrder(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-order",
		Title:              "Order test",
		Description:        "Test section ordering",
		AcceptanceCriteria: "Sections in correct order",
		MemoryContext:      "Some memory context",
		WorktreePath:       "/tmp/wt-order",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Verify sections appear in the correct order
	lastIdx := -1
	for _, header := range expectedSectionHeaders {
		idx := strings.Index(prompt, header)
		if idx == -1 {
			t.Errorf("section header %q not found in prompt", header)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("section %q (at index %d) appears before or at the same position as the previous section (at index %d)", header, idx, lastIdx)
		}
		lastIdx = idx
	}
}

func TestAssemblePrompt_RoleContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-role",
		Title:              "Role test",
		Description:        "Test role section",
		AcceptanceCriteria: "Role text present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-role",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "You are an oro worker") {
		t.Error("expected Role section to contain 'You are an oro worker'")
	}
	if !strings.Contains(prompt, "one bead at a time") {
		t.Error("expected Role section to contain 'one bead at a time'")
	}
}

func TestAssemblePrompt_TDDContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-tdd",
		Title:              "TDD test",
		Description:        "Test TDD section",
		AcceptanceCriteria: "TDD text present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-tdd",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "Write tests FIRST") {
		t.Error("expected TDD section to contain 'Write tests FIRST'")
	}
	if !strings.Contains(prompt, "Red-green-refactor") {
		t.Error("expected TDD section to contain 'Red-green-refactor'")
	}
}

func TestAssemblePrompt_QualityGateContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-qg",
		Title:              "QG test",
		Description:        "Test quality gate section",
		AcceptanceCriteria: "Quality gate command present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-qg",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "./quality_gate.sh") {
		t.Error("expected Quality Gate section to contain './quality_gate.sh'")
	}
}

func TestAssemblePrompt_ConstraintsContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-constraints",
		Title:              "Constraints test",
		Description:        "Test constraints section",
		AcceptanceCriteria: "Constraints present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-constraints",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "no git push") {
		t.Error("expected Constraints section to contain 'no git push'")
	}
}

func TestAssemblePrompt_FailureContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-failure",
		Title:              "Failure test",
		Description:        "Test failure section",
		AcceptanceCriteria: "Failure protocols present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-failure",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "3 failed test attempts") {
		t.Error("expected Failure section to contain '3 failed test attempts'")
	}
	if !strings.Contains(prompt, "bd create") {
		t.Error("expected Failure section to mention bd create for decomposition")
	}
}

func TestAssemblePrompt_ExitContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-exit",
		Title:              "Exit test",
		Description:        "Test exit section",
		AcceptanceCriteria: "Exit text present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-exit",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "acceptance criteria pass") {
		t.Error("expected Exit section to contain 'acceptance criteria pass'")
	}
	if !strings.Contains(prompt, "quality gate is green") {
		t.Error("expected Exit section to contain 'quality gate is green'")
	}
}

func TestAssemblePrompt_BeadsToolsContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-tools",
		Title:              "Tools test",
		Description:        "Test beads tools section",
		AcceptanceCriteria: "Tools commands present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-tools",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "bd create") {
		t.Error("expected Beads Tools section to contain 'bd create'")
	}
	if !strings.Contains(prompt, "bd dep add") {
		t.Error("expected Beads Tools section to contain 'bd dep add'")
	}
}

// TestAssemblePrompt_BeadsToolsDoesNotContainBdClose verifies that the Beads
// Tools section does NOT list `bd close` as a worker tool. Workers must not
// close beads — the dispatcher handles bead closure after merging to main.
//
// Context: oro-u74j bug — listing `bd close` in Beads Tools contradicts the
// Exit section's instruction that the dispatcher handles closure, leading
// workers to close beads without merging to main.
func TestAssemblePrompt_BeadsToolsDoesNotContainBdClose(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-no-close",
		Title:              "No bd close in tools",
		Description:        "Workers must not close beads",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-no-close",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract just the Beads Tools section
	toolsStart := strings.Index(prompt, "## Beads Tools")
	if toolsStart == -1 {
		t.Fatal("expected prompt to contain ## Beads Tools section")
	}
	toolsEnd := strings.Index(prompt[toolsStart+1:], "## ")
	var toolsSection string
	if toolsEnd == -1 {
		toolsSection = prompt[toolsStart:]
	} else {
		toolsSection = prompt[toolsStart : toolsStart+1+toolsEnd]
	}

	if strings.Contains(toolsSection, "bd close") {
		t.Errorf("Beads Tools section must NOT contain 'bd close' — dispatcher handles bead closure (oro-u74j). Got:\n%s", toolsSection)
	}
}

func TestAssemblePrompt_GitContent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-git",
		Title:              "Git test",
		Description:        "Test git section",
		AcceptanceCriteria: "Git instructions present",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-git",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "conventional commits") {
		t.Error("expected Git section to reference conventional commits")
	}
	if !strings.Contains(prompt, "feat(") {
		t.Error("expected Git section to show conventional commit format example")
	}
	if !strings.Contains(prompt, "new commits only") {
		t.Error("expected Git section to mention 'new commits only'")
	}
}

func TestAssemblePrompt_FailureSectionHasBdCreateExamples(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-fail-ex",
		Title:              "Failure examples test",
		Description:        "Test failure section has bd create examples",
		AcceptanceCriteria: "bd create examples present in Failure section",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-fail-ex",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract just the Failure section for focused assertions
	failStart := strings.Index(prompt, "## Failure")
	if failStart == -1 {
		t.Fatal("expected prompt to contain ## Failure section")
	}
	failEnd := strings.Index(prompt[failStart+1:], "## ")
	var failureSection string
	if failEnd == -1 {
		failureSection = prompt[failStart:]
	} else {
		failureSection = prompt[failStart : failStart+1+failEnd]
	}

	// Each failure mode should have a concrete bd create command example
	checks := []struct {
		name   string
		substr string
	}{
		{"bd create --title flag", `bd create --title=`},
		{"test failure bug type+priority", `--type=bug --priority=0`},
		{"decompose with parent", `--parent=`},
		{"context limit handoff", `bd create --title="Continue:`},
		{"blocker bug creation", `bd create --title="Blocker:`},
		{"bd dep add example", `bd dep add`},
	}

	for _, c := range checks {
		if !strings.Contains(failureSection, c.substr) {
			t.Errorf("%s: expected Failure section to contain %q", c.name, c.substr)
		}
	}
}

func TestAssemblePrompt_AttemptZero_NoRetryNote(t *testing.T) {
	t.Parallel()
	params := worker.PromptParams{BeadID: "bead-no-retry", Title: "No retry", Description: "First attempt", AcceptanceCriteria: "Tests pass", WorktreePath: "/tmp/wt-no-retry", Model: "opus", Attempt: 0}
	prompt := worker.AssemblePrompt(params)
	if strings.Contains(prompt, "Retry attempt") {
		t.Error("prompt should NOT contain Retry attempt when Attempt=0")
	}
}

func TestAssemblePrompt_AttemptPositive_IncludesRetryNote(t *testing.T) {
	t.Parallel()
	params := worker.PromptParams{BeadID: "bead-retry", Title: "Retry", Description: "Second attempt", AcceptanceCriteria: "Tests pass", WorktreePath: "/tmp/wt-retry", Model: "opus", Attempt: 2}
	prompt := worker.AssemblePrompt(params)
	if !strings.Contains(prompt, "Retry attempt 2") {
		t.Error("expected Retry attempt 2")
	}
	if !strings.Contains(prompt, "quality gate has failed") {
		t.Error("expected quality gate has failed note")
	}
}

func TestAssemblePrompt_FeedbackIncludedInRetry(t *testing.T) {
	t.Parallel()
	params := worker.PromptParams{
		BeadID:             "bead-fb",
		Title:              "Fix bug",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-fb",
		Model:              "opus",
		Attempt:            1,
		Feedback:           "FAIL: TestFoo expected 42 got 0",
	}
	prompt := worker.AssemblePrompt(params)
	if !strings.Contains(prompt, "FAIL: TestFoo expected 42 got 0") {
		t.Errorf("expected feedback in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Previous Feedback") {
		t.Error("expected 'Previous Feedback' section header")
	}
}

func TestAssemblePrompt_CodeSearchContext_Empty(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-no-code",
		Title:              "Test with no code search",
		Description:        "Test description",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "Some memory",
		CodeSearchContext:  "", // No code search results
		WorktreePath:       "/tmp/wt-no-code",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Should NOT contain Relevant Code section when CodeSearchContext is empty
	if strings.Contains(prompt, "## Relevant Code") {
		t.Error("prompt should NOT contain '## Relevant Code' section when CodeSearchContext is empty")
	}
}

func TestAssemblePrompt_CodeSearchContext_Present(t *testing.T) {
	t.Parallel()

	codeSearchCtx := "### pkg/foo/bar.go:10-20\n```go\nfunc Example() {\n\treturn nil\n}\n```"

	params := worker.PromptParams{
		BeadID:             "bead-with-code",
		Title:              "Test with code search",
		Description:        "Test description",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "Some memory",
		CodeSearchContext:  codeSearchCtx,
		WorktreePath:       "/tmp/wt-with-code",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Should contain Relevant Code section
	if !strings.Contains(prompt, "## Relevant Code") {
		t.Error("expected prompt to contain '## Relevant Code' section when CodeSearchContext is provided")
	}

	// Should contain the actual code search results
	if !strings.Contains(prompt, "pkg/foo/bar.go:10-20") {
		t.Error("expected prompt to contain code search file path")
	}

	if !strings.Contains(prompt, "func Example()") {
		t.Error("expected prompt to contain code search content")
	}

	// Relevant Code should appear AFTER Memory section
	memIdx := strings.Index(prompt, "## Memory")
	codeIdx := strings.Index(prompt, "## Relevant Code")
	if memIdx == -1 || codeIdx == -1 || codeIdx <= memIdx {
		t.Error("expected '## Relevant Code' section to appear after '## Memory' section")
	}

	// Relevant Code should appear BEFORE Coding Rules section
	rulesIdx := strings.Index(prompt, "## Coding Rules")
	if rulesIdx == -1 || codeIdx >= rulesIdx {
		t.Error("expected '## Relevant Code' section to appear before '## Coding Rules' section")
	}
}

// TestAssemblePrompt_CodeSearchSection verifies that the ## Relevant Code section
// is rendered conditionally: present when CodeSearchContext is non-empty, omitted
// when empty, and positioned correctly between Memory and Coding Rules sections.
func TestAssemblePrompt_CodeSearchSection(t *testing.T) {
	t.Parallel()

	t.Run("section_omitted_when_empty", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "bead-no-search",
			Title:              "No code search",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Some context",
			CodeSearchContext:  "", // Empty
			WorktreePath:       "/tmp/wt-no-search",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		if strings.Contains(prompt, "## Relevant Code") {
			t.Error("expected ## Relevant Code section to be omitted when CodeSearchContext is empty")
		}
	})

	t.Run("section_present_when_non_empty", func(t *testing.T) {
		t.Parallel()

		codeSearchCtx := "### pkg/example/example.go:15-30\n```go\nfunc Test() {}\n```"

		params := worker.PromptParams{
			BeadID:             "bead-with-search",
			Title:              "With code search",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Some context",
			CodeSearchContext:  codeSearchCtx,
			WorktreePath:       "/tmp/wt-with-search",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		if !strings.Contains(prompt, "## Relevant Code") {
			t.Error("expected ## Relevant Code section to be present when CodeSearchContext is non-empty")
		}

		if !strings.Contains(prompt, codeSearchCtx) {
			t.Error("expected prompt to contain the CodeSearchContext content")
		}
	})

	t.Run("section_ordering", func(t *testing.T) {
		t.Parallel()

		codeSearchCtx := "### pkg/foo/bar.go:5-10\n```go\nfunc Foo() {}\n```"

		params := worker.PromptParams{
			BeadID:             "bead-order-test",
			Title:              "Order test",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Some context",
			CodeSearchContext:  codeSearchCtx,
			WorktreePath:       "/tmp/wt-order",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		memIdx := strings.Index(prompt, "## Memory")
		codeIdx := strings.Index(prompt, "## Relevant Code")
		rulesIdx := strings.Index(prompt, "## Coding Rules")

		if memIdx == -1 {
			t.Fatal("## Memory section not found in prompt")
		}
		if codeIdx == -1 {
			t.Fatal("## Relevant Code section not found in prompt")
		}
		if rulesIdx == -1 {
			t.Fatal("## Coding Rules section not found in prompt")
		}

		if codeIdx <= memIdx {
			t.Errorf("expected ## Relevant Code to appear after ## Memory (Memory at %d, Code at %d)", memIdx, codeIdx)
		}

		if codeIdx >= rulesIdx {
			t.Errorf("expected ## Relevant Code to appear before ## Coding Rules (Code at %d, Rules at %d)", codeIdx, rulesIdx)
		}
	})
}

func TestPromptHandoffTemplate(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-xyz123",
		Title:              "Large task requiring handoff",
		Description:        "Test description",
		AcceptanceCriteria: "- [ ] Feature A works\n- [ ] Feature B works",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-handoff",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Failure section for focused assertions
	failStart := strings.Index(prompt, "## Failure")
	if failStart == -1 {
		t.Fatal("expected prompt to contain ## Failure section")
	}
	failEnd := strings.Index(prompt[failStart+1:], "## ")
	var failureSection string
	if failEnd == -1 {
		failureSection = prompt[failStart:]
	} else {
		failureSection = prompt[failStart : failStart+1+failEnd]
	}

	// Check that handoff template contains --parent flag with actual bead-id (not placeholder)
	if !strings.Contains(failureSection, "--parent=oro-xyz123") {
		t.Error("expected handoff template to contain --parent=oro-xyz123 (actual bead-id, not placeholder)")
	}

	// Check that handoff template contains --acceptance-criteria flag
	if !strings.Contains(failureSection, "--acceptance-criteria") {
		t.Error("expected handoff template to contain --acceptance-criteria flag")
	}

	// Check that handoff template instructs agent to copy AC from assignment context
	if !strings.Contains(failureSection, "copy") && !strings.Contains(failureSection, "same acceptance criteria") {
		t.Error("expected handoff template to instruct agent to copy AC from assignment context")
	}
}

func TestAssemblePrompt_ExitSection_RequiresMergeToMain(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-test-exit",
		Title:              "Test merge requirement",
		Description:        "Test description",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-exit-test",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Exit section for focused assertions
	exitStart := strings.Index(prompt, "## Exit")
	if exitStart == -1 {
		t.Fatal("expected prompt to contain ## Exit section")
	}
	// Exit is the last section, so take everything from exitStart to end
	exitSection := prompt[exitStart:]

	// Exit section must explain dispatcher handles merge and close (oro-u74j fix)
	if !strings.Contains(exitSection, "dispatcher") {
		t.Error("expected Exit section to mention 'dispatcher' handles merge")
	}
	if !strings.Contains(exitSection, "merge") {
		t.Error("expected Exit section to mention 'merge' process")
	}
	// Worker should NOT be told to close bead themselves
	if strings.Contains(exitSection, "bd close") {
		t.Error("Exit section must NOT tell worker to run 'bd close' (dispatcher handles this)")
	}
}

func TestAssemblePrompt_ExitSection_HandlesUnrelatedTestFailures(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-test-blocker",
		Title:              "Test blocker handling",
		Description:        "Test description",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-blocker-test",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Exit section
	exitStart := strings.Index(prompt, "## Exit")
	if exitStart == -1 {
		t.Fatal("expected prompt to contain ## Exit section")
	}
	exitSection := prompt[exitStart:]

	// Exit section should explain dispatcher escalates merge failures (oro-u74j)
	// The worker no longer handles merge failures directly
	if !strings.Contains(strings.ToLower(exitSection), "dispatcher") {
		t.Error("expected Exit section to mention dispatcher handles merge process")
	}
	if !strings.Contains(strings.ToLower(exitSection), "merge") {
		t.Error("expected Exit section to mention merge process")
	}
}

func TestBuildAssignPromptUsesEpicDecomposition(t *testing.T) {
	t.Parallel()

	t.Run("epic_decomposition_uses_decomposition_prompt", func(t *testing.T) {
		t.Parallel()

		prompt, _ := worker.BuildAssignPrompt(&protocol.AssignPayload{
			BeadID:              "oro-epic-1",
			Worktree:            "/tmp/wt-epic",
			Title:               "Epic: Add auth",
			Description:         "Add JWT authentication",
			Model:               "opus",
			IsEpicDecomposition: true,
		})

		// Must contain beadcraft instructions
		if !strings.Contains(prompt, "beadcraft") {
			t.Errorf("expected epic decomp prompt to contain 'beadcraft', got:\n%s", prompt)
		}
		// Must contain bd create and --parent= flag for child bead creation
		if !strings.Contains(prompt, "bd create") {
			t.Errorf("expected epic decomp prompt to contain 'bd create', got:\n%s", prompt)
		}
		if !strings.Contains(prompt, "--parent=") {
			t.Errorf("expected epic decomp prompt to contain '--parent=', got:\n%s", prompt)
		}
		// Must NOT contain standard worker sections
		if strings.Contains(prompt, "## Quality Gate") {
			t.Error("epic decomp prompt must NOT contain '## Quality Gate'")
		}
		if strings.Contains(prompt, "## Worktree") {
			t.Error("epic decomp prompt must NOT contain '## Worktree'")
		}
	})

	t.Run("standard_assignment_uses_standard_prompt", func(t *testing.T) {
		t.Parallel()

		prompt, _ := worker.BuildAssignPrompt(&protocol.AssignPayload{
			BeadID:              "oro-task-1",
			Worktree:            "/tmp/wt-task",
			Title:               "Fix bug",
			Description:         "Fix the auth bug",
			AcceptanceCriteria:  "Tests pass",
			Model:               "opus",
			IsEpicDecomposition: false,
		})

		// Standard prompt contains Quality Gate and Worktree sections
		if !strings.Contains(prompt, "## Quality Gate") {
			t.Error("standard prompt must contain '## Quality Gate'")
		}
		if !strings.Contains(prompt, "## Worktree") {
			t.Error("standard prompt must contain '## Worktree'")
		}
		// Standard prompt should NOT contain epic decomposition workflow
		if strings.Contains(prompt, "epic decomposition mode") {
			t.Error("standard prompt must NOT contain 'epic decomposition mode'")
		}
	})
}

// extractSection returns the content from the start of header to the start of
// the next ## header. Fatal if header not found.
func extractSection(t *testing.T, prompt, header string) string {
	t.Helper()
	start := strings.Index(prompt, header)
	if start == -1 {
		t.Fatalf("section %q not found in prompt", header)
	}
	rest := prompt[start+1:]
	end := strings.Index(rest, "## ")
	if end == -1 {
		return prompt[start:]
	}
	return prompt[start : start+1+end]
}

// TestAssemblePrompt_ConfigDrivenRules verifies that the Coding Rules section
// is driven by .oro/config.yaml when ProjectRoot is set, and falls back to
// hardcoded rules when config is absent or has no coding_rules entries.
func TestAssemblePrompt_ConfigDrivenRules(t *testing.T) {
	t.Parallel()

	const hardcodedRule = "Functional first: pure functions, immutability, early returns"
	const configRule1 = "- Use interfaces for dependencies"
	const configRule2 = "- Prefer table-driven tests"

	makeParams := func(projectRoot string) worker.PromptParams {
		return worker.PromptParams{
			BeadID:             "bead-cfg",
			Title:              "Config test",
			Description:        "Test config-driven rules",
			AcceptanceCriteria: "Rules from config",
			WorktreePath:       "/tmp/wt-cfg",
			Model:              "opus",
			ProjectRoot:        projectRoot,
		}
	}

	writeConfig := func(t *testing.T, dir, content string) {
		t.Helper()
		oroDir := filepath.Join(dir, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatalf("setup MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(content), 0o600); err != nil {
			t.Fatalf("setup WriteFile: %v", err)
		}
	}

	t.Run("uses_config_rules_when_present", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeConfig(t, dir, "languages:\n  go:\n    coding_rules:\n      - \""+configRule1+"\"\n      - \""+configRule2+"\"\n")

		prompt := worker.AssemblePrompt(makeParams(dir))
		section := extractSection(t, prompt, "## Coding Rules")

		if !strings.Contains(section, configRule1) {
			t.Errorf("expected Coding Rules to contain config rule %q, got:\n%s", configRule1, section)
		}
		if !strings.Contains(section, configRule2) {
			t.Errorf("expected Coding Rules to contain config rule %q, got:\n%s", configRule2, section)
		}
		if strings.Contains(section, hardcodedRule) {
			t.Errorf("expected Coding Rules NOT to contain hardcoded rule %q when config present, got:\n%s", hardcodedRule, section)
		}
	})

	t.Run("falls_back_to_hardcoded_when_config_missing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir() // no .oro/config.yaml

		prompt := worker.AssemblePrompt(makeParams(dir))
		section := extractSection(t, prompt, "## Coding Rules")

		if !strings.Contains(section, hardcodedRule) {
			t.Errorf("expected Coding Rules to contain hardcoded rule %q when config missing, got:\n%s", hardcodedRule, section)
		}
	})

	t.Run("falls_back_to_hardcoded_when_project_root_empty", func(t *testing.T) {
		t.Parallel()

		prompt := worker.AssemblePrompt(makeParams(""))
		section := extractSection(t, prompt, "## Coding Rules")

		if !strings.Contains(section, hardcodedRule) {
			t.Errorf("expected Coding Rules to contain hardcoded rule %q when ProjectRoot empty, got:\n%s", hardcodedRule, section)
		}
	})

	t.Run("falls_back_to_hardcoded_when_coding_rules_empty", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeConfig(t, dir, "languages:\n  go:\n    test_cmd: go test ./...\n")

		prompt := worker.AssemblePrompt(makeParams(dir))
		section := extractSection(t, prompt, "## Coding Rules")

		if !strings.Contains(section, hardcodedRule) {
			t.Errorf("expected Coding Rules to contain hardcoded rule %q when config has no coding_rules, got:\n%s", hardcodedRule, section)
		}
	})
}

func TestBuildEpicDecompositionPrompt(t *testing.T) {
	t.Parallel()

	params := worker.EpicPromptParams{
		BeadID:      "oro-epic-1",
		Title:       "Implement JWT authentication",
		Description: "Add JWT auth to the API with token generation and validation",
	}

	prompt := worker.BuildEpicDecompositionPrompt(params)

	t.Run("contains_role", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(prompt, "## Role") {
			t.Error("expected prompt to contain ## Role section")
		}
	})

	t.Run("contains_epic_details", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(prompt, "oro-epic-1") {
			t.Error("expected prompt to contain epic ID")
		}
		if !strings.Contains(prompt, "Implement JWT authentication") {
			t.Error("expected prompt to contain epic title")
		}
		if !strings.Contains(prompt, "Add JWT auth") {
			t.Error("expected prompt to contain epic description")
		}
	})

	t.Run("contains_bead_craft_instructions", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(prompt, "beadcraft") || !strings.Contains(prompt, "bd create") {
			t.Error("expected prompt to contain beadcraft decomposition instructions")
		}
	})

	t.Run("contains_premortem_step", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(strings.ToLower(prompt), "premortem") {
			t.Error("expected prompt to contain premortem step")
		}
	})

	t.Run("no_tdd_or_qg", func(t *testing.T) {
		t.Parallel()
		if strings.Contains(prompt, "## TDD") {
			t.Error("epic decomposition prompt should not contain TDD section")
		}
		if strings.Contains(prompt, "## Quality Gate") {
			t.Error("epic decomposition prompt should not contain Quality Gate section")
		}
	})

	t.Run("empty_description_still_valid", func(t *testing.T) {
		t.Parallel()
		p := worker.BuildEpicDecompositionPrompt(worker.EpicPromptParams{
			BeadID: "oro-epic-2",
			Title:  "Title only epic",
		})
		if !strings.Contains(p, "oro-epic-2") {
			t.Error("expected prompt with empty description to still contain ID")
		}
	})
}

func TestAssemblePrompt_SavingLearningsSection(t *testing.T) {
	t.Parallel()

	t.Run("section_present_between_memory_and_coding_rules_no_code", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "bead-learning",
			Title:              "Test saving learnings",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Prior context",
			CodeSearchContext:  "", // No code search results
			WorktreePath:       "/tmp/wt-learning",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		// Check that section exists
		if !strings.Contains(prompt, "## Saving Learnings") {
			t.Error("expected prompt to contain '## Saving Learnings' section")
		}

		// Verify ordering: Memory -> Saving Learnings -> Coding Rules (when no Relevant Code)
		memIdx := strings.Index(prompt, "## Memory")
		learningsIdx := strings.Index(prompt, "## Saving Learnings")
		rulesIdx := strings.Index(prompt, "## Coding Rules")

		if memIdx == -1 {
			t.Fatal("## Memory section not found")
		}
		if learningsIdx == -1 {
			t.Fatal("## Saving Learnings section not found")
		}
		if rulesIdx == -1 {
			t.Fatal("## Coding Rules section not found")
		}

		if learningsIdx <= memIdx {
			t.Errorf("expected Saving Learnings after Memory (Memory at %d, Learnings at %d)", memIdx, learningsIdx)
		}
		if learningsIdx >= rulesIdx {
			t.Errorf("expected Saving Learnings before Coding Rules (Learnings at %d, Rules at %d)", learningsIdx, rulesIdx)
		}
	})

	t.Run("section_present_between_memory_and_relevant_code_with_code", func(t *testing.T) {
		t.Parallel()

		codeSearchCtx := "### pkg/example/example.go:15-30\n```go\nfunc Test() {}\n```"

		params := worker.PromptParams{
			BeadID:             "bead-learning-code",
			Title:              "Test with code",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Prior context",
			CodeSearchContext:  codeSearchCtx,
			WorktreePath:       "/tmp/wt-learning-code",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		// Verify ordering: Memory -> Saving Learnings -> Relevant Code
		memIdx := strings.Index(prompt, "## Memory")
		learningsIdx := strings.Index(prompt, "## Saving Learnings")
		codeIdx := strings.Index(prompt, "## Relevant Code")

		if memIdx == -1 || learningsIdx == -1 || codeIdx == -1 {
			t.Fatalf("expected all sections present")
		}

		if learningsIdx <= memIdx {
			t.Errorf("expected Saving Learnings after Memory (Memory at %d, Learnings at %d)", memIdx, learningsIdx)
		}
		if learningsIdx >= codeIdx {
			t.Errorf("expected Saving Learnings before Relevant Code (Learnings at %d, Code at %d)", learningsIdx, codeIdx)
		}
	})

	t.Run("section_contains_required_examples", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "bead-examples",
			Title:              "Test examples",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "",
			WorktreePath:       "/tmp/wt-examples",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		// Extract Saving Learnings section
		learningsStart := strings.Index(prompt, "## Saving Learnings")
		if learningsStart == -1 {
			t.Fatal("## Saving Learnings section not found")
		}
		learningsEnd := strings.Index(prompt[learningsStart+1:], "## ")
		var learningsSection string
		if learningsEnd == -1 {
			learningsSection = prompt[learningsStart:]
		} else {
			learningsSection = prompt[learningsStart : learningsStart+1+learningsEnd]
		}

		// Check for required content
		if !strings.Contains(learningsSection, "[MEMORY]") {
			t.Error("expected Saving Learnings section to contain '[MEMORY]' marker")
		}
		if !strings.Contains(learningsSection, "I learned") {
			t.Error("expected Saving Learnings section to contain 'I learned' example")
		}
		if !strings.Contains(learningsSection, "Gotcha:") {
			t.Error("expected Saving Learnings section to contain 'Gotcha:' example")
		}
		if !strings.Contains(learningsSection, "Note:") {
			t.Error("expected Saving Learnings section to contain 'Note:' example")
		}
		if !strings.Contains(learningsSection, "Decision:") {
			t.Error("expected Saving Learnings section to contain 'Decision:' example")
		}
	})
}

// TestPromptContainsContextThresholds verifies that the worker prompt includes
// Layer 1 context handoff instructions: "atomic step" guidance, per-model soft and
// hard threshold percentages (opus 65/85, sonnet 50/70, haiku 40/60), and a
// "create-handoff" instruction.
func TestPromptContainsContextThresholds(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-ctx-threshold",
		Title:              "Context threshold test",
		Description:        "Test context threshold instructions in prompt",
		AcceptanceCriteria: "Thresholds present in prompt",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-ctx-threshold",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Must contain "atomic step" — encourages small commits before threshold
	if !strings.Contains(prompt, "atomic step") {
		t.Error("expected prompt to contain 'atomic step'")
	}

	// Must contain "create-handoff" instruction
	if !strings.Contains(prompt, "create-handoff") {
		t.Error("expected prompt to contain 'create-handoff' instruction")
	}

	// Must contain soft threshold percentages for all models
	for _, pct := range []string{"65%", "50%", "40%"} {
		if !strings.Contains(prompt, pct) {
			t.Errorf("expected prompt to contain soft threshold %q", pct)
		}
	}

	// Must contain hard threshold percentages for all models (soft + 20)
	for _, pct := range []string{"85%", "70%", "60%"} {
		if !strings.Contains(prompt, pct) {
			t.Errorf("expected prompt to contain hard threshold %q", pct)
		}
	}
}

func TestAssemblePrompt_EndToEnd(t *testing.T) {
	t.Parallel()

	// Create a temporary project directory with .oro/config.yaml
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test config dir
		t.Fatalf("failed to create .oro dir: %v", err)
	}

	// Write a config.yaml with coding rules
	configContent := `languages:
  go:
    coding_rules:
      - "Functional first: pure functions, immutability"
      - "Error handling: wrap with context"
      - "Testing: use table-driven tests"
`
	configPath := filepath.Join(oroDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil { //nolint:gosec // test config file
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	params := worker.PromptParams{
		BeadID:             "oro-test-endtoend",
		Title:              "End-to-end ProjectRoot test",
		Description:        "Test that ProjectRoot loads config-driven coding rules",
		AcceptanceCriteria: "Prompt includes config-driven rules",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-endtoend",
		Model:              "opus",
		ProjectRoot:        tmpDir,
	}

	prompt := worker.AssemblePrompt(params)

	// Verify that the Coding Rules section includes config-driven rules
	if !strings.Contains(prompt, "Functional first: pure functions, immutability") {
		t.Errorf("expected Coding Rules section to contain config-driven rule from config.yaml, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Error handling: wrap with context") {
		t.Errorf("expected Coding Rules section to contain error handling rule from config.yaml, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Testing: use table-driven tests") {
		t.Errorf("expected Coding Rules section to contain testing rule from config.yaml, got:\n%s", prompt)
	}

	// Verify that Coding Rules section is still present
	if !strings.Contains(prompt, "## Coding Rules") {
		t.Error("expected prompt to contain ## Coding Rules section")
	}
}

// TestPromptSoftThresholdSaysGitCommit verifies that the Context Handoff section
// explicitly instructs agents to run 'git add' and 'git commit' at the soft threshold,
// rather than the ambiguous 'commit current work' which haiku agents misinterpreted
// as "write files to disk" (oro-3eve).
func TestPromptSoftThresholdSaysGitCommit(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-soft-threshold",
		Title:              "Soft threshold git commit test",
		Description:        "Test that soft threshold instructs explicit git commands",
		AcceptanceCriteria: "Prompt contains git add and git commit",
		WorktreePath:       "/tmp/wt-soft-threshold",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Context Handoff section for focused assertions
	handoffStart := strings.Index(prompt, "## Context Handoff")
	if handoffStart == -1 {
		t.Fatal("expected prompt to contain ## Context Handoff section")
	}
	handoffEnd := strings.Index(prompt[handoffStart+1:], "## ")
	var handoffSection string
	if handoffEnd == -1 {
		handoffSection = prompt[handoffStart:]
	} else {
		handoffSection = prompt[handoffStart : handoffStart+1+handoffEnd]
	}

	// Must explicitly say 'git add' — not just 'commit current work'
	if !strings.Contains(handoffSection, "git add") {
		t.Errorf("soft threshold instruction must contain 'git add' (not just 'commit current work'). Got:\n%s", handoffSection)
	}

	// Must explicitly say 'git commit' — not just 'commit current work'
	if !strings.Contains(handoffSection, "git commit") {
		t.Errorf("soft threshold instruction must contain 'git commit' (not just 'commit current work'). Got:\n%s", handoffSection)
	}
}

// TestPromptConstrainsDeadCodeNoOps verifies that the Constraints section warns
// against replacing function calls with blank identifier assignments — a pattern
// observed across multiple workers during QG retry cycles (oro-7nzy).
func TestPromptConstrainsDeadCodeNoOps(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-noop-guard",
		Title:              "Dead-code no-op guard test",
		Description:        "Test that prompt forbids blank assignment replacement",
		AcceptanceCriteria: "Prompt warns against _, _ = fn patterns",
		WorktreePath:       "/tmp/wt-noop-guard",
		Model:              "sonnet",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Constraints section
	constraintsStart := strings.Index(prompt, "## Constraints")
	if constraintsStart == -1 {
		t.Fatal("expected prompt to contain ## Constraints section")
	}
	constraintsEnd := strings.Index(prompt[constraintsStart+1:], "## ")
	var constraintsSection string
	if constraintsEnd == -1 {
		constraintsSection = prompt[constraintsStart:]
	} else {
		constraintsSection = prompt[constraintsStart : constraintsStart+1+constraintsEnd]
	}

	// Must warn about blank identifier replacement of function calls
	if !strings.Contains(constraintsSection, "_ =") {
		t.Errorf("Constraints must warn against '_ =' blank assignment pattern. Got:\n%s", constraintsSection)
	}
	if !strings.Contains(strings.ToLower(constraintsSection), "never replace") {
		t.Errorf("Constraints must say NEVER replace function calls with blank assignments. Got:\n%s", constraintsSection)
	}
}

// TestContextHandoffPrompt verifies that the Context Handoff section explicitly
// tells the agent to exit immediately after invoking create-handoff, preventing
// agents from continuing work after handing off (which defeats the handoff).
func TestContextHandoffPrompt(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-ctx-handoff",
		Title:              "Context handoff exit test",
		Description:        "Verify prompt tells agent to stop after handoff",
		AcceptanceCriteria: "Prompt contains exit instruction after create-handoff",
		WorktreePath:       "/tmp/wt-ctx-handoff",
		Model:              "sonnet",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Context Handoff section for focused assertions
	handoffStart := strings.Index(prompt, "## Context Handoff")
	if handoffStart == -1 {
		t.Fatal("expected prompt to contain ## Context Handoff section")
	}
	handoffEnd := strings.Index(prompt[handoffStart+1:], "## ")
	var handoffSection string
	if handoffEnd == -1 {
		handoffSection = prompt[handoffStart:]
	} else {
		handoffSection = prompt[handoffStart : handoffStart+1+handoffEnd]
	}

	// Find the create-handoff instruction within the section
	createHandoffIdx := strings.Index(handoffSection, "create-handoff")
	if createHandoffIdx == -1 {
		t.Fatal("expected Context Handoff section to contain 'create-handoff' instruction")
	}

	// The text after the create-handoff mention must include an explicit stop instruction
	afterHandoff := handoffSection[createHandoffIdx:]
	if !strings.Contains(afterHandoff, "exit immediately") && !strings.Contains(afterHandoff, "do not continue") {
		t.Errorf("expected Context Handoff section to contain 'exit immediately' or 'do not continue' after create-handoff instruction.\nGot (after create-handoff):\n%s", afterHandoff)
	}
}

// TestAssemblePrompt_BugP0Rule verifies that the Failure section contains
// the mandatory rule: all bug beads must use --priority=0.
func TestAssemblePrompt_BugP0Rule(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-p0-rule",
		Title:              "Bug P0 rule test",
		Description:        "Verify bug P0 rule in prompt",
		AcceptanceCriteria: "Prompt contains bug P0 rule",
		WorktreePath:       "/tmp/wt-p0-rule",
		Model:              "sonnet",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Failure section
	failureStart := strings.Index(prompt, "## Failure")
	if failureStart == -1 {
		t.Fatal("expected prompt to contain ## Failure section")
	}
	failureEnd := strings.Index(prompt[failureStart+1:], "## ")
	var failureSection string
	if failureEnd == -1 {
		failureSection = prompt[failureStart:]
	} else {
		failureSection = prompt[failureStart : failureStart+1+failureEnd]
	}

	if !strings.Contains(failureSection, "All bug beads MUST use --priority=0") {
		t.Errorf("Failure section must contain 'All bug beads MUST use --priority=0'. Got:\n%s", failureSection)
	}
}

func TestCreateHandoffWritesSentinel(t *testing.T) {
	t.Parallel()

	// Walk up from the working directory to find .claude/skills/.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		candidate := filepath.Join(dir, ".claude", "skills", "create-handoff", "SKILL.md")
		if _, err := os.Stat(candidate); err == nil {
			data, readErr := os.ReadFile(candidate) //nolint:gosec // reads fixed path under .claude/
			if readErr != nil {
				t.Fatalf("failed to read %s: %v", candidate, readErr)
			}
			content := string(data)
			if !strings.Contains(content, "handoff_done") {
				t.Error("create-handoff SKILL.md must contain instruction to write .oro/handoff_done sentinel file")
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find .claude/skills/create-handoff/SKILL.md by walking up from working directory")
		}
		dir = parent
	}
}
