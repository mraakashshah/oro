package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"sync"
	"testing"
	"time"

	"oro/pkg/memory"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// TestDreamTriggersAfterNBeads verifies that beadsSinceDream increments with
// each mergeAndComplete and that a dream is spawned when DreamInterval is reached.
// It also verifies that DreamInterval=0 disables dreaming entirely.
func TestDreamTriggersAfterNBeads(t *testing.T) {
	t.Run("counter increments and resets at interval", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}
		d.cfg.DreamInterval = 3

		var mu sync.Mutex
		var dreamCalls int
		d.dreamExecuteFn = func(_ context.Context, _ []memory.DreamAction, _ *memory.Store, _ func(string)) error {
			mu.Lock()
			dreamCalls++
			mu.Unlock()
			return nil
		}

		// Two completions: counter must be 2, no dream triggered.
		d.mergeAndComplete(ctx, "bead-a", "worker-x", "/tmp/wt-da", "agent/bead-a", "", "")
		d.mergeAndComplete(ctx, "bead-b", "worker-x", "/tmp/wt-db", "agent/bead-b", "", "")

		d.mu.Lock()
		got := d.beadsSinceDream
		d.mu.Unlock()
		if got != 2 {
			t.Errorf("beadsSinceDream = %d, want 2 after 2 completions", got)
		}
		mu.Lock()
		before := dreamCalls
		mu.Unlock()
		if before != 0 {
			t.Errorf("dreamCalls = %d, want 0 before interval reached", before)
		}

		// Third completion hits DreamInterval=3: dream triggered, counter resets to 0.
		d.mergeAndComplete(ctx, "bead-c", "worker-x", "/tmp/wt-dc", "agent/bead-c", "", "")

		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return dreamCalls > 0
		}, 2*time.Second)

		d.mu.Lock()
		after := d.beadsSinceDream
		d.mu.Unlock()
		if after != 0 {
			t.Errorf("beadsSinceDream = %d, want 0 after dream triggered", after)
		}
	})

	t.Run("DreamInterval=0 never dreams", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}
		d.cfg.DreamInterval = 0

		var mu sync.Mutex
		var dreamCalls int
		d.dreamExecuteFn = func(_ context.Context, _ []memory.DreamAction, _ *memory.Store, _ func(string)) error {
			mu.Lock()
			dreamCalls++
			mu.Unlock()
			return nil
		}

		// Complete many beads — no dream should fire.
		for i := 0; i < 20; i++ {
			d.mergeAndComplete(ctx, "bead-z", "worker-x", "/tmp/wt-dz", "agent/bead-z", "", "")
		}

		// Give any async goroutines a moment to run.
		time.Sleep(50 * time.Millisecond)

		mu.Lock()
		calls := dreamCalls
		mu.Unlock()
		if calls != 0 {
			t.Errorf("dreamCalls = %d, want 0 when DreamInterval=0", calls)
		}
	})
}

// TestDreamTriggerCompleteEpicClose verifies that completeEpicClose always
// spawns a dream agent, independent of the beadsSinceDream counter.
func TestDreamTriggerCompleteEpicClose(t *testing.T) {
	d, beadSource, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	d.cfg.DreamInterval = 100 // high — should not interfere

	var mu sync.Mutex
	var dreamCalls int
	d.dreamExecuteFn = func(_ context.Context, _ []memory.DreamAction, _ *memory.Store, _ func(string)) error {
		mu.Lock()
		dreamCalls++
		mu.Unlock()
		return nil
	}

	beadSource.mu.Lock()
	beadSource.shown["epic-dream-1"] = &protocol.BeadDetail{ID: "epic-dream-1"}
	beadSource.mu.Unlock()

	d.completeEpicClose(ctx, "epic-dream-1", "worker-1", "All children completed", "main")

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dreamCalls > 0
	}, 2*time.Second)

	mu.Lock()
	calls := dreamCalls
	mu.Unlock()
	if calls == 0 {
		t.Error("expected dream to be triggered by completeEpicClose, got 0 calls")
	}
}

// TestHandleDreamResult verifies that handleDreamResult parses the dream agent's
// output and calls the execute function with the extracted actions.
func TestHandleDreamResult(t *testing.T) {
	t.Run("calls executeActions with parsed actions", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		var mu sync.Mutex
		var capturedActions []memory.DreamAction
		d.dreamExecuteFn = func(_ context.Context, actions []memory.DreamAction, _ *memory.Store, _ func(string)) error {
			mu.Lock()
			capturedActions = append(capturedActions, actions...)
			mu.Unlock()
			return nil
		}

		resultCh := make(chan ops.Result, 1)
		resultCh <- ops.Result{
			Type:     ops.OpsDream,
			Feedback: "[CREATE] type=pattern: prefer functional patterns over OOP",
		}

		d.handleDreamResult(ctx, resultCh)

		mu.Lock()
		actions := capturedActions
		mu.Unlock()

		if len(actions) != 1 {
			t.Fatalf("expected 1 dream action, got %d", len(actions))
		}
		if actions[0].Kind != "CREATE" {
			t.Errorf("action Kind = %q, want CREATE", actions[0].Kind)
		}
	})

	t.Run("ops error logs and does not crash", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		var mu sync.Mutex
		var executeCalled bool
		d.dreamExecuteFn = func(_ context.Context, _ []memory.DreamAction, _ *memory.Store, _ func(string)) error {
			mu.Lock()
			executeCalled = true
			mu.Unlock()
			return nil
		}

		resultCh := make(chan ops.Result, 1)
		resultCh <- ops.Result{
			Type: ops.OpsDream,
			Err:  context.DeadlineExceeded,
		}

		// Must not panic.
		d.handleDreamResult(ctx, resultCh)

		mu.Lock()
		called := executeCalled
		mu.Unlock()
		if called {
			t.Error("expected executeActions not to be called on error result")
		}
	})
}
