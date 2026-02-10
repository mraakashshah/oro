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

func TestStopGraceful(t *testing.T) {
	// Setup: PID file with our own PID (so DaemonStatus returns "running").
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	// Setup: mock UDS dispatcher that records directives.
	sockPath := fmt.Sprintf("/tmp/oro-stop-%d.sock", time.Now().UnixNano())
	defer os.Remove(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ops := make(chan string, 10)
	ready := make(chan struct{})
	go runMockStopDispatcher(ctx, t, sockPath, ops, ready)

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for mock dispatcher")
	}

	// Setup: fakeCmd to record tmux and bd calls.
	fake := newFakeCmd()

	// Track whether signal was sent.
	signaled := false

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: sockPath,
		tmuxName: "oro",
		runner:   fake,
		w:        &buf,
		signalFn: func(pid int) error { signaled = true; return nil },
		aliveFn:  func(pid int) bool { return false }, // process "exits" immediately
	}

	if err := runStopSequence(ctx, cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	// Assert: UDS "stop" directive was sent.
	select {
	case op := <-ops:
		if op != "stop" {
			t.Errorf("directive op = %q, want \"stop\"", op)
		}
	default:
		t.Fatal("no directive received by mock dispatcher")
	}

	// Assert: SIGTERM was sent.
	if !signaled {
		t.Error("expected signalFn to be called")
	}

	// Assert: tmux kill-session was called.
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Errorf("tmux kill-session not called; calls = %v", fake.calls)
	}

	// Assert: bd sync was called.
	bdSynced := false
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "bd" && call[1] == "sync" {
			bdSynced = true
		}
	}
	if !bdSynced {
		t.Errorf("bd sync not called; calls = %v", fake.calls)
	}
}

func TestStop_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath: pidFile,
		w:       &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "not running") {
		t.Errorf("expected 'not running' message, got %q", buf.String())
	}
}

func TestStop_Stale(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath: pidFile,
		w:       &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "stale") {
		t.Errorf("expected 'stale' message, got %q", buf.String())
	}

	// PID file should be removed.
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
}

func TestStop_UDSFailContinues(t *testing.T) {
	// When UDS connection fails, stop should still signal the process and continue.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	signaled := false

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"), // will fail to connect
		tmuxName: "oro",
		runner:   fake,
		w:        &buf,
		signalFn: func(pid int) error { signaled = true; return nil },
		aliveFn:  func(pid int) bool { return false },
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still signal and continue.
	if !signaled {
		t.Error("expected signalFn to be called even when UDS fails")
	}

	// Should still kill tmux.
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Error("tmux kill-session not called")
	}
}

// runMockStopDispatcher listens on a UDS, accepts one connection,
// reads a directive, records its op, and sends an ACK.
func runMockStopDispatcher(ctx context.Context, t *testing.T, sockPath string, ops chan<- string, ready chan<- struct{}) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock stop dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

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
			t.Error("mock stop dispatcher: no data")
			return
		}

		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Errorf("mock stop dispatcher unmarshal: %v", err)
			return
		}

		if msg.Directive != nil {
			ops <- msg.Directive.Op
		}

		ack := protocol.Message{
			Type: protocol.MsgACK,
			ACK:  &protocol.ACKPayload{OK: true, Detail: "stopping"},
		}
		data, _ := json.Marshal(ack)
		data = append(data, '\n')
		_, _ = conn.Write(data)

	case <-ctx.Done():
		return
	}
}
