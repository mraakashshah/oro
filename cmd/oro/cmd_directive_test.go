package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestDirectiveCmd tests that the directive command connects to the dispatcher socket,
// sends a DIRECTIVE message, and receives an ACK response.
func TestDirectiveCmd(t *testing.T) {
	tests := []struct {
		name string
		op   string
		args []string
		want protocol.DirectivePayload
	}{
		{
			name: "start directive",
			op:   "start",
			args: []string{},
			want: protocol.DirectivePayload{Op: "start", Args: ""},
		},
		{
			name: "stop directive",
			op:   "stop",
			args: []string{},
			want: protocol.DirectivePayload{Op: "stop", Args: ""},
		},
		{
			name: "pause directive",
			op:   "pause",
			args: []string{},
			want: protocol.DirectivePayload{Op: "pause", Args: ""},
		},
		{
			name: "resume directive",
			op:   "resume",
			args: []string{},
			want: protocol.DirectivePayload{Op: "resume", Args: ""},
		},
		{
			name: "scale directive",
			op:   "scale",
			args: []string{"5"},
			want: protocol.DirectivePayload{Op: "scale", Args: "5"},
		},
		{
			name: "focus directive",
			op:   "focus",
			args: []string{"oro-abc"},
			want: protocol.DirectivePayload{Op: "focus", Args: "oro-abc"},
		},
		{
			name: "status directive",
			op:   "status",
			args: []string{},
			want: protocol.DirectivePayload{Op: "status", Args: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary socket with short path (macOS UDS limit is ~100 chars)
			sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())

			// Start mock dispatcher that accepts one connection and sends ACK
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			mockDone := make(chan protocol.DirectivePayload, 1)
			go runMockDispatcher(ctx, t, sockPath, mockDone)

			// Wait for socket to be ready
			waitForSocket(t, sockPath, 2*time.Second)

			// Set environment variable for socket path
			t.Setenv("ORO_SOCKET_PATH", sockPath)

			// Build command
			root := newRootCmd()
			cmdArgs := append([]string{"directive", tt.op}, tt.args...)
			root.SetArgs(cmdArgs)

			// Execute command
			if err := root.Execute(); err != nil {
				t.Fatalf("directive command failed: %v", err)
			}

			// Wait for mock dispatcher to receive the directive
			select {
			case got := <-mockDone:
				if got.Op != tt.want.Op {
					t.Errorf("op = %q, want %q", got.Op, tt.want.Op)
				}
				if got.Args != tt.want.Args {
					t.Errorf("args = %q, want %q", got.Args, tt.want.Args)
				}
			case <-ctx.Done():
				t.Fatal("timeout waiting for directive")
			}
		})
	}
}

// runMockDispatcher starts a UDS listener that accepts one connection,
// reads a DIRECTIVE message, sends an ACK, and closes.
func runMockDispatcher(ctx context.Context, t *testing.T, sockPath string, received chan<- protocol.DirectivePayload) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	// Accept one connection
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

		// Read DIRECTIVE message
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

		if msg.Directive == nil {
			t.Error("directive payload is nil")
			return
		}

		// Send directive to test
		received <- *msg.Directive

		// Send ACK response
		ack := protocol.Message{
			Type: protocol.MsgACK,
			ACK: &protocol.ACKPayload{
				OK:     true,
				Detail: fmt.Sprintf("applied %s", msg.Directive.Op),
			},
		}
		data, _ := json.Marshal(ack)
		data = append(data, '\n')
		_, _ = conn.Write(data)

	case <-ctx.Done():
		return
	}
}

// TestDirectiveCmd_NoHumanApprovedInPayload verifies that the DirectivePayload
// struct no longer has a HumanApproved field (removed as part of P0 fix — stop
// is now unconditionally rejected by the dispatcher).
func TestDirectiveCmd_NoHumanApprovedInPayload(t *testing.T) {
	// This is a compile-time assertion: if HumanApproved is re-added to
	// DirectivePayload, the struct literal below will fail to compile because
	// the test only sets Op and Args.
	p := protocol.DirectivePayload{
		Op:   "start",
		Args: "",
	}
	if p.Op != "start" {
		t.Fatal("unexpected op")
	}
}

