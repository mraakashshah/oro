package worker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// expectedSectionHeaders lists all 12 section headers in order.
var expectedSectionHeaders = []string{
	"## Role",
	"## Task",
	"## Cards",
	"## Coding Rules",
	"## TDD",
	"## Quality Gate",
	"## Worktree",
	"## Git",
	"## Task Tools",
	"## Constraints",
	"## Autonomy",
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
		BeadID:       "bead-nomem",
		Title:        "Test bead",
		Description:  "A test bead",
		WorktreePath: "/tmp/wt-nomem",
		Model:        "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Cards section header should be present even when no cards are provided.
	if !strings.Contains(prompt, "## Cards") {
		t.Error("expected prompt to contain ## Cards header even when no cards provided")
	}
	// Should contain the empty-cards placeholder.
	if !strings.Contains(prompt, "No relevant cards for this task") {
		t.Error("expected prompt to contain 'No relevant cards' placeholder when Cards is empty")
	}
}

func TestAssemblePrompt_NonEmptyMemoryContext(t *testing.T) {
	t.Parallel()

	// MemoryContext is deprecated after D.4 (subsumed by Cards). Verify it is
	// no longer rendered — setting it must not cause memory content to appear.
	memCtx := "- [lesson] always run go vet before committing\n- [gotcha] FTS5 needs triggers"

	params := worker.PromptParams{
		BeadID:        "bead-withmem",
		Title:         "Test bead with memory",
		Description:   "A test bead",
		MemoryContext: memCtx,
		WorktreePath:  "/tmp/wt-withmem",
		Model:         "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if strings.Contains(prompt, "always run go vet before committing") {
		t.Error("MemoryContext must NOT be rendered after D.4 cutover — use Cards instead")
	}
	if strings.Contains(prompt, "FTS5 needs triggers") {
		t.Error("MemoryContext must NOT be rendered after D.4 cutover — use Cards instead")
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
	if !strings.Contains(prompt, "one task at a time") {
		t.Error("expected Role section to contain 'one task at a time'")
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

	if strings.Contains(prompt, "./quality_gate.sh") {
		t.Error("Quality Gate section should not instruct the subprocess to run './quality_gate.sh'")
	}
	if strings.Contains(prompt, "./scripts/quality_gate.sh") {
		t.Error("Quality Gate section should not instruct the subprocess to run './scripts/quality_gate.sh'")
	}
	if strings.Contains(prompt, "ORO_SKIP_MUTATION") {
		t.Error("Quality Gate section should not teach agents to use ORO_SKIP_MUTATION for local QG")
	}
	if strings.Contains(prompt, "Mutation runs in the push quality gate") {
		t.Error("Quality Gate section should not say mutation runs by default on push")
	}
	if strings.Contains(prompt, "ORO_RUN_MUTATION") {
		t.Error("Quality Gate section should not teach agents to use ORO_RUN_MUTATION")
	}
	if strings.Contains(prompt, "--mutation-testing") {
		t.Error("Quality Gate section should not instruct the subprocess to use --mutation-testing")
	}
	if !strings.Contains(prompt, "worker harness") || !strings.Contains(prompt, "full quality gate") {
		t.Error("expected Quality Gate section to delegate the full quality gate to the worker harness")
	}
}

func TestAssemblePrompt_DelegatesAuthoritativeQG(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-qg-delegate",
		Title:              "QG delegation test",
		Description:        "Test quality gate delegation section",
		AcceptanceCriteria: "Test: pkg/worker/prompt_test.go:TestAssemblePrompt_DelegatesAuthoritativeQG | Cmd: go test ./pkg/worker -run '^TestAssemblePrompt_DelegatesAuthoritativeQG$' -count=1 | Assert: PASS",
		WorktreePath:       "/tmp/wt-qg-delegate",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	if !strings.Contains(prompt, "acceptance") || !strings.Contains(prompt, "focused") {
		t.Error("expected prompt to require the task acceptance command and focused verification")
	}
	if !strings.Contains(prompt, "worker harness") || !strings.Contains(prompt, "full quality gate") {
		t.Error("expected prompt to identify the worker harness as full-QG owner")
	}
	for _, forbidden := range []string{"./quality_gate.sh", "./scripts/quality_gate.sh", "--mutation-testing"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt must not instruct coding subprocess to run %q", forbidden)
		}
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

	if !strings.Contains(prompt, "NEVER run `git push`") {
		t.Error("expected Constraints section to contain 'NEVER run `git push`'")
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
	if !strings.Contains(prompt, "oro task create") {
		t.Error("expected Failure section to mention oro task create for decomposition")
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
	if !strings.Contains(prompt, "record learnings with the cards flow") {
		t.Error("expected Exit section to tell workers to record learnings with the cards flow")
	}
	if !strings.Contains(prompt, "oro current") {
		t.Error("expected Exit section to mention oro current for learning context")
	}
	if !strings.Contains(prompt, "oro cards review-queue") {
		t.Error("expected Exit section to mention the cards review queue")
	}
	if strings.Contains(prompt, "oro remember") {
		t.Error("Exit section must not mention retired oro remember command")
	}
}

func TestAssemblePrompt_BeadToolsContent(t *testing.T) {
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

	if !strings.Contains(prompt, "oro task create") {
		t.Error("expected Task Tools section to contain 'oro task create'")
	}
	if !strings.Contains(prompt, "oro task dep add") {
		t.Error("expected Task Tools section to contain 'oro task dep add'")
	}
}

func TestPromptGolden(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-prompt-golden",
		Title:              "Prompt golden",
		Description:        "Guard worker prompt command strings",
		AcceptanceCriteria: "Prompt uses oro bead commands",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-prompt-golden",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)
	epicPrompt := worker.BuildEpicDecompositionPrompt(worker.EpicPromptParams{
		BeadID:      "oro-epic-golden",
		Title:       "Epic prompt golden",
		Description: "Guard epic decomposition command strings",
	})
	combined := prompt + "\n" + epicPrompt

	for _, oldCommand := range []string{
		"bd create",
		"bd update",
		"bd dep add",
		"bd show",
	} {
		if strings.Contains(combined, oldCommand) {
			t.Fatalf("prompt must not contain legacy bead command %q", oldCommand)
		}
	}

	for _, newCommand := range []string{
		"oro task create",
		"oro task dep add",
		"oro task show",
	} {
		if !strings.Contains(combined, newCommand) {
			t.Fatalf("prompt must contain %q", newCommand)
		}
	}

	for _, forbiddenGuidance := range []string{
		"adds a backwards dependency",
		"Do NOT use `oro bead create --parent`",
		"do NOT use `--parent` flag on create",
	} {
		if strings.Contains(combined, forbiddenGuidance) {
			t.Fatalf("prompt must not describe native create --parent as unsafe: found %q", forbiddenGuidance)
		}
	}

	if !strings.Contains(epicPrompt, "--parent oro-epic-golden") {
		t.Fatalf("epic decomposition prompt must use native create --parent for child beads")
	}
}

// TestAssemblePrompt_TaskToolsDoesNotContainTaskClose verifies that the Task
// Tools section does NOT list `oro task close` as a worker tool. Workers must not
// close tasks — the dispatcher handles task closure after merging to main.
//
// Context: oro-u74j bug — listing a close command in Task Tools contradicts the
// Exit section's instruction that the dispatcher handles closure, leading
// workers to close tasks without merging to main.
func TestAssemblePrompt_TaskToolsDoesNotContainTaskClose(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-no-close",
		Title:              "No oro task close in tools",
		Description:        "Workers must not close beads",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-no-close",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract just the Task Tools section.
	toolsStart := strings.Index(prompt, "## Task Tools")
	if toolsStart == -1 {
		t.Fatal("expected prompt to contain ## Task Tools section")
	}
	toolsEnd := strings.Index(prompt[toolsStart+1:], "## ")
	var toolsSection string
	if toolsEnd == -1 {
		toolsSection = prompt[toolsStart:]
	} else {
		toolsSection = prompt[toolsStart : toolsStart+1+toolsEnd]
	}

	if strings.Contains(toolsSection, "oro task close") || strings.Contains(toolsSection, "oro bead close") {
		t.Errorf("Task Tools section must NOT contain close commands — dispatcher handles task closure (oro-u74j). Got:\n%s", toolsSection)
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

func TestAssemblePrompt_FailureSectionHasOroBeadCreateExamples(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-fail-ex",
		Title:              "Failure examples test",
		Description:        "Test failure section has oro task create examples",
		AcceptanceCriteria: "oro task create examples present in Failure section",
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

	// Each failure mode should have a concrete oro task create command example.
	checks := []struct {
		name   string
		substr string
	}{
		{"oro task create --title flag", `oro task create --title=`},
		{"test failure bug type+priority", `--type=bug --priority=0`},
		{"decompose uses native create parent", `oro task create --title="<subtask>" --type=task --parent <task-id>`},
		{"context limit handoff", `oro task create --title="Continue:`},
		{"blocker bug creation", `oro task create --title="Blocker:`},
		{"oro task dep add example", `oro task dep add`},
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
	// After D.4 cutover, Previous Feedback is subsumed by Cards (§13.2).
	// Feedback text no longer appears as a dedicated section; the retry
	// note remains in the Task section.
	params := worker.PromptParams{
		BeadID:       "bead-fb",
		Title:        "Fix bug",
		WorktreePath: "/tmp/wt-fb",
		Model:        "opus",
		Attempt:      1,
		Feedback:     "FAIL: TestFoo expected 42 got 0",
	}
	prompt := worker.AssemblePrompt(params)
	if strings.Contains(prompt, "## Previous Feedback") {
		t.Error("prompt must NOT contain ## Previous Feedback section after D.4 cutover")
	}
	// Retry note is still injected into the Task section.
	if !strings.Contains(prompt, "Retry attempt 1") {
		t.Error("expected retry note in Task section")
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

	// Relevant Code should appear AFTER Cards section
	memIdx := strings.Index(prompt, "## Cards")
	codeIdx := strings.Index(prompt, "## Relevant Code")
	if memIdx == -1 || codeIdx == -1 || codeIdx <= memIdx {
		t.Error("expected '## Relevant Code' section to appear after '## Cards' section")
	}

	// Relevant Code should appear BEFORE Coding Rules section
	rulesIdx := strings.Index(prompt, "## Coding Rules")
	if rulesIdx == -1 || codeIdx >= rulesIdx {
		t.Error("expected '## Relevant Code' section to appear before '## Coding Rules' section")
	}
}

// TestAssemblePrompt_CodeSearchSection verifies that the ## Relevant Code section
// is rendered conditionally: present when CodeSearchContext is non-empty, omitted
// when empty, and positioned correctly between Cards and Coding Rules sections.
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

		memIdx := strings.Index(prompt, "## Cards")
		codeIdx := strings.Index(prompt, "## Relevant Code")
		rulesIdx := strings.Index(prompt, "## Coding Rules")

		if memIdx == -1 {
			t.Fatal("## Cards section not found in prompt")
		}
		if codeIdx == -1 {
			t.Fatal("## Relevant Code section not found in prompt")
		}
		if rulesIdx == -1 {
			t.Fatal("## Coding Rules section not found in prompt")
		}

		if codeIdx <= memIdx {
			t.Errorf("expected ## Relevant Code to appear after ## Cards (Cards at %d, Code at %d)", memIdx, codeIdx)
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

	// Check that handoff template attaches child bead with native create --parent.
	if !strings.Contains(failureSection, "--parent oro-xyz123") {
		t.Error("expected handoff template to contain 'oro task create ... --parent oro-xyz123'")
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
	if strings.Contains(exitSection, "oro bead close") {
		t.Error("Exit section must NOT tell worker to run 'oro bead close' (dispatcher handles this)")
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
			AcceptanceCriteria:  "Test: auth_test.go:TestJWT | Cmd: go test ./auth -run TestJWT -count=1 | Assert: PASS",
			Model:               "opus",
			IsEpicDecomposition: true,
		})

		// Must contain beadcraft instructions
		if !strings.Contains(prompt, "beadcraft") {
			t.Errorf("expected epic decomp prompt to contain 'beadcraft', got:\n%s", prompt)
		}
		// Must contain oro task create and parent wiring via native create --parent.
		if !strings.Contains(prompt, "oro task create") {
			t.Errorf("expected epic decomp prompt to contain 'oro task create', got:\n%s", prompt)
		}
		if !strings.Contains(prompt, "--parent oro-epic-1") {
			t.Errorf("expected epic decomp prompt to contain native create --parent, got:\n%s", prompt)
		}
		// Must NOT contain standard worker sections
		if strings.Contains(prompt, "## Quality Gate") {
			t.Error("epic decomp prompt must NOT contain '## Quality Gate'")
		}
		if strings.Contains(prompt, "## Worktree") {
			t.Error("epic decomp prompt must NOT contain '## Worktree'")
		}
		if !strings.Contains(prompt, "Cmd: go test ./auth -run TestJWT -count=1") {
			t.Errorf("expected epic decomp prompt to include acceptance criteria, got:\n%s", prompt)
		}
		if !strings.Contains(prompt, "Run the epic acceptance command") {
			t.Errorf("expected epic decomp prompt to require goal-satisfaction gate, got:\n%s", prompt)
		}
		if !strings.Contains(prompt, "Do NOT create child tasks if the command passes") {
			t.Errorf("expected epic decomp prompt to stop creating duplicate children when satisfied, got:\n%s", prompt)
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

func TestBuildAssignPrompt_MapsNewFields(t *testing.T) {
	t.Parallel()

	t.Run("maps_gitlog_to_prompt", func(t *testing.T) {
		t.Parallel()

		gitLog := "commit abc123: Add feature\ncommit def456: Fix bug"
		prompt, _ := worker.BuildAssignPrompt(&protocol.AssignPayload{
			BeadID:             "oro-test-1",
			Worktree:           "/tmp/wt-test",
			Title:              "Test task",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			Model:              "opus",
			GitLog:             gitLog,
		})

		// Verify prompt contains Git History section with the git log
		if !strings.Contains(prompt, "## Git History") {
			t.Error("expected prompt to contain '## Git History' section")
		}
		if !strings.Contains(prompt, gitLog) {
			t.Error("expected prompt to contain git log content")
		}
	})

	t.Run("maps_worker_program_to_prompt", func(t *testing.T) {
		t.Parallel()

		workerProgram := "Using skill: test-driven-development"
		prompt, _ := worker.BuildAssignPrompt(&protocol.AssignPayload{
			BeadID:             "oro-test-2",
			Worktree:           "/tmp/wt-test",
			Title:              "Test task",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			Model:              "opus",
			WorkerProgram:      workerProgram,
		})

		// Verify prompt contains Worker Program section with the worker program
		if !strings.Contains(prompt, "## Worker Program") {
			t.Error("expected prompt to contain '## Worker Program' section")
		}
		if !strings.Contains(prompt, workerProgram) {
			t.Error("expected prompt to contain worker program content")
		}
	})

	t.Run("maps_both_fields_together", func(t *testing.T) {
		t.Parallel()

		gitLog := "commit xyz789: Another change"
		workerProgram := "Invoking systematic-debugging"
		prompt, _ := worker.BuildAssignPrompt(&protocol.AssignPayload{
			BeadID:             "oro-test-3",
			Worktree:           "/tmp/wt-test",
			Title:              "Test task",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			Model:              "opus",
			GitLog:             gitLog,
			WorkerProgram:      workerProgram,
		})

		// Verify both sections are present with content
		if !strings.Contains(prompt, "## Git History") {
			t.Error("expected prompt to contain '## Git History' section")
		}
		if !strings.Contains(prompt, gitLog) {
			t.Error("expected prompt to contain git log content")
		}
		if !strings.Contains(prompt, "## Worker Program") {
			t.Error("expected prompt to contain '## Worker Program' section")
		}
		if !strings.Contains(prompt, workerProgram) {
			t.Error("expected prompt to contain worker program content")
		}
	})
}

func TestBuildAssignPrompt_UsesCardsContext(t *testing.T) {
	t.Parallel()

	prompt, _ := worker.BuildAssignPrompt(&protocol.AssignPayload{
		BeadID:             "oro-cards-1",
		Worktree:           "/tmp/wt-cards",
		Title:              "Carry cards",
		Description:        "Render relevant cards in worker assignment prompts",
		AcceptanceCriteria: "Cards are rendered",
		MemoryContext:      "legacy memory context should not render",
		Cards: cards.RelevantCards{
			Deck: []cards.DeckCard{
				{
					ID:          "card-deck-1",
					Type:        cards.CardTypePattern,
					Title:       "Seeded deck card",
					BodySummary: "Deck summary should be available.",
					Score:       8.5,
				},
			},
			Inlined: []cards.InlinedCard{
				{
					ID:       "card-inline-1",
					Type:     cards.CardTypeDecision,
					Title:    "Seeded inline card",
					BodyFull: "Use cards as the worker prompt knowledge source.",
					Score:    13.75,
				},
			},
		},
	})

	cardsSection := extractSection(t, prompt, "## Cards")
	if !strings.Contains(cardsSection, "Seeded inline card") {
		t.Fatalf("Cards section missing inlined card title:\n%s", cardsSection)
	}
	if !strings.Contains(cardsSection, "Use cards as the worker prompt knowledge source.") {
		t.Fatalf("Cards section missing inlined card body:\n%s", cardsSection)
	}
	if strings.Contains(prompt, "legacy memory context should not render") {
		t.Fatalf("legacy MemoryContext rendered in prompt:\n%s", prompt)
	}
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
		if !strings.Contains(prompt, "beadcraft") || !strings.Contains(prompt, "oro task create") {
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

// TestEpicDecompPromptCreatesBranch verifies that the epic decomposition prompt
// includes instructions for creating a feature branch and a rebase bead.
func TestEpicDecompPromptCreatesBranch(t *testing.T) {
	t.Parallel()

	params := worker.EpicPromptParams{
		BeadID:      "oro-epic-42",
		Title:       "Refactor authentication system",
		Description: "Break apart and modernize the auth subsystem",
	}

	prompt := worker.BuildEpicDecompositionPrompt(params)

	// Check for git branch instruction with epic/<epicID> main format
	if !strings.Contains(prompt, "git branch") || !strings.Contains(prompt, "epic/oro-epic-42") {
		t.Error("expected prompt to contain instruction to create branch: git branch epic/oro-epic-42 main")
	}

	// Check for rebase bead creation instruction with --tag rebase
	if !strings.Contains(prompt, "--tag rebase") {
		t.Error("expected prompt to contain instruction to create rebase bead with --tag rebase")
	}

	// Check for instruction to make rebase bead depend on all siblings
	if !strings.Contains(prompt, "depend") || !strings.Contains(prompt, "sibling") {
		t.Error("expected prompt to contain instruction to make rebase bead depend on all siblings")
	}
}

// TestEpicDecompPromptOmitsBranchWhenBeadIDEmpty verifies that when BeadID is
// empty, the epic decomposition prompt omits the Branch & Rebase Bead section.
func TestEpicDecompPromptOmitsBranchWhenBeadIDEmpty(t *testing.T) {
	t.Parallel()

	params := worker.EpicPromptParams{
		BeadID:      "",
		Title:       "Refactor authentication system",
		Description: "Break apart and modernize the auth subsystem",
	}

	prompt := worker.BuildEpicDecompositionPrompt(params)

	if strings.Contains(prompt, "Branch & Rebase Bead") {
		t.Error("expected prompt to omit Branch & Rebase Bead section when BeadID is empty")
	}
	if strings.Contains(prompt, "git branch epic/") {
		t.Error("expected prompt to omit git branch instruction when BeadID is empty")
	}
}

// TestPromptContainsContextThresholds verifies that the worker prompt includes
// Layer 1 context handoff instructions: "atomic step" guidance, per-model soft and
// hard threshold percentages (all models 40/50), and a
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

	// Must contain soft threshold percentage (40% for all models)
	if !strings.Contains(prompt, "40%") {
		t.Error("expected prompt to contain soft threshold '40%'")
	}

	// Must contain hard threshold percentage (50% for all models)
	if !strings.Contains(prompt, "50%") {
		t.Error("expected prompt to contain hard threshold '50%'")
	}
}

func TestPromptUsesNeutralTierLanguage(t *testing.T) {
	t.Parallel()

	workerPrompt := worker.AssemblePrompt(worker.PromptParams{
		BeadID:             "bead-neutral-tier",
		Title:              "Neutral tier wording",
		Description:        "Ensure worker prompt prefers neutral tier language",
		AcceptanceCriteria: "Prompt uses fast/balanced/deep/background tiers",
		WorktreePath:       "/tmp/wt-neutral-tier",
		Model:              "opus",
	})
	epicPrompt := worker.BuildEpicDecompositionPrompt(worker.EpicPromptParams{
		BeadID:      "epic-neutral-tier",
		Title:       "Neutral tier decomposition",
		Description: "Ensure decomposition prompt avoids Claude-family routing terms",
	})

	for _, term := range []string{"fast", "balanced", "deep", "background"} {
		if !strings.Contains(workerPrompt, term) {
			t.Fatalf("worker prompt missing neutral tier term %q", term)
		}
		if !strings.Contains(epicPrompt, term) {
			t.Fatalf("epic decomposition prompt missing neutral tier term %q", term)
		}
	}

	workerHandoffStart := strings.Index(workerPrompt, "## Context Handoff")
	if workerHandoffStart == -1 {
		t.Fatal("worker prompt missing Context Handoff section")
	}
	workerHandoffEnd := strings.Index(workerPrompt[workerHandoffStart+1:], "## ")
	workerHandoff := workerPrompt[workerHandoffStart:]
	if workerHandoffEnd != -1 {
		workerHandoff = workerPrompt[workerHandoffStart : workerHandoffStart+1+workerHandoffEnd]
	}
	for _, legacy := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(workerHandoff, legacy) {
			t.Fatalf("worker Context Handoff should not use legacy model term %q as the primary control surface", legacy)
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

// TestPromptSoftThresholdUsesNonInteractiveGitCommit verifies that the Context
// Handoff section explicitly instructs agents to run git add and a
// non-interactive git commit at the soft threshold. A bare `git commit` opens
// $EDITOR for the message and can strand workers in an editor.
func TestPromptSoftThresholdUsesNonInteractiveGitCommit(t *testing.T) {
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

	// Must explicitly say 'git commit -m' — not just bare 'git commit' or
	// 'commit current work'.
	if !strings.Contains(handoffSection, "git commit -m") {
		t.Errorf("soft threshold instruction must contain 'git commit' (not just 'commit current work'). Got:\n%s", handoffSection)
	}
	if strings.Contains(handoffSection, "git add && git commit`") {
		t.Errorf("soft threshold instruction must not contain bare git commit. Got:\n%s", handoffSection)
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
// the mandatory rule: all bug tasks must use --priority=0.
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

	if !strings.Contains(failureSection, "All bug tasks MUST use --priority=0") {
		t.Errorf("Failure section must contain 'All bug tasks MUST use --priority=0'. Got:\n%s", failureSection)
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

// TestAssemblePrompt_AutonomySectionPresent verifies that the worker prompt
// includes an ## Autonomy section between Constraints and Context Handoff,
// containing "full authority" and "3 strategies" phrases.
func TestAssemblePrompt_AutonomySectionPresent(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-autonomy",
		Title:              "Autonomy section test",
		Description:        "Verify autonomy section in prompt",
		AcceptanceCriteria: "Prompt contains autonomy section",
		WorktreePath:       "/tmp/wt-autonomy",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// Must contain ## Autonomy header
	if !strings.Contains(prompt, "## Autonomy") {
		t.Fatal("expected prompt to contain '## Autonomy' section header")
	}

	// Extract Autonomy section for focused assertions
	autonomySection := extractSection(t, prompt, "## Autonomy")

	// Must contain "full authority" phrase
	if !strings.Contains(autonomySection, "full authority") {
		t.Errorf("expected Autonomy section to contain 'full authority'. Got:\n%s", autonomySection)
	}

	// Must contain "3 strategies" phrase
	if !strings.Contains(autonomySection, "3 strategies") {
		t.Errorf("expected Autonomy section to contain '3 strategies'. Got:\n%s", autonomySection)
	}

	// Autonomy must appear between Constraints and Context Handoff
	constraintsIdx := strings.Index(prompt, "## Constraints")
	autonomyIdx := strings.Index(prompt, "## Autonomy")
	handoffIdx := strings.Index(prompt, "## Context Handoff")

	if constraintsIdx == -1 || autonomyIdx == -1 || handoffIdx == -1 {
		t.Fatal("expected all three sections to be present")
	}
	if autonomyIdx <= constraintsIdx {
		t.Errorf("expected ## Autonomy (at %d) to appear after ## Constraints (at %d)", autonomyIdx, constraintsIdx)
	}
	if autonomyIdx >= handoffIdx {
		t.Errorf("expected ## Autonomy (at %d) to appear before ## Context Handoff (at %d)", autonomyIdx, handoffIdx)
	}
}

func TestAssemblePromptIncludesTargetBranch(t *testing.T) {
	t.Parallel()

	// Test 1: TargetBranch set explicitly
	params := worker.PromptParams{
		BeadID:             "bead-branch",
		Title:              "Implement feature",
		Description:        "Feature description",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-branch",
		Model:              "opus",
		TargetBranch:       "develop",
	}

	prompt := worker.AssemblePrompt(params)

	// Should contain the specific merge target rendering
	if !strings.Contains(prompt, "merges to branch `develop`") {
		t.Error("expected prompt to contain explicit target branch 'develop'")
	}

	// Test 2: TargetBranch empty, should default to main
	paramsEmpty := worker.PromptParams{
		BeadID:             "bead-mainbranch",
		Title:              "Implement feature",
		Description:        "Feature description",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-mainbranch",
		Model:              "opus",
		TargetBranch:       "", // Empty should default to main
	}

	promptEmpty := worker.AssemblePrompt(paramsEmpty)

	// Should mention main as the default merge target (not just in constraints)
	if !strings.Contains(promptEmpty, "merges to branch `main`") {
		t.Error("expected prompt to default to 'main' when TargetBranch is empty")
	}
}

// TestAssemblePrompt_GitHistoryPresent verifies that when PromptParams.GitLog is set,
// the prompt contains a ## Git History section with the git log content.
func TestAssemblePrompt_GitHistoryPresent(t *testing.T) {
	t.Parallel()

	gitLog := `commit abc123def456 (HEAD -> main, origin/main)
Author: Test User <test@example.com>
Date:   Wed Mar 19 2026 10:00:00 +0000

    feat(core): add new feature`

	params := worker.PromptParams{
		BeadID:             "bead-git-history",
		Title:              "Git history test",
		Description:        "Test git history section",
		AcceptanceCriteria: "Git history section present",
		WorktreePath:       "/tmp/wt-git-history",
		Model:              "opus",
		GitLog:             gitLog,
	}

	prompt := worker.AssemblePrompt(params)

	// Should contain ## Git History header
	if !strings.Contains(prompt, "## Git History") {
		t.Error("expected prompt to contain '## Git History' section when GitLog is set")
	}

	// Should contain the git log content
	if !strings.Contains(prompt, "abc123def456") {
		t.Error("expected prompt to contain git log content")
	}
	if !strings.Contains(prompt, "feat(core): add new feature") {
		t.Error("expected prompt to contain commit message")
	}
}

// TestAssemblePrompt_GitHistoryEmpty verifies that when PromptParams.GitLog is empty,
// the ## Git History section is omitted entirely from the prompt.
func TestAssemblePrompt_GitHistoryEmpty(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-no-git-history",
		Title:              "No git history test",
		Description:        "Test without git history",
		AcceptanceCriteria: "Git history section omitted",
		WorktreePath:       "/tmp/wt-no-git-history",
		Model:              "opus",
		GitLog:             "", // Empty GitLog
	}

	prompt := worker.AssemblePrompt(params)

	// Should NOT contain ## Git History section
	if strings.Contains(prompt, "## Git History") {
		t.Error("expected prompt to omit '## Git History' section when GitLog is empty")
	}
}

// TestAssemblePrompt_WorkerProgramPresent verifies that the Worker Program section
// is rendered conditionally: present when PromptParams.WorkerProgram is non-empty,
// omitted when empty, and positioned correctly between Coding Rules and TDD sections.
func TestAssemblePrompt_WorkerProgramPresent(t *testing.T) {
	t.Parallel()

	t.Run("section_omitted_when_empty", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "bead-no-program",
			Title:              "No worker program",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Some context",
			WorkerProgram:      "", // Empty
			WorktreePath:       "/tmp/wt-no-program",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		if strings.Contains(prompt, "## Worker Program") {
			t.Error("expected ## Worker Program section to be omitted when WorkerProgram is empty")
		}
	})

	t.Run("section_present_when_non_empty", func(t *testing.T) {
		t.Parallel()

		workerProgram := `package main

import "fmt"

func main() {
	fmt.Println("Hello from worker program")
}`

		params := worker.PromptParams{
			BeadID:             "bead-with-program",
			Title:              "With worker program",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Some context",
			WorkerProgram:      workerProgram,
			WorktreePath:       "/tmp/wt-with-program",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		if !strings.Contains(prompt, "## Worker Program") {
			t.Error("expected ## Worker Program section to be present when WorkerProgram is non-empty")
		}

		if !strings.Contains(prompt, workerProgram) {
			t.Error("expected prompt to contain the WorkerProgram content")
		}
	})

	t.Run("section_ordering", func(t *testing.T) {
		t.Parallel()

		workerProgram := `func example() { return nil }`

		params := worker.PromptParams{
			BeadID:             "bead-order-test",
			Title:              "Order test",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			MemoryContext:      "Some context",
			WorkerProgram:      workerProgram,
			WorktreePath:       "/tmp/wt-order",
			Model:              "opus",
		}

		prompt := worker.AssemblePrompt(params)

		codingRulesIdx := strings.Index(prompt, "## Coding Rules")
		workerProgramIdx := strings.Index(prompt, "## Worker Program")
		tddIdx := strings.Index(prompt, "## TDD")

		if codingRulesIdx == -1 {
			t.Fatal("## Coding Rules section not found in prompt")
		}
		if workerProgramIdx == -1 {
			t.Fatal("## Worker Program section not found in prompt")
		}
		if tddIdx == -1 {
			t.Fatal("## TDD section not found in prompt")
		}

		if workerProgramIdx <= codingRulesIdx {
			t.Errorf("expected ## Worker Program (at %d) to appear after ## Coding Rules (at %d)", workerProgramIdx, codingRulesIdx)
		}

		if workerProgramIdx >= tddIdx {
			t.Errorf("expected ## Worker Program (at %d) to appear before ## TDD (at %d)", workerProgramIdx, tddIdx)
		}
	})
}

// TestAssemblePrompt_GstackPatterns verifies that the Constraints section
// contains the gstack anti-rationalization table, DO/NEVER lists, 3-strike
// one-liner, and Iron Law one-liner.
func TestAssemblePrompt_GstackPatterns(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-gstack",
		Title:              "Gstack patterns test",
		Description:        "Verify gstack patterns in Constraints section",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-gstack",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)
	constraintsSection := extractSection(t, prompt, "## Constraints")

	// Anti-rationalization table entries
	antiRationalizationChecks := []struct {
		name   string
		substr string
	}{
		{"excuse: Issue is simple", "Issue is simple"},
		{"rebuttal: Simple issues have root causes too", "Simple issues have root causes too"},
		{"excuse: Emergency no time", "Emergency no time"},
		{"rebuttal: Systematic is FASTER than thrashing", "Systematic is FASTER than thrashing"},
		{"excuse: Just try this first", "Just try this first"},
		{"rebuttal: First fix sets the pattern", "First fix sets the pattern"},
	}
	for _, c := range antiRationalizationChecks {
		if !strings.Contains(constraintsSection, c.substr) {
			t.Errorf("anti-rationalization table: %s: expected Constraints section to contain %q", c.name, c.substr)
		}
	}

	// DO list: stop for these
	doChecks := []struct {
		name   string
		substr string
	}{
		{"DO stop for test failures", "test failures"},
		{"DO stop for 3 failed debugging hypotheses", "3 failed debugging hypotheses"},
		{"DO stop for security concerns", "security concerns"},
	}
	for _, c := range doChecks {
		if !strings.Contains(constraintsSection, c.substr) {
			t.Errorf("DO list: %s: expected Constraints section to contain %q", c.name, c.substr)
		}
	}

	// NEVER list: do not stop for these
	neverChecks := []struct {
		name   string
		substr string
	}{
		{"NEVER stop for style preferences", "style preferences"},
		{"NEVER stop for naming choices", "naming choices"},
		{"NEVER stop for trivial confirmations", "trivial confirmations"},
	}
	for _, c := range neverChecks {
		if !strings.Contains(constraintsSection, c.substr) {
			t.Errorf("NEVER list: %s: expected Constraints section to contain %q", c.name, c.substr)
		}
	}

	// 3-strike one-liner (debugging hypotheses — distinct from QG retry "3 failed test attempts" in Failure section)
	if !strings.Contains(constraintsSection, "3 failed debugging hypotheses") {
		t.Error("expected Constraints section to contain 3-strike one-liner about debugging hypotheses")
	}
	if !strings.Contains(constraintsSection, "re-read the error from scratch") {
		t.Error("expected Constraints section to contain 3-strike instruction to re-read error from scratch")
	}

	// Iron Law one-liner
	if !strings.Contains(constraintsSection, "No fixes without root cause") {
		t.Error("expected Constraints section to contain Iron Law: 'No fixes without root cause'")
	}
	if !strings.Contains(constraintsSection, "diagnose before changing code") {
		t.Error("expected Constraints section to contain Iron Law: 'diagnose before changing code'")
	}
}

// TestPrompt_TargetBranchConstraint verifies that the Constraints section says
// "Do not modify the <target> branch" using the actual TargetBranch value, and
// defaults to "main" when TargetBranch is empty.
func TestPrompt_TargetBranchConstraint(t *testing.T) {
	t.Parallel()

	t.Run("custom_target_branch_interpolated", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "oro-tb-1",
			Title:              "Target branch test",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			WorktreePath:       "/tmp/wt-tb",
			Model:              "opus",
			TargetBranch:       "epic/oro-3ya3",
		}

		prompt := worker.AssemblePrompt(params)

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

		if !strings.Contains(constraintsSection, "Do not modify the epic/oro-3ya3 branch") {
			t.Errorf("expected Constraints to say 'Do not modify the epic/oro-3ya3 branch', got:\n%s", constraintsSection)
		}
		if strings.Contains(constraintsSection, "Do not modify the main branch") {
			t.Errorf("expected Constraints NOT to say 'Do not modify the main branch' when TargetBranch is 'epic/oro-3ya3', got:\n%s", constraintsSection)
		}
	})

	t.Run("empty_target_branch_defaults_to_main", func(t *testing.T) {
		t.Parallel()

		params := worker.PromptParams{
			BeadID:             "oro-tb-2",
			Title:              "Default branch test",
			Description:        "Test description",
			AcceptanceCriteria: "Tests pass",
			WorktreePath:       "/tmp/wt-tb-default",
			Model:              "opus",
			TargetBranch:       "",
		}

		prompt := worker.AssemblePrompt(params)

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

		if !strings.Contains(constraintsSection, "Do not modify the main branch") {
			t.Errorf("expected Constraints to say 'Do not modify the main branch' when TargetBranch is empty, got:\n%s", constraintsSection)
		}
	})
}

// TestPromptStalenessWarning verifies that AssemblePrompt appends a staleness
// verification warning to the Memory section when MemoryContext contains a
// stale marker (⚠), and that no warning is added otherwise.
// TestPromptTaskTerminology verifies that the worker and epic decomposition prompts
// use "oro task" as the primary command for create/show/dep/update operations.
// Workers must NOT be instructed to close the assigned task — the dispatcher handles closure.
func TestPromptTaskTerminology(t *testing.T) {
	t.Parallel()

	prompt := worker.AssemblePrompt(worker.PromptParams{
		BeadID:             "oro-term-test",
		Title:              "Task terminology test",
		Description:        "Verify prompt uses oro task commands",
		AcceptanceCriteria: "Tests pass",
		WorktreePath:       "/tmp/wt-term-test",
		Model:              "opus",
	})
	epicPrompt := worker.BuildEpicDecompositionPrompt(worker.EpicPromptParams{
		BeadID:      "oro-epic-term",
		Title:       "Epic term test",
		Description: "Verify epic prompt uses oro task commands",
	})
	combined := prompt + "\n" + epicPrompt

	for _, cmd := range []string{"oro task create", "oro task show", "oro task dep add"} {
		if !strings.Contains(combined, cmd) {
			t.Errorf("prompts must contain %q as the primary task command", cmd)
		}
	}

	// Self-close guardrail: Exit section must NOT instruct the worker to close the assigned task.
	exitStart := strings.Index(prompt, "## Exit")
	if exitStart == -1 {
		t.Fatal("expected prompt to contain ## Exit section")
	}
	exitSection := prompt[exitStart:]
	if strings.Contains(exitSection, "oro task close") || strings.Contains(exitSection, "oro bead close") {
		t.Error("Exit section must NOT instruct worker to close the assigned task — dispatcher handles closure")
	}
	if !strings.Contains(strings.ToLower(exitSection), "dispatcher") {
		t.Error("Exit section must mention dispatcher to confirm worker does not close the task")
	}
}

// TestAssemblePromptIncludesEditSurface verifies that AssemblePrompt emits the
// edit:* surface in the worker tool prompt section so workers know how to
// invoke oro edit subcommands from Bash.
func TestAssemblePromptIncludesEditSurface(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "bead-edit-surface",
		Title:              "Edit surface test",
		Description:        "Verify edit:* surface in prompt",
		AcceptanceCriteria: "All 12 edit ops listed",
		WorktreePath:       "/tmp/wt-edit-surface",
		Model:              "opus",
	}

	prompt := worker.AssemblePrompt(params)

	// All 12 edit:* subcommands must appear in the prompt.
	editOps := []string{
		"edit:replace",
		"edit:after",
		"edit:delete",
		"edit:rename",
		"edit:rename-all",
		"edit:move",
		"edit:move-to-file",
		"edit:read",
		"edit:diff",
		"edit:undo",
		"edit:batch",
		"edit:check",
	}
	for _, op := range editOps {
		if !strings.Contains(prompt, op) {
			t.Errorf("expected prompt to contain edit op %q in tool surface", op)
		}
	}

	// The edit surface must appear in the Task Tools section or a dedicated Edit Tools section.
	toolsIdx := strings.Index(prompt, "## Task Tools")
	editToolsIdx := strings.Index(prompt, "## Edit Tools")
	if toolsIdx == -1 && editToolsIdx == -1 {
		t.Fatal("expected prompt to contain '## Task Tools' or '## Edit Tools' section")
	}
}

func TestPromptStalenessWarning(t *testing.T) {
	t.Parallel()

	const stalenessWarning = "verify by reading the actual source"

	t.Run("stale_marker_present", func(t *testing.T) {
		t.Parallel()
		// After D.4 cutover, MemoryContext is no longer rendered — staleness
		// warnings from the old memory system must NOT appear in the prompt.
		memCtx := "| 42 | gotcha | some stale memory | 10d ⚠ | ~15 |"
		params := worker.PromptParams{
			BeadID:        "bead-stale",
			Title:         "Stale memory test",
			MemoryContext: memCtx,
			WorktreePath:  "/tmp/wt-stale",
			Model:         "opus",
		}
		prompt := worker.AssemblePrompt(params)
		if strings.Contains(prompt, stalenessWarning) {
			t.Errorf("prompt must NOT contain staleness warning after D.4 cutover (MemoryContext is no longer rendered)")
		}
	})

	t.Run("no_stale_marker", func(t *testing.T) {
		t.Parallel()
		// MemoryContext with no stale marker — staleness warning must still be absent.
		memCtx := "| 10 | lesson | always run go vet | 2d | ~12 |"
		params := worker.PromptParams{
			BeadID:        "bead-fresh",
			Title:         "Fresh memory test",
			MemoryContext: memCtx,
			WorktreePath:  "/tmp/wt-fresh",
			Model:         "opus",
		}
		prompt := worker.AssemblePrompt(params)
		if strings.Contains(prompt, stalenessWarning) {
			t.Errorf("prompt must NOT contain staleness warning when MemoryContext has no ⚠ marker")
		}
	})

	t.Run("empty_memory_context", func(t *testing.T) {
		t.Parallel()
		params := worker.PromptParams{
			BeadID:       "bead-empty-mem",
			Title:        "Empty memory test",
			WorktreePath: "/tmp/wt-empty-mem",
			Model:        "opus",
		}
		prompt := worker.AssemblePrompt(params)
		// Cards section shows empty-cards placeholder instead of "No prior context".
		if !strings.Contains(prompt, "No relevant cards for this task") {
			t.Error("expected 'No relevant cards' placeholder when Cards is empty")
		}
		if strings.Contains(prompt, stalenessWarning) {
			t.Errorf("prompt must NOT contain staleness warning when MemoryContext is empty")
		}
	})
}

func TestWorkerPromptTaskCreateIncludesTier(t *testing.T) {
	t.Parallel()

	t.Run("includes tier when parent has tier", func(t *testing.T) {
		t.Parallel()
		params := worker.EpicPromptParams{
			BeadID:      "oro-epic-tier",
			Title:       "Epic with tier",
			Description: "An epic running at balanced tier",
			ParentTier:  "balanced",
		}
		prompt := worker.BuildEpicDecompositionPrompt(params)
		if !strings.Contains(prompt, "--tier=balanced") {
			t.Errorf("epic decomposition prompt must include --tier=balanced when parent tier is set; got:\n%s", prompt)
		}
	})

	t.Run("omits tier when parent has no tier", func(t *testing.T) {
		t.Parallel()
		params := worker.EpicPromptParams{
			BeadID:      "oro-epic-no-tier",
			Title:       "Epic without tier",
			Description: "An epic with no tier set",
		}
		prompt := worker.BuildEpicDecompositionPrompt(params)
		if strings.Contains(prompt, "--tier=") {
			t.Errorf("epic decomposition prompt must not include --tier= when parent tier is empty; got:\n%s", prompt)
		}
	})
}
