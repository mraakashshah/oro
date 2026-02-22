package dispatcher //nolint:testpackage // white-box tests for hashQGOutput and isQGStuck

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestHashQGOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "empty string",
			output: "",
		},
		{
			name:   "simple string",
			output: "hello world",
		},
		{
			name:   "QG output with newlines",
			output: "Task: Fix bug\nStatus: In Progress\nNext: Run tests",
		},
		{
			name:   "large output",
			output: strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 1000),
		},
		{
			name:   "unicode characters",
			output: "Task: 修复错误 🐛 — Status: ✅ Complete",
		},
		{
			name:   "JSON-like output",
			output: `{"task": "fix-bug", "status": "in_progress", "next": ["test", "commit"]}`,
		},
		{
			name:   "whitespace variations",
			output: "   spaces\ttabs\n\nnewlines   ",
		},
		{
			name:   "special characters",
			output: "!@#$%^&*()_+-=[]{}|;':\",./<>?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := hashQGOutput(tt.output)

			// Hash should be 64 hex characters (SHA-256 produces 32 bytes = 64 hex chars)
			if len(hash) != 64 {
				t.Errorf("HashQGOutput(%q) hash length = %d, want 64", tt.output, len(hash))
			}

			// Hash should only contain hex characters (0-9a-f)
			for _, ch := range hash {
				if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
					t.Errorf("HashQGOutput(%q) hash contains non-hex character: %c", tt.output, ch)
					break
				}
			}

			// Hash should be deterministic - same input produces same output
			hash2 := hashQGOutput(tt.output)
			if hash != hash2 {
				t.Errorf("HashQGOutput(%q) not deterministic: got %q and %q", tt.output, hash, hash2)
			}
		})
	}
}

func TestHashQGOutput_Uniqueness(t *testing.T) {
	// Different inputs should produce different hashes
	tests := []struct {
		name    string
		output1 string
		output2 string
	}{
		{
			name:    "different strings",
			output1: "hello",
			output2: "world",
		},
		{
			name:    "case sensitivity",
			output1: "Hello",
			output2: "hello",
		},
		{
			name:    "trailing whitespace",
			output1: "test",
			output2: "test ",
		},
		{
			name:    "newline difference",
			output1: "line1\nline2",
			output2: "line1 line2",
		},
		{
			name:    "empty vs space",
			output1: "",
			output2: " ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := hashQGOutput(tt.output1)
			hash2 := hashQGOutput(tt.output2)

			if hash1 == hash2 {
				t.Errorf("HashQGOutput produced same hash for different inputs:\n  input1: %q\n  input2: %q\n  hash: %q",
					tt.output1, tt.output2, hash1)
			}
		})
	}
}

func TestHashQGOutput_KnownVector(t *testing.T) {
	// Test against a known SHA-256 hash to ensure correctness
	// echo -n "test" | sha256sum produces: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
	output := "test"
	want := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	got := hashQGOutput(output)
	if got != want {
		t.Errorf("HashQGOutput(%q) = %q, want %q", output, got, want)
	}
}

// newStuckDispatcher returns a minimal Dispatcher wired only for isQGStuck tests.
// It avoids the full constructor (which requires a DB and config).
func newStuckDispatcher() *Dispatcher {
	return &Dispatcher{
		BeadTracker: BeadTracker{
			qgStuckTracker: make(map[string]*qgHistory),
		},
	}
}

// --- isQGStuck unit tests ---

// TestIsQGStuck_BelowThresholdNotStuck verifies that fewer than maxStuckCount
// identical outputs do NOT trigger stuck detection.
// Kills mutants 2 (remove return false when n<maxStuckCount) and
// 5 (<= instead of < in n < maxStuckCount guard).
func TestIsQGStuck_BelowThresholdNotStuck(t *testing.T) {
	d := newStuckDispatcher()

	// maxStuckCount = 3; send 2 identical outputs — must NOT be stuck.
	for i := 0; i < maxStuckCount-1; i++ {
		got := d.isQGStuck("bead-1", "identical output")
		if got {
			t.Fatalf("call %d: expected false (below threshold), got true", i+1)
		}
	}
}

