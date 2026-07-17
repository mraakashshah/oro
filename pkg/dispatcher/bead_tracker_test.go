package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"testing"
)

func TestGCWorktreesWithoutBeadSourceDoesNotPanic(t *testing.T) {
	d, _, worktrees, _, _, _ := newTestDispatcher(t)
	d.beads = nil

	gcCalled := false
	worktrees.gcClosedFn = func(_ context.Context, isBeadClosed func(string) bool) error {
		gcCalled = true
		return nil
	}

	d.gcWorktrees(context.Background())

	if gcCalled {
		t.Fatal("GC scanned worktrees without a bead source")
	}
}

func TestGCWorktreesNilBeadDetailDoesNotPanic(t *testing.T) {
	d, beads, worktrees, _, _, _ := newTestDispatcher(t)
	beads.shownNil = map[string]bool{"oro-deleted": true}

	gcCalled := false
	worktrees.gcClosedFn = func(_ context.Context, isBeadClosed func(string) bool) error {
		gcCalled = true
		if isBeadClosed("oro-deleted") {
			t.Fatal("GC authorized removal for a bead with nil detail")
		}
		return nil
	}

	d.gcWorktrees(context.Background())

	if !gcCalled {
		t.Fatal("GC did not scan worktrees")
	}
}
