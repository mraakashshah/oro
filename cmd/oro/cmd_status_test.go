package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestStatusCmd_Stopped(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "nonexistent.sock"))

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "stopped") {
		t.Errorf("output should contain 'stopped', got: %q", got)
	}
}

func TestStatusCmd_Stale(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "nonexistent.sock"))

	// Write a PID that doesn't correspond to a running process.
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "stale") {
		t.Errorf("output should contain 'stale', got: %q", got)
	}
}

func TestStatusCmd_RunningWithDispatcherInfo(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	// Write our own PID so DaemonStatus returns "running".
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer os.Remove(pidFile)

	t.Setenv("ORO_PID_PATH", pidFile)

	// Use /tmp for socket to stay under macOS 104-char UDS path limit.
	sockPath := fmt.Sprintf("/tmp/oro-st-%d.sock", time.Now().UnixNano())
	defer os.Remove(sockPath)
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	statusJSON := `{"state":"running","worker_count":3,"queue_depth":2,"assignments":{"worker-1":"oro-abc","worker-2":"oro-def"},"focused_epic":"epic-42","workers":[{"id":"worker-1","state":"busy","bead_id":"oro-abc","last_progress_secs":10},{"id":"worker-2","state":"busy","bead_id":"oro-def","last_progress_secs":5},{"id":"worker-3","state":"idle","bead_id":"","last_progress_secs":0}],"active_count":2,"idle_count":1,"target_count":3,"progress_timeout_secs":600}`

	ready := make(chan struct{})
	go runMockStatusDispatcher(ctx, t, sockPath, statusJSON, ready)

	// Wait for socket to be ready.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for mock dispatcher")
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}

	got := buf.String()

	// Should show PID info.
	if !strings.Contains(got, fmt.Sprintf("PID %d", os.Getpid())) {
		t.Errorf("output should contain PID info, got: %q", got)
	}

	// Should show state.
	if !strings.Contains(got, "running") {
		t.Errorf("output should contain state 'running', got: %q", got)
	}

	// Should show enriched worker count.
	if !strings.Contains(got, "2 active") {
		t.Errorf("output should contain '2 active', got: %q", got)
	}
	if !strings.Contains(got, "target: 3") {
		t.Errorf("output should contain 'target: 3', got: %q", got)
	}

	// Should show in_progress beads with assignments.
	if !strings.Contains(got, "in_progress beads:") {
		t.Errorf("output should contain 'in_progress beads:', got: %q", got)
	}
	if !strings.Contains(got, "oro-abc") {
		t.Errorf("output should contain bead assignment 'oro-abc', got: %q", got)
	}
	if !strings.Contains(got, "oro-def") {
		t.Errorf("output should contain bead assignment 'oro-def', got: %q", got)
	}

	// Should show focused epic.
	if !strings.Contains(got, "epic-42") {
		t.Errorf("output should contain focused epic 'epic-42', got: %q", got)
	}
}

func TestStatusCmd_RunningNoSocket(t *testing.T) {
	// When the dispatcher is running but we can't connect to the socket,
	// should still show PID info gracefully.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer os.Remove(pidFile)

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "nonexistent.sock"))

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status command should not fail: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, fmt.Sprintf("PID %d", os.Getpid())) {
		t.Errorf("output should contain PID info, got: %q", got)
	}
	// Should indicate socket connection failed gracefully.
	if !strings.Contains(got, "dispatcher detail unavailable") {
		t.Errorf("output should indicate dispatcher detail unavailable, got: %q", got)
	}
}

func TestStatusCmd_RunningEmptyAssignments(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer os.Remove(pidFile)

	t.Setenv("ORO_PID_PATH", pidFile)

	// Use /tmp for socket to stay under macOS 104-char UDS path limit.
	sockPath := fmt.Sprintf("/tmp/oro-st-%d.sock", time.Now().UnixNano())
	defer os.Remove(sockPath)
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	statusJSON := `{"state":"inert","worker_count":0,"queue_depth":0,"assignments":{}}`

	ready := make(chan struct{})
	go runMockStatusDispatcher(ctx, t, sockPath, statusJSON, ready)

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for mock dispatcher")
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "inert") {
		t.Errorf("output should contain state 'inert', got: %q", got)
	}
	if !strings.Contains(got, "0") {
		t.Errorf("output should contain worker count '0', got: %q", got)
	}
}

