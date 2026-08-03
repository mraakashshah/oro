package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
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
// routes MISSING_AC to ops.WriteAC (deep Sol, AC-writing prompt) instead of
// the generic ops escalation prompt.
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
	d.spawnEscalationOneShot(ctx, 0, 0, string(protocol.EscMissingAC), beadID, "w1", msg)

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

	if last.model != "gpt-5.6-sol" {
		t.Errorf("spawn model = %q, want Sol", last.model)
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

// TestMalformedReadySkip verifies that checkBeadReady emits a bead_skipped_missing_ac
// event (in the events table) with the bead_id when a bead has no acceptance criteria.
// This is a regression guard: the scheduler must emit a structured skip event so
// operators and tests can detect quarantined beads without parsing escalation messages.
func TestMalformedReadySkip(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-malformed1"

	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Malformed Bead",
		AcceptanceCriteria: "", // intentionally missing
	}

	bead := protocol.Bead{ID: beadID, Title: "Malformed Bead"}
	_, _, ok := d.checkBeadReady(ctx, bead, "w1")

	if ok {
		t.Fatal("checkBeadReady returned ok=true for bead with no AC, want false")
	}

	// A bead_skipped_missing_ac event must be logged with the bead ID.
	var count int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE type='bead_skipped_missing_ac' AND bead_id=?`,
		beadID,
	).Scan(&count); err != nil {
		t.Fatalf("query bead_skipped_missing_ac event: %v", err)
	}
	if count == 0 {
		t.Errorf("no bead_skipped_missing_ac event logged for bead %q, want 1", beadID)
	}

	// Verify the payload contains reason=missing_acceptance.
	var payload string
	if err := d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='bead_skipped_missing_ac' AND bead_id=? LIMIT 1`,
		beadID,
	).Scan(&payload); err != nil {
		t.Fatalf("query bead_skipped_missing_ac payload: %v", err)
	}
	if !strings.Contains(payload, "missing_acceptance") {
		t.Errorf("bead_skipped_missing_ac payload %q does not contain 'missing_acceptance'", payload)
	}
}

// TestMissingAcceptanceDoesNotBlock verifies that when the ready list contains a
// bead with missing AC followed by a valid bead, the scheduler skips the malformed
// bead and still assigns the valid bead to the idle worker.
// This is the core regression: a malformed ready bead must not block the queue.
func TestMissingAcceptanceDoesNotBlock(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		malformedID = "oro-noac-blk1"
		validID     = "oro-valid-blk1"
	)

	// Seed beads: malformed has no AC; valid has AC (mock default returns AC for unknown IDs).
	beadSrc.shown[malformedID] = &protocol.BeadDetail{
		ID:                 malformedID,
		Title:              "Malformed — no AC",
		AcceptanceCriteria: "",
	}
	// valid bead: don't seed shown — mock returns default AC "Test: auto | Assert: PASS".

	// Ready list: malformed first, then valid.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: malformedID, Title: "Malformed — no AC", Status: "open"},
		{ID: validID, Title: "Valid bead", Status: "open"},
	})

	// Set state to running so tryAssign proceeds (default is StateInert).
	d.mu.Lock()
	d.state = StateRunning
	d.mu.Unlock()

	// Add one idle worker directly so tryAssign can assign.
	// Drain client in a goroutine so sendToWorker doesn't block on the synchronous pipe.
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()
	d.mu.Lock()
	d.workers["w-idle-blk"] = &trackedWorker{
		id:      "w-idle-blk",
		conn:    server,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(server),
	}
	d.mu.Unlock()

	d.tryAssign(ctx)

	// Malformed bead must NOT have been set to in_progress.
	beadSrc.mu.Lock()
	malformedStatus := beadSrc.updated[malformedID]
	validStatus := beadSrc.updated[validID]
	beadSrc.mu.Unlock()

	if malformedStatus == "in_progress" {
		t.Errorf("malformed bead %q was assigned (status=in_progress), want skipped", malformedID)
	}

	// Valid bead must have been assigned (set to in_progress).
	if validStatus != "in_progress" {
		t.Errorf("valid bead %q status = %q, want in_progress", validID, validStatus)
	}

	// A bead_skipped_missing_ac event must exist for the malformed bead.
	var skipCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE type='bead_skipped_missing_ac' AND bead_id=?`,
		malformedID,
	).Scan(&skipCount); err != nil {
		t.Fatalf("query skip event: %v", err)
	}
	if skipCount == 0 {
		t.Errorf("no bead_skipped_missing_ac event for malformed bead %q", malformedID)
	}
}

// TestMissingACOpsFailureAcksAndCooldown verifies that a failed one-shot
// MISSING_AC ops run fails closed: the failure is visible, the bead is kept out
// of immediate reassignment, and the persisted escalation is acked so the retry
// loop does not replay the same broken one-shot forever.
func TestMissingACOpsFailureAcksAndCooldown(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		beadID   = "oro-noac-opserr"
		workerID = "w-noac-opserr"
	)

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO escalations (type, bead_id, worker_id, message) VALUES (?, ?, ?, ?)`,
		protocol.EscMissingAC, beadID, workerID, "missing AC")
	if err != nil {
		t.Fatalf("insert escalation: %v", err)
	}
	escalationID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{
		Type:    ops.OpsWriteAC,
		BeadID:  beadID,
		Verdict: ops.VerdictFailed,
		Err:     errors.New("codex unsupported model"),
	}

	d.handleEscalationResult(ctx, 0, escalationID, string(protocol.EscMissingAC), beadID, workerID, resultCh)

	var failedCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE type='oneshot_escalation_failed' AND bead_id=?`,
		beadID).Scan(&failedCount); err != nil {
		t.Fatalf("query oneshot failure events: %v", err)
	}
	if failedCount != 1 {
		t.Fatalf("oneshot_escalation_failed count = %d for %s, want 1", failedCount, beadID)
	}

	d.mu.Lock()
	_, inCooldown := d.worktreeFailures[beadID]
	d.mu.Unlock()
	if !inCooldown {
		t.Fatalf("MISSING_AC ops failure should put %s in assignment cooldown", beadID)
	}

	var status string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM escalations WHERE id=?`,
		escalationID).Scan(&status); err != nil {
		t.Fatalf("query escalation status: %v", err)
	}
	if status != "acked" {
		t.Fatalf("escalation status = %q, want acked to prevent retry loop", status)
	}
}
