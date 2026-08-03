package dispatcher //nolint:testpackage // recovery tests exercise dispatcher-internal durable state.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

const epicBranchRecoveryTag = "epic-branch-recovery"

type observingEpicRecoveryStore struct {
	*beadstore.FakeStore
	db *sql.DB

	mu                          sync.Mutex
	createCalls                 int
	updateCalls                 map[string]int
	blockedBeforeRecoveryCreate bool
	afterDependency             func()
	afterDependencyOnce         sync.Once
}

func (s *observingEpicRecoveryStore) Create(ctx context.Context, params beadstore.CreateParams) (*protocol.Bead, error) {
	var state string
	var recoveryID sql.NullString
	blockedBeforeCreate := s.db.QueryRowContext(ctx, `
SELECT state, recovery_bead_id
FROM epic_branch_admissions
WHERE branch=?`, params.Metadata["epic_branch_recovery_branch"]).Scan(&state, &recoveryID) == nil &&
		state == "blocked" && !recoveryID.Valid
	s.mu.Lock()
	s.createCalls++
	s.blockedBeforeRecoveryCreate = s.blockedBeforeRecoveryCreate || blockedBeforeCreate
	s.mu.Unlock()
	return s.FakeStore.Create(ctx, params)
}

func (s *observingEpicRecoveryStore) Update(ctx context.Context, id string, params beadstore.UpdateParams) error {
	s.mu.Lock()
	if s.updateCalls == nil {
		s.updateCalls = make(map[string]int)
	}
	s.updateCalls[id]++
	s.mu.Unlock()
	return s.FakeStore.Update(ctx, id, params)
}

func (s *observingEpicRecoveryStore) AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error {
	if err := s.FakeStore.AddDependency(ctx, beadID, dependsOnID, depType); err != nil {
		return err
	}
	s.afterDependencyOnce.Do(func() {
		if s.afterDependency != nil {
			s.afterDependency()
		}
	})
	return nil
}

