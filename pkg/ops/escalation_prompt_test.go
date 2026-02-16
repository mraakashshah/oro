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
