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

	// Exit section must explain dispatcher handles merge (oro-u74j fix)
	// Worker should NOT be instructed to merge or close themselves
	requiredPhrases := map[string][]string{
		"dispatcher responsibility": {"dispatcher"},
		"merge mention":             {"merge", "main"},
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
			t.Errorf("Exit section must contain %s (one of %v). Got:\n%s", category, phrases, exitSection)
		}
	}

	// Worker should NOT be told to run merge or close commands themselves
	prohibitedPhrases := []string{"git merge", "bd close", "checkout main"}
	for _, phrase := range prohibitedPhrases {
		if strings.Contains(strings.ToLower(exitSection), phrase) {
			t.Errorf("Exit section must NOT instruct worker to run '%s' (dispatcher handles this). Got:\n%s", phrase, exitSection)
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

	// Exit section should explain dispatcher handles merge failures (oro-u74j)
	if !strings.Contains(strings.ToLower(exitSection), "dispatcher") {
		t.Errorf("Exit section should mention dispatcher handles merge process. Got:\n%s", exitSection)
	}

	// Worker should NOT be instructed to handle merge failures themselves
	if strings.Contains(strings.ToLower(exitSection), "bd close") {
		t.Errorf("Exit section must NOT instruct worker to close bead (dispatcher handles this). Got:\n%s", exitSection)
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

	// Exit section should list steps dispatcher will take (oro-u74j)
	// Look for numbered steps describing dispatcher's actions
	hasSteps := strings.Contains(exitSection, "1.") || strings.Contains(exitSection, "2.")

	if !hasSteps {
		t.Error("Exit section should describe dispatcher's step-by-step process")
	}

	// Verify dispatcher and merge are mentioned
	lowerExit := strings.ToLower(exitSection)
	if !strings.Contains(lowerExit, "dispatcher") {
		t.Error("Exit section must mention 'dispatcher' handles merge")
	}
	if !strings.Contains(lowerExit, "merge") {
		t.Error("Exit section must mention 'merge' process")
	}

	// Worker should NOT be told to close bead themselves
	if strings.Contains(lowerExit, "bd close") {
		t.Error("Exit section must NOT tell worker to run 'bd close' (dispatcher handles this)")
	}
}
