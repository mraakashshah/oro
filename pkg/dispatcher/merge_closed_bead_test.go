package dispatcher //nolint:testpackage // white-box: asserts mergeAndComplete short-circuits before merger.Merge

import (
	"context"
	"go/ast"
	"testing"

	"oro/pkg/protocol"
)

// TestMergeAndCompleteAbortsOnClosedBead asserts that when a bead is already
// closed (e.g. by manager dedup) before the worker's review completes,
// mergeAndComplete must NOT call merger.Merge — otherwise an orphan commit can
// land on the target branch on top of an already-resolved bead.
//
// Regression: oro-jev9 — live observation 2026-05-04. Manager closed oro-0yse
// as duplicate of oro-mdrh while a worker was mid-review. Dispatcher proceeded
// to merge the worker's commit (8cc25431) to main on the already-closed bead.
func TestMergeAndCompleteAbortsOnClosedBead(t *testing.T) {
	d, beadSrc, wtMgr, _, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const beadID = "bead-already-closed"

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		beadID: {ID: beadID, Status: "closed"},
	}
	beadSrc.mu.Unlock()

	worktree := "/tmp/wt-" + beadID
	d.mergeAndComplete(ctx, beadID, "w-x", worktree, "agent/"+beadID, "", "", 0)

	if calls := gitRunner.RebaseCalls(); len(calls) != 0 {
		t.Fatalf("expected no rebase calls when bead is already closed, got %d: %v", len(calls), calls)
	}

	beadSrc.mu.Lock()
	closedCalls := append([]string(nil), beadSrc.closed...)
	beadSrc.mu.Unlock()
	for _, id := range closedCalls {
		if id == beadID {
			t.Fatalf("dispatcher must not re-close already-closed bead %q (would emit \"Merged: <sha>\" reason masking the dedup close)", beadID)
		}
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("closed-bead merge abort has no merge proof; worktree should be preserved, removed=%v", removed)
	}
	if eventCount(t, d.db, "recovery_work_quarantined") == 0 {
		t.Fatalf("expected recovery_work_quarantined event")
	}
}

func TestFinalizeSuccessfulMergeCompletesAssignmentBeforeClosingBead(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		beadID   = "bead-merged-cleanup-order"
		workerID = "worker-merged-cleanup-order"
		worktree = "/tmp/wt-bead-merged-cleanup-order"
	)

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beadSrc.closeFn = func(ctx context.Context, id, reason string) error {
		if id != beadID {
			t.Fatalf("CloseBead id = %q, want %q", id, beadID)
		}
		var activeAtClose int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`,
			beadID).Scan(&activeAtClose); err != nil {
			t.Fatalf("count active assignments at close: %v", err)
		}
		if activeAtClose != 0 {
			t.Fatalf("active assignments at CloseBead = %d, want 0", activeAtClose)
		}
		return nil
	}
	beadSrc.mu.Unlock()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, "", "", assignmentID, "abc123")
}

func TestNoopMergeCompletesAssignmentBeforeClose(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		beadID   = "bead-noop-cleanup-order"
		workerID = "worker-noop-cleanup-order"
		worktree = "/tmp/wt-bead-noop-cleanup-order"
		branch   = "agent/bead-noop-cleanup-order"
	)

	closeCalls := 0
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beadSrc.closeFn = func(ctx context.Context, id, reason string) error {
		closeCalls++
		if id != beadID {
			t.Fatalf("CloseBead id = %q, want %q", id, beadID)
		}
		var activeAtClose int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`,
			beadID).Scan(&activeAtClose); err != nil {
			t.Fatalf("count active assignments at close: %v", err)
		}
		if activeAtClose != 0 {
			t.Fatalf("active assignments at CloseBead = %d, want 0", activeAtClose)
		}
		return nil
	}
	beadSrc.mu.Unlock()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	d.handleNoopMerge(ctx, beadID, workerID, worktree, branch, "", "epic/noop", assignmentID, "abc123")

	if closeCalls != 1 {
		t.Fatalf("CloseBead calls = %d, want 1", closeCalls)
	}
	if got := esc.Messages(); len(got) != 0 {
		t.Fatalf("no-op merge must not escalate, got messages: %v", got)
	}
	beadSrc.mu.Lock()
	updates := map[string]string{}
	for id, status := range beadSrc.updated {
		updates[id] = status
	}
	beadSrc.mu.Unlock()
	if status, ok := updates[beadID]; ok {
		t.Fatalf("no-op merge must not reopen bead %q; status update=%q updates=%v", beadID, status, updates)
	}
}

