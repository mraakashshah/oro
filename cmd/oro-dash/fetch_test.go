package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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

// --- convertWorkerEntries ---

func TestConvertWorkerEntries_Empty(t *testing.T) {
	result := convertWorkerEntries(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 workers, got %d", len(result))
	}
}

func TestConvertWorkerEntries_MapsAllFields(t *testing.T) {
	entries := []workerEntry{
		{
			ID:               "w-alpha",
			State:            "working",
			BeadID:           "oro-001",
			LastProgressSecs: 12.5,
			ContextPct:       42,
		},
		{
			ID:               "w-beta",
			State:            "idle",
			BeadID:           "",
			LastProgressSecs: 0,
			ContextPct:       0,
		},
	}

	workers := convertWorkerEntries(entries)

	if len(workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(workers))
	}

	w0 := workers[0]
	if w0.ID != "w-alpha" {
		t.Errorf("w0.ID = %q, want %q", w0.ID, "w-alpha")
	}
	if w0.Status != "working" {
		t.Errorf("w0.Status = %q, want %q", w0.Status, "working")
	}
	if w0.BeadID != "oro-001" {
		t.Errorf("w0.BeadID = %q, want %q", w0.BeadID, "oro-001")
	}
	if w0.LastProgressSecs != 12.5 {
		t.Errorf("w0.LastProgressSecs = %v, want 12.5", w0.LastProgressSecs)
	}
	if w0.ContextPct != 42 {
		t.Errorf("w0.ContextPct = %d, want 42", w0.ContextPct)
	}

	w1 := workers[1]
	if w1.ID != "w-beta" {
		t.Errorf("w1.ID = %q, want %q", w1.ID, "w-beta")
	}
	if w1.Status != "idle" {
		t.Errorf("w1.Status = %q, want %q", w1.Status, "idle")
	}
	if w1.BeadID != "" {
		t.Errorf("w1.BeadID = %q, want empty", w1.BeadID)
	}
}

// --- invertAssignments ---

func TestInvertAssignments_Empty(t *testing.T) {
	result := invertAssignments(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestInvertAssignments_FlipsKeyValue(t *testing.T) {
	// Input: workerID -> beadID
	m := map[string]string{
		"w-alpha": "oro-001",
		"w-beta":  "oro-002",
	}

	inv := invertAssignments(m)

	if len(inv) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(inv))
	}
	if inv["oro-001"] != "w-alpha" {
		t.Errorf("inv[%q] = %q, want %q", "oro-001", inv["oro-001"], "w-alpha")
	}
	if inv["oro-002"] != "w-beta" {
		t.Errorf("inv[%q] = %q, want %q", "oro-002", inv["oro-002"], "w-beta")
	}
}

func TestInvertAssignments_SingleEntry(t *testing.T) {
	inv := invertAssignments(map[string]string{"worker-1": "bead-42"})
	if inv["bead-42"] != "worker-1" {
		t.Errorf("inv[%q] = %q, want %q", "bead-42", inv["bead-42"], "worker-1")
	}
}

// --- fetchBeadsWithStatus ---

// writeFakeBdEcho writes a fake bd that echoes a bead array for any status, or
// returns an error JSON when called with "--fail" as the status argument.
func writeFakeBdEcho(t *testing.T, fakeBin string) {
	t.Helper()
	script := `#!/bin/sh
prev=""
for arg in "$@"; do
  if [ "$prev" = "--status" ]; then
    if [ "$arg" = "fail" ]; then
      echo "error output" >&2
      exit 1
    fi
    printf '[{"id":"oro-s1","title":"Status bead","status":"%s","issue_type":"task"}]\n' "$arg"
    exit 0
  fi
  prev="$arg"
done
# no --status: return two beads for empty-status call
echo '[{"id":"oro-a","title":"All beads","issue_type":"task"},{"id":"oro-b","title":"Second","issue_type":"bug"}]'
`
	path := filepath.Join(fakeBin, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test-only executable stub
		t.Fatalf("write fake bd echo: %v", err)
	}
}

func TestFetchBeadsWithStatus_SpecificStatus(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBdEcho(t, fakeBin)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	beads, err := fetchBeadsWithStatus(context.Background(), "open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	if beads[0].Status != "open" {
		t.Errorf("bead status = %q, want %q", beads[0].Status, "open")
	}
}

func TestFetchBeadsWithStatus_EmptyStatus(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBdEcho(t, fakeBin)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	// Empty status → calls bd list --json (no --status flag) → 2 beads
	beads, err := fetchBeadsWithStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(beads) != 2 {
		t.Errorf("expected 2 beads for empty status, got %d", len(beads))
	}
}

func TestFetchBeadsWithStatus_BdError(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBdEcho(t, fakeBin)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	// "fail" status triggers non-zero exit from fake bd
	_, err := fetchBeadsWithStatus(context.Background(), "fail")
	if err == nil {
		t.Fatal("expected error when bd exits non-zero, got nil")
	}
}

// --- fetchBeads ---

func TestFetchBeads_ReturnsBeadsFromAllStatuses(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBdMultiStatus(t, fakeBin)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	beads, err := fetchBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// fetchBeads calls 4 statuses; fake bd returns 1 bead per status call.
	if len(beads) != 4 {
		t.Errorf("expected 4 beads (one per status), got %d", len(beads))
	}
}

