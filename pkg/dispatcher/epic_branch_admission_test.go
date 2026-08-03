package dispatcher //nolint:testpackage // white-box test exercises dispatcher admission orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

type admissionTestWorktreeManager struct {
	*mockWorktreeManager

	mu                 sync.Mutex
	inspectionCalls    int
	mutationCalls      int
	inspectionComplete bool
	oldOID             string
	newOID             string
	inspection         epicBranchInspection
	mutationErr        error
	mutationHook       func()
	inspectionStarted  chan struct{}
	continueInspection chan struct{}
}

func (m *admissionTestWorktreeManager) inspectEpicBranch(ctx context.Context, branch, targetBranch string) (epicBranchInspection, error) {
	m.mu.Lock()
	m.inspectionCalls++
	first := m.inspectionCalls == 1
	m.mu.Unlock()
	if first {
		close(m.inspectionStarted)
	}
	select {
	case <-ctx.Done():
		return epicBranchInspection{}, ctx.Err()
	case <-m.continueInspection:
	}
	m.mu.Lock()
	m.inspectionComplete = true
	inspection := m.inspection
	m.mu.Unlock()
	if inspection.BranchOID == "" {
		inspection = epicBranchInspection{
			BranchOID: "branch-before",
			BaseOID:   "target-current",
			Relation:  branchStrictlyBehind,
		}
	}
	return inspection, nil
}

func (m *admissionTestWorktreeManager) compareAndSwapBranch(_ context.Context, _ string, oldOID, newOID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.inspectionComplete {
		return fmt.Errorf("mutation ran before fresh inspection completed")
	}
	m.mutationCalls++
	m.oldOID = oldOID
	m.newOID = newOID
	if m.mutationHook != nil {
		m.mutationHook()
	}
	return m.mutationErr
}

func TestPrepareEpicBranchAdmissionSerializesFreshInspection(t *testing.T) {
	t.Run("serializes concurrent fresh inspection", testConcurrentFreshEpicBranchAdmission)
	t.Run("assignment uses admission guard", testAssignmentUsesEpicBranchAdmission)
}

