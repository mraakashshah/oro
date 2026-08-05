package dispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

func TestExecutableAfterEpicSideEffectsClassifiesNonEpicAndChildlessEpic(t *testing.T) {
	ctx := context.Background()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	if !d.executableAfterEpicSideEffects(ctx, protocol.Bead{ID: "task", Type: "task"}) {
		t.Fatal("non-epic bead is not executable")
	}
	beads.hasChildrenMap = map[string]bool{"childless": false}
	hookCalls := 0
	d.beforeAssignmentSideEffectAdmission = func() { hookCalls++ }
	if !d.executableAfterEpicSideEffects(ctx, protocol.Bead{ID: "childless", Type: "EpIc"}) {
		t.Fatal("childless epic is not executable")
	}
	if hookCalls != 0 {
		t.Fatalf("side-effect admission hook calls = %d, want 0", hookCalls)
	}
}

func TestExecutableAfterEpicSideEffectsFailsClosedAndAuditsChildLookupError(t *testing.T) {
	ctx := context.Background()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	beads.hasChildrenErr = errors.New("injected child lookup failure")
	if d.executableAfterEpicSideEffects(ctx, protocol.Bead{ID: "epic-error", Type: "epic"}) {
		t.Fatal("epic with unobservable children is executable")
	}
	var payload string
	if err := d.db.QueryRowContext(ctx, `
		SELECT payload FROM events WHERE type='epic_has_children_error' AND bead_id='epic-error'
		ORDER BY id DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("query lookup failure event: %v", err)
	}
	if !strings.Contains(payload, "injected child lookup failure") {
		t.Fatalf("lookup failure payload = %q", payload)
	}
}

func TestExecutableAfterEpicSideEffectsProcessesAndReleasesDecomposedEpic(t *testing.T) {
	ctx := context.Background()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	beads.hasChildrenMap = map[string]bool{"epic-decomposed": true}
	beads.allChildrenClosedMap = map[string]bool{"epic-decomposed": false}
	hookCalls := 0
	d.beforeAssignmentSideEffectAdmission = func() { hookCalls++ }
	if d.executableAfterEpicSideEffects(ctx, protocol.Bead{ID: "epic-decomposed", Type: "epic"}) {
		t.Fatal("decomposed epic is executable")
	}
	if hookCalls != 1 {
		t.Fatalf("side-effect admission hook calls = %d, want 1", hookCalls)
	}
	if got := mutationEventCount(t, d, "non_executable_issue_type", "epic-decomposed"); got != 1 {
		t.Fatalf("non-executable events = %d, want 1", got)
	}
	var admissions int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignment_side_effect_admissions WHERE bead_id='epic-decomposed'`,
	).Scan(&admissions); err != nil {
		t.Fatalf("count admissions: %v", err)
	}
	if admissions != 0 {
		t.Fatalf("admissions after epic processing = %d, want 0", admissions)
	}
}

func TestExecutableAfterEpicSideEffectsDoesNotProcessBlockedOrUnknownAdmission(t *testing.T) {
	ctx := context.Background()
	t.Run("reserved", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		beads.hasChildrenMap = map[string]bool{"epic-reserved": true}
		first, err := d.acquireAssignmentSideEffectAdmission(ctx, "epic-reserved", "worker", "fixture")
		if err != nil || first == nil {
			t.Fatalf("acquire fixture admission = %#v, %v", first, err)
		}
		defer d.releaseAssignmentSideEffectAdmission(ctx, first)
		if d.executableAfterEpicSideEffects(ctx, protocol.Bead{ID: "epic-reserved", Type: "epic"}) {
			t.Fatal("reserved epic is executable")
		}
		if got := mutationEventCount(t, d, "non_executable_issue_type", "epic-reserved"); got != 0 {
			t.Fatalf("epic processing events = %d, want 0 while admission blocked", got)
		}
	})
	t.Run("storage failure", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		beads.hasChildrenMap = map[string]bool{"epic-unknown": true}
		if _, err := d.db.ExecContext(ctx, `DROP TABLE assignment_side_effect_admissions`); err != nil {
			t.Fatalf("drop admission table: %v", err)
		}
		if d.executableAfterEpicSideEffects(ctx, protocol.Bead{ID: "epic-unknown", Type: "epic"}) {
			t.Fatal("epic with unknown admission is executable")
		}
		if got := mutationEventCount(t, d, "non_executable_issue_type", "epic-unknown"); got != 0 {
			t.Fatalf("epic processing events = %d, want 0 on admission failure", got)
		}
	})
}

