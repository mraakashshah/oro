package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"oro/pkg/testutil/qgserial"
	"sync"
	"testing"
)

// TestReconcileScale_NoDuplicateWorkerIDs verifies that when reconcileScale is
// called concurrently from multiple goroutines (e.g., assignLoop and directive
// handler), no duplicate worker IDs are generated.
//
// The bug (oro-ovpc): scaleUp() generates worker IDs using time.Now().UnixNano()
// in a loop. When called concurrently, multiple goroutines can generate the same
// IDs, spawning duplicate processes for the same worker ID.
//
// This test reproduces the race condition by calling reconcileScale concurrently
// and asserting that each spawned worker has a unique ID.
func TestReconcileScale_NoDuplicateWorkerIDs(t *testing.T) {
	qgserial.RequireSerial(t)
	t.Parallel()

	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Mock process manager to track spawned worker IDs
	mockProcMgr := &mockProcessManager{}
	d.procMgr = mockProcMgr
	d.cfg.MaxWorkers = 10

	// Set target to 5 workers with no current workers
	d.mu.Lock()
	d.targetWorkers = 5
	d.mu.Unlock()

	// Call reconcileScale concurrently from multiple goroutines to simulate:
	// - Thread A: assignLoop → tryAssign → reconcileScale
	// - Thread B: directive handler → handleScale → reconcileScale
	var wg sync.WaitGroup
	numGoroutines := 3
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			_ = d.reconcileScale()
		}()
	}

	wg.Wait()

	// Count occurrences of each worker ID in the spawned list
	mockProcMgr.mu.Lock()
	idCounts := make(map[string]int)
	for _, id := range mockProcMgr.spawned {
		idCounts[id]++
	}
	totalSpawned := len(mockProcMgr.spawned)
	mockProcMgr.mu.Unlock()

	// CRITICAL ASSERTION 1: Each worker ID should be spawned exactly once.
	// If the race exists, multiple goroutines will generate the same ID
	// (e.g., "worker-1234567890-0") and spawn duplicate processes.
	duplicates := []string{}
	for id, count := range idCounts {
		if count > 1 {
			duplicates = append(duplicates, id)
		}
	}

	if len(duplicates) > 0 {
		t.Errorf("Race condition detected: %d worker IDs spawned multiple times: %v",
			len(duplicates), duplicates)
	}

	// CRITICAL ASSERTION 2: Total spawned workers should be exactly 5 (target).
	// If reconcileScale runs 3 times concurrently without protection, we might
	// spawn 15 workers (3 * 5), though some would have duplicate IDs.
	uniqueSpawned := len(idCounts)

	if totalSpawned != 5 {
		t.Errorf("Expected 5 total spawns, got %d (race in reconcileScale)", totalSpawned)
	}

	if uniqueSpawned != 5 {
		t.Errorf("Expected 5 unique workers spawned, got %d unique IDs", uniqueSpawned)
	}
}