func testConcurrentFreshEpicBranchAdmission(t *testing.T) {
	ctx := context.Background()
	d, beads, baseWorktrees, escalator, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  make(chan struct{}),
	}
	d.worktrees = manager
	d.epicAdmissionRenewEvery = 5 * time.Millisecond

	logicalNow := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	d.nowFunc = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now := logicalNow
		logicalNow = logicalNow.Add(epicBranchAdmissionLeaseRenewInterval)
		return now
	}

	type result struct {
		beadID  string
		proceed bool
	}
	results := make(chan result, 2)
	run := func(beadID, workerID string) {
		bead := protocol.Bead{ID: beadID, Epic: "oro-e"}
		results <- result{
			beadID:  beadID,
			proceed: d.withEpicBranchAdmission(ctx, bead, workerID, "epic/oro-e", "oro-e", "main"),
		}
	}

	go run("oro-child-a", "worker-a")
	select {
	case <-manager.inspectionStarted:
	case early := <-results:
		var eventType, payload string
		_ = d.db.QueryRowContext(ctx, `SELECT type, payload FROM events ORDER BY id DESC LIMIT 1`).Scan(&eventType, &payload)
		t.Fatalf("winner returned before fresh inspection: %+v; latest event %q %q", early, eventType, payload)
	case <-time.After(time.Second):
		t.Fatal("winner did not begin fresh inspection")
	}
	d.mu.Lock()
	d.assigningBeads["oro-child-b"] = true
	d.mu.Unlock()
	go run("oro-child-b", "worker-b")

	select {
	case loser := <-results:
		if loser.beadID != "oro-child-b" || loser.proceed {
			t.Fatalf("contender result = %+v, want child-b quiet skip", loser)
		}
	case <-time.After(time.Second):
		t.Fatal("contender did not skip while winner held branch lease")
	}
	d.mu.Lock()
	_, contenderStillAssigning := d.assigningBeads["oro-child-b"]
	d.mu.Unlock()
	beads.mu.Lock()
	contenderStatus := beads.updated["oro-child-b"]
	beads.mu.Unlock()
	if contenderStillAssigning || contenderStatus != "open" {
		t.Fatalf("contender cleanup = assigning %v status %q, want false/open", contenderStillAssigning, contenderStatus)
	}

	waitFor(t, func() bool {
		var state, expiresAtText string
		if err := d.db.QueryRowContext(ctx, `
SELECT state, lease_expires_at
FROM epic_branch_admissions
WHERE branch = 'epic/oro-e'`).Scan(&state, &expiresAtText); err != nil {
			return false
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
		return err == nil && state == "leased" && expiresAt.After(time.Date(2026, 8, 3, 12, 2, 0, 0, time.UTC))
	}, time.Second)

	close(manager.continueInspection)
	select {
	case winner := <-results:
		if winner.beadID != "oro-child-a" || !winner.proceed {
			t.Fatalf("winner result = %+v, want child-a proceed", winner)
		}
	case <-time.After(time.Second):
		t.Fatal("winner did not finish after inspection was released")
	}

	manager.mu.Lock()
	inspectionCalls := manager.inspectionCalls
	mutationCalls := manager.mutationCalls
	oldOID, newOID := manager.oldOID, manager.newOID
	manager.mu.Unlock()
	if inspectionCalls != 1 || mutationCalls != 1 || oldOID != "branch-before" || newOID != "target-current" {
		t.Fatalf("fresh inspection/mutation = inspections %d mutations %d CAS %q -> %q", inspectionCalls, mutationCalls, oldOID, newOID)
	}

	var state string
	var generation int64
	var leaseOwner sql.NullString
	var leaseExpiresAt *string
	if err := d.db.QueryRowContext(ctx, `
SELECT state, generation, lease_owner, lease_expires_at
FROM epic_branch_admissions
WHERE branch = 'epic/oro-e'`).Scan(&state, &generation, &leaseOwner, &leaseExpiresAt); err != nil {
		t.Fatalf("read released admission: %v", err)
	}
	if state != "resolved" || generation != 1 || leaseOwner.Valid || leaseExpiresAt != nil {
		t.Fatalf("released admission = state %q generation %d owner %v expiry %v", state, generation, leaseOwner, leaseExpiresAt)
	}

	d.mu.Lock()
	_, contenderCooldown := d.worktreeFailures["oro-child-b"]
	frozen := d.assignmentFrozenByQuarantine
	d.mu.Unlock()
	if contenderCooldown || frozen || len(escalator.Messages()) != 0 {
		t.Fatalf("contention side effects = cooldown %v frozen %v escalations %q", contenderCooldown, frozen, escalator.Messages())
	}
	for _, eventType := range []string{"epic_branch_prepare_failed", "epic_branch_missing", "assignment_race_detected"} {
		if got := eventCount(t, d.db, eventType); got != 0 {
			t.Errorf("contention emitted %s events = %d, want 0", eventType, got)
		}
	}
}

func testAssignmentUsesEpicBranchAdmission(t *testing.T) {
	ctx := context.Background()
	d, beads, baseWorktrees, _, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	continueInspection := make(chan struct{})
	close(continueInspection)
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  continueInspection,
	}
	d.worktrees = manager

	const (
		epicID = "oro-e-assignment"
		beadID = "oro-e-assignment-child"
	)
	beads.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Admission epic", Type: "epic", Status: "in_progress"}
	beads.shown[beadID] = &protocol.BeadDetail{
		ID: beadID, Title: "Admission child", Type: "task", Status: "open",
		AcceptanceCriteria: "Test: admission | Assert: guarded",
	}
	worker := &trackedWorker{id: "worker-assignment", state: protocol.WorkerIdle, conn: newMockConn()}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()

	if err := d.assignBead(ctx, worker, protocol.Bead{ID: beadID, Title: "Admission child", Type: "task", Epic: epicID}); err != nil {
		t.Fatalf("assign epic child: %v", err)
	}
	var state string
	if err := d.db.QueryRowContext(ctx, `SELECT state FROM epic_branch_admissions WHERE branch=?`, "epic/"+epicID).Scan(&state); err != nil {
		t.Fatalf("load assignment admission: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("assignment admission state = %q, want resolved", state)
	}
	manager.mu.Lock()
	inspectionCalls := manager.inspectionCalls
	manager.mu.Unlock()
	if inspectionCalls != 1 {
		t.Fatalf("assignment fresh inspections = %d, want 1", inspectionCalls)
	}
}