// TestIsQGStuck_ExactThresholdIsStuck verifies that exactly maxStuckCount
// identical consecutive outputs return true.
// Kills mutants 2, 3, 5, 6.
func TestIsQGStuck_ExactThresholdIsStuck(t *testing.T) {
	d := newStuckDispatcher()

	// First maxStuckCount-1 calls must return false.
	for i := 0; i < maxStuckCount-1; i++ {
		got := d.isQGStuck("bead-thresh", "same output")
		if got {
			t.Fatalf("call %d: expected false before threshold, got true", i+1)
		}
	}

	// The maxStuckCount-th call with the same output must return true.
	got := d.isQGStuck("bead-thresh", "same output")
	if !got {
		t.Fatalf("call %d: expected true at threshold, got false", maxStuckCount)
	}
}

// TestIsQGStuck_DifferentOutputNotStuck verifies that a different output
// in the last position does not trigger stuck detection.
// Kills mutant 3 (remove inner return false for non-matching hash).
func TestIsQGStuck_DifferentOutputNotStuck(t *testing.T) {
	d := newStuckDispatcher()

	// Two identical outputs, then one different.
	for i := 0; i < maxStuckCount-1; i++ {
		d.isQGStuck("bead-diff", "same error") //nolint:errcheck
	}
	got := d.isQGStuck("bead-diff", "different error")
	if got {
		t.Fatal("expected false when last output differs, got true")
	}
}

// TestIsQGStuck_MixedThenIdenticalIsStuck verifies that after mixed outputs,
// maxStuckCount consecutive identical ones still trigger stuck.
// Kills mutant 3 (inner return false) and mutant 5 (guard boundary).
func TestIsQGStuck_MixedThenIdenticalIsStuck(t *testing.T) {
	d := newStuckDispatcher()

	// One different output, then maxStuckCount identical.
	d.isQGStuck("bead-mix", "different") //nolint:errcheck
	for i := 0; i < maxStuckCount-1; i++ {
		got := d.isQGStuck("bead-mix", "same error")
		if got {
			t.Fatalf("premature stuck on call %d", i+1)
		}
	}
	got := d.isQGStuck("bead-mix", "same error")
	if !got {
		t.Fatal("expected true after maxStuckCount identical outputs following a different one, got false")
	}
}

// TestIsQGStuck_SlidingWindowEvictsOldEntries verifies that the sliding window
// caps hashes at maxQGHistorySize, evicting the oldest entry.
// Kills mutant 1 (skip the trim assignment) and
// mutant 4 (>= instead of > in trim guard).
func TestIsQGStuck_SlidingWindowEvictsOldEntries(t *testing.T) {
	d := newStuckDispatcher()
	beadID := "bead-window"

	// Fill to exactly maxQGHistorySize with unique outputs so none are stuck.
	for i := 0; i < maxQGHistorySize; i++ {
		got := d.isQGStuck(beadID, fmt.Sprintf("unique-%d", i))
		if got {
			t.Fatalf("unexpected stuck at unique call %d", i)
		}
	}

	// Verify internal history is exactly maxQGHistorySize entries.
	d.mu.Lock()
	hist := d.qgStuckTracker[beadID]
	d.mu.Unlock()
	if hist == nil {
		t.Fatal("expected qgHistory to exist")
	}
	if len(hist.hashes) != maxQGHistorySize {
		t.Fatalf("expected %d hashes after %d unique calls, got %d",
			maxQGHistorySize, maxQGHistorySize, len(hist.hashes))
	}

	// One more unique call triggers the sliding-window trim (maxQGHistorySize+1 > maxQGHistorySize).
	d.isQGStuck(beadID, "one-more-unique") //nolint:errcheck
	d.mu.Lock()
	hist = d.qgStuckTracker[beadID]
	d.mu.Unlock()
	if len(hist.hashes) != maxQGHistorySize {
		t.Fatalf("after trim: expected %d hashes, got %d", maxQGHistorySize, len(hist.hashes))
	}
}

