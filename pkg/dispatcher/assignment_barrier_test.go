package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// waitForSetupTimeout bounds how long tests wait for post-reservation
// assignment setup (worktree creation, the assignments row, the ASSIGN send)
// to finish before treating it as stalled. It must be generous enough for
// real worktree operations under load but must never be unbounded — an
// unbounded wait on a stalled setup goroutine would hang the test binary
// instead of failing the test.
const waitForSetupTimeout = 10 * time.Second

// tryAssignAndWait runs one scheduling pass and blocks until every
// assignment setup that pass launched has finished, failing the test if
// setup stalls past waitForSetupTimeout.
//
// Use this instead of a bare d.tryAssign(ctx) in any test that reads state
// the post-reservation setup goroutine writes — worktree paths, the
// assignments DB row, the ASSIGN send to the worker. Since
// c629e33e2a26f0e199b869e7ae9d1d7d5c83a997, d.tryAssign returns as soon as
// workers are reserved; it does not wait for that goroutine, and tests that
// assumed otherwise now read state nondeterministically.
//
// Do NOT replace this with d.wg.Wait(). d.wg is the WaitGroup safeGo uses
// for every tracked goroutine, including the long-lived assignLoop,
// heartbeat and janitor loops (dispatcher.go ~1679-1686, ~1730-1742).
// Waiting on it deadlocks in any test that starts the dispatcher — this is
// the exact trap c629e33e fell into when it added d.wg.Wait() to three
// tests. A per-invocation handle from tryAssignBatch cannot include a
// dispatcher loop or a later pass, so it has no equivalent hazard.
func tryAssignAndWait(t *testing.T, d *Dispatcher, ctx context.Context) { //nolint:revive // (t, d, ctx) is the fixed call-site shape used across the 13 migration sites this helper targets
	t.Helper()
	waitForSetup(t, d.tryAssignBatch(ctx))
}

// waitForSetup blocks until every handle in handles has closed, or fails the
// test via t.Fatal if waitForSetupTimeout elapses first. It never hangs: a
// handle that never closes still returns control to the test within the
// deadline.
func waitForSetup(t *testing.T, handles []<-chan struct{}) {
	t.Helper()
	if err := waitForHandlesOrTimeout(handles, waitForSetupTimeout); err != nil {
		t.Fatal(err)
	}
}

// waitForHandlesOrTimeout is the deadline-bounded wait logic behind
// waitForSetup, factored out into a plain function so it can be exercised
// directly against a handle that never closes — proving the fail-fast
// property — without needing to fail a real *testing.T to do so.
func waitForHandlesOrTimeout(handles []<-chan struct{}, timeout time.Duration) error {
	deadline := time.After(timeout)
	for i, h := range handles {
		select {
		case <-h:
		case <-deadline:
			return fmt.Errorf("waitForSetup: handle %d of %d did not close within %s; assignment setup stalled", i, len(handles), timeout)
		}
	}
	return nil
}

// TestTryAssignAndWaitFailsFastWhenSetupStalls proves the wait logic behind
// tryAssignAndWait/waitForSetup fails fast on a stalled handle instead of
// hanging. It exercises waitForHandlesOrTimeout directly against a handle
// that is deliberately never closed, with a short deadline — this is the
// documented pattern for testing timeout behavior without making a real test
// block for waitForSetupTimeout (or forever) to prove the failure path.
func TestTryAssignAndWaitFailsFastWhenSetupStalls(t *testing.T) {
	stalled := make(chan struct{}) // never closed: simulates a stuck setup goroutine
	handles := []<-chan struct{}{stalled}

	const shortDeadline = 50 * time.Millisecond
	start := time.Now()
	err := waitForHandlesOrTimeout(handles, shortDeadline)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitForHandlesOrTimeout returned nil error for a handle that never closes")
	}
	// Generous upper bound: proves this returns near the deadline rather than
	// hanging, without making the test timing-sensitive on a loaded machine.
	if elapsed > 5*time.Second {
		t.Fatalf("waitForHandlesOrTimeout took %s to fail on a stalled handle with a %s deadline; it must fail fast, not hang", elapsed, shortDeadline)
	}
}

// TestTryAssignAndWaitReturnsAfterAllLaunchedSetupCompletes proves the
// success path: once every launched setup finishes, waitForHandlesOrTimeout
// returns promptly with no error, well inside the deadline.
func TestTryAssignAndWaitReturnsAfterAllLaunchedSetupCompletes(t *testing.T) {
	a := make(chan struct{})
	b := make(chan struct{})
	close(a)
	close(b)
	handles := []<-chan struct{}{a, b}

	if err := waitForHandlesOrTimeout(handles, waitForSetupTimeout); err != nil {
		t.Fatalf("waitForHandlesOrTimeout returned an error for already-closed handles: %v", err)
	}
}

// TestTryAssignAndWaitObservesSetupCompletion exercises tryAssignAndWait
// end-to-end against a real (fast) dispatcher pass: once it returns, the
// state that only the post-reservation setup goroutine writes — the
// assignments row, the ASSIGN send to the worker — must already be visible,
// with no separate poll or sleep required by the caller. This is the
// intended replacement for the nondeterministic `d.tryAssign(ctx)` call
// pattern (see the assignment-setup-observability design doc); it is not a
// migration of an existing test, just proof the helper does what it claims.
func TestTryAssignAndWaitObservesSetupCompletion(t *testing.T) {
	d, beadSrc, workers := setupTryAssignSchedulingTest(t, 1)
	seedTryAssignBead(t, beadSrc, protocol.Bead{ID: "oro-barrier-demo", Priority: 0})
	beadSrc.SetBeads([]protocol.Bead{{ID: "oro-barrier-demo", Priority: 0}})

	tryAssignAndWait(t, d, context.Background())

	got := assignedBeadIDsByCreation(t, d.db)
	want := []string{"oro-barrier-demo"}
	if !slices.Equal(got, want) {
		t.Fatalf("assigned beads = %v, want %v", got, want)
	}
	assertMockWorkerAssignCount(t, workers, 1)
}
