package dispatcher //nolint:testpackage // white-box mutation coverage for connection lifecycle routing

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestHandleConnLifecycleMatrix(t *testing.T) {
	t.Run("deferred cleanup closes the served connection", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		conn := newStartupRecoveryScriptedConn(t, protocol.Message{
			Type:      protocol.MsgDirective,
			Directive: &protocol.DirectivePayload{Op: "not-a-directive"},
		})

		invokeHandleConnBounded(t, d, context.Background(), conn)

		if got := conn.closeCount(); got != 1 {
			t.Fatalf("connection Close calls = %d, want exactly 1", got)
		}
		response := conn.singleWrittenMessage(t)
		if response.Type != protocol.MsgACK || response.ACK == nil || response.ACK.OK {
			t.Fatalf("directive response = %#v, want negative ACK", response)
		}
	})

	t.Run("legacy active idle worker is shut down after dispatch", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "drained-idle-worker"
			beadID   = "drained-idle-bead"
		)
		insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
		conn := newStartupRecoveryScriptedConn(t, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID: workerID,
				BeadID:   beadID,
			},
		})

		invokeHandleConnBounded(t, d, context.Background(), conn)

		response := conn.singleWrittenMessage(t)
		if response.Type != protocol.MsgShutdown {
			t.Fatalf("drained idle response = %s, want %s", response.Type, protocol.MsgShutdown)
		}
		assertStartupRecoveryEventCount(t, d, "worker_protocol_drain_started", beadID, 1)
		if got := conn.closeCount(); got != 1 {
			t.Fatalf("drained connection Close calls = %d, want exactly 1", got)
		}
	})

	t.Run("failed registration returns before buffered follow-up", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "registration-fenced-worker"
		originalConn := newMockConn()
		original := &trackedWorker{
			id: workerID, conn: originalConn, state: protocol.WorkerBusy, reviewReleaseToken: 41,
		}
		d.mu.Lock()
		d.workers[workerID] = original
		d.mu.Unlock()
		conn := newStartupRecoveryScriptedConn(t,
			canonicalHeartbeatMessage(workerID, 7),
			protocol.Message{
				Type: protocol.MsgDirective,
				Directive: &protocol.DirectivePayload{
					Op: string(protocol.DirectivePause), Source: "buffered-follow-up", Reason: "must-not-run",
				},
			},
		)

		invokeHandleConnBounded(t, d, context.Background(), conn)

		if state := d.GetState(); state != StateInert {
			t.Fatalf("dispatcher state = %s, want %s; buffered follow-up ran after failed registration", state, StateInert)
		}
		assertStartupRecoveryEventCount(t, d, "directive", "", 0)
		d.mu.Lock()
		got := d.workers[workerID]
		d.mu.Unlock()
		if got != original || got.conn != originalConn || got.reviewReleaseToken != 41 {
			t.Fatalf("failed registration changed fenced worker: %#v", got)
		}
		if got := conn.closeCount(); got != 2 {
			t.Fatalf("failed-registration connection Close calls = %d, want 2", got)
		}
	})

	t.Run("canceled context rejects the first decoded message", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client, done := startHandleConnMutationTest(t, d, ctx)
		encodeHandleConnMessage(t, client, canonicalHeartbeatMessage("canceled-conn-worker", 19))
		waitHandleConnDone(t, done)
		if got := d.ConnectedWorkers(); got != 0 {
			t.Fatalf("connected workers = %d, want 0 after canceled admission", got)
		}
	})

	t.Run("directive receives ack closes and never registers worker", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		client, done := startHandleConnMutationTest(t, d, context.Background())
		encodeHandleConnMessage(t, client, protocol.Message{
			Type:      protocol.MsgDirective,
			Directive: &protocol.DirectivePayload{Op: "not-a-directive"},
		})
		response := decodeHandleConnMessage(t, client)
		if response.Type != protocol.MsgACK || response.ACK == nil || response.ACK.OK {
			t.Fatalf("directive response = %#v, want negative ACK", response)
		}
		waitHandleConnDone(t, done)
		if err := client.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err == nil {
			if err := json.NewEncoder(client).Encode(canonicalHeartbeatMessage("must-not-register", 1)); err == nil {
				t.Fatal("directive connection remained writable after handleConn returned")
			}
		}
	})

	t.Run("rerank request receives response and closes", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.rerankerFactory = func(string) (Reranker, error) {
			return &fakeReranker{scores: []float64{0.75}}, nil
		}
		client, done := startHandleConnMutationTest(t, d, context.Background())
		encodeHandleConnMessage(t, client, protocol.Message{
			Type: protocol.MsgRerankByIDsRequest,
			RerankReq: &protocol.RerankByIDsRequest{
				Query: "connection routing",
			},
		})
		response := decodeHandleConnMessage(t, client)
		if response.Type != protocol.MsgRerankByIDsResponse || response.RerankResp == nil ||
			len(response.RerankResp.Scores) != 1 || response.RerankResp.Scores[0] != 0.75 {
			t.Fatalf("rerank response = %#v, want score 0.75", response)
		}
		waitHandleConnDone(t, done)
	})

	t.Run("work request receives response and closes", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		client, done := startHandleConnMutationTest(t, d, context.Background())
		encodeHandleConnMessage(t, client, protocol.Message{
			Type:            protocol.MsgEvidenceRequest,
			EvidenceRequest: &protocol.EvidenceRequest{},
		})
		response := decodeHandleConnMessage(t, client)
		if response.Type != protocol.MsgEvidenceResponse || response.EvidenceResponse == nil || response.EvidenceResponse.Error == "" {
			t.Fatalf("work response = %#v, want typed error response", response)
		}
		waitHandleConnDone(t, done)
	})

	t.Run("rejected legacy admission sends shutdown and returns", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		client, done := startHandleConnMutationTest(t, d, context.Background())
		encodeHandleConnMessage(t, client, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "legacy-rejected-worker"},
		})
		response := decodeHandleConnMessage(t, client)
		if response.Type != protocol.MsgShutdown {
			t.Fatalf("legacy response = %s, want %s", response.Type, protocol.MsgShutdown)
		}
		waitHandleConnDone(t, done)
		if got := d.ConnectedWorkers(); got != 0 {
			t.Fatalf("connected legacy workers = %d, want 0", got)
		}
	})

	t.Run("scanner accepts messages above default buffer", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		client, done := startHandleConnMutationTest(t, d, context.Background())
		const workerID = "large-message-worker"
		encodeHandleConnMessage(t, client, canonicalHeartbeatMessage(workerID, 4))
		waitFor(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.workers[workerID] != nil
		}, time.Second)
		encodeHandleConnMessage(t, client, protocol.Message{
			Type: protocol.MsgStatus,
			Status: &protocol.StatusPayload{
				WorkerID: workerID, BeadID: "large-message-bead", State: "qg_retry_received",
				Result: strings.Repeat("x", 70*1024),
			},
		})
		waitFor(t, func() bool {
			return startupRecoveryEventCount(d, "qg_retry_received", "large-message-bead") == 1
		}, time.Second)
		_ = client.Close()
		waitHandleConnDone(t, done)
	})

	t.Run("eof closes connection and runs ownership cleanup", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		client, done := startHandleConnMutationTest(t, d, context.Background())
		const (
			workerID = "eof-cleanup-worker"
			beadID   = "eof-cleanup-bead"
		)
		encodeHandleConnMessage(t, client, canonicalHeartbeatMessage(workerID, 6))
		waitFor(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.workers[workerID] != nil
		}, time.Second)
		d.mu.Lock()
		worker := d.workers[workerID]
		worker.state = protocol.WorkerBusy
		worker.beadID = beadID
		d.attemptCounts[beadID] = 1
		d.assigningBeads[beadID] = true
		d.mu.Unlock()

		_ = client.Close()
		waitHandleConnDone(t, done)
		assertConnCloseWorkerAndTrackingGone(t, d, workerID, beadID)
		waitFor(t, func() bool {
			beads.mu.Lock()
			defer beads.mu.Unlock()
			return beads.updated[beadID] == "open"
		}, time.Second)
	})
}

