package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// newDreamTestDispatcher creates a Dispatcher with a specific DreamInterval
// set through the constructor path (Config → withDefaults → New) so that the
// real defaulting logic is exercised.
func newDreamTestDispatcher(t *testing.T, dreamInterval int) (*Dispatcher, *fakeBeadStore, *mockBatchSpawner) {
	t.Helper()
	db := newTestDB(t)

	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	spawnMock := &mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}
	opsSpawner := ops.NewSpawner(spawnMock)

	beadSrc := &fakeBeadStore{
		beads: []protocol.Bead{},
		shown: make(map[string]*protocol.BeadDetail),
	}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       5,
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     50 * time.Millisecond,
		ShutdownTimeout:  200 * time.Millisecond,
		DreamInterval:    dreamInterval,
	}

	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	d.qgRunner = &mockQGRunner{passed: true}
	d.escalationRetryInterval = 50 * time.Millisecond
	return d, beadSrc, spawnMock
}

// TestDreamTriggersAfterNBeads verifies that beadsSinceDream increments with
// each mergeAndComplete and that a dream is spawned when DreamInterval is reached.
// It also verifies that DreamInterval=0 disables dreaming entirely.
func TestDreamTriggersAfterNBeads(t *testing.T) {
	t.Run("counter increments and resets at interval", func(t *testing.T) {
		d, _, _ := newDreamTestDispatcher(t, 3)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		var mu sync.Mutex
		var dreamCalls int
		d.dreamExecuteFn = func(_ context.Context, _ []memory.DreamAction, _ *memory.Store, _ func(string)) error {
			mu.Lock()
			dreamCalls++
			mu.Unlock()
			return nil
		}

		// Two completions: counter must be 2, no dream triggered.
		d.mergeAndComplete(ctx, "bead-a", "worker-x", "/tmp/wt-da", "agent/bead-a", "", "", 0)
		d.mergeAndComplete(ctx, "bead-b", "worker-x", "/tmp/wt-db", "agent/bead-b", "", "", 0)

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
		d.mergeAndComplete(ctx, "bead-c", "worker-x", "/tmp/wt-dc", "agent/bead-c", "", "", 0)

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
		// DreamInterval=0 flows through Config → withDefaults → New,
		// verifying that withDefaults preserves the 0 (disabled) value.
		d, _, _ := newDreamTestDispatcher(t, 0)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		// Verify withDefaults preserved DreamInterval=0.
		if d.cfg.DreamInterval != 0 {
			t.Fatalf("withDefaults changed DreamInterval=0 to %d", d.cfg.DreamInterval)
		}

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
			d.mergeAndComplete(ctx, "bead-z", "worker-x", "/tmp/wt-dz", "agent/bead-z", "", "", 0)
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

// TestDreamTriggerCompleteEpicClose verifies that completeEpicClose spawns a
// dream agent when DreamInterval>0, and respects DreamInterval=0 (disabled).
func TestDreamTriggerCompleteEpicClose(t *testing.T) {
	t.Run("triggers dream when DreamInterval>0", func(t *testing.T) {
		d, beadSource, _ := newDreamTestDispatcher(t, 100) // high — should not interfere
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

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
	})

	t.Run("DreamInterval=0 skips dream on epic close", func(t *testing.T) {
		d, beadSource, _ := newDreamTestDispatcher(t, 0)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		var mu sync.Mutex
		var dreamCalls int
		d.dreamExecuteFn = func(_ context.Context, _ []memory.DreamAction, _ *memory.Store, _ func(string)) error {
			mu.Lock()
			dreamCalls++
			mu.Unlock()
			return nil
		}

		beadSource.mu.Lock()
		beadSource.shown["epic-dream-2"] = &protocol.BeadDetail{ID: "epic-dream-2"}
		beadSource.mu.Unlock()

		d.completeEpicClose(ctx, "epic-dream-2", "worker-2", "All children completed", "main")

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

// TestDreamPassesMemories verifies that triggerDream serializes memories from
// the store into DreamOpts.Memories so the dream agent sees actual content.
func TestDreamPassesMemories(t *testing.T) {
	d, _, spawnMock := newDreamTestDispatcher(t, 1)
	ctx := context.Background()
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Seed the memory store with a test memory.
	_, err := d.memories.Insert(ctx, memory.InsertParams{
		Content:    "always run tests before committing code changes",
		Type:       "lesson",
		Tags:       []string{"testing"},
		Source:     "self_report",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	// DreamInterval=1 means first mergeAndComplete triggers a dream.
	d.mergeAndComplete(ctx, "bead-mem", "worker-x", "/tmp/wt-mem", "agent/bead-mem", "", "", 0)

	// Wait for the dream spawn to happen.
	waitFor(t, func() bool {
		return spawnMock.SpawnCount() > 0
	}, 2*time.Second)

	// Verify the spawned prompt contains our memory content.
	spawnMock.mu.Lock()
	defer spawnMock.mu.Unlock()

	found := false
	for _, call := range spawnMock.spawns {
		if strings.Contains(call.prompt, "always run tests before committing") {
			found = true
			break
		}
	}
	if !found {
		t.Error("dream agent prompt did not contain memory content; DreamOpts.Memories was empty")
	}
}

// TestHandleDreamResult verifies that handleDreamResult parses the dream agent's
// output and calls the execute function with the extracted actions.
func TestHandleDreamResult(t *testing.T) {
	t.Run("calls executeActions with parsed actions", func(t *testing.T) {
		d, _, _ := newDreamTestDispatcher(t, 10)
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
		d, _, _ := newDreamTestDispatcher(t, 10)
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