func TestFormatStatusResponse(t *testing.T) {
	tests := []struct {
		name   string
		resp   statusResponse
		checks []string
	}{
		{
			name: "full response",
			resp: statusResponse{
				State:       "running",
				WorkerCount: 3,
				QueueDepth:  2,
				Assignments: map[string]string{
					"worker-1": "oro-abc",
				},
				FocusedEpic:         "epic-42",
				ActiveCount:         2,
				IdleCount:           1,
				TargetCount:         3,
				ProgressTimeoutSecs: 600,
			},
			checks: []string{
				"state:       running",
				"workers:     2 active",
				"queue:       2 ready",
				"focus:       epic-42",
			},
		},
		{
			name: "no assignments no focus",
			resp: statusResponse{
				State:               "paused",
				WorkerCount:         1,
				QueueDepth:          0,
				Assignments:         map[string]string{},
				ActiveCount:         1,
				IdleCount:           0,
				TargetCount:         1,
				ProgressTimeoutSecs: 600,
			},
			checks: []string{
				"state:       paused",
				"workers:     1 active",
				"queue:       0 ready",
			},
		},
		{
			name: "zero workers shows 0 active",
			resp: statusResponse{
				State:               "running",
				WorkerCount:         0,
				QueueDepth:          3,
				ProgressTimeoutSecs: 600,
			},
			checks: []string{
				"workers:     0 active",
				"in_progress beads: none",
			},
		},
		{
			name: "workers present but none busy shows in_progress none",
			resp: statusResponse{
				State:       "running",
				WorkerCount: 2,
				QueueDepth:  1,
				Workers: []workerStatus{
					{ID: "worker-1", State: "idle"},
					{ID: "worker-2", State: "idle"},
				},
				IdleCount:           2,
				TargetCount:         2,
				ProgressTimeoutSecs: 600,
			},
			checks: []string{
				"workers:     0 active",
				"in_progress beads: none",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			formatStatusResponse(&buf, &tt.resp)
			got := buf.String()
			for _, check := range tt.checks {
				if !strings.Contains(got, check) {
					t.Errorf("output should contain %q, got:\n%s", check, got)
				}
			}
		})
	}
}