type startupRecoveryScriptedConn struct {
	mu         sync.Mutex
	input      *bytes.Reader
	output     bytes.Buffer
	closeCalls int
}

func newStartupRecoveryScriptedConn(t *testing.T, messages ...protocol.Message) *startupRecoveryScriptedConn {
	t.Helper()
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, msg := range messages {
		if err := encoder.Encode(msg); err != nil {
			t.Fatalf("encode scripted message: %v", err)
		}
	}
	return &startupRecoveryScriptedConn{input: bytes.NewReader(input.Bytes())}
}

func (c *startupRecoveryScriptedConn) Read(p []byte) (int, error) {
	return c.input.Read(p)
}

func (c *startupRecoveryScriptedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.Write(p)
}

func (c *startupRecoveryScriptedConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCalls++
	return nil
}

func (c *startupRecoveryScriptedConn) LocalAddr() net.Addr              { return startupRecoveryTestAddr("local") }
func (c *startupRecoveryScriptedConn) RemoteAddr() net.Addr             { return startupRecoveryTestAddr("remote") }
func (c *startupRecoveryScriptedConn) SetDeadline(time.Time) error      { return nil }
func (c *startupRecoveryScriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *startupRecoveryScriptedConn) SetWriteDeadline(time.Time) error { return nil }

