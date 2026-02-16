package worker_test

import (
	"strings"
	"testing"

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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "bd create") {
		t.Error("expected Beads Tools section to contain 'bd create'")
	}
	if !strings.Contains(prompt, "bd close") {
		t.Error("expected Beads Tools section to contain 'bd close'")
	}
	if !strings.Contains(prompt, "bd dep add") {
		t.Error("expected Beads Tools section to contain 'bd dep add'")
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
	params := worker.PromptParams{BeadID: "bead-no-retry", Title: "No retry", Description: "First attempt", AcceptanceCriteria: "Tests pass", WorktreePath: "/tmp/wt-no-retry", Model: "claude-opus-4-6", Attempt: 0}
	prompt := worker.AssemblePrompt(params)
	if strings.Contains(prompt, "Retry attempt") {
		t.Error("prompt should NOT contain Retry attempt when Attempt=0")
	}
}

func TestAssemblePrompt_AttemptPositive_IncludesRetryNote(t *testing.T) {
	t.Parallel()
	params := worker.PromptParams{BeadID: "bead-retry", Title: "Retry", Description: "Second attempt", AcceptanceCriteria: "Tests pass", WorktreePath: "/tmp/wt-retry", Model: "claude-opus-4-6", Attempt: 2}
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
		Model:              "claude-opus-4-6",
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