func TestParseStatusFromACK(t *testing.T) {
	tests := []struct {
		name    string
		detail  string
		wantErr bool
		check   func(*testing.T, *statusResponse)
	}{
		{
			name:   "valid JSON",
			detail: `{"state":"running","worker_count":2,"queue_depth":1,"assignments":{"w1":"b1"}}`,
			check: func(t *testing.T, r *statusResponse) {
				t.Helper()
				if r.State != "running" {
					t.Errorf("state = %q, want running", r.State)
				}
				if r.WorkerCount != 2 {
					t.Errorf("worker_count = %d, want 2", r.WorkerCount)
				}
				if r.Assignments["w1"] != "b1" {
					t.Errorf("assignments[w1] = %q, want b1", r.Assignments["w1"])
				}
			},
		},
		{
			name:    "invalid JSON",
			detail:  "not json",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := parseStatusFromACK(tt.detail)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

func TestFormatStatusResponse_DefaultWithAlerts(t *testing.T) {
	resp := statusResponse{
		State:       "running",
		PID:         1234,
		WorkerCount: 3,
		QueueDepth:  5,
		Assignments: map[string]string{
			"worker-1": "oro-abc",
			"worker-3": "oro-xyz",
		},
		FocusedEpic: "epic-42",
		Workers: []workerStatus{
			{ID: "worker-1", State: "busy", BeadID: "oro-abc", LastProgressSecs: 120},
			{ID: "worker-2", State: "idle", BeadID: "", LastProgressSecs: 0},
			{ID: "worker-3", State: "busy", BeadID: "oro-xyz", LastProgressSecs: 900}, // exceeds half of 15min=900s
		},
		ActiveCount:         2,
		IdleCount:           1,
		TargetCount:         5,
		UptimeSeconds:       3600,
		PendingHandoffCount: 0,
		AttemptCounts:       map[string]int{"oro-xyz": 3},
		ProgressTimeoutSecs: 600, // 10 minutes
	}

	var buf bytes.Buffer
	formatStatusResponse(&buf, &resp)
	got := buf.String()

	// Should have ALERTS section for stuck worker (600s >= 600/2=300)
	if !strings.Contains(got, "ALERTS") {
		t.Errorf("expected ALERTS section, got:\n%s", got)
	}
	if !strings.Contains(got, "worker-3") {
		t.Errorf("expected stuck worker alert for worker-3, got:\n%s", got)
	}

	// Should have QG failure alert for oro-xyz (3 attempts)
	if !strings.Contains(got, "oro-xyz") && !strings.Contains(got, "QG") {
		t.Errorf("expected QG failure alert for oro-xyz, got:\n%s", got)
	}

	// Should show worker summary with active/idle/target
	if !strings.Contains(got, "2 active") {
		t.Errorf("expected '2 active' in worker summary, got:\n%s", got)
	}
	if !strings.Contains(got, "1 idle") {
		t.Errorf("expected '1 idle' in worker summary, got:\n%s", got)
	}
	if !strings.Contains(got, "target: 5") {
		t.Errorf("expected 'target: 5' in worker summary, got:\n%s", got)
	}

	// Should show queue depth
	if !strings.Contains(got, "5") {
		t.Errorf("expected queue depth '5', got:\n%s", got)
	}

	// Should use "in_progress beads:" label (not "active beads:")
	if !strings.Contains(got, "in_progress beads:") {
		t.Errorf("expected 'in_progress beads:' section header, got:\n%s", got)
	}
}

func TestFormatStatusResponse_JSONFlag(t *testing.T) {
	resp := statusResponse{
		State:       "running",
		PID:         1234,
		WorkerCount: 2,
		QueueDepth:  3,
		Assignments: map[string]string{"worker-1": "oro-abc"},
		Workers: []workerStatus{
			{ID: "worker-1", State: "busy", BeadID: "oro-abc", LastProgressSecs: 30},
		},
		ActiveCount:         1,
		IdleCount:           1,
		TargetCount:         5,
		UptimeSeconds:       600,
		ProgressTimeoutSecs: 600,
	}

	var buf bytes.Buffer
	formatStatusJSON(&buf, &resp)
	got := buf.String()

	// Must be valid JSON
	var parsed statusResponse
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, got)
	}

	// Verify key fields survived round-trip
	if parsed.State != "running" {
		t.Errorf("expected state 'running', got %q", parsed.State)
	}
	if parsed.ActiveCount != 1 {
		t.Errorf("expected active_count=1, got %d", parsed.ActiveCount)
	}
	if len(parsed.Workers) != 1 {
		t.Errorf("expected 1 worker in JSON, got %d", len(parsed.Workers))
	}
}

func TestFormatStatusResponse_VerboseFlag(t *testing.T) {
	resp := statusResponse{
		State:       "running",
		PID:         1234,
		WorkerCount: 2,
		QueueDepth:  1,
		Assignments: map[string]string{"worker-1": "oro-abc"},
		Workers: []workerStatus{
			{ID: "worker-1", State: "busy", BeadID: "oro-abc", LastProgressSecs: 45},
			{ID: "worker-2", State: "idle", BeadID: "", LastProgressSecs: 0},
		},
		ActiveCount:         1,
		IdleCount:           1,
		TargetCount:         5,
		UptimeSeconds:       1800,
		PendingHandoffCount: 1,
		AttemptCounts:       map[string]int{"oro-abc": 2},
		ProgressTimeoutSecs: 600,
	}

	var buf bytes.Buffer
	formatStatusVerbose(&buf, &resp)
	got := buf.String()

	// Should include worker health table with IDs and states
	if !strings.Contains(got, "worker-1") {
		t.Errorf("expected worker-1 in verbose output, got:\n%s", got)
	}
	if !strings.Contains(got, "worker-2") {
		t.Errorf("expected worker-2 in verbose output, got:\n%s", got)
	}

	// Should show attempt counts
	if !strings.Contains(got, "oro-abc") {
		t.Errorf("expected attempt count for oro-abc, got:\n%s", got)
	}

	// Should show uptime
	if !strings.Contains(got, "30m") || !strings.Contains(got, "uptime") {
		t.Errorf("expected uptime '30m' in verbose output, got:\n%s", got)
	}

	// Should show pending handoffs
	if !strings.Contains(got, "handoff") {
		t.Errorf("expected pending handoff info in verbose output, got:\n%s", got)
	}
}

// TestFormatAlerts_HeartbeatingWorkerNotFlagged verifies that a worker with a
// recent heartbeat is NOT shown in alerts as slow/stuck, even if LastProgressSecs
// exceeds the threshold.
func TestFormatAlerts_HeartbeatingWorkerNotFlagged(t *testing.T) {
	resp := statusResponse{
		ProgressTimeoutSecs: 600, // halfTimeout = 300
		Workers: []workerStatus{
			// Would trigger "slow" alert without heartbeat check (400 > 300).
			{ID: "worker-1", State: "busy", BeadID: "bead-abc", LastProgressSecs: 400, LastHeartbeatSecs: 5},
		},
	}

	var buf bytes.Buffer
	hadAlerts := formatAlerts(&buf, &resp)
	got := buf.String()

	if hadAlerts {
		t.Errorf("expected no alerts for heartbeating worker, but got alerts:\n%s", got)
	}
	if strings.Contains(got, "worker-1") {
		t.Errorf("heartbeating worker should not appear in alerts, got:\n%s", got)
	}
}

// TestFormatAlerts_StuckWorkerWithStaleHeartbeatStillFlagged verifies that a worker
// with stale progress AND stale heartbeat is still flagged in alerts.
func TestFormatAlerts_StuckWorkerWithStaleHeartbeatStillFlagged(t *testing.T) {
	resp := statusResponse{
		ProgressTimeoutSecs: 600, // halfTimeout = 300
		Workers: []workerStatus{
			// Both progress and heartbeat are stale: should still alert.
			{ID: "worker-1", State: "busy", BeadID: "bead-abc", LastProgressSecs: 400, LastHeartbeatSecs: 400},
		},
	}

	var buf bytes.Buffer
	hadAlerts := formatAlerts(&buf, &resp)
	got := buf.String()

	if !hadAlerts {
		t.Errorf("expected alerts for stuck worker (stale heartbeat), but got none")
	}
	if !strings.Contains(got, "worker-1") {
		t.Errorf("stuck worker should appear in alerts, got:\n%s", got)
	}
}

// TestFormatInProgressBeads_HeartbeatingWorkerShowsHealthy verifies that a worker
// emitting heartbeats is labeled "healthy" even when LastProgressSecs is high.
func TestFormatInProgressBeads_HeartbeatingWorkerShowsHealthy(t *testing.T) {
	resp := statusResponse{
		ProgressTimeoutSecs: 600, // halfTimeout = 300
		Workers: []workerStatus{
			// Without heartbeat check this would be "slow"; with it, "healthy".
			{ID: "worker-1", State: "busy", BeadID: "bead-abc", LastProgressSecs: 400, LastHeartbeatSecs: 5},
		},
	}

	var buf bytes.Buffer
	formatInProgressBeads(&buf, &resp)
	got := buf.String()

	if strings.Contains(got, "slow") {
		t.Errorf("heartbeating worker should not be 'slow', got:\n%s", got)
	}
	if strings.Contains(got, "STUCK") {
		t.Errorf("heartbeating worker should not be 'STUCK', got:\n%s", got)
	}
	if !strings.Contains(got, "healthy") {
		t.Errorf("heartbeating worker should be 'healthy', got:\n%s", got)
	}
}

// TestStatusDisplay_HealthyWithRecentHeartbeat verifies end-to-end that a worker
// with stale progress but recent heartbeat shows "healthy" in full status output.
func TestStatusDisplay_HealthyWithRecentHeartbeat(t *testing.T) {
	resp := statusResponse{
		State:               "running",
		WorkerCount:         1,
		ProgressTimeoutSecs: 600, // halfTimeout = 300
		Workers: []workerStatus{
			{ID: "worker-1", State: "busy", BeadID: "oro-abc", LastProgressSecs: 400, LastHeartbeatSecs: 50},
		},
		ActiveCount: 1,
		TargetCount: 1,
	}

	var buf bytes.Buffer
	formatStatusResponse(&buf, &resp)
	got := buf.String()

	if !strings.Contains(got, "healthy") {
		t.Errorf("expected health label 'healthy', got:\n%s", got)
	}
	if strings.Contains(got, "slow") {
		t.Errorf("should not show 'slow' when heartbeat is recent, got:\n%s", got)
	}
}

// TestStatusCommand verifies that the header active count matches the number of
// table rows (busy + reviewing). Reviewing workers must appear in both.
func TestStatusCommand(t *testing.T) {
	// Dispatcher may return active_count=1 (only counting 'busy'), but we have
	// 1 busy + 1 reviewing + 1 idle. The header must show 2 active, 1 idle.
	resp := statusResponse{
		State:       "running",
		WorkerCount: 3,
		QueueDepth:  0,
		Workers: []workerStatus{
			{ID: "worker-1", State: "busy", BeadID: "oro-abc", LastProgressSecs: 10},
			{ID: "worker-2", State: "reviewing", BeadID: "oro-def", LastProgressSecs: 5},
			{ID: "worker-3", State: "idle", BeadID: "", LastProgressSecs: 0},
		},
		// Simulate dispatcher providing only busy count (the stale value).
		ActiveCount:         1,
		IdleCount:           2,
		TargetCount:         3,
		ProgressTimeoutSecs: 600,
	}

	var buf bytes.Buffer
	formatStatusResponse(&buf, &resp)
	got := buf.String()

	// Header must count busy + reviewing = 2 active.
	if !strings.Contains(got, "2 active") {
		t.Errorf("header should show '2 active' (busy+reviewing), got:\n%s", got)
	}
	if !strings.Contains(got, "1 idle") {
		t.Errorf("header should show '1 idle', got:\n%s", got)
	}

	// Table must include both busy and reviewing workers.
	if !strings.Contains(got, "worker-1") {
		t.Errorf("table should contain busy worker-1, got:\n%s", got)
	}
	if !strings.Contains(got, "worker-2") {
		t.Errorf("table should contain reviewing worker-2, got:\n%s", got)
	}
	// Idle worker must not appear in in-progress table.
	if strings.Contains(got, "worker-3") {
		t.Errorf("idle worker-3 should not appear in in-progress table, got:\n%s", got)
	}
}

// runMockStatusDispatcher starts a UDS listener that accepts multiple connections,
// reads a DIRECTIVE message with op=status, and sends an ACK with the given
// status JSON as the detail. Handles multiple connections (probeSocket + queryDispatcherStatus).
func runMockStatusDispatcher(ctx context.Context, t *testing.T, sockPath, statusJSON string, ready chan<- struct{}) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	// Signal that the socket is ready.
	close(ready)

	for {
		connCh := make(chan net.Conn, 1)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connCh <- conn
		}()

		select {
		case conn := <-connCh:
			func() {
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
			}()

		case <-ctx.Done():
			return
		}
	}
}
