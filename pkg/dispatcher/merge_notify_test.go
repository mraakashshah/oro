package dispatcher //nolint:testpackage // white-box: verifies per-bead SHA routing in MERGE_COMPLETE

import (
	"context"
	"strings"
	"sync"
	"testing"

	"oro/pkg/merge"
	"oro/pkg/protocol"
)

// branchSHAGitRunner is a GitRunner that returns branch-specific SHAs for
// rev-parse calls. When the production code calls rev-parse <branch>, it gets
// the SHA registered for that branch. When it calls rev-parse HEAD it gets
// headSHA — the "tick-wide" SHA that is wrong to use for per-bead notification.
type branchSHAGitRunner struct {
	mu        sync.Mutex
	branchSHA map[string]string // branch name → SHA
	headSHA   string            // returned for rev-parse HEAD
}

func (r *branchSHAGitRunner) Run(_ context.Context, _ string, args ...string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(args) == 0 {
		return "", "", nil
	}
	switch args[0] {
	case "rebase":
		return "", "", nil // always succeeds (no conflict)
	case "rev-list":
		return "1\n", "", nil // 1 unmerged commit → bypass isBranchMerged fast path
	case "diff":
		return "", "", nil
	case "merge":
		return "", "", nil // ff-only succeeds
	case "worktree":
		return "", "", nil // worktree remove succeeds
	case "rev-parse":
		if len(args) < 2 {
			return r.headSHA + "\n", "", nil
		}
		switch args[1] {
		case "--git-common-dir":
			return "mock-repo/.git\n", "", nil
		case "--show-toplevel":
			return "mock-repo\n", "", nil
		case "HEAD":
			return r.headSHA + "\n", "", nil
		default:
			if sha, ok := r.branchSHA[args[1]]; ok {
				return sha + "\n", "", nil
			}
			return r.headSHA + "\n", "", nil
		}
	}
	return "", "", nil
}

// TestMergeCompleteUsesPerBeadSHA asserts that when two beads are merged in the
// same tick (separate commits), each MERGE_COMPLETE escalation contains the SHA
// for that specific bead's commit — not the latest tick-wide HEAD SHA.
//
// Regression: oro-fsks — observed 2026-05-05: oro-nxrkt merged at 5ec78241 and
// oro-d99x at 173bbdd1; both MERGE_COMPLETE notifications quoted 5ec78241.
func TestMergeCompleteUsesPerBeadSHA(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const (
		beadA   = "bead-a"
		workerA = "w-a"
		shaA    = "aaabbbccc111"
		beadB   = "bead-b"
		workerB = "w-b"
		shaB    = "dddeeefff222"
		// headSHA simulates the tick-wide HEAD (bead-a's SHA after its ff-merge).
		// If bead-b's notification erroneously reads rev-parse HEAD after bead-a
		// has merged, it gets headSHA instead of shaB.
		headSHA = "head-tick-wide-wrong"
	)

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		beadA: {ID: beadA, Status: "in_progress"},
		beadB: {ID: beadB, Status: "in_progress"},
	}
	beadSrc.mu.Unlock()

	// Replace the merger with one whose git runner returns per-branch SHAs.
	// rev-parse HEAD returns headSHA (the "wrong" tick-wide value).
	// rev-parse agent/<beadX> returns the correct per-bead SHA.
	customRunner := &branchSHAGitRunner{
		branchSHA: map[string]string{
			protocol.BranchPrefix + beadA: shaA,
			protocol.BranchPrefix + beadB: shaB,
		},
		headSHA: headSHA,
	}
	d.merger = merge.NewCoordinator(customRunner)

	insertAssignment := func(beadID, workerID string) int64 {
		res, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
			beadID, workerID, "/tmp/wt-"+beadID)
		if err != nil {
			t.Fatalf("insert assignment for %s: %v", beadID, err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	idA := insertAssignment(beadA, workerA)
	idB := insertAssignment(beadB, workerB)

	// Merge both beads in the same "tick" (sequential, same goroutine).
	d.mergeAndComplete(ctx, beadA, workerA, "/tmp/wt-"+beadA, protocol.BranchPrefix+beadA, "", "", idA)
	d.mergeAndComplete(ctx, beadB, workerB, "/tmp/wt-"+beadB, protocol.BranchPrefix+beadB, "", "", idB)

	// Collect MERGE_COMPLETE escalation messages.
	var msgA, msgB string
	for _, m := range esc.Messages() {
		if !strings.Contains(m, string(protocol.EscMergeComplete)) {
			continue
		}
		switch {
		case strings.Contains(m, beadA):
			msgA = m
		case strings.Contains(m, beadB):
			msgB = m
		}
	}

	if msgA == "" {
		t.Fatal("no MERGE_COMPLETE escalation received for bead-a")
	}
	if msgB == "" {
		t.Fatal("no MERGE_COMPLETE escalation received for bead-b")
	}

	// Each escalation must carry its own bead's per-commit SHA.
	if !strings.Contains(msgA, shaA) {
		t.Errorf("MERGE_COMPLETE for %s: want SHA %q; got message: %q", beadA, shaA, msgA)
	}
	if strings.Contains(msgA, headSHA) {
		t.Errorf("MERGE_COMPLETE for %s: must not contain tick-wide HEAD SHA %q; got: %q", beadA, headSHA, msgA)
	}
	if !strings.Contains(msgB, shaB) {
		t.Errorf("MERGE_COMPLETE for %s: want SHA %q; got message: %q", beadB, shaB, msgB)
	}
	if strings.Contains(msgB, headSHA) {
		t.Errorf("MERGE_COMPLETE for %s: must not contain tick-wide HEAD SHA %q; got: %q", beadB, headSHA, msgB)
	}
}
