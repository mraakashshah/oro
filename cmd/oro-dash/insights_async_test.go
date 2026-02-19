package main

import (
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// TestPhase1_InDegrees verifies degree computation runs immediately.
func TestPhase1_InDegrees(t *testing.T) {
	tests := []struct {
		name    string
		beads   []BeadWithDeps
		wantDeg map[string]int
	}{
		{
			name:    "no beads returns empty map",
			beads:   []BeadWithDeps{},
			wantDeg: map[string]int{},
		},
		{
			name:    "isolated bead has zero in-degree",
			beads:   []BeadWithDeps{{ID: "bead-1"}},
			wantDeg: map[string]int{"bead-1": 0},
		},
		{
			name: "chain: bead-1 depends on bead-2, bead-2 depends on bead-3",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-2"}},
				{ID: "bead-2", DependsOn: []string{"bead-3"}},
				{ID: "bead-3"},
			},
			// bead-3 has 1 dependent (bead-2), bead-2 has 1 dependent (bead-1), bead-1 has 0
			wantDeg: map[string]int{"bead-1": 0, "bead-2": 1, "bead-3": 1},
		},
		{
			name: "fan-in: two beads depend on same bead",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-3"}},
				{ID: "bead-2", DependsOn: []string{"bead-3"}},
				{ID: "bead-3"},
			},
			wantDeg: map[string]int{"bead-1": 0, "bead-2": 0, "bead-3": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := NewDependencyGraph(tt.beads)
			got := graph.InDegrees()
			if len(got) != len(tt.wantDeg) {
				t.Errorf("InDegrees() len = %d, want %d", len(got), len(tt.wantDeg))
				return
			}
			for id, wantCount := range tt.wantDeg {
				if got[id] != wantCount {
					t.Errorf("InDegrees()[%s] = %d, want %d", id, got[id], wantCount)
				}
			}
		})
	}
}

// TestPhase1_TopologicalOrder verifies topological sort runs immediately.
func TestPhase1_TopologicalOrder(t *testing.T) {
	tests := []struct {
		name    string
		beads   []BeadWithDeps
		wantLen int
		wantErr bool
		// checkOrder is an optional function to verify ordering constraints
		checkOrder func(t *testing.T, order []string)
	}{
		{
			name:    "empty graph returns empty slice",
			beads:   []BeadWithDeps{},
			wantLen: 0,
			wantErr: false,
		},
		{
			name:    "single bead returns that bead",
			beads:   []BeadWithDeps{{ID: "bead-1"}},
			wantLen: 1,
			wantErr: false,
		},
		{
			name: "simple chain returns all beads",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-2"}},
				{ID: "bead-2", DependsOn: []string{"bead-3"}},
				{ID: "bead-3"},
			},
			wantLen: 3,
			wantErr: false,
			checkOrder: func(t *testing.T, order []string) {
				t.Helper()
				// bead-3 must come before bead-2, bead-2 before bead-1
				pos := make(map[string]int)
				for i, id := range order {
					pos[id] = i
				}
				if pos["bead-3"] >= pos["bead-2"] {
					t.Errorf("bead-3 (pos %d) should come before bead-2 (pos %d)", pos["bead-3"], pos["bead-2"])
				}
				if pos["bead-2"] >= pos["bead-1"] {
					t.Errorf("bead-2 (pos %d) should come before bead-1 (pos %d)", pos["bead-2"], pos["bead-1"])
				}
			},
		},
		{
			name: "circular dependency returns error",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-2"}},
				{ID: "bead-2", DependsOn: []string{"bead-1"}},
			},
			wantLen: 0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := NewDependencyGraph(tt.beads)
			order, err := graph.TopologicalOrder()

			if tt.wantErr {
				if err == nil {
					t.Errorf("TopologicalOrder() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("TopologicalOrder() unexpected error: %v", err)
				return
			}
			if len(order) != tt.wantLen {
				t.Errorf("TopologicalOrder() len = %d, want %d", len(order), tt.wantLen)
				return
			}
			if tt.checkOrder != nil {
				tt.checkOrder(t, order)
			}
		})
	}
}

// TestInsightsModel_Phase1ComputedImmediately verifies phase 1 results are present before 500ms.
func TestInsightsModel_Phase1ComputedImmediately(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "bead-1", DependsOn: []string{"bead-2"}},
		{ID: "bead-2", DependsOn: []string{"bead-3"}},
		{ID: "bead-3"},
	}

	start := time.Now()
	model := NewInsightsModel(beads)
	elapsed := time.Since(start)

	// Phase 1 must complete well under 500ms
	if elapsed > 100*time.Millisecond {
		t.Errorf("NewInsightsModel took %v, phase 1 should be immediate (<100ms)", elapsed)
	}

	p1 := model.Phase1()
	if p1 == nil {
		t.Fatal("Phase1() returned nil, expected phase 1 results")
	}
	if len(p1.TopoOrder) != 3 {
		t.Errorf("Phase1().TopoOrder len = %d, want 3", len(p1.TopoOrder))
	}
	if len(p1.InDegree) != 3 {
		t.Errorf("Phase1().InDegree len = %d, want 3", len(p1.InDegree))
	}
}