func TestFilterAssignableAppliesEveryDurableEligibilityStage(t *testing.T) {
	ctx := context.Background()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	beads.hasChildrenMap = map[string]bool{"epic-decomposed-filter": true}
	beads.allChildrenClosedMap = map[string]bool{"epic-decomposed-filter": false}
	if _, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, reviewCheckpointInput("bead-checkpoint-filter")); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `
		INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
		VALUES ('bead-quarantine-filter', 'agent/bead-quarantine-filter', 'mutation-test', 'blocked', 'open')`); err != nil {
		t.Fatalf("create recovery quarantine: %v", err)
	}
	all := []protocol.Bead{
		{ID: "bead-checkpoint-filter", Type: "task", Status: "open"},
		{ID: "epic-decomposed-filter", Type: "epic", Status: "open"},
		{ID: "bead-quarantine-filter", Type: "task", Status: "open"},
		{ID: "bead-eligible-filter", Type: "task", Status: "open"},
	}
	filtered := d.filterAssignable(ctx, all)
	if len(filtered) != 1 || filtered[0].ID != "bead-eligible-filter" {
		t.Fatalf("filtered beads = %+v, want only eligible bead", filtered)
	}
}

func TestFilterExecutableBeadsReturnsOnlyExecutableInputs(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	beads.hasChildrenMap = map[string]bool{"epic-with-child": true}
	got := d.filterExecutableBeads(context.Background(), []protocol.Bead{
		{ID: "task-executable", Type: "task"},
		{ID: "epic-with-child", Type: "epic"},
	})
	if len(got) != 1 || got[0].ID != "task-executable" {
		t.Fatalf("executable beads = %+v", got)
	}
}

func TestFilterReviewCheckpointBlockedBeadsShortCircuitsEmptyAndNilDatabase(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.ExecContext(ctx, `DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
		t.Fatalf("drop checkpoint view: %v", err)
	}
	got := d.filterReviewCheckpointBlockedBeads(ctx, []protocol.Bead{})
	if got == nil || len(got) != 0 {
		t.Fatalf("empty result = %#v, want non-nil empty input", got)
	}
	d.mu.Lock()
	observation := d.checkpointObservationError
	d.mu.Unlock()
	if observation != "" {
		t.Fatalf("empty input recorded observation = %q", observation)
	}

	nilDB := &Dispatcher{}
	input := []protocol.Bead{{ID: "bead", Type: "task"}}
	got = nilDB.filterReviewCheckpointBlockedBeads(ctx, input)
	if len(got) != 1 || got[0].ID != "bead" {
		t.Fatalf("nil database result = %+v", got)
	}
}

func TestFilterReviewCheckpointBlockedBeadsFiltersAndAuditsExactRows(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, reviewCheckpointInput("bead-blocked")); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	got := d.filterReviewCheckpointBlockedBeads(ctx, []protocol.Bead{
		{ID: "bead-blocked", Type: "task"},
		{ID: "bead-allowed", Type: "task"},
	})
	if len(got) != 1 || got[0].ID != "bead-allowed" {
		t.Fatalf("filtered beads = %+v", got)
	}
	if count := mutationEventCount(t, d, "review_checkpoint_assignment_blocked", "bead-blocked"); count != 1 {
		t.Fatalf("blocked events = %d, want 1", count)
	}
}

func TestFilterReviewCheckpointBlockedBeadsFailsClosedAndRecordsObservation(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.ExecContext(ctx, `DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
		t.Fatalf("drop checkpoint view: %v", err)
	}
	got := d.filterReviewCheckpointBlockedBeads(ctx, []protocol.Bead{{ID: "bead", Type: "task"}})
	if got != nil {
		t.Fatalf("filter result = %+v, want nil on observation failure", got)
	}
	d.mu.Lock()
	observation := d.checkpointObservationError
	d.mu.Unlock()
	if !strings.Contains(observation, "review_checkpoints_blocking_assignment") {
		t.Fatalf("checkpoint observation = %q", observation)
	}
	if count := mutationEventCount(t, d, "review_checkpoint_assignment_filter_failed", ""); count != 1 {
		t.Fatalf("filter failure events = %d, want 1", count)
	}
}