func TestEpicBranchBlockCreatesOneCrashSafeCanonicalRecoveryChild(t *testing.T) {
	t.Run("scheduler remains compatible before admission migration", func(t *testing.T) {
		ctx := context.Background()
		d, _, _, _, _, _ := newTestDispatcher(t)
		beads := beadstore.NewFakeStore(protocol.Bead{
			ID: "oro-pre-migration-ready", Title: "Pre-migration ready", Type: "task", Status: "open",
			AcceptanceCriteria: "Test: pre-migration scheduling | Assert: ready bead remains visible",
		})
		d.beads = beads
		ready, err := d.readyBeadsForScheduling(ctx)
		if err != nil {
			t.Fatalf("ready beads before admission migration: %v", err)
		}
		if len(ready) != 1 || ready[0].ID != "oro-pre-migration-ready" {
			t.Fatalf("pre-migration ready beads = %+v, want one ready bead", ready)
		}
	})

	t.Run("checked out and diverged pipeline blocks materialize one canonical child", func(t *testing.T) {
		for _, tt := range []struct {
			name       string
			blocker    string
			inspection epicBranchInspection
		}{
			{
				name: "checked out", blocker: "checked_out",
				inspection: epicBranchInspection{
					BranchOID: "checked-branch", BaseOID: "target-sha", Relation: branchContainsBase,
					CheckedOutPaths: []string{"/tmp/epic-recovery"},
				},
			},
			{
				name: "diverged", blocker: "diverged",
				inspection: epicBranchInspection{BranchOID: "diverged-branch", BaseOID: "target-sha", Relation: branchDiverged},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				ctx := context.Background()
				d, beads, manager, worker, epicID, beadID, branch := newEpicRecoveryPipeline(t, tt.inspection)
				bead := protocol.Bead{ID: beadID, Title: "Blocked child", Type: "task", Epic: epicID}

				if err := d.assignBead(ctx, worker, bead); err != nil {
					t.Fatalf("assign unsafe epic child: %v", err)
				}
				admission := loadRecoveryAdmission(t, d, branch)
				if admission.state != "blocked" || admission.blockerKind != tt.blocker || admission.recoveryBeadID == "" {
					t.Fatalf("blocked admission = state %q blocker %q recovery %q, want blocked/%s/nonempty",
						admission.state, admission.blockerKind, admission.recoveryBeadID, tt.blocker)
				}
				recovery := assertCanonicalEpicRecovery(ctx, t, d, beads, admission)
				beads.mu.Lock()
				blockedBeforeCreate := beads.blockedBeforeRecoveryCreate
				createCalls := beads.createCalls
				beads.mu.Unlock()
				if !blockedBeforeCreate {
					t.Fatal("recovery child was created before the durable blocked row")
				}

				if err := d.assignBead(ctx, worker, bead); err != nil {
					t.Fatalf("repeat blocked assignment: %v", err)
				}
				children, err := beads.FindByParentAndTag(ctx, epicID, epicBranchRecoveryTag)
				if err != nil {
					t.Fatalf("find recovery children: %v", err)
				}
				if len(children) != 1 || children[0].ID != recovery.ID {
					t.Fatalf("repeated repair children = %+v, want only %q", children, recovery.ID)
				}
				beads.mu.Lock()
				repeatedCreateCalls := beads.createCalls
				beads.mu.Unlock()
				if repeatedCreateCalls != createCalls {
					t.Fatalf("repeated repair create calls = %d, want %d", repeatedCreateCalls, createCalls)
				}
				manager.mu.Lock()
				inspectionCalls := manager.inspectionCalls
				manager.mu.Unlock()
				if inspectionCalls != 1 {
					t.Fatalf("Git inspections while blocked = %d, want 1", inspectionCalls)
				}
			})
		}
	})

	t.Run("restart repairs a blocked row with null recovery linkage", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, _, epicID, _, branch := newEpicRecoveryPipeline(t, epicBranchInspection{})
		admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "diverged")
		if admission.recoveryBeadID != "" {
			t.Fatalf("seeded recovery link = %q, want null", admission.recoveryBeadID)
		}

		if err := d.startupRecovery(ctx); err != nil {
			t.Fatalf("startup recovery: %v", err)
		}
		linked := loadRecoveryAdmission(t, d, branch)
		assertCanonicalEpicRecovery(ctx, t, d, beads, linked)
		if err := d.startupRecovery(ctx); err != nil {
			t.Fatalf("repeat startup recovery: %v", err)
		}
		children, err := beads.FindByParentAndTag(ctx, epicID, epicBranchRecoveryTag)
		if err != nil {
			t.Fatalf("find restart recovery children: %v", err)
		}
		if len(children) != 1 || children[0].ID != linked.recoveryBeadID {
			t.Fatalf("restart repair children = %+v, want only %q", children, linked.recoveryBeadID)
		}
	})

	t.Run("shadow mode forwards the dependency and restart repair stays healthy", func(t *testing.T) {
		ctx := context.Background()
		d, primary, manager, worker, epicID, beadID, branch := newEpicRecoveryPipeline(t, epicBranchInspection{
			BranchOID: "shadow-diverged", BaseOID: "shadow-target", Relation: branchDiverged,
		})
		shadow, err := selectStore(ctx, "shadow", primary, d.db)
		if err != nil {
			t.Fatalf("select shadow store: %v", err)
		}
		d.beads = shadow
		bead := protocol.Bead{ID: beadID, Title: "Shadow blocked child", Type: "task", Epic: epicID}
		if err := d.assignBead(ctx, worker, bead); err != nil {
			t.Fatalf("assign shadow blocked child: %v", err)
		}
		admission := loadRecoveryAdmission(t, d, branch)
		if admission.recoveryBeadID == "" {
			t.Fatal("shadow recovery link is empty")
		}
		assertCanonicalEpicRecovery(ctx, t, d, primary, admission)
		if err := d.startupRecovery(ctx); err != nil {
			t.Fatalf("restart shadow recovery: %v", err)
		}
		manager.mu.Lock()
		inspectionCalls := manager.inspectionCalls
		manager.mu.Unlock()
		if inspectionCalls != 1 {
			t.Fatalf("shadow restart Git inspections = %d, want 1", inspectionCalls)
		}
	})

	t.Run("scheduler offers only the linked recovery while siblings stay unchanged", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, worker, epicID, beadID, branch := newEpicRecoveryPipeline(t, epicBranchInspection{
			BranchOID: "scheduler-diverged", BaseOID: "scheduler-target", Relation: branchDiverged,
		})
		if err := d.assignBead(ctx, worker, protocol.Bead{ID: beadID, Title: "Initial blocked child", Type: "task", Epic: epicID}); err != nil {
			t.Fatalf("establish blocked admission: %v", err)
		}
		admission := loadRecoveryAdmission(t, d, branch)
		const siblingID = "oro-aaa-blocked-sibling"
		if _, err := beads.Create(ctx, beadstore.CreateParams{
			ID: siblingID, Title: "Earlier blocked sibling", Type: "task", Priority: 0, ParentID: epicID,
			AcceptanceCriteria: "Test: blocked sibling stays out of the scheduler | Assert: no status churn",
		}); err != nil {
			t.Fatalf("create blocked sibling: %v", err)
		}
		beads.mu.Lock()
		siblingUpdates := beads.updateCalls[siblingID]
		beads.mu.Unlock()
		d.setState(StateRunning)

		tryAssignAndWait(t, d, ctx)
		d.mu.Lock()
		gotState, gotBeadID := worker.state, worker.beadID
		d.mu.Unlock()
		if gotState != protocol.WorkerBusy || gotBeadID != admission.recoveryBeadID {
			t.Fatalf("scheduled worker = state %q bead %q, want busy recovery %q", gotState, gotBeadID, admission.recoveryBeadID)
		}
		for range 3 {
			tryAssignAndWait(t, d, ctx)
		}
		beads.mu.Lock()
		gotSiblingUpdates := beads.updateCalls[siblingID]
		beads.mu.Unlock()
		if gotSiblingUpdates != siblingUpdates {
			t.Fatalf("blocked sibling status updates = %d, want unchanged %d", gotSiblingUpdates, siblingUpdates)
		}
	})

	t.Run("nested recovery bypass applies only to its own blocked ancestor", func(t *testing.T) {
		ctx := context.Background()
		d, _, _, _, _, _ := newTestDispatcher(t)
		if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
			t.Fatalf("migrate nested recovery schema: %v", err)
		}
		const outerEpic = "oro-nested-outer"
		const innerEpic = "oro-nested-inner"
		beads := beadstore.NewFakeStore(
			protocol.Bead{ID: outerEpic, Title: "Outer", Type: "epic", Status: "in_progress"},
			protocol.Bead{ID: innerEpic, Title: "Inner", Type: "epic", Status: "in_progress", Epic: outerEpic},
		)
		d.beads = beads
		outer := seedBlockedRecoveryAdmission(ctx, t, d, outerEpic, protocol.EpicBranchPrefix+outerEpic, "diverged")
		outerRecovery, err := d.ensureEpicBranchBlockRecovery(ctx, outer)
		if err != nil {
			t.Fatalf("ensure outer recovery: %v", err)
		}
		inner := seedBlockedRecoveryAdmission(ctx, t, d, innerEpic, protocol.EpicBranchPrefix+innerEpic, "checked_out")
		innerRecovery, err := d.ensureEpicBranchBlockRecovery(ctx, inner)
		if err != nil {
			t.Fatalf("ensure inner recovery: %v", err)
		}
		ready, err := d.readyBeadsForScheduling(ctx)
		if err != nil {
			t.Fatalf("filter nested recovery readiness: %v", err)
		}
		if !slices.ContainsFunc(ready, func(bead protocol.Bead) bool { return bead.ID == outerRecovery.ID }) {
			t.Fatalf("outer recovery %q is not schedulable: %+v", outerRecovery.ID, ready)
		}
		if slices.ContainsFunc(ready, func(bead protocol.Bead) bool { return bead.ID == innerRecovery.ID }) {
			t.Fatalf("inner recovery %q bypassed blocked outer ancestor: %+v", innerRecovery.ID, ready)
		}
	})

	t.Run("generation race retires unlinked side effects and restart creates one canonical child", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, _, epicID, _, branch := newEpicRecoveryPipeline(t, epicBranchInspection{})
		admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "diverged")
		candidateID := epicBranchRecoveryBeadID(admission, "")
		beads.afterDependency = func() {
			if _, err := d.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET generation=generation+1, recovery_bead_id=NULL, updated_at=?
WHERE branch=? AND state='blocked' AND generation=?`,
				formatEpicBranchAdmissionTime(d.nowFunc()), branch, admission.generation); err != nil {
				t.Errorf("advance admission generation during dependency insert: %v", err)
			}
		}
		if _, err := d.ensureEpicBranchBlockRecovery(ctx, admission); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
			t.Fatalf("generation-raced ensure error = %v, want admission CAS", err)
		}
		candidate, err := beads.Show(ctx, candidateID)
		if err != nil {
			t.Fatalf("show raced candidate: %v", err)
		}
		if candidate != nil && (candidate.Status == "open" || candidate.Status == "in_progress") {
			t.Fatalf("raced candidate remains active: %+v", candidate)
		}
		assertNoEpicRecoveryDependency(ctx, t, beads.FakeStore, epicID, candidateID)

		if err := d.startupRecovery(ctx); err != nil {
			t.Fatalf("restart after generation race: %v", err)
		}
		current := loadRecoveryAdmission(t, d, branch)
		if current.generation != admission.generation+1 || current.recoveryBeadID == "" || current.recoveryBeadID == candidateID {
			t.Fatalf("restarted admission = generation %d recovery %q, want next generation new recovery",
				current.generation, current.recoveryBeadID)
		}
		assertCanonicalEpicRecovery(ctx, t, d, beads, current)
	})

	t.Run("live noncanonical predecessor is durably retired after successor repair", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, _, epicID, _, branch := newEpicRecoveryPipeline(t, epicBranchInspection{})
		admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "diverged")
		first, err := d.ensureEpicBranchBlockRecovery(ctx, admission)
		if err != nil {
			t.Fatalf("materialize predecessor: %v", err)
		}
		mutatedTitle := first.Title + " mutated"
		if err := beads.Update(ctx, first.ID, beadstore.UpdateParams{Title: &mutatedTitle}); err != nil {
			t.Fatalf("mutate linked predecessor: %v", err)
		}
		admission = loadRecoveryAdmission(t, d, branch)
		successor, err := d.ensureEpicBranchBlockRecovery(ctx, admission)
		if err != nil {
			t.Fatalf("repair noncanonical predecessor: %v", err)
		}
		if successor.ID == first.ID {
			t.Fatalf("successor reused noncanonical predecessor %q", first.ID)
		}
		predecessor, err := beads.Show(ctx, first.ID)
		if err != nil {
			t.Fatalf("show retired predecessor: %v", err)
		}
		if predecessor != nil && (predecessor.Status == "open" || predecessor.Status == "in_progress") {
			t.Fatalf("noncanonical predecessor remains active: %+v", predecessor)
		}
		assertNoEpicRecoveryDependency(ctx, t, beads.FakeStore, epicID, first.ID)
		assertCanonicalEpicRecovery(ctx, t, d, beads, loadRecoveryAdmission(t, d, branch))
		if err := d.startupRecovery(ctx); err != nil {
			t.Fatalf("restart after predecessor cleanup: %v", err)
		}
		assertNoEpicRecoveryDependency(ctx, t, beads.FakeStore, epicID, first.ID)
	})

	t.Run("missing parent fails closed before assignment status churn", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, worker, epicID, _, branch := newEpicRecoveryPipeline(t, epicBranchInspection{})
		admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "diverged")
		if _, err := d.ensureEpicBranchBlockRecovery(ctx, admission); err != nil {
			t.Fatalf("ensure blocker for missing-parent filter: %v", err)
		}
		const beadID = "oro-missing-parent-child"
		if _, err := beads.Create(ctx, beadstore.CreateParams{
			ID: beadID, Title: "Missing parent child", Type: "task", ParentID: "oro-parent-does-not-exist",
			AcceptanceCriteria: "Test: missing ancestry fails closed | Assert: no assignment status churn",
		}); err != nil {
			t.Fatalf("create missing-parent child: %v", err)
		}
		if _, err := d.readyBeadsForScheduling(ctx); err == nil {
			t.Fatal("missing parent ancestry returned no scheduling error")
		}
		d.setState(StateRunning)
		tryAssignAndWait(t, d, ctx)
		d.mu.Lock()
		state, assigned := worker.state, worker.beadID
		d.mu.Unlock()
		beads.mu.Lock()
		updates := beads.updateCalls[beadID]
		beads.mu.Unlock()
		if state != protocol.WorkerIdle || assigned != "" || updates != 0 {
			t.Fatalf("missing-parent scheduling = state %q bead %q updates %d, want idle/empty/0", state, assigned, updates)
		}
	})

	t.Run("concurrent repair reuses exact bead and dependency", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, _, epicID, _, branch := newEpicRecoveryPipeline(t, epicBranchInspection{})
		admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "checked_out")
		const repairs = 12
		children := make(chan *protocol.Bead, repairs)
		errs := make(chan error, repairs)
		var wg sync.WaitGroup
		for range repairs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				child, err := d.ensureEpicBranchBlockRecovery(ctx, admission)
				children <- child
				errs <- err
			}()
		}
		wg.Wait()
		close(children)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent repair: %v", err)
			}
		}
		linked := loadRecoveryAdmission(t, d, branch)
		for child := range children {
			if child == nil || child.ID != linked.recoveryBeadID {
				t.Fatalf("concurrent child = %+v, want %q", child, linked.recoveryBeadID)
			}
		}
		assertCanonicalEpicRecovery(ctx, t, d, beads, linked)
		matches, err := beads.FindByParentAndTag(ctx, epicID, epicBranchRecoveryTag)
		if err != nil {
			t.Fatalf("find concurrent children: %v", err)
		}
		if len(matches) != 1 {
			t.Fatalf("concurrent repair created %d children, want 1", len(matches))
		}
	})

	t.Run("lookalikes never qualify and only the linked child bypasses the block", func(t *testing.T) {
		ctx := context.Background()
		d, beads, _, worker, epicID, beadID, branch := newEpicRecoveryPipeline(t,
			epicBranchInspection{BranchOID: "diverged", BaseOID: "target", Relation: branchDiverged})
		if err := d.assignBead(ctx, worker, protocol.Bead{ID: beadID, Title: "Blocked child", Type: "task", Epic: epicID}); err != nil {
			t.Fatalf("create durable blocker: %v", err)
		}
		admission := loadRecoveryAdmission(t, d, branch)
		recovery := assertCanonicalEpicRecovery(ctx, t, d, beads, admission)
		if !isExactEpicBranchRecoveryChild(recovery, admission) {
			t.Fatal("stored canonical recovery child was not exact")
		}
		for _, tc := range []struct {
			name string
			fn   func(*protocol.Bead)
		}{
			{name: "different id", fn: func(child *protocol.Bead) { child.ID += "-lookalike" }},
			{name: "title", fn: func(child *protocol.Bead) { child.Title += " lookalike" }},
			{name: "tag", fn: func(child *protocol.Bead) { child.Tags = nil }},
			{name: "branch", fn: func(child *protocol.Bead) { child.Metadata["epic_branch_recovery_branch"] = "epic/other" }},
			{name: "generation", fn: func(child *protocol.Bead) { child.Metadata["epic_branch_recovery_generation"] = "2" }},
			{name: "closed", fn: func(child *protocol.Bead) { child.Status = "closed" }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				lookalike := cloneRecoveryBead(recovery)
				tc.fn(&lookalike)
				if isExactEpicBranchRecoveryChild(&lookalike, admission) {
					t.Fatalf("lookalike qualified as exact: %+v", lookalike)
				}
			})
		}

		lookalike := cloneRecoveryBead(recovery)
		lookalike.ID += "-lookalike"
		if _, err := beads.Create(ctx, recoveryCreateParams(lookalike)); err != nil {
			t.Fatalf("create linked-child lookalike: %v", err)
		}
		if err := d.assignBead(ctx, worker, lookalike); err != nil {
			t.Fatalf("assign lookalike: %v", err)
		}
		if got := activeAssignmentCount(t, d, lookalike.ID); got != 0 {
			t.Fatalf("lookalike active assignments = %d, want 0", got)
		}
		if err := d.assignBead(ctx, worker, *recovery); err != nil {
			t.Fatalf("assign linked recovery child: %v", err)
		}
		if got := activeAssignmentCount(t, d, recovery.ID); got != 1 {
			t.Fatalf("linked recovery active assignments = %d, want 1", got)
		}
	})

	for _, removal := range []string{"closed", "deleted"} {
		t.Run(removal+" linked child repairs once", func(t *testing.T) {
			ctx := context.Background()
			d, beads, _, _, epicID, _, branch := newEpicRecoveryPipeline(t, epicBranchInspection{})
			admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "diverged")
			first, err := d.ensureEpicBranchBlockRecovery(ctx, admission)
			if err != nil {
				t.Fatalf("initial repair: %v", err)
			}
			linked := loadRecoveryAdmission(t, d, branch)
			switch removal {
			case "closed":
				if err := beads.Close(ctx, first.ID, "operator closed while branch remained blocked"); err != nil {
					t.Fatalf("close recovery child: %v", err)
				}
			case "deleted":
				if err := beads.Delete(ctx, first.ID, "operator deleted while branch remained blocked"); err != nil {
					t.Fatalf("delete recovery child: %v", err)
				}
			}
			replacement, err := d.ensureEpicBranchBlockRecovery(ctx, linked)
			if err != nil {
				t.Fatalf("repair removed child: %v", err)
			}
			if replacement.ID == first.ID {
				t.Fatalf("replacement ID = removed child %q", first.ID)
			}
			stable, err := d.ensureEpicBranchBlockRecovery(ctx, loadRecoveryAdmission(t, d, branch))
			if err != nil {
				t.Fatalf("repeat replacement repair: %v", err)
			}
			if stable.ID != replacement.ID {
				t.Fatalf("repeat repair child = %q, want stable %q", stable.ID, replacement.ID)
			}
			assertCanonicalEpicRecovery(ctx, t, d, beads, loadRecoveryAdmission(t, d, branch))
			children, err := beads.FindByParentAndTag(ctx, epicID, epicBranchRecoveryTag)
			if err != nil {
				t.Fatalf("find repaired children: %v", err)
			}
			active := 0
			for i := range children {
				if children[i].Status == "open" || children[i].Status == "in_progress" {
					active++
				}
			}
			if active != 1 {
				t.Fatalf("active repaired children = %d, want 1", active)
			}
		})
	}

	t.Run("sqlite soft deletion advances one deterministic successor", func(t *testing.T) {
		ctx := context.Background()
		d, _, _, _, _, _ := newTestDispatcher(t)
		if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
			t.Fatalf("migrate sqlite recovery schema: %v", err)
		}
		const (
			epicID = "oro-recovery-sqlite-delete"
			branch = protocol.EpicBranchPrefix + epicID
		)
		store := beadstore.NewSQLiteStore(d.db)
		if _, err := store.Create(ctx, beadstore.CreateParams{ID: epicID, Title: "SQLite recovery epic", Type: "epic", Status: "in_progress"}); err != nil {
			t.Fatalf("create sqlite recovery epic: %v", err)
		}
		d.beads = store
		admission := seedBlockedRecoveryAdmission(ctx, t, d, epicID, branch, "diverged")
		first, err := d.ensureEpicBranchBlockRecovery(ctx, admission)
		if err != nil {
			t.Fatalf("materialize sqlite recovery: %v", err)
		}
		linked := loadRecoveryAdmission(t, d, branch)
		if err := store.Delete(ctx, first.ID, "test soft deletion"); err != nil {
			t.Fatalf("soft-delete sqlite recovery: %v", err)
		}
		replacement, err := d.ensureEpicBranchBlockRecovery(ctx, linked)
		if err != nil {
			t.Fatalf("repair sqlite soft deletion: %v", err)
		}
		if replacement.ID == first.ID {
			t.Fatalf("sqlite replacement reused soft-deleted ID %q", first.ID)
		}
		if deleted, err := store.Show(ctx, first.ID); err != nil || deleted != nil {
			t.Fatalf("soft-deleted child remains visible: child=%+v err=%v", deleted, err)
		}
		stable, err := d.ensureEpicBranchBlockRecovery(ctx, loadRecoveryAdmission(t, d, branch))
		if err != nil {
			t.Fatalf("repeat sqlite recovery repair: %v", err)
		}
		if stable.ID != replacement.ID {
			t.Fatalf("sqlite repeat repair child = %q, want %q", stable.ID, replacement.ID)
		}
		var dependencyEvents int
		if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM events
WHERE type='bead_dependency_added' AND bead_id=?`, epicID).Scan(&dependencyEvents); err != nil {
			t.Fatalf("count sqlite dependency events: %v", err)
		}
		if dependencyEvents != 2 {
			t.Fatalf("sqlite dependency events = %d, want 2 distinct edges", dependencyEvents)
		}
		var active int
		if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM beads b