// TestIsQGStuck_SlidingWindowExactBoundary verifies that the trim fires
// when len > maxQGHistorySize (not >=). Specifically, at exactly
// maxQGHistorySize entries the trim must NOT fire yet.
// Kills mutant 4 (>= instead of > in trim guard).
func TestIsQGStuck_SlidingWindowExactBoundary(t *testing.T) {
	d := newStuckDispatcher()
	beadID := "bead-boundary"

	// Add exactly maxQGHistorySize entries without trimming.
	for i := 0; i < maxQGHistorySize; i++ {
		d.isQGStuck(beadID, fmt.Sprintf("entry-%d", i)) //nolint:errcheck
	}

	d.mu.Lock()
	count := len(d.qgStuckTracker[beadID].hashes)
	d.mu.Unlock()

	// At exactly maxQGHistorySize, the guard `len > maxQGHistorySize` is false,
	// so no trim should have happened yet.
	if count != maxQGHistorySize {
		t.Fatalf("expected exactly %d entries (no trim yet), got %d", maxQGHistorySize, count)
	}
}

// TestIsQGStuck_NewBeadInitialisedOnFirstCall verifies that calling isQGStuck
// for an unknown beadID creates a fresh qgHistory entry.
// Kills mutants 0, 9, 10 (skip history init / store).
func TestIsQGStuck_NewBeadInitialisedOnFirstCall(t *testing.T) {
	d := newStuckDispatcher()

	// Before first call, no entry exists.
	d.mu.Lock()
	_, exists := d.qgStuckTracker["fresh-bead"]
	d.mu.Unlock()
	if exists {
		t.Fatal("expected no entry before first isQGStuck call")
	}

	// First call should create an entry and append the hash.
	d.isQGStuck("fresh-bead", "some output") //nolint:errcheck

	d.mu.Lock()
	hist, exists := d.qgStuckTracker["fresh-bead"]
	d.mu.Unlock()

	if !exists {
		t.Fatal("expected qgHistory entry to be created on first call")
	}
	if len(hist.hashes) != 1 {
		t.Fatalf("expected 1 hash after first call, got %d", len(hist.hashes))
	}
}

// TestIsQGStuck_HashAppendedEachCall verifies that every call to isQGStuck
// appends a new hash to the history.
// Kills mutant 8 (skip append).
func TestIsQGStuck_HashAppendedEachCall(t *testing.T) {
	d := newStuckDispatcher()
	beadID := "bead-append"

	for i := 1; i <= 5; i++ {
		d.isQGStuck(beadID, fmt.Sprintf("output-%d", i)) //nolint:errcheck
		d.mu.Lock()
		got := len(d.qgStuckTracker[beadID].hashes)
		d.mu.Unlock()
		if got != i {
			t.Fatalf("after call %d: expected %d hashes, got %d", i, i, got)
		}
	}
}

// TestIsQGStuck_HashMatchesHashQGOutput verifies that the stored hash equals
// what hashQGOutput produces for the same input.
// Kills mutant 8 indirectly and validates the hash path.
func TestIsQGStuck_HashMatchesHashQGOutput(t *testing.T) {
	d := newStuckDispatcher()
	beadID := "bead-hash-verify"
	output := "FAIL: TestFoo expected 42 got 0"

	d.isQGStuck(beadID, output) //nolint:errcheck

	d.mu.Lock()
	storedHash := d.qgStuckTracker[beadID].hashes[0]
	d.mu.Unlock()

	want := hashQGOutput(output)
	if storedHash != want {
		t.Fatalf("stored hash %q != hashQGOutput(%q) = %q", storedHash, output, want)
	}
}

