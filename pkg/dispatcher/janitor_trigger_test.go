package dispatcher //nolint:testpackage // white-box test verifies merge-trigger state under d.mu

import (
	"context"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestJanitorTriggerGate(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled settings leave counters untouched", func(t *testing.T) {
		for _, cfg := range []Config{
			{JanitorEnabled: false, JanitorInterval: 50},
			{JanitorEnabled: true, JanitorInterval: 0, StorageCatalogPath: "catalog.db"},
		} {
			d, _, _, _, _, _ := newTestDispatcher(t)
			d.cfg = cfg
			d.mergesSinceJanitor = 49
			d.janitorRunsSinceAudit = 4

			d.maybeTriggerJanitor(ctx)

			if d.mergesSinceJanitor != 49 || d.janitorRunsSinceAudit != 4 {
				t.Fatalf("disabled trigger mutated counters: merges=%d auditRuns=%d", d.mergesSinceJanitor, d.janitorRunsSinceAudit)
			}
		}
	})

	t.Run("idle gate defers then force fires", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg = Config{JanitorEnabled: true, JanitorInterval: 50, JanitorIdleThreshold: 0}

		var mu sync.Mutex
		janitorCalls := 0
		d.janitorSpawnFn = func(context.Context) {
			mu.Lock()
			janitorCalls++
			mu.Unlock()
		}

		d.cachedQueueDepth = 1
		d.mergesSinceJanitor = 49
		d.maybeTriggerJanitor(ctx)
		if d.mergesSinceJanitor != 50 {
			t.Fatalf("busy queue counter = %d, want 50", d.mergesSinceJanitor)
		}

		d.mergesSinceJanitor = 149
		d.maybeTriggerJanitor(ctx)
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return janitorCalls == 1
		}, 2*time.Second)
		if d.mergesSinceJanitor != 0 {
			t.Fatalf("force-run counter = %d, want 0", d.mergesSinceJanitor)
		}
	})

	t.Run("idle threshold fires and every fifth cycle uses audit only", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg = Config{
			JanitorEnabled:       true,
			JanitorInterval:      50,
			JanitorIdleThreshold: 2,
			AuditEnabled:         true,
			AuditEveryNJanitors:  5,
		}
		d.cachedQueueDepth = 2

		var mu sync.Mutex
		janitorCalls := 0
		auditCalls := 0
		d.janitorSpawnFn = func(context.Context) {
			mu.Lock()
			janitorCalls++
			mu.Unlock()
		}
		d.auditSpawnFn = func(context.Context) {
			mu.Lock()
			auditCalls++
			mu.Unlock()
		}

		for range 5 {
			d.mergesSinceJanitor = 49
			d.maybeTriggerJanitor(ctx)
		}
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return janitorCalls+auditCalls == 5
		}, 2*time.Second)

		mu.Lock()
		defer mu.Unlock()
		if janitorCalls != 4 || auditCalls != 1 {
			t.Fatalf("spawn calls: janitor=%d audit=%d, want 4 and 1", janitorCalls, auditCalls)
		}
		if d.janitorRunsSinceAudit != 0 {
			t.Fatalf("janitorRunsSinceAudit = %d, want 0 after audit", d.janitorRunsSinceAudit)
		}
	})

	t.Run("both merge completion paths increment the counter", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		d.cfg = Config{JanitorEnabled: true, JanitorInterval: 50}

		for _, beadID := range []string{"successful-merge", "noop-merge"} {
			beadSrc.mu.Lock()
			beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
			beadSrc.mu.Unlock()
			if _, err := d.db.ExecContext(ctx,
				`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
				beadID, "worker-"+beadID, "/tmp/"+beadID); err != nil {
				t.Fatalf("insert assignment for %s: %v", beadID, err)
			}
		}

		d.finalizeSuccessfulMerge(ctx, "successful-merge", "worker-successful-merge", "/tmp/successful-merge", "", "", 1, "abc123")
		d.handleNoopMerge(ctx, "noop-merge", "worker-noop-merge", "/tmp/noop-merge", "agent/noop-merge", "", "", 2, "abc123")

		d.mu.Lock()
		defer d.mu.Unlock()
		if d.mergesSinceJanitor != 2 {
			t.Fatalf("mergesSinceJanitor = %d, want 2 after successful and no-op merges", d.mergesSinceJanitor)
		}
	})
}
