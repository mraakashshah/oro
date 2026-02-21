package ops //nolint:testpackage // internal test needs access to unexported buildEscalationPrompt

import (
	"strings"
	"testing"
)

func TestBuildEscalationPrompt_ContainsEscalationType(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-abc",
		BeadTitle:      "Implement feature X",
		BeadContext:    "Worker has been stuck for 15 minutes",
		RecentHistory:  "heartbeat timeout at 14:30",
	})
	if !strings.Contains(prompt, "STUCK_WORKER") {
		t.Fatalf("prompt missing escalation type, got:\n%s", prompt)
	}
}

func TestBuildEscalationPrompt_ContainsBeadContext(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "MERGE_CONFLICT",
		BeadID:         "oro-xyz",
		BeadTitle:      "Fix authentication",
		BeadContext:    "Conflict in auth.go between feature branches",
		RecentHistory:  "merge attempted at 15:00, failed",
	})
	if !strings.Contains(prompt, "oro-xyz") {
		t.Fatalf("prompt missing bead ID, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Fix authentication") {
		t.Fatalf("prompt missing bead title, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Conflict in auth.go between feature branches") {
		t.Fatalf("prompt missing bead context, got:\n%s", prompt)
	}
}

func TestBuildEscalationPrompt_ContainsRecentHistory(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "PRIORITY_CONTENTION",
		BeadID:         "oro-p0",
		BeadContext:    "P0 bead queued",
		RecentHistory:  "3 escalations in last hour: stuck at 13:00, crash at 13:30, stuck at 14:00",
	})
	if !strings.Contains(prompt, "3 escalations in last hour") {
		t.Fatalf("prompt missing recent history, got:\n%s", prompt)
	}
}

func TestBuildEscalationPrompt_ContainsCLICommands(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "MISSING_AC",
		BeadID:         "oro-noac",
	})
	if !strings.Contains(prompt, "bd") {
		t.Fatalf("prompt missing bd CLI reference, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "oro directive") {
		t.Fatalf("prompt missing oro directive reference, got:\n%s", prompt)
	}
}

func TestBuildEscalationPrompt_HasPlaybookForEachType(t *testing.T) {
	types := []string{
		"STUCK_WORKER",
		"MERGE_CONFLICT",
		"PRIORITY_CONTENTION",
		"MISSING_AC",
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			prompt := buildEscalationPrompt(EscalationOpts{
				EscalationType: typ,
				BeadID:         "oro-test",
			})
			// Each type should have specific guidance in the prompt
			if len(prompt) < 100 {
				t.Fatalf("prompt too short for %s, likely missing playbook: %s", typ, prompt)
			}
		})
	}
}

func TestBuildEscalationPrompt_EmptyOptionalFields(t *testing.T) {
	// Should not panic with minimal fields
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-min",
	})
	if !strings.Contains(prompt, "STUCK_WORKER") {
		t.Fatalf("prompt missing escalation type with minimal opts, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "oro-min") {
		t.Fatalf("prompt missing bead ID with minimal opts, got:\n%s", prompt)
	}
}

// --- writeEscalationHeader content tests ---

func TestWriteEscalationHeader_OneShotManagerAgent(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-hdr",
	})
	if !strings.Contains(prompt, "one-shot manager agent") {
		t.Fatalf("header missing 'one-shot manager agent', got:\n%s", prompt)
	}
}

func TestWriteEscalationHeader_EscalationTypeInHeader(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "MERGE_CONFLICT",
		BeadID:         "oro-hdr2",
	})
	// The header should explicitly label the escalation type
	if !strings.Contains(prompt, "Escalation type: MERGE_CONFLICT") {
		t.Fatalf("header missing 'Escalation type: MERGE_CONFLICT', got:\n%s", prompt)
	}
}

func TestWriteEscalationHeader_TaskOutputProhibition(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "MISSING_AC",
		BeadID:         "oro-hdr3",
	})
	if !strings.Contains(prompt, "TaskOutput") {
		t.Fatalf("header missing TaskOutput prohibition, got:\n%s", prompt)
	}
}

// --- writePlaybook section content tests ---

func TestWritePlaybook_StuckWorker_StepsContent(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-sw",
	})
	if !strings.Contains(prompt, "Check the worker") {
		t.Fatalf("STUCK_WORKER playbook missing 'Check the worker', got:\n%s", prompt)
	}
}

