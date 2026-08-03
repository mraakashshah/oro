package dispatcher //nolint:testpackage // end-to-end contract exercises dispatcher-private admission seams.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
)

func TestEpicBranchAdmissionPersistsAcrossRestartWithoutRetrySpam(t *testing.T) { //nolint:funlen // one end-to-end contract intentionally keeps the full lifecycle visible.
	ctx := context.Background()
	clock := &epicBranchAdmissionE2EClock{now: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	dbPath := t.TempDir() + "/state.db"
	db := openEpicBranchAdmissionTestDB(t, dbPath)

	checkedOut := epicBranchInspection{
		BranchOID:       "checked-out-branch",
		BaseOID:         "target-before",
		Relation:        branchDiverged,
		CheckedOutPaths: []string{"/tmp/epic-admission-e2e"},
	}
	d1, beads, admissionManager, workerA, epicID, directID, branch := newEpicRecoveryPipeline(t, checkedOut)
	manager := &epicBranchAdmissionE2EManager{
		admissionTestWorktreeManager: admissionManager,
		blockedBranch:                branch,
	}
	d1.db = db
	d1.worktrees = manager
	d1.nowFunc = clock.Now
	d1.epicAdmissionRenewEvery = 250 * time.Millisecond
	beads.db = db
	manager.mu.Lock()
	manager.inspectionStarted = make(chan struct{})
	manager.continueInspection = make(chan struct{})
	manager.mu.Unlock()

	d2, _, _, _, _, _ := newTestDispatcher(t)
	d2.db = db
	d2.beads = beads
	d2.worktrees = manager
	d2.nowFunc = clock.Now
	workerB := &trackedWorker{id: "worker-admission-b", state: protocol.WorkerIdle, conn: newMockConn()}
	d2.mu.Lock()
	d2.workers[workerB.id] = workerB
	d2.mu.Unlock()

	const (
		middleID        = "oro-admission-middle"
		nestedID        = "oro-admission-nested"
		titleLookalike  = "oro-admission-title-lookalike"
		tagLookalike    = "oro-admission-tag-lookalike"
		unrelatedEpicID = "oro-admission-unrelated-epic"
		unrelatedID     = "oro-admission-unrelated"
	)
	createEpicBranchAdmissionE2EBead(t, beads, beadstore.CreateParams{
		ID: middleID, Title: "Nested parent", Type: "task", Status: "open", ParentID: epicID,
		AcceptanceCriteria: "Test: nested parent | Assert: descendants remain blocked",
	})
	createEpicBranchAdmissionE2EBead(t, beads, beadstore.CreateParams{
		ID: nestedID, Title: "Nested blocked work", Type: "task", Status: "open", ParentID: middleID,
		AcceptanceCriteria: "Test: nested admission | Assert: full ancestry is filtered",
	})
	createEpicBranchAdmissionE2EBead(t, beads, beadstore.CreateParams{
		ID: titleLookalike, Title: "Repair blocked epic branch", Type: "task", Status: "open", ParentID: epicID,
		AcceptanceCriteria: "Test: title lookalike | Assert: title cannot bypass admission",
	})
	createEpicBranchAdmissionE2EBead(t, beads, beadstore.CreateParams{
		ID: tagLookalike, Title: "Tagged lookalike", Type: "task", Status: "open", ParentID: epicID,
		Tags:               []string{epicBranchRecoveryTag},
		AcceptanceCriteria: "Test: tag lookalike | Assert: tag cannot bypass admission",
	})
	createEpicBranchAdmissionE2EBead(t, beads, beadstore.CreateParams{
		ID: unrelatedEpicID, Title: "Unrelated epic", Type: "epic", Status: "in_progress",
		AcceptanceCriteria: "Test: unrelated epic | Assert: no global freeze",
	})
	createEpicBranchAdmissionE2EBead(t, beads, beadstore.CreateParams{
		ID: unrelatedID, Title: "Unrelated runnable work", Type: "task", Status: "open", ParentID: unrelatedEpicID,
		AcceptanceCriteria: "Test: unrelated work | Assert: assignment proceeds",
	})
	recoveryCreate := &epicBranchAdmissionE2ERecoveryBarrier{
		observingEpicRecoveryStore: beads,
		clock:                      clock,
		started:                    make(chan context.Context, 1),
		proceed:                    make(chan struct{}),
	}
	d1.beads = recoveryCreate

	type assignmentResult struct {
		beadID string
		err    error
	}
	results := make(chan assignmentResult, 2)
	go func() {
		results <- assignmentResult{
			beadID: directID,
			err: d1.assignBead(ctx, workerA, protocol.Bead{
				ID: directID, Title: "Direct blocked work", Type: "task", Status: "open", Epic: epicID,
			}),
		}
	}()
	select {
	case <-manager.inspectionStarted:
	case <-time.After(time.Second):
		t.Fatal("first dispatcher did not begin checked-out branch inspection")
	}

	var leasedToken, leasedExpiry string
	var leasedGeneration int64
	if err := db.QueryRowContext(ctx, `
SELECT generation, lease_token, lease_expires_at
FROM epic_branch_admissions
WHERE branch=? AND state='leased'`, branch).Scan(&leasedGeneration, &leasedToken, &leasedExpiry); err != nil {
		t.Fatalf("read first dispatcher lease: %v", err)
	}
	assertEpicBranchAdmissionE2EExpiry(t, leasedExpiry, clock.Now().Add(epicBranchAdmissionLeaseTTL))

	go func() {
		results <- assignmentResult{
			beadID: nestedID,
			err: d2.assignBead(ctx, workerB, protocol.Bead{
				ID: nestedID, Title: "Nested blocked work", Type: "task", Status: "open", Epic: middleID,
			}),
		}
	}()
	select {
	case result := <-results:
		if result.beadID != nestedID || result.err != nil {
			t.Fatalf("contending dispatcher result = %+v, want quiet nested skip", result)
		}
	case <-time.After(time.Second):
		t.Fatal("contending dispatcher did not skip the held branch lease")
	}

	if epicBranchAdmissionLeaseTTL != 2*time.Minute || epicBranchAdmissionLeaseRenewInterval != 30*time.Second {
		t.Fatalf("admission timing = TTL %s renewal %s, want 2m/30s",
			epicBranchAdmissionLeaseTTL, epicBranchAdmissionLeaseRenewInterval)
	}
	close(manager.continueInspection)
	var recoveryCtx context.Context
	select {
	case recoveryCtx = <-recoveryCreate.started:
	case <-time.After(time.Second):
		t.Fatal("blocked admission did not reach recovery materialization")
	}
	blockedBeforeRecovery := loadEpicBranchAdmissionE2E(t, db, branch)
	if blockedBeforeRecovery.state != "blocked" || blockedBeforeRecovery.recoveryBeadID != "" {
		t.Fatalf("admission before recovery materialization = %+v, want durable unlinked block", blockedBeforeRecovery)
	}
	clock.Advance(epicBranchAdmissionLeaseRenewInterval)
	waitFor(t, func() bool { return clock.BlockedRenewals() > 0 || recoveryCtx.Err() != nil }, time.Second)
	select {
	case <-recoveryCtx.Done():
		t.Fatalf("owned blocked transition canceled recovery materialization after renewal: %v", context.Cause(recoveryCtx))
	case <-time.After(25 * time.Millisecond):
	}
	if clock.BlockedRenewals() == 0 {
		t.Fatal("recovery materialization continued without observing a blocked-state renewal")
	}
	close(recoveryCreate.proceed)
	select {
	case result := <-results:
		if result.beadID != directID || result.err != nil {
			t.Fatalf("checked-out dispatcher result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("checked-out dispatcher did not finish after inspection release")
	}

	blocked := loadEpicBranchAdmissionE2E(t, db, branch)
	if blocked.state != "blocked" || blocked.blockerKind != "checked_out" || blocked.generation != leasedGeneration || blocked.recoveryBeadID == "" {
		t.Fatalf("blocked admission = %+v, want one checked_out row with stable generation and recovery", blocked)
	}
	wantRecoveryID := epicBranchRecoveryBeadID(epicBranchAdmission{
		branch: branch, epicID: epicID, targetBranch: blocked.targetBranch, generation: blocked.generation,
		blockerKind: blocked.blockerKind, checkoutPath: blocked.checkoutPath,
		branchSHA: blocked.branchSHA, targetSHA: blocked.targetSHA,
	}, "")
	if blocked.recoveryBeadID != wantRecoveryID {
		t.Fatalf("recovery ID = %q, want deterministic %q", blocked.recoveryBeadID, wantRecoveryID)
	}
	assertEpicBranchAdmissionE2ECounts(ctx, t, db, beads, epicID, wantRecoveryID, 1, 1)

	store := newEpicBranchAdmissionStore(db)
	const reclaimedToken = "reclaimed-by-another-dispatcher"
	if reclaimedToken == leasedToken {
		t.Fatal("stale-token fixture unexpectedly equals the current lease token")
	}
	if _, err := store.block(ctx, branch, reclaimedToken, blocked.generation, "diverged", "", "stale", "stale", "stale-child", "stale holder", clock.Now()); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("stale lease block error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	if got := loadEpicBranchAdmissionE2E(t, db, branch); got != blocked {
		t.Fatalf("stale token mutated admission:\ngot:  %+v\nwant: %+v", got, blocked)
	}
	if err := store.resolve(ctx, branch, blocked.generation+1, clock.Now()); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("stale generation resolve error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	if got := loadEpicBranchAdmissionE2E(t, db, branch); got != blocked {
		t.Fatalf("stale generation mutated admission:\ngot:  %+v\nwant: %+v", got, blocked)
	}

	trackedIDs := []string{directID, middleID, nestedID, titleLookalike, tagLookalike, wantRecoveryID}
	preCrash := snapshotEpicBranchAdmissionE2E(t, db, beads, manager, d1, branch, trackedIDs)
	assertEpicBranchAdmissionE2EForbiddenRetryState(t, preCrash)
	beads.mu.Lock()
	createCallsBeforeCrash := beads.createCalls
	beads.mu.Unlock()
	if _, err := db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET recovery_bead_id=NULL
WHERE branch=? AND state='blocked' AND generation=?`, branch, blocked.generation); err != nil {
		t.Fatalf("simulate crash before recovery linkage: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close durable admission DB for restart: %v", err)
	}

	restartedDB := openEpicBranchAdmissionTestDB(t, dbPath)
	t.Cleanup(func() { _ = restartedDB.Close() })
	beads.db = restartedDB
	d3, _, _, _, _, _ := newTestDispatcher(t)
	d3.db = restartedDB
	d3.beads = beads
	d3.worktrees = manager
	d3.nowFunc = clock.Now
	manager.mu.Lock()
	manager.inspection = epicBranchInspection{BranchOID: "diverged-after-restart", BaseOID: "target-after", Relation: branchDiverged}
	manager.mu.Unlock()
	if err := d3.startupRecovery(ctx); err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
	restarted := loadEpicBranchAdmissionE2E(t, restartedDB, branch)
	if restarted.state != "blocked" || restarted.generation != blocked.generation || restarted.recoveryBeadID != wantRecoveryID {
		t.Fatalf("restarted admission = %+v, want stable blocked generation and repaired link %q", restarted, wantRecoveryID)
	}
	beads.mu.Lock()
	createCallsAfterRestart := beads.createCalls
	beads.mu.Unlock()
	if createCallsAfterRestart != createCallsBeforeCrash {
		t.Fatalf("restart recovery create calls = %d, want unchanged %d", createCallsAfterRestart, createCallsBeforeCrash)
	}
	assertEpicBranchAdmissionE2ECounts(ctx, t, restartedDB, beads, epicID, wantRecoveryID, 1, 1)
	afterStartup := snapshotEpicBranchAdmissionE2E(t, restartedDB, beads, manager, d3, branch, trackedIDs)
	assertEpicBranchAdmissionE2ENoRetrySideEffects(t, afterStartup, preCrash)
	assertEpicBranchAdmissionE2EForbiddenRetryState(t, afterStartup)

	quiet := afterStartup
	for cycle := 1; cycle <= 3; cycle++ {
		ready, err := d3.readyBeadsForScheduling(ctx)
		if err != nil {
			t.Fatalf("scheduler cycle %d: %v", cycle, err)
		}
		ids := beadIDs(ready)
		for _, blockedID := range []string{directID, nestedID, titleLookalike, tagLookalike} {
			if slices.Contains(ids, blockedID) {
				t.Fatalf("scheduler cycle %d admitted blocked/lookalike bead %q: %v", cycle, blockedID, ids)
			}
		}
		for _, admittedID := range []string{wantRecoveryID, unrelatedID} {
			if !slices.Contains(ids, admittedID) {
				t.Fatalf("scheduler cycle %d omitted admitted bead %q: %v", cycle, admittedID, ids)
			}
		}
		assertEpicBranchAdmissionE2EQuiet(t, restartedDB, beads, manager, d3, branch, trackedIDs, quiet)
	}

	metrics, err := factoryhealth.LoadEpicBranchAdmissionMetrics(ctx, restartedDB, clock.Now())
	if err != nil {
		t.Fatalf("load admission health metrics: %v", err)
	}
	if metrics.Blocked != 1 || metrics.ActiveLeases != 0 {
		t.Fatalf("admission health metrics = %+v, want one block and no active lease", metrics)
	}
	var status statusResponse
	if err := json.Unmarshal([]byte(d3.buildStatusJSONWithStorage(ctx, nil)), &status); err != nil {
		t.Fatalf("decode additive status: %v", err)
	}
	if status.EpicBranchBlocksOpen != 1 || status.EpicBranchLeasesActive != 0 || status.AssignmentFrozenByQuarantine {
		t.Fatalf("status admission fields = blocks %d leases %d frozen %v, want 1/0/false",
			status.EpicBranchBlocksOpen, status.EpicBranchLeasesActive, status.AssignmentFrozenByQuarantine)
	}

	workerRecovery := &trackedWorker{id: "worker-admission-recovery", state: protocol.WorkerIdle, conn: newMockConn()}
	workerUnrelated := &trackedWorker{id: "worker-admission-unrelated", state: protocol.WorkerIdle, conn: newMockConn()}
	d3.mu.Lock()
	d3.workers[workerRecovery.id] = workerRecovery
	d3.workers[workerUnrelated.id] = workerUnrelated
	d3.targetWorkers = 2
	d3.mu.Unlock()
	d3.setState(StateRunning)
	tryAssignAndWait(t, d3, ctx)
	assigned := assignedBeadIDsSorted(t, restartedDB)
	if !slices.Contains(assigned, wantRecoveryID) || !slices.Contains(assigned, unrelatedID) {
		t.Fatalf("scheduled assignments = %v, want exact recovery and unrelated work", assigned)
	}
	for _, blockedID := range []string{directID, nestedID, titleLookalike, tagLookalike} {
		if slices.Contains(assigned, blockedID) {
			t.Fatalf("blocked/lookalike bead %q reached assignment: %v", blockedID, assigned)
		}
	}

	manager.mu.Lock()
	manager.inspection = epicBranchInspection{BranchOID: "diverged-after-restart", BaseOID: "target-after", Relation: branchDiverged}
	manager.mu.Unlock()
	inspection, err := manager.inspectEpicBranch(ctx, branch, restarted.targetBranch)
	if err != nil || inspection.Relation != branchDiverged {
		t.Fatalf("fresh diverged inspection = %+v err %v", inspection, err)
	}
	if got := loadEpicBranchAdmissionE2E(t, restartedDB, branch); got.state != "blocked" {
		t.Fatalf("diverged inspection changed admission state to %q", got.state)
	}

	manager.mu.Lock()
	manager.inspection = epicBranchInspection{BranchOID: "safe-sha", BaseOID: "safe-sha", Relation: branchSame}
	manager.mu.Unlock()
	inspection, err = manager.inspectEpicBranch(ctx, branch, restarted.targetBranch)
	if err != nil || inspection.Relation != branchSame || inspection.BranchOID != inspection.BaseOID {
		t.Fatalf("fresh safe inspection = %+v err %v", inspection, err)
	}
	if err := newEpicBranchAdmissionStore(restartedDB).resolve(ctx, branch, restarted.generation, clock.Now()); err != nil {
		t.Fatalf("resolve after fresh safe inspection: %v", err)
	}
	readyAfterResolve, err := d3.readyBeadsForScheduling(ctx)
	if err != nil {
		t.Fatalf("ready work after safe resolution: %v", err)
	}
	resolvedIDs := beadIDs(readyAfterResolve)
	for _, readmittedID := range []string{directID, nestedID, titleLookalike, tagLookalike} {
		if !slices.Contains(resolvedIDs, readmittedID) {
			t.Fatalf("safe resolution did not re-admit %q: %v", readmittedID, resolvedIDs)
		}
	}
}

type epicBranchAdmissionE2EClock struct {
	mu                    sync.Mutex
	now                   time.Time
	recoveryBarrierActive bool
	blockedRenewals       int
}

type epicBranchAdmissionE2ERecoveryBarrier struct {
	*observingEpicRecoveryStore
	clock   *epicBranchAdmissionE2EClock
	started chan context.Context
	proceed chan struct{}
	once    sync.Once
}

type epicBranchAdmissionE2EManager struct {
	*admissionTestWorktreeManager
	blockedBranch string
}

func (m *epicBranchAdmissionE2EManager) inspectEpicBranch(
	ctx context.Context,
	branch, targetBranch string,
) (epicBranchInspection, error) {
	if branch != m.blockedBranch {
		return epicBranchInspection{BranchOID: "safe-" + branch, BaseOID: "safe-" + branch, Relation: branchSame}, nil
	}
	return m.admissionTestWorktreeManager.inspectEpicBranch(ctx, branch, targetBranch)
}

func (c *epicBranchAdmissionE2EClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.recoveryBarrierActive {
		c.blockedRenewals++
	}
	return c.now
}

func (c *epicBranchAdmissionE2EClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func (c *epicBranchAdmissionE2EClock) BlockedRenewals() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blockedRenewals
}

func (c *epicBranchAdmissionE2EClock) StartRecoveryBarrier() {
	c.mu.Lock()
	c.recoveryBarrierActive = true
	c.mu.Unlock()
}

func (s *epicBranchAdmissionE2ERecoveryBarrier) Create(
	ctx context.Context,
	params beadstore.CreateParams,
) (*protocol.Bead, error) {
	if params.Metadata["epic_branch_recovery_branch"] != "" {
		s.once.Do(func() {
			s.clock.StartRecoveryBarrier()
			s.started <- ctx
			<-s.proceed
		})
	}
	return s.observingEpicRecoveryStore.Create(ctx, params)
}

type epicBranchAdmissionE2ESnapshot struct {
	admission       epicBranchAdmission
	createCalls     int
	children        []string
	dependencies    []string
	events          map[string]int
	inspectionCalls int
	attempts        map[string]int
	cooldowns       map[string]time.Time
	assignments     map[string]int
}

func snapshotEpicBranchAdmissionE2E(
	t *testing.T,
	db *sql.DB,
	beads *observingEpicRecoveryStore,
	manager *epicBranchAdmissionE2EManager,
	d *Dispatcher,
	branch string,
	trackedIDs []string,
) epicBranchAdmissionE2ESnapshot {
	t.Helper()
	beads.mu.Lock()
	createCalls := beads.createCalls
	beads.mu.Unlock()
	manager.mu.Lock()
	inspectionCalls := manager.inspectionCalls
	manager.mu.Unlock()
	admission := loadEpicBranchAdmissionE2E(t, db, branch)
	epic, err := beads.Show(context.Background(), admission.epicID)
	if err != nil || epic == nil {
		t.Fatalf("load epic dependencies: epic=%+v err=%v", epic, err)
	}
	dependencies := make([]string, 0, len(epic.Dependencies))
	for _, dependency := range epic.Dependencies {
		dependencies = append(dependencies, dependency.DependsOnID+"\x00"+dependency.Type)
	}
	slices.Sort(dependencies)
	children, err := beads.FindByParentAndTag(context.Background(), admission.epicID, epicBranchRecoveryTag)
	if err != nil {
		t.Fatalf("load epic recovery children: %v", err)
	}
	childState := make([]string, 0, len(children))
	for _, child := range children {
		childState = append(childState, child.ID+"\x00"+child.Status)
	}
	slices.Sort(childState)
	events := epicBranchAdmissionE2EEventCounts(t, db)
	tracked := make(map[string]bool, len(trackedIDs))
	for _, beadID := range trackedIDs {
		tracked[beadID] = true
	}
	d.mu.Lock()
	attempts := make(map[string]int)
	cooldowns := make(map[string]time.Time)
	for _, beadID := range trackedIDs {
		if attempt, ok := d.attemptCounts[beadID]; ok {
			attempts[beadID] = attempt
		}
		if cooldown, ok := d.worktreeFailures[beadID]; ok {
			cooldowns[beadID] = cooldown
		}
	}
	d.mu.Unlock()
	assignments := make(map[string]int)
	rows, err := db.Query(`SELECT bead_id, status FROM assignments`)
	if err != nil {
		t.Fatalf("load tracked assignments: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var beadID, status string
		if err := rows.Scan(&beadID, &status); err != nil {
			t.Fatalf("scan tracked assignment: %v", err)
		}
		if tracked[beadID] {
			assignments[beadID+"\x00"+status]++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tracked assignments: %v", err)
	}
	return epicBranchAdmissionE2ESnapshot{
		admission: admission, createCalls: createCalls,
		children: childState, dependencies: dependencies, events: events,
		inspectionCalls: inspectionCalls, attempts: attempts, cooldowns: cooldowns, assignments: assignments,
	}
}

func assertEpicBranchAdmissionE2EQuiet(
	t *testing.T,
	db *sql.DB,
	beads *observingEpicRecoveryStore,
	manager *epicBranchAdmissionE2EManager,
	d *Dispatcher,
	branch string,
	trackedIDs []string,
	want epicBranchAdmissionE2ESnapshot,
) {
	t.Helper()
	got := snapshotEpicBranchAdmissionE2E(t, db, beads, manager, d, branch, trackedIDs)
	if got.admission != want.admission {
		t.Fatalf("quiet scheduler state changed:\ngot:  %+v\nwant: %+v", got, want)
	}
	assertEpicBranchAdmissionE2ENoRetrySideEffects(t, got, want)
	assertEpicBranchAdmissionE2EForbiddenRetryState(t, got)
}

func assertEpicBranchAdmissionE2ENoRetrySideEffects(
	t *testing.T,
	got, want epicBranchAdmissionE2ESnapshot,
) {
	t.Helper()
	if got.createCalls != want.createCalls || !slices.Equal(got.children, want.children) ||
		!slices.Equal(got.dependencies, want.dependencies) || !maps.Equal(got.events, want.events) ||
		got.inspectionCalls != want.inspectionCalls || !maps.Equal(got.attempts, want.attempts) ||
		!maps.Equal(got.cooldowns, want.cooldowns) || !maps.Equal(got.assignments, want.assignments) {
		t.Fatalf("retry side effects changed:\ngot:  %+v\nwant: %+v", got, want)
	}
}

func assertEpicBranchAdmissionE2EForbiddenRetryState(t *testing.T, got epicBranchAdmissionE2ESnapshot) {
	t.Helper()
	for eventType, count := range got.events {
		if count != 0 {
			t.Fatalf("forbidden retry event %s count = %d, want 0", eventType, count)
		}
	}
	if len(got.attempts) != 0 || len(got.cooldowns) != 0 || len(got.assignments) != 0 {
		t.Fatalf("forbidden retry state = attempts %v cooldowns %v assignments %v, want all absent",
			got.attempts, got.cooldowns, got.assignments)
	}
}

func epicBranchAdmissionE2EEventCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	events := map[string]int{
		"assignment_persist_failed":          0,
		"assignment_race_detected":           0,
		"epic_branch_admission_block_failed": 0,
		"epic_branch_admission_renew_failed": 0,
		"epic_branch_missing":                0,
		"epic_branch_prepare_failed":         0,
		"epic_branch_recovery_ensure_failed": 0,
	}
	for eventType := range events {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=?`, eventType).Scan(&count); err != nil {
			t.Fatalf("count forbidden retry event %s: %v", eventType, err)
		}
		events[eventType] = count
	}
	return events
}

func createEpicBranchAdmissionE2EBead(t *testing.T, beads *observingEpicRecoveryStore, params beadstore.CreateParams) {
	t.Helper()
	if _, err := beads.Create(context.Background(), params); err != nil {
		t.Fatalf("create %s: %v", params.ID, err)
	}
}

func loadEpicBranchAdmissionE2E(t *testing.T, db *sql.DB, branch string) epicBranchAdmission {
	t.Helper()
	admission, err := loadEpicBranchAdmission(context.Background(), db, branch)
	if err != nil {
		t.Fatalf("load admission %s: %v", branch, err)
	}
	return admission
}

func assertEpicBranchAdmissionE2EExpiry(t *testing.T, text string, want time.Time) {
	t.Helper()
	got, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatalf("parse lease expiry %q: %v", text, err)
	}
	if !got.Equal(want) {
		t.Fatalf("lease expiry = %s, want %s", got, want)
	}
}

