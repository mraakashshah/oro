package dispatcher //nolint:testpackage // white-box coverage for admission filtering

import (
	"context"
	"errors"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestBlockedEpicBranchFiltersDescendantsWithoutRetrySpam(t *testing.T) {
	ctx := context.Background()
	d, beads, _, escalator, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate epic branch admission schema: %v", err)
	}

	const (
		epicID        = "oro-blocked-epic"
		directID      = "oro-blocked-direct"
		intermediate  = "oro-blocked-middle"
		nestedID      = "oro-blocked-nested"
		unrelatedEpic = "oro-unrelated-epic"
		unrelatedID   = "oro-unrelated-task"
	)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newEpicBranchAdmissionStore(d.db)
	lease, acquired, err := store.acquire(ctx, protocol.EpicBranchPrefix+epicID, epicID, "main", "lease-token", "worker-a", now)
	if err != nil || !acquired {
		t.Fatalf("acquire blocked epic admission = acquired %v err %v", acquired, err)
	}
	blockedIdentity := lease
	blockedIdentity.state = "blocked"
	blockedIdentity.blockerKind = "checked_out"
	recoveryID := epicBranchRecoveryBeadID(blockedIdentity, "")
	blocked, err := store.block(ctx, lease.branch, lease.leaseToken, lease.generation,
		blockedIdentity.blockerKind, "/tmp/epic", "branch-sha", "target-sha", recoveryID, "preserve branch", now)
	if err != nil {
		t.Fatalf("block epic admission: %v", err)
	}

	recoveryParams := epicBranchRecoveryCreateParams(ctx, d, blocked, "")
	recovery := protocol.Bead{
		ID: recoveryID, Title: recoveryParams.Title, Status: "open", Priority: recoveryParams.Priority,
		Type: recoveryParams.Type, Epic: recoveryParams.ParentID, Description: recoveryParams.Description,
		AcceptanceCriteria: recoveryParams.AcceptanceCriteria, Tags: recoveryParams.Tags,
		Metadata: map[string]any{},
	}
	for key, value := range recoveryParams.Metadata {
		recovery.Metadata[key] = value
	}

	beads.shown[epicID] = &protocol.BeadDetail{ID: epicID, Type: "epic", Status: "in_progress"}
	beads.shown[intermediate] = &protocol.BeadDetail{ID: intermediate, Type: "task", Status: "open", Epic: epicID}
	beads.shown[unrelatedEpic] = &protocol.BeadDetail{ID: unrelatedEpic, Type: "epic", Status: "in_progress"}
	beads.shown[recoveryID] = beadDetailFromBead(recovery)
	beads.dependencies = append(beads.dependencies, protocol.Dependency{IssueID: epicID, DependsOnID: recoveryID, Type: "blocks"})

	ready := []protocol.Bead{
		{ID: directID, Type: "task", Status: "open", Epic: epicID},
		{ID: nestedID, Type: "task", Status: "open", Epic: intermediate},
		recovery,
		{ID: unrelatedID, Type: "task", Status: "open", Epic: unrelatedEpic},
	}
	d.mu.Lock()
	d.attemptCounts[directID] = 4
	d.attemptCounts[nestedID] = 5
	d.mu.Unlock()

	baseline := epicBranchQuietSnapshot{
		children:        len(beads.created),
		dependencies:    len(beads.dependencies),
		blockers:        epicBranchBlockerCount(t, d),
		prepareFailures: eventCount(t, d.db, "epic_branch_prepare_failed"),
		escalations:     len(escalator.Messages()),
		directAttempts:  4,
		nestedAttempts:  5,
	}
	assignmentAttempts := make(map[string]int)
	for tick := 1; tick <= 3; tick++ {
		filtered := d.filterEpicBranchAdmissions(ctx, ready)
		for _, bead := range filtered {
			assignmentAttempts[bead.ID]++ // every admitted bead would reach assignBead
		}
		if err := d.reconcileEpicBranchAdmissions(ctx, now.Add(time.Duration(tick)*time.Minute)); err != nil {
			t.Fatalf("reconcile tick %d: %v", tick, err)
		}
	}

	if assignmentAttempts[directID] != 0 || assignmentAttempts[nestedID] != 0 {
		t.Fatalf("blocked descendants reached assignBead: direct=%d nested=%d", assignmentAttempts[directID], assignmentAttempts[nestedID])
	}
	if assignmentAttempts[recoveryID] != 3 || assignmentAttempts[unrelatedID] != 3 || len(assignmentAttempts) != 2 {
		t.Fatalf("assignment attempts = %#v, want only recovery and unrelated once per tick", assignmentAttempts)
	}
	assertEpicBranchQuietSnapshot(t, d, beads, escalator, baseline)

	beads.showErrFn = map[string]error{intermediate: errors.New("temporary parent inspection failure")}
	filtered := d.filterEpicBranchAdmissions(ctx, ready)
	if got := beadIDs(filtered); len(got) != 2 || got[0] != recoveryID || got[1] != unrelatedID {
		t.Fatalf("branch-local inspection failure filtered IDs = %v, want recovery and unrelated", got)
	}
}

type epicBranchQuietSnapshot struct {
	children, dependencies, blockers, prepareFailures, escalations int
	directAttempts, nestedAttempts                                 int
}

func assertEpicBranchQuietSnapshot(t *testing.T, d *Dispatcher, beads *fakeBeadStore, escalator *mockEscalator, want epicBranchQuietSnapshot) {
	t.Helper()
	beads.mu.Lock()
	children, dependencies := len(beads.created), len(beads.dependencies)
	beads.mu.Unlock()
	d.mu.Lock()
	directAttempts := d.attemptCounts["oro-blocked-direct"]
	nestedAttempts := d.attemptCounts["oro-blocked-nested"]
	d.mu.Unlock()
	got := epicBranchQuietSnapshot{
		children: children, dependencies: dependencies, blockers: epicBranchBlockerCount(t, d),
		prepareFailures: eventCount(t, d.db, "epic_branch_prepare_failed"), escalations: len(escalator.Messages()),
		directAttempts: directAttempts, nestedAttempts: nestedAttempts,
	}
	if got != want {
		t.Fatalf("quiet reconciliation side effects = %+v, want unchanged %+v", got, want)
	}
}

func epicBranchBlockerCount(t *testing.T, d *Dispatcher) int {
	t.Helper()
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM epic_branch_admissions WHERE state='blocked'`).Scan(&count); err != nil {
		t.Fatalf("count blocked epic admissions: %v", err)
	}
	return count
}

func beadDetailFromBead(bead protocol.Bead) *protocol.BeadDetail {
	return &protocol.BeadDetail{
		ID: bead.ID, Title: bead.Title, Description: bead.Description, Status: bead.Status,
		Priority: bead.Priority, Type: bead.Type, Epic: bead.Epic, AcceptanceCriteria: bead.AcceptanceCriteria,
		Tags: bead.Tags, Metadata: bead.Metadata,
	}
}

func beadIDs(beads []protocol.Bead) []string {
	ids := make([]string, 0, len(beads))
	for _, bead := range beads {
		ids = append(ids, bead.ID)
	}
	return ids
}