func TestEpicBranchAdmissionBlocksUnsafeFreshInspection(t *testing.T) {
	tests := []struct {
		name         string
		inspection   epicBranchInspection
		mutationErr  error
		wantKind     string
		wantCheckout string
	}{
		{
			name: "checked out during inspection",
			inspection: epicBranchInspection{
				BranchOID: "checked-branch", BaseOID: "target-sha", Relation: branchContainsBase,
				CheckedOutPaths: []string{"/tmp/epic-checkout"},
			},
			wantKind: "checked_out", wantCheckout: "/tmp/epic-checkout",
		},
		{
			name:       "diverged during inspection",
			inspection: epicBranchInspection{BranchOID: "diverged-branch", BaseOID: "target-sha", Relation: branchDiverged},
			wantKind:   "diverged",
		},
		{
			name:         "checked out before compare and swap",
			inspection:   epicBranchInspection{BranchOID: "behind-branch", BaseOID: "target-sha", Relation: branchStrictlyBehind},
			mutationErr:  &epicBranchCheckedOutError{Branch: "epic/oro-unsafe", CheckedOutPaths: []string{"/tmp/late-checkout"}},
			wantKind:     "checked_out",
			wantCheckout: "/tmp/late-checkout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, beads, baseWorktrees, escalator, _, _ := newTestDispatcher(t)
			if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
				t.Fatalf("migrate epic branch admission schema: %v", err)
			}
			continueInspection := make(chan struct{})
			close(continueInspection)
			manager := &admissionTestWorktreeManager{
				mockWorktreeManager: baseWorktrees,
				inspection:          tt.inspection,
				mutationErr:         tt.mutationErr,
				inspectionStarted:   make(chan struct{}),
				continueInspection:  continueInspection,
			}
			d.worktrees = manager

			bead := protocol.Bead{ID: "oro-unsafe-child", Epic: "oro-unsafe"}
			d.mu.Lock()
			d.assigningBeads[bead.ID] = true
			d.mu.Unlock()
			if d.withEpicBranchAdmission(ctx, bead, "worker-a", "epic/oro-unsafe", "oro-unsafe", "main") {
				t.Fatal("unsafe epic branch admission proceeded")
			}
			var state, blockerKind, branchSHA, targetSHA, details string
			var checkoutPath sql.NullString
			if err := d.db.QueryRowContext(ctx, `
SELECT state, blocker_kind, checkout_path, branch_sha, target_sha, details
FROM epic_branch_admissions
WHERE branch = 'epic/oro-unsafe'`).Scan(
				&state, &blockerKind, &checkoutPath, &branchSHA, &targetSHA, &details,
			); err != nil {
				t.Fatalf("read blocked admission: %v", err)
			}
			if state != "blocked" || blockerKind != tt.wantKind || checkoutPath.String != tt.wantCheckout ||
				branchSHA != tt.inspection.BranchOID || targetSHA != tt.inspection.BaseOID || details == "" {
				t.Fatalf("blocked admission = state %q kind %q checkout %v branch %q target %q details %q",
					state, blockerKind, checkoutPath, branchSHA, targetSHA, details)
			}
			otherBead := protocol.Bead{ID: "oro-other-child", Epic: "oro-unsafe"}
			d.mu.Lock()
			d.assigningBeads[otherBead.ID] = true
			d.mu.Unlock()
			if d.withEpicBranchAdmission(ctx, otherBead, "worker-b", "epic/oro-unsafe", "oro-unsafe", "main") {
				t.Fatal("durably blocked epic branch was admitted")
			}
			manager.mu.Lock()
			inspectionCalls := manager.inspectionCalls
			manager.mu.Unlock()
			if inspectionCalls != 1 {
				t.Fatalf("durable blocker inspections = %d, want one", inspectionCalls)
			}
			d.mu.Lock()
			_, unsafeStillAssigning := d.assigningBeads[bead.ID]
			_, blockedStillAssigning := d.assigningBeads[otherBead.ID]
			_, cooldown := d.worktreeFailures[bead.ID]
			d.mu.Unlock()
			beads.mu.Lock()
			unsafeStatus, otherStatus := beads.updated[bead.ID], beads.updated[otherBead.ID]
			beads.mu.Unlock()
			if unsafeStillAssigning || blockedStillAssigning || unsafeStatus != "open" || otherStatus != "open" || cooldown || len(escalator.Messages()) != 0 {
				t.Fatalf("durable blocker side effects = assigning %v/%v statuses %q/%q cooldown %v escalations %q",
					unsafeStillAssigning, blockedStillAssigning, unsafeStatus, otherStatus, cooldown, escalator.Messages())
			}
		})
	}
}