func TestNoopMergeCleanupUsesTargetBranch(t *testing.T) {
	_, files := parseDispatcherSourceFiles(t)
	var handleNoopMerges []*ast.FuncDecl
	for _, file := range files {
		if fn := findFuncDecl(file, "handleNoopMerge"); fn != nil {
			handleNoopMerges = append(handleNoopMerges, fn)
		}
	}
	if len(handleNoopMerges) == 0 {
		t.Fatal("handleNoopMerge not found")
	}
	for _, handleNoopMerge := range handleNoopMerges {
		var cleanupArg ast.Expr
		ast.Inspect(handleNoopMerge, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "removeWorktreeAndClearTracking" {
				return true
			}
			if len(call.Args) != 5 {
				t.Fatalf("removeWorktreeAndClearTracking arg count = %d, want 5", len(call.Args))
			}
			cleanupArg = call.Args[4]
			return false
		})
		ident, ok := cleanupArg.(*ast.Ident)
		if !ok || ident.Name != "target" {
			t.Fatalf("handleNoopMerge cleanup target arg = %T %[1]v, want ident target", cleanupArg)
		}
	}

	t.Run("explicit target", func(t *testing.T) {
		d, _, wtMgr, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		const beadID = "bead-noop-cleanup-target"
		d.handleNoopMerge(ctx, beadID, "worker-noop-cleanup-target", "/tmp/wt-"+beadID, "agent/"+beadID, "", "epic/parent", 0, "abc123")

		wtMgr.mu.Lock()
		deletedInto := append([]deleteBranchMergedIntoCall(nil), wtMgr.deletedInto...)
		wtMgr.mu.Unlock()
		if len(deletedInto) != 1 {
			t.Fatalf("DeleteBranchMergedInto calls = %d, want 1: %v", len(deletedInto), deletedInto)
		}
		if got := deletedInto[0]; got != (deleteBranchMergedIntoCall{branch: "agent/" + beadID, targetBranch: "epic/parent"}) {
			t.Fatalf("DeleteBranchMergedInto call = %+v, want branch %q target %q", got, "agent/"+beadID, "epic/parent")
		}
		if got := esc.Messages(); len(got) != 0 {
			t.Fatalf("no-op merge must not escalate, got messages: %v", got)
		}
	})

	t.Run("empty target defaults", func(t *testing.T) {
		d, _, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		const beadID = "bead-noop-cleanup-default"
		d.handleNoopMerge(ctx, beadID, "worker-noop-cleanup-default", "/tmp/wt-"+beadID, "agent/"+beadID, "", "", 0, "abc123")

		wtMgr.mu.Lock()
		deletedInto := append([]deleteBranchMergedIntoCall(nil), wtMgr.deletedInto...)
		wtMgr.mu.Unlock()
		if len(deletedInto) != 1 {
			t.Fatalf("DeleteBranchMergedInto calls = %d, want 1: %v", len(deletedInto), deletedInto)
		}
		if got := deletedInto[0]; got != (deleteBranchMergedIntoCall{branch: "agent/" + beadID, targetBranch: d.cfg.DefaultBranch}) {
			t.Fatalf("DeleteBranchMergedInto call = %+v, want branch %q target %q", got, "agent/"+beadID, d.cfg.DefaultBranch)
		}
	})
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}