func TestFetchBeads_SkipsStatusOnError(t *testing.T) {
	fakeBin := t.TempDir()
	// Write a bd that always exits non-zero.
	script := "#!/bin/sh\nexit 1\n"
	path := filepath.Join(fakeBin, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test-only executable stub
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	beads, err := fetchBeads(context.Background())
	// fetchBeads silently skips errors and returns nil error.
	if err != nil {
		t.Errorf("expected nil error when all statuses fail, got: %v", err)
	}
	if len(beads) != 0 {
		t.Errorf("expected 0 beads when all statuses fail, got %d", len(beads))
	}
}

// --- fetchWorkerStatus (via UDS) ---

// runMockStatusDispatcher starts a UDS listener that accepts one connection,
// reads a DIRECTIVE message with op=status, and responds with statusJSON.
func runMockStatusDispatcher(t *testing.T, sockPath, statusJSON string, ready chan<- struct{}) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock status dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	close(ready)

	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}

	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		return
	}

	if msg.Type != protocol.MsgDirective || msg.Directive == nil || msg.Directive.Op != "status" {
		return
	}

	ack := protocol.Message{
		Type: protocol.MsgACK,
		ACK: &protocol.ACKPayload{
			OK:     true,
			Detail: statusJSON,
		},
	}
	data, _ := json.Marshal(ack)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

func TestFetchWorkerStatus_LiveSocket(t *testing.T) {
	sockPath := os.TempDir() + "/ws.sock"
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	statusJSON := `{
		"state":"running",
		"workers":[
			{"id":"w-1","state":"working","bead_id":"oro-x1","last_progress_secs":5.0,"context_pct":30},
			{"id":"w-2","state":"idle","last_progress_secs":0,"context_pct":0}
		],
		"worker_count":2,
		"assignments":{"w-1":"oro-x1"},
		"focused_epic":"epic-99"
	}`

	ready := make(chan struct{})
	go runMockStatusDispatcher(t, sockPath, statusJSON, ready)
	<-ready

	workers, assignments, focusedEpic, err := fetchWorkerStatus(context.Background(), sockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(workers))
	}
	if workers[0].ID != "w-1" {
		t.Errorf("workers[0].ID = %q, want %q", workers[0].ID, "w-1")
	}
	if workers[0].Status != "working" {
		t.Errorf("workers[0].Status = %q, want %q", workers[0].Status, "working")
	}
	if workers[0].BeadID != "oro-x1" {
		t.Errorf("workers[0].BeadID = %q, want %q", workers[0].BeadID, "oro-x1")
	}
	if workers[0].ContextPct != 30 {
		t.Errorf("workers[0].ContextPct = %d, want 30", workers[0].ContextPct)
	}

	// assignments is inverted: beadID -> workerID
	if assignments["oro-x1"] != "w-1" {
		t.Errorf("assignments[%q] = %q, want %q", "oro-x1", assignments["oro-x1"], "w-1")
	}

	if focusedEpic != "epic-99" {
		t.Errorf("focusedEpic = %q, want %q", focusedEpic, "epic-99")
	}
}

// --- fetchWorkerOutput ---

// runMockWorkerOutputDispatcher accepts one connection, reads a worker-logs
// DIRECTIVE, and replies with the provided output lines as a newline-joined
// ACK detail string.
func runMockWorkerOutputDispatcher(t *testing.T, sockPath string, outputLines []string, ready chan<- struct{}) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock worker-output dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	close(ready)

	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}

	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		return
	}

	if msg.Type != protocol.MsgDirective || msg.Directive == nil || msg.Directive.Op != "worker-logs" {
		return
	}

	detail := ""
	for i, line := range outputLines {
		if i > 0 {
			detail += "\n"
		}
		detail += line
	}

	ack := protocol.Message{
		Type: protocol.MsgACK,
		ACK: &protocol.ACKPayload{
			OK:     true,
			Detail: detail,
		},
	}
	data, _ := json.Marshal(ack)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

func TestFetchWorkerOutput_Offline(t *testing.T) {
	ctx := context.Background()
	_, err := fetchWorkerOutput(ctx, "/nonexistent/socket/wo.sock", "w-1", 10)
	if err == nil {
		t.Fatal("expected error for offline dispatcher, got nil")
	}
}

func TestFetchWorkerOutput_LiveSocket(t *testing.T) {
	sockPath := os.TempDir() + "/wo.sock"
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	expectedLines := []string{"line one", "line two", "line three"}

	ready := make(chan struct{})
	go runMockWorkerOutputDispatcher(t, sockPath, expectedLines, ready)
	<-ready

	lines, err := fetchWorkerOutput(context.Background(), sockPath, "w-1", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) != len(expectedLines) {
		t.Fatalf("expected %d lines, got %d", len(expectedLines), len(lines))
	}
	for i, want := range expectedLines {
		if lines[i] != want {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], want)
		}
	}
}

func TestFetchWorkerOutput_EmptyOutput(t *testing.T) {
	sockPath := os.TempDir() + "/wo2.sock"
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	ready := make(chan struct{})
	go runMockWorkerOutputDispatcher(t, sockPath, []string{}, ready)
	<-ready

	lines, err := fetchWorkerOutput(context.Background(), sockPath, "w-idle", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty output, got %d", len(lines))
	}
}