func TestEpicBranchAdmissionCreatesMissingBranchInsideLease(t *testing.T) {
	ctx := context.Background()
	d, _, baseWorktrees, _, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	var mu sync.Mutex
	branchExists := false
	createCalls := 0
	baseWorktrees.branchExistsFn = func(context.Context, string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return branchExists, nil
	}
	baseWorktrees.createBranchFn = func(_ context.Context, name, target string) error {
		mu.Lock()
		defer mu.Unlock()
		if name != "epic/oro-missing" || target != "release/target" {
			return fmt.Errorf("create branch %q from %q", name, target)
		}
		createCalls++
		branchExists = true
		return nil
	}
	continueInspection := make(chan struct{})
	close(continueInspection)
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  continueInspection,
	}
	d.worktrees = manager

	bead := protocol.Bead{ID: "oro-missing-child", Epic: "oro-missing"}
	if !d.withEpicBranchAdmission(ctx, bead, "worker-a", "epic/oro-missing", "oro-missing", "release/target") {
		t.Fatal("missing epic branch was not created under admission")
	}
	mu.Lock()
	gotCreateCalls := createCalls
	mu.Unlock()
	manager.mu.Lock()
	inspectionCalls := manager.inspectionCalls
	manager.mu.Unlock()
	if gotCreateCalls != 1 || inspectionCalls != 0 {
		t.Fatalf("missing branch preparation = creates %d inspections %d, want 1/0", gotCreateCalls, inspectionCalls)
	}
	var state string
	if err := d.db.QueryRowContext(ctx, `SELECT state FROM epic_branch_admissions WHERE branch='epic/oro-missing'`).Scan(&state); err != nil {
		t.Fatalf("read missing-branch admission: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("missing-branch admission state = %q, want resolved", state)
	}
}