func assertEpicBranchAdmissionE2ECounts(
	ctx context.Context,
	t *testing.T,
	db *sql.DB,
	beads *observingEpicRecoveryStore,
	epicID, recoveryID string,
	wantRows, wantChildren int,
) {
	t.Helper()
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM epic_branch_admissions WHERE epic_id=?`, epicID).Scan(&rows); err != nil {
		t.Fatalf("count admission rows: %v", err)
	}
	if rows != wantRows {
		t.Fatalf("admission rows = %d, want %d", rows, wantRows)
	}
	var links int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM epic_branch_admissions
WHERE epic_id=? AND recovery_bead_id=?`, epicID, recoveryID).Scan(&links); err != nil {
		t.Fatalf("count admission recovery links: %v", err)
	}
	if links != wantChildren {
		t.Fatalf("admission recovery links = %d, want %d exact link to %q", links, wantChildren, recoveryID)
	}
	children, err := beads.FindByParentAndTag(ctx, epicID, epicBranchRecoveryTag)
	if err != nil {
		t.Fatalf("find recovery children: %v", err)
	}
	canonicalChildren := 0
	for _, child := range children {
		if child.ID == recoveryID {
			canonicalChildren++
		}
	}
	if canonicalChildren != wantChildren {
		t.Fatalf("recovery children = %+v, want %d exact canonical child %q", children, wantChildren, recoveryID)
	}
}
