package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/worker"
)

// TestAssemblePrompt_ExitRequiresMergeToMain verifies that the Exit section
// explicitly states that the bead is not done until the commit is on main.
func TestAssemblePrompt_ExitRequiresMergeToMain(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-test",
		Title:              "Test merge requirement",
		Description:        "Verify Exit section requires merge to main",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-test",
		Model:              "claude-opus-4-6",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Exit section
	exitStart := strings.Index(prompt, "## Exit")
	if exitStart == -1 {
		t.Fatal("expected prompt to contain ## Exit section")
	}

	// Find the next section header after Exit (or end of prompt)
	nextSectionStart := strings.Index(prompt[exitStart+len("## Exit"):], "## ")
	var exitSection string
	if nextSectionStart == -1 {
		exitSection = prompt[exitStart:]
	} else {
		exitSection = prompt[exitStart : exitStart+len("## Exit")+nextSectionStart]
	}

	// Exit section must explicitly state merge-to-main is required
	requiredPhrases := map[string][]string{
		"main branch requirement": {"main branch", "on main", "to main"},
		"merge instruction":       {"merge", "git merge"},
		"close command":           {"bd close", "close"},
	}

	for category, phrases := range requiredPhrases {
		found := false
		for _, phrase := range phrases {
			if strings.Contains(strings.ToLower(exitSection), strings.ToLower(phrase)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Exit section must contain %s (one of %v) to enforce merge-before-close. Got:\n%s", category, phrases, exitSection)
		}
	}
}

// TestAssemblePrompt_ExitMergeBlockerHandling verifies that the Exit section
// instructs workers to report blockers instead of closing when merge fails.
func TestAssemblePrompt_ExitMergeBlockerHandling(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-blocker",
		Title:              "Test blocker handling",
		Description:        "Verify Exit section handles merge failures",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-blocker",
		Model:              "claude-opus-4-6",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Exit section
	exitStart := strings.Index(prompt, "## Exit")
	if exitStart == -1 {
		t.Fatal("expected prompt to contain ## Exit section")
	}

	nextSectionStart := strings.Index(prompt[exitStart+len("## Exit"):], "## ")
	var exitSection string
	if nextSectionStart == -1 {
		exitSection = prompt[exitStart:]
	} else {
		exitSection = prompt[exitStart : exitStart+len("## Exit")+nextSectionStart]
	}

	// Exit section should mention handling merge failures
	blockingPhrases := []string{
		"merge fails",
		"test failures",
		"report",
	}

	foundCount := 0
	for _, phrase := range blockingPhrases {
		if strings.Contains(strings.ToLower(exitSection), strings.ToLower(phrase)) {
			foundCount++
		}
	}

	// At least 2 of the 3 blocking-related phrases should be present
	if foundCount < 2 {
		t.Errorf("Exit section should mention merge failure handling (found %d/3 phrases). Got:\n%s", foundCount, exitSection)
	}
}

// TestAssemblePrompt_ExitStepByStepLifecycle verifies that the Exit section
// lists merge-to-main as an explicit step before bd close.
func TestAssemblePrompt_ExitStepByStepLifecycle(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-steps",
		Title:              "Test lifecycle steps",
		Description:        "Verify Exit section has ordered steps",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-steps",
		Model:              "claude-opus-4-6",
	}

	prompt := worker.AssemblePrompt(params)

	// Extract Exit section
	exitStart := strings.Index(prompt, "## Exit")
	if exitStart == -1 {
		t.Fatal("expected prompt to contain ## Exit section")
	}

	nextSectionStart := strings.Index(prompt[exitStart+len("## Exit"):], "## ")
	var exitSection string
	if nextSectionStart == -1 {
		exitSection = prompt[exitStart:]
	} else {
		exitSection = prompt[exitStart : exitStart+len("## Exit")+nextSectionStart]
	}

	// Exit section should list steps in order
	// Look for numbered steps (1., 2., 3.) or bullet points with sequential actions
	hasSteps := strings.Contains(exitSection, "1.") || strings.Contains(exitSection, "- ")

	if !hasSteps {
		t.Error("Exit section should contain a step-by-step procedure (numbered or bulleted)")
	}

	// Verify merge appears BEFORE bd close in the text
	lowerExit := strings.ToLower(exitSection)
	mergeIdx := strings.Index(lowerExit, "merge")
	closeIdx := strings.Index(lowerExit, "bd close")

	if mergeIdx == -1 {
		t.Error("Exit section must contain 'merge' instruction")
	}
	if closeIdx == -1 {
		t.Error("Exit section must contain 'bd close' instruction")
	}
	if mergeIdx > closeIdx {
		t.Error("Exit section must list merge BEFORE bd close (merge must happen first)")
	}
}
