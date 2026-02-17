package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"oro/pkg/protocol"
)

// TestDashModel_Init verifies the model initializes with BoardView active.
func TestDashModel_Init(t *testing.T) {
	m := newModel()

	// Model should initialize with BoardView as the active view
	if m.activeView != BoardView {
		t.Errorf("expected activeView to be BoardView, got %v", m.activeView)
	}

	// Init should return a tick command for periodic refresh
	cmd := m.Init()
	if cmd == nil {
		t.Error("expected Init() to return a tick command, got nil")
	}
}

// TestRobotModeJSON verifies that robotMode fetches beads and writes JSON to stdout.
func TestRobotModeJSON(t *testing.T) {
	// Capture stdout
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	// Use a stub fetchFn that returns a known bead
	stubBeads := []protocol.Bead{
		{ID: "test-1", Title: "Test Bead", Status: "open", Priority: 2, Type: "task"},
	}
	err = robotModeWithFetch(context.Background(), func(ctx context.Context) ([]protocol.Bead, error) {
		return stubBeads, nil
	})

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("failed to close pipe: %v", closeErr)
	}
	os.Stdout = origStdout

	if err != nil {
		t.Fatalf("robotModeWithFetch returned error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var beads []protocol.Bead
	if jsonErr := json.Unmarshal([]byte(output), &beads); jsonErr != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", jsonErr, output)
	}

	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	if beads[0].ID != "test-1" {
		t.Errorf("expected bead ID test-1, got %s", beads[0].ID)
	}
}

// TestRobotModeJSON_EmptyBeads verifies that an empty bead list produces a valid empty JSON array.
func TestRobotModeJSON_EmptyBeads(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	err = robotModeWithFetch(context.Background(), func(ctx context.Context) ([]protocol.Bead, error) {
		return nil, nil
	})

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("failed to close pipe: %v", closeErr)
	}
	os.Stdout = origStdout

	if err != nil {
		t.Fatalf("robotModeWithFetch returned error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var beads []protocol.Bead
	if jsonErr := json.Unmarshal([]byte(output), &beads); jsonErr != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", jsonErr, output)
	}

	if beads == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(beads) != 0 {
		t.Errorf("expected 0 beads, got %d", len(beads))
	}
}

// TestRobotModeJSON_FetchError verifies that a fetch error produces a JSON error object.
func TestRobotModeJSON_FetchError(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	fetchErr := context.DeadlineExceeded
	returnedErr := robotModeWithFetch(context.Background(), func(ctx context.Context) ([]protocol.Bead, error) {
		return nil, fetchErr
	})

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("failed to close pipe: %v", closeErr)
	}
	os.Stdout = origStdout

	if returnedErr == nil {
		t.Fatal("expected error, got nil")
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var errObj map[string]string
	if jsonErr := json.Unmarshal([]byte(output), &errObj); jsonErr != nil {
		t.Fatalf("error output is not valid JSON: %v\noutput: %q", jsonErr, output)
	}

	if errObj["error"] == "" {
		t.Error("expected 'error' field in JSON error object")
	}
}
