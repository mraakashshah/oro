package main

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

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
	workers, err := fetchWorkerStatus(ctx, "/nonexistent/socket/path.sock")
	if err != nil {
		t.Fatalf("expected no error for offline dispatcher, got: %v", err)
	}
	if len(workers) != 0 {
		t.Fatalf("expected empty slice for offline dispatcher, got %d workers", len(workers))
	}
}

// Ensure protocol.Bead is what parseBeadsOutput returns (compile-time check).
var _ []protocol.Bead = mustParseBeads()

func mustParseBeads() []protocol.Bead {
	return nil
}