func TestEpicBranchAdmissionCancellationReleasesOnlyHeldLease(t *testing.T) {
	for _, tt := range []struct {
		name           string
		replaceHolder  bool
		wantState      string
		wantGeneration int64
		wantToken      string
	}{
		{name: "held lease resolves", wantState: "resolved", wantGeneration: 1},
		{name: "stale holder cannot resolve replacement", replaceHolder: true, wantState: "leased", wantGeneration: 2, wantToken: "replacement-token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			d, _, baseWorktrees, _, _, _ := newTestDispatcher(t)
			if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
				t.Fatalf("migrate epic branch admission schema: %v", err)
			}
			manager := &admissionTestWorktreeManager{
				mockWorktreeManager: baseWorktrees,
				inspectionStarted:   make(chan struct{}),
				continueInspection:  make(chan struct{}),
			}
			d.worktrees = manager
			d.epicAdmissionRenewEvery = 5 * time.Millisecond
			result := make(chan bool, 1)
			go func() {
				result <- d.withEpicBranchAdmission(ctx, protocol.Bead{ID: "oro-cancel-child", Epic: "oro-cancel"},
					"worker-a", "epic/oro-cancel", "oro-cancel", "main")
			}()
			select {
			case <-manager.inspectionStarted:
			case <-time.After(time.Second):
				t.Fatal("canceled admission did not begin inspection")
			}
			var originalToken string
			if err := d.db.QueryRowContext(ctx, `SELECT lease_token FROM epic_branch_admissions WHERE branch='epic/oro-cancel'`).Scan(&originalToken); err != nil {
				t.Fatalf("read original lease token: %v", err)
			}
			if tt.replaceHolder {
				if _, err := d.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET generation=2, lease_token='replacement-token', lease_owner='worker-b',
    lease_expires_at='2099-01-01T00:00:00Z'
WHERE branch='epic/oro-cancel'`); err != nil {
					t.Fatalf("replace lease holder: %v", err)
				}
			}
			cancel()
			select {
			case proceed := <-result:
				if proceed {
					t.Fatal("canceled admission proceeded")
				}
			case <-time.After(time.Second):
				t.Fatal("canceled admission did not stop")
			}
			var state, token string
			var generation int64
			if err := d.db.QueryRow(`
SELECT state, generation, COALESCE(lease_token, '')
FROM epic_branch_admissions
WHERE branch='epic/oro-cancel'`).Scan(&state, &generation, &token); err != nil {
				t.Fatalf("read canceled admission: %v", err)
			}
			wantToken := originalToken
			if tt.replaceHolder {
				wantToken = tt.wantToken
			}
			if state != tt.wantState || generation != tt.wantGeneration || token != wantToken {
				t.Fatalf("canceled admission = state %q generation %d token %q, want %q/%d/%q",
					state, generation, token, tt.wantState, tt.wantGeneration, wantToken)
			}
		})
	}
}

func TestEpicBranchAdmissionCancellationAfterPreparationReleasesHeldLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d, _, baseWorktrees, _, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	continueInspection := make(chan struct{})
	close(continueInspection)
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  continueInspection,
		mutationHook:        cancel,
	}
	d.worktrees = manager

	bead := protocol.Bead{ID: "oro-cancel-after-child", Epic: "oro-cancel-after"}
	if d.withEpicBranchAdmission(ctx, bead, "worker-a", "epic/oro-cancel-after", "oro-cancel-after", "main") {
		t.Fatal("admission proceeded after cancellation at the end of preparation")
	}
	var state string
	if err := d.db.QueryRow(`SELECT state FROM epic_branch_admissions WHERE branch='epic/oro-cancel-after'`).Scan(&state); err != nil {
		t.Fatalf("read canceled admission: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("canceled admission state = %q, want matching held lease resolved", state)
	}
}

func TestEpicBranchAdmissionRenewalLossStopsMutation(t *testing.T) {
	ctx := context.Background()
	d, _, baseWorktrees, _, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  make(chan struct{}),
	}
	d.worktrees = manager
	d.epicAdmissionRenewEvery = 5 * time.Millisecond
	result := make(chan bool, 1)
	go func() {
		result <- d.withEpicBranchAdmission(ctx, protocol.Bead{ID: "oro-renew-child", Epic: "oro-renew"},
			"worker-a", "epic/oro-renew", "oro-renew", "main")
	}()
	select {
	case <-manager.inspectionStarted:
	case <-time.After(time.Second):
		t.Fatal("admission did not begin inspection")
	}
	if _, err := d.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET generation=2, lease_token='replacement-token', lease_owner='worker-b',
    lease_expires_at='2099-01-01T00:00:00Z'
WHERE branch='epic/oro-renew'`); err != nil {
		t.Fatalf("replace lease holder: %v", err)
	}
	waitFor(t, func() bool {
		return eventCount(t, d.db, "epic_branch_admission_renew_failed") == 1
	}, time.Second)
	close(manager.continueInspection)
	select {
	case proceed := <-result:
		if proceed {
			t.Fatal("admission proceeded after renewal ownership was lost")
		}
	case <-time.After(time.Second):
		t.Fatal("admission did not stop after renewal ownership was lost")
	}
	manager.mu.Lock()
	mutationCalls := manager.mutationCalls
	manager.mu.Unlock()
	if mutationCalls != 0 {
		t.Fatalf("mutations after renewal loss = %d, want 0", mutationCalls)
	}
	var state, token string
	var generation int64
	if err := d.db.QueryRow(`
SELECT state, generation, lease_token
FROM epic_branch_admissions
WHERE branch='epic/oro-renew'`).Scan(&state, &generation, &token); err != nil {
		t.Fatalf("read replacement admission: %v", err)
	}
	if state != "leased" || generation != 2 || token != "replacement-token" {
		t.Fatalf("replacement admission = %q/%d/%q, want leased/2/replacement-token", state, generation, token)
	}
}

func TestEpicBranchAdmissionRenewalLossRestoresClaimedAssignment(t *testing.T) {
	ctx := context.Background()
	d, beads, baseWorktrees, escalator, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  make(chan struct{}),
	}
	d.worktrees = manager
	d.epicAdmissionRenewEvery = 5 * time.Millisecond
	const (
		epicID = "oro-renew-pipeline"
		beadID = "oro-renew-pipeline-child"
	)
	beads.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Renewal epic", Type: "epic", Status: "in_progress"}
	beads.shown[beadID] = &protocol.BeadDetail{
		ID: beadID, Title: "Renewal child", Type: "task", Status: "open",
		AcceptanceCriteria: "Test: renewal ownership | Assert: assignment aborts quietly",
	}
	worker := &trackedWorker{id: "worker-renew-pipeline", state: protocol.WorkerIdle, conn: newMockConn()}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()

	assignDone := make(chan error, 1)
	go func() {
		assignDone <- d.assignBead(ctx, worker, protocol.Bead{ID: beadID, Title: "Renewal child", Type: "task", Epic: epicID})
	}()
	select {
	case <-manager.inspectionStarted:
	case <-time.After(time.Second):
		t.Fatal("assignment did not begin epic inspection")
	}
	if _, err := d.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET generation=2, lease_token='replacement-token', lease_owner='worker-b',
    lease_expires_at='2099-01-01T00:00:00Z'
WHERE branch='epic/oro-renew-pipeline'`); err != nil {
		t.Fatalf("replace lease holder: %v", err)
	}
	waitFor(t, func() bool {
		return eventCount(t, d.db, "epic_branch_admission_renew_failed") == 1
	}, time.Second)
	close(manager.continueInspection)
	select {
	case err := <-assignDone:
		if err != nil {
			t.Fatalf("assign bead after renewal loss: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("assignment did not stop after renewal ownership loss")
	}

	beads.mu.Lock()
	beadStatus := beads.updated[beadID]
	beads.mu.Unlock()
	d.mu.Lock()
	workerState, workerBead := worker.state, worker.beadID
	_, stillAssigning := d.assigningBeads[beadID]
	_, cooldown := d.worktreeFailures[beadID]
	_, attempts := d.attemptCounts[beadID]
	frozen := d.assignmentFrozenByQuarantine
	d.mu.Unlock()
	if beadStatus != "open" || workerState != protocol.WorkerIdle || workerBead != "" || stillAssigning {
		t.Fatalf("renewal-loss lifecycle = status %q worker %s/%q assigning %v, want open idle/empty false",
			beadStatus, workerState, workerBead, stillAssigning)
	}
	if cooldown || attempts || frozen || len(escalator.Messages()) != 0 {
		t.Fatalf("renewal-loss side effects = cooldown %v attempts %v frozen %v escalations %q",
			cooldown, attempts, frozen, escalator.Messages())
	}
	if got := eventCount(t, d.db, "epic_branch_prepare_failed"); got != 0 {
		t.Fatalf("renewal loss emitted preparation failures = %d, want 0", got)
	}
	var assignments int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE bead_id=?`, beadID).Scan(&assignments); err != nil {
		t.Fatalf("count renewal-loss assignments: %v", err)
	}
	if assignments != 0 {
		t.Fatalf("renewal-loss assignments = %d, want 0", assignments)
	}
	var state, token string
	var generation int64
	if err := d.db.QueryRowContext(ctx, `
SELECT state, generation, lease_token
FROM epic_branch_admissions
WHERE branch='epic/oro-renew-pipeline'`).Scan(&state, &generation, &token); err != nil {
		t.Fatalf("read replacement admission: %v", err)
	}
	if state != "leased" || generation != 2 || token != "replacement-token" {
		t.Fatalf("replacement admission = %q/%d/%q, want leased/2/replacement-token", state, generation, token)
	}
}

