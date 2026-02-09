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

	statusJSON := `{"state":"running","worker_count":3,"queue_depth":2,"assignments":{"worker-1":"oro-abc","worker-2":"oro-def"},"focused_epic":"epic-42"}`

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

	// Should show worker count.
	if !strings.Contains(got, "3") {
		t.Errorf("output should contain worker count '3', got: %q", got)
	}

	// Should show assignments.
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
				FocusedEpic: "epic-42",
			},
			checks: []string{
				"state:       running",
				"workers:     3",
				"queue depth: 2",
				"focus:       epic-42",
				"worker-1",
				"oro-abc",
			},
		},
		{
			name: "no assignments no focus",
			resp: statusResponse{
				State:       "paused",
				WorkerCount: 1,
				QueueDepth:  0,
				Assignments: map[string]string{},
			},
			checks: []string{
				"state:       paused",
				"workers:     1",
				"queue depth: 0",
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

// runMockStatusDispatcher starts a UDS listener that accepts one connection,
// reads a DIRECTIVE message with op=status, and sends an ACK with the given
// status JSON as the detail.
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
		defer conn.Close()

		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			t.Error("failed to read line from connection")
			return
		}

		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Errorf("unmarshal message: %v", err)
			return
		}

		if msg.Type != protocol.MsgDirective {
			t.Errorf("unexpected message type: %s", msg.Type)
			return
		}

		if msg.Directive == nil || msg.Directive.Op != "status" {
			t.Errorf("expected status directive, got: %+v", msg.Directive)
			return
		}

		// Send ACK with status JSON as detail.
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

	case <-ctx.Done():
		return
	}
}