JOIN bead_tags t ON t.bead_id=b.id AND t.tag=?
WHERE b.parent_id=? AND b.deleted=0 AND b.status IN ('open','in_progress')`,
			epicBranchRecoveryTag, epicID).Scan(&active); err != nil {
			t.Fatalf("count active sqlite recoveries: %v", err)
		}
		if active != 1 {
			t.Fatalf("active sqlite recoveries = %d, want 1", active)
		}
	})
}

func newEpicRecoveryPipeline(
	t *testing.T,
	inspection epicBranchInspection,
) (*Dispatcher, *observingEpicRecoveryStore, *admissionTestWorktreeManager, *trackedWorker, string, string, string) {
	t.Helper()
	ctx := context.Background()
	d, _, baseWorktrees, _, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	epicID := "oro-recovery-" + inspection.BranchOID + inspection.BaseOID
	if epicID == "oro-recovery-" {
		epicID = "oro-recovery-seeded"
	}
	beadID := epicID + "-child"
	branch := protocol.EpicBranchPrefix + epicID
	store := &observingEpicRecoveryStore{
		FakeStore: beadstore.NewFakeStore(
			protocol.Bead{ID: epicID, Title: "Recovery epic", Type: "epic", Status: "in_progress", Tier: protocol.TierDeep},
			protocol.Bead{
				ID: beadID, Title: "Blocked child", Type: "task", Status: "open", Epic: epicID,
				AcceptanceCriteria: "Test: blocked child | Assert: recovery materialized",
			},
		),
		db: d.db,
	}
	d.beads = store
	continueInspection := make(chan struct{})
	close(continueInspection)
	manager := &admissionTestWorktreeManager{
		mockWorktreeManager: baseWorktrees,
		inspection:          inspection,
		inspectionStarted:   make(chan struct{}),
		continueInspection:  continueInspection,
	}
	d.worktrees = manager
	worker := &trackedWorker{id: "worker-" + beadID, state: protocol.WorkerIdle, conn: newMockConn()}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	return d, store, manager, worker, epicID, beadID, branch
}

func seedBlockedRecoveryAdmission(
	ctx context.Context,
	t *testing.T,
	d *Dispatcher,
	epicID, branch, blockerKind string,
) epicBranchAdmission {
	t.Helper()
	store := newEpicBranchAdmissionStore(d.db)
	lease, acquired, err := store.acquire(ctx, branch, epicID, d.cfg.DefaultBranch, "seed-token", "seed-worker", d.nowFunc())
	if err != nil || !acquired {
		t.Fatalf("acquire seed admission: acquired=%v err=%v", acquired, err)
	}
	blocked, err := store.block(ctx, branch, lease.leaseToken, lease.generation, blockerKind, "/tmp/checkout",
		"branch-sha", "target-sha", "", "seeded blocker", d.nowFunc())
	if err != nil {
		t.Fatalf("block seed admission: %v", err)
	}
	return blocked
}

func loadRecoveryAdmission(t *testing.T, d *Dispatcher, branch string) epicBranchAdmission {
	t.Helper()
	admission, err := loadEpicBranchAdmission(context.Background(), d.db, branch)
	if err != nil {
		t.Fatalf("load admission %s: %v", branch, err)
	}
	return admission
}

func assertCanonicalEpicRecovery(
	ctx context.Context,
	t *testing.T,
	d *Dispatcher,
	beads *observingEpicRecoveryStore,
	admission epicBranchAdmission,
) *protocol.Bead {
	t.Helper()
	child, err := beads.Show(ctx, admission.recoveryBeadID)
	if err != nil {
		t.Fatalf("show recovery child: %v", err)
	}
	if child == nil {
		t.Fatalf("recovery child %q not found", admission.recoveryBeadID)
	}
	if child.Priority != 0 || child.Epic != admission.epicID || !slices.Contains(child.Tags, epicBranchRecoveryTag) {
		t.Fatalf("recovery child identity = priority %d epic %q tags %v, want P0/%q/%q",
			child.Priority, child.Epic, child.Tags, admission.epicID, epicBranchRecoveryTag)
	}
	if !isExactEpicBranchRecoveryChild(child, admission) {
		t.Fatalf("recovery child is not exact for admission: %+v %+v", child, admission)
	}
	epic, err := beads.Show(ctx, admission.epicID)
	if err != nil || epic == nil {
		t.Fatalf("show recovery epic: bead=%+v err=%v", epic, err)
	}
	if !slices.ContainsFunc(epic.Dependencies, func(dep protocol.Dependency) bool {
		return dep.DependsOnID == child.ID && dep.Type == "blocks"
	}) {
		t.Fatalf("epic dependencies = %+v, want blocker %q", epic.Dependencies, child.ID)
	}
	return child
}

func assertNoEpicRecoveryDependency(
	ctx context.Context,
	t *testing.T,
	beads *beadstore.FakeStore,
	epicID, recoveryID string,
) {
	t.Helper()
	epic, err := beads.Show(ctx, epicID)
	if err != nil || epic == nil {
		t.Fatalf("show recovery epic: bead=%+v err=%v", epic, err)
	}
	if slices.ContainsFunc(epic.Dependencies, func(dep protocol.Dependency) bool {
		return dep.DependsOnID == recoveryID && dep.Type == "blocks"
	}) {
		t.Fatalf("epic dependencies still contain recovery %q: %+v", recoveryID, epic.Dependencies)
	}
}

func cloneRecoveryBead(child *protocol.Bead) protocol.Bead {
	clone := *child
	clone.Tags = append([]string(nil), child.Tags...)
	clone.Metadata = make(map[string]any, len(child.Metadata))
	for key, value := range child.Metadata {
		clone.Metadata[key] = value
	}
	return clone
}

func recoveryCreateParams(child protocol.Bead) beadstore.CreateParams {
	metadata := make(map[string]string, len(child.Metadata))
	for key, value := range child.Metadata {
		metadata[key] = fmt.Sprint(value)
	}
	return beadstore.CreateParams{
		ID: child.ID, Title: child.Title, Type: child.Type, Priority: child.Priority,
		Description: child.Description, ParentID: child.Epic, AcceptanceCriteria: child.AcceptanceCriteria,
		Tags: append([]string(nil), child.Tags...), Metadata: metadata, Tier: string(child.Tier),
	}
}

func activeAssignmentCount(t *testing.T, d *Dispatcher, beadID string) int {
	t.Helper()
	var count int
	if err := d.db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID).Scan(&count); err != nil {
		t.Fatalf("count active assignments for %s: %v", beadID, err)
	}
	return count
}
