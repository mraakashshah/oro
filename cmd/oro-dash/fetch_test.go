package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	ds, err := fetchWorkerStatus(ctx, "/nonexistent/socket/path.sock")
	if err != nil {
		t.Fatalf("expected no error for offline dispatcher, got: %v", err)
	}
	if ds != nil {
		t.Fatalf("expected nil dispatcherStatus for offline dispatcher, got %+v", ds)
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

// --- fetchBeadsWithStatus: sort order ---

// writeFakeBdArgCapture writes a fake bd that dumps all received args
// to a capture file, then echoes a minimal bead array.
func writeFakeBdArgCapture(t *testing.T, fakeBin, captureFile string) {
	t.Helper()
	// Each invocation appends a line with all args to the capture file.
	script := `#!/bin/sh
echo "$@" >> "` + captureFile + `"
prev=""
for arg in "$@"; do
  if [ "$prev" = "--status" ]; then
    printf '[{"id":"oro-cap","title":"Captured","status":"%s","issue_type":"task"}]\n' "$arg"
    exit 0
  fi
  prev="$arg"
done
echo '[]'
`
	path := filepath.Join(fakeBin, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test-only executable stub
		t.Fatalf("write fake bd arg-capture: %v", err)
	}
}

func TestFetchBeads_ClosedSortedByMostRecent(t *testing.T) {
	fakeBin := t.TempDir()
	captureFile := filepath.Join(t.TempDir(), "args.log")
	writeFakeBdArgCapture(t, fakeBin, captureFile)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	_, err := fetchBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read captured args
	data, err := os.ReadFile(captureFile) //nolint:gosec // G304: captureFile is a test-controlled temp path
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}

	lines := splitNonEmpty(string(data))

	// Find the closed status call
	var closedArgs string
	for _, line := range lines {
		if containsAll(line, "--status", "closed") {
			closedArgs = line
			break
		}
	}
	if closedArgs == "" {
		t.Fatal("no bd call with --status closed found in captured args")
	}

	// Closed beads must be sorted by close date, most recent first
	if !containsAll(closedArgs, "--sort", "closed", "--reverse") {
		t.Errorf("closed fetch missing sort flags; got args: %s", closedArgs)
	}
}

func TestFetchBeads_PrioritySortForOpenStatuses(t *testing.T) {
	fakeBin := t.TempDir()
	captureFile := filepath.Join(t.TempDir(), "args.log")
	writeFakeBdArgCapture(t, fakeBin, captureFile)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	_, err := fetchBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read captured args
	data, err := os.ReadFile(captureFile) //nolint:gosec // G304: captureFile is a test-controlled temp path
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}

	lines := splitNonEmpty(string(data))

	// Check that open, in_progress, and blocked all have --sort priority
	statusesToCheck := []string{"open", "in_progress", "blocked"}
	for _, status := range statusesToCheck {
		var statusArgs string
		for _, line := range lines {
			if containsAll(line, "--status", status) {
				statusArgs = line
				break
			}
		}
		if statusArgs == "" {
			t.Fatalf("no bd call with --status %s found in captured args", status)
		}

		if !containsAll(statusArgs, "--sort", "priority") {
			t.Errorf("%s fetch missing --sort priority; got args: %s", status, statusArgs)
		}
	}
}

// splitNonEmpty splits s by newline and returns non-empty lines.
func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