func (c *startupRecoveryScriptedConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCalls
}

func (c *startupRecoveryScriptedConn) singleWrittenMessage(t *testing.T) protocol.Message {
	t.Helper()
	c.mu.Lock()
	data := append([]byte(nil), c.output.Bytes()...)
	c.mu.Unlock()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var msg protocol.Message
	if err := decoder.Decode(&msg); err != nil {
		t.Fatalf("decode scripted response: %v", err)
	}
	var extra protocol.Message
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("unexpected second scripted response: %#v", extra)
	}
	return msg
}

type startupRecoveryTestAddr string

func (a startupRecoveryTestAddr) Network() string { return string(a) }
func (a startupRecoveryTestAddr) String() string  { return string(a) }

func invokeHandleConnBounded(t *testing.T, d *Dispatcher, ctx context.Context, conn net.Conn) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		d.handleConn(ctx, conn)
		close(done)
	}()
	waitHandleConnDone(t, done)
}

func startHandleConnMutationTest(t *testing.T, d *Dispatcher, ctx context.Context) (net.Conn, <-chan struct{}) {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		d.handleConn(ctx, server)
		close(done)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, done
}

func canonicalHeartbeatMessage(workerID string, contextPct int) protocol.Message {
	return protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID: workerID, ContextPct: contextPct,
			ProtocolVersion: protocol.WorkerProtocolVersion,
			Capabilities:    []string{protocol.CapabilityReadyEvidenceV1},
		},
	}
}

func encodeHandleConnMessage(t *testing.T, conn net.Conn, msg protocol.Message) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(msg); err != nil {
		t.Fatalf("encode handleConn message: %v", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
}

func decodeHandleConnMessage(t *testing.T, conn net.Conn) protocol.Message {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var msg protocol.Message
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		t.Fatalf("decode handleConn response: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return msg
}

func waitHandleConnDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleConn did not finish")
	}
}

func startupRecoveryEventCount(d *Dispatcher, eventType, beadID string) int {
	var count int
	_ = d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&count)
	return count
}