func TestEpicBranchAdmissionCancellationRestoresClaimedAssignment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d, beads, baseWorktrees, escalator, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  make(chan struct{}),
	}
	d.worktrees = manager
	const (
		epicID = "oro-cancel-pipeline"
		beadID = "oro-cancel-pipeline-child"
	)
	beads.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Cancellation epic", Type: "epic", Status: "in_progress"}
	beads.shown[beadID] = &protocol.BeadDetail{
		ID: beadID, Title: "Cancellation child", Type: "task", Status: "open",
		AcceptanceCriteria: "Test: cancellation cleanup | Assert: task reopens",
	}
	beads.updateFn = func(updateCtx context.Context, id string, params beadstore.UpdateParams) error {
		if err := updateCtx.Err(); err != nil {
			return err
		}
		beads.mu.Lock()
		defer beads.mu.Unlock()
		if beads.updated == nil {
			beads.updated = make(map[string]string)
		}
		if params.Status != nil {
			beads.updated[id] = *params.Status
		}
		return nil
	}
	worker := &trackedWorker{id: "worker-cancel-pipeline", state: protocol.WorkerIdle, conn: newMockConn()}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	assignDone := make(chan error, 1)
	go func() {
		assignDone <- d.assignBead(ctx, worker, protocol.Bead{ID: beadID, Title: "Cancellation child", Type: "task", Epic: epicID})
	}()
	select {
	case <-manager.inspectionStarted:
	case <-time.After(time.Second):
		t.Fatal("assignment did not begin epic inspection")
	}
	cancel()
	select {
	case err := <-assignDone:
		if err != nil {
			t.Fatalf("assign bead after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled assignment did not stop")
	}
	assertQuietAdmissionAssignmentAbort(t, d, beads, escalator, worker, beadID)
	var state string
	if err := d.db.QueryRow(`SELECT state FROM epic_branch_admissions WHERE branch='epic/oro-cancel-pipeline'`).Scan(&state); err != nil {
		t.Fatalf("read canceled admission: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("canceled admission state = %q, want resolved", state)
	}
}

func TestEpicBranchAdmissionBlockedAssignmentIsRecoverable(t *testing.T) {
	for _, tt := range []struct {
		name         string
		blockerKind  string
		inspection   epicBranchInspection
		checkoutPath string
	}{
		{
			name: "checked out", blockerKind: "checked_out", checkoutPath: "/tmp/epic-pipeline",
			inspection: epicBranchInspection{
				BranchOID: "checked-branch", BaseOID: "target-sha", Relation: branchContainsBase,
				CheckedOutPaths: []string{"/tmp/epic-pipeline"},
			},
		},
		{
			name: "diverged", blockerKind: "diverged",
			inspection: epicBranchInspection{BranchOID: "diverged-branch", BaseOID: "target-sha", Relation: branchDiverged},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, beads, baseWorktrees, escalator, _, _ := newTestDispatcher(t)
			if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
				t.Fatalf("migrate epic branch admission schema: %v", err)
			}
			continueInspection := make(chan struct{})
			close(continueInspection)
			manager := &admissionTestWorktreeManager{
				mockWorktreeManager: baseWorktrees,
				inspection:          tt.inspection,
				inspectionStarted:   make(chan struct{}),
				continueInspection:  continueInspection,
			}
			d.worktrees = manager
			epicID := "oro-blocked-pipeline-" + strings.ReplaceAll(tt.blockerKind, "_", "-")
			beadID := epicID + "-child"
			branch := "epic/" + epicID
			beads.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Blocked epic", Type: "epic", Status: "in_progress"}
			beads.shown[beadID] = &protocol.BeadDetail{
				ID: beadID, Title: "Blocked child", Type: "task", Status: "open",
				AcceptanceCriteria: "Test: durable blocker | Assert: resolves and assigns",
			}
			worker := &trackedWorker{id: "worker-" + tt.blockerKind, state: protocol.WorkerIdle, conn: newMockConn()}
			d.mu.Lock()
			d.workers[worker.id] = worker
			d.mu.Unlock()
			bead := protocol.Bead{ID: beadID, Title: "Blocked child", Type: "task", Epic: epicID}

			if err := d.assignBead(ctx, worker, bead); err != nil {
				t.Fatalf("assign unsafe epic child: %v", err)
			}
			assertQuietAdmissionAssignmentAbort(t, d, beads, escalator, worker, beadID)
			var state, blockerKind, checkoutPath string
			var generation int64
			if err := d.db.QueryRowContext(ctx, `
SELECT state, generation, blocker_kind, COALESCE(checkout_path, '')
FROM epic_branch_admissions
WHERE branch=?`, branch).Scan(&state, &generation, &blockerKind, &checkoutPath); err != nil {
				t.Fatalf("read durable blocker: %v", err)
			}
			if state != "blocked" || generation != 1 || blockerKind != tt.blockerKind || checkoutPath != tt.checkoutPath {
				t.Fatalf("durable blocker = %q/%d/%q/%q, want blocked/1/%q/%q",
					state, generation, blockerKind, checkoutPath, tt.blockerKind, tt.checkoutPath)
			}

			if err := d.assignBead(ctx, worker, bead); err != nil {
				t.Fatalf("assign while durable blocker remains: %v", err)
			}
			assertQuietAdmissionAssignmentAbort(t, d, beads, escalator, worker, beadID)
			manager.mu.Lock()
			inspectionCalls := manager.inspectionCalls
			manager.mu.Unlock()
			if inspectionCalls != 1 {
				t.Fatalf("Git inspections while blocker remained = %d, want 1", inspectionCalls)
			}
			if err := newEpicBranchAdmissionStore(d.db).resolve(ctx, branch, generation, d.nowFunc()); err != nil {
				t.Fatalf("resolve durable blocker: %v", err)
			}
			manager.mu.Lock()
			manager.inspection = epicBranchInspection{BranchOID: "safe-sha", BaseOID: "safe-sha", Relation: branchSame}
			manager.mu.Unlock()

			if err := d.assignBead(ctx, worker, bead); err != nil {
				t.Fatalf("assign after blocker resolution: %v", err)
			}
			var assignments int
			if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID).Scan(&assignments); err != nil {
				t.Fatalf("count recovered assignments: %v", err)
			}
			d.mu.Lock()
			workerState, workerBead := worker.state, worker.beadID
			_, stillAssigning := d.assigningBeads[beadID]
			d.mu.Unlock()
			if assignments != 1 || workerState != protocol.WorkerBusy || workerBead != beadID || stillAssigning {
				t.Fatalf("recovered assignment = rows %d worker %s/%q assigning %v, want 1 busy/%q false",
					assignments, workerState, workerBead, stillAssigning, beadID)
			}
			if err := d.db.QueryRowContext(ctx, `SELECT state, generation FROM epic_branch_admissions WHERE branch=?`, branch).Scan(&state, &generation); err != nil {
				t.Fatalf("read recovered admission: %v", err)
			}
			if state != "resolved" || generation != 2 {
				t.Fatalf("recovered admission = %q/%d, want resolved/2", state, generation)
			}
		})
	}
}