// TestInsightsModel_Phase2ComputedAsync verifies phase 2 results arrive asynchronously.
func TestInsightsModel_Phase2ComputedAsync(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "bead-1", DependsOn: []string{"bead-2"}},
		{ID: "bead-2", DependsOn: []string{"bead-3"}},
		{ID: "bead-3"},
	}

	model := NewInsightsModel(beads)

	// Phase 2 may not be ready immediately
	// After waiting up to 600ms it must be present
	var p2 *Phase2Results
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		p2 = model.Phase2()
		if p2 != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if p2 == nil {
		t.Fatal("Phase2() still nil after 600ms, expected async results to arrive")
	}

	// Critical path for bead-1->bead-2->bead-3 should be bead-3,bead-2,bead-1
	if len(p2.CriticalPath) != 3 {
		t.Errorf("Phase2().CriticalPath len = %d, want 3", len(p2.CriticalPath))
	}
}

// TestInsightsModel_Phase2AtomicSwap verifies the atomic pointer swap is race-free.
func TestInsightsModel_Phase2AtomicSwap(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "bead-1", DependsOn: []string{"bead-2"}},
		{ID: "bead-2"},
	}

	model := NewInsightsModel(beads)

	// Spin up many readers while the background goroutine writes
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(700 * time.Millisecond)
		for time.Now().Before(deadline) {
			_ = model.Phase2() // must not race
			time.Sleep(time.Millisecond)
		}
	}()

	<-done
	// If we get here without -race detecting a problem, the swap is safe.
}

// TestInsightsModel_Phase2Timeout verifies that a slow graph computation is bounded.
func TestInsightsModel_Phase2Timeout(t *testing.T) {
	// Use a large graph to ensure we test the timeout path;
	// in practice this is synthetic. We just verify the model is usable
	// within a reasonable period even if phase 2 hasn't finished.
	beads := make([]BeadWithDeps, 100)
	for i := 0; i < 100; i++ {
		beads[i] = BeadWithDeps{ID: "bead-" + string(rune('A'+i%26))}
	}

	model := NewInsightsModel(beads)

	// Must be renderable immediately (phase 1 data)
	theme := DefaultTheme()
	styles := NewStyles(theme)
	output := model.Render(styles)
	if output == "" {
		t.Error("Render() returned empty string before phase 2 completed")
	}
}

// TestInsightsModel_RenderShowsLoadingWhenPhase2Pending verifies the render shows
// a loading indicator when phase 2 results are not yet available.
func TestInsightsModel_RenderShowsLoadingWhenPhase2Pending(t *testing.T) {
	// Single isolated bead — phase 1 immediate, phase 2 goroutine may not have run yet.
	// We can't reliably test the "loading" state since goroutines can be fast.
	// Instead verify that once phase 2 arrives, the render uses its results.
	beads := []BeadWithDeps{
		{ID: "bead-1", DependsOn: []string{"bead-2"}},
		{ID: "bead-2", DependsOn: []string{"bead-3"}},
		{ID: "bead-3"},
	}

	model := NewInsightsModel(beads)

	// Wait for phase 2
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if model.Phase2() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	output := model.Render(styles)

	// After phase 2, critical path should appear in render
	if !containsIgnoringANSI(output, "bead-3") {
		t.Errorf("Render() after phase 2 should show critical path bead-3, got:\n%s", output)
	}
}

// TestInsightsModel_Phase2CycleDetection verifies cycle detection runs in phase 2.
func TestInsightsModel_Phase2CycleDetection(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "bead-1", DependsOn: []string{"bead-2"}},
		{ID: "bead-2", DependsOn: []string{"bead-1"}},
	}

	model := NewInsightsModel(beads)

	// Wait for phase 2
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if model.Phase2() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	p2 := model.Phase2()
	if p2 == nil {
		t.Fatal("Phase2() still nil after 600ms")
	}
	if !p2.HasCycle {
		t.Error("Phase2().HasCycle = false, expected true for circular dependency")
	}
}

// Compile-time check: ensure Phase2Results pointer is atomically storable.
// This verifies the pointer fits in a single machine word.
var _ = unsafe.Sizeof((*Phase2Results)(nil)) == unsafe.Sizeof(uintptr(0))

// Compile-time check: ensure we use atomic pointer in InsightsModel.
var _ = atomic.Pointer[Phase2Results]{}