func TestWritePlaybook_MergeConflict_GoTestStep(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "MERGE_CONFLICT",
		BeadID:         "oro-mc",
	})
	if !strings.Contains(prompt, "go test ./...") {
		t.Fatalf("MERGE_CONFLICT playbook missing 'go test ./...', got:\n%s", prompt)
	}
}

func TestWritePlaybook_PriorityContention_Steps(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "PRIORITY_CONTENTION",
		BeadID:         "oro-pc",
	})
	if !strings.Contains(prompt, "bd list --status=in_progress") {
		t.Fatalf("PRIORITY_CONTENTION playbook missing 'bd list --status=in_progress', got:\n%s", prompt)
	}
}

func TestWritePlaybook_MissingAC_Steps(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "MISSING_AC",
		BeadID:         "oro-mac",
	})
	if !strings.Contains(prompt, "bd show") {
		t.Fatalf("MISSING_AC playbook missing 'bd show', got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "--acceptance") {
		t.Fatalf("MISSING_AC playbook missing '--acceptance' flag, got:\n%s", prompt)
	}
}

func TestWritePlaybook_DefaultType_UnknownEscalationText(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "TOTALLY_UNKNOWN_TYPE",
		BeadID:         "oro-def",
	})
	if !strings.Contains(prompt, "Unknown escalation type: TOTALLY_UNKNOWN_TYPE") {
		t.Fatalf("default case missing 'Unknown escalation type: TOTALLY_UNKNOWN_TYPE', got:\n%s", prompt)
	}
}

// --- writeAvailableCLI section content tests ---

func TestWriteAvailableCLI_BdShow(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-cli",
	})
	if !strings.Contains(prompt, "bd show <id>") {
		t.Fatalf("CLI section missing 'bd show <id>', got:\n%s", prompt)
	}
}

func TestWriteAvailableCLI_OroDirective(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-cli2",
	})
	if !strings.Contains(prompt, "oro directive restart-worker") {
		t.Fatalf("CLI section missing 'oro directive restart-worker', got:\n%s", prompt)
	}
}

// --- writeEscalationOutput section content tests ---

func TestWriteEscalationOutput_ACKFormat(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-out",
	})
	if !strings.Contains(prompt, "ACK: <brief description of action taken>") {
		t.Fatalf("output section missing ACK format, got:\n%s", prompt)
	}
}

func TestWriteEscalationOutput_ESCALATEFormat(t *testing.T) {
	prompt := buildEscalationPrompt(EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-out2",
	})
	if !strings.Contains(prompt, "ESCALATE: <reason this needs human attention>") {
		t.Fatalf("output section missing ESCALATE format, got:\n%s", prompt)
	}
}

// --- parseEscalationOutput tests ---

func TestParseEscalationOutput_ACK_ReturnsVerdictResolved(t *testing.T) {
	verdict, feedback := parseEscalationOutput("ACK: restarted the stalled worker")
	if verdict != VerdictResolved {
		t.Fatalf("expected VerdictResolved for ACK output, got %q", verdict)
	}
	if !strings.Contains(feedback, "restarted the stalled worker") {
		t.Fatalf("expected feedback to contain action, got %q", feedback)
	}
}

func TestParseEscalationOutput_ESCALATE_ReturnsVerdictFailed(t *testing.T) {
	verdict, feedback := parseEscalationOutput("ESCALATE: semantic conflict requires human judgment")
	if verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed for ESCALATE output, got %q", verdict)
	}
	if !strings.Contains(feedback, "semantic conflict requires human judgment") {
		t.Fatalf("expected feedback to contain reason, got %q", feedback)
	}
}

func TestParseEscalationOutput_NoVerdict_ReturnsVerdictFailedWithFullOutput(t *testing.T) {
	fullOutput := "I looked at the bead but couldn't figure out what to do next."
	verdict, feedback := parseEscalationOutput(fullOutput)
	if verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed when no verdict keyword present, got %q", verdict)
	}
	if feedback != fullOutput {
		t.Fatalf("expected full output as feedback, got %q", feedback)
	}
}

func TestParseEscalationOutput_ACK_CaseInsensitive(t *testing.T) {
	verdict, _ := parseEscalationOutput("ack: did the thing")
	if verdict != VerdictResolved {
		t.Fatalf("expected VerdictResolved for lowercase 'ack:', got %q", verdict)
	}
}

func TestParseEscalationOutput_ESCALATE_CaseInsensitive(t *testing.T) {
	verdict, _ := parseEscalationOutput("escalate: need human help")
	if verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed for lowercase 'escalate:', got %q", verdict)
	}
}
