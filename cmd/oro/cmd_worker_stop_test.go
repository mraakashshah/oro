package main

import (
	"bufio"
	"bytes"
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

// mockDispatcherForStop runs a UDS listener that handles directives for worker stop tests.
// It accepts a handler func per connection so tests can inject custom logic.
type mockDispatcherForStop struct {
	ln       net.Listener
	sockPath string
}

func newMockDispatcherForStop(t *testing.T) *mockDispatcherForStop {
	t.Helper()
	sockPath := fmt.Sprintf("/tmp/oro-ws-%d.sock", time.Now().UnixNano())
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	})
	return &mockDispatcherForStop{ln: ln, sockPath: sockPath}
}

// serveOne accepts one connection, calls handler, and closes.
func (m *mockDispatcherForStop) serveOne(t *testing.T, handler func(conn net.Conn)) {
	t.Helper()
	conn, err := m.ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	handler(conn)
}

// sendACK writes a success ACK with optional detail.
func sendACK(conn net.Conn, detail string) {
	ack := protocol.Message{
		Type: protocol.MsgACK,
		ACK:  &protocol.ACKPayload{OK: true, Detail: detail},
	}
	data, _ := json.Marshal(ack)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// sendErrorACK writes a failure ACK.
func sendErrorACK(conn net.Conn, detail string) {
	ack := protocol.Message{
		Type: protocol.MsgACK,
		ACK:  &protocol.ACKPayload{OK: false, Detail: detail},
	}
	data, _ := json.Marshal(ack)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// readDirective reads one DIRECTIVE message from conn and returns its payload.
func readDirective(t *testing.T, conn net.Conn) protocol.DirectivePayload {
	t.Helper()
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("failed to read directive from connection")
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal directive: %v", err)
	}
	if msg.Type != protocol.MsgDirective {
		t.Fatalf("expected DIRECTIVE message, got %s", msg.Type)
	}
	if msg.Directive == nil {
		t.Fatal("directive payload is nil")
	}
	return *msg.Directive
}

// buildStatusDetail builds a JSON status detail string with the given worker IDs.
func buildStatusDetail(workerIDs []string) string {
	type ws struct {
		ID string `json:"id"`
	}
	type sr struct {
		Workers []ws `json:"workers"`
	}
	workers := make([]ws, len(workerIDs))
	for i, id := range workerIDs {
		workers[i] = ws{ID: id}
	}
	data, _ := json.Marshal(sr{Workers: workers})
	return string(data)
}

// TestWorkerStopSendsKillDirective is the acceptance-criteria test for oro-18c5.7.
// It verifies that `oro worker stop <id>` sends kill-worker directive via UDS
// and prints confirmation. --all queries status then sends kill-worker for each worker ID.
func TestWorkerStopSendsKillDirective(t *testing.T) {
	t.Run("sends kill-worker directive for given worker ID", func(t *testing.T) {
		m := newMockDispatcherForStop(t)
		t.Setenv("ORO_SOCKET_PATH", m.sockPath)

		received := make(chan protocol.DirectivePayload, 1)
		go m.serveOne(t, func(conn net.Conn) {
			d := readDirective(t, conn)
			received <- d
			sendACK(conn, "worker w-01 killed")
		})

		waitForSocket(t, m.sockPath, 2*time.Second)

		var out bytes.Buffer
		err := runWorkerStop(&out, "w-01", false)
		if err != nil {
			t.Fatalf("runWorkerStop returned error: %v", err)
		}

		select {
		case got := <-received:
			if got.Op != string(protocol.DirectiveKillWorker) {
				t.Errorf("expected op=%q, got %q", protocol.DirectiveKillWorker, got.Op)
			}
			if got.Args != "w-01" {
				t.Errorf("expected args=%q, got %q", "w-01", got.Args)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for directive")
		}

		if !strings.Contains(out.String(), "w-01") {
			t.Errorf("expected output to mention worker ID w-01, got: %q", out.String())
		}
	})

	t.Run("--all queries status then sends kill-worker for each connected worker", func(t *testing.T) {
		m := newMockDispatcherForStop(t)
		t.Setenv("ORO_SOCKET_PATH", m.sockPath)

		workerIDs := []string{"w-01", "w-02", "w-03"}

		// The --all flow: first connection receives status, subsequent ones receive kill-worker.
		// Serve connections sequentially to guarantee ordering.
		directives := make(chan protocol.DirectivePayload, 10)
		go func() {
			// First connection: status query.
			conn1, err := m.ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer func() { _ = conn1.Close() }()
				d := readDirective(t, conn1)
				directives <- d
				sendACK(conn1, buildStatusDetail(workerIDs))
			}()

			// Subsequent connections: kill-worker for each worker.
			for range workerIDs {
				conn, err := m.ln.Accept()
				if err != nil {
					return
				}
				func() {
					defer func() { _ = conn.Close() }()
					d := readDirective(t, conn)
					directives <- d
					sendACK(conn, fmt.Sprintf("killed %s", d.Args))
				}()
			}
		}()

		waitForSocket(t, m.sockPath, 2*time.Second)

		var out bytes.Buffer
		err := runWorkerStop(&out, "", true)
		if err != nil {
			t.Fatalf("runWorkerStop --all returned error: %v", err)
		}

		// Collect all directives (status + one per worker).
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		var got []protocol.DirectivePayload
		for len(got) < len(workerIDs)+1 {
			select {
			case d := <-directives:
				got = append(got, d)
			case <-ctx.Done():
				t.Fatalf("timeout: received %d directives, expected %d", len(got), len(workerIDs)+1)
			}
		}

		// First directive must be status.
		if got[0].Op != string(protocol.DirectiveStatus) {
			t.Errorf("first directive: expected %q, got %q", protocol.DirectiveStatus, got[0].Op)
		}

		// Remaining directives must be kill-worker for each worker ID.
		killedIDs := make(map[string]bool)
		for _, d := range got[1:] {
			if d.Op != string(protocol.DirectiveKillWorker) {
				t.Errorf("expected kill-worker directive, got %q", d.Op)
			}
			killedIDs[d.Args] = true
		}
		for _, id := range workerIDs {
			if !killedIDs[id] {
				t.Errorf("expected kill-worker for %q, but not found in directives", id)
			}
		}

		// Output must mention all killed workers.
		outStr := out.String()
		for _, id := range workerIDs {
			if !strings.Contains(outStr, id) {
				t.Errorf("expected output to mention worker %q, got: %q", id, outStr)
			}
		}
	})

	t.Run("--all with no workers connected prints message and returns nil", func(t *testing.T) {
		m := newMockDispatcherForStop(t)
		t.Setenv("ORO_SOCKET_PATH", m.sockPath)

		go m.serveOne(t, func(conn net.Conn) {
			d := readDirective(t, conn)
			if d.Op != string(protocol.DirectiveStatus) {
				t.Errorf("expected status directive, got %q", d.Op)
			}
			// Return status with empty workers list.
			sendACK(conn, buildStatusDetail(nil))
		})

		waitForSocket(t, m.sockPath, 2*time.Second)

		var out bytes.Buffer
		err := runWorkerStop(&out, "", true)
		if err != nil {
			t.Fatalf("runWorkerStop --all with no workers returned error: %v", err)
		}

		outStr := out.String()
		if !strings.Contains(strings.ToLower(outStr), "no workers") {
			t.Errorf("expected 'no workers' message, got: %q", outStr)
		}
	})

	t.Run("dispatcher not running (socket missing) returns error", func(t *testing.T) {
		sockPath := fmt.Sprintf("/tmp/oro-ws-missing-%d.sock", time.Now().UnixNano())
		t.Setenv("ORO_SOCKET_PATH", sockPath)

		var out bytes.Buffer
		err := runWorkerStop(&out, "w-01", false)
		if err == nil {
			t.Fatal("expected error when socket is missing")
		}
		if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "no such file") {
			t.Errorf("expected connection error, got: %v", err)
		}
	})

	t.Run("worker ID not found returns error from dispatcher", func(t *testing.T) {
		m := newMockDispatcherForStop(t)
		t.Setenv("ORO_SOCKET_PATH", m.sockPath)

		go m.serveOne(t, func(conn net.Conn) {
			readDirective(t, conn)
			sendErrorACK(conn, "worker w-99 not found")
		})

		waitForSocket(t, m.sockPath, 2*time.Second)

		var out bytes.Buffer
		err := runWorkerStop(&out, "w-99", false)
		if err == nil {
			t.Fatal("expected error when worker not found")
		}
		if !strings.Contains(err.Error(), "w-99") && !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected error mentioning worker or not found, got: %v", err)
		}
	})
}

// TestWorkerStopCmdStructure verifies the cobra command hierarchy for worker stop.
func TestWorkerStopCmdStructure(t *testing.T) {
	cmd := newWorkerCmd()

	stopCmd := findSubcmd(cmd.Commands(), "stop")
	if stopCmd == nil {
		t.Fatal("expected 'stop' subcommand under 'worker'")
	}

	assertFlagExists(t, stopCmd, "all")
}

// TestWorkerStopRegisteredInRoot verifies worker stop subcommand is reachable from root.
func TestWorkerStopRegisteredInRoot(t *testing.T) {
	root := newRootCmd()

	workerCmd := findSubcmd(root.Commands(), "worker")
	if workerCmd == nil {
		t.Fatal("expected 'worker' subcommand in root")
	}

	if findSubcmd(workerCmd.Commands(), "stop") == nil {
		t.Error("expected 'stop' subcommand under 'worker'")
	}
}