func assertQuietAdmissionAssignmentAbort(
	t *testing.T,
	d *Dispatcher,
	beads *fakeBeadStore,
	escalator *mockEscalator,
	worker *trackedWorker,
	beadID string,
) {
	t.Helper()
	beads.mu.Lock()
	status := beads.updated[beadID]
	beads.mu.Unlock()
	d.mu.Lock()
	workerState, workerBead := worker.state, worker.beadID
	_, stillAssigning := d.assigningBeads[beadID]
	_, cooldown := d.worktreeFailures[beadID]
	_, attempts := d.attemptCounts[beadID]
	frozen := d.assignmentFrozenByQuarantine
	d.mu.Unlock()
	if status != "open" || workerState != protocol.WorkerIdle || workerBead != "" || stillAssigning {
		t.Fatalf("blocked lifecycle = status %q worker %s/%q assigning %v, want open idle/empty false",
			status, workerState, workerBead, stillAssigning)
	}
	if cooldown || attempts || frozen || len(escalator.Messages()) != 0 {
		t.Fatalf("blocked side effects = cooldown %v attempts %v frozen %v escalations %q",
			cooldown, attempts, frozen, escalator.Messages())
	}
	var assignments int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE bead_id=?`, beadID).Scan(&assignments); err != nil {
		t.Fatalf("count blocked assignments: %v", err)
	}
	if assignments != 0 {
		t.Fatalf("blocked assignments = %d, want 0", assignments)
	}
}

func TestEpicBranchAdmissionBypassesLedgerForNonEpicBranches(t *testing.T) {
	for _, tt := range []struct {
		name   string
		branch string
	}{
		{name: "default branch", branch: "main"},
		{name: "custom metadata branch", branch: "release/next"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, _, baseWorktrees, _, _, _ := newTestDispatcher(t)
			if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
				t.Fatalf("migrate epic branch admission schema: %v", err)
			}
			branchChecks := 0
			baseWorktrees.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
				branchChecks++
				return branch == tt.branch, nil
			}
			d.worktrees = baseWorktrees
			if !d.withEpicBranchAdmission(ctx, protocol.Bead{ID: "oro-non-epic"}, "worker-a", tt.branch, "", "main") {
				t.Fatalf("non-epic branch %q did not preserve existing readiness behavior", tt.branch)
			}
			var rows int
			if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM epic_branch_admissions`).Scan(&rows); err != nil {
				t.Fatalf("count admission rows: %v", err)
			}
			if rows != 0 || branchChecks != 1 {
				t.Fatalf("non-epic admission = rows %d branch checks %d, want 0/1", rows, branchChecks)
			}
		})
	}
}
