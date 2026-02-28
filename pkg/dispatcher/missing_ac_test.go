package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestCheckBeadReady_MissingAC_Escalates verifies that checkBeadReady fires a
// MISSING_AC escalation and returns (title, "", false) when a bead has no AC.
// The 60-second cooldown (worktreeFailures) must also be set to prevent loops.
func TestCheckBeadReady_MissingAC_Escalates(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-noac1"

	// Seed bead with empty acceptance criteria.
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Bead Without AC",
		AcceptanceCriteria: "", // no AC
	}

	bead := protocol.Bead{ID: beadID, Title: "Bead Without AC"}
	title, acceptance, ok := d.checkBeadReady(ctx, bead, "w1")

	// Should return false — bead not ready for assignment.
	if ok {
		t.Error("checkBeadReady returned ok=true for bead with no AC, want false")
	}
	if acceptance != "" {
		t.Errorf("checkBeadReady returned acceptance=%q, want empty", acceptance)
	}
	if title == "" {
		t.Error("checkBeadReady returned empty title, want non-empty")
	}

	// worktreeFailures must be set to enforce the 60-second cooldown.
	d.mu.Lock()
	_, cooldownSet := d.worktreeFailures[beadID]
	d.mu.Unlock()
	if !cooldownSet {
		t.Error("worktreeFailures[beadID] not set after missing-AC escalation, want cooldown entry")
	}

	// A MISSING_AC escalation must have been dispatched.
	msgs := esc.Messages()
	if len(msgs) == 0 {
		t.Fatal("no escalation messages sent, want MISSING_AC escalation")
	}
	found := false
	for _, m := range msgs {
		if strings.Contains(m, string(protocol.EscMissingAC)) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("escalation messages %v do not contain MISSING_AC", msgs)
	}
}

// TestSpawnOneShot_MissingAC_UsesWriteAC verifies that spawnEscalationOneShot
// routes MISSING_AC to ops.WriteAC (model=opus, AC-writing prompt) instead of
// ops.Escalate (model=sonnet, manager prompt).
func TestSpawnOneShot_MissingAC_UsesWriteAC(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-noac2"

	// Seed bead details so spawnEscalationOneShot can look them up.
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:          beadID,
		Title:       "Needs AC",
		Description: "A task that is missing acceptance criteria",
	}

	msg := protocol.FormatEscalation(protocol.EscMissingAC, beadID, "no acceptance criteria — spawning AC writer", "")
	d.spawnEscalationOneShot(ctx, 0, string(protocol.EscMissingAC), beadID, "w1", msg)

	// Wait for the async spawn to happen (ops.Spawner.run launches a goroutine).
	waitFor(t, func() bool {
		return spawnMock.SpawnCount() > 0
	}, 2*time.Second)

	spawnMock.mu.Lock()
	spawns := make([]spawnCall, len(spawnMock.spawns))
	copy(spawns, spawnMock.spawns)
	spawnMock.mu.Unlock()

	if len(spawns) == 0 {
		t.Fatal("no spawn calls recorded, want WriteAC to be spawned")
	}
	last := spawns[len(spawns)-1]

	// WriteAC uses model "opus"; ops.Escalate uses "sonnet".
	if last.model != "opus" {
		t.Errorf("spawn model = %q, want %q (WriteAC uses opus)", last.model, "opus")
	}

	// The prompt must be the AC-writing prompt, not the escalation manager prompt.
	// buildWriteACPrompt starts with "You are a one-shot Opus agent. Your sole job is to write precise, testable acceptance criteria".
	if !strings.Contains(last.prompt, "acceptance criteria") {
		t.Errorf("spawn prompt does not mention 'acceptance criteria'; got prefix: %q", last.prompt[:min(120, len(last.prompt))])
	}
	// Must NOT be the generic escalation prompt.
	if strings.Contains(last.prompt, "You are the oro ops manager") {
		t.Errorf("spawn prompt appears to be an escalation manager prompt, want WriteAC prompt")
	}
}