func TestReviewCheckpointBlockedBeadsReturnsExactSetAndScanErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("exact set", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		for _, beadID := range []string{"bead-a", "bead-b"} {
			if _, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, reviewCheckpointInput(beadID)); err != nil {
				t.Fatalf("create checkpoint %s: %v", beadID, err)
			}
		}
		blocked, err := d.reviewCheckpointBlockedBeads(ctx)
		if err != nil || len(blocked) != 2 || !blocked["bead-a"] || !blocked["bead-b"] {
			t.Fatalf("blocked set = %#v, %v", blocked, err)
		}
	})
	t.Run("scan error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.db.ExecContext(ctx, `
			DROP VIEW review_checkpoints_blocking_assignment;
			CREATE VIEW review_checkpoints_blocking_assignment AS SELECT NULL AS bead_id`); err != nil {
			t.Fatalf("install invalid checkpoint view: %v", err)
		}
		blocked, err := d.reviewCheckpointBlockedBeads(ctx)
		if err == nil || blocked != nil || !strings.Contains(err.Error(), "scan blocking") {
			t.Fatalf("blocked set = %#v, %v, want scan error", blocked, err)
		}
	})
}

func TestReviewCheckpointBlocksAssignmentHandlesNilDatabaseAndExactState(t *testing.T) {
	ctx := context.Background()
	if blocked, err := (&Dispatcher{}).reviewCheckpointBlocksAssignment(ctx, "bead"); err != nil || blocked {
		t.Fatalf("nil database blocked = %t, %v", blocked, err)
	}
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, reviewCheckpointInput("bead-blocked")); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	for beadID, want := range map[string]bool{"bead-blocked": true, "bead-allowed": false} {
		blocked, err := d.reviewCheckpointBlocksAssignment(ctx, beadID)
		if err != nil || blocked != want {
			t.Fatalf("blocked(%s) = %t, %v, want %t", beadID, blocked, err, want)
		}
	}
}

func TestReviewCheckpointBlocksAssignmentReportsObservationFailure(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.Exec(`DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
		t.Fatalf("drop checkpoint view: %v", err)
	}
	blocked, err := d.reviewCheckpointBlocksAssignment(context.Background(), "bead")
	if err == nil || blocked || !strings.Contains(err.Error(), "query blocking review checkpoint") {
		t.Fatalf("blocked = %t, %v, want query error", blocked, err)
	}
}

func TestTryRecoverExternalCloseWorkAuditsSuccessProof(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if !d.tryRecoverExternalCloseWork(ctx, "worker-success", "bead-success", "/tmp/bead-success", "main") {
		t.Fatal("recover external close = false, want true")
	}
	var payload string
	if err := d.db.QueryRowContext(ctx, `
		SELECT payload FROM events WHERE type='external_close_recovered' AND bead_id='bead-success'
		ORDER BY id DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("query recovery event: %v", err)
	}
	if !strings.Contains(payload, `"branch":"agent/bead-success"`) || !strings.Contains(payload, `"target":"main"`) {
		t.Fatalf("recovery payload = %q", payload)
	}
}

func TestTryRecoverExternalCloseWorkAuditsAndEscalatesFailureCause(t *testing.T) {
	ctx := context.Background()
	d, _, _, esc, git, _ := newTestDispatcher(t)
	git.conflict = true
	if d.tryRecoverExternalCloseWork(ctx, "worker-failure", "bead-failure", "/tmp/bead-failure", "main") {
		t.Fatal("recover external close = true, want false")
	}
	var payload string
	if err := d.db.QueryRowContext(ctx, `
		SELECT payload FROM events WHERE type='external_close_recovery_failed' AND bead_id='bead-failure'
		ORDER BY id DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("query recovery failure event: %v", err)
	}
	if !strings.Contains(payload, "conflict") || !strings.Contains(payload, "/tmp/bead-failure") {
		t.Fatalf("recovery failure payload = %q", payload)
	}
	messages := esc.Messages()
	if len(messages) != 1 || !strings.Contains(messages[0], "conflict") || !strings.Contains(messages[0], "/tmp/bead-failure") {
		t.Fatalf("recovery escalations = %q", messages)
	}
}

func mutationEventCount(t *testing.T, d *Dispatcher, eventType, beadID string) int {
	t.Helper()
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&count); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return count
}