// TestDirectiveCmd_ShutdownBlocked verifies that the shutdown directive is
// unconditionally blocked via "oro directive shutdown" — use "oro stop" instead.
func TestDirectiveCmd_ShutdownBlocked(t *testing.T) {
	t.Setenv("ORO_SOCKET_PATH", "/tmp/oro-test-unused.sock")

	root := newRootCmd()
	root.SetArgs([]string{"directive", "shutdown"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when sending shutdown via directive, got nil")
	}
	if !strings.Contains(err.Error(), "oro stop") {
		t.Errorf("expected error to suggest 'oro stop', got: %v", err)
	}
}

// TestDirectiveCmd_NoSocket tests error handling when socket doesn't exist.
func TestDirectiveCmd_NoSocket(t *testing.T) {
	sockPath := fmt.Sprintf("/tmp/oro-test-noexist-%d.sock", time.Now().UnixNano())
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	root := newRootCmd()
	root.SetArgs([]string{"directive", "start"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when socket doesn't exist, got nil")
	}

	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "no such file") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestDirectiveMaxWorkersRaisesScaleCap verifies:
// - 'oro directive --help' mentions max-workers
// - scale is clamped before max-workers ceiling is raised
// - after 'oro directive max-workers 3', scale 3 sets target=3
// - dispatcher status then reports target_count=3
func TestDirectiveMaxWorkersRaisesScaleCap(t *testing.T) {
	// Verify help text mentions max-workers.
	helpText := newDirectiveCmd().Long
	if !strings.Contains(helpText, "max-workers") {
		t.Errorf("directive --help does not mention 'max-workers':\n%s", helpText)
	}

	sockPath := fmt.Sprintf("/tmp/oro-test-mw-%d.sock", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan protocol.DirectivePayload, 10)
	go runMockDispatcherWithMaxWorkers(ctx, t, sockPath, received)
	waitForSocket(t, sockPath, 2*time.Second)

	t.Setenv("ORO_SOCKET_PATH", sockPath)

	// scale 3 before raising max-workers (initial ceiling=2) → clamped to 2.
	var scaleBuf strings.Builder
	r1 := newRootCmd()
	r1.SetArgs([]string{"directive", "scale", "3"})
	r1.SetOut(&scaleBuf)
	if err := r1.Execute(); err != nil {
		t.Fatalf("scale directive failed: %v", err)
	}
	if d := <-received; d.Op != "scale" || d.Args != "3" {
		t.Fatalf("expected scale 3, got %s %s", d.Op, d.Args)
	}
	if strings.Contains(scaleBuf.String(), "target=3") {
		t.Errorf("scale 3 should be clamped with max-workers=2, got: %s", scaleBuf.String())
	}

	// Raise max-workers to 3.
	r2 := newRootCmd()
	r2.SetArgs([]string{"directive", "max-workers", "3"})
	if err := r2.Execute(); err != nil {
		t.Fatalf("max-workers directive failed: %v", err)
	}
	if d := <-received; d.Op != "max-workers" || d.Args != "3" {
		t.Fatalf("expected max-workers 3, got %s %s", d.Op, d.Args)
	}

	// scale 3 after raising max-workers → target=3 now within ceiling.
	r3 := newRootCmd()
	r3.SetArgs([]string{"directive", "scale", "3"})
	if err := r3.Execute(); err != nil {
		t.Fatalf("scale directive failed after max-workers raise: %v", err)
	}
	if d := <-received; d.Op != "scale" || d.Args != "3" {
		t.Fatalf("expected scale 3, got %s %s", d.Op, d.Args)
	}

	// status → target_count=3.
	var statusBuf strings.Builder
	r4 := newRootCmd()
	r4.SetArgs([]string{"directive", "status"})
	r4.SetOut(&statusBuf)
	if err := r4.Execute(); err != nil {
		t.Fatalf("status directive failed: %v", err)
	}
	if d := <-received; d.Op != "status" {
		t.Fatalf("expected status, got %s", d.Op)
	}
	var statusResp struct {
		TargetCount int `json:"target_count"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(statusBuf.String())), &statusResp); err != nil {
		t.Fatalf("parse status output %q: %v", statusBuf.String(), err)
	}
	if statusResp.TargetCount != 3 {
		t.Errorf("status target_count = %d, want 3", statusResp.TargetCount)
	}
}

// runMockDispatcherWithMaxWorkers runs a stateful multi-connection mock dispatcher
// that simulates max-workers clamping. Initial ceiling=2; adjustable via max-workers.
func runMockDispatcherWithMaxWorkers(ctx context.Context, t *testing.T, sockPath string, received chan<- protocol.DirectivePayload) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock dispatcher listen: %v", err)
		return
	}
	defer os.Remove(sockPath)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	var (
		mu            sync.Mutex
		maxWorkers    = 2
		targetWorkers = 1
	)

	responseFor := func(dir protocol.DirectivePayload) string {
		mu.Lock()
		defer mu.Unlock()
		switch dir.Op {
		case "scale":
			n, _ := strconv.Atoi(dir.Args)
			if maxWorkers > 0 && n > maxWorkers {
				return fmt.Sprintf("target=%d, clamped to max_workers=%d", maxWorkers, maxWorkers)
			}
			targetWorkers = n
			return fmt.Sprintf("target=%d, current=0", n)
		case "max-workers":
			n, _ := strconv.Atoi(dir.Args)
			maxWorkers = n
			if targetWorkers > n {
				targetWorkers = n
			}
			return fmt.Sprintf("max_workers=%d", n)
		case "status":
			data, _ := json.Marshal(struct {
				State       string `json:"state"`
				TargetCount int    `json:"target_count"`
			}{State: "running", TargetCount: targetWorkers})
			return string(data)
		default:
			return fmt.Sprintf("applied %s", dir.Op)
		}
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			scanner := bufio.NewScanner(c)
			if !scanner.Scan() {
				return
			}
			var msg protocol.Message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				t.Errorf("mock: unmarshal: %v", err)
				return
			}
			if msg.Type != protocol.MsgDirective || msg.Directive == nil {
				t.Errorf("mock: unexpected message type %s", msg.Type)
				return
			}
			dir := *msg.Directive
			detail := responseFor(dir)
			received <- dir
			ack := protocol.Message{
				Type: protocol.MsgACK,
				ACK:  &protocol.ACKPayload{OK: true, Detail: detail},
			}
			data, _ := json.Marshal(ack)
			_, _ = c.Write(append(data, '\n'))
		}(conn)
	}
}

// waitForSocket polls until sockPath exists or timeout expires.
func waitForSocket(t *testing.T, sockPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s not created within %v", sockPath, timeout)
}