func TestConnCloseCleanupAdmissionAndEffects(t *testing.T) {
	t.Run("empty worker identity is ignored", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		conn := newMockConn()
		worker := &trackedWorker{id: "", conn: conn, state: protocol.WorkerBusy, beadID: "empty-id-bead"}
		d.mu.Lock()
		d.workers[""] = worker
		d.mu.Unlock()
		drainStartupRecoveryWake(d)

		invokeConnCloseCleanup(t, d, "", conn)

		d.mu.Lock()
		got := d.workers[""]
		d.mu.Unlock()
		if got != worker {
			t.Fatalf("empty-ID cleanup changed worker: got %p, want %p", got, worker)
		}
		assertStartupRecoveryWake(t, d, false)
	})

	t.Run("stale connection preserves replacement without wake", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		staleConn, replacementConn := newMockConn(), newMockConn()
		worker := &trackedWorker{
			id: "stale-worker", conn: replacementConn, state: protocol.WorkerBusy, beadID: "stale-bead",
		}
		d.mu.Lock()
		d.workers[worker.id] = worker
		d.mu.Unlock()
		drainStartupRecoveryWake(d)

		invokeConnCloseCleanup(t, d, worker.id, staleConn)

		d.mu.Lock()
		got := d.workers[worker.id]
		d.mu.Unlock()
		if got != worker || got.conn != replacementConn {
			t.Fatalf("stale cleanup changed replacement: got worker/conn %p/%p, want %p/%p",
				got, got.conn, worker, replacementConn)
		}
		assertStartupRecoveryWake(t, d, false)
	})

	t.Run("stopping spawn-for remains tracked and wakes assignment", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		conn := newMockConn()
		now := time.Date(2032, 3, 4, 5, 6, 7, 0, time.UTC)
		d.nowFunc = func() time.Time { return now }
		worker := &trackedWorker{
			id: "stopping-spawn-worker", conn: conn, state: protocol.WorkerShuttingDown, spawnFor: true,
		}
		d.mu.Lock()
		d.workers[worker.id] = worker
		d.mu.Unlock()
		drainStartupRecoveryWake(d)

		invokeConnCloseCleanup(t, d, worker.id, conn)

		d.mu.Lock()
		got := d.workers[worker.id]
		lastSeen := got.lastSeen
		d.mu.Unlock()
		if got != worker || !lastSeen.Equal(now) {
			t.Fatalf("spawn-for cleanup = worker %p lastSeen %s, want %p/%s", got, lastSeen, worker, now)
		}
		assertStartupRecoveryWake(t, d, true)
	})

	t.Run("ordinary disconnect clears tracking reopens bead and wakes", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "ordinary-disconnect-worker"
			beadID   = "ordinary-disconnect-bead"
		)
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: conn, state: protocol.WorkerBusy, beadID: beadID,
		}
		d.attemptCounts[beadID] = 1
		d.rejectionCounts[beadID] = 1
		d.assigningBeads[beadID] = true
		d.mu.Unlock()
		drainStartupRecoveryWake(d)

		invokeConnCloseCleanup(t, d, workerID, conn)

		assertConnCloseWorkerAndTrackingGone(t, d, workerID, beadID)
		waitFor(t, func() bool {
			beads.mu.Lock()
			defer beads.mu.Unlock()
			return beads.updated[beadID] == "open"
		}, time.Second)
		assertStartupRecoveryWake(t, d, true)
	})

	t.Run("preserved work quarantines and blocks before wake", func(t *testing.T) {
		d, beads, worktrees, _, _, _ := newTestDispatcher(t)
		const (
			workerID   = "preserved-disconnect-worker"
			beadID     = "preserved-disconnect-bead"
			worktree   = "/tmp/preserved-disconnect-worktree"
			baseBranch = "main"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.mu.Unlock()
		worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
		d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 4 && args[2] == "status" {
				return nil, nil
			}
			if len(args) == 5 && args[2] == "rev-list" {
				return []byte("1\n"), nil
			}
			t.Fatalf("unexpected git arguments: %q", args)
			return nil, nil
		}}
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: conn, state: protocol.WorkerBusy, beadID: beadID,
			assignmentID: assignmentID, worktree: worktree, baseBranch: baseBranch,
		}
		d.attemptCounts[beadID] = 1
		d.assigningBeads[beadID] = true
		d.mu.Unlock()
		drainStartupRecoveryWake(d)

		invokeConnCloseCleanup(t, d, workerID, conn)

		assertConnCloseWorkerAndTrackingGone(t, d, workerID, beadID)
		var assignmentStatus string
		if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
			t.Fatalf("read assignment status: %v", err)
		}
		if assignmentStatus != "quarantined" {
			t.Fatalf("assignment status = %q, want quarantined", assignmentStatus)
		}
		beads.mu.Lock()
		beadStatus := beads.updated[beadID]
		beads.mu.Unlock()
		if beadStatus != "blocked" {
			t.Fatalf("bead status = %q, want blocked", beadStatus)
		}
		assertStartupRecoveryWake(t, d, true)
	})

	t.Run("preempted disconnect terminalizes before reopening", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "preempted-disconnect-worker"
			beadID   = "preempted-disconnect-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.mu.Unlock()
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: conn, state: protocol.WorkerPreempting, beadID: beadID,
			assignmentID: assignmentID, worktree: t.TempDir(),
		}
		d.attemptCounts[beadID] = 1
		d.mu.Unlock()
		drainStartupRecoveryWake(d)

		invokeConnCloseCleanup(t, d, workerID, conn)

		assertConnCloseWorkerAndTrackingGone(t, d, workerID, beadID)
		var assignmentStatus string
		if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
			t.Fatalf("read assignment status: %v", err)
		}
		if assignmentStatus != "completed" {
			t.Fatalf("assignment status = %q, want completed", assignmentStatus)
		}
		beads.mu.Lock()
		beadStatus := beads.updated[beadID]
		beads.mu.Unlock()
		if beadStatus != "open" {
			t.Fatalf("bead status = %q, want open", beadStatus)
		}
		assertStartupRecoveryWake(t, d, true)
	})
}

func invokeConnCloseCleanup(t *testing.T, d *Dispatcher, workerID string, conn net.Conn) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		d.connCloseCleanup(workerID, conn)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connCloseCleanup did not finish")
	}
}

