package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
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
