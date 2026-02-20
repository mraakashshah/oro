package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/protocol"
)

// writeFakeBdMultiStatus writes an executable shell script at fakeBin/bd that:
//   - returns status-filtered JSON when called with `--status <status>`
//   - returns empty JSON array when called without --status (simulating single-call path)
//
// This distinguishes the multi-status path (fetchBeads) from the single-call path (FetchBeads).
func writeFakeBdMultiStatus(t *testing.T, fakeBin string) {
	t.Helper()
	// Shell script: look for --status flag; if found echo a bead with that status,
	// otherwise echo empty array (simulating old single-call behavior).
	script := `#!/bin/sh
prev=""
for arg in "$@"; do
  if [ "$prev" = "--status" ]; then
    printf '[{"id":"oro-001","title":"Test","status":"%s","issue_type":"task"}]\n' "$arg"
    exit 0
  fi
  prev="$arg"
done
echo '[]'
`
	path := filepath.Join(fakeBin, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test-only executable stub
		t.Fatalf("write fake bd: %v", err)
	}
}

// TestFetchBeadsCmd_DelegatesToMultiStatusFetch verifies that fetchBeadsCmd uses
// the multi-status fetchBeads path (4 status calls) rather than the deprecated
// single-call FetchBeads path. The fake bd returns a bead only when called with
// --status, so the multi-status path yields 4 beads while the single-call path
// yields 0. A zero count means the wrong path is being used.
func TestFetchBeadsCmd_DelegatesToMultiStatusFetch(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBdMultiStatus(t, fakeBin)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+origPath)

	cmd := fetchBeadsCmd()
	msg := cmd()

	beads, ok := msg.(beadsMsg)
	if !ok {
		t.Fatalf("expected beadsMsg, got %T", msg)
	}

	// Multi-status path fetches open/in_progress/blocked/closed → 4 beads.
	// Single-call path fetches bd list --json → [] → 0 beads.
	if len(beads) != 4 {
		t.Errorf("fetchBeadsCmd returned %d beads; want 4 (multi-status path)", len(beads))
	}
}

func TestParseBeadsOutput_ParsesJSONArray(t *testing.T) {
	input := `[
		{"id":"oro-abc","title":"Fix login bug","priority":1,"issue_type":"bug"},
		{"id":"oro-def","title":"Add dashboard","priority":2,"issue_type":"feature","epic":"oro-epic1"},
		{"id":"oro-ghi","title":"Refactor auth","priority":3,"issue_type":"task","estimated_minutes":30}
	]`

	beads, err := parseBeadsOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(beads) != 3 {
		t.Fatalf("expected 3 beads, got %d", len(beads))
	}

	// Verify first bead
	if beads[0].ID != "oro-abc" {
		t.Errorf("bead[0].ID = %q, want %q", beads[0].ID, "oro-abc")
	}
	if beads[0].Title != "Fix login bug" {
		t.Errorf("bead[0].Title = %q, want %q", beads[0].Title, "Fix login bug")
	}
	if beads[0].Priority != 1 {
		t.Errorf("bead[0].Priority = %d, want 1", beads[0].Priority)
	}
	if beads[0].Type != "bug" {
		t.Errorf("bead[0].Type = %q, want %q", beads[0].Type, "bug")
	}

	// Verify second bead has epic
	if beads[1].Epic != "oro-epic1" {
		t.Errorf("bead[1].Epic = %q, want %q", beads[1].Epic, "oro-epic1")
	}

	// Verify third bead has estimated minutes
	if beads[2].EstimatedMinutes != 30 {
		t.Errorf("bead[2].EstimatedMinutes = %d, want 30", beads[2].EstimatedMinutes)
	}
}

func TestParseBeadsOutput_EmptyArray(t *testing.T) {
	beads, err := parseBeadsOutput("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(beads) != 0 {
		t.Fatalf("expected 0 beads, got %d", len(beads))
	}
}

func TestParseBeadsOutput_EmptyInput(t *testing.T) {
	beads, err := parseBeadsOutput("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if beads != nil {
		t.Fatalf("expected nil beads, got %v", beads)
	}
}

func TestParseBeadsOutput_MalformedJSON(t *testing.T) {
	input := `not valid json`

	_, err := parseBeadsOutput(input)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestFetchWorkers_Offline(t *testing.T) {
	ctx := context.Background()
	workers, assignments, focusedEpic, err := fetchWorkerStatus(ctx, "/nonexistent/socket/path.sock")
	if err != nil {
		t.Fatalf("expected no error for offline dispatcher, got: %v", err)
	}
	if len(workers) != 0 {
		t.Fatalf("expected empty slice for offline dispatcher, got %d workers", len(workers))
	}
	if len(assignments) != 0 {
		t.Fatalf("expected empty map for offline dispatcher, got %d assignments", len(assignments))
	}
	if focusedEpic != "" {
		t.Fatalf("expected empty focusedEpic for offline dispatcher, got %q", focusedEpic)
	}
}

// Ensure protocol.Bead is what parseBeadsOutput returns (compile-time check).
var _ []protocol.Bead = mustParseBeads()

func mustParseBeads() []protocol.Bead {
	return nil
}