// TestIsQGStuck_IndependentBeads verifies that two separate beadIDs track
// their histories independently.
func TestIsQGStuck_IndependentBeads(t *testing.T) {
	d := newStuckDispatcher()

	// Bead A gets maxStuckCount identical outputs — should be stuck.
	for i := 0; i < maxStuckCount; i++ {
		d.isQGStuck("bead-A", "stuck output") //nolint:errcheck
	}

	d.mu.Lock()
	histA := d.qgStuckTracker["bead-A"]
	_, beadBExists := d.qgStuckTracker["bead-B"]
	d.mu.Unlock()

	if len(histA.hashes) != maxStuckCount {
		t.Fatalf("bead-A: expected %d hashes, got %d", maxStuckCount, len(histA.hashes))
	}
	if beadBExists {
		t.Fatal("bead-B should not have a history entry when only bead-A was used")
	}

	// Bead B with one call should not be stuck.
	got := d.isQGStuck("bead-B", "some output")
	if got {
		t.Fatal("bead-B: expected false on first call, got true")
	}
}

// TestIsQGStuck_ConcurrentSafety exercises isQGStuck under concurrent access
// to verify the mu.Lock() call is not removed.
// Kills mutant 7 (skip d.mu.Lock()).
// This test will data-race without the lock (caught by -race).
func TestIsQGStuck_ConcurrentSafety(t *testing.T) {
	d := newStuckDispatcher()
	const goroutines = 20
	const callsEach = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			beadID := fmt.Sprintf("bead-concurrent-%d", g)
			for c := 0; c < callsEach; c++ {
				d.isQGStuck(beadID, fmt.Sprintf("output-%d", c))
			}
		}()
	}
	wg.Wait()

	// Verify each bead has exactly callsEach hashes (no concurrent corruption).
	d.mu.Lock()
	defer d.mu.Unlock()
	for g := 0; g < goroutines; g++ {
		beadID := fmt.Sprintf("bead-concurrent-%d", g)
		hist := d.qgStuckTracker[beadID]
		if hist == nil {
			t.Errorf("bead %s: nil history after concurrent calls", beadID)
			continue
		}
		if len(hist.hashes) != callsEach {
			t.Errorf("bead %s: expected %d hashes, got %d", beadID, callsEach, len(hist.hashes))
		}
	}
}

// TestIsQGStuck_AboveThresholdRemainsStuck verifies that once stuck, subsequent
// identical calls continue returning true (the loop covers [n-maxStuckCount, n)).
// Kills mutant 6 (i <= n instead of i < n in the for loop).
func TestIsQGStuck_AboveThresholdRemainsStuck(t *testing.T) {
	d := newStuckDispatcher()

	// Reach stuck state.
	for i := 0; i < maxStuckCount; i++ {
		d.isQGStuck("bead-persist", "stuck") //nolint:errcheck
	}

	// Each subsequent identical call should still return true.
	for extra := 0; extra < 3; extra++ {
		got := d.isQGStuck("bead-persist", "stuck")
		if !got {
			t.Fatalf("extra call %d: expected true (still stuck), got false", extra+1)
		}
	}
}

// TestIsQGStuck_ReturnsFalseFirstTwoCalls pins the exact boundary where
// the early-return guard fires (n < maxStuckCount = 3 means n=1 and n=2
// must return false; n=3 with identical hashes must return true).
// Kills mutant 5 (<= instead of < in the n < maxStuckCount guard).
func TestIsQGStuck_ReturnsFalseFirstTwoCalls(t *testing.T) {
	d := newStuckDispatcher()
	beadID := "bead-pin"

	// Call 1: n=1 < 3 → must return false.
	if d.isQGStuck(beadID, "x") {
		t.Fatal("call 1 (n=1): expected false, got true")
	}
	// Call 2: n=2 < 3 → must return false.
	if d.isQGStuck(beadID, "x") {
		t.Fatal("call 2 (n=2): expected false, got true")
	}
	// Call 3: n=3 == maxStuckCount → must return true (all 3 identical).
	if !d.isQGStuck(beadID, "x") {
		t.Fatal("call 3 (n=3): expected true, got false")
	}
}
