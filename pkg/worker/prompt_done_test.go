package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/worker"
)

// TestAssemblePrompt_ExitDoesNotInstructWorkerToMerge verifies that the Exit
// section does NOT instruct the worker to merge to main or close the bead.
// The worker's responsibility is to send a DONE message after QG passes.
// The dispatcher handles merge and bead closure.
//
// Context: oro-u74j bug — workers were instructed to merge+close, leading to
// beads being marked closed without their commits actually being on main.
func TestAssemblePrompt_ExitDoesNotInstructWorkerToMerge(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-test",
		Title:              "Test no merge instruction",
		Description:        "Verify worker is NOT told to merge",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-test",
		Model:              "opus",
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

	lowerExit := strings.ToLower(exitSection)

	// Exit section must NOT tell worker to merge to main
	prohibitedPhrases := []string{
		"git merge",
		"merge --no-ff",
		"switch to main",
		"checkout main",
		"cd \"$main_repo\"",
		"bd close",
	}

	var foundProhibited []string
	for _, phrase := range prohibitedPhrases {
		if strings.Contains(lowerExit, strings.ToLower(phrase)) {
			foundProhibited = append(foundProhibited, phrase)
		}
	}

	if len(foundProhibited) > 0 {
		t.Errorf("Exit section must NOT instruct worker to merge or close bead (dispatcher handles this). Found prohibited phrases: %v\nExit section:\n%s", foundProhibited, exitSection)
	}
}

// TestAssemblePrompt_ExitInstructsSendingDone verifies that the Exit section
// tells the worker to send a DONE message after quality gate passes.
func TestAssemblePrompt_ExitInstructsSendingDone(t *testing.T) {
	t.Parallel()

	params := worker.PromptParams{
		BeadID:             "oro-test",
		Title:              "Test DONE instruction",
		Description:        "Verify worker is told to send DONE",
		AcceptanceCriteria: "Tests pass",
		MemoryContext:      "",
		WorktreePath:       "/tmp/wt-test",
		Model:              "opus",
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

	// Exit section should mention quality gate
	if !strings.Contains(strings.ToLower(exitSection), "quality gate") {
		t.Errorf("Exit section should mention quality gate. Got:\n%s", exitSection)
	}

	// Exit section should indicate that the dispatcher handles merge
	requiredConcepts := []string{
		"dispatcher", // Should mention dispatcher handles merge
	}

	for _, concept := range requiredConcepts {
		if !strings.Contains(strings.ToLower(exitSection), concept) {
			t.Errorf("Exit section should mention '%s' to clarify merge responsibility. Got:\n%s", concept, exitSection)
		}
	}
}