// containsAll returns true if s contains all substrings.
func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
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

	ds, err := fetchWorkerStatus(context.Background(), sockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil dispatcherStatus")
	}

	if len(ds.workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(ds.workers))
	}
	if ds.workers[0].ID != "w-1" {
		t.Errorf("workers[0].ID = %q, want %q", ds.workers[0].ID, "w-1")
	}
	if ds.workers[0].Status != "working" {
		t.Errorf("workers[0].Status = %q, want %q", ds.workers[0].Status, "working")
	}
	if ds.workers[0].BeadID != "oro-x1" {
		t.Errorf("workers[0].BeadID = %q, want %q", ds.workers[0].BeadID, "oro-x1")
	}
	if ds.workers[0].ContextPct != 30 {
		t.Errorf("workers[0].ContextPct = %d, want 30", ds.workers[0].ContextPct)
	}

	// assignments is inverted: beadID -> workerID
	if ds.assignments["oro-x1"] != "w-1" {
		t.Errorf("assignments[%q] = %q, want %q", "oro-x1", ds.assignments["oro-x1"], "w-1")
	}

	if ds.focusedEpic != "epic-99" {
		t.Errorf("focusedEpic = %q, want %q", ds.focusedEpic, "epic-99")
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

// --- statusResponse enriched fields (oro-yqvn.3) ---

func TestStatusResponse_NewFields(t *testing.T) {
	t.Run("ParsesUptimeAndHandoffAndAttempts", func(t *testing.T) {
		jsonData := `{
			"state":"running",
			"workers":[],
			"worker_count":0,
			"assignments":{},
			"uptime_seconds":123.45,
			"pending_handoff_count":3,
			"attempt_counts":{"oro-a":2,"oro-b":1}
		}`

		var resp statusResponse
		if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if resp.UptimeSeconds != 123.45 {
			t.Errorf("UptimeSeconds = %v, want 123.45", resp.UptimeSeconds)
		}
		if resp.PendingHandoffCount != 3 {
			t.Errorf("PendingHandoffCount = %d, want 3", resp.PendingHandoffCount)
		}
		if len(resp.AttemptCounts) != 2 {
			t.Fatalf("AttemptCounts len = %d, want 2", len(resp.AttemptCounts))
		}
		if resp.AttemptCounts["oro-a"] != 2 {
			t.Errorf("AttemptCounts[oro-a] = %d, want 2", resp.AttemptCounts["oro-a"])
		}
	})

	t.Run("NewFieldsPropagatedViaWorkerDataMsg", func(t *testing.T) {
		sockPath := os.TempDir() + "/enriched.sock"
		_ = os.Remove(sockPath)
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		statusJSON := `{
			"state":"running",
			"workers":[{"id":"w-1","state":"idle","last_progress_secs":0,"context_pct":0}],
			"worker_count":1,
			"assignments":{},
			"uptime_seconds":99.9,
			"pending_handoff_count":2,
			"attempt_counts":{"oro-x":5}
		}`

		ready := make(chan struct{})
		go runMockStatusDispatcher(t, sockPath, statusJSON, ready)
		<-ready

		ds, err := fetchWorkerStatus(context.Background(), sockPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ds == nil {
			t.Fatal("expected non-nil dispatcherStatus")
		}
		if len(ds.workers) != 1 {
			t.Fatalf("expected 1 worker, got %d", len(ds.workers))
		}
		if ds.uptimeSeconds != 99.9 {
			t.Errorf("uptimeSeconds = %v, want 99.9", ds.uptimeSeconds)
		}
		if ds.pendingHandoffCount != 2 {
			t.Errorf("pendingHandoffCount = %d, want 2", ds.pendingHandoffCount)
		}
		if ds.attemptCounts["oro-x"] != 5 {
			t.Errorf("attemptCounts[oro-x] = %d, want 5", ds.attemptCounts["oro-x"])
		}
	})

	t.Run("ClosedCountComputedInBeadsMsg", func(t *testing.T) {
		m := newModel()
		beads := beadsMsg{
			{ID: "oro-1", Status: "open"},
			{ID: "oro-2", Status: "closed"},
			{ID: "oro-3", Status: "closed"},
			{ID: "oro-4", Status: "in_progress"},
		}
		updated, _ := m.Update(beads)
		um, ok := updated.(Model)
		if !ok {
			t.Fatal("expected Model type from Update")
		}

		if um.closedCount != 2 {
			t.Errorf("closedCount = %d, want 2", um.closedCount)
		}
		if um.openCount != 1 {
			t.Errorf("openCount = %d, want 1", um.openCount)
		}
		if um.inProgressCount != 1 {
			t.Errorf("inProgressCount = %d, want 1", um.inProgressCount)
		}
	})
}

// --- sample collection wiring (oro-yqvn.4) ---

func TestSampleCollection(t *testing.T) {
	t.Run("TickSetsSamplePending", func(t *testing.T) {
		m := newModel()
		m.metricsBuffer = NewMetricsBuffer()

		// Simulate tick message
		updated, _ := m.Update(tickMsg(time.Now()))
		um, ok := updated.(Model)
		if !ok {
			t.Fatal("expected Model type from Update")
		}
		if !um.samplePending {
			t.Error("samplePending should be true after tickMsg")
		}
	})

	t.Run("SampleRecordedAfterBothMsgs", func(t *testing.T) {
		m := newModel()
		m.metricsBuffer = NewMetricsBuffer()
		m.samplePending = true

		// Process beadsMsg first
		beads := beadsMsg{
			{ID: "oro-1", Status: "open"},
			{ID: "oro-2", Status: "in_progress"},
			{ID: "oro-3", Status: "closed"},
		}
		updated, _ := m.Update(beads)
		m = updated.(Model) //nolint:errcheck // test assertion

		// After beadsMsg alone, beadsReady but workers not yet
		if m.metricsBuffer.Len() != 0 {
			t.Error("sample should not be recorded until both messages arrive")
		}

		// Process workerDataMsg
		wdm := workerDataMsg{
			workers: []WorkerStatus{
				{ID: "w-1", Status: "working"},
				{ID: "w-2", Status: "idle"},
			},
		}
		updated, _ = m.Update(wdm)
		m = updated.(Model) //nolint:errcheck // test assertion

		// Now both have arrived; sample should be recorded
		if m.metricsBuffer.Len() != 1 {
			t.Errorf("expected 1 sample after both msgs, got %d", m.metricsBuffer.Len())
		}

		// Verify sample content
		samples := m.metricsBuffer.Last(1)
		s := samples[0]
		if s.QueueReady != 1 {
			t.Errorf("QueueReady = %d, want 1", s.QueueReady)
		}
		if s.QueueWIP != 1 {
			t.Errorf("QueueWIP = %d, want 1", s.QueueWIP)
		}
		if s.BeadsClosed != 1 {
			t.Errorf("BeadsClosed = %d, want 1", s.BeadsClosed)
		}
		if s.WorkersActive != 1 {
			t.Errorf("WorkersActive = %d, want 1", s.WorkersActive)
		}
		if s.WorkersIdle != 1 {
			t.Errorf("WorkersIdle = %d, want 1", s.WorkersIdle)
		}
		if s.WorkersTotal != 2 {
			t.Errorf("WorkersTotal = %d, want 2", s.WorkersTotal)
		}
	})

	t.Run("SamplePendingClearedAfterRecord", func(t *testing.T) {
		m := newModel()
		m.metricsBuffer = NewMetricsBuffer()
		m.samplePending = true
		m.beadsReady = true

		// workerDataMsg should record and clear
		wdm := workerDataMsg{
			workers: []WorkerStatus{{ID: "w-1", Status: "idle"}},
		}
		updated, _ := m.Update(wdm)
		m = updated.(Model) //nolint:errcheck // test assertion

		if m.samplePending {
			t.Error("samplePending should be false after sample recorded")
		}
		if m.beadsReady {
			t.Error("beadsReady should be false after sample recorded")
		}
	})

	t.Run("BuildCurrentSampleFields", func(t *testing.T) {
		m := newModel()
		m.openCount = 3
		m.inProgressCount = 2
		m.closedCount = 5
		m.workers = []WorkerStatus{
			{ID: "w-1", Status: "working", BeadID: "oro-1", ContextPct: 40},
			{ID: "w-2", Status: "idle", ContextPct: 0},
			{ID: "w-3", Status: "working", BeadID: "oro-2", ContextPct: 60},
		}

		s := m.buildCurrentSample()

		if s.QueueReady != 3 {
			t.Errorf("QueueReady = %d, want 3", s.QueueReady)
		}
		if s.QueueWIP != 2 {
			t.Errorf("QueueWIP = %d, want 2", s.QueueWIP)
		}
		if s.BeadsClosed != 5 {
			t.Errorf("BeadsClosed = %d, want 5", s.BeadsClosed)
		}
		if s.WorkersActive != 2 {
			t.Errorf("WorkersActive = %d, want 2", s.WorkersActive)
		}
		if s.WorkersIdle != 1 {
			t.Errorf("WorkersIdle = %d, want 1", s.WorkersIdle)
		}
		if s.WorkersTotal != 3 {
			t.Errorf("WorkersTotal = %d, want 3", s.WorkersTotal)
		}
		if len(s.Workers) != 3 {
			t.Fatalf("len(Workers) = %d, want 3", len(s.Workers))
		}
		if s.Workers[0].ContextPct != 40 {
			t.Errorf("Workers[0].ContextPct = %d, want 40", s.Workers[0].ContextPct)
		}
		if s.Timestamp.IsZero() {
			t.Error("Timestamp should not be zero")
		}
	})
}