func drainStartupRecoveryWake(d *Dispatcher) {
	select {
	case <-d.workerReadyCh:
	default:
	}
}

func assertStartupRecoveryWake(t *testing.T, d *Dispatcher, want bool) {
	t.Helper()
	select {
	case <-d.workerReadyCh:
		if !want {
			t.Fatal("unexpected assignment-loop wake")
		}
	case <-time.After(25 * time.Millisecond):
		if want {
			t.Fatal("assignment-loop wake was not emitted")
		}
	}
}

func assertConnCloseWorkerAndTrackingGone(t *testing.T, d *Dispatcher, workerID, beadID string) {
	t.Helper()
	d.mu.Lock()
	_, workerExists := d.workers[workerID]
	_, attemptExists := d.attemptCounts[beadID]
	_, rejectionExists := d.rejectionCounts[beadID]
	_, assigningExists := d.assigningBeads[beadID]
	d.mu.Unlock()
	if workerExists || attemptExists || rejectionExists || assigningExists {
		t.Fatalf("cleanup residue: worker=%t attempt=%t rejection=%t assigning=%t",
			workerExists, attemptExists, rejectionExists, assigningExists)
	}
}

func TestHandleMessageUncheckedRoutingMatrix(t *testing.T) {
	t.Run("heartbeat", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "route-heartbeat-worker"
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{id: workerID, state: protocol.WorkerBusy}
		d.mu.Unlock()

		d.handleMessageUnchecked(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID: workerID, BeadID: "route-heartbeat-bead", ContextPct: 37,
			},
		})

		d.mu.Lock()
		got := d.workers[workerID].contextPct
		d.mu.Unlock()
		if got != 37 {
			t.Fatalf("heartbeat context percentage = %d, want 37", got)
		}
	})

	t.Run("status", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "route-status-worker"
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{id: workerID, state: protocol.WorkerBusy}
		d.mu.Unlock()

		d.handleMessageUnchecked(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgStatus,
			Status: &protocol.StatusPayload{
				WorkerID: workerID, BeadID: "route-status-bead", State: "qg_retry_received", Result: "received",
			},
		})
		assertStartupRecoveryEventCount(t, d, "qg_retry_received", "route-status-bead", 1)
	})

	t.Run("done", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.handleMessageUnchecked(context.Background(), "route-done-worker", protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				WorkerID: "route-done-worker", BeadID: "route-done-bead", QualityGatePassed: true,
			},
		})
		assertStartupRecoveryEventCount(t, d, "done", "route-done-bead", 1)
	})

	t.Run("handoff", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.handleMessageUnchecked(context.Background(), "route-handoff-worker", protocol.Message{
			Type: protocol.MsgHandoff,
			Handoff: &protocol.HandoffPayload{
				WorkerID: "route-handoff-worker", BeadID: "route-handoff-bead",
			},
		})
		assertStartupRecoveryEventCount(t, d, "handoff", "route-handoff-bead", 1)
	})

	t.Run("ready for review", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "route-review-worker"
			beadID   = "route-review-bead"
		)
		worktree := t.TempDir()
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.mu.Unlock()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, state: protocol.WorkerBusy, beadID: beadID,
			assignmentID: assignmentID, worktree: worktree, targetBranch: "main",
		}
		d.mu.Unlock()

		d.handleMessageUnchecked(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				WorkerID: workerID, BeadID: beadID,
			},
		})
		assertStartupRecoveryEventCount(t, d, "ready_for_review", beadID, 1)
	})

	t.Run("reconnect", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "route-reconnect-worker"
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{id: workerID, state: protocol.WorkerBusy, beadID: "stale-bead"}
		d.mu.Unlock()

		d.handleMessageUnchecked(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgReconnect,
			Reconnect: &protocol.ReconnectPayload{
				WorkerID: workerID, State: "idle",
			},
		})

		d.mu.Lock()
		state, beadID := d.workers[workerID].state, d.workers[workerID].beadID
		d.mu.Unlock()
		if state != protocol.WorkerIdle || beadID != "" {
			t.Fatalf("reconnected worker = (%s, %q), want (%s, empty)", state, beadID, protocol.WorkerIdle)
		}
		assertStartupRecoveryEventCount(t, d, "reconnect", "", 1)
	})

	t.Run("shutdown approved", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "route-shutdown-worker"
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: conn, encoder: json.NewEncoder(conn), state: protocol.WorkerIdle,
		}
		d.mu.Unlock()

		d.handleMessageUnchecked(context.Background(), workerID, protocol.Message{
			Type:             protocol.MsgShutdownApproved,
			ShutdownApproved: &protocol.ShutdownApprovedPayload{WorkerID: workerID},
		})

		d.mu.Lock()
		approved, state := d.workers[workerID].shutdownApproved, d.workers[workerID].state
		d.mu.Unlock()
		if !approved || state != protocol.WorkerShuttingDown {
			t.Fatalf("shutdown approval = (%t, %s), want (true, %s)", approved, state, protocol.WorkerShuttingDown)
		}
		assertStartupRecoveryEventCount(t, d, "shutdown_approved", "", 1)
	})

	t.Run("checkpoint ack", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.handleMessageUnchecked(context.Background(), "route-checkpoint-worker", protocol.Message{
			Type: protocol.MsgCheckpointAck,
			CheckpointAck: &protocol.CheckpointAckPayload{
				BeadID: "route-checkpoint-bead", CheckpointID: "stale-checkpoint",
			},
		})
		assertStartupRecoveryEventCount(t, d, "note", "route-checkpoint-bead", 1)
	})

	t.Run("capability refresh ack", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.nowFunc = func() time.Time { return now }
		assignmentID, err := d.createAssignment(ctx, "route-capability-bead", "route-capability-worker", t.TempDir())
		if err != nil {
			t.Fatalf("create assignment: %v", err)
		}
		predecessor, err := d.issueAssignmentCapability(ctx, assignmentID, 1, ActorRoleExecutionWorker)
		if err != nil {
			t.Fatalf("issue predecessor: %v", err)
		}
		replacement, err := d.issueAssignmentCapabilityWithState(ctx, assignmentID, 1, ActorRoleExecutionWorker, "pending")
		if err != nil {
			t.Fatalf("issue replacement: %v", err)
		}
		if _, err := d.db.ExecContext(ctx,
			`UPDATE assignment_capabilities SET pending_replacement_id=? WHERE capability_id=?`,
			replacement.ID, predecessor.ID); err != nil {
			t.Fatalf("link replacement: %v", err)
		}
		d.mu.Lock()
		d.workers["route-capability-worker"] = &trackedWorker{
			id: "route-capability-worker", state: protocol.WorkerBusy, assignmentID: assignmentID,
		}
		d.mu.Unlock()

		d.handleMessageUnchecked(ctx, "route-capability-worker", protocol.Message{
			Type: protocol.MsgCapabilityRefreshACK,
			CapabilityRefreshACK: &protocol.CapabilityRefreshACKPayload{
				AssignmentID: assignmentID, CapabilityID: replacement.ID,
			},
		})

		var active, revoked int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignment_capabilities WHERE assignment_id=? AND state='active'`, assignmentID).Scan(&active); err != nil {
			t.Fatalf("count active capabilities: %v", err)
		}
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignment_capabilities WHERE assignment_id=? AND state='revoked'`, assignmentID).Scan(&revoked); err != nil {
			t.Fatalf("count revoked capabilities: %v", err)
		}
		if active != 1 || revoked != 1 {
			t.Fatalf("capability states = active:%d revoked:%d, want 1/1", active, revoked)
		}
	})

	t.Run("invalid bead id logs and stops routing", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "route-invalid-worker"
		before := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		d.nowFunc = func() time.Time { return before.Add(time.Hour) }
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{id: workerID, state: protocol.WorkerBusy, lastProgress: before}
		d.mu.Unlock()

		d.handleMessageUnchecked(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgStatus,
			Status: &protocol.StatusPayload{
				WorkerID: workerID, BeadID: "../invalid", State: "qg_retry_received",
			},
		})

		assertStartupRecoveryEventCount(t, d, "invalid_bead_id", "../invalid", 1)
		assertStartupRecoveryEventCount(t, d, "qg_retry_received", "../invalid", 0)
		d.mu.Lock()
		lastProgress := d.workers[workerID].lastProgress
		d.mu.Unlock()
		if !lastProgress.Equal(before) {
			t.Fatalf("invalid message updated progress to %s, want %s", lastProgress, before)
		}
	})
}

func assertStartupRecoveryEventCount(t *testing.T, d *Dispatcher, eventType, beadID string, want int) {
	t.Helper()
	var got int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&got); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	if got != want {
		t.Fatalf("%s event count = %d, want %d", eventType, got, want)
	}
}
